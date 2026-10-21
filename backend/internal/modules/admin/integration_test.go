package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/migrations"
	"golang.org/x/crypto/argon2"
)

const adminTestOrigin = "http://localhost:3000"
const adminTestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

type adminFixture struct {
	pool    *pgxpool.Pool
	handler http.Handler
}

type adminTestCaptcha struct{}

func (adminTestCaptcha) Verify(context.Context, string, string, string) error { return nil }

type adminTestMailer struct{}

func (adminTestMailer) Send(context.Context, string, string, authcontracts.MailContent) error {
	return nil
}

type oauthAuditTestMailer struct{ content authcontracts.MailContent }

func (m *oauthAuditTestMailer) Send(_ context.Context, _ string, _ string, content authcontracts.MailContent) error {
	m.content = content
	return nil
}

type oauthAuditTestProvider struct{}

func (oauthAuditTestProvider) ID() string { return "yandex" }
func (oauthAuditTestProvider) AuthorizationURL(state, _ string) string {
	return "https://provider.example/authorize?state=" + url.QueryEscape(state)
}
func (oauthAuditTestProvider) Exchange(_ context.Context, code, verifier, deviceID, state string) (auth.OAuthIdentityProfile, error) {
	if code != "fake-code" || verifier == "" || deviceID != "fake-device" || state == "" {
		return auth.OAuthIdentityProfile{}, fmt.Errorf("invalid synthetic callback")
	}
	return auth.OAuthIdentityProfile{Subject: "synthetic-audit-subject", Email: "oauth-audit@example.test", Name: "Synthetic OAuth"}, nil
}

type adminTestActor struct {
	id    string
	token string
	csrf  string
	name  string
	email string
	role  string
	mfa   bool
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	baseURL := os.Getenv("HEARTBEAT_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set HEARTBEAT_TEST_DATABASE_URL to the isolated local PostgreSQL server URL")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host != "127.0.0.1:55432" {
		t.Fatal("HEARTBEAT_TEST_DATABASE_URL must target local Compose PostgreSQL at 127.0.0.1:55432")
	}
	adminConn, err := pgx.Connect(context.Background(), baseURL)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("hc_admin_test_%d", time.Now().UnixNano())
	if _, err := adminConn.Exec(context.Background(), "CREATE DATABASE "+dbName); err != nil {
		_ = adminConn.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, _ = adminConn.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()", dbName)
		_, _ = adminConn.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName)
		_ = adminConn.Close(ctx)
	})
	u.Path = "/" + dbName
	pool, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(context.Background(), pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	accountModule := accounts.New(pool)
	billingModule := billing.New(pool, false)
	incidentModule := incidents.New(pool)
	monitorModule := monitors.New(pool, billingModule, incidentModule, accountModule)
	authModule, err := auth.New(pool, accountModule, adminTestCaptcha{}, adminTestMailer{}, auth.Config{Origin: adminTestOrigin, MFAEncryptionKey: adminTestKey, MFAAudit: AuditMFA})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	authModule.Register(mux)
	New(pool, accountModule, authModule, billingModule, monitorModule).Register(mux, func(next http.Handler) http.Handler {
		return authModule.RequireRole("admin", next)
	})
	return &adminFixture{pool: pool, handler: mux}
}

func TestOAuthRegistrationAuditFailureRollsBackAccountAndIdentity(t *testing.T) {
	f := newAdminFixture(t)
	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION admin.reject_oauth_registered_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='oauth.registered' THEN RAISE EXCEPTION 'synthetic audit trigger'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_oauth_registered_audit BEFORE INSERT ON admin.audit_events FOR EACH ROW EXECUTE FUNCTION admin.reject_oauth_registered_audit()`); err != nil {
		t.Fatal(err)
	}
	mailer := &oauthAuditTestMailer{}
	oauthModule, err := auth.New(f.pool, accounts.New(f.pool), adminTestCaptcha{}, mailer, auth.Config{
		Origin: adminTestOrigin, ChallengeTTL: time.Hour, ResetTTL: time.Hour, RateLimitWindow: time.Hour,
		OAuthProviders: map[string]auth.OAuthProvider{"yandex": oauthAuditTestProvider{}}, OAuthAudit: AuditOAuth,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	oauthModule.Register(mux)
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/yandex/start", strings.NewReader(`{"mode":"login","returnTo":"/app"}`))
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("Origin", adminTestOrigin)
	startW := httptest.NewRecorder()
	mux.ServeHTTP(startW, startReq)
	if startW.Code != http.StatusOK {
		t.Fatalf("OAuth start status=%d body=%s", startW.Code, startW.Body.String())
	}
	var startBody struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if err := json.Unmarshal(startW.Body.Bytes(), &startBody); err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(startBody.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	var attempt *http.Cookie
	for _, cookie := range startW.Result().Cookies() {
		if cookie.Name == "hc_oauth_attempt" {
			attempt = cookie
		}
	}
	if attempt == nil || authorize.Query().Get("state") == "" {
		t.Fatalf("OAuth start missing state/cookie: %#v %q", attempt, authorize.Query().Get("state"))
	}
	callbackReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/yandex/callback?code=fake-code&state="+url.QueryEscape(authorize.Query().Get("state"))+"&device_id=fake-device", nil)
	callbackReq.AddCookie(attempt)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)
	var registration *http.Cookie
	for _, cookie := range callbackW.Result().Cookies() {
		if cookie.Name == "hc_oauth_registration" {
			registration = cookie
		}
	}
	if registration == nil {
		t.Fatalf("OAuth callback missing registration proof: %v", callbackW.Result().Cookies())
	}
	var token string
	for _, word := range strings.Fields(mailer.content.Text) {
		if strings.Contains(word, "verify-email?oauth=1") {
			u, err := url.Parse(strings.TrimSuffix(word, "."))
			if err == nil {
				token = u.Query().Get("token")
				if token == "" {
					fragment, _ := url.ParseQuery(u.Fragment)
					token = fragment.Get("token")
				}
			}
		}
	}
	if token == "" {
		t.Fatal("OAuth confirmation token was not captured in the synthetic mail")
	}
	confirmReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/registration/confirm", strings.NewReader(`{"token":"`+token+`"}`))
	confirmReq.Header.Set("Content-Type", "application/json")
	confirmReq.Header.Set("Origin", adminTestOrigin)
	confirmReq.AddCookie(registration)
	confirmW := httptest.NewRecorder()
	mux.ServeHTTP(confirmW, confirmReq)
	if confirmW.Code == http.StatusOK {
		t.Fatal("OAuth confirmation succeeded although real AuditOAuth insert was rejected")
	}
	var credentials, identities, consumed, auditRows int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.credentials WHERE email='oauth-audit@example.test'").Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_identities WHERE subject='synthetic-audit-subject'").Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.registration_challenges WHERE oauth_subject='synthetic-audit-subject' AND consumed_at IS NOT NULL").Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM admin.audit_events WHERE action='oauth.registered'").Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if credentials != 0 || identities != 0 || consumed != 0 || auditRows != 0 {
		t.Fatalf("real audit failure left partial state: credentials=%d identities=%d consumed=%d audit=%d", credentials, identities, consumed, auditRows)
	}
}

func (f *adminFixture) seedActor(t *testing.T, accountID, sessionID, token, name, email, role string, mfa bool) adminTestActor {
	t.Helper()
	tokenHash := sha256.Sum256([]byte(token))
	csrfMAC := hmac.New(sha256.New, []byte(token))
	_, _ = csrfMAC.Write([]byte("heartbeat-csrf-v1"))
	csrf := base64.RawURLEncoding.EncodeToString(csrfMAC.Sum(nil))
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO accounts.accounts(id,name,role,status) VALUES($1,$2,$3,'active')`, accountID, name, role); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.credentials(account_id,email,password_hash,version) VALUES($1,$2,'synthetic-test-hash',1)`, accountID, email); err != nil {
		t.Fatal(err)
	}
	if mfa {
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.mfa_factors(account_id,secret_cipher,version,last_used_counter) VALUES($1,$2,1,0)`, accountID, []byte{1, 2, 3}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,mfa_version,mfa_verified_until,mfa_step_up_until,expires_at,user_agent)
		VALUES($1,$2,$3,1,$4,$5,$6,now()+interval '1 hour','admin integration test')`, sessionID, accountID, tokenHash[:], func() any {
		if mfa {
			return 1
		}
		return nil
	}(), func() any {
		if mfa {
			return time.Now().Add(12 * time.Hour)
		}
		return nil
	}(), func() any {
		if mfa {
			return time.Now().Add(5 * time.Minute)
		}
		return nil
	}())
	if err != nil {
		t.Fatal(err)
	}
	return adminTestActor{id: accountID, token: token, csrf: csrf, name: name, email: email, role: role, mfa: mfa}
}

func adminTestPasswordHash(t *testing.T, password string) string {
	t.Helper()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
}

func (f *adminFixture) request(t *testing.T, actor adminTestActor, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body strings.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = *strings.NewReader(string(data))
	} else {
		body = *strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, &body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", adminTestOrigin)
		req.Header.Set("X-CSRF-Token", actor.csrf)
	}
	req.AddCookie(&http.Cookie{Name: "hc_session", Value: actor.token, Path: "/"})
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminAccountNameChangeAuditCapturesBeforeAndAfter(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.seedActor(t, "a1000000-0000-4000-8000-000000000001", "b1000000-0000-4000-8000-000000000001", strings.Repeat("A", 43), "Admin", "admin-name-audit@example.test", "admin", true)
	target := f.seedActor(t, "a1000000-0000-4000-8000-000000000002", "b1000000-0000-4000-8000-000000000002", strings.Repeat("B", 43), "Before Name", "name-audit-target@example.test", "user", false)
	const reason = "correct account display name"

	updated := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{
		"version": 1,
		"name":    "After Name",
		"reason":  reason,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("name-only account update status=%d body=%s", updated.Code, updated.Body.String())
	}
	var response struct {
		Name    string `json:"name"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &response); err != nil || response.Name != "After Name" || response.Role != "user" || response.Status != "active" || response.Version != 2 {
		t.Fatalf("name-only update response=%+v err=%v", response, err)
	}

	var before, after []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT before_state,after_state FROM admin.audit_events WHERE target_account_id=$1 AND action='account.updated' AND reason=$2`, target.id, reason).Scan(&before, &after); err != nil {
		t.Fatalf("load account.updated audit event: %v", err)
	}
	var beforeState, afterState struct {
		Name    string `json:"name"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(before, &beforeState); err != nil {
		t.Fatalf("decode before_state: %v", err)
	}
	if err := json.Unmarshal(after, &afterState); err != nil {
		t.Fatalf("decode after_state: %v", err)
	}
	if beforeState.Name != "Before Name" || beforeState.Role != "user" || beforeState.Status != "active" || beforeState.Version != 1 {
		t.Fatalf("unexpected before_state: %+v", beforeState)
	}
	if afterState.Name != "After Name" || afterState.Role != "user" || afterState.Status != "active" || afterState.Version != 2 {
		t.Fatalf("unexpected after_state: %+v", afterState)
	}
}

func TestAdminUnexpectedMFAFenceDatabaseErrorIsServerFailure(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.seedActor(t, "a2000000-0000-4000-8000-000000000001", "b2000000-0000-4000-8000-000000000001", strings.Repeat("A", 43), "Admin", "admin-fence-error@example.test", "admin", true)
	target := f.seedActor(t, "a2000000-0000-4000-8000-000000000002", "b2000000-0000-4000-8000-000000000002", strings.Repeat("B", 43), "Target User", "fence-error-target@example.test", "user", false)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE auth.mfa_factors RENAME TO mfa_factors_unavailable`); err != nil {
		t.Fatal(err)
	}

	got := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{
		"version": 1,
		"name":    "Must Not Be Saved",
		"reason":  "unexpected MFA fence database error",
	})
	if got.Code < http.StatusInternalServerError {
		t.Fatalf("unexpected MFA verification database error status=%d body=%s, want 5xx", got.Code, got.Body.String())
	}
	var name string
	if err := f.pool.QueryRow(context.Background(), `SELECT name FROM accounts.accounts WHERE id=$1`, target.id).Scan(&name); err != nil || name != "Target User" {
		t.Fatalf("unexpected MFA verification error changed account name=%q err=%v", name, err)
	}
	var auditRows int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin.audit_events WHERE target_account_id=$1 AND action='account.updated'`, target.id).Scan(&auditRows); err != nil || auditRows != 0 {
		t.Fatalf("unexpected MFA verification error left account.updated audit rows=%d err=%v", auditRows, err)
	}
}

func TestAdminMFAFenceVersionedMutationsAndAuditAtomicity(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.seedActor(t, "a0000000-0000-4000-8000-000000000001", "b0000000-0000-4000-8000-000000000001", strings.Repeat("A", 43), "Admin", "admin@example.test", "admin", true)
	noMFA := f.seedActor(t, "a0000000-0000-4000-8000-000000000002", "b0000000-0000-4000-8000-000000000002", strings.Repeat("B", 43), "Unverified Admin", "no-mfa@example.test", "admin", false)
	target := f.seedActor(t, "a0000000-0000-4000-8000-000000000003", "b0000000-0000-4000-8000-000000000003", strings.Repeat("C", 43), "Target User", "target@example.test", "user", false)
	rollback := f.seedActor(t, "a0000000-0000-4000-8000-000000000004", "b0000000-0000-4000-8000-000000000004", strings.Repeat("D", 43), "Rollback User", "rollback@example.test", "user", false)
	race := f.seedActor(t, "a0000000-0000-4000-8000-000000000005", "b0000000-0000-4000-8000-000000000005", strings.Repeat("E", 43), "Login Race User", "login-race@example.test", "user", false)
	const racePassword = "Synthetic-Race-Password-2026!"
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.credentials SET password_hash=$2 WHERE account_id=$1`, race.id, adminTestPasswordHash(t, racePassword)); err != nil {
		t.Fatal(err)
	}
	// Login and an administrative account revocation acquire related row locks
	// through different paths. This race must finish without a lock-order deadlock.
	type raceResult struct {
		login *httptest.ResponseRecorder
		block *httptest.ResponseRecorder
	}
	results := make(chan raceResult, 2)
	start := make(chan struct{})
	go func() {
		<-start
		results <- raceResult{login: f.request(t, race, http.MethodPost, "/api/v1/auth/login", map[string]any{"email": race.email, "password": racePassword, "turnstileToken": "synthetic"})}
	}()
	go func() {
		<-start
		results <- raceResult{block: f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+race.id, map[string]any{"version": 1, "status": "blocked", "reason": "race login with account revoke"})}
	}()
	close(start)
	var raced raceResult
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for raced.login == nil || raced.block == nil {
		select {
		case result := <-results:
			if result.login != nil {
				raced.login = result.login
			}
			if result.block != nil {
				raced.block = result.block
			}
		case <-deadline.C:
			t.Fatal("concurrent login and administrative account block deadlocked")
		}
	}
	if raced.block.Code != http.StatusOK {
		t.Fatalf("account block in login race status=%d body=%s", raced.block.Code, raced.block.Body.String())
	}
	if raced.login.Code != http.StatusOK && raced.login.Code != http.StatusUnauthorized {
		t.Fatalf("login in account block race status=%d body=%s", raced.login.Code, raced.login.Body.String())
	}
	if raced.login.Code == http.StatusOK {
		var sessionCookie *http.Cookie
		for _, candidate := range raced.login.Result().Cookies() {
			if candidate.Name == "hc_session" {
				sessionCookie = candidate
			}
		}
		if sessionCookie == nil || f.request(t, adminTestActor{token: sessionCookie.Value}, http.MethodGet, "/api/v1/me", nil).Code != http.StatusUnauthorized {
			t.Fatal("session created during account block race remained authorized")
		}
	}
	if got := f.request(t, admin, http.MethodGet, "/api/v1/admin/accounts", nil); got.Code != http.StatusOK {
		t.Fatalf("admin read with current MFA status=%d body=%s", got.Code, got.Body.String())
	}
	if got := f.request(t, noMFA, http.MethodGet, "/api/v1/admin/accounts", nil); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "mfa_enrollment_required") {
		t.Fatalf("admin read without MFA status=%d body=%s", got.Code, got.Body.String())
	}
	if got := f.request(t, target, http.MethodGet, "/api/v1/admin/accounts", nil); got.Code != http.StatusForbidden {
		t.Fatalf("ordinary user accessed admin API status=%d body=%s", got.Code, got.Body.String())
	}
	if got := f.request(t, admin, http.MethodGet, "/api/v1/admin/accounts?limit=101", nil); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid account page status=%d body=%s", got.Code, got.Body.String())
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.sessions SET mfa_step_up_until=now()-interval '1 second' WHERE id=$1`, "b0000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if got := f.request(t, admin, http.MethodGet, "/api/v1/admin/accounts", nil); got.Code != http.StatusOK {
		t.Fatalf("read with valid 12h MFA but expired step-up status=%d body=%s", got.Code, got.Body.String())
	}
	stepUpDenied := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{"version": 1, "role": "admin", "reason": "requires current step-up"})
	if stepUpDenied.Code != http.StatusForbidden || !strings.Contains(stepUpDenied.Body.String(), "mfa_step_up_required") {
		t.Fatalf("write without fresh step-up status=%d body=%s", stepUpDenied.Code, stepUpDenied.Body.String())
	}
	var targetRole string
	if err := f.pool.QueryRow(context.Background(), `SELECT role FROM accounts.accounts WHERE id=$1`, target.id).Scan(&targetRole); err != nil || targetRole != "user" {
		t.Fatalf("step-up failure mutated target role=%q err=%v", targetRole, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.sessions SET mfa_step_up_until=now()+interval '5 minutes',mfa_verified_until=now()-interval '1 second' WHERE id=$1`, "b0000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if got := f.request(t, admin, http.MethodGet, "/api/v1/admin/accounts", nil); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "mfa_required") {
		t.Fatalf("read after MFA verification expiry status=%d body=%s", got.Code, got.Body.String())
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.sessions SET mfa_step_up_until=now()+interval '5 minutes',mfa_verified_until=now()+interval '12 hours' WHERE id=$1`, "b0000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}

	promote := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{"version": 1, "role": "admin", "reason": "promote backup operator"})
	if promote.Code != http.StatusOK {
		t.Fatalf("promote target status=%d body=%s", promote.Code, promote.Body.String())
	}
	var promoted struct {
		Role    string `json:"role"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(promote.Body.Bytes(), &promoted); err != nil || promoted.Role != "admin" || promoted.Version != 2 {
		t.Fatalf("promotion=%+v err=%v", promoted, err)
	}
	if got := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{"version": 1, "status": "blocked", "reason": "stale edit"}); got.Code != http.StatusConflict {
		t.Fatalf("stale account edit status=%d body=%s", got.Code, got.Body.String())
	}
	demote := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{"version": 2, "role": "user", "reason": "revoke admin role and sessions"})
	if demote.Code != http.StatusOK {
		t.Fatalf("admin role revocation status=%d body=%s", demote.Code, demote.Body.String())
	}
	if got := f.request(t, target, http.MethodGet, "/api/v1/me", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("role-revoked account session remained valid status=%d body=%s", got.Code, got.Body.String())
	}
	for _, update := range []map[string]any{
		{"version": 1, "role": "user", "reason": "self demotion"},
		{"version": 1, "status": "blocked", "reason": "self block"},
	} {
		if got := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+admin.id, update); got.Code != http.StatusForbidden {
			t.Fatalf("self demotion/block status=%d body=%s", got.Code, got.Body.String())
		}
	}
	uppercaseSelf := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+strings.ToUpper(admin.id), map[string]any{"version": 1, "role": "user", "reason": "uppercase UUID bypass"})
	if uppercaseSelf.Code != http.StatusBadRequest && uppercaseSelf.Code != http.StatusNotFound {
		t.Fatalf("uppercase self UUID was not rejected status=%d body=%s", uppercaseSelf.Code, uppercaseSelf.Body.String())
	}
	var unchangedRole string
	if err := f.pool.QueryRow(context.Background(), `SELECT role FROM accounts.accounts WHERE id=$1`, admin.id).Scan(&unchangedRole); err != nil || unchangedRole != "admin" {
		t.Fatalf("uppercase UUID changed actor role=%q err=%v", unchangedRole, err)
	}

	validUntil := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	grant := f.request(t, admin, http.MethodPut, "/api/v1/admin/accounts/"+target.id+"/entitlement", map[string]any{"version": 0, "quota": 10, "validUntil": validUntil, "reason": "manual support grant"})
	if grant.Code != http.StatusOK {
		t.Fatalf("admin grant status=%d body=%s", grant.Code, grant.Body.String())
	}
	var grantResponse struct {
		Entitlement struct {
			Quota   int    `json:"quota"`
			Source  string `json:"source"`
			Version int64  `json:"version"`
		} `json:"entitlement"`
	}
	if err := json.Unmarshal(grant.Body.Bytes(), &grantResponse); err != nil || grantResponse.Entitlement.Quota != 10 || grantResponse.Entitlement.Source != "admin_grant" || grantResponse.Entitlement.Version != 1 {
		t.Fatalf("grant=%+v err=%v", grantResponse, err)
	}
	if got := f.request(t, admin, http.MethodPut, "/api/v1/admin/accounts/"+target.id+"/entitlement", map[string]any{"version": 0, "quota": 11, "validUntil": validUntil, "reason": "stale quota editor"}); got.Code != http.StatusConflict {
		t.Fatalf("stale entitlement edit status=%d body=%s", got.Code, got.Body.String())
	}
	audit := f.request(t, admin, http.MethodGet, "/api/v1/admin/audit?accountId="+target.id, nil)
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), "entitlement.granted") || !strings.Contains(audit.Body.String(), "account.updated") {
		t.Fatalf("audit did not record successful administration status=%d body=%s", audit.Code, audit.Body.String())
	}
	// Exercise the exact production MFA audit writer against PostgreSQL and
	// prove that its failure rolls back with the owning MFA transaction.
	auditTx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := AuditMFA(context.Background(), auditTx, admin.id, "b0000000-0000-4000-8000-000000000001", "mfa.enrollment_started", "totp", 1); err != nil {
		_ = auditTx.Rollback(context.Background())
		t.Fatalf("production MFA audit callback failed: %v", err)
	}
	if err := auditTx.Commit(context.Background()); err != nil {
		t.Fatalf("commit production MFA audit: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION public.reject_mfa_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action='mfa.enrollment_started' THEN RAISE EXCEPTION 'synthetic MFA audit failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `CREATE TRIGGER reject_mfa_test_audit BEFORE INSERT ON admin.audit_events FOR EACH ROW EXECUTE FUNCTION public.reject_mfa_test_audit()`); err != nil {
		t.Fatal(err)
	}
	auditTx, err = f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := AuditMFA(context.Background(), auditTx, admin.id, "b0000000-0000-4000-8000-000000000001", "mfa.enrollment_started", "totp", 1); err == nil {
		_ = auditTx.Rollback(context.Background())
		t.Fatal("production MFA audit callback ignored a database failure")
	}
	_ = auditTx.Rollback(context.Background())
	var mfaAuditRows int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin.audit_events WHERE actor_account_id=$1 AND action='mfa.enrollment_started'`, admin.id).Scan(&mfaAuditRows); err != nil || mfaAuditRows != 1 {
		t.Fatalf("MFA audit failure did not roll back atomically rows=%d err=%v", mfaAuditRows, err)
	}

	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION public.reject_admin_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action='entitlement.granted' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `CREATE TRIGGER reject_admin_test_audit BEFORE INSERT ON admin.audit_events FOR EACH ROW EXECUTE FUNCTION public.reject_admin_test_audit()`); err != nil {
		t.Fatal(err)
	}
	failedGrant := f.request(t, admin, http.MethodPut, "/api/v1/admin/accounts/"+rollback.id+"/entitlement", map[string]any{"version": 0, "quota": 5, "validUntil": validUntil, "reason": "must roll back with audit"})
	if failedGrant.Code < 500 {
		t.Fatalf("audit trigger response status=%d body=%s, want 5xx", failedGrant.Code, failedGrant.Body.String())
	}
	var entitlementRows, auditRows int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.admin_grants WHERE account_id=$1`, rollback.id).Scan(&entitlementRows); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin.audit_events WHERE target_account_id=$1 AND action='entitlement.granted'`, rollback.id).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if entitlementRows != 0 || auditRows != 0 {
		t.Fatalf("failed audit left mutation state entitlement=%d audit=%d", entitlementRows, auditRows)
	}

	block := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{"version": 3, "status": "blocked", "reason": "disable user and revoke sessions"})
	if block.Code != http.StatusOK {
		t.Fatalf("block target status=%d body=%s", block.Code, block.Body.String())
	}
	if got := f.request(t, target, http.MethodGet, "/api/v1/me", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("blocked account session remained valid status=%d body=%s", got.Code, got.Body.String())
	}
}
