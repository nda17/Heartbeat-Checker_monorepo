package contracts

import (
	"context"
	"github.com/jackc/pgx/v5"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"time"
)

type Service interface {
	Apply(context.Context, pgx.Tx, string, string, int64, string, time.Time) error
	Reset(context.Context, pgx.Tx, string, string) error
	List(context.Context, string, string) ([]monitorcontracts.Incident, error)
}

const (
	CloseRecovered     = "recovered"
	CloseConfigChanged = "config_changed"
	ClosePaused        = "paused"
	CloseDeleted       = "deleted"
)
