package postgres

import (
	"context"
	"errors"
	"net"
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
	CheckConfigVersion  int64
	Sequence            int64
	Rank                int
	RunID, AttemptToken *string
	LeaseUntil          *time.Time
}

const columns = `id::text,account_id::text,name,COALESCE(url,''),COALESCE(method,''),COALESCE(expected_status,0),COALESCE(body_contains,''),interval_minutes,failure_threshold,enabled,version,outcome,last_checked_at,last_http_status,last_latency_ms,next_check_at,target_key,sequence,run_id::text,attempt_token::text,lease_until,check_type,COALESCE(host,''),COALESCE(port,0),last_round_trip_ms,time_zone,check_config_version,
 COALESCE((SELECT provider_names FROM monitors.provider_lookups p WHERE p.monitor_id=id),'{}'::text[]),
 COALESCE((SELECT status FROM monitors.provider_lookups p WHERE p.monitor_id=id),'unknown'),
 (SELECT checked_at FROM monitors.provider_lookups p WHERE p.monitor_id=id)`

func scan(row pgx.Row) (Row, error) {
	var m Row
	err := row.Scan(m.scanFields()...)
	return m, err
}
func (m *Row) scanFields() []any {
	return []any{&m.ID, &m.AccountID, &m.Name, &m.URL, &m.Method, &m.ExpectedStatus, &m.BodyContains, &m.IntervalMinutes, &m.FailureThreshold, &m.Enabled, &m.Version, &m.State, &m.LastCheckedAt, &m.LastHTTPStatus, &m.LastLatencyMS, &m.NextCheckAt, &m.TargetKey, &m.Sequence, &m.RunID, &m.AttemptToken, &m.LeaseUntil, &m.CheckType, &m.Host, &m.Port, &m.LastRoundTripMS, &m.TimeZone, &m.CheckConfigVersion, &m.Provider.Names, &m.Provider.Status, &m.Provider.CheckedAt}
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

// A literal substring keeps search behavior identical for punctuation, % and _.
const searchMatch = `($2='' OR position(lower($2) IN lower(name || ' ' ||
 CASE check_type WHEN 'http' THEN COALESCE(url,'') WHEN 'tcp' THEN COALESCE(host,'') || ':' || COALESCE(port::text,'') ELSE COALESCE(host,'') END))>0)`

// Rank and effective state are resolved across the account before any filter.
// Summary and page share this SQL and the exact same quota/freshness instant.
const listStates = `WITH ranked AS (
 SELECT *,sum(enabled::integer) OVER (ORDER BY created_at,id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS quota_rank
 FROM monitors.monitors WHERE account_id=$1
 ), states AS (
 SELECT *,CASE WHEN NOT enabled THEN 'PAUSED'
 WHEN $3::integer=0 OR quota_rank>$3 THEN 'SUBSCRIPTION_REQUIRED'
 WHEN last_checked_at IS NULL OR $4::timestamptz-last_checked_at > (2*interval_minutes*interval '1 minute'+interval '30 seconds') THEN 'UNKNOWN'
 ELSE outcome END AS effective_state FROM ranked
 ) `

const statusMatch = `($5='all' OR effective_state=$5 OR ($5='problems' AND effective_state IN ('DOWN','UNKNOWN','SUBSCRIPTION_REQUIRED')))`

func (r *Repository) PageSummary(ctx context.Context, tx pgx.Tx, owner string, options contracts.ListOptions, quota int, asOf time.Time) (contracts.MonitorSummary, int, error) {
	var summary contracts.MonitorSummary
	var total int
	err := tx.QueryRow(ctx, listStates+`SELECT count(*),count(*) FILTER (WHERE effective_state='UP'),count(*) FILTER (WHERE effective_state='DOWN'),
 count(*) FILTER (WHERE effective_state='UNKNOWN'),count(*) FILTER (WHERE effective_state='PAUSED'),
 count(*) FILTER (WHERE effective_state='SUBSCRIPTION_REQUIRED'),count(*) FILTER (WHERE `+searchMatch+` AND `+statusMatch+`)
 FROM states WHERE account_id=$1`, owner, options.Query, quota, asOf, options.Status).Scan(&summary.Total, &summary.Up, &summary.Down, &summary.Unknown, &summary.Paused, &summary.SubscriptionRequired, &total)
	return summary, total, err
}

func (r *Repository) ListPage(ctx context.Context, tx pgx.Tx, owner string, options contracts.ListOptions, quota int, asOf time.Time) ([]Row, error) {
	// Only reviewed SQL fragments can reach ORDER BY; client text stays in values.
	order := map[string]string{
		"created_desc": "created_at DESC,id DESC", "created_asc": "created_at,id",
		"name_asc": "lower(name),id", "name_desc": "lower(name) DESC,id DESC",
		"problems_first": "CASE effective_state WHEN 'DOWN' THEN 0 WHEN 'UNKNOWN' THEN 1 WHEN 'SUBSCRIPTION_REQUIRED' THEN 2 WHEN 'UP' THEN 3 ELSE 4 END,created_at DESC,id DESC",
	}[options.Sort]
	if order == "" {
		return nil, errors.New("invalid monitor ordering")
	}
	rows, err := tx.Query(ctx, listStates+`SELECT `+columns+`,quota_rank FROM states WHERE account_id=$1 AND `+searchMatch+` AND `+statusMatch+`
 ORDER BY `+order+` LIMIT $6 OFFSET $7`, owner, options.Query, quota, asOf, options.Status, options.PageSize, (options.Page-1)*options.PageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Row{}
	for rows.Next() {
		var m Row
		if err := rows.Scan(append(m.scanFields(), &m.Rank)...); err != nil {
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
func (r *Repository) CountEnabled(ctx context.Context, tx pgx.Tx, owner string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM monitors.monitors WHERE account_id=$1 AND enabled`, owner).Scan(&n)
	return n, err
}
func (r *Repository) Rank(ctx context.Context, tx pgx.Tx, owner, id string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT position FROM (SELECT id,sum(enabled::integer) OVER (ORDER BY created_at,id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS position FROM monitors.monitors WHERE account_id=$1) ranked WHERE id=$2`, owner, id).Scan(&n)
	return n, err
}
func (r *Repository) Insert(ctx context.Context, tx pgx.Tx, owner, id, target string, in contracts.MonitorInput) error {
	_, err := tx.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status,body_contains,interval_minutes,failure_threshold,enabled,check_type,host,port,time_zone) VALUES($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),NULLIF($7,0),CASE WHEN $12='http' THEN $8 ELSE NULL END,$9,$10,$11,$12,NULLIF($13,''),NULLIF($14,0),$15)`, id, owner, in.Name, in.URL, target, in.Method, in.ExpectedStatus, in.BodyContains, *in.IntervalMinutes, *in.FailureThreshold, in.Enabled, in.CheckType, in.Host, in.Port, in.TimeZone)
	if err != nil {
		return err
	}
	return r.queueProvider(ctx, tx, id, target)
}
func (r *Repository) Update(ctx context.Context, tx pgx.Tx, owner, id, target string, in contracts.MonitorInput, resetCheck bool) error {
	_, err := tx.Exec(ctx, `UPDATE monitors.monitors SET name=$3,url=NULLIF($4,''),target_key=$5,method=NULLIF($6,''),expected_status=NULLIF($7,0),body_contains=CASE WHEN $13='http' THEN $8 ELSE NULL END,check_type=$13,host=NULLIF($14,''),port=NULLIF($15,0),interval_minutes=$9,failure_threshold=$10,enabled=$11,time_zone=$16,version=version+1,check_config_version=check_config_version+CASE WHEN $12 THEN 1 ELSE 0 END,outcome=CASE WHEN $12 THEN 'UNKNOWN' ELSE outcome END,
 last_checked_at=CASE WHEN $12 THEN NULL ELSE last_checked_at END,
 last_http_status=CASE WHEN $12 THEN NULL ELSE last_http_status END,
 last_latency_ms=CASE WHEN $12 THEN NULL ELSE last_latency_ms END,
 last_round_trip_ms=CASE WHEN $12 THEN NULL ELSE last_round_trip_ms END,
 next_check_at=CASE WHEN $12 THEN now() ELSE next_check_at END,
 run_id=CASE WHEN $12 THEN NULL ELSE run_id END,attempt_token=CASE WHEN $12 THEN NULL ELSE attempt_token END,lease_until=CASE WHEN $12 THEN NULL ELSE lease_until END,updated_at=now() WHERE account_id=$1 AND id=$2`, owner, id, in.Name, in.URL, target, in.Method, in.ExpectedStatus, in.BodyContains, *in.IntervalMinutes, *in.FailureThreshold, in.Enabled, resetCheck, in.CheckType, in.Host, in.Port, in.TimeZone)
	if err != nil {
		return err
	}
	return r.queueProvider(ctx, tx, id, target)
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
	_, err := tx.Exec(ctx, `INSERT INTO monitors.results(id,monitor_id,account_id,run_id,attempt_token,config_version,sequence,outcome,checked_at,http_status,latency_ms,error_code,check_type,round_trip_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, eventID, m.ID, m.AccountID, runID, token, m.CheckConfigVersion, m.Sequence+1, outcome, checked, status, latency, code, m.CheckType, roundTrip)
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

// queueProvider only invalidates enrichment when the normalized host changes.
// All user writes already hold the monitor lock; network work happens in worker.
func (r *Repository) queueProvider(ctx context.Context, tx pgx.Tx, id, target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO monitors.provider_lookups(monitor_id,target_host) VALUES($1,$2)
 ON CONFLICT(monitor_id) DO UPDATE SET target_host=excluded.target_host,generation=monitors.provider_lookups.generation+1,
 status='pending',provider_names='{}',asns='{}',checked_at=NULL,dataset_version='',attempts=0,next_attempt_at=now(),lease_until=NULL,attempt_token=NULL
 WHERE monitors.provider_lookups.target_host IS DISTINCT FROM excluded.target_host`, id, host)
	return err
}
