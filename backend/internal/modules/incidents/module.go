package incidents

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/internal/service"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"time"
)

type Module struct{ service *service.Service }

func New(pool *pgxpool.Pool, recorder ...contracts.Recorder) *Module {
	var selected contracts.Recorder
	if len(recorder) > 0 {
		selected = recorder[0]
	}
	return &Module{service.New(pool, selected)}
}
func (m *Module) Apply(ctx context.Context, tx pgx.Tx, monitorID, accountID string, seq int64, outcome string, threshold int, checked time.Time) error {
	return m.service.Apply(ctx, tx, monitorID, accountID, seq, outcome, threshold, checked)
}
func (m *Module) Reset(ctx context.Context, tx pgx.Tx, id, reason string) error {
	return m.service.Reset(ctx, tx, id, reason)
}
func (m *Module) List(ctx context.Context, accountID, monitorID string) ([]monitorcontracts.Incident, error) {
	return m.service.List(ctx, accountID, monitorID)
}
func (m *Module) Cleanup(ctx context.Context, tx pgx.Tx) error { return m.service.Cleanup(ctx, tx) }
