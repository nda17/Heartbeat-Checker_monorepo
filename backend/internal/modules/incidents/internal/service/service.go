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

type Service struct {
	pool     *pgxpool.Pool
	recorder contracts.Recorder
}

func New(pool *pgxpool.Pool, recorder contracts.Recorder) *Service {
	return &Service{pool: pool, recorder: recorder}
}

// Apply is invoked in the accepted-result transaction while that monitor is locked.
// Sequence prevents duplicate delivery from counting as a second failed check.
func (s *Service) Apply(ctx context.Context, tx pgx.Tx, monitorID, accountID string, seq int64, outcome string, threshold int, checked time.Time, observation contracts.Observation) error {
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
				if err = s.notify(ctx, tx, monitorID, accountID, id, "incident_opened", seq, checked, observation); err != nil {
					return err
				}
			}
		}
		if s.recorder != nil && failures == threshold {
			var incident contracts.IncidentState
			err = tx.QueryRow(ctx, `SELECT id::text,number,opened_at,confirmed_at,closed_at FROM incidents.incidents WHERE monitor_id=$1 AND closed_at IS NULL`, monitorID).Scan(&incident.ID, &incident.Number, &incident.OpenedAt, &incident.ConfirmedAt, &incident.ClosedAt)
			if err != nil {
				return err
			}
			if err = s.recorder.ObserveDown(ctx, tx, monitorID, accountID, incident, observation); err != nil {
				return err
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
			if err = s.notify(ctx, tx, monitorID, accountID, id, "incident_closed", seq, checked, observation); err != nil {
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
func (s *Service) notify(ctx context.Context, tx pgx.Tx, monitorID, accountID, incidentID, kind string, sequence int64, at time.Time, observation contracts.Observation) error {
	id, err := outbox.ID()
	if err != nil {
		return err
	}
	if err := outbox.Append(ctx, tx, id, monitorID, accountID, "NotificationRequested", map[string]any{"incidentId": incidentID, "kind": kind, "at": at, "sequence": sequence}, false); err != nil {
		return err
	}
	if s.recorder != nil {
		var incident contracts.IncidentState
		if err := tx.QueryRow(ctx, `SELECT id::text,number,opened_at,confirmed_at,closed_at FROM incidents.incidents WHERE id=$1`, incidentID).Scan(&incident.ID, &incident.Number, &incident.OpenedAt, &incident.ConfirmedAt, &incident.ClosedAt); err != nil {
			return err
		}
		if err := s.recorder.Enqueue(ctx, tx, contracts.Notification{EventID: id, AccountID: accountID, MonitorID: monitorID, IncidentID: incidentID, Kind: kind, Sequence: sequence, At: at, Observation: observation, Incident: incident}); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE outbox.events SET processed_at=now() WHERE id=$1`, id)
	return err
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
	if s.recorder != nil {
		if err := s.recorder.CancelForReset(ctx, tx, id); err != nil {
			return err
		}
	}
	// The caller holds the monitor lock. Closing silently prevents a later UP for
	// changed settings from generating recovery for an earlier check configuration.
	_, err := tx.Exec(ctx, `UPDATE incidents.incidents SET closed_at=GREATEST(now(),confirmed_at),close_reason=$2 WHERE monitor_id=$1 AND closed_at IS NULL`, id, reason)
	return err
}
func (s *Service) List(ctx context.Context, accountID, monitorID string) ([]monitorcontracts.Incident, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,number,opened_at,confirmed_at,closed_at,close_reason FROM incidents.incidents WHERE account_id=$1 AND monitor_id=$2 ORDER BY opened_at DESC,id DESC LIMIT 100`, accountID, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []monitorcontracts.Incident{}
	for rows.Next() {
		var i monitorcontracts.Incident
		if err = rows.Scan(&i.ID, &i.Number, &i.OpenedAt, &i.ConfirmedAt, &i.ClosedAt, &i.CloseReason); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
func (s *Service) Get(ctx context.Context, accountID, monitorID, incidentID string) (monitorcontracts.Incident, error) {
	var i monitorcontracts.Incident
	err := s.pool.QueryRow(ctx, `SELECT id::text,number,opened_at,confirmed_at,closed_at,close_reason FROM incidents.incidents WHERE account_id=$1 AND monitor_id=$2 AND id=$3`, accountID, monitorID, incidentID).Scan(&i.ID, &i.Number, &i.OpenedAt, &i.ConfirmedAt, &i.ClosedAt, &i.CloseReason)
	return i, err
}
func (s *Service) Cleanup(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM incidents.incidents WHERE id IN (SELECT id FROM incidents.incidents WHERE closed_at < now()-interval '90 days' ORDER BY closed_at LIMIT 500)`)
	return err
}

// Feed uses the caller's account transaction and returns one extra keyset row.
// A backdated incident confirmed after AsOf cannot enter a later feed page.
func (s *Service) Feed(ctx context.Context, tx pgx.Tx, accountID string, options contracts.FeedOptions) ([]contracts.FeedIncident, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,number,monitor_id::text,opened_at,confirmed_at,
 CASE WHEN closed_at <= $2 THEN closed_at END,
 CASE WHEN closed_at <= $2 THEN close_reason ELSE '' END
 FROM incidents.incidents WHERE account_id=$1 AND confirmed_at <= $2
 AND (closed_at IS NULL OR closed_at >= $2-interval '90 days')
 AND ($3='all' OR $3='active' AND (closed_at IS NULL OR closed_at>$2) OR $3='closed' AND closed_at <= $2)
 AND ($4::timestamptz IS NULL OR (opened_at,id)<($4::timestamptz,NULLIF($5,'')::uuid))
 ORDER BY opened_at DESC,id DESC LIMIT $6`, accountID, options.AsOf, options.State, options.BeforeOpenedAt, options.BeforeID, options.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []contracts.FeedIncident{}
	for rows.Next() {
		var i contracts.FeedIncident
		if err := rows.Scan(&i.ID, &i.Number, &i.MonitorID, &i.OpenedAt, &i.ConfirmedAt, &i.ClosedAt, &i.CloseReason); err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	return result, rows.Err()
}
