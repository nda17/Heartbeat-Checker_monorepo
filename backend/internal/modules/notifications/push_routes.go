package notifications

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

// EnabledChannelsTx returns only configured push routes whose installation
// remains ready under its original web session. The caller holds billing.Lock.
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
	rows, err := tx.Query(ctx, `SELECT DISTINCT i.id::text,COALESCE(i.session_id::text,'') FROM notifications.push_installations i JOIN notifications.push_routes r ON r.installation_id=i.id AND r.account_id=i.account_id WHERE i.account_id=$1 AND r.monitor_id=ANY($2::uuid[]) AND r.enabled AND i.status='ready' AND i.permission='granted' AND i.token_valid AND i.provider_available AND i.encrypted_token IS NOT NULL ORDER BY i.id::text`, accountID, monitorIDs)
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
	rows, err = tx.Query(ctx, `SELECT DISTINCT monitor_id::text FROM notifications.push_routes WHERE account_id=$1 AND monitor_id=ANY($2::uuid[]) AND installation_id=ANY($3::uuid[]) AND enabled`, accountID, monitorIDs, ready)
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

func (p *Push) ListPushRoutes(ctx context.Context, accountID, monitorID string) ([]PushRoute, error) {
	if err := pushContextID(monitorID); err != nil {
		return nil, err
	}
	var owned bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM monitors.monitors WHERE id=$1 AND account_id=$2)`, monitorID, accountID).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrPushNotFound
	}
	result := []PushRoute{}
	if !p.cfg.Enabled {
		return result, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT i.id::text,COALESCE(r.enabled,false),COALESCE(r.settings_version,0),i.status='ready' AND i.permission='granted' AND i.token_valid AND i.provider_available,COALESCE(i.session_id::text,'') FROM notifications.push_installations i LEFT JOIN notifications.push_routes r ON r.installation_id=i.id AND r.monitor_id=$2 AND r.account_id=$1 WHERE i.account_id=$1 AND i.status<>'revoked' ORDER BY i.created_at,i.id`, accountID, monitorID)
	if err != nil {
		return nil, err
	}
	sessions := []string{}
	for rows.Next() {
		var r PushRoute
		var session string
		if err = rows.Scan(&r.InstallationID, &r.Enabled, &r.Version, &r.Ready, &session); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, r)
		sessions = append(sessions, session)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for index := range result {
		if result[index].Ready {
			tx, e := p.pool.Begin(ctx)
			if e != nil {
				return nil, e
			}
			_, e = p.sessionLease(ctx, tx, accountID, sessions[index])
			tx.Rollback(ctx)
			if errors.Is(e, ErrPushReconnect) {
				result[index].Ready = false
			} else if e != nil {
				return nil, e
			}
		}
	}
	return result, nil
}

func (p *Push) PutPushRoute(ctx context.Context, accountID, sessionID, monitorID, installationID string, in PutPushRoute) (PushRoute, error) {
	if err := p.enabled(); err != nil {
		return PushRoute{}, err
	}
	if !messengerValidID(monitorID) || !messengerValidID(installationID) || in.Version < 0 {
		return PushRoute{}, ErrPushInvalid
	}
	var sourceSession string
	if err := p.pool.QueryRow(ctx, `SELECT COALESCE(session_id::text,'') FROM notifications.push_installations WHERE id=$1 AND account_id=$2 AND status<>'revoked'`, installationID, accountID).Scan(&sourceSession); err != nil {
		return PushRoute{}, pushError(err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushRoute{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = p.entitlement.Lock(ctx, tx, accountID); err != nil {
		return PushRoute{}, err
	}
	if _, err = p.sessionLease(ctx, tx, accountID, sessionID); err != nil {
		return PushRoute{}, err
	}
	if in.Enabled {
		if _, err = p.sessionLease(ctx, tx, accountID, sourceSession); err != nil {
			return PushRoute{}, err
		}
	}
	var monitor string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM monitors.monitors WHERE id=$1 AND account_id=$2 FOR UPDATE`, monitorID, accountID).Scan(&monitor); err != nil {
		return PushRoute{}, pushError(err)
	}
	i, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 AND account_id=$2 AND status<>'revoked' FOR UPDATE`, installationID, accountID))
	if err != nil {
		return PushRoute{}, pushError(err)
	}
	if i.SessionID != sourceSession || in.Enabled && (i.Status != "ready" || i.Permission != "granted" || !i.TokenValid) {
		return PushRoute{}, ErrPushReconnect
	}
	var id string
	var version int64
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT id::text,settings_version,enabled FROM notifications.push_routes WHERE monitor_id=$1 AND installation_id=$2 AND account_id=$3 FOR UPDATE`, monitorID, installationID, accountID).Scan(&id, &version, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.Version != 0 {
			return PushRoute{}, ErrPushConflict
		}
		id, err = outbox.ID()
		if err != nil {
			return PushRoute{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications.push_routes(id,account_id,monitor_id,installation_id,enabled) VALUES($1,$2,$3,$4,$5)`, id, accountID, monitorID, installationID, in.Enabled)
		version = 1
	} else if err == nil {
		if in.Version != version {
			return PushRoute{}, ErrPushConflict
		}
		_, err = tx.Exec(ctx, `UPDATE notifications.push_routes SET enabled=$2,settings_version=settings_version+1,delivery_generation=delivery_generation+CASE WHEN enabled<>$2 THEN 1 ELSE 0 END,updated_at=now() WHERE id=$1`, id, in.Enabled)
		version++
		if err == nil && enabled != in.Enabled {
			_, err = tx.Exec(ctx, `UPDATE notifications.push_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='route_changed' WHERE route_id=$1 AND status IN ('pending','leased')`, id)
		}
	}
	if err != nil {
		return PushRoute{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PushRoute{}, err
	}
	return PushRoute{InstallationID: installationID, Enabled: in.Enabled, Ready: i.Status == "ready" && i.Permission == "granted" && i.TokenValid, Version: version}, nil
}
