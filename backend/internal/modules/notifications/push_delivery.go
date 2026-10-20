package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	incidentcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

func (p *Push) Enqueue(ctx context.Context, tx pgx.Tx, n incidentcontracts.Notification) error {
	if !p.cfg.Enabled {
		return nil
	}
	kind := "down"
	if n.Kind == "incident_closed" {
		kind = "up"
	} else if n.Kind != "incident_opened" {
		return ErrPushInvalid
	}
	// The monitor is already locked by incident acceptance. Do not acquire auth
	// or installation locks here; the worker and native fetch recheck all guards.
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.installation_id::text,r.delivery_generation,i.binding_generation,i.registration_version FROM notifications.push_routes r JOIN notifications.push_installations i ON i.id=r.installation_id AND i.account_id=r.account_id WHERE r.account_id=$1 AND r.monitor_id=$2 AND r.enabled AND i.status='ready' AND i.permission='granted' AND i.token_valid`, n.AccountID, n.MonitorID)
	if err != nil {
		return err
	}
	type route struct {
		id, installation             string
		generation, binding, version int64
	}
	items := []route{}
	for rows.Next() {
		var r route
		if err = rows.Scan(&r.id, &r.installation, &r.generation, &r.binding, &r.version); err != nil {
			rows.Close()
			return err
		}
		items = append(items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range items {
		id, e := outbox.ID()
		if e != nil {
			return e
		}
		title := []rune(strings.TrimSpace(n.Observation.Name))
		if len(title) > 100 {
			title = title[:100]
		}
		if len(title) == 0 {
			title = []rune("Heartbeat Checker")
		}
		label := "Монитор недоступен"
		if kind == "up" {
			label = "Монитор снова доступен"
		}
		content := PushDeliveryContent{DeliveryID: id, Kind: kind, Sequence: n.Sequence, MonitorID: n.MonitorID, IncidentID: n.IncidentID, OccurredAt: n.At.UTC(), ExpiresAt: n.At.Add(time.Hour).UTC(), Title: string(title), Body: label + " · " + n.At.In(time.FixedZone("MSK", 3*3600)).Format("02.01 15:04 МСК")}
		body, e := json.Marshal(content)
		if e != nil {
			return e
		}
		sealed, e := p.seal(id, "delivery", body)
		if e != nil {
			return e
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications.push_deliveries(id,event_id,account_id,monitor_id,incident_id,route_id,installation_id,kind,sequence,route_generation,binding_generation,registration_version,encrypted_snapshot,deadline_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(event_id,route_id) DO NOTHING`, id, n.EventID, n.AccountID, n.MonitorID, n.IncidentID, r.id, r.installation, kind, n.Sequence, r.generation, r.binding, r.version, sealed, content.ExpiresAt)
		if err != nil {
			return err
		}
	}
	return nil
}

type PushAttempt struct {
	ID, AccountID, MonitorID, RouteID, InstallationID, SessionID, Token, RegistrationToken string
	BindingGeneration, RouteGeneration, RegistrationVersion, Sequence                      int64
	Attempts                                                                               int
	Deadline                                                                               time.Time
}

type pushDeliveryState struct {
	PushAttempt
	Status   string
	Lease    *time.Time
	Next     time.Time
	Snapshot []byte
}

// deliveryGuard follows billing -> account/auth -> monitor -> installation ->
// route -> delivery. It never trusts an earlier unlocked installation snapshot.
func (p *Push) deliveryGuard(ctx context.Context, tx pgx.Tx, id, installationID string) (pushDeliveryState, bool, error) {
	var d pushDeliveryState
	err := tx.QueryRow(ctx, `SELECT d.account_id::text,d.monitor_id::text,d.route_id::text,d.installation_id::text,COALESCE(i.session_id::text,'') FROM notifications.push_deliveries d JOIN notifications.push_installations i ON i.id=d.installation_id WHERE d.id=$1 AND ($2='' OR d.installation_id=NULLIF($2,'')::uuid)`, id, installationID).Scan(&d.AccountID, &d.MonitorID, &d.RouteID, &d.InstallationID, &d.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	entitlement, err := p.entitlement.Lock(ctx, tx, d.AccountID)
	if err != nil {
		return d, false, err
	}
	if _, err = p.sessionLease(ctx, tx, d.AccountID, d.SessionID); errors.Is(err, ErrPushReconnect) {
		return d, false, nil
	} else if err != nil {
		return d, false, err
	}
	var enabled bool
	var monitorSequence int64
	err = tx.QueryRow(ctx, `SELECT enabled,sequence FROM monitors.monitors WHERE id=$1 AND account_id=$2 FOR SHARE`, d.MonitorID, d.AccountID).Scan(&enabled, &monitorSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	var rank int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM monitors.monitors WHERE account_id=$1 AND (created_at,id)<=(SELECT created_at,id FROM monitors.monitors WHERE id=$2)`, d.AccountID, d.MonitorID).Scan(&rank); err != nil {
		return d, false, err
	}
	i, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 FOR UPDATE`, d.InstallationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	var routeEnabled bool
	var generation int64
	err = tx.QueryRow(ctx, `SELECT enabled,delivery_generation FROM notifications.push_routes WHERE id=$1 AND account_id=$2 AND installation_id=$3 AND monitor_id=$4 FOR SHARE`, d.RouteID, d.AccountID, d.InstallationID, d.MonitorID).Scan(&routeEnabled, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	var binding, routeGeneration int64
	err = tx.QueryRow(ctx, `SELECT id::text,status,attempts,next_attempt_at,lease_until,COALESCE(attempt_token::text,''),deadline_at,sequence,binding_generation,route_generation,registration_version,encrypted_snapshot FROM notifications.push_deliveries WHERE id=$1 FOR UPDATE`, id).Scan(&d.ID, &d.Status, &d.Attempts, &d.Next, &d.Lease, &d.Token, &d.Deadline, &d.Sequence, &binding, &routeGeneration, &d.RegistrationVersion, &d.Snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.BindingGeneration, d.RouteGeneration = binding, routeGeneration
	valid := enabled && rank > 0 && entitlement.Quota >= rank && monitorSequence >= d.Sequence && i.AccountID == d.AccountID && i.SessionID == d.SessionID && i.Status == "ready" && i.Permission == "granted" && i.TokenValid && i.Generation == binding && routeEnabled && generation == routeGeneration && d.Deadline.After(time.Now())
	if valid {
		token, e := p.open(i.ID, "token", i.Token)
		if e != nil {
			return d, false, e
		}
		d.RegistrationToken = string(token)
		d.RegistrationVersion = i.Version
	}
	return d, valid, nil
}

func (p *Push) ClaimPush(ctx context.Context) (*PushAttempt, error) {
	if !p.cfg.Enabled || p.cfg.Sender == nil {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT d.id::text FROM notifications.push_deliveries d WHERE d.status IN ('pending','leased') AND (d.status='pending' AND d.next_attempt_at<=now() OR d.status='leased' AND d.lease_until<=now()) AND NOT EXISTS(SELECT 1 FROM notifications.push_deliveries prior WHERE prior.route_id=d.route_id AND prior.sequence<d.sequence AND prior.status IN ('pending','leased')) ORDER BY d.next_attempt_at,d.id LIMIT 100`)
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
		tx, e := p.pool.Begin(ctx)
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7349281729)`); e != nil {
			tx.Rollback(ctx)
			return nil, e
		}
		var active int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM notifications.push_deliveries WHERE status='leased' AND lease_until>now()`).Scan(&active); e != nil {
			tx.Rollback(ctx)
			return nil, e
		}
		if active >= 2 {
			tx.Rollback(ctx)
			return nil, nil
		}
		d, valid, e := p.deliveryGuard(ctx, tx, id, "")
		if e != nil {
			tx.Rollback(ctx)
			return nil, e
		}
		if !valid {
			_, e = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='delivery_inactive' WHERE id=$1 AND status IN ('pending','leased')`, id)
		} else if d.Status != "pending" && d.Status != "leased" || d.Status == "pending" && d.Next.After(time.Now()) || d.Status == "leased" && d.Lease != nil && d.Lease.After(time.Now()) {
			tx.Rollback(ctx)
			continue
		} else if d.Attempts >= 8 {
			_, e = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='deadline_exceeded' WHERE id=$1`, id)
		} else {
			var prior bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications.push_deliveries WHERE route_id=$1 AND sequence<$2 AND status IN ('pending','leased'))`, d.RouteID, d.Sequence).Scan(&prior)
			if e == nil && prior {
				tx.Rollback(ctx)
				continue
			}
			if e == nil {
				d.Token, e = outbox.ID()
			}
			if e == nil {
				_, e = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='leased',attempts=attempts+1,attempt_token=$2,lease_until=now()+interval '45 seconds',registration_version=$3 WHERE id=$1`, id, d.Token, d.RegistrationVersion)
			}
			if e == nil {
				e = tx.Commit(ctx)
			} else {
				tx.Rollback(ctx)
			}
			if e != nil {
				return nil, e
			}
			d.Attempts++
			return &d.PushAttempt, nil
		}
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if e != nil {
			return nil, e
		}
	}
	return nil, nil
}

func (p *Push) beforePush(ctx context.Context, a PushAttempt) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	d, valid, err := p.deliveryGuard(ctx, tx, a.ID, a.InstallationID)
	if err != nil {
		return false, err
	}
	valid = valid && d.Status == "leased" && d.Token == a.Token && d.Lease != nil && d.Lease.After(time.Now())
	if !valid {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='delivery_inactive' WHERE id=$1 AND status='leased' AND attempt_token=$2`, a.ID, a.Token)
	} else if d.RegistrationVersion != a.RegistrationVersion {
		valid = false
		_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='pending',attempt_token=NULL,lease_until=NULL,next_attempt_at=now() WHERE id=$1 AND attempt_token=$2`, a.ID, a.Token)
	}
	if err != nil {
		return false, err
	}
	return valid, tx.Commit(ctx)
}

func (p *Push) CompletePush(ctx context.Context, a PushAttempt, sendErr error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	d, valid, err := p.deliveryGuard(ctx, tx, a.ID, a.InstallationID)
	if err != nil {
		return err
	}
	if d.Status != "leased" || d.Token != a.Token || d.Lease == nil || !d.Lease.After(time.Now()) {
		return nil
	}
	if !valid {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='delivery_inactive' WHERE id=$1`, a.ID)
	} else if d.RegistrationVersion != a.RegistrationVersion {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='pending',attempt_token=NULL,lease_until=NULL,next_attempt_at=now() WHERE id=$1`, a.ID)
	} else if sendErr == nil {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='accepted',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='' WHERE id=$1`, a.ID)
	} else {
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
		if failure.permanent || d.Attempts >= 8 || !time.Now().Add(delay).Before(d.Deadline) {
			_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code=$2 WHERE id=$1`, a.ID, failure.code)
		} else {
			_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='pending',attempt_token=NULL,lease_until=NULL,next_attempt_at=now()+$2*interval '1 second',error_code=$3 WHERE id=$1`, a.ID, int(delay.Seconds()), failure.code)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Push) ProcessDelivery(ctx context.Context) (bool, error) {
	a, err := p.ClaimPush(ctx)
	if err != nil || a == nil {
		return false, err
	}
	valid, err := p.beforePush(ctx, *a)
	if err != nil || !valid {
		return true, err
	}
	ttl := time.Until(a.Deadline)
	if ttl < time.Second {
		return true, p.CompletePush(ctx, *a, pushProviderError{code: "deadline_exceeded", permanent: true})
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = p.cfg.Sender.Send(sendCtx, a.RegistrationToken, map[string]string{"v": "1", "deliveryId": a.ID, "installationId": a.InstallationID, "bindingGeneration": strconv.FormatInt(a.BindingGeneration, 10), "expiresAt": a.Deadline.UTC().Format(time.RFC3339Nano)}, ttl)
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	return true, p.CompletePush(ctx, *a, err)
}

func (p *Push) DeliveryContent(ctx context.Context, bearer, id string) (PushDeliveryContent, error) {
	i, err := p.authenticate(ctx, bearer)
	if err != nil {
		return PushDeliveryContent{}, err
	}
	if err = pushContextID(id); err != nil {
		return PushDeliveryContent{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushDeliveryContent{}, err
	}
	defer tx.Rollback(ctx)
	d, valid, err := p.deliveryGuard(ctx, tx, id, i.ID)
	if err != nil {
		return PushDeliveryContent{}, err
	}
	// A provider can accept an envelope while its HTTP response is lost. The
	// same attempted delivery remains readable during retries or exhaustion,
	// subject to every live guard above and the original deadline.
	if !valid || d.Attempts == 0 || d.Status == "cancelled" {
		return PushDeliveryContent{}, ErrPushNotFound
	}
	body, err := p.open(id, "delivery", d.Snapshot)
	if err != nil {
		return PushDeliveryContent{}, err
	}
	var content PushDeliveryContent
	if err = json.Unmarshal(body, &content); err != nil {
		return content, err
	}
	return content, nil
}

func (p *Push) CancelForReset(ctx context.Context, tx pgx.Tx, monitorID string) error {
	if !p.cfg.Enabled {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='monitor_reset' WHERE monitor_id=$1 AND status IN ('pending','leased','accepted','failed')`, monitorID)
	return err
}

// Cleanup must run in a separate retention transaction. Lock all installation
// candidates first, then pairings, and only then delivery rows. Every bounded
// candidate query skips locks owned by a concurrent mutation or cleaner.
func (p *Push) Cleanup(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT i.id::text FROM notifications.push_installations i WHERE i.account_id IS NULL AND i.created_at<now()-interval '10 minutes' OR i.status='revoked' AND i.updated_at<now()-interval '1 day' OR EXISTS(SELECT 1 FROM notifications.push_pairings q WHERE q.installation_id=i.id AND q.expires_at<now()-interval '10 minutes') ORDER BY i.id LIMIT 500 FOR UPDATE OF i SKIP LOCKED`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err = tx.Exec(ctx, `DELETE FROM notifications.push_pairings WHERE id IN (SELECT id FROM notifications.push_pairings WHERE installation_id=ANY($1::uuid[]) AND expires_at<now()-interval '10 minutes' ORDER BY id LIMIT 500 FOR UPDATE SKIP LOCKED)`, ids); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM notifications.push_installations WHERE id=ANY($1::uuid[]) AND (account_id IS NULL AND created_at<now()-interval '10 minutes' OR status='revoked' AND updated_at<now()-interval '1 day')`, ids); err != nil {
			return err
		}
	}
	for _, query := range []string{
		`UPDATE notifications.push_deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='deadline_exceeded' WHERE id IN (SELECT id FROM notifications.push_deliveries WHERE status IN ('pending','leased') AND deadline_at<=now() ORDER BY deadline_at,id LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM notifications.push_deliveries WHERE id IN (SELECT id FROM notifications.push_deliveries WHERE completed_at<now()-interval '30 days' ORDER BY completed_at,id LIMIT 500 FOR UPDATE SKIP LOCKED)`,
	} {
		if _, err = tx.Exec(ctx, query); err != nil {
			return err
		}
	}
	return nil
}
