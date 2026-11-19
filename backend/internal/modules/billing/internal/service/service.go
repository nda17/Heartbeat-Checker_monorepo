package service

import (
	"context"
	"crypto/cipher"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

type Service struct {
	pool         *pgxpool.Pool
	local        bool
	provider     string
	allowed      map[string]bool
	accounts     accounts.ActiveGuard
	payments     contracts.Provider
	shopID       string
	mode         string
	profiles     map[string]paymentProfile
	nativeMode   string
	nativeShopID string
	testAllowed  map[string]bool
	sdkEnabled   bool
	sdkBox       cipher.AEAD
	sessionLease func(context.Context, pgx.Tx, string, string) (time.Time, error)
}

func New(pool *pgxpool.Pool, local bool) *Service {
	return &Service{pool: pool, local: local, provider: "disabled"}
}

func (s *Service) Lock(ctx context.Context, tx pgx.Tx, accountID string) (contracts.Entitlement, error) {
	balance, err := s.WalletTx(ctx, tx, accountID)
	return contracts.Entitlement{
		Quota: balance.ActiveMonitors, ActiveUntil: balance.FundedUntil, Source: "balance",
		EnabledMonitorCount: balance.EnabledMonitors, ActiveMonitorCount: balance.ActiveMonitors,
		Packages: []contracts.Subscription{}, Balance: balance,
	}, err
}

func (s *Service) CreditLocal(ctx context.Context, accountID string, amount int, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.CreditLocalTx(ctx, tx, accountID, amount, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreditLocalTx is used only by the server-side local development commands.
func (s *Service) CreditLocalTx(ctx context.Context, tx pgx.Tx, accountID string, amount int, reason string) error {
	if !s.local || !validID(accountID) || amount < 1 || amount > MaxOrderAmountMinor || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return ErrInvalid
	}
	_, _, _, err := s.creditWallet(ctx, tx, accountID, amount, "local_credit", nil, "", nil)
	return err
}
