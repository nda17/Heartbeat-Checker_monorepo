package contracts

import (
	"context"
	"github.com/jackc/pgx/v5"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"time"
)

type Service interface {
	Apply(context.Context, pgx.Tx, string, string, int64, string, int, time.Time, Observation) error
	Reset(context.Context, pgx.Tx, string, string) error
	List(context.Context, string, string) ([]monitorcontracts.Incident, error)
	Get(context.Context, string, string, string) (monitorcontracts.Incident, error)
}

type Notification struct {
	EventID     string
	AccountID   string
	MonitorID   string
	IncidentID  string
	Kind        string
	Sequence    int64
	At          time.Time
	Observation Observation
	Incident    IncidentState
}

type IncidentState struct {
	ID          string
	Number      int64
	OpenedAt    time.Time
	ConfirmedAt time.Time
	ClosedAt    *time.Time
}

// Observation is copied from the accepted check while the monitor is locked.
type Observation struct {
	TimeZone      string
	ProviderNames []string
	CheckType     string
	Name          string
	URL           string
	Host          string
	Port          int
	Outcome       string
	HTTPStatus    int
	ErrorCode     string
	LatencyMS     int64
	RoundTripMS   *int64
	CheckedAt     time.Time
	Sequence      int64
}

type Recorder interface {
	Enqueue(context.Context, pgx.Tx, Notification) error
	ObserveDown(context.Context, pgx.Tx, string, string, IncidentState, Observation) error
	CancelReminders(context.Context, pgx.Tx, string) error
	CancelForReset(context.Context, pgx.Tx, string) error
}

const (
	CloseRecovered     = "recovered"
	CloseConfigChanged = "config_changed"
	ClosePaused        = "paused"
	CloseDeleted       = "deleted"
)
