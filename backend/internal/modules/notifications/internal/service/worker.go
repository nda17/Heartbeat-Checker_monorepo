package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type candidate struct {
	ID, AccountID, MonitorID, EndpointID string
	NextAttemptAt                        time.Time
}

const (
	claimPageSize = 100
	maxClaimPages = 4
)

func (s *Service) resetClaimCursor() {
	s.claimAfter = time.Unix(0, 0).UTC()
	s.claimAfterID = "00000000-0000-0000-0000-000000000000"
	s.claimFence = time.Time{}
	s.claimScanActive = false
}

func (s *Service) Claim(ctx context.Context) (*contracts.Attempt, error) {
	// A persistent keyset cursor prevents a large ineligible prefix from hiding
	// ready deliveries behind LIMIT. The mutex only covers database claims.
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if !s.claimScanActive {
		s.resetClaimCursor()
		if err := s.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&s.claimFence); err != nil {
			return nil, err
		}
		s.claimScanActive = true
	}
	for page := 0; page < maxClaimPages; page++ {
		rows, err := s.pool.Query(ctx, `SELECT d.id::text,d.account_id::text,d.monitor_id::text,d.endpoint_id::text,d.next_attempt_at
		FROM notifications.deliveries d WHERE d.status IN ('pending','leased')
		AND (d.status='pending' AND d.next_attempt_at<=now() OR d.status='leased' AND d.lease_until<=now())
		AND d.next_attempt_at<=$3
		AND (d.next_attempt_at,d.id)>($1::timestamptz,$2::uuid)
		AND NOT EXISTS (SELECT 1 FROM notifications.deliveries prior WHERE prior.endpoint_id=d.endpoint_id AND prior.sequence<d.sequence AND prior.status IN ('pending','leased'))
		ORDER BY d.next_attempt_at,d.id LIMIT $4`, s.claimAfter, s.claimAfterID, s.claimFence, claimPageSize)
		if err != nil {
			return nil, err
		}
		candidates := []candidate{}
		for rows.Next() {
			var c candidate
			if err = rows.Scan(&c.ID, &c.AccountID, &c.MonitorID, &c.EndpointID, &c.NextAttemptAt); err != nil {
				rows.Close()
				return nil, err
			}
			candidates = append(candidates, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			s.resetClaimCursor()
			return nil, nil
		}
		for _, c := range candidates {
			s.claimAfter, s.claimAfterID = c.NextAttemptAt, c.ID
			tx, err := s.pool.Begin(ctx)
			if err != nil {
				return nil, err
			}
			attempt, err := s.claimOne(ctx, tx, c)
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				tx.Rollback(ctx)
			}
			if err != nil {
				return nil, err
			}
			if attempt != nil {
				return attempt, nil
			}
		}
		if len(candidates) < claimPageSize {
			s.resetClaimCursor()
			return nil, nil
		}
	}
	return nil, nil
}

func (s *Service) claimOne(ctx context.Context, tx pgx.Tx, c candidate) (*contracts.Attempt, error) {
	// Serializing short claims makes capacity limits reliable across worker processes.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7349281723)`); err != nil {
		return nil, err
	}
	active, available, err := s.accounts.TryActive(ctx, tx, c.AccountID)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, nil
	}
	err = s.owners.TryLockOwned(ctx, tx, c.AccountID, c.MonitorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var version int64
	var enabled bool
	var encryptedURL, encryptedSecret []byte
	var host string
	err = tx.QueryRow(ctx, `SELECT version,enabled,encrypted_url,encrypted_secret,normalized_host FROM notifications.webhooks WHERE id=$1 AND account_id=$2 AND monitor_id=$3 FOR UPDATE SKIP LOCKED`, c.EndpointID, c.AccountID, c.MonitorID).Scan(&version, &enabled, &encryptedURL, &encryptedSecret, &host)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a contracts.Attempt
	var status string
	var attempts int
	var sequence int64
	var next, deadline time.Time
	var lease *time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,event_id::text,endpoint_version,body,status,attempts,sequence,next_attempt_at,deadline_at,lease_until FROM notifications.deliveries WHERE id=$1 FOR UPDATE SKIP LOCKED`, c.ID).Scan(&a.ID, &a.EventID, &a.EndpointVersion, &a.Body, &status, &attempts, &sequence, &next, &deadline, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.AccountID, a.MonitorID, a.EndpointID = c.AccountID, c.MonitorID, c.EndpointID
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if status != "pending" && status != "leased" || status == "pending" && next.After(now) || status == "leased" && (lease == nil || lease.After(now)) {
		return nil, nil
	}
	if !active || !enabled || version != a.EndpointVersion {
		_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),last_error_code=$2 WHERE id=$1`, a.ID, "endpoint_inactive")
		return nil, err
	}
	if !deadline.After(now) || attempts >= 8 {
		_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),last_error_code='deadline_exceeded' WHERE id=$1`, a.ID)
		return nil, err
	}
	var earlier bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications.deliveries WHERE endpoint_id=$1 AND sequence<$2 AND status IN ('pending','leased'))`, c.EndpointID, sequence).Scan(&earlier)
	if err != nil {
		return nil, err
	}
	if earlier {
		return nil, nil
	}
	var accountActive, hostActive int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE account_id=$1),count(*) FILTER(WHERE normalized_host=$2)
		FROM notifications.reservations WHERE expires_at>now()`, c.AccountID, host).Scan(&accountActive, &hostActive)
	if err != nil {
		return nil, err
	}
	if accountActive >= 1 || hostActive >= 1 {
		return nil, nil
	}
	a.URL, err = s.box.open(c.AccountID, c.EndpointID, "url", encryptedURL)
	if err != nil {
		return nil, err
	}
	a.Secret, err = s.box.open(c.AccountID, c.EndpointID, "secret", encryptedSecret)
	if err != nil {
		return nil, err
	}
	a.Token, err = outbox.ID()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='leased',attempts=attempts+1,attempt_token=$2,lease_until=now()+interval '30 seconds' WHERE id=$1`, a.ID, a.Token)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications.reservations(attempt_token,account_id,normalized_host,expires_at)
		VALUES($1,$2,$3,now()+interval '30 seconds')`, a.Token, c.AccountID, host)
	return &a, err
}

var retryDelays = [...]time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}

func retryable(result contracts.Result) bool {
	if result.ErrorCode == "probe_unavailable" || result.ErrorCode == "request_failed" {
		return true
	}
	return result.HTTPStatus == 408 || result.HTTPStatus == 429 || result.HTTPStatus >= 500 && result.HTTPStatus <= 599
}

func (s *Service) Complete(ctx context.Context, a contracts.Attempt, result contracts.Result) (bool, error) {
	// The outgoing request has returned. Release only its own cancellation
	// reservation, even if the delivery or endpoint was deleted meanwhile.
	if _, err := s.pool.Exec(ctx, `DELETE FROM notifications.reservations WHERE attempt_token=$1`, a.Token); err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	active, err := s.accounts.Active(ctx, tx, a.AccountID)
	if err != nil {
		return false, err
	}
	if err = s.owners.LockOwned(ctx, tx, a.AccountID, a.MonitorID); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var version int64
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT version,enabled FROM notifications.webhooks WHERE id=$1 AND account_id=$2 FOR UPDATE`, a.EndpointID, a.AccountID).Scan(&version, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var status, token string
	var attempts int
	var deadline, lease time.Time
	err = tx.QueryRow(ctx, `SELECT status,COALESCE(attempt_token::text,''),attempts,deadline_at,COALESCE(lease_until,'epoch'::timestamptz) FROM notifications.deliveries WHERE id=$1 FOR UPDATE`, a.ID).Scan(&status, &token, &attempts, &deadline, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, err
	}
	if status != "leased" || token != a.Token || !lease.After(now) || version != a.EndpointVersion || !enabled || !active {
		return false, nil
	}
	code := result.ErrorCode
	if len(code) > 64 {
		code = "invalid_probe_response"
	}
	var httpStatus *int
	if result.HTTPStatus >= 100 && result.HTTPStatus <= 599 {
		httpStatus = &result.HTTPStatus
	}
	if code == "" && result.HTTPStatus >= 200 && result.HTTPStatus < 300 {
		_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='delivered',attempt_token=NULL,lease_until=NULL,completed_at=now(),last_http_status=$2,last_error_code='' WHERE id=$1`, a.ID, httpStatus)
	} else if retryable(result) && attempts < 8 && deadline.After(now) {
		delay := retryDelays[attempts-1]
		if result.RetryAfterSeconds > 0 && result.RetryAfterSeconds <= 86400 && time.Duration(result.RetryAfterSeconds)*time.Second > delay {
			delay = time.Duration(result.RetryAfterSeconds) * time.Second
		}
		if now.Add(delay).Before(deadline) {
			_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='pending',attempt_token=NULL,lease_until=NULL,next_attempt_at=now()+$2*interval '1 second',last_http_status=$3,last_error_code=$4 WHERE id=$1`, a.ID, int(delay.Seconds()), httpStatus, code)
		} else {
			_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),last_http_status=$2,last_error_code='deadline_exceeded' WHERE id=$1`, a.ID, httpStatus)
		}
	} else {
		if code == "" {
			code = "http_status"
		}
		_, err = tx.Exec(ctx, `UPDATE notifications.deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),last_http_status=$2,last_error_code=$3 WHERE id=$1`, a.ID, httpStatus, code)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s *Service) Cleanup(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM notifications.deliveries WHERE id IN (SELECT id FROM notifications.deliveries WHERE status IN ('delivered','failed','cancelled') AND completed_at<now()-interval '30 days' ORDER BY completed_at,id LIMIT 500)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM notifications.reservations WHERE attempt_token IN (SELECT attempt_token FROM notifications.reservations WHERE expires_at<=now() ORDER BY expires_at,attempt_token LIMIT 500)`)
	return err
}
