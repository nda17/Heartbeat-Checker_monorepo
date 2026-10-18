package contracts

import (
	"context"
	"github.com/jackc/pgx/v5"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"time"
)

type Service interface {
	Apply(context.Context, pgx.Tx, string, string, int64, string, int, time.Time) error
	Reset(context.Context, pgx.Tx, string, string) error
	List(context.Context, string, string) ([]monitorcontracts.Incident, error)
}

type Notification struct {
	EventID    string
	AccountID  string
	MonitorID  string
	IncidentID string
	Kind       string
	Sequence   int64
	At         time.Time
}

type Recorder interface {
	Enqueue(context.Context, pgx.Tx, Notification) error
}

const (
	CloseRecovered     = "recovered"
	CloseConfigChanged = "config_changed"
	ClosePaused        = "paused"
	CloseDeleted       = "deleted"
)
