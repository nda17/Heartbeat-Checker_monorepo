package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type ProviderAttempt struct {
	MonitorID, Host, Token string
	Generation             int64
}

// ClaimProvider has no monitor/account locks and performs no network I/O. The
// lease is scoped only to metadata; stale completions also require the token.
func (r *Repository) ClaimProvider(ctx context.Context) (*ProviderAttempt, error) {
	token, err := outbox.ID()
	if err != nil {
		return nil, err
	}
	a := &ProviderAttempt{Token: token}
	err = r.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT monitor_id FROM monitors.provider_lookups
 WHERE status='pending' AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<=now())
 ORDER BY next_attempt_at,monitor_id FOR UPDATE SKIP LOCKED LIMIT 1
 ) UPDATE monitors.provider_lookups p SET lease_until=now()+interval '2 minutes',attempt_token=$1
 FROM candidate c WHERE p.monitor_id=c.monitor_id
 RETURNING p.monitor_id::text,p.target_host,p.generation`, token).Scan(&a.MonitorID, &a.Host, &a.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (r *Repository) CompleteProvider(ctx context.Context, a ProviderAttempt, names []string, asns []int64, version string, retry bool) error {
	status := "unknown"
	if len(names) > 0 {
		status = "ready"
	}
	_, err := r.Pool.Exec(ctx, `UPDATE monitors.provider_lookups SET
 attempts=attempts+1,
 status=CASE WHEN $8 AND attempts+1<3 THEN 'pending' ELSE $5 END,
 provider_names=$6,asns=$7,checked_at=now(),dataset_version=$9,
 next_attempt_at=now()+CASE WHEN attempts=0 THEN interval '1 minute' ELSE interval '10 minutes' END,
 lease_until=NULL,attempt_token=NULL
 WHERE monitor_id=$1 AND target_host=$2 AND generation=$3 AND attempt_token=$4 AND lease_until>now()`,
		a.MonitorID, a.Host, a.Generation, a.Token, status, names, asns, retry, version)
	return err
}
