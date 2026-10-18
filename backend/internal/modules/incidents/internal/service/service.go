package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Apply is invoked in the accepted-result transaction while that monitor is locked.
// Sequence prevents duplicate delivery from counting as a second failed check.
func (s *Service) Apply(ctx context.Context, tx pgx.Tx, monitorID, accountID string, seq int64, outcome string, threshold int, checked time.Time) error {
	if threshold < 1 || threshold > 10 {
		return errors.New("invalid failure threshold")
	}
	_, err := tx.Exec(ctx, `INSERT INTO incidents.monitor_state(monitor_id) VALUES($1) ON CONFLICT DO NOTHING`, monitorID)
	if err != nil {
		return err
	}
	var last int64
	var failures int
	var first *time.Time
	if err = tx.QueryRow(ctx, `SELECT last_sequence,failure_count,first_failure_at FROM incidents.monitor_state WHERE monitor_id=$1 FOR UPDATE`, monitorID).Scan(&last, &failures, &first); err != nil {
		return err
	}
	if seq <= last {
		return nil
	}
	switch outcome {
	case "DOWN":
		if failures == 0 {
			first = &checked
		}
		if failures < threshold {
			failures++
		}
		if failures == threshold {
			id, err := outbox.ID()
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(monitor_id) WHERE closed_at IS NULL DO NOTHING`, id, monitorID, accountID, first, checked)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				if err = s.notify(ctx, tx, monitorID, accountID, id, "incident_opened", checked); err != nil {
					return err
				}
			}
		}
	case "UP":
		failures = 0
		first = nil
		var id string
		err = tx.QueryRow(ctx, `UPDATE incidents.incidents SET closed_at=$2,close_reason='recovered' WHERE monitor_id=$1 AND closed_at IS NULL RETURNING id::text`, monitorID, checked).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if err = s.notify(ctx, tx, monitorID, accountID, id, "incident_closed", checked); err != nil {
				return err
			}
		}
	default:
		failures = 0
		first = nil
	}
	_, err = tx.Exec(ctx, `UPDATE incidents.monitor_state SET last_sequence=$2,failure_count=$3,first_failure_at=$4 WHERE monitor_id=$1`, monitorID, seq, failures, first)
	return err
}
func (s *Service) notify(ctx context.Context, tx pgx.Tx, monitorID, accountID, incidentID, kind string, at time.Time) error {
	id, err := outbox.ID()
	if err != nil {
		return err
	}
	return outbox.Append(ctx, tx, id, monitorID, accountID, "NotificationRequested", map[string]any{"incidentId": incidentID, "kind": kind, "at": at}, false)
}

// Reset separates operational changes from recovery observed by an actual check.
// Empty reason resets only the failure streak, for example when resuming a monitor.
func (s *Service) Reset(ctx context.Context, tx pgx.Tx, id, reason string) error {
	if reason != "" && reason != contracts.CloseConfigChanged && reason != contracts.ClosePaused && reason != contracts.CloseDeleted {
		return errors.New("invalid incident reset reason")
	}
	if _, err := tx.Exec(ctx, `UPDATE incidents.monitor_state SET failure_count=0,first_failure_at=NULL WHERE monitor_id=$1`, id); err != nil {
		return err
	}
	if reason == "" {
		return nil
	}
	// The caller holds the monitor lock. Closing silently prevents a later UP for
	// changed settings from generating recovery for an earlier check configuration.
	_, err := tx.Exec(ctx, `UPDATE incidents.incidents SET closed_at=GREATEST(now(),confirmed_at),close_reason=$2 WHERE monitor_id=$1 AND closed_at IS NULL`, id, reason)
	return err
}
func (s *Service) List(ctx context.Context, accountID, monitorID string) ([]monitorcontracts.Incident, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,opened_at,confirmed_at,closed_at,close_reason FROM incidents.incidents WHERE account_id=$1 AND monitor_id=$2 ORDER BY opened_at DESC,id DESC LIMIT 100`, accountID, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []monitorcontracts.Incident{}
	for rows.Next() {
		var i monitorcontracts.Incident
		if err = rows.Scan(&i.ID, &i.OpenedAt, &i.ConfirmedAt, &i.ClosedAt, &i.CloseReason); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
func (s *Service) Cleanup(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM incidents.incidents WHERE id IN (SELECT id FROM incidents.incidents WHERE closed_at < now()-interval '90 days' ORDER BY closed_at LIMIT 500)`)
	return err
}
