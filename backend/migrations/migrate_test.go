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

func TestSeparateAdminGrantsMovesOnlyAdminRowsAndPreservesPaymentHistory(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0025_separate_admin_grants.sql" {
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
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, strings.TrimSuffix(name, ".sql"), hex.EncodeToString(sum[:])); err != nil {
			t.Fatalf("record migration %s: %v", name, err)
		}
	}
	const liveAdmin = "a2500000-0000-4000-8000-000000000001"
	const expiredAdmin = "a2500000-0000-4000-8000-000000000002"
	const paid = "a2500000-0000-4000-8000-000000000003"
	const local = "a2500000-0000-4000-8000-000000000004"
	const mock = "a2500000-0000-4000-8000-000000000005"
	for _, account := range []struct{ id, name string }{
		{liveAdmin, "Live admin grant migration fixture"},
		{expiredAdmin, "Expired admin grant migration fixture"},
		{paid, "Paid fixture migration"},
		{local, "Local fixture migration"},
		{mock, "Mock fixture migration"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,$2)`, account.id, account.name); err != nil {
			t.Fatalf("insert account %s: %v", account.name, err)
		}
	}
	seed := `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,version,granted_at,reason,renewal_anchor_day)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`
	grantedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, row := range []struct {
		id, source, reason string
		quota              int
		version            int
		until              time.Time
		anchor             int
	}{
		{liveAdmin, "admin_grant", "preserve live grant", 17, 4, time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC), 0},
		{expiredAdmin, "admin_grant", "preserve expired grant", 9, 8, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), 0},
		{paid, "yookassa_test", "keep paid renewal", 3, 12, time.Date(2027, 2, 3, 4, 5, 6, 0, time.UTC), 31},
		{local, "local_test", "keep local fixture", 2, 6, time.Date(2027, 3, 4, 5, 6, 7, 0, time.UTC), 0},
		{mock, "mock_test", "keep mock fixture", 4, 10, time.Date(2027, 4, 5, 6, 7, 8, 0, time.UTC), 0},
	} {
		var anchor any
		if row.anchor != 0 {
			anchor = row.anchor
		}
		if _, err := pool.Exec(ctx, seed, row.id, row.quota, row.until, row.source, row.version, grantedAt, row.reason, anchor); err != nil {
			t.Fatalf("seed %s entitlement: %v", row.source, err)
		}
	}
	const orderID = "b2500000-0000-4000-8000-000000000001"
	reportCreated := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor)
		VALUES($1,$2,'c2500000-0000-4000-8000-000000000001','mock',3,8700)`, orderID, paid); err != nil {
		t.Fatalf("seed historical payment order: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO billing.payment_reports(order_id,created_at) VALUES($1,$2)`, orderID, reportCreated); err != nil {
		t.Fatalf("seed historical payment report: %v", err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply admin-grant separation migration: %v", err)
	}
	for _, expected := range []struct {
		id, reason string
		quota      int
		version    int
		until      time.Time
	}{
		{liveAdmin, "preserve live grant", 17, 4, time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)},
		{expiredAdmin, "preserve expired grant", 9, 8, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)},
	} {
		var reason string
		var quota int
		var version int
		var until time.Time
		if err := pool.QueryRow(ctx, `SELECT monitor_limit,valid_until,version,reason FROM billing.admin_grants WHERE account_id=$1`, expected.id).Scan(&quota, &until, &version, &reason); err != nil {
			t.Fatalf("read moved admin grant: %v", err)
		}
		if quota != expected.quota || version != expected.version || !until.Equal(expected.until) || reason != expected.reason {
			t.Errorf("moved grant changed: quota=%d version=%d until=%s reason=%q", quota, version, until, reason)
		}
	}
	for _, expected := range []struct {
		id, source, reason     string
		quota, version, anchor int
	}{
		{paid, "yookassa_test", "keep paid renewal", 3, 12, 31},
		{local, "local_test", "keep local fixture", 2, 6, 0},
		{mock, "mock_test", "keep mock fixture", 4, 10, 0},
	} {
		var source, reason string
		var quota, version int
		var anchor *int16
		if err := pool.QueryRow(ctx, `SELECT monitor_limit,source,version,reason,renewal_anchor_day FROM billing.entitlements WHERE account_id=$1`, expected.id).Scan(&quota, &source, &version, &reason, &anchor); err != nil {
			t.Fatalf("read retained %s entitlement: %v", expected.source, err)
		}
		if quota != expected.quota || source != expected.source || version != expected.version || reason != expected.reason || (expected.anchor == 0 && anchor != nil) || (expected.anchor != 0 && (anchor == nil || int(*anchor) != expected.anchor)) {
			t.Errorf("retained entitlement changed: source=%s quota=%d version=%d reason=%q anchor=%v", source, quota, version, reason, anchor)
		}
	}
	var adminRows, paymentRows, orderCount, reportCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.admin_grants`).Scan(&adminRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.entitlements`).Scan(&paymentRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.orders WHERE id=$1 AND status='pending'`, orderID).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.payment_reports WHERE order_id=$1 AND created_at=$2`, orderID, reportCreated).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if adminRows != 2 || paymentRows != 3 || orderCount != 1 || reportCount != 1 {
		t.Fatalf("migration row counts admin=%d payment=%d order=%d report=%d", adminRows, paymentRows, orderCount, reportCount)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason) VALUES($1,1,now()+interval '1 day','admin_grant','must use separate table')`, local); err == nil {
		t.Fatal("post-migration entitlement source check still accepts admin_grant")
	}
}

func TestIndependentPackagesMigrationPreservesLegacyEntitlementAndBlockedOrder(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0026_independent_packages.sql" {
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
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, strings.TrimSuffix(name, ".sql"), hex.EncodeToString(sum[:])); err != nil {
			t.Fatalf("record migration %s: %v", name, err)
		}
	}
	const accountID = "a2600000-0000-4000-8000-000000000001"
	const orderID = "a2600000-0000-4000-8000-000000000002"
	const idempotencyKey = "a2600000-0000-4000-8000-000000000003"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Independent package migration fixture')`, accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	legacyExpiry := time.Date(2027, time.January, 31, 10, 11, 12, 123000, time.UTC)
	legacyGranted := time.Date(2026, time.October, 20, 9, 8, 7, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,version,granted_at,reason,renewal_anchor_day)
		VALUES($1,4,$2,'yookassa_test',7,$3,'legacy paid package',31)`, accountID, legacyExpiry, legacyGranted); err != nil {
		t.Fatalf("insert legacy entitlement: %v", err)
	}
	completed := time.Date(2026, time.October, 21, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO billing.orders(
		id,account_id,idempotency_key,provider,status,monitor_count,amount_minor,completed_at,fulfillment_status,fulfillment_reason,reconciliation_status,provider_mode,provider_shop_id,provider_request)
		VALUES($1,$2,$3,'yookassa','succeeded',4,9600,$4,'blocked','admin_grant','complete','test','123456','{}'::bytea)`,
		orderID, accountID, idempotencyKey, completed); err != nil {
		t.Fatalf("insert terminal blocked order: %v", err)
	}

	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply independent packages migration: %v", err)
	}
	var packageID, packageSource, packageReason string
	var packageQuota, packageVersion, anchor int
	var packageUntil, packageGranted time.Time
	var packageOrigin *string
	if err := pool.QueryRow(ctx, `SELECT id::text,monitor_limit,valid_until,source,version,granted_at,renewal_anchor_day,reason,origin_order_id::text
		FROM billing.entitlements WHERE account_id=$1`, accountID).
		Scan(&packageID, &packageQuota, &packageUntil, &packageSource, &packageVersion, &packageGranted, &anchor, &packageReason, &packageOrigin); err != nil {
		t.Fatalf("read migrated entitlement: %v", err)
	}
	if packageID != accountID || packageQuota != 4 || !packageUntil.Equal(legacyExpiry) || packageSource != "yookassa_test" || packageVersion != 7 || !packageGranted.Equal(legacyGranted) || anchor != 31 || packageReason != "legacy paid package" || packageOrigin != nil {
		t.Fatalf("legacy entitlement changed during package migration: id=%s quota=%d until=%s source=%s version=%d granted=%s anchor=%d reason=%s origin=%v", packageID, packageQuota, packageUntil, packageSource, packageVersion, packageGranted, anchor, packageReason, packageOrigin)
	}
	var status, fulfillment, reason, purchaseKind string
	var renewPackage, fulfilledPackage *string
	var orderCount int
	var monitorCount int
	var amount int64
	var providerRequest []byte
	if err := pool.QueryRow(ctx, `SELECT status,fulfillment_status,fulfillment_reason,purchase_kind,renew_package_id::text,fulfilled_package_id::text,monitor_count,amount_minor,provider_request
		FROM billing.orders WHERE id=$1`, orderID).
		Scan(&status, &fulfillment, &reason, &purchaseKind, &renewPackage, &fulfilledPackage, &monitorCount, &amount, &providerRequest); err != nil {
		t.Fatalf("read migrated blocked order: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.entitlements WHERE account_id=$1`, accountID).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || fulfillment != "blocked" || reason != "admin_grant" || purchaseKind != "legacy" || renewPackage != nil || fulfilledPackage != nil || monitorCount != 4 || amount != 9600 || string(providerRequest) != "{}" || orderCount != 1 {
		t.Fatalf("terminal order or package rows changed during migration: status=%s fulfillment=%s reason=%s kind=%s renew=%v fulfilled=%v count=%d amount=%d request=%q rows=%d", status, fulfillment, reason, purchaseKind, renewPackage, fulfilledPackage, monitorCount, amount, providerRequest, orderCount)
	}
}

func TestMonitorMetadataMigrationBackfillsLegacyTimezoneAndCheckConfigVersion(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	for _, name := range names {
		if name >= "0023_monitor_metadata.sql" {
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
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, strings.TrimSuffix(name, ".sql"), hex.EncodeToString(sum[:])); err != nil {
			t.Fatalf("record legacy migration %s: %v", name, err)
		}
	}
	const accountID = "a2300000-0000-4000-8000-000000000001"
	const monitorID = "b2300000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Legacy timezone owner')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status,version) VALUES($1,$2,'Legacy timezone monitor','https://migration.example.test/health','migration.example.test:443','GET',200,7)`, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply metadata migration after legacy monitor exists: %v", err)
	}
	var zone, target string
	var checkConfigVersion int64
	if err := pool.QueryRow(ctx, `SELECT m.time_zone,m.check_config_version,p.target_host FROM monitors.monitors m JOIN monitors.provider_lookups p ON p.monitor_id=m.id WHERE m.id=$1`, monitorID).Scan(&zone, &checkConfigVersion, &target); err != nil {
		t.Fatal(err)
	}
	if zone != "Europe/Moscow" || checkConfigVersion != 7 || target != "migration.example.test" {
		t.Fatalf("legacy migration values zone=%q checkConfigVersion=%d providerTarget=%q", zone, checkConfigVersion, target)
	}
}

func TestMultiProviderMigrationFailsOnlyAttemptedLegacyDeliveries(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE public.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0024_multi_provider_push.sql" {
			break
		}
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		if _, err := pool.Exec(ctx, `INSERT INTO public.schema_migrations(version,checksum) VALUES($1,$2)`, strings.TrimSuffix(name, ".sql"), hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	const accountID = "a2400000-0000-4000-8000-000000000001"
	const monitorID = "b2400000-0000-4000-8000-000000000001"
	const incidentID = "c2400000-0000-4000-8000-000000000001"
	const installationID = "d2400000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Legacy push owner')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status) VALUES($1,$2,'Legacy push monitor','https://migration.example.test/health','migration.example.test:443','GET',200)`, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at) VALUES($1,$2,$3,now(),now())`, incidentID, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO notifications.push_installations(id,credential_hash,account_id,provider,device_label,app_version) VALUES($1,$2,$3,'rustore','Legacy test','1.1.3')`, installationID, make([]byte, 32), accountID); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, status string
		attempts   int
	}{
		{"e2400000-0000-4000-8000-000000000001", "pending", 1},
		{"e2400000-0000-4000-8000-000000000002", "leased", 2},
		{"e2400000-0000-4000-8000-000000000003", "pending", 0},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO notifications.push_deliveries(id,event_id,account_id,monitor_id,incident_id,installation_id,kind,sequence,policy_generation,binding_generation,registration_version,encrypted_snapshot,status,attempts,lease_until,attempt_token,deadline_at) VALUES($1,$1,$2,$3,$4,$5,'down',1,1,1,1,$6,$7,$8,CASE WHEN $7='leased' THEN now()+interval '1 minute' END,CASE WHEN $7='leased' THEN $9::uuid END,now()+interval '1 day')`, row.id, accountID, monitorID, incidentID, installationID, []byte("snapshot"), row.status, row.attempts, "f2400000-0000-4000-8000-000000000001")
		if err != nil {
			t.Fatalf("seed legacy delivery %s: %v", row.id, err)
		}
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("apply multi-provider migration: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT status,error_code,attempt_token IS NULL,lease_until IS NULL,delivery_provider IS NULL,encrypted_destination IS NULL FROM notifications.push_deliveries ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var results []struct {
		status, reason                                        string
		attemptNull, leaseNull, providerNull, destinationNull bool
	}
	for rows.Next() {
		var value struct {
			status, reason                                        string
			attemptNull, leaseNull, providerNull, destinationNull bool
		}
		if err := rows.Scan(&value.status, &value.reason, &value.attemptNull, &value.leaseNull, &value.providerNull, &value.destinationNull); err != nil {
			t.Fatal(err)
		}
		results = append(results, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("migrated delivery count = %d, want 3", len(results))
	}
	for i := 0; i < 2; i++ {
		if results[i].status != "failed" || results[i].reason != "route_migration" || !results[i].attemptNull || !results[i].leaseNull || !results[i].providerNull || !results[i].destinationNull {
			t.Errorf("attempted legacy delivery %d = %+v, want failed route_migration with cleared lease and no frozen route", i, results[i])
		}
	}
	if results[2].status != "pending" || results[2].reason != "" || !results[2].attemptNull || !results[2].leaseNull || !results[2].providerNull || !results[2].destinationNull {
		t.Errorf("unattempted legacy delivery = %+v, want pending and eligible for first route", results[2])
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
