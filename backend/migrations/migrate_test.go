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
