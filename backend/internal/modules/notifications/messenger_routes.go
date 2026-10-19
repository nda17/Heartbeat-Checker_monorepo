package notifications

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

func reminderInterval(n int) bool {
	switch n {
	case 0, 15, 30, 60, 180, 360, 1440:
		return true
	}
	return false
}

func (m *Messenger) ListRoutes(ctx context.Context, accountID, monitorID string) ([]MessengerRoute, error) {
	if err := m.enabled(); err != nil {
		return nil, err
	}
	if !messengerValidID(monitorID) {
		return nil, ErrMessengerNotFound
	}
	var owned bool
	err := m.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM monitors.monitors WHERE id=$1 AND account_id=$2)`, monitorID, accountID).Scan(&owned)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrMessengerNotFound
	}
	rows, err := m.pool.Query(ctx, `SELECT provider,destination_id::text,enabled,reminder_minutes,settings_version FROM notifications.messenger_routes WHERE monitor_id=$1 AND account_id=$2 ORDER BY provider`, monitorID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MessengerRoute{}
	for rows.Next() {
		var r MessengerRoute
		if err = rows.Scan(&r.Provider, &r.DestinationID, &r.Enabled, &r.ReminderMinutes, &r.Version); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

func (m *Messenger) PutRoute(ctx context.Context, accountID, monitorID, provider string, in PutMessengerRoute) (MessengerRoute, error) {
	if err := m.enabled(); err != nil {
		return MessengerRoute{}, err
	}
	if !messengerValidID(monitorID) || !messengerValidID(in.DestinationID) || !m.providerEnabled(provider) || !reminderInterval(in.ReminderMinutes) || in.Version < 0 {
		return MessengerRoute{}, ErrMessengerInvalid
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return MessengerRoute{}, err
	}
	defer tx.Rollback(ctx)
	active, err := m.accountActive(ctx, tx, accountID)
	if err != nil {
		return MessengerRoute{}, err
	}
	if !active {
		return MessengerRoute{}, ErrMessengerNotFound
	}
	var currentEmail string
	if provider == "email" && in.Enabled {
		currentEmail, err = m.cfg.EmailAddressReader(ctx, tx, accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return MessengerRoute{}, ErrMessengerNotFound
		}
		if err != nil {
			return MessengerRoute{}, err
		}
	}
	var monitorOutcome string
	err = tx.QueryRow(ctx, `SELECT outcome FROM monitors.monitors WHERE id=$1 AND account_id=$2 FOR UPDATE`, monitorID, accountID).Scan(&monitorOutcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return MessengerRoute{}, ErrMessengerNotFound
	}
	if err != nil {
		return MessengerRoute{}, err
	}
	var ready bool
	var encryptedTarget []byte
	err = tx.QueryRow(ctx, `SELECT status='ready',encrypted_target FROM notifications.messenger_destinations WHERE id=$1 AND account_id=$2 AND provider=$3 FOR SHARE`, in.DestinationID, accountID, provider).Scan(&ready, &encryptedTarget)
	if errors.Is(err, pgx.ErrNoRows) {
		return MessengerRoute{}, ErrMessengerNotFound
	}
	if err != nil {
		return MessengerRoute{}, err
	}
	if in.Enabled && !ready {
		return MessengerRoute{}, ErrMessengerInvalid
	}
	if provider == "email" && in.Enabled {
		recipient, err := m.readRecipient(accountID, in.DestinationID, "target", encryptedTarget)
		if err != nil {
			return MessengerRoute{}, err
		}
		if currentEmail == "" || recipient.Target.Email != currentEmail {
			return MessengerRoute{}, ErrMessengerNotReady
		}
	}
	var id, currentDestination string
	var enabled bool
	var version int64
	var oldInterval int
	err = tx.QueryRow(ctx, `SELECT id::text,destination_id::text,enabled,settings_version,reminder_minutes FROM notifications.messenger_routes WHERE monitor_id=$1 AND account_id=$2 AND provider=$3 FOR UPDATE`, monitorID, accountID, provider).Scan(&id, &currentDestination, &enabled, &version, &oldInterval)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return MessengerRoute{}, err
	}
	schedule := false
	cancelReminders := false
	if errors.Is(err, pgx.ErrNoRows) {
		if in.Version != 0 {
			return MessengerRoute{}, ErrMessengerConflict
		}
		id, err = outbox.ID()
		if err != nil {
			return MessengerRoute{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_routes(id,account_id,monitor_id,provider,destination_id,enabled,reminder_minutes) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, accountID, monitorID, provider, in.DestinationID, in.Enabled, in.ReminderMinutes)
		if err != nil {
			return MessengerRoute{}, err
		}
		version = 1
		schedule = in.Enabled && in.ReminderMinutes > 0
	} else {
		if version != in.Version {
			return MessengerRoute{}, ErrMessengerConflict
		}
		routingChanged := currentDestination != in.DestinationID || enabled != in.Enabled
		schedule = in.Enabled && in.ReminderMinutes > 0 && (routingChanged || oldInterval != in.ReminderMinutes)
		cancelReminders = oldInterval != in.ReminderMinutes
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET destination_id=$2,enabled=$3,reminder_minutes=$4,settings_version=settings_version+1,delivery_generation=delivery_generation+CASE WHEN $5 THEN 1 ELSE 0 END,reminder_incident_id=CASE WHEN $5 OR $4=0 OR $6 THEN NULL ELSE reminder_incident_id END,next_due=CASE WHEN $5 OR $4=0 OR $6 THEN NULL ELSE next_due END,updated_at=now() WHERE id=$1`, id, in.DestinationID, in.Enabled, in.ReminderMinutes, routingChanged, cancelReminders)
		if err != nil {
			return MessengerRoute{}, err
		}
		if routingChanged {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='route_changed' WHERE route_id=$1 AND status IN ('pending','leased')`, id)
			if err != nil {
				return MessengerRoute{}, err
			}
		}
		version++
	}
	if in.ReminderMinutes == 0 || cancelReminders {
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='reminders_disabled' WHERE route_id=$1 AND kind='reminder' AND status IN ('pending','leased')`, id)
		if err != nil {
			return MessengerRoute{}, err
		}
	}
	if schedule && monitorOutcome == "DOWN" {
		var incidentID string
		err = tx.QueryRow(ctx, `SELECT id::text FROM incidents.incidents WHERE monitor_id=$1 AND account_id=$2 AND closed_at IS NULL`, monitorID, accountID).Scan(&incidentID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return MessengerRoute{}, err
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET reminder_incident_id=$2,next_due=now()+$3*interval '1 minute' WHERE id=$1`, id, incidentID, in.ReminderMinutes)
			if err != nil {
				return MessengerRoute{}, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return MessengerRoute{}, err
	}
	return MessengerRoute{Provider: provider, DestinationID: in.DestinationID, Enabled: in.Enabled, ReminderMinutes: in.ReminderMinutes, Version: version}, nil
}
