package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
)

type Repository struct{ Pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{Pool: pool} }

type Row struct {
	contracts.Monitor
	AccountID           string
	TargetKey           string
	Sequence            int64
	RunID, AttemptToken *string
	LeaseUntil          *time.Time
}

const columns = `id::text,account_id::text,name,COALESCE(url,''),COALESCE(method,''),COALESCE(expected_status,0),COALESCE(body_contains,''),interval_minutes,failure_threshold,enabled,version,outcome,last_checked_at,last_http_status,last_latency_ms,next_check_at,target_key,sequence,run_id::text,attempt_token::text,lease_until,check_type,COALESCE(host,''),COALESCE(port,0),last_round_trip_ms`

func scan(row pgx.Row) (Row, error) {
	var m Row
	err := row.Scan(&m.ID, &m.AccountID, &m.Name, &m.URL, &m.Method, &m.ExpectedStatus, &m.BodyContains, &m.IntervalMinutes, &m.FailureThreshold, &m.Enabled, &m.Version, &m.State, &m.LastCheckedAt, &m.LastHTTPStatus, &m.LastLatencyMS, &m.NextCheckAt, &m.TargetKey, &m.Sequence, &m.RunID, &m.AttemptToken, &m.LeaseUntil, &m.CheckType, &m.Host, &m.Port, &m.LastRoundTripMS)
	return m, err
}
func (r *Repository) List(ctx context.Context, tx pgx.Tx, owner string) ([]Row, error) {
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM monitors.monitors WHERE account_id=$1 ORDER BY created_at,id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Row{}
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}
func (r *Repository) Get(ctx context.Context, tx pgx.Tx, owner, id string, skip bool) (Row, error) {
	query := `SELECT ` + columns + ` FROM monitors.monitors WHERE account_id=$1 AND id=$2 FOR UPDATE`
	if skip {
		query += ` SKIP LOCKED`
	}
	return scan(tx.QueryRow(ctx, query, owner, id))
}
func (r *Repository) Count(ctx context.Context, tx pgx.Tx, owner string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM monitors.monitors WHERE account_id=$1`, owner).Scan(&n)
	return n, err
}
func (r *Repository) Rank(ctx context.Context, tx pgx.Tx, owner, id string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT position FROM (SELECT id,row_number() OVER (ORDER BY created_at,id) AS position FROM monitors.monitors WHERE account_id=$1) ranked WHERE id=$2`, owner, id).Scan(&n)
	return n, err
}
func (r *Repository) Insert(ctx context.Context, tx pgx.Tx, owner, id, target string, in contracts.MonitorInput) error {
	_, err := tx.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status,body_contains,interval_minutes,failure_threshold,enabled,check_type,host,port) VALUES($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),NULLIF($7,0),CASE WHEN $12='http' THEN $8 ELSE NULL END,$9,$10,$11,$12,NULLIF($13,''),NULLIF($14,0))`, id, owner, in.Name, in.URL, target, in.Method, in.ExpectedStatus, in.BodyContains, *in.IntervalMinutes, *in.FailureThreshold, in.Enabled, in.CheckType, in.Host, in.Port)
	return err
}
func (r *Repository) Update(ctx context.Context, tx pgx.Tx, owner, id, target string, in contracts.MonitorInput, resetCheck bool) error {
	_, err := tx.Exec(ctx, `UPDATE monitors.monitors SET name=$3,url=NULLIF($4,''),target_key=$5,method=NULLIF($6,''),expected_status=NULLIF($7,0),body_contains=CASE WHEN $13='http' THEN $8 ELSE NULL END,check_type=$13,host=NULLIF($14,''),port=NULLIF($15,0),interval_minutes=$9,failure_threshold=$10,enabled=$11,version=version+1,outcome=CASE WHEN $12 THEN 'UNKNOWN' ELSE outcome END,
 last_checked_at=CASE WHEN $12 THEN NULL ELSE last_checked_at END,
 last_http_status=CASE WHEN $12 THEN NULL ELSE last_http_status END,
 last_latency_ms=CASE WHEN $12 THEN NULL ELSE last_latency_ms END,
 last_round_trip_ms=CASE WHEN $12 THEN NULL ELSE last_round_trip_ms END,
 next_check_at=CASE WHEN $12 THEN now() ELSE next_check_at END,
 run_id=NULL,attempt_token=NULL,lease_until=NULL,updated_at=now() WHERE account_id=$1 AND id=$2`, owner, id, in.Name, in.URL, target, in.Method, in.ExpectedStatus, in.BodyContains, *in.IntervalMinutes, *in.FailureThreshold, in.Enabled, resetCheck, in.CheckType, in.Host, in.Port)
	return err
}
func (r *Repository) Delete(ctx context.Context, tx pgx.Tx, owner, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM monitors.monitors WHERE account_id=$1 AND id=$2`, owner, id)
	return err
}
func (r *Repository) Results(ctx context.Context, owner, id string, limit int, before time.Time) ([]contracts.Result, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id::text,outcome,error_code,checked_at,http_status,latency_ms,check_type,round_trip_ms FROM monitors.results WHERE account_id=$1 AND monitor_id=$2 AND checked_at<$3 AND accepted_at>=now()-interval '7 days' ORDER BY checked_at DESC,id DESC LIMIT $4`, owner, id, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []contracts.Result{}
	for rows.Next() {
		var i contracts.Result
		if err = rows.Scan(&i.ID, &i.Outcome, &i.ErrorCode, &i.CheckedAt, &i.HTTPStatus, &i.LatencyMS, &i.CheckType, &i.RoundTripMS); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

type Candidate struct{ ID, AccountID string }

func (r *Repository) Candidates(ctx context.Context) ([]Candidate, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id::text,account_id::text FROM monitors.monitors due WHERE enabled AND next_check_at<=now() AND (lease_until IS NULL OR lease_until<=now())
 AND NOT EXISTS (SELECT 1 FROM monitors.monitors active WHERE active.target_key=due.target_key AND active.lease_until>now())
 AND (SELECT count(*) FROM monitors.monitors active WHERE active.account_id=due.account_id AND active.lease_until>now())<2
 ORDER BY next_check_at,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Candidate{}
	for rows.Next() {
		var c Candidate
		if err = rows.Scan(&c.ID, &c.AccountID); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (r *Repository) Capacity(ctx context.Context, tx pgx.Tx, owner, target string) (bool, error) {
	var global, account, host int
	err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE account_id=$1),count(*) FILTER(WHERE target_key=$2) FROM monitors.monitors WHERE lease_until>now()`, owner, target).Scan(&global, &account, &host)
	return global < 4 && account < 2 && host < 1, err
}
func (r *Repository) Claim(ctx context.Context, tx pgx.Tx, owner, id, runID, token string) error {
	_, err := tx.Exec(ctx, `UPDATE monitors.monitors SET run_id=$3,attempt_token=$4,lease_until=now()+interval '20 seconds' WHERE account_id=$1 AND id=$2`, owner, id, runID, token)
	return err
}
func (r *Repository) StoreResult(ctx context.Context, tx pgx.Tx, m Row, eventID, runID, token, outcome string, checked time.Time, status *int, latency int64, code string, roundTrip *int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO monitors.results(id,monitor_id,account_id,run_id,attempt_token,config_version,sequence,outcome,checked_at,http_status,latency_ms,error_code,check_type,round_trip_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, eventID, m.ID, m.AccountID, runID, token, m.Version, m.Sequence+1, outcome, checked, status, latency, code, m.CheckType, roundTrip)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE monitors.monitors SET outcome=$3,last_checked_at=$4,last_http_status=$5,last_latency_ms=$6,last_round_trip_ms=$7,next_check_at=now()+interval_minutes*interval '1 minute',run_id=NULL,attempt_token=NULL,lease_until=NULL,sequence=sequence+1 WHERE account_id=$1 AND id=$2`, m.AccountID, m.ID, outcome, checked, status, latency, roundTrip)
	if err != nil {
		return err
	}
	// UNKNOWN is counted separately and never increases the downtime denominator.
	// Internal probe failures also do not distort measured request latency.
	_, err = tx.Exec(ctx, `INSERT INTO monitors.daily_statistics
 (monitor_id,account_id,day,up_count,down_count,unknown_count,latency_sum_ms,latency_count)
 VALUES($1,$2,($3::timestamptz AT TIME ZONE 'UTC')::date,
 CASE WHEN $4='UP' THEN 1 ELSE 0 END, CASE WHEN $4='DOWN' THEN 1 ELSE 0 END,
 CASE WHEN $4='UNKNOWN' THEN 1 ELSE 0 END, CASE WHEN $4='UNKNOWN' THEN 0 ELSE $5 END,
 CASE WHEN $4='UNKNOWN' THEN 0 ELSE 1 END)
 ON CONFLICT(monitor_id,day) DO UPDATE SET
 up_count=monitors.daily_statistics.up_count+excluded.up_count,
 down_count=monitors.daily_statistics.down_count+excluded.down_count,
 unknown_count=monitors.daily_statistics.unknown_count+excluded.unknown_count,
 latency_sum_ms=monitors.daily_statistics.latency_sum_ms+excluded.latency_sum_ms,
 latency_count=monitors.daily_statistics.latency_count+excluded.latency_count`, m.ID, m.AccountID, checked, outcome, latency)
	return err
}
func (r *Repository) Cleanup(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM monitors.results WHERE id IN (SELECT id FROM monitors.results WHERE accepted_at<now()-interval '7 days' ORDER BY accepted_at LIMIT 500)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM monitors.daily_statistics WHERE (monitor_id,day) IN
 (SELECT monitor_id,day FROM monitors.daily_statistics WHERE day<(now() AT TIME ZONE 'UTC')::date-90 ORDER BY day LIMIT 500)`)
	return err
}

func (r *Repository) Defer(ctx context.Context, tx pgx.Tx, owner, id string) error {
	_, err := tx.Exec(ctx, `UPDATE monitors.monitors SET next_check_at=now()+interval_minutes*interval '1 minute',run_id=NULL,attempt_token=NULL,lease_until=NULL WHERE account_id=$1 AND id=$2`, owner, id)
	return err
}
