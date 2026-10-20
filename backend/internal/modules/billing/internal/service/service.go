package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

type Service struct {
	pool     *pgxpool.Pool
	local    bool
	provider string
	allowed  map[string]bool
	accounts accounts.ActiveGuard
	payments contracts.Provider
	shopID   string
	mode     string
}

var ErrAdminGrant = errors.New("invalid administrative grant")
var ErrAdminGrantConflict = errors.New("administrative grant version conflict")

func New(pool *pgxpool.Pool, local bool) *Service {
	return &Service{pool: pool, local: local, provider: "disabled"}
}

func (s *Service) Lock(ctx context.Context, tx pgx.Tx, accountID string) (contracts.Entitlement, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 61729))`, accountID); err != nil {
		return contracts.Entitlement{}, err
	}
	var e contracts.Entitlement
	var until time.Time
	err := tx.QueryRow(ctx, `SELECT monitor_limit,valid_until,source,COALESCE(renewal_anchor_day,0),version FROM billing.entitlements WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&e.Quota, &until, &e.Source, &e.RenewalAnchorDay, &e.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	e.ActiveUntil = &until
	validSource := e.Source == "admin_grant" || e.Source == "yookassa" || (s.local && e.Source == "local_test") || (s.provider == "mock" && s.allowed[accountID] && e.Source == "mock_test") || (s.provider == "yookassa" && s.mode == "test" && s.allowed[accountID] && e.Source == "yookassa_test")
	if !validSource || !until.After(time.Now()) {
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
 valid_until=excluded.valid_until,source='local_test',renewal_anchor_day=NULL,version=billing.entitlements.version+1,granted_at=now(),reason=excluded.reason`, accountID, quota, until, reason)
	return err
}

func (s *Service) validateLocalGrant(quota int, until time.Time, reason string) error {
	if !s.local || quota < 1 || quota > 1000 || !until.After(time.Now()) || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return errors.New("invalid local entitlement grant")
	}
	return nil
}

// GrantAdminTx is called only by an MFA-gated administrator transaction. The
// caller owns its audit record and account lock in the same transaction.
func (s *Service) GrantAdminTx(ctx context.Context, tx pgx.Tx, accountID string, version int64, quota int, until time.Time, reason string) (contracts.Entitlement, error) {
	if quota < 1 || quota > 1000 || !until.After(time.Now()) || strings.TrimSpace(reason) == "" || len(reason) > 500 || version < 0 {
		return contracts.Entitlement{}, ErrAdminGrant
	}
	e, err := s.Lock(ctx, tx, accountID)
	if err != nil {
		return e, err
	}
	if e.Version != version {
		return e, ErrAdminGrantConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason,renewal_anchor_day)
		VALUES($1,$2,$3,'admin_grant',$4,NULL)
		ON CONFLICT(account_id) DO UPDATE SET monitor_limit=excluded.monitor_limit,valid_until=excluded.valid_until,
			source='admin_grant',reason=excluded.reason,renewal_anchor_day=NULL,version=billing.entitlements.version+1,granted_at=now()`, accountID, quota, until, reason)
	if err != nil {
		return e, err
	}
	e.Quota, e.ActiveUntil, e.Source, e.Version, e.RenewalAnchorDay = quota, &until, "admin_grant", version+1, 0
	return e, nil
}

// ExpireAdminTx revokes only an administrative grant; paid history is untouched.
func (s *Service) ExpireAdminTx(ctx context.Context, tx pgx.Tx, accountID string, version int64, reason string) error {
	if version < 1 || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return ErrAdminGrant
	}
	e, err := s.Lock(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if e.Version != version || e.Source != "admin_grant" {
		return ErrAdminGrantConflict
	}
	_, err = tx.Exec(ctx, `UPDATE billing.entitlements SET valid_until=now(),version=version+1,
		granted_at=now(),reason=$2 WHERE account_id=$1`, accountID, reason)
	return err
}
