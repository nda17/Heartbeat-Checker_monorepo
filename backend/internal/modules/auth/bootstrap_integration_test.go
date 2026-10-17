package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
)

func TestBootstrapLocalCreatesAdminCredentialsAndMaximumLocalQuota(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	accountModule := accounts.New(f.pool)
	billingModule := billing.New(f.pool, true)
	until := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	input := BootstrapInput{Email: "local-admin@example.test", Name: "Local Admin", Password: "local admin secure password"}

	id, err := BootstrapLocal(ctx, f.pool, input,
		func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
			account, err := accountModule.CreateLocalAdmin(ctx, tx, name)
			return account.ID, err
		},
		func(ctx context.Context, tx pgx.Tx, accountID string) error {
			return billingModule.GrantLocalTx(ctx, tx, accountID, 1000, until, "local admin bootstrap integration test")
		},
	)
	if err != nil {
		t.Fatalf("bootstrap local admin: %v", err)
	}

	var name, role, status, email string
	var passwordHash, source, reason string
	var quota int
	var validUntil time.Time
	if err := f.pool.QueryRow(ctx, `SELECT a.name,a.role,a.status,c.email,c.password_hash,
		e.monitor_limit,e.valid_until,e.source,e.reason
		FROM accounts.accounts a
		JOIN auth.credentials c ON c.account_id=a.id
		JOIN billing.entitlements e ON e.account_id=a.id WHERE a.id=$1`, id).
		Scan(&name, &role, &status, &email, &passwordHash, &quota, &validUntil, &source, &reason); err != nil {
		t.Fatalf("read bootstrapped admin records: %v", err)
	}
	if name != input.Name || role != "admin" || status != "active" || email != input.Email {
		t.Fatalf("bootstrapped account = name %q role %q status %q email %q", name, role, status, email)
	}
	if passwordHash == "" || passwordHash == input.Password || quota != 1000 || source != "local_test" || reason == "" || !validUntil.Equal(until) {
		t.Fatalf("bootstrapped credential/entitlement = hash-present %t quota %d source %q reason-present %t until %s", passwordHash != "", quota, source, reason != "", validUntil)
	}

	cookie, _, login := f.login(t, input.Email, input.Password)
	if login.Code != http.StatusOK || cookie == nil {
		t.Fatalf("login with bootstrapped credentials status=%d body=%s", login.Code, login.Body.String())
	}
}

func TestBootstrapLocalExistingEmailPreservesAllExistingState(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const email = "existing-bootstrap@example.test"
	f.signup(t, email)
	cookie, _, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK || cookie == nil {
		t.Fatalf("fixture login status=%d", login.Code)
	}
	var accountID string
	if err := f.pool.QueryRow(ctx, "SELECT account_id FROM auth.credentials WHERE email=$1", email).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if err := billing.New(f.pool, true).GrantLocal(ctx, accountID, 7, time.Now().Add(24*time.Hour), "existing bootstrap fixture"); err != nil {
		t.Fatalf("grant fixture entitlement: %v", err)
	}

	var beforeName, beforeRole, beforeStatus, beforeHash string
	var beforeVersion int64
	var beforeQuota int
	var beforeSource, beforeReason string
	var beforeUntil time.Time
	var beforeSessions int
	if err := f.pool.QueryRow(ctx, `SELECT a.name,a.role,a.status,c.password_hash,c.version,
		e.monitor_limit,e.valid_until,e.source,e.reason,
		(SELECT count(*) FROM auth.sessions s WHERE s.account_id=a.id AND s.revoked_at IS NULL)
		FROM accounts.accounts a JOIN auth.credentials c ON c.account_id=a.id
		JOIN billing.entitlements e ON e.account_id=a.id WHERE a.id=$1`, accountID).
		Scan(&beforeName, &beforeRole, &beforeStatus, &beforeHash, &beforeVersion, &beforeQuota, &beforeUntil, &beforeSource, &beforeReason, &beforeSessions); err != nil {
		t.Fatal(err)
	}
	grantCalled := false
	_, err := BootstrapLocal(ctx, f.pool, BootstrapInput{Email: email, Name: "Replacement Admin", Password: "replacement secure password"},
		func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
			account, err := accounts.New(f.pool).CreateLocalAdmin(ctx, tx, name)
			return account.ID, err
		},
		func(context.Context, pgx.Tx, string) error {
			grantCalled = true
			return nil
		},
	)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("bootstrap existing email error=%v, want ErrAlreadyExists", err)
	}
	if grantCalled {
		t.Fatal("grant callback ran for an existing email")
	}

	var afterName, afterRole, afterStatus, afterHash string
	var afterVersion int64
	var afterQuota int
	var afterSource, afterReason string
	var afterUntil time.Time
	var afterSessions int
	if err := f.pool.QueryRow(ctx, `SELECT a.name,a.role,a.status,c.password_hash,c.version,
		e.monitor_limit,e.valid_until,e.source,e.reason,
		(SELECT count(*) FROM auth.sessions s WHERE s.account_id=a.id AND s.revoked_at IS NULL)
		FROM accounts.accounts a JOIN auth.credentials c ON c.account_id=a.id
		JOIN billing.entitlements e ON e.account_id=a.id WHERE a.id=$1`, accountID).
		Scan(&afterName, &afterRole, &afterStatus, &afterHash, &afterVersion, &afterQuota, &afterUntil, &afterSource, &afterReason, &afterSessions); err != nil {
		t.Fatal(err)
	}
	if beforeName != afterName || beforeRole != afterRole || beforeStatus != afterStatus || beforeHash != afterHash || beforeVersion != afterVersion || beforeQuota != afterQuota || !beforeUntil.Equal(afterUntil) || beforeSource != afterSource || beforeReason != afterReason || beforeSessions != afterSessions {
		t.Fatalf("existing account state changed: name=%t role=%t status=%t password_hash_equal=%t version=%t quota=%t expiry=%t source=%t reason=%t sessions=%t",
			beforeName == afterName, beforeRole == afterRole, beforeStatus == afterStatus, beforeHash == afterHash,
			beforeVersion == afterVersion, beforeQuota == afterQuota, beforeUntil.Equal(afterUntil), beforeSource == afterSource, beforeReason == afterReason, beforeSessions == afterSessions)
	}
	if session := f.request(t, http.MethodGet, "/api/v1/me", nil, cookie, "", "", ""); session.Code != http.StatusOK {
		t.Fatalf("existing session after conflict status=%d", session.Code)
	}
	if _, _, existingLogin := f.login(t, email, integrationPass); existingLogin.Code != http.StatusOK {
		t.Fatalf("original password stopped working after conflict: status=%d", existingLogin.Code)
	}
	if _, _, replacementLogin := f.login(t, email, "replacement secure password"); replacementLogin.Code != http.StatusUnauthorized {
		t.Fatalf("replacement password unexpectedly works: status=%d", replacementLogin.Code)
	}
}

func TestBootstrapLocalGrantFailureRollsBackAccountAndCredential(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	grantFailure := errors.New("injected entitlement failure")
	var createdID string
	_, err := BootstrapLocal(ctx, f.pool, BootstrapInput{Email: "rollback-bootstrap@example.test", Name: "Rollback Admin", Password: "rollback secure password"},
		func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
			account, err := accounts.New(f.pool).CreateLocalAdmin(ctx, tx, name)
			createdID = account.ID
			return account.ID, err
		},
		func(ctx context.Context, tx pgx.Tx, accountID string) error {
			if err := billing.New(f.pool, true).GrantLocalTx(ctx, tx, accountID, 1000, time.Now().Add(24*time.Hour), "rollback integration test"); err != nil {
				return err
			}
			return grantFailure
		},
	)
	if !errors.Is(err, grantFailure) {
		t.Fatalf("bootstrap grant failure=%v, want injected error", err)
	}
	var accountsCount, credentialsCount, entitlementsCount, sessionsCount int
	if err := f.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM accounts.accounts WHERE id=$1),
		(SELECT count(*) FROM auth.credentials WHERE account_id=$1 OR email=$2),
		(SELECT count(*) FROM billing.entitlements WHERE account_id=$1),
		(SELECT count(*) FROM auth.sessions WHERE account_id=$1)`, createdID, "rollback-bootstrap@example.test").
		Scan(&accountsCount, &credentialsCount, &entitlementsCount, &sessionsCount); err != nil {
		t.Fatal(err)
	}
	if accountsCount != 0 || credentialsCount != 0 || entitlementsCount != 0 || sessionsCount != 0 {
		t.Fatalf("failed bootstrap left rows: accounts=%d credentials=%d entitlements=%d sessions=%d", accountsCount, credentialsCount, entitlementsCount, sessionsCount)
	}
}

func TestBootstrapLocalConcurrentSameEmailCreatesExactlyOneAdmin(t *testing.T) {
	f := newAuthFixtureWithPoolSize(t, 4)
	const email = "concurrent-bootstrap@example.test"
	start := make(chan struct{})
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			input := BootstrapInput{Email: email, Name: []string{"Concurrent Admin A", "Concurrent Admin B"}[i], Password: "concurrent secure password"}
			id, err := BootstrapLocal(context.Background(), f.pool, input,
				func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
					account, err := accounts.New(f.pool).CreateLocalAdmin(ctx, tx, name)
					return account.ID, err
				},
				func(ctx context.Context, tx pgx.Tx, accountID string) error {
					return billing.New(f.pool, true).GrantLocalTx(ctx, tx, accountID, 1000, time.Now().Add(24*time.Hour), "concurrent bootstrap integration test")
				},
			)
			results <- result{id: id, err: err}
		}(i)
	}
	close(start)

	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		got := <-results
		switch {
		case got.err == nil:
			successes++
		case errors.Is(got.err, ErrAlreadyExists):
			conflicts++
		default:
			t.Fatalf("concurrent bootstrap failed unexpectedly: %v", got.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent bootstrap results: successes=%d conflicts=%d, want one each", successes, conflicts)
	}

	var accountCount, credentialCount, entitlementCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM accounts.accounts WHERE name IN ('Concurrent Admin A','Concurrent Admin B')),
		(SELECT count(*) FROM auth.credentials WHERE email=$1),
		(SELECT count(*) FROM billing.entitlements WHERE account_id IN (SELECT account_id FROM auth.credentials WHERE email=$1))`, email).
		Scan(&accountCount, &credentialCount, &entitlementCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 1 || credentialCount != 1 || entitlementCount != 1 {
		t.Fatalf("concurrent bootstrap row counts: accounts=%d credentials=%d entitlements=%d", accountCount, credentialCount, entitlementCount)
	}
}
