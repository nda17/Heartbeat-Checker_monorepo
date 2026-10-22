package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type PushTestInput struct {
	RequestID string `json:"requestId"`
}
type PushTestStatus struct {
	ID                 string     `json:"id"`
	State              string     `json:"state"`
	CreatedAt          time.Time  `json:"createdAt"`
	ExpiresAt          time.Time  `json:"expiresAt"`
	NextAllowedAt      time.Time  `json:"nextAllowedAt"`
	ReceivedAt         *time.Time `json:"receivedAt"`
	DisplaySubmittedAt *time.Time `json:"displaySubmittedAt"`
	ErrorCode          string     `json:"errorCode"`
}
type PushTestContent struct {
	DeliveryID   string    `json:"deliveryId"`
	Kind         string    `json:"kind"`
	OccurredAt   time.Time `json:"occurredAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	ReceiptToken string    `json:"receiptToken"`
}
type PushTestReceipt struct {
	ReceiptToken string `json:"receiptToken"`
	Stage        string `json:"stage"`
}

type pushTestRateError struct{ Next time.Time }

func (e pushTestRateError) Error() string { return ErrPushRateLimited.Error() }
func (e pushTestRateError) Unwrap() error { return ErrPushRateLimited }

type pushTestDelivery struct {
	PushAttempt
	RequestID                                       string
	Fingerprint, ReceiptHash, Snapshot, Destination []byte
	Status, ErrorCode                               string
	CreatedAt                                       time.Time
	Lease                                           *time.Time
	Next                                            time.Time
	ReceivedAt, DisplaySubmittedAt, DisplayFailedAt *time.Time
	Current                                         pushInstallation
}

const pushTestColumns = `id::text,request_id::text,account_id::text,session_id::text,installation_id::text,binding_generation,request_fingerprint,receipt_token_hash,encrypted_snapshot,registration_version,status,attempts,next_attempt_at,lease_until,COALESCE(attempt_token::text,''),deadline_at,COALESCE(delivery_provider,''),encrypted_destination,error_code,created_at,received_at,display_submitted_at,display_failed_at`

func scanPushTest(row pgx.Row) (pushTestDelivery, error) {
	var d pushTestDelivery
	err := row.Scan(&d.ID, &d.RequestID, &d.AccountID, &d.SessionID, &d.InstallationID, &d.BindingGeneration, &d.Fingerprint, &d.ReceiptHash, &d.Snapshot, &d.RegistrationVersion, &d.Status, &d.Attempts, &d.Next, &d.Lease, &d.Token, &d.Deadline, &d.Provider, &d.Destination, &d.ErrorCode, &d.CreatedAt, &d.ReceivedAt, &d.DisplaySubmittedAt, &d.DisplayFailedAt)
	return d, err
}

func pushTestView(d pushTestDelivery, next time.Time) PushTestStatus {
	state := map[string]string{"pending": "queued", "leased": "sending", "accepted": "provider_accepted", "failed": "failed", "cancelled": "cancelled"}[d.Status]
	if !time.Now().Before(d.Deadline) && d.Status != "cancelled" {
		state = "expired"
	}
	if d.ReceivedAt != nil {
		state = "received"
	}
	if d.DisplayFailedAt != nil {
		state = "failed"
	}
	if d.DisplaySubmittedAt != nil {
		state = "display_submitted"
	}
	return PushTestStatus{ID: d.ID, State: state, CreatedAt: d.CreatedAt, ExpiresAt: d.Deadline, NextAllowedAt: next, ReceivedAt: d.ReceivedAt, DisplaySubmittedAt: d.DisplaySubmittedAt, ErrorCode: d.ErrorCode}
}

// Every new request and throttle read holds the account's existing billing lock.
// The entitlement quota is deliberately irrelevant for a phone test.
func (p *Push) testNextAllowed(ctx context.Context, tx pgx.Tx, accountID, installationID string, now time.Time) (time.Time, error) {
	var installationNext, accountNext *time.Time
	err := tx.QueryRow(ctx, `SELECT (SELECT max(created_at)+interval '60 seconds' FROM notifications.push_test_deliveries WHERE installation_id=$1),
 (SELECT min(created_at)+interval '1 hour' FROM (SELECT created_at FROM notifications.push_test_deliveries WHERE account_id=$2 AND created_at>$3::timestamptz-interval '1 hour' ORDER BY created_at DESC LIMIT 5) recent HAVING count(*)>=5)`, installationID, accountID, now).Scan(&installationNext, &accountNext)
	next := now
	if installationNext != nil && installationNext.After(next) {
		next = *installationNext
	}
	if accountNext != nil && accountNext.After(next) {
		next = *accountNext
	}
	return next, err
}

func (p *Push) testActorInstallation(ctx context.Context, tx pgx.Tx, actor authcontracts.Principal, credential string) (pushInstallation, error) {
	id, ok := nativeCredentialID(credential)
	if actor.ClientKind != "android" || !ok {
		return pushInstallation{}, ErrPushUnauthorized
	}
	if _, err := p.entitlement.Lock(ctx, tx, actor.AccountID); err != nil {
		return pushInstallation{}, err
	}
	if _, err := p.sessionLease(ctx, tx, actor.AccountID, actor.SessionID); err != nil {
		return pushInstallation{}, err
	}
	hash := sha256.Sum256([]byte(credential))
	i, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 AND credential_hash=$2 AND status<>'revoked' FOR UPDATE`, id, hash[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return i, ErrPushUnauthorized
	}
	if err != nil {
		return i, err
	}
	if i.AccountID != actor.AccountID || i.SessionID != actor.SessionID {
		return i, ErrPushReconnect
	}
	return i, nil
}

func (p *Push) CreatePushTest(ctx context.Context, actor authcontracts.Principal, credential string, in PushTestInput) (PushTestStatus, bool, error) {
	if err := p.enabled(); err != nil {
		return PushTestStatus{}, false, err
	}
	if !messengerValidID(in.RequestID) {
		return PushTestStatus{}, false, ErrPushInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushTestStatus{}, false, err
	}
	defer tx.Rollback(ctx)
	i, err := p.testActorInstallation(ctx, tx, actor, credential)
	if err != nil {
		return PushTestStatus{}, false, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	fingerprint := p.fingerprint(strings.Join([]string{"push-test", actor.AccountID, actor.SessionID, i.ID, strconv.FormatInt(i.Generation, 10)}, "\x00"))
	prior, err := scanPushTest(tx.QueryRow(ctx, `SELECT `+pushTestColumns+` FROM notifications.push_test_deliveries WHERE account_id=$1 AND request_id=$2 FOR UPDATE`, actor.AccountID, in.RequestID))
	if err == nil {
		if !hmac.Equal(prior.Fingerprint, fingerprint) {
			return PushTestStatus{}, false, ErrPushConflict
		}
		next, err := p.testNextAllowed(ctx, tx, actor.AccountID, i.ID, now)
		if err != nil {
			return PushTestStatus{}, false, err
		}
		return pushTestView(prior, next), false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PushTestStatus{}, false, err
	}
	// API instances only enqueue; provider credentials and senders belong to
	// worker instances and are checked again when claiming the delivery.
	if i.Status != "ready" || i.Permission != "granted" || !i.TokenValid || !i.ProviderAvailable || len(i.Token) == 0 || !p.providerEnabled(i.Provider) {
		return PushTestStatus{}, false, ErrPushUnavailable
	}
	next, err := p.testNextAllowed(ctx, tx, actor.AccountID, i.ID, now)
	if err != nil {
		return PushTestStatus{}, false, err
	}
	if next.After(now) {
		return PushTestStatus{}, false, pushTestRateError{Next: next}
	}
	id, err := outbox.ID()
	if err != nil {
		return PushTestStatus{}, false, err
	}
	receipt, err := pushSecret()
	if err != nil {
		return PushTestStatus{}, false, err
	}
	content := PushTestContent{DeliveryID: id, Kind: "test", OccurredAt: now, ExpiresAt: now.Add(5 * time.Minute), Title: "Проверка уведомлений", Body: "Уведомления Heartbeat Checker работают на этом телефоне.", ReceiptToken: receipt}
	raw, err := json.Marshal(content)
	if err != nil {
		return PushTestStatus{}, false, err
	}
	snapshot, err := p.seal(id, "test-delivery", raw)
	if err != nil {
		return PushTestStatus{}, false, err
	}
	receiptHash := sha256.Sum256([]byte(receipt))
	_, err = tx.Exec(ctx, `INSERT INTO notifications.push_test_deliveries(id,request_id,account_id,session_id,installation_id,binding_generation,request_fingerprint,encrypted_snapshot,receipt_token_hash,registration_version,deadline_at,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, in.RequestID, actor.AccountID, actor.SessionID, i.ID, i.Generation, fingerprint, snapshot, receiptHash[:], i.Version, content.ExpiresAt, now)
	if err != nil {
		return PushTestStatus{}, false, err
	}
	next, err = p.testNextAllowed(ctx, tx, actor.AccountID, i.ID, now)
	if err != nil {
		return PushTestStatus{}, false, err
	}
	return PushTestStatus{ID: id, State: "queued", CreatedAt: now, ExpiresAt: content.ExpiresAt, NextAllowedAt: next}, true, tx.Commit(ctx)
}

func (p *Push) PushTest(ctx context.Context, actor authcontracts.Principal, credential, id string) (PushTestStatus, error) {
	if err := p.enabled(); err != nil {
		return PushTestStatus{}, err
	}
	if !messengerValidID(id) {
		return PushTestStatus{}, ErrPushNotFound
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushTestStatus{}, err
	}
	defer tx.Rollback(ctx)
	i, err := p.testActorInstallation(ctx, tx, actor, credential)
	if err != nil {
		return PushTestStatus{}, err
	}
	d, err := scanPushTest(tx.QueryRow(ctx, `SELECT `+pushTestColumns+` FROM notifications.push_test_deliveries WHERE id=$1 AND account_id=$2 AND session_id=$3 AND installation_id=$4 AND binding_generation=$5 FOR SHARE`, id, actor.AccountID, actor.SessionID, i.ID, i.Generation))
	if err != nil {
		return PushTestStatus{}, pushError(err)
	}
	next, err := p.testNextAllowed(ctx, tx, actor.AccountID, i.ID, time.Now().UTC())
	if err != nil {
		return PushTestStatus{}, err
	}
	return pushTestView(d, next), tx.Commit(ctx)
}

// The source account/session and binding are immutable. The route is frozen on
// first claim and is never redirected to a newly registered token on a retry.
func (p *Push) testDeliveryGuard(ctx context.Context, tx pgx.Tx, id, installationID string) (pushTestDelivery, bool, error) {
	d, err := scanPushTest(tx.QueryRow(ctx, `SELECT `+pushTestColumns+` FROM notifications.push_test_deliveries WHERE id=$1 AND ($2='' OR installation_id=NULLIF($2,'')::uuid)`, id, installationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	if _, err = p.entitlement.Lock(ctx, tx, d.AccountID); err != nil {
		return d, false, err
	}
	if _, err = p.sessionLease(ctx, tx, d.AccountID, d.SessionID); errors.Is(err, ErrPushReconnect) {
		return d, false, nil
	} else if err != nil {
		return d, false, err
	}
	i, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 FOR UPDATE`, d.InstallationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d, err = scanPushTest(tx.QueryRow(ctx, `SELECT `+pushTestColumns+` FROM notifications.push_test_deliveries WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.Current = i
	valid := i.AccountID == d.AccountID && i.SessionID == d.SessionID && i.Generation == d.BindingGeneration && i.Status == "ready" && i.Permission == "granted" && i.TokenValid && i.ProviderAvailable && len(i.Token) > 0 && d.Deadline.After(time.Now()) && d.Status != "cancelled"
	if valid && len(d.Destination) > 0 {
		raw, err := p.open(d.ID, "test-destination", d.Destination)
		if err != nil {
			return d, false, err
		}
		d.RegistrationToken = string(raw)
	}
	return d, valid, nil
}

func (p *Push) claimPushTest(ctx context.Context) (*PushAttempt, error) {
	if !p.cfg.Enabled || p.cfg.Sender == nil && len(p.cfg.Senders) == 0 {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text FROM notifications.push_test_deliveries WHERE status IN ('pending','leased') AND (status='pending' AND next_attempt_at<=now() OR status='leased' AND lease_until<=now()) ORDER BY next_attempt_at,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		attempt, err := p.claimOnePushTest(ctx, id)
		if err != nil || attempt != nil {
			return attempt, err
		}
	}
	return nil, nil
}

func (p *Push) claimOnePushTest(ctx context.Context, id string) (*PushAttempt, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7349281729)`); err != nil {
		return nil, err
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM notifications.push_deliveries WHERE status='leased' AND lease_until>now())+(SELECT count(*) FROM notifications.push_test_deliveries WHERE status='leased' AND lease_until>now())`).Scan(&active); err != nil {
		return nil, err
	}
	if active >= 2 {
		return nil, nil
	}
	d, valid, err := p.testDeliveryGuard(ctx, tx, id, "")
	if err != nil {
		return nil, err
	}
	if d.ID == "" {
		return nil, nil
	}
	if !valid {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status=CASE WHEN deadline_at<=now() THEN 'failed' ELSE 'cancelled' END,attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=CASE WHEN deadline_at<=now() THEN 'deadline_exceeded' ELSE 'delivery_inactive' END WHERE id=$1 AND status IN ('pending','leased')`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if d.Status != "pending" && d.Status != "leased" || d.Status == "pending" && d.Next.After(time.Now()) || d.Status == "leased" && d.Lease != nil && d.Lease.After(time.Now()) {
		return nil, nil
	}
	if d.ReceivedAt != nil || d.Attempts >= 3 {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status=CASE WHEN received_at IS NOT NULL THEN 'accepted' ELSE 'failed' END,attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=CASE WHEN received_at IS NOT NULL THEN error_code ELSE 'attempts_exhausted' END WHERE id=$1`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if len(d.Destination) == 0 {
		if !p.providerEnabled(d.Current.Provider) || p.sender(d.Current.Provider) == nil {
			return nil, nil
		}
		raw, err := p.open(d.InstallationID, "token", d.Current.Token)
		if err != nil {
			return nil, err
		}
		d.Destination, err = p.seal(d.ID, "test-destination", raw)
		if err != nil {
			return nil, err
		}
		d.Provider, d.RegistrationToken, d.RegistrationVersion = d.Current.Provider, string(raw), d.Current.Version
	}
	if p.sender(d.Provider) == nil {
		return nil, nil
	}
	d.Token, err = outbox.ID()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status='leased',attempts=attempts+1,attempt_token=$2,lease_until=now()+interval '45 seconds',registration_version=$3,delivery_provider=$4,encrypted_destination=$5 WHERE id=$1`, id, d.Token, d.RegistrationVersion, d.Provider, d.Destination)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	d.Attempts++
	return &d.PushAttempt, nil
}

func (p *Push) beforePushTest(ctx context.Context, a PushAttempt) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	d, valid, err := p.testDeliveryGuard(ctx, tx, a.ID, a.InstallationID)
	if err != nil {
		return false, err
	}
	valid = valid && d.Status == "leased" && d.Token == a.Token && d.Lease != nil && d.Lease.After(time.Now()) && d.ReceivedAt == nil
	if valid && (d.Provider != a.Provider || d.RegistrationVersion != a.RegistrationVersion) {
		return false, ErrPushConflict
	}
	if !valid {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status=CASE WHEN received_at IS NOT NULL THEN 'accepted' ELSE 'cancelled' END,attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=CASE WHEN received_at IS NOT NULL THEN error_code ELSE 'delivery_inactive' END WHERE id=$1 AND status='leased' AND attempt_token=$2`, a.ID, a.Token)
		if err != nil {
			return false, err
		}
	}
	return valid, tx.Commit(ctx)
}

func (p *Push) completePushTest(ctx context.Context, a PushAttempt, sendErr error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	d, valid, err := p.testDeliveryGuard(ctx, tx, a.ID, a.InstallationID)
	if err != nil {
		return err
	}
	if d.Status != "leased" || d.Token != a.Token || d.Lease == nil || !d.Lease.After(time.Now()) {
		return nil
	}
	switch {
	case !valid:
		_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='delivery_inactive' WHERE id=$1`, a.ID)
	case d.Provider != a.Provider || d.RegistrationVersion != a.RegistrationVersion:
		return ErrPushConflict
	case sendErr == nil || d.ReceivedAt != nil:
		_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status='accepted',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=CASE WHEN display_submitted_at IS NOT NULL THEN '' WHEN display_failed_at IS NOT NULL THEN 'display_failed' ELSE '' END WHERE id=$1`, a.ID)
	default:
		failure := pushProviderError{code: "request_failed"}
		var provider pushProviderError
		if errors.As(sendErr, &provider) {
			failure = provider
		}
		if failure.invalidToken {
			_, err = tx.Exec(ctx, `UPDATE notifications.push_installations SET token_valid=false,updated_at=now() WHERE id=$1 AND registration_version=$2`, a.InstallationID, a.RegistrationVersion)
		}
		if err == nil && failure.unavailable {
			_, err = tx.Exec(ctx, `UPDATE notifications.push_installations SET provider_available=false,updated_at=now() WHERE id=$1 AND registration_version=$2`, a.InstallationID, a.RegistrationVersion)
		}
		if err != nil {
			return err
		}
		delay := time.Duration(1<<min(d.Attempts, 6)) * time.Second
		if failure.retryAfter > delay {
			delay = failure.retryAfter
		}
		if failure.permanent || d.Attempts >= 3 || !time.Now().Add(delay).Before(d.Deadline) {
			_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=$2 WHERE id=$1`, a.ID, failure.code)
		} else {
			_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET status='pending',attempt_token=NULL,lease_until=NULL,next_attempt_at=now()+$2*interval '1 second',error_code=$3 WHERE id=$1`, a.ID, int(delay.Seconds()), failure.code)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Push) processTestDelivery(ctx context.Context) (bool, error) {
	a, err := p.claimPushTest(ctx)
	if err != nil || a == nil {
		return false, err
	}
	valid, err := p.beforePushTest(ctx, *a)
	if err != nil || !valid {
		return true, err
	}
	ttl := time.Until(a.Deadline)
	if ttl < time.Second {
		return true, p.completePushTest(ctx, *a, pushProviderError{code: "deadline_exceeded", permanent: true})
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = p.sender(a.Provider).Send(sendCtx, a.RegistrationToken, map[string]string{"v": "2", "deliveryId": a.ID, "installationId": a.InstallationID, "bindingGeneration": strconv.FormatInt(a.BindingGeneration, 10), "expiresAt": a.Deadline.UTC().Format(time.RFC3339Nano)}, ttl)
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	return true, p.completePushTest(ctx, *a, err)
}

func (p *Push) PushTestContent(ctx context.Context, credential, id string) (PushTestContent, error) {
	i, err := p.authenticate(ctx, credential)
	if err != nil {
		return PushTestContent{}, err
	}
	if !messengerValidID(id) {
		return PushTestContent{}, ErrPushNotFound
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushTestContent{}, err
	}
	defer tx.Rollback(ctx)
	d, valid, err := p.testDeliveryGuard(ctx, tx, id, i.ID)
	if err != nil {
		return PushTestContent{}, err
	}
	if !valid || d.Attempts == 0 {
		return PushTestContent{}, ErrPushNotFound
	}
	raw, err := p.open(id, "test-delivery", d.Snapshot)
	if err != nil {
		return PushTestContent{}, err
	}
	var content PushTestContent
	if err = json.Unmarshal(raw, &content); err != nil {
		return content, err
	}
	return content, tx.Commit(ctx)
}

func (p *Push) RecordPushTestReceipt(ctx context.Context, credential, id string, in PushTestReceipt) error {
	if !pushChallenge(in.ReceiptToken) || (in.Stage != "received" && in.Stage != "display_submitted" && in.Stage != "display_failed") {
		return ErrPushInvalid
	}
	i, err := p.authenticate(ctx, credential)
	if err != nil {
		return err
	}
	if !messengerValidID(id) {
		return ErrPushNotFound
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	d, valid, err := p.testDeliveryGuard(ctx, tx, id, i.ID)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(in.ReceiptToken))
	if !valid || d.Attempts == 0 || !hmac.Equal(hash[:], d.ReceiptHash) {
		return ErrPushNotFound
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.push_test_deliveries SET received_at=COALESCE(received_at,now()),
 display_submitted_at=CASE WHEN $2='display_submitted' THEN COALESCE(display_submitted_at,now()) ELSE display_submitted_at END,
 display_failed_at=CASE WHEN $2='display_failed' AND display_submitted_at IS NULL THEN COALESCE(display_failed_at,now()) ELSE display_failed_at END,
 error_code=CASE WHEN $2='display_submitted' OR display_submitted_at IS NOT NULL THEN '' WHEN $2='display_failed' OR display_failed_at IS NOT NULL THEN 'display_failed' ELSE '' END
 WHERE id=$1`, id, in.Stage)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
