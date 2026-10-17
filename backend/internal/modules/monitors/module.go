package monitors

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	billing "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	incidents "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/internal/service"
	monitorhttp "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/internal/transport/http"
	probe "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
	"net/http"
)

type Module struct {
	service *service.Service
	server  *monitorhttp.Server
}

func New(pool *pgxpool.Pool, billing billing.Service, incidents incidents.Service, accounts accounts.ActiveGuard) *Module {
	s := service.New(postgres.New(pool), billing, incidents, accounts)
	return &Module{s, monitorhttp.New(s)}
}
func (m *Module) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	m.server.Register(mux, protect)
}
func (m *Module) Claim(ctx context.Context) (*contracts.Attempt, error) { return m.service.Claim(ctx) }
func (m *Module) Complete(ctx context.Context, a contracts.Attempt, r probe.Result) (bool, error) {
	return m.service.Complete(ctx, a, r)
}
func (m *Module) Cleanup(ctx context.Context, tx pgx.Tx) error { return m.service.Cleanup(ctx, tx) }
