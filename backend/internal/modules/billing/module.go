package billing

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/service"
	"time"
)

type Module struct{ service *service.Service }

func New(pool *pgxpool.Pool, local bool) *Module { return &Module{service: service.New(pool, local)} }
func (m *Module) Lock(ctx context.Context, tx pgx.Tx, accountID string) (contracts.Entitlement, error) {
	return m.service.Lock(ctx, tx, accountID)
}
func (m *Module) GrantLocal(ctx context.Context, accountID string, quota int, until time.Time, reason string) error {
	return m.service.GrantLocal(ctx, accountID, quota, until, reason)
}
func (m *Module) GrantLocalTx(ctx context.Context, tx pgx.Tx, accountID string, quota int, until time.Time, reason string) error {
	return m.service.GrantLocalTx(ctx, tx, accountID, quota, until, reason)
}
