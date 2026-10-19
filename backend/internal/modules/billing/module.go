package billing

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/adapters/yookassa"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/service"
	billinghttp "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/transport/http"
	"net/http"
	"time"
)

type Module struct{ service *service.Service }

var ErrAdminGrant = service.ErrAdminGrant
var ErrAdminGrantConflict = service.ErrAdminGrantConflict

func New(pool *pgxpool.Pool, local bool) *Module { return &Module{service: service.New(pool, local)} }

type Config struct {
	Provider        string
	MockAccountIDs  []string
	TestAccountIDs  []string
	Mode            string
	ShopID          string
	SecretKey       string
	PaymentProvider contracts.Provider
}

func NewConfigured(pool *pgxpool.Pool, local bool, cfg Config, guard accounts.ActiveGuard) (*Module, error) {
	s := service.New(pool, local)
	if err := s.Configure(cfg.Provider, cfg.MockAccountIDs, guard); err != nil {
		return nil, err
	}
	if cfg.Provider == "yookassa" {
		if cfg.Mode != "test" {
			return nil, service.ErrInvalid
		}
		provider := cfg.PaymentProvider
		if provider == nil {
			var err error
			provider, err = yookassa.New(cfg.ShopID, cfg.SecretKey)
			if err != nil {
				return nil, err
			}
		}
		if err := s.ConfigureYooKassa(provider, cfg.ShopID, cfg.TestAccountIDs); err != nil {
			return nil, err
		}
	}
	return &Module{service: s}, nil
}

func (m *Module) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	billinghttp.New(m.service).Register(mux, protect)
}

func (m *Module) Lock(ctx context.Context, tx pgx.Tx, accountID string) (contracts.Entitlement, error) {
	return m.service.Lock(ctx, tx, accountID)
}
func (m *Module) GrantAdminTx(ctx context.Context, tx pgx.Tx, accountID string, version int64, quota int, until time.Time, reason string) (contracts.Entitlement, error) {
	return m.service.GrantAdminTx(ctx, tx, accountID, version, quota, until, reason)
}
func (m *Module) ExpireAdminTx(ctx context.Context, tx pgx.Tx, accountID string, version int64, reason string) error {
	return m.service.ExpireAdminTx(ctx, tx, accountID, version, reason)
}
func (m *Module) GrantLocal(ctx context.Context, accountID string, quota int, until time.Time, reason string) error {
	return m.service.GrantLocal(ctx, accountID, quota, until, reason)
}
func (m *Module) GrantLocalTx(ctx context.Context, tx pgx.Tx, accountID string, quota int, until time.Time, reason string) error {
	return m.service.GrantLocalTx(ctx, tx, accountID, quota, until, reason)
}

// DeleteAccountTx cancels future billing work without erasing financial amounts.
// The caller has acquired Lock and the account row lock in the same transaction.
func (m *Module) DeleteAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	return m.service.DeleteAccountTx(ctx, tx, accountID)
}

// ProcessOnePayment reconciles one provider attempt outside database locks.
func (m *Module) ProcessOnePayment(ctx context.Context) (bool, error) {
	return m.service.ProcessOnePayment(ctx)
}

var ErrPricingConflict = service.ErrPricingConflict

func (m *Module) Pricing(ctx context.Context) (contracts.Pricing, error) {
	return m.service.Pricing(ctx)
}
func (m *Module) PricingTx(ctx context.Context, tx pgx.Tx, lock bool) (contracts.Pricing, error) {
	return m.service.PricingTx(ctx, tx, lock)
}
func (m *Module) UpdatePricingTx(ctx context.Context, tx pgx.Tx, version int64, minor int) (contracts.Pricing, contracts.Pricing, error) {
	return m.service.UpdatePricingTx(ctx, tx, version, minor)
}
