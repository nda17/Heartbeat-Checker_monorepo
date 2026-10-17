package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

type Service struct {
	pool  *pgxpool.Pool
	local bool
}

func New(pool *pgxpool.Pool, local bool) *Service { return &Service{pool: pool, local: local} }

func (s *Service) Lock(ctx context.Context, tx pgx.Tx, accountID string) (contracts.Entitlement, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 61729))`, accountID); err != nil {
		return contracts.Entitlement{}, err
	}
	var e contracts.Entitlement
	var until time.Time
	err := tx.QueryRow(ctx, `SELECT monitor_limit,valid_until,source FROM billing.entitlements WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&e.Quota, &until, &e.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	e.ActiveUntil = &until
	if !s.local || e.Source != "local_test" || !until.After(time.Now()) {
		e.Quota = 0
	}
	return e, nil
}

// GrantLocal is available only to the local command, never an HTTP endpoint.
func (s *Service) GrantLocal(ctx context.Context, accountID string, quota int, until time.Time, reason string) error {
	if err := s.validateLocalGrant(quota, until, reason); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.GrantLocalTx(ctx, tx, accountID, quota, until, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GrantLocalTx lets local bootstrap share the account and credential transaction.
func (s *Service) GrantLocalTx(ctx context.Context, tx pgx.Tx, accountID string, quota int, until time.Time, reason string) error {
	if err := s.validateLocalGrant(quota, until, reason); err != nil {
		return err
	}
	if _, err := s.Lock(ctx, tx, accountID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason)
 VALUES($1,$2,$3,'local_test',$4) ON CONFLICT(account_id) DO UPDATE SET monitor_limit=excluded.monitor_limit,
 valid_until=excluded.valid_until,source='local_test',version=billing.entitlements.version+1,granted_at=now(),reason=excluded.reason`, accountID, quota, until, reason)
	return err
}

func (s *Service) validateLocalGrant(quota int, until time.Time, reason string) error {
	if !s.local || quota < 1 || quota > 1000 || !until.After(time.Now()) || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return errors.New("invalid local entitlement grant")
	}
	return nil
}
