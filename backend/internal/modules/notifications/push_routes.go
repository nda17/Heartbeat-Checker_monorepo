package notifications

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// EnabledChannelsTx resolves the monitor policy and requires at least one
// installation ready under its original web session. The caller holds billing.Lock.
func (p *Push) EnabledChannelsTx(ctx context.Context, tx pgx.Tx, accountID string, monitorIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(monitorIDs))
	for _, id := range monitorIDs {
		if !messengerValidID(id) {
			return nil, ErrPushInvalid
		}
		result[id] = []string{}
	}
	if !p.cfg.Enabled || len(monitorIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT i.id::text,COALESCE(i.session_id::text,'') FROM notifications.push_installations i WHERE i.account_id=$1 AND i.status='ready' AND i.permission='granted' AND i.token_valid AND i.provider_available AND i.encrypted_token IS NOT NULL ORDER BY i.id::text`, accountID)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, session string }
	items := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.session); err != nil {
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
	eligible := []candidate{}
	for _, c := range items {
		if _, err = p.sessionLease(ctx, tx, accountID, c.session); errors.Is(err, ErrPushReconnect) {
			continue
		} else if err != nil {
			return nil, err
		}
		eligible = append(eligible, c)
	}
	// Finish every auth guard before taking the first installation lock.
	ready := []string{}
	for _, c := range eligible {
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text FROM notifications.push_installations WHERE id=$1 AND account_id=$2 AND session_id=NULLIF($3,'')::uuid AND status='ready' AND permission='granted' AND token_valid AND provider_available AND encrypted_token IS NOT NULL FOR SHARE`, c.id, accountID, c.session).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return nil, err
		}
		ready = append(ready, id)
	}
	if len(ready) == 0 {
		return result, nil
	}
	rows, err = tx.Query(ctx, `SELECT m.id::text FROM monitors.monitors m LEFT JOIN notifications.push_monitor_settings s ON s.monitor_id=m.id AND s.account_id=m.account_id LEFT JOIN notifications.push_account_settings a ON a.account_id=m.account_id WHERE m.account_id=$1 AND m.id=ANY($2::uuid[]) AND COALESCE(s.enabled,a.default_enabled,false)`, accountID, monitorIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = []string{"push"}
	}
	return result, rows.Err()
}

func (p *Push) ListInstallations(ctx context.Context, accountID string) ([]PushInstallationStatus, error) {
	result := []PushInstallationStatus{}
	if !p.cfg.Enabled {
		return result, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE account_id=$1 AND status<>'revoked' ORDER BY created_at,id`, accountID)
	if err != nil {
		return nil, err
	}
	items := []pushInstallation{}
	for rows.Next() {
		i, e := scanPushInstallation(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, i := range items {
		tx, e := p.pool.Begin(ctx)
		if e != nil {
			return nil, e
		}
		expiry, e := p.sessionLease(ctx, tx, accountID, i.SessionID)
		status := PushInstallationStatus{ID: i.ID, Provider: "rustore", DeviceLabel: i.Label, BindingGeneration: i.Generation, RegistrationVersion: i.Version, LastPairingID: pushOptionalID(i.LastPairingID), LastSeenAt: i.LastSeenAt, Status: "ready"}
		if errors.Is(e, ErrPushReconnect) {
			status.Status = pushSessionStatus(e)
		} else if e != nil {
			tx.Rollback(ctx)
			return nil, e
		} else {
			current, readErr := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 AND account_id=$2 AND status<>'revoked' FOR SHARE`, i.ID, accountID))
			if errors.Is(readErr, pgx.ErrNoRows) {
				tx.Rollback(ctx)
				continue
			}
			if readErr != nil {
				tx.Rollback(ctx)
				return nil, readErr
			}
			if current.Generation != i.Generation || current.SessionID != i.SessionID {
				tx.Rollback(ctx)
				continue
			}
			status.LeaseExpiresAt = &expiry
			switch {
			case current.Status == "pending":
				status.Status = "pending"
			case current.Permission != "granted":
				status.Status = "permission_denied"
			case !current.TokenValid:
				status.Status = "invalid_token"
			case !current.ProviderAvailable:
				status.Status = "provider_unavailable"
			}
		}
		tx.Rollback(ctx)
		result = append(result, status)
	}
	return result, nil
}

func (p *Push) MonitorPush(ctx context.Context, accountID, monitorID string) (PushMonitorSettings, error) {
	if err := pushContextID(monitorID); err != nil {
		return PushMonitorSettings{}, err
	}
	var result PushMonitorSettings
	err := p.pool.QueryRow(ctx, `SELECT COALESCE(s.enabled,a.default_enabled,false),COALESCE(s.settings_version,0) FROM monitors.monitors m LEFT JOIN notifications.push_monitor_settings s ON s.monitor_id=m.id AND s.account_id=m.account_id LEFT JOIN notifications.push_account_settings a ON a.account_id=m.account_id WHERE m.id=$1 AND m.account_id=$2`, monitorID, accountID).Scan(&result.Enabled, &result.Version)
	return result, pushError(err)
}

func (p *Push) PutMonitorPush(ctx context.Context, accountID, sessionID, monitorID string, in PutPushMonitorSettings) (PushMonitorSettings, error) {
	if err := p.enabled(); err != nil {
		return PushMonitorSettings{}, err
	}
	if !messengerValidID(monitorID) || in.Version < 0 {
		return PushMonitorSettings{}, ErrPushInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushMonitorSettings{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = p.entitlement.Lock(ctx, tx, accountID); err != nil {
		return PushMonitorSettings{}, err
	}
	if _, err = p.sessionLease(ctx, tx, accountID, sessionID); err != nil {
		return PushMonitorSettings{}, err
	}
	var monitor string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM monitors.monitors WHERE id=$1 AND account_id=$2 FOR UPDATE`, monitorID, accountID).Scan(&monitor); err != nil {
		return PushMonitorSettings{}, pushError(err)
	}
	var current PushMonitorSettings
	err = tx.QueryRow(ctx, `SELECT COALESCE(s.enabled,a.default_enabled,false),COALESCE(s.settings_version,0) FROM monitors.monitors m LEFT JOIN notifications.push_monitor_settings s ON s.monitor_id=m.id AND s.account_id=m.account_id LEFT JOIN notifications.push_account_settings a ON a.account_id=m.account_id WHERE m.id=$1 AND m.account_id=$2`, monitorID, accountID).Scan(&current.Enabled, &current.Version)
	if err != nil {
		return PushMonitorSettings{}, err
	}
	if current.Version != in.Version {
		return PushMonitorSettings{}, ErrPushConflict
	}
	var version int64
	err = tx.QueryRow(ctx, `INSERT INTO notifications.push_monitor_settings(monitor_id,account_id,enabled) VALUES($1,$2,$3) ON CONFLICT(monitor_id) DO UPDATE SET enabled=EXCLUDED.enabled,settings_version=push_monitor_settings.settings_version+1,delivery_generation=push_monitor_settings.delivery_generation+CASE WHEN push_monitor_settings.enabled<>EXCLUDED.enabled THEN 1 ELSE 0 END,updated_at=now() RETURNING settings_version`, monitorID, accountID, in.Enabled).Scan(&version)
	if err != nil {
		return PushMonitorSettings{}, err
	}
	// The first override changes generation 0 to 1, even if its value matches
	// the inherited default. Existing snapshots must not survive that fence.
	if current.Version == 0 || current.Enabled != in.Enabled {
		_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='policy_changed' WHERE monitor_id=$1 AND account_id=$2 AND status IN ('pending','leased')`, monitorID, accountID)
		if err != nil {
			return PushMonitorSettings{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return PushMonitorSettings{}, err
	}
	return PushMonitorSettings{Enabled: in.Enabled, Version: version}, nil
}
