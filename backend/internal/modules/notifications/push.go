package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	billingcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

var (
	ErrPushInvalid         = errors.New("invalid push request")
	ErrPushUnauthorized    = errors.New("invalid installation credential")
	ErrPushNotFound        = errors.New("push resource not found")
	ErrPushConflict        = errors.New("push version conflict")
	ErrPushPairingConsumed = errors.New("push pairing consumed")
	ErrPushUnavailable     = errors.New("push unavailable")
	ErrPushReconnect       = errors.New("push session requires reconnect")
	ErrPushRateLimited     = errors.New("push rate limited")
	ErrPushAccountChange   = errors.New("push account change needs confirmation")
)

type PushSender interface {
	Send(context.Context, string, map[string]string, time.Duration) error
}

type PushConfig struct {
	Enabled      bool
	ProjectID    string
	Origin       string
	SessionLease func(context.Context, pgx.Tx, string, string) (time.Time, error)
	Sender       PushSender
	Senders      map[string]PushSender
	Providers    []string
}

type Push struct {
	pool        *pgxpool.Pool
	cfg         PushConfig
	box         cipher.AEAD
	hashKey     []byte
	entitlement billingcontracts.Service
}

func NewPush(pool *pgxpool.Pool, encodedKey string, cfg PushConfig, entitlement billingcontracts.Service) (*Push, error) {
	p := &Push{pool: pool, cfg: cfg, entitlement: entitlement}
	if !cfg.Enabled {
		return p, nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != 32 || pool == nil || entitlement == nil || cfg.SessionLease == nil || cfg.ProjectID == "" {
		return nil, errors.New("push configuration is incomplete")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	p.box, err = cipher.NewGCM(block)
	p.hashKey = key
	return p, err
}

func (p *Push) enabled() error {
	if !p.cfg.Enabled {
		return ErrPushUnavailable
	}
	return nil
}
func pushSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (p *Push) fingerprint(value string) []byte {
	m := hmac.New(sha256.New, p.hashKey)
	m.Write([]byte("android-push\x00" + value))
	return m.Sum(nil)
}
func (p *Push) seal(id, field string, value []byte) ([]byte, error) {
	nonce := make([]byte, p.box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return p.box.Seal(nonce, nonce, value, []byte("android-push:"+id+":"+field)), nil
}
func (p *Push) open(id, field string, value []byte) ([]byte, error) {
	n := p.box.NonceSize()
	if len(value) < n {
		return nil, ErrPushInvalid
	}
	return p.box.Open(nil, value[:n], value[n:], []byte("android-push:"+id+":"+field))
}
func pushChallenge(value string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return len(value) == 43 && err == nil && len(b) == 32
}
func pushAppVersion(value string) bool {
	return len(value) > 0 && len(value) <= 32 && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+-") == ""
}
func pushLabel(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) > 0 && utf8.RuneCountInString(value) <= 64 && !strings.ContainsAny(value, "\r\n\x00")
}
func pushToken(value string) bool {
	return len(value) > 0 && len(value) <= 4096 && !strings.ContainsAny(value, " \t\r\n\x00")
}

type pushInstallation struct {
	ID, AccountID, SessionID, Label, AppVersion, Status, Permission, LastPairingID, Provider string
	Generation, Version                                                                      int64
	Token                                                                                    []byte
	TokenValid, ProviderAvailable                                                            bool
	CreatedAt, UpdatedAt, LastSeenAt                                                         time.Time
}

const pushInstallationColumns = `id::text,COALESCE(account_id::text,''),COALESCE(session_id::text,''),device_label,app_version,status,permission,COALESCE(last_pairing_id::text,''),binding_generation,registration_version,encrypted_token,token_valid,provider_available,created_at,updated_at,last_seen_at,provider`

func scanPushInstallation(row pgx.Row) (pushInstallation, error) {
	var i pushInstallation
	err := row.Scan(&i.ID, &i.AccountID, &i.SessionID, &i.Label, &i.AppVersion, &i.Status, &i.Permission, &i.LastPairingID, &i.Generation, &i.Version, &i.Token, &i.TokenValid, &i.ProviderAvailable, &i.CreatedAt, &i.UpdatedAt, &i.LastSeenAt, &i.Provider)
	return i, err
}
func (p *Push) authenticate(ctx context.Context, bearer string) (pushInstallation, error) {
	if err := p.enabled(); err != nil {
		return pushInstallation{}, err
	}
	parts := strings.Split(bearer, ".")
	if len(parts) != 2 || !messengerValidID(parts[0]) || !pushChallenge(parts[1]) {
		return pushInstallation{}, ErrPushUnauthorized
	}
	hash := sha256.Sum256([]byte(bearer))
	i, err := scanPushInstallation(p.pool.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 AND credential_hash=$2 AND status<>'revoked'`, parts[0], hash[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return i, ErrPushUnauthorized
	}
	return i, err
}

func (p *Push) sessionLease(ctx context.Context, tx pgx.Tx, accountID, sessionID string) (time.Time, error) {
	if accountID == "" || sessionID == "" {
		return time.Time{}, ErrPushReconnect
	}
	expires, err := p.cfg.SessionLease(ctx, tx, accountID, sessionID)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, authcontracts.ErrSessionRevoked) || errors.Is(err, authcontracts.ErrSessionExpired) {
		return time.Time{}, errors.Join(ErrPushReconnect, err)
	}
	return expires, err
}

func (p *Push) Installation(ctx context.Context, bearer string) (PushInstallationStatus, error) {
	i, err := p.authenticate(ctx, bearer)
	if err != nil {
		return PushInstallationStatus{}, err
	}
	result := PushInstallationStatus{ID: i.ID, Provider: i.Provider, DeviceLabel: i.Label, LastSeenAt: i.LastSeenAt, BindingGeneration: i.Generation, RegistrationVersion: i.Version, Status: "pending", LastPairingID: pushOptionalID(i.LastPairingID)}
	if i.AccountID == "" {
		return result, nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	expires, err := p.sessionLease(ctx, tx, i.AccountID, i.SessionID)
	if errors.Is(err, ErrPushReconnect) {
		result.Status = pushSessionStatus(err)
		return result, nil
	}
	if err != nil {
		return result, err
	}
	current, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 AND status<>'revoked' FOR SHARE`, i.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrPushUnauthorized
	}
	if err != nil {
		return result, err
	}
	if current.Generation != i.Generation || current.SessionID != i.SessionID || current.AccountID != i.AccountID {
		result.Status = "session_revoked"
		return result, nil
	}
	i = current
	result.Provider = i.Provider
	result.RegistrationVersion = i.Version
	result.LastSeenAt = i.LastSeenAt
	result.LeaseExpiresAt = &expires
	switch {
	case i.Status == "pending":
		result.Status = "pending"
	case i.Permission != "granted":
		result.Status = "permission_denied"
	case !i.TokenValid:
		result.Status = "invalid_token"
	case !i.ProviderAvailable:
		result.Status = "provider_unavailable"
	default:
		result.Status = "ready"
	}
	return result, nil
}

func pushProvider(value string) bool {
	return value == "rustore" || value == "fcm" || value == "huawei"
}

func (p *Push) providerEnabled(value string) bool {
	if !p.cfg.Enabled || !pushProvider(value) {
		return false
	}
	if p.cfg.Providers == nil {
		return value == "rustore"
	}
	for _, provider := range p.cfg.Providers {
		if provider == value {
			return true
		}
	}
	return false
}

func (p *Push) sender(provider string) PushSender {
	if sender := p.cfg.Senders[provider]; sender != nil {
		return sender
	}
	if provider == "rustore" {
		return p.cfg.Sender
	}
	return nil
}

func (p *Push) cancelInstallation(ctx context.Context, tx pgx.Tx, id, code string) error {
	_, err := tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=$2 WHERE installation_id=$1 AND status IN ('pending','leased')`, id, code)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=$2 WHERE installation_id=$1 AND status IN ('pending','leased')`, id, code)
	}
	return err
}
func pushError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" && databaseError.ConstraintName == "push_installations_active_token_idx" {
		return ErrPushConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPushNotFound
	}
	return err
}
func pushContextID(id string) error {
	if !messengerValidID(id) {
		return ErrPushNotFound
	}
	return nil
}
func pushSessionStatus(err error) string {
	if errors.Is(err, authcontracts.ErrSessionExpired) {
		return "session_expired"
	}
	return "session_revoked"
}
func pushOptionalID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func (p *Push) rateInstallation(ctx context.Context, bearer string) error {
	i, err := p.authenticate(ctx, bearer)
	if err != nil {
		return err
	}
	cmd, err := p.pool.Exec(ctx, `UPDATE notifications.push_installations SET last_seen_at=now(),request_count=CASE WHEN request_window<now()-interval '1 minute' THEN 1 ELSE request_count+1 END,request_window=CASE WHEN request_window<now()-interval '1 minute' THEN now() ELSE request_window END WHERE id=$1 AND (request_window<now()-interval '1 minute' OR request_count<120)`, i.ID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrPushRateLimited
	}
	return nil
}
