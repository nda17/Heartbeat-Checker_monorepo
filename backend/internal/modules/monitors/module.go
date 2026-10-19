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
	"time"
)

type Module struct {
	service *service.Service
	server  *monitorhttp.Server
}

var ErrInvalid = service.ErrInvalid
var ErrNotFound = service.ErrNotFound
var ErrConflict = service.ErrConflict
var ErrEntitlement = service.ErrEntitlement
var ErrQuota = service.ErrQuota

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
func (m *Module) List(ctx context.Context, owner string) ([]contracts.Monitor, error) {
	return m.service.List(ctx, owner)
}
func (m *Module) Entitlement(ctx context.Context, owner string) (billing.Entitlement, error) {
	return m.service.Entitlement(ctx, owner)
}
func (m *Module) Get(ctx context.Context, owner, id string) (contracts.Monitor, error) {
	return m.service.Get(ctx, owner, id)
}
func (m *Module) Results(ctx context.Context, owner, id string, limit int, before time.Time) ([]contracts.Result, error) {
	return m.service.Results(ctx, owner, id, limit, before)
}
func (m *Module) Incidents(ctx context.Context, owner, id string) ([]contracts.Incident, error) {
	return m.service.Incidents(ctx, owner, id)
}
func (m *Module) Incident(ctx context.Context, owner, id, incidentID string) (contracts.Incident, error) {
	return m.service.Incident(ctx, owner, id, incidentID)
}
func (m *Module) GetTx(ctx context.Context, tx pgx.Tx, owner, id string, e billing.Entitlement) (contracts.Monitor, error) {
	return m.service.GetTx(ctx, tx, owner, id, e)
}
func (m *Module) CreateTx(ctx context.Context, tx pgx.Tx, owner string, e billing.Entitlement, in contracts.MonitorInput) (contracts.Monitor, error) {
	return m.service.CreateTx(ctx, tx, owner, e, in)
}
func (m *Module) PatchTx(ctx context.Context, tx pgx.Tx, owner, id string, e billing.Entitlement, in contracts.Patch) (contracts.Monitor, error) {
	return m.service.PatchTx(ctx, tx, owner, id, e, in)
}
func (m *Module) DeleteTx(ctx context.Context, tx pgx.Tx, owner, id string) error {
	return m.service.DeleteTx(ctx, tx, owner, id)
}
