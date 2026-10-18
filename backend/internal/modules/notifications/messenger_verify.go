package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type messengerLinkAttempt struct {
	id, accountID, provider, kind, token string
	encrypted                            []byte
	expires                              time.Time
}

func (m *Messenger) ProcessLink(ctx context.Context) (bool, error) {
	if !m.cfg.Enabled {
		return false, nil
	}
	if m.client == nil {
		return false, ErrMessengerUnavailable
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var a messengerLinkAttempt
	err = tx.QueryRow(ctx, `SELECT id::text,account_id::text,provider,kind,encrypted_observation,expires_at FROM notifications.messenger_links WHERE status='observed' AND attempts<8 AND expires_at>now() AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<=now()) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&a.id, &a.accountID, &a.provider, &a.kind, &a.encrypted, &a.expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	a.token, err = outbox.ID()
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_links SET attempt_token=$2,lease_until=now()+interval '30 seconds',attempts=attempts+1 WHERE id=$1`, a.id, a.token)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	plain, err := m.open(a.accountID, a.id, "observation", a.encrypted)
	if err != nil {
		return true, m.finishLink(ctx, a, "", messengerTarget{}, messengerProviderError{permanent: true})
	}
	var target messengerTarget
	if err = target.decode(plain); err != nil || target.Provider != a.provider || target.Kind != a.kind {
		return true, m.finishLink(ctx, a, "", messengerTarget{}, messengerProviderError{permanent: true})
	}
	if target.Kind != "private" {
		var linked bool
		err = m.pool.QueryRow(verifyCtx, `SELECT EXISTS(SELECT 1 FROM notifications.messenger_destinations WHERE account_id=$1 AND provider='telegram' AND kind='private' AND status='ready' AND user_fingerprint=$2)`, a.accountID, m.fingerprint("telegram", "private", target.UserID)).Scan(&linked)
		if err != nil {
			return true, err
		}
		if !linked {
			return true, m.finishLink(ctx, a, "", target, messengerProviderError{permanent: true})
		}
	}
	if target.Provider == "max" {
		var bot struct {
			ID       int64  `json:"user_id"`
			Bot      bool   `json:"is_bot"`
			Username string `json:"username"`
		}
		err = m.providerRequest(verifyCtx, http.MethodGet, "https://platform-api2.max.ru/me", m.cfg.MAXToken, nil, &bot)
		if err == nil && (!bot.Bot || bot.ID <= 0 || !strings.EqualFold("https://max.ru/"+bot.Username, m.cfg.MAXBotURL)) {
			err = messengerProviderError{permanent: true}
		}
		if err != nil {
			return true, m.finishLink(ctx, a, "", target, err)
		}
	}
	label, err := m.verifyTarget(verifyCtx, target)
	return true, m.finishLink(ctx, a, label, target, err)
}

func (m *Messenger) finishLink(ctx context.Context, a messengerLinkAttempt, label string, target messengerTarget, verifyErr error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var token *string
	var attempts int
	var expires time.Time
	var lease *time.Time
	err = tx.QueryRow(ctx, `SELECT status,attempt_token::text,attempts,expires_at,lease_until FROM notifications.messenger_links WHERE id=$1 FOR UPDATE`, a.id).Scan(&status, &token, &attempts, &expires, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "observed" || token == nil || *token != a.token || lease == nil || !lease.After(time.Now()) {
		return nil
	}
	if verifyErr != nil || !expires.After(time.Now()) {
		var p messengerProviderError
		permanent := errors.As(verifyErr, &p) && p.permanent
		if permanent || attempts >= 8 || !expires.After(time.Now()) {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_links SET status='expired',encrypted_observation=NULL,attempt_token=NULL,lease_until=NULL WHERE id=$1`, a.id)
		} else {
			backoff := time.Duration(1<<min(attempts, 6)) * time.Second
			if p.retryAfter > backoff {
				backoff = p.retryAfter
			}
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_links SET attempt_token=NULL,lease_until=NULL,next_attempt_at=now()+$2::interval WHERE id=$1`, a.id, backoff.String())
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if label == "" || len([]rune(label)) > 120 {
		return ErrMessengerInvalid
	}
	body, err := json.Marshal(messengerRecipient{Target: target, Label: label})
	if err != nil {
		return err
	}
	encrypted, err := m.seal(a.accountID, a.id, "candidate", body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_links SET status='candidate',encrypted_observation=NULL,encrypted_candidate=$2,attempt_token=NULL,lease_until=NULL WHERE id=$1`, a.id, encrypted)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
