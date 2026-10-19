package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests create and drop only a uniquely named database on the
// local Compose PostgreSQL instance. Set HEARTBEAT_TEST_DATABASE_URL to the
// local compose URL when running them; no existing database is modified.
func testDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	baseURL := os.Getenv("HEARTBEAT_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set HEARTBEAT_TEST_DATABASE_URL to the isolated local PostgreSQL server URL")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Host != "127.0.0.1:55432" {
		t.Fatalf("HEARTBEAT_TEST_DATABASE_URL must target local Compose PostgreSQL at 127.0.0.1:55432")
	}
	admin, err := pgx.Connect(context.Background(), baseURL)
	if err != nil {
		t.Fatalf("connect to local test PostgreSQL: %v", err)
	}
	name := fmt.Sprintf("hc_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("create isolated test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()", name)
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
		_ = admin.Close(ctx)
	})
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(context.Background(), parsed.String())
	if err != nil {
		t.Fatalf("connect to isolated test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestApplyIsIdempotent(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("first Apply(): %v", err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("second Apply(): %v", err)
	}
	embeddedMigrations, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(embeddedMigrations) {
		t.Fatalf("migration count = %d, want %d embedded migrations", count, len(embeddedMigrations))
	}
	for _, table := range []string{
		"accounts.accounts", "auth.credentials", "billing.entitlements", "monitors.monitors",
		"monitors.results", "incidents.incidents", "outbox.events",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
			t.Fatalf("check business table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("expected business table %s after applying migrations", table)
		}
	}
}

func TestMockBillingPriceMigrationPreservesLegacyAmountsAndChecksNewAmounts(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	previousChecksums := make(map[string]string)
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	for _, name := range names {
		if name > "0008_mock_billing.sql" {
			break
		}
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("read legacy migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		checksum := hex.EncodeToString(sum[:])
		version := strings.TrimSuffix(name, ".sql")
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, version, checksum); err != nil {
			t.Fatalf("record legacy migration %s: %v", name, err)
		}
		previousChecksums[version] = checksum
	}

	const legacyAccountID = "a0000000-0000-4000-8000-000000000001"
	const legacyOrderID = "b0000000-0000-4000-8000-000000000001"
	const legacyIdempotencyKey = "c0000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Legacy billing owner')`, legacyAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor) VALUES($1,$2,$3,'mock',1,2400)`, legacyOrderID, legacyAccountID, legacyIdempotencyKey); err != nil {
		t.Fatalf("seed legacy 2400-minor order before price migration: %v", err)
	}

	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply price migration after legacy order exists: %v", err)
	}
	var preservedAmount int
	if err := pool.QueryRow(ctx, `SELECT amount_minor FROM billing.orders WHERE id=$1`, legacyOrderID).Scan(&preservedAmount); err != nil || preservedAmount != 2400 {
		t.Fatalf("legacy order amount after migration=%d err=%v, want unchanged 2400", preservedAmount, err)
	}
	for version, checksum := range previousChecksums {
		var after string
		if err := pool.QueryRow(ctx, `SELECT checksum FROM public.schema_migrations WHERE version=$1`, version).Scan(&after); err != nil || after != checksum {
			t.Errorf("legacy migration %s checksum changed: before=%s after=%s err=%v", version, checksum, after, err)
		}
	}

	const validAccountID = "a0000000-0000-4000-8000-000000000002"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'New billing owner')`, validAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor) VALUES('b0000000-0000-4000-8000-000000000002',$1,'c0000000-0000-4000-8000-000000000002','mock',3,8700)`, validAccountID); err != nil {
		t.Fatalf("insert new 2900-minor-per-monitor order: %v", err)
	}

	for index, amount := range []int{0, -8700, 8701} {
		accountID := fmt.Sprintf("a0000000-0000-4000-8000-%012d", index+3)
		if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Invalid amount owner')`, accountID); err != nil {
			t.Fatal(err)
		}
		orderID := fmt.Sprintf("b0000000-0000-4000-8000-%012d", index+3)
		key := fmt.Sprintf("c0000000-0000-4000-8000-%012d", index+3)
		if _, err := pool.Exec(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor) VALUES($1,$2,$3,'mock',3,$4)`, orderID, accountID, key, amount); err == nil {
			t.Errorf("insert monitor_count=3 amount_minor=%d succeeded; want constraint rejection", amount)
		}
	}
}

func TestSupportNoticeMigrationPreservesLegacyInboxRowsAndChecksums(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	previousChecksums := map[string]string{}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	for _, name := range names {
		if name >= "0010_support_notices.sql" {
			break
		}
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("read legacy migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		checksum := hex.EncodeToString(sum[:])
		version := strings.TrimSuffix(name, ".sql")
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, version, checksum); err != nil {
			t.Fatalf("record legacy migration %s: %v", name, err)
		}
		previousChecksums[version] = checksum
	}
	if len(previousChecksums) != 9 {
		t.Fatalf("seeded %d pre-notice migrations, want 9", len(previousChecksums))
	}
	legacyPayload := []byte{1, 2, 3, 4}
	legacyHash := []byte{5, 6, 7}
	legacySender := []byte{8, 9}
	if _, err := pool.Exec(ctx, `INSERT INTO support.inbox(provider,event_key,event_kind,sender_key,payload_hash,payload,status,attempts,last_error) VALUES('telegram','old-update','message',$1,$2,$3,'pending',2,'provider_unavailable')`, legacySender, legacyHash, legacyPayload); err != nil {
		t.Fatalf("seed legacy message before notice migration: %v", err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply notice migration after legacy support row exists: %v", err)
	}
	var kind, status, lastError string
	var attempts int
	var gotSender, gotHash, gotPayload []byte
	if err := pool.QueryRow(ctx, `SELECT event_kind,status,attempts,last_error,sender_key,payload_hash,payload FROM support.inbox WHERE event_key='old-update'`).Scan(&kind, &status, &attempts, &lastError, &gotSender, &gotHash, &gotPayload); err != nil {
		t.Fatal(err)
	}
	if kind != "message" || status != "pending" || attempts != 2 || lastError != "provider_unavailable" || string(gotSender) != string(legacySender) || string(gotHash) != string(legacyHash) || string(gotPayload) != string(legacyPayload) {
		t.Fatalf("legacy support row changed after 0010: kind=%s status=%s attempts=%d error=%s sender=%v hash=%v payload=%v", kind, status, attempts, lastError, gotSender, gotHash, gotPayload)
	}
	for version, checksum := range previousChecksums {
		var after string
		if err := pool.QueryRow(ctx, `SELECT checksum FROM public.schema_migrations WHERE version=$1`, version).Scan(&after); err != nil || after != checksum {
			t.Errorf("legacy migration %s checksum changed: before=%s after=%s err=%v", version, checksum, after, err)
		}
	}
	for _, eventKind := range []string{"welcome", "ack"} {
		if _, err := pool.Exec(ctx, `INSERT INTO support.inbox(provider,event_key,event_kind,sender_key,payload_hash,payload) VALUES('telegram',$1,$2,$3,$4,$5)`, "new-"+eventKind, eventKind, []byte{1}, []byte{2}, []byte{3}); err != nil {
			t.Errorf("insert new %s support kind after migration: %v", eventKind, err)
		}
	}
}

func TestMessengerMigrationNumbersExistingIncidentsAndPreservesPriorChecksums(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	previousChecksums := map[string]string{}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	for _, name := range names {
		if name >= "0011_messenger_notifications.sql" {
			break
		}
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("read legacy migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		checksum := hex.EncodeToString(sum[:])
		version := strings.TrimSuffix(name, ".sql")
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, version, checksum); err != nil {
			t.Fatalf("record legacy migration %s: %v", name, err)
		}
		previousChecksums[version] = checksum
	}
	if len(previousChecksums) != 10 {
		t.Fatalf("seeded %d pre-messenger migrations, want 10", len(previousChecksums))
	}
	const accountID = "a1000000-0000-4000-8000-000000000001"
	const monitorID = "b1000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Messenger migration owner')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status) VALUES($1,$2,'Migration monitor','https://migration.example.test/health','migration.example.test:443','GET',200)`, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	const openIncidentID = "c1000000-0000-4000-8000-000000000001"
	const closedIncidentID = "c1000000-0000-4000-8000-000000000002"
	if _, err := pool.Exec(ctx, `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at) VALUES($1,$2,$3,'2026-01-02T03:04:05Z','2026-01-02T03:05:05Z')`, openIncidentID, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at,closed_at,close_reason) VALUES($1,$2,$3,'2026-01-01T03:04:05Z','2026-01-01T03:05:05Z','2026-01-01T04:05:05Z','recovered')`, closedIncidentID, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply messenger migration with existing incidents: %v", err)
	}
	var openNumber, closedNumber int64
	var openReason, closedReason string
	var closeTime *time.Time
	if err := pool.QueryRow(ctx, `SELECT number,close_reason FROM incidents.incidents WHERE id=$1`, openIncidentID).Scan(&openNumber, &openReason); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT number,close_reason,closed_at FROM incidents.incidents WHERE id=$1`, closedIncidentID).Scan(&closedNumber, &closedReason, &closeTime); err != nil {
		t.Fatal(err)
	}
	if openNumber <= 0 || closedNumber <= 0 || openNumber == closedNumber || openReason != "" || closedReason != "recovered" || closeTime == nil {
		t.Fatalf("existing incident migration state open=(%d,%q) closed=(%d,%q,%v)", openNumber, openReason, closedNumber, closedReason, closeTime)
	}
	for _, table := range []string{"notifications.messenger_links", "notifications.messenger_destinations", "notifications.messenger_routes", "notifications.messenger_deliveries", "notifications.messenger_reservations"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Errorf("messenger table %s exists=%t err=%v", table, exists, err)
		}
	}
	for version, checksum := range previousChecksums {
		var after string
		if err := pool.QueryRow(ctx, `SELECT checksum FROM public.schema_migrations WHERE version=$1`, version).Scan(&after); err != nil || after != checksum {
			t.Errorf("legacy migration %s checksum changed: before=%s after=%s err=%v", version, checksum, after, err)
		}
	}
}

func TestAdminConsoleMigrationPreservesPriorStateAndAddsVersionedMFA(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	previousChecksums := map[string]string{}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	for _, name := range names {
		if name >= "0012_admin_console.sql" {
			break
		}
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		version := strings.TrimSuffix(name, ".sql")
		checksum := hex.EncodeToString(sum[:])
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, version, checksum); err != nil {
			t.Fatalf("record migration %s: %v", name, err)
		}
		previousChecksums[version] = checksum
	}
	if len(previousChecksums) != 11 {
		t.Fatalf("seeded %d migrations before 0012, want 11", len(previousChecksums))
	}
	const accountID = "a0000000-0000-4000-8000-000000000012"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Pre-admin-console account')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason) VALUES($1,3,now()+interval '1 day','mock_test','legacy grant')`, accountID); err != nil {
		t.Fatalf("seed preexisting entitlement: %v", err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply admin console migration: %v", err)
	}
	var accountVersion int64
	var source, reason string
	var limit int
	if err := pool.QueryRow(ctx, `SELECT a.version,e.source,e.reason,e.monitor_limit FROM accounts.accounts a JOIN billing.entitlements e ON e.account_id=a.id WHERE a.id=$1`, accountID).Scan(&accountVersion, &source, &reason, &limit); err != nil {
		t.Fatal(err)
	}
	if accountVersion != 1 || source != "mock_test" || reason != "legacy grant" || limit != 3 {
		t.Fatalf("migration changed existing account/entitlement: version=%d source=%q reason=%q limit=%d", accountVersion, source, reason, limit)
	}
	for _, table := range []string{"auth.mfa_factors", "auth.mfa_enrollments", "auth.mfa_recovery_codes", "admin.audit_events"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s missing after 0012: exists=%t err=%v", table, exists, err)
		}
	}
	for version, checksum := range previousChecksums {
		var after string
		if err := pool.QueryRow(ctx, `SELECT checksum FROM public.schema_migrations WHERE version=$1`, version).Scan(&after); err != nil || after != checksum {
			t.Errorf("prior migration %s checksum changed: before=%s after=%s err=%v", version, checksum, after, err)
		}
	}
}

func TestSchedulingMigrationPreservesExistingMonitorsAndDefaults(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	legacyChecksums := map[string]string{
		"0001_accounts_auth":         "794604f3dd9531bf8c0947c6c5a082d94d76eeb625795a37a31620bfe3dc5ba8",
		"0002_monitoring":            "3f382b3a54ce15d7d4adec740aaa02547f6e9397f3f40c613090031621622f97",
		"0003_incident_close_reason": "56139e1ad19b05b16c0c75b696d7c273e055a69d2c84843c6293742919bf56b4",
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_accounts_auth.sql", "0002_monitoring.sql", "0003_incident_close_reason.sql"} {
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		if want := legacyChecksums[strings.TrimSuffix(name, ".sql")]; hex.EncodeToString(sum[:]) != want {
			t.Fatalf("legacy migration %s changed: checksum=%s want=%s", name, hex.EncodeToString(sum[:]), want)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, strings.TrimSuffix(name, ".sql"), hex.EncodeToString(sum[:])); err != nil {
			t.Fatalf("record legacy migration %s: %v", name, err)
		}
	}

	accountID := "a0000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Legacy monitor owner')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status) VALUES
 ('b0000000-0000-4000-8000-000000000001',$1,'legacy','https://legacy.example/health','legacy.example:443','GET',200)`, accountID); err != nil {
		t.Fatalf("seed monitor before scheduling migration: %v", err)
	}
	var previous map[string]string = make(map[string]string)
	rows, err := pool.Query(ctx, `SELECT version,checksum FROM public.schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		previous[version] = checksum
	}
	rows.Close()
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply scheduling migration: %v", err)
	}
	var interval, threshold int
	if err := pool.QueryRow(ctx, `SELECT interval_minutes,failure_threshold FROM monitors.monitors WHERE id='b0000000-0000-4000-8000-000000000001'`).Scan(&interval, &threshold); err != nil || interval != 1 || threshold != 2 {
		t.Fatalf("legacy monitor scheduling=%d/%d err=%v, want 1/2", interval, threshold, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status) VALUES
 ('b0000000-0000-4000-8000-000000000002',$1,'new','https://new.example/health','new.example:443','GET',200)`, accountID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT interval_minutes,failure_threshold FROM monitors.monitors WHERE id='b0000000-0000-4000-8000-000000000002'`).Scan(&interval, &threshold); err != nil || interval != 5 || threshold != 2 {
		t.Fatalf("new monitor scheduling=%d/%d err=%v, want DB defaults 5/2", interval, threshold, err)
	}
	for version, checksum := range previous {
		var after string
		if err := pool.QueryRow(ctx, `SELECT checksum FROM public.schema_migrations WHERE version=$1`, version).Scan(&after); err != nil || after != checksum {
			t.Errorf("legacy migration %s checksum changed: before=%s after=%s err=%v", version, checksum, after, err)
		}
	}
}

func TestApplyRejectsChangedMigrationChecksum(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if err := Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.schema_migrations SET checksum='changed'"); err != nil {
		t.Fatal(err)
	}
	err := Apply(ctx, pool)
	if err == nil || !strings.Contains(err.Error(), "checksum differs") {
		t.Fatalf("Apply() error = %v, want checksum drift error", err)
	}
}

func TestApplyRejectsAppliedMigrationMissingFromCode(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if err := Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO public.schema_migrations (version,checksum) VALUES ('0000_removed.sql','old')"); err != nil {
		t.Fatal(err)
	}
	err := Apply(ctx, pool)
	if err == nil || !strings.Contains(err.Error(), "missing from code") {
		t.Fatalf("Apply() error = %v, want removed migration error", err)
	}
}

func TestApplyRollsBackPartialMigration(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA auth; CREATE VIEW auth.credentials AS SELECT 1 AS incompatible"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool); err == nil {
		t.Fatal("Apply() error = nil, want migration failure caused by conflicting view")
	}
	var accountsSchema, ledgerExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regnamespace('accounts') IS NOT NULL, to_regclass('public.schema_migrations') IS NOT NULL").Scan(&accountsSchema, &ledgerExists); err != nil {
		t.Fatal(err)
	}
	if accountsSchema || ledgerExists {
		t.Fatalf("partial migration was not rolled back: accounts schema=%t ledger=%t", accountsSchema, ledgerExists)
	}
}
