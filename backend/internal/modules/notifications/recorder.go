package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	incidentcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type Recorder struct {
	messenger *Messenger
	push      *Push
}

func (r Recorder) WithPush(push *Push) Recorder { r.push = push; return r }

func NewRecorder(messenger ...*Messenger) Recorder {
	if len(messenger) > 0 {
		return Recorder{messenger: messenger[0]}
	}
	return Recorder{}
}

func (r Recorder) Enqueue(ctx context.Context, tx pgx.Tx, n incidentcontracts.Notification) error {
	if err := enqueueWebhook(ctx, tx, n); err != nil {
		return err
	}
	if r.messenger != nil && (r.messenger.cfg.Enabled || r.messenger.cfg.EmailEnabled) {
		if err := r.messenger.Enqueue(ctx, tx, n); err != nil {
			return err
		}
	}
	if r.push != nil {
		return r.push.Enqueue(ctx, tx, n)
	}
	return nil
}

func (r Recorder) ObserveDown(ctx context.Context, tx pgx.Tx, monitorID, accountID string, incident incidentcontracts.IncidentState, o incidentcontracts.Observation) error {
	if r.messenger != nil && (r.messenger.cfg.Enabled || r.messenger.cfg.EmailEnabled) {
		return r.messenger.ObserveDown(ctx, tx, monitorID, accountID, incident, o)
	}
	return nil
}
func (r Recorder) CancelReminders(ctx context.Context, tx pgx.Tx, monitorID string) error {
	if r.messenger != nil && (r.messenger.cfg.Enabled || r.messenger.cfg.EmailEnabled) {
		return r.messenger.CancelReminders(ctx, tx, monitorID)
	}
	return nil
}
func (r Recorder) CancelForReset(ctx context.Context, tx pgx.Tx, monitorID string) error {
	if r.messenger != nil && (r.messenger.cfg.Enabled || r.messenger.cfg.EmailEnabled) {
		if err := r.messenger.CancelForReset(ctx, tx, monitorID); err != nil {
			return err
		}
	}
	if r.push != nil {
		return r.push.CancelForReset(ctx, tx, monitorID)
	}
	return nil
}

func enqueueWebhook(ctx context.Context, tx pgx.Tx, n incidentcontracts.Notification) error {
	var kind string
	switch n.Kind {
	case "incident_opened":
		kind = "monitor.down"
	case "incident_closed":
		kind = "monitor.up"
	default:
		return errors.New("unsupported notification kind")
	}
	var endpointID string
	var version int64
	err := tx.QueryRow(ctx, `SELECT id::text,version FROM notifications.webhooks WHERE monitor_id=$1 AND account_id=$2 AND enabled`, n.MonitorID, n.AccountID).Scan(&endpointID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		SchemaVersion int       `json:"schemaVersion"`
		ID            string    `json:"id"`
		Type          string    `json:"type"`
		OccurredAt    time.Time `json:"occurredAt"`
		MonitorID     string    `json:"monitorId"`
		IncidentID    string    `json:"incidentId"`
		Sequence      int64     `json:"sequence"`
	}{1, n.EventID, kind, n.At.UTC(), n.MonitorID, n.IncidentID, n.Sequence})
	if err != nil {
		return err
	}
	id, err := outbox.ID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications.deliveries(id,event_id,account_id,monitor_id,endpoint_id,endpoint_version,sequence,body,status,deadline_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',now()+interval '24 hours') ON CONFLICT(event_id,endpoint_id) DO NOTHING`,
		id, n.EventID, n.AccountID, n.MonitorID, endpointID, version, n.Sequence, body)
	return err
}
