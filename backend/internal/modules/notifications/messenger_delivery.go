package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type MessengerAttempt struct {
	ID            string
	AccountID     string
	MonitorID     string
	RouteID       string
	DestinationID string
	IncidentID    string
	Kind          string
	Token         string
	Generation    int64
	TargetVersion int64
	Target        messengerTarget
	Snapshot      messengerSnapshot
}

var errMessengerCancelled = errors.New("messenger delivery cancelled")

func (m *Messenger) ClaimDelivery(ctx context.Context) (*MessengerAttempt, error) {
	if !m.cfg.Enabled {
		return nil, nil
	}
	rows, err := m.pool.Query(ctx, `SELECT id::text,account_id::text,monitor_id::text,route_id::text,destination_id::text FROM notifications.messenger_deliveries WHERE status IN ('pending','leased') AND (status='pending' AND next_attempt_at<=now() OR status='leased' AND lease_until<=now()) AND NOT EXISTS (SELECT 1 FROM notifications.messenger_deliveries p WHERE p.route_id=messenger_deliveries.route_id AND p.sequence<messenger_deliveries.sequence AND p.status IN ('pending','leased')) ORDER BY next_attempt_at,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, account, monitor, route, destination string }
	items := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.account, &c.monitor, &c.route, &c.destination); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range items {
		tx, err := m.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		a, err := m.claimDelivery(ctx, tx, c)
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if err != nil {
			return nil, err
		}
		if a != nil {
			return a, nil
		}
	}
	return nil, nil
}

func (m *Messenger) claimDelivery(ctx context.Context, tx pgx.Tx, c struct{ id, account, monitor, route, destination string }) (*MessengerAttempt, error) {
	// Capacity is serialized across worker processes; no external request runs in this transaction.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7349281725)`); err != nil {
		return nil, err
	}
	var inFlight int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM notifications.messenger_reservations WHERE expires_at>now()`).Scan(&inFlight); err != nil {
		return nil, err
	}
	if inFlight >= 4 {
		return nil, nil
	}
	entitlement, err := m.entitlement.Lock(ctx, tx, c.account)
	if err != nil {
		return nil, err
	}
	var accountStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM accounts.accounts WHERE id=$1 FOR SHARE SKIP LOCKED`, c.account).Scan(&accountStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var monitorEnabled bool
	var outcome string
	var sequence int64
	var intervalMinutes int
	var lastChecked *time.Time
	err = tx.QueryRow(ctx, `SELECT enabled,outcome,sequence,interval_minutes,last_checked_at FROM monitors.monitors WHERE id=$1 AND account_id=$2 FOR SHARE SKIP LOCKED`, c.monitor, c.account).Scan(&monitorEnabled, &outcome, &sequence, &intervalMinutes, &lastChecked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rank int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM monitors.monitors m WHERE m.account_id=$1 AND (m.created_at,m.id)<=(SELECT created_at,id FROM monitors.monitors WHERE id=$2)`, c.account, c.monitor).Scan(&rank)
	if err != nil {
		return nil, err
	}
	var recipientFingerprint, encryptedTarget []byte
	var destinationStatus, destinationProvider string
	var targetVersion int64
	err = tx.QueryRow(ctx, `SELECT status,provider,recipient_fingerprint,encrypted_target,version FROM notifications.messenger_destinations WHERE id=$1 AND account_id=$2 FOR SHARE SKIP LOCKED`, c.destination, c.account).Scan(&destinationStatus, &destinationProvider, &recipientFingerprint, &encryptedTarget, &targetVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var routeEnabled bool
	var generation int64
	var routeDestination string
	var reminderMinutes int
	var reminderIncident *string
	err = tx.QueryRow(ctx, `SELECT enabled,delivery_generation,destination_id::text,reminder_minutes,reminder_incident_id::text FROM notifications.messenger_routes WHERE id=$1 FOR UPDATE SKIP LOCKED`, c.route).Scan(&routeEnabled, &generation, &routeDestination, &reminderMinutes, &reminderIncident)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a MessengerAttempt
	var status string
	var attempts int
	var next, deadline time.Time
	var lease *time.Time
	var storedGeneration, storedSequence int64
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT id::text,destination_id::text,incident_id::text,kind,status,attempts,next_attempt_at,lease_until,deadline_at,route_generation,sequence,encrypted_snapshot FROM notifications.messenger_deliveries WHERE id=$1 FOR UPDATE SKIP LOCKED`, c.id).Scan(&a.ID, &a.DestinationID, &a.IncidentID, &a.Kind, &status, &attempts, &next, &lease, &deadline, &storedGeneration, &storedSequence, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.AccountID, a.MonitorID, a.RouteID = c.account, c.monitor, c.route
	a.Generation, a.TargetVersion = generation, targetVersion
	now := time.Now()
	if status == "pending" && next.After(now) || status == "leased" && (lease == nil || lease.After(now)) || status != "pending" && status != "leased" {
		return nil, nil
	}
	valid := accountStatus == "active" && monitorEnabled && entitlement.Quota >= rank && rank > 0 && routeEnabled && generation == storedGeneration && routeDestination == a.DestinationID && c.destination == a.DestinationID && destinationStatus == "ready" && sequence >= storedSequence
	if a.Kind == "reminder" {
		var open bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incidents.incidents WHERE id=$1 AND monitor_id=$2 AND closed_at IS NULL)`, a.IncidentID, c.monitor).Scan(&open)
		if err != nil {
			return nil, err
		}
		fresh := lastChecked != nil && time.Since(*lastChecked) <= time.Duration(2*intervalMinutes)*time.Minute+30*time.Second
		valid = valid && reminderMinutes > 0 && reminderIncident != nil && *reminderIncident == a.IncidentID && open && outcome == "DOWN" && fresh
	} else if a.Kind != "down" && a.Kind != "up" {
		valid = false
	}
	if !valid {
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='route_inactive' WHERE id=$1`, a.ID)
		return nil, err
	}
	if !deadline.After(now) || attempts >= 8 {
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='deadline_exceeded' WHERE id=$1`, a.ID)
		return nil, err
	}
	targetBytes, err := m.open(c.account, a.DestinationID, "target", encryptedTarget)
	if err != nil {
		return nil, err
	}
	var recipient messengerRecipient
	if err = json.Unmarshal(targetBytes, &recipient); err != nil {
		return nil, err
	}
	a.Target = recipient.Target
	if err = a.Target.decodeMust(); err != nil || a.Target.Provider != destinationProvider || !bytes.Equal(recipientFingerprint, m.recipientFingerprint(a.Target)) {
		return nil, ErrMessengerInvalid
	}
	sendFingerprint := m.sendFingerprint(a.Target)
	var earlier, busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications.messenger_deliveries WHERE route_id=$1 AND sequence<$2 AND status IN ('pending','leased'))`, c.route, storedSequence).Scan(&earlier)
	if err != nil {
		return nil, err
	}
	if earlier {
		return nil, nil
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications.messenger_reservations WHERE expires_at>now() AND (account_id=$1 OR provider=$2 AND recipient_fingerprint=$3))`, c.account, destinationProvider, sendFingerprint).Scan(&busy)
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, nil
	}
	var nextSend *time.Time
	err = tx.QueryRow(ctx, `SELECT next_send_at FROM notifications.messenger_target_rates WHERE provider=$1 AND recipient_fingerprint=$2 FOR UPDATE`, destinationProvider, sendFingerprint).Scan(&nextSend)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if nextSend != nil && nextSend.After(now) {
		return nil, nil
	}
	globalFingerprint := m.fingerprint(destinationProvider, "global")
	var globalNextSend *time.Time
	err = tx.QueryRow(ctx, `SELECT next_send_at FROM notifications.messenger_target_rates WHERE provider=$1 AND recipient_fingerprint=$2 FOR UPDATE`, destinationProvider, globalFingerprint).Scan(&globalNextSend)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if globalNextSend != nil && globalNextSend.After(now) {
		return nil, nil
	}
	interval := time.Second
	if a.Target.Provider == "max" {
		interval = 500 * time.Millisecond
	} else if a.Target.Kind != "private" {
		interval = 3 * time.Second
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_target_rates(provider,recipient_fingerprint,next_send_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(provider,recipient_fingerprint) DO UPDATE SET next_send_at=EXCLUDED.next_send_at`, a.Target.Provider, sendFingerprint, interval.String())
	if err != nil {
		return nil, err
	}
	globalInterval := time.Second
	if a.Target.Provider == "max" {
		globalInterval = 500 * time.Millisecond
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_target_rates(provider,recipient_fingerprint,next_send_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(provider,recipient_fingerprint) DO UPDATE SET next_send_at=EXCLUDED.next_send_at`, a.Target.Provider, globalFingerprint, globalInterval.String())
	if err != nil {
		return nil, err
	}
	snapshotBytes, err := m.open(c.account, a.ID, "delivery", encrypted)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(snapshotBytes, &a.Snapshot); err != nil {
		return nil, err
	}
	a.Token, err = outbox.ID()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_reservations(attempt_token,account_id,provider,recipient_fingerprint,expires_at) VALUES($1,$2,$3,$4,now()+interval '45 seconds')`, a.Token, c.account, a.Target.Provider, sendFingerprint)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='leased',attempts=attempts+1,attempt_token=$2,lease_until=now()+interval '45 seconds' WHERE id=$1`, a.ID, a.Token)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (m *Messenger) CompleteDelivery(ctx context.Context, a MessengerAttempt, sendErr error) (bool, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// Route mutations lock route before delivery. Keep completion in that order
	// so a successful provider send cannot deadlock with a route edit.
	var routeID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM notifications.messenger_routes WHERE id=$1 FOR UPDATE`, a.RouteID).Scan(&routeID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM notifications.messenger_reservations WHERE attempt_token=$1`, a.Token)
	if err != nil {
		return false, err
	}
	var status string
	var token *string
	var attempts int
	var deadline time.Time
	var lease *time.Time
	err = tx.QueryRow(ctx, `SELECT status,attempt_token::text,attempts,deadline_at,lease_until FROM notifications.messenger_deliveries WHERE id=$1 FOR UPDATE`, a.ID).Scan(&status, &token, &attempts, &deadline, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if !errors.Is(sendErr, errMessengerCancelled) {
		// Verification can take longer than the initial rate interval. Start the
		// next interval after the provider attempt, including a stale completion.
		if err = m.extendPacing(ctx, tx, a.Target); err != nil {
			return false, err
		}
	}
	if status != "leased" || token == nil || *token != a.Token || lease == nil || !lease.After(time.Now()) {
		return false, tx.Commit(ctx)
	}
	if errors.Is(sendErr, errMessengerCancelled) {
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='route_inactive' WHERE id=$1`, a.ID)
		if err != nil {
			return false, err
		}
	} else if sendErr == nil {
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='delivered',attempt_token=NULL,lease_until=NULL,completed_at=now() WHERE id=$1`, a.ID)
		if err != nil {
			return false, err
		}
		if a.Kind == "down" || a.Kind == "reminder" {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes r SET reminder_incident_id=$2,next_due=now()+r.reminder_minutes*interval '1 minute' WHERE r.id=$1 AND r.enabled AND r.reminder_minutes>0 AND r.delivery_generation=(SELECT route_generation FROM notifications.messenger_deliveries WHERE id=$3) AND EXISTS(SELECT 1 FROM incidents.incidents i WHERE i.id=$2 AND i.closed_at IS NULL)`, a.RouteID, a.IncidentID, a.ID)
			if err != nil {
				return false, err
			}
		}
	} else {
		var p messengerProviderError
		permanent := errors.As(sendErr, &p) && p.permanent
		if permanent || attempts >= 8 || !deadline.After(time.Now()) {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='provider_failed' WHERE id=$1`, a.ID)
		} else {
			backoff := time.Duration(1<<min(attempts, 6)) * time.Second
			if p.retryAfter > backoff {
				backoff = p.retryAfter
			}
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='pending',attempt_token=NULL,lease_until=NULL,next_attempt_at=now()+$2::interval,error_code='provider_retry' WHERE id=$1`, a.ID, backoff.String())
		}
		if err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

func (m *Messenger) extendPacing(ctx context.Context, tx pgx.Tx, target messengerTarget) error {
	interval := time.Second
	if target.Provider == "max" {
		interval = 500 * time.Millisecond
	} else if target.Kind != "private" {
		interval = 3 * time.Second
	}
	globalInterval := time.Second
	if target.Provider == "max" {
		globalInterval = 500 * time.Millisecond
	}
	for _, rate := range []struct {
		fingerprint []byte
		interval    time.Duration
	}{
		{m.sendFingerprint(target), interval},
		{m.fingerprint(target.Provider, "global"), globalInterval},
	} {
		_, err := tx.Exec(ctx, `INSERT INTO notifications.messenger_target_rates(provider,recipient_fingerprint,next_send_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(provider,recipient_fingerprint) DO UPDATE SET next_send_at=GREATEST(notifications.messenger_target_rates.next_send_at,EXCLUDED.next_send_at)`, target.Provider, rate.fingerprint, rate.interval.String())
		if err != nil {
			return err
		}
	}
	return nil
}

// A provider check runs outside the transaction. Re-read mutable authorization
// and route state immediately before sending, with the same lease as the claim.
func (m *Messenger) deliveryStillEligible(ctx context.Context, a MessengerAttempt) (bool, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	entitlement, err := m.entitlement.Lock(ctx, tx, a.AccountID)
	if err != nil {
		return false, err
	}
	var active, monitorEnabled, routeEnabled, ready bool
	var outcome string
	var sequence, generation, targetVersion int64
	var intervalMinutes, reminderMinutes, rank int
	var lastChecked, lease *time.Time
	var reminderIncident, routeDestination, token *string
	var status string
	err = tx.QueryRow(ctx, `SELECT status='active' FROM accounts.accounts WHERE id=$1 FOR SHARE`, a.AccountID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `SELECT enabled,outcome,sequence,interval_minutes,last_checked_at FROM monitors.monitors WHERE id=$1 AND account_id=$2 FOR SHARE`, a.MonitorID, a.AccountID).Scan(&monitorEnabled, &outcome, &sequence, &intervalMinutes, &lastChecked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM monitors.monitors m WHERE m.account_id=$1 AND (m.created_at,m.id)<=(SELECT created_at,id FROM monitors.monitors WHERE id=$2)`, a.AccountID, a.MonitorID).Scan(&rank)
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `SELECT status='ready',version FROM notifications.messenger_destinations WHERE id=$1 AND account_id=$2 FOR SHARE`, a.DestinationID, a.AccountID).Scan(&ready, &targetVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `SELECT enabled,delivery_generation,destination_id::text,reminder_minutes,reminder_incident_id::text FROM notifications.messenger_routes WHERE id=$1 AND account_id=$2 FOR SHARE`, a.RouteID, a.AccountID).Scan(&routeEnabled, &generation, &routeDestination, &reminderMinutes, &reminderIncident)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `SELECT status,attempt_token::text,lease_until FROM notifications.messenger_deliveries WHERE id=$1 FOR SHARE`, a.ID).Scan(&status, &token, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	valid := active && monitorEnabled && entitlement.Quota >= rank && rank > 0 && ready && targetVersion == a.TargetVersion && routeEnabled && generation == a.Generation && routeDestination != nil && *routeDestination == a.DestinationID && status == "leased" && token != nil && *token == a.Token && lease != nil && lease.After(time.Now()) && sequence >= a.Snapshot.Sequence
	if a.Kind == "reminder" {
		var open bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incidents.incidents WHERE id=$1 AND monitor_id=$2 AND closed_at IS NULL)`, a.IncidentID, a.MonitorID).Scan(&open)
		if err != nil {
			return false, err
		}
		fresh := lastChecked != nil && time.Since(*lastChecked) <= time.Duration(2*intervalMinutes)*time.Minute+30*time.Second
		valid = valid && reminderMinutes > 0 && reminderIncident != nil && *reminderIncident == a.IncidentID && open && outcome == "DOWN" && fresh
	}
	return valid, tx.Commit(ctx)
}

func (m *Messenger) disconnectIfCurrent(ctx context.Context, a MessengerAttempt) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var version int64
	err = tx.QueryRow(ctx, `SELECT version FROM notifications.messenger_destinations WHERE id=$1 AND account_id=$2 FOR NO KEY UPDATE`, a.DestinationID, a.AccountID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && version != a.TargetVersion {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_destinations SET status='disconnected',version=version+1,updated_at=now() WHERE id=$1`, a.DestinationID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET enabled=false,settings_version=settings_version+1,delivery_generation=delivery_generation+1,reminder_incident_id=NULL,next_due=NULL WHERE destination_id=$1`, a.DestinationID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='destination_disconnected' WHERE destination_id=$1 AND status IN ('pending','leased')`, a.DestinationID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Messenger) ProcessDelivery(ctx context.Context) (bool, error) {
	if !m.cfg.Enabled {
		return false, nil
	}
	if m.client == nil {
		return false, ErrMessengerUnavailable
	}
	a, err := m.ClaimDelivery(ctx)
	if err != nil || a == nil {
		return false, err
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if a.Target.Provider == "telegram" && a.Target.Kind != "private" {
		_, err = m.verifyTarget(attemptCtx, a.Target)
		if err != nil {
			var providerErr messengerProviderError
			if errors.As(err, &providerErr) && providerErr.permanent {
				if disconnectErr := m.disconnectIfCurrent(ctx, *a); disconnectErr != nil {
					return true, disconnectErr
				}
			}
			_, completeErr := m.CompleteDelivery(ctx, *a, err)
			return true, completeErr
		}
	}
	eligible, err := m.deliveryStillEligible(attemptCtx, *a)
	if err != nil {
		return true, err
	}
	if !eligible {
		_, err = m.CompleteDelivery(ctx, *a, errMessengerCancelled)
		return true, err
	}
	text, buttons := messengerMessage(a.Snapshot)
	sendErr := m.sendTarget(attemptCtx, a.Target, text, buttons)
	var providerErr messengerProviderError
	if a.Target.Provider == "telegram" && a.Target.Kind != "private" && errors.As(sendErr, &providerErr) && (providerErr.statusCode == 403 || providerErr.statusCode == 404 || providerErr.rebind) {
		if disconnectErr := m.disconnectIfCurrent(ctx, *a); disconnectErr != nil {
			return true, disconnectErr
		}
	}
	_, err = m.CompleteDelivery(ctx, *a, sendErr)
	return true, err
}
