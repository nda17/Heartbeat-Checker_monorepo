package migrations

import (
	"context"
	"fmt"
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
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('auth.credentials') IS NOT NULL AND to_regclass('accounts.accounts') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected auth and accounts tables after applying migration")
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
