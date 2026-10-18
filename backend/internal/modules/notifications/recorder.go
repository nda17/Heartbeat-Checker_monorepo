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

type Recorder struct{}

func NewRecorder() Recorder { return Recorder{} }

func (Recorder) Enqueue(ctx context.Context, tx pgx.Tx, n incidentcontracts.Notification) error {
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
