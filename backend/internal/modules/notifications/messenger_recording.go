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

type messengerSnapshot struct {
	TimeZone      string     `json:"timeZone,omitempty"`
	ProviderNames []string   `json:"providerNames,omitempty"`
	CheckType     string     `json:"checkType,omitempty"`
	Host          string     `json:"host,omitempty"`
	Port          int        `json:"port,omitempty"`
	RoundTripMS   *int64     `json:"roundTripMs,omitempty"`
	MonitorID     string     `json:"monitorId"`
	IncidentID    string     `json:"incidentId"`
	Number        int64      `json:"number"`
	Kind          string     `json:"kind"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	Outcome       string     `json:"outcome"`
	HTTPStatus    int        `json:"httpStatus"`
	ErrorCode     string     `json:"errorCode"`
	LatencyMS     int64      `json:"latencyMs"`
	CheckedAt     time.Time  `json:"checkedAt"`
	OpenedAt      time.Time  `json:"openedAt"`
	ConfirmedAt   time.Time  `json:"confirmedAt"`
	ClosedAt      *time.Time `json:"closedAt,omitempty"`
	Sequence      int64      `json:"sequence"`
}

func (m *Messenger) Enqueue(ctx context.Context, tx pgx.Tx, n incidentcontracts.Notification) error {
	kind := "down"
	if n.Kind == "incident_closed" {
		kind = "up"
	} else if n.Kind != "incident_opened" {
		return errors.New("unsupported messenger incident kind")
	}
	if kind == "up" {
		if err := m.CancelReminders(ctx, tx, n.MonitorID); err != nil {
			return err
		}
	}
	return m.enqueue(ctx, tx, n.AccountID, n.MonitorID, n.Incident, n.EventID, n.Sequence, kind, n.Observation)
}

func (m *Messenger) ObserveDown(ctx context.Context, tx pgx.Tx, monitorID, accountID string, incident incidentcontracts.IncidentState, o incidentcontracts.Observation) error {
	// A stale/UNKNOWN or exhausted reminder leaves next_due empty. Resume from
	// this newly accepted DOWN. reminder_incident_id is set only by a successful
	// initial send or an explicit route edit during the open incident.
	// This never catches up missed intervals or overtakes a pending delivery.
	_, err := tx.Exec(ctx, `UPDATE notifications.messenger_routes r SET next_due=now()+r.reminder_minutes*interval '1 minute' WHERE r.monitor_id=$1 AND r.account_id=$2 AND r.enabled AND r.reminder_minutes>0 AND r.reminder_incident_id=$3 AND r.next_due IS NULL AND NOT EXISTS(SELECT 1 FROM notifications.messenger_deliveries d WHERE d.route_id=r.id AND d.incident_id=$3 AND d.kind IN ('down','reminder') AND d.status IN ('pending','leased'))`, monitorID, accountID, incident.ID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,reminder_minutes FROM notifications.messenger_routes WHERE monitor_id=$1 AND account_id=$2 AND enabled AND reminder_minutes>0 AND reminder_incident_id=$3 AND next_due<=now() FOR UPDATE`, monitorID, accountID, incident.ID)
	if err != nil {
		return err
	}
	type due struct {
		id      string
		minutes int
	}
	items := []due{}
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.id, &d.minutes); err != nil {
			rows.Close()
			return err
		}
		items = append(items, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range items {
		var pending bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications.messenger_deliveries WHERE route_id=$1 AND incident_id=$2 AND kind IN ('down','reminder') AND status IN ('pending','leased'))`, d.id, incident.ID).Scan(&pending)
		if err != nil {
			return err
		}
		if pending {
			continue
		}
		id, err := outbox.ID()
		if err != nil {
			return err
		}
		if err = m.enqueueRoute(ctx, tx, accountID, monitorID, incident, id, o.Sequence, "reminder", o, d.id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET next_due=NULL WHERE id=$1`, d.id)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Messenger) CancelReminders(ctx context.Context, tx pgx.Tx, monitorID string) error {
	_, err := tx.Exec(ctx, `UPDATE notifications.messenger_routes SET reminder_incident_id=NULL,next_due=NULL WHERE monitor_id=$1`, monitorID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='incident_closed' WHERE monitor_id=$1 AND kind='reminder' AND status IN ('pending','leased')`, monitorID)
	return err
}
func (m *Messenger) CancelForReset(ctx context.Context, tx pgx.Tx, monitorID string) error {
	_, err := tx.Exec(ctx, `UPDATE notifications.messenger_routes SET reminder_incident_id=NULL,next_due=NULL WHERE monitor_id=$1`, monitorID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='monitor_reset' WHERE monitor_id=$1 AND status IN ('pending','leased')`, monitorID)
	return err
}

func (m *Messenger) enqueue(ctx context.Context, tx pgx.Tx, accountID, monitorID string, incident incidentcontracts.IncidentState, eventID string, sequence int64, kind string, o incidentcontracts.Observation) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM notifications.messenger_routes WHERE monitor_id=$1 AND account_id=$2 AND enabled AND (provider='email' AND $3 OR provider IN ('telegram','max') AND $4) ORDER BY id`, monitorID, accountID, m.cfg.EmailEnabled, m.cfg.Enabled)
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
	for _, id := range ids {
		if err = m.enqueueRoute(ctx, tx, accountID, monitorID, incident, eventID, sequence, kind, o, id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Messenger) enqueueRoute(ctx context.Context, tx pgx.Tx, accountID, monitorID string, incident incidentcontracts.IncidentState, eventID string, sequence int64, kind string, o incidentcontracts.Observation, routeID string) error {
	var destinationID, provider string
	var generation int64
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT destination_id::text,delivery_generation,enabled,provider FROM notifications.messenger_routes WHERE id=$1 FOR UPDATE`, routeID).Scan(&destinationID, &generation, &enabled, &provider)
	if err != nil {
		return err
	}
	if !enabled || !m.providerEnabled(provider) {
		return nil
	}
	var snapshot messengerSnapshot
	snapshot.MonitorID, snapshot.IncidentID, snapshot.Kind = monitorID, incident.ID, kind
	snapshot.Name, snapshot.URL, snapshot.Outcome = o.Name, o.URL, o.Outcome
	snapshot.TimeZone, snapshot.ProviderNames = o.TimeZone, o.ProviderNames
	snapshot.CheckType, snapshot.Host, snapshot.Port, snapshot.RoundTripMS = o.CheckType, o.Host, o.Port, o.RoundTripMS
	snapshot.HTTPStatus, snapshot.ErrorCode, snapshot.LatencyMS = o.HTTPStatus, o.ErrorCode, o.LatencyMS
	snapshot.CheckedAt, snapshot.Sequence = o.CheckedAt, sequence
	snapshot.Number, snapshot.OpenedAt, snapshot.ConfirmedAt, snapshot.ClosedAt = incident.Number, incident.OpenedAt, incident.ConfirmedAt, incident.ClosedAt
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	id, err := outbox.ID()
	if err != nil {
		return err
	}
	encrypted, err := m.seal(accountID, id, "delivery", body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_deliveries(id,event_id,account_id,monitor_id,route_id,destination_id,incident_id,route_generation,sequence,kind,encrypted_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(event_id,route_id) DO NOTHING`, id, eventID, accountID, monitorID, routeID, destinationID, incident.ID, generation, sequence, kind, encrypted)
	return err
}
