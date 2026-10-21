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
	e := contracts.Entitlement{Packages: []contracts.Subscription{}}
	rows, err := tx.Query(ctx, `SELECT id::text,monitor_limit,valid_until,source,COALESCE(renewal_anchor_day,0),version FROM billing.entitlements WHERE account_id=$1 ORDER BY id FOR UPDATE`, accountID)
	if err != nil {
		return e, err
	}
	for rows.Next() {
		var part contracts.Subscription
		var until time.Time
		if err = rows.Scan(&part.ID, &part.Quota, &until, &part.Source, &part.RenewalAnchorDay, &part.Version); err != nil {
			rows.Close()
			return e, err
		}
		part.ActiveUntil = &until
		e.Packages = append(e.Packages, part)
		e.Version += part.Version
		e.Source = part.Source
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return e, err
	}
	var admin contracts.AdminGrant
	var adminUntil time.Time
	err = tx.QueryRow(ctx, `SELECT monitor_limit,valid_until,version FROM billing.admin_grants WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&admin.Quota, &adminUntil, &admin.Version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return e, err
	}
	if err == nil {
		admin.ActiveUntil = &adminUntil
		e.Admin = &admin
		e.Version += admin.Version
		if len(e.Packages) == 0 {
			e.Source = "admin_grant"
		}
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return e, err
	}
	var paid contracts.Subscription
	hasReal := false
	for _, part := range e.Packages {
		hasReal = hasReal || (part.Source == "yookassa" && part.ActiveUntil.After(now))
	}
	for i := range e.Packages {
		part := &e.Packages[i]
		part.Active = s.activeSubscription(accountID, part, now) && (!hasReal || part.Source == "yookassa")
		paid.Version += part.Version
		if !part.Active {
			continue
		}
		paid.Quota += part.Quota
		if paid.Source == "" {
			paid.Source = part.Source
		} else if paid.Source != part.Source {
			paid.Source = "combined"
		}
		if paid.ActiveUntil == nil || part.ActiveUntil.Before(*paid.ActiveUntil) {
			paid.ActiveUntil = part.ActiveUntil
		}
	}
	switch len(e.Packages) {
	case 0:
	case 1:
		e.Paid = &e.Packages[0]
		e.RenewalAnchorDay = e.Paid.RenewalAnchorDay
	default:
		paid.Active = paid.Quota > 0
		e.Paid = &paid
	}
	activeSource := ""
	for _, part := range e.Packages {
		if !part.Active {
			continue
		}
		e.Quota += part.Quota
		if activeSource == "" {
			activeSource = part.Source
		} else if activeSource != part.Source {
			activeSource = "combined"
		}
		if e.ActiveUntil == nil || part.ActiveUntil.Before(*e.ActiveUntil) {
			e.ActiveUntil = part.ActiveUntil
		}
	}
	if e.Admin != nil && adminUntil.After(now) {
		e.Quota += admin.Quota
		if activeSource == "" {
			activeSource = "admin_grant"
		} else if activeSource != "admin_grant" {
			activeSource = "combined"
		}
		if e.ActiveUntil == nil || adminUntil.Before(*e.ActiveUntil) {
			e.ActiveUntil = &adminUntil
		}
	}
	if activeSource != "" {
		e.Source = activeSource
	}
	return e, nil
}

func (s *Service) activeSubscription(accountID string, paid *contracts.Subscription, now time.Time) bool {
	return paid != nil && paid.ActiveUntil != nil && paid.ActiveUntil.After(now) &&
		(paid.Source == "yookassa" || (s.local && paid.Source == "local_test") ||
			(s.provider == "mock" && s.allowed[accountID] && paid.Source == "mock_test") ||
			(s.provider == "yookassa" && s.mode == "test" && s.allowed[accountID] && paid.Source == "yookassa_test"))
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
 VALUES($1,$2,$3,'local_test',$4) ON CONFLICT(account_id) WHERE source='local_test' DO UPDATE SET monitor_limit=excluded.monitor_limit,
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
	_, err = tx.Exec(ctx, `INSERT INTO billing.admin_grants(account_id,monitor_limit,valid_until,reason)
		VALUES($1,$2,$3,$4)
		ON CONFLICT(account_id) DO UPDATE SET monitor_limit=excluded.monitor_limit,valid_until=excluded.valid_until,
			reason=excluded.reason,version=billing.admin_grants.version+1,granted_at=now()`, accountID, quota, until, reason)
	if err != nil {
		return e, err
	}
	return s.Lock(ctx, tx, accountID)
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
	if e.Admin == nil || e.Version != version {
		return ErrAdminGrantConflict
	}
	_, err = tx.Exec(ctx, `UPDATE billing.admin_grants SET valid_until=now(),version=version+1,
		granted_at=now(),reason=$2 WHERE account_id=$1`, accountID, reason)
	return err
}
