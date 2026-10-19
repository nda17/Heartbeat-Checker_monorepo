package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	monitors "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

var (
	ErrInvalid  = errors.New("invalid webhook")
	ErrNotFound = errors.New("webhook or monitor not found")
	ErrConflict = errors.New("webhook version conflict")
)

type Service struct {
	pool            *pgxpool.Pool
	box             cipherBox
	owners          monitors.OwnershipGuard
	accounts        accounts.ActiveGuard
	claimMu         sync.Mutex
	claimAfter      time.Time
	claimAfterID    string
	claimFence      time.Time
	claimScanActive bool
}

func New(pool *pgxpool.Pool, encodedKey string, owners monitors.OwnershipGuard, accounts accounts.ActiveGuard) (*Service, error) {
	box, err := newCipherBox(encodedKey)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, box: box, owners: owners, accounts: accounts, claimAfter: time.Unix(0, 0).UTC(), claimAfterID: "00000000-0000-0000-0000-000000000000"}, nil
}

func cleanURL(raw string) (string, string, error) {
	if raw != strings.TrimSpace(raw) || len(raw) < 12 || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\t\\") {
		return "", "", ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Port() != "" && u.Port() != "443" {
		return "", "", ErrInvalid
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if len(host) > 253 || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.ContainsAny(host, " %\t\r\n") {
		return "", "", ErrInvalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Is6() && ip.Is4In6() {
			return "", "", ErrInvalid
		}
	} else {
		if strings.Contains(host, ":") || !strings.Contains(host, ".") {
			return "", "", ErrInvalid
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", "", ErrInvalid
			}
			for _, c := range label {
				if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
					return "", "", ErrInvalid
				}
			}
		}
	}
	u.Host = net.JoinHostPort(host, "443")
	u.Fragment = ""
	return u.String(), host, nil
}

func (s *Service) begin(ctx context.Context, owner, monitorID string) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	active, err := s.accounts.Active(ctx, tx, owner)
	if err != nil || !active {
		tx.Rollback(ctx)
		if err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	if err := s.owners.LockOwned(ctx, tx, owner, monitorID); err != nil {
		tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return tx, nil
}

func (s *Service) Get(ctx context.Context, owner, monitorID string) (contracts.Webhook, error) {
	tx, err := s.begin(ctx, owner, monitorID)
	if err != nil {
		return contracts.Webhook{}, err
	}
	defer tx.Rollback(ctx)
	w, err := s.read(ctx, tx, owner, monitorID, false)
	if err != nil {
		return w, err
	}
	return w, tx.Commit(ctx)
}

func (s *Service) read(ctx context.Context, tx pgx.Tx, owner, monitorID string, lock bool) (contracts.Webhook, error) {
	var w contracts.Webhook
	var encrypted []byte
	query := `SELECT id::text,monitor_id::text,enabled,version,encrypted_url,created_at,updated_at FROM notifications.webhooks WHERE account_id=$1 AND monitor_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, query, owner, monitorID).Scan(&w.ID, &w.MonitorID, &w.Enabled, &w.Version, &encrypted, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	w.URL, err = s.box.open(owner, w.ID, "url", encrypted)
	return w, err
}

func (s *Service) Put(ctx context.Context, owner, monitorID string, in contracts.PutWebhook) (contracts.Webhook, string, error) {
	urlValue, host, err := cleanURL(in.URL)
	if err != nil || in.Version < 0 {
		return contracts.Webhook{}, "", ErrInvalid
	}
	tx, err := s.begin(ctx, owner, monitorID)
	if err != nil {
		return contracts.Webhook{}, "", err
	}
	defer tx.Rollback(ctx)
	current, err := s.read(ctx, tx, owner, monitorID, true)
	if errors.Is(err, ErrNotFound) {
		if in.Version != 0 || in.ID != "" {
			return contracts.Webhook{}, "", ErrConflict
		}
		id, err := outbox.ID()
		if err != nil {
			return contracts.Webhook{}, "", err
		}
		secret, err := newSecret()
		if err != nil {
			return contracts.Webhook{}, "", err
		}
		urlCipher, err := s.box.seal(owner, id, "url", urlValue)
		if err != nil {
			return contracts.Webhook{}, "", err
		}
		secretCipher, err := s.box.seal(owner, id, "secret", secret)
		if err != nil {
			return contracts.Webhook{}, "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications.webhooks(id,account_id,monitor_id,enabled,encrypted_url,encrypted_secret,normalized_host) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, owner, monitorID, in.Enabled, urlCipher, secretCipher, host)
		if err != nil {
			return contracts.Webhook{}, "", err
		}
		current, err = s.read(ctx, tx, owner, monitorID, false)
		if err != nil {
			return contracts.Webhook{}, "", err
		}
		return current, secret, tx.Commit(ctx)
	}
	if err != nil {
		return contracts.Webhook{}, "", err
	}
	if in.Version != current.Version || in.ID != "" && in.ID != current.ID {
		return contracts.Webhook{}, "", ErrConflict
	}
	if urlValue == current.URL && in.Enabled == current.Enabled && !in.RotateSecret {
		return current, "", tx.Commit(ctx)
	}
	urlCipher, err := s.box.seal(owner, current.ID, "url", urlValue)
	if err != nil {
		return contracts.Webhook{}, "", err
	}
	var secret string
	var secretCipher []byte
	if urlValue != current.URL || in.RotateSecret {
		secret, err = newSecret()
		if err != nil {
			return contracts.Webhook{}, "", err
		}
		secretCipher, err = s.box.seal(owner, current.ID, "secret", secret)
		if err != nil {
			return contracts.Webhook{}, "", err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.webhooks SET enabled=$3,encrypted_url=$4,encrypted_secret=COALESCE($5,encrypted_secret),normalized_host=$6,version=version+1,updated_at=now() WHERE account_id=$1 AND monitor_id=$2`, owner, monitorID, in.Enabled, urlCipher, secretCipher, host)
	if err != nil {
		return contracts.Webhook{}, "", err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='cancelled',lease_until=NULL,attempt_token=NULL,completed_at=now(),last_error_code='endpoint_changed' WHERE endpoint_id=$1 AND status IN ('pending','leased')`, current.ID)
	if err != nil {
		return contracts.Webhook{}, "", err
	}
	current, err = s.read(ctx, tx, owner, monitorID, false)
	if err != nil {
		return contracts.Webhook{}, "", err
	}
	return current, secret, tx.Commit(ctx)
}

func (s *Service) Delete(ctx context.Context, owner, monitorID string, in contracts.DeleteWebhook) error {
	if in.ID == "" || in.Version < 1 {
		return ErrInvalid
	}
	tx, err := s.begin(ctx, owner, monitorID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := s.read(ctx, tx, owner, monitorID, true)
	if err != nil {
		return err
	}
	if in.ID != current.ID || in.Version != current.Version {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `DELETE FROM notifications.webhooks WHERE account_id=$1 AND monitor_id=$2`, owner, monitorID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) List(ctx context.Context, owner, monitorID string, limit int, before time.Time) ([]contracts.Delivery, error) {
	if limit < 1 || limit > 100 || before.IsZero() {
		return nil, ErrInvalid
	}
	tx, err := s.begin(ctx, owner, monitorID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text,event_id::text,body,status,attempts,created_at,completed_at,last_http_status,last_error_code FROM notifications.deliveries WHERE account_id=$1 AND monitor_id=$2 AND created_at<$3 ORDER BY created_at DESC,id DESC LIMIT $4`, owner, monitorID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []contracts.Delivery{}
	for rows.Next() {
		var d contracts.Delivery
		var body []byte
		if err = rows.Scan(&d.ID, &d.EventID, &body, &d.Status, &d.Attempts, &d.CreatedAt, &d.CompletedAt, &d.LastHTTPStatus, &d.LastErrorCode); err != nil {
			return nil, err
		}
		var payload struct {
			Type       string    `json:"type"`
			OccurredAt time.Time `json:"occurredAt"`
		}
		if err = json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		d.Type, d.OccurredAt = payload.Type, payload.OccurredAt
		items = append(items, d)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return items, tx.Commit(ctx)
}
