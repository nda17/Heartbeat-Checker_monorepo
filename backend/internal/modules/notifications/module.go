package notifications

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	monitors "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/internal/service"
	notificationhttp "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/internal/transport/http"
)

type Module struct {
	service *service.Service
	server  *notificationhttp.Server
}

func New(pool *pgxpool.Pool, key string, owners monitors.OwnershipGuard, accounts accounts.ActiveGuard) (*Module, error) {
	s, err := service.New(pool, key, owners, accounts)
	if err != nil {
		return nil, err
	}
	return &Module{service: s, server: notificationhttp.New(s)}, nil
}
func (m *Module) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	m.server.Register(mux, protect)
}
func (m *Module) Claim(ctx context.Context) (*contracts.Attempt, error) { return m.service.Claim(ctx) }
func (m *Module) Complete(ctx context.Context, a contracts.Attempt, r contracts.Result) (bool, error) {
	return m.service.Complete(ctx, a, r)
}
func (m *Module) Cleanup(ctx context.Context, tx pgx.Tx) error { return m.service.Cleanup(ctx, tx) }
