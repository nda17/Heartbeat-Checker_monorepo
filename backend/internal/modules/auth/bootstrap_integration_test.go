package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
)

func TestBootstrapLocalCreatesAdminCredentialsAndFundsLocalWallet(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	accountModule := accounts.New(f.pool)
	billingModule := billing.New(f.pool, true)
	input := BootstrapInput{Email: "local-admin@example.test", Name: "Local Admin", Password: "local admin secure password"}

	id, err := BootstrapLocal(ctx, f.pool, input,
		func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
			account, err := accountModule.CreateLocalAdmin(ctx, tx, name)
			return account.ID, err
		},
		func(ctx context.Context, tx pgx.Tx, accountID string) error {
			return billingModule.CreditLocalTx(ctx, tx, accountID, 1000, "local admin bootstrap integration test")
		},
	)
	if err != nil {
		t.Fatalf("bootstrap local admin: %v", err)
	}

	var name, role, status, email string
	var passwordHash, kind string
	var amount int
	if err := f.pool.QueryRow(ctx, `SELECT a.name,a.role,a.status,c.email,c.password_hash,l.amount_minor,l.kind
		FROM accounts.accounts a JOIN auth.credentials c ON c.account_id=a.id
		JOIN billing.wallet_ledger l ON l.account_id=a.id WHERE a.id=$1`, id).
		Scan(&name, &role, &status, &email, &passwordHash, &amount, &kind); err != nil {
		t.Fatal(err)
	}
	if name != input.Name || role != "admin" || status != "active" || email != input.Email || passwordHash == "" || passwordHash == input.Password || amount != 1000 || kind != "local_credit" {
		t.Fatal("wrong local admin, credential or initial credit")
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
	if err := billing.New(f.pool, true).CreditLocal(ctx, accountID, 7, "existing bootstrap fixture"); err != nil {
		t.Fatalf("fund test wallet: %v", err)
	}

	type snapshot struct {
		name, role, status, hash, units string
		version                         int64
		sessions, entries               int
	}
	read := func() snapshot {
		var v snapshot
		if err := f.pool.QueryRow(ctx, `SELECT a.name,a.role,a.status,c.password_hash,c.version,
			w.available_units::text,(SELECT count(*) FROM billing.wallet_ledger WHERE account_id=a.id),
			(SELECT count(*) FROM auth.sessions WHERE account_id=a.id AND revoked_at IS NULL)
			FROM accounts.accounts a JOIN auth.credentials c ON c.account_id=a.id
			JOIN billing.wallets w ON w.account_id=a.id WHERE a.id=$1`, accountID).
			Scan(&v.name, &v.role, &v.status, &v.hash, &v.version, &v.units, &v.entries, &v.sessions); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
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

	if read() != before {
		t.Fatal("bootstrap changed an existing user, their sessions or their balance")
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
	grantFailure := errors.New("injected credit failure")
	var createdID string
	_, err := BootstrapLocal(ctx, f.pool, BootstrapInput{Email: "rollback-bootstrap@example.test", Name: "Rollback Admin", Password: "rollback secure password"},
		func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
			account, err := accounts.New(f.pool).CreateLocalAdmin(ctx, tx, name)
			createdID = account.ID
			return account.ID, err
		},
		func(ctx context.Context, tx pgx.Tx, accountID string) error {
			if err := billing.New(f.pool, true).CreditLocalTx(ctx, tx, accountID, 1000, "rollback integration test"); err != nil {
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
		(SELECT count(*) FROM billing.wallet_ledger WHERE account_id=$1),
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
					return billing.New(f.pool, true).CreditLocalTx(ctx, tx, accountID, 1000, "concurrent bootstrap integration test")
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
		(SELECT count(*) FROM billing.wallet_ledger WHERE account_id IN (SELECT account_id FROM auth.credentials WHERE email=$1))`, email).
		Scan(&accountCount, &credentialCount, &entitlementCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 1 || credentialCount != 1 || entitlementCount != 1 {
		t.Fatalf("concurrent bootstrap row counts: accounts=%d credentials=%d entitlements=%d", accountCount, credentialCount, entitlementCount)
	}
}
