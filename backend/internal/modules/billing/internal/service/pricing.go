package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

var ErrOrderAmount = errors.New("billing order amount exceeds limit")

const MaxOrderAmountMinor = 35000000

var ErrPriceChanged = errors.New("billing price changed")
var ErrPricingUnavailable = errors.New("billing pricing is unavailable")
var ErrPricingConflict = errors.New("billing pricing version conflict")

func scanPricing(row pgx.Row) (contracts.Pricing, error) {
	p := contracts.Pricing{Currency: "RUB", Period: "month"}
	err := row.Scan(&p.PricePerMonitorMinor, &p.Version, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrPricingUnavailable
	}
	return p, err
}

const pricingColumns = `price_per_monitor_minor,version,updated_at`

func (s *Service) Pricing(ctx context.Context) (contracts.Pricing, error) {
	return scanPricing(s.pool.QueryRow(ctx, `SELECT `+pricingColumns+` FROM billing.pricing WHERE singleton=1`))
}

// PricingTx is called after account locks. A shared lock keeps a new order's quote stable until commit.
func (s *Service) PricingTx(ctx context.Context, tx pgx.Tx, lock bool) (contracts.Pricing, error) {
	query := `SELECT ` + pricingColumns + ` FROM billing.pricing WHERE singleton=1`
	if lock {
		query += ` FOR SHARE`
	}
	return scanPricing(tx.QueryRow(ctx, query))
}

// UpdatePricingTx returns both snapshots for an atomic administrative audit.
func (s *Service) UpdatePricingTx(ctx context.Context, tx pgx.Tx, version int64, minor int) (contracts.Pricing, contracts.Pricing, error) {
	if version < 1 || minor < 100 || minor > 1000000 {
		return contracts.Pricing{}, contracts.Pricing{}, ErrInvalid
	}
	before, err := scanPricing(tx.QueryRow(ctx, `SELECT `+pricingColumns+` FROM billing.pricing WHERE singleton=1 FOR UPDATE`))
	if err != nil {
		return before, before, err
	}
	if before.Version != version {
		return before, before, ErrPricingConflict
	}
	if before.PricePerMonitorMinor == minor {
		return before, before, nil
	}
	after, err := scanPricing(tx.QueryRow(ctx, `UPDATE billing.pricing SET price_per_monitor_minor=$1,version=version+1,updated_at=clock_timestamp() WHERE singleton=1 RETURNING `+pricingColumns, minor))
	return before, after, err
}
