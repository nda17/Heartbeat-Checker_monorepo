package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/migrations"
)

const (
	integrationOrigin = "http://localhost:3000"
	integrationPass   = "original secure password"
	integrationToken  = "test-captcha-token"
)

type integrationCaptcha struct{}

func (integrationCaptcha) Verify(_ context.Context, token, _ string, _ string) error {
	if token != integrationToken {
		return errors.New("unexpected test captcha token")
	}
	return nil
}

type capturedMail struct {
	to, subject string
	body        contracts.MailContent
}

type integrationMailer struct {
	mu       sync.Mutex
	messages []capturedMail
}

func (m *integrationMailer) Send(_ context.Context, to, subject string, body contracts.MailContent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, capturedMail{to: to, subject: subject, body: body})
	return nil
}

func (m *integrationMailer) token(t *testing.T, to, path string, occurrence int) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []capturedMail
	for _, message := range m.messages {
		if message.to == to && strings.Contains(message.body.Text, path) {
			found = append(found, message)
		}
	}
	if occurrence < 0 || occurrence >= len(found) {
		t.Fatalf("email %q has %d messages for %q, requested %d", to, len(found), path, occurrence)
	}
	fields := strings.Fields(found[occurrence].body.Text)
	for _, field := range fields {
		if !strings.Contains(field, path) {
			continue
		}
		link := strings.TrimSuffix(field, ".")
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("parse token link %q: %v", link, err)
		}
		token := u.Query().Get("token")
		if token == "" {
			fragment, err := url.ParseQuery(u.Fragment)
			if err == nil {
				token = fragment.Get("token")
			}
		}
		if token == "" {
			t.Fatalf("message link %q has no token", link)
		}
		return token
	}
	t.Fatalf("message for %q does not include %q token link", to, path)
	return ""
}

type authFixture struct {
	pool   *pgxpool.Pool
	module *Module
	h      http.Handler
	mail   *integrationMailer
}

func newAuthFixture(t *testing.T) *authFixture {
	return newAuthFixtureWithPoolSize(t, 1)
}

func newAuthFixtureWithPoolSize(t *testing.T, maxConns int32) *authFixture {
	return newAuthFixtureWithPoolSizeAndPrefix(t, maxConns, "hc_auth_test_")
}

func newAuthFixtureWithPoolSizeAndPrefix(t *testing.T, maxConns int32, dbPrefix string) *authFixture {
	return newAuthFixtureWithPoolSizeAndPrefixAndDeletionFence(t, maxConns, dbPrefix, nil)
}

func newAuthFixtureWithPoolSizeAndPrefixAndDeletionFence(t *testing.T, maxConns int32, dbPrefix string, afterFence func()) *authFixture {
	t.Helper()
	baseURL := os.Getenv("HEARTBEAT_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set HEARTBEAT_TEST_DATABASE_URL to the isolated local PostgreSQL server URL")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host != "127.0.0.1:55432" {
		t.Fatal("HEARTBEAT_TEST_DATABASE_URL must target local Compose PostgreSQL at 127.0.0.1:55432")
	}
	admin, err := pgx.Connect(context.Background(), baseURL)
	if err != nil {
		t.Fatalf("connect to local test PostgreSQL: %v", err)
	}
	dbName := fmt.Sprintf("%s%d", dbPrefix, time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+dbName); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("create isolated auth test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()", dbName)
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName)
		_ = admin.Close(ctx)
	})
	u.Path = "/" + dbName
	poolConfig, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("parse isolated auth test database URL: %v", err)
	}
	poolConfig.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatalf("connect to isolated auth test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(context.Background(), pool); err != nil {
		t.Fatalf("apply auth test migrations: %v", err)
	}
	mailer := &integrationMailer{}
	deletion := newTestAccountDeletion(pool)
	deletion.afterFence = afterFence
	module, err := New(pool, accounts.New(pool), integrationCaptcha{}, mailer, Config{
		Origin:          integrationOrigin,
		ChallengeTTL:    time.Hour,
		ResetTTL:        time.Hour,
		RateLimitWindow: time.Hour,
		AccountDeletion: deletion,
	})
	if err != nil {
		t.Fatalf("create auth module: %v", err)
	}
	return &authFixture{pool: pool, module: module, h: module.Handler(), mail: mailer}
}

func (f *authFixture) request(t *testing.T, method, path string, payload any, cookie *http.Cookie, csrf, origin, remoteIP string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(data))
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if remoteIP == "" {
		remoteIP = "192.0.2.10"
	}
	req.RemoteAddr = netJoin(remoteIP, "43125")
	recorder := httptest.NewRecorder()
	f.h.ServeHTTP(recorder, req)
	return recorder
}

func (f *authFixture) concurrentVerify(token, password string) int {
	response := f.concurrentRequest(http.MethodPost, "/api/v1/auth/verify", map[string]string{"token": token, "password": password}, nil, "")
	return response.Code
}

func (f *authFixture) concurrentRequest(method, path string, payload any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", integrationOrigin)
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	req.RemoteAddr = netJoin("192.0.2.10", "43125")
	recorder := httptest.NewRecorder()
	f.h.ServeHTTP(recorder, req)
	return recorder
}

func netJoin(ip, port string) string { return ip + ":" + port }

func (f *authFixture) register(t *testing.T, email, name, password string) string {
	return f.registerOccurrence(t, email, name, password, 0)
}

func (f *authFixture) registerOccurrence(t *testing.T, email, name, password string, occurrence int) string {
	t.Helper()
	response := f.request(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "name": name, "password": password, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("register %q: status=%d body=%s", email, response.Code, response.Body.String())
	}
	return f.mail.token(t, email, "/verify-email", occurrence)
}

func (f *authFixture) verify(t *testing.T, token, password string) *httptest.ResponseRecorder {
	t.Helper()
	return f.request(t, http.MethodPost, "/api/v1/auth/verify", map[string]string{"token": token, "password": password}, nil, "", integrationOrigin, "")
}

type loginResponse struct {
	CSRFToken string `json:"csrfToken"`
	Account   struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"account"`
}

func (f *authFixture) login(t *testing.T, email, password string) (*http.Cookie, string, *httptest.ResponseRecorder) {
	return f.loginWithCaptcha(t, email, password, integrationToken)
}

func (f *authFixture) loginWithCaptcha(t *testing.T, email, password, captchaToken string) (*http.Cookie, string, *httptest.ResponseRecorder) {
	t.Helper()
	response := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password, "turnstileToken": captchaToken,
	}, nil, "", integrationOrigin, "")
	if response.Code != http.StatusOK {
		return nil, "", response
	}
	var out loginResponse
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "hc_session" {
			return cookie, out.CSRFToken, response
		}
	}
	t.Fatalf("login response did not set session cookie: %v", response.Result().Cookies())
	return nil, "", response
}

func (f *authFixture) loginWithCookie(t *testing.T, email, password string, previous *http.Cookie) (*http.Cookie, string, *httptest.ResponseRecorder) {
	t.Helper()
	response := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password, "turnstileToken": integrationToken,
	}, previous, "", integrationOrigin, "")
	if response.Code != http.StatusOK {
		return nil, "", response
	}
	var out loginResponse
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode replacement login response: %v", err)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "hc_session" {
			return cookie, out.CSRFToken, response
		}
	}
	t.Fatalf("replacement login response did not set a session cookie")
	return nil, "", response
}

func (f *authFixture) signup(t *testing.T, email string) {
	t.Helper()
	token := f.register(t, email, "Test User", integrationPass)
	response := f.verify(t, token, integrationPass)
	if response.Code != http.StatusNoContent {
		t.Fatalf("verify %q: status=%d body=%s", email, response.Code, response.Body.String())
	}
}

func (f *authFixture) sessionID(t *testing.T, cookie *http.Cookie) string {
	t.Helper()
	response := f.request(t, http.MethodGet, "/api/v1/me/sessions", nil, cookie, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list sessions: status=%d body=%s", response.Code, response.Body.String())
	}
	var out struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, item := range out.Sessions {
		if item.Current {
			return item.ID
		}
	}
	t.Fatal("session list has no current session")
	return ""
}

func TestIntegrationRegistrationVerificationAndOpaqueLoginSession(t *testing.T) {
	f := newAuthFixture(t)
	const email = "alice@example.com"
	token := f.register(t, email, "Alice", integrationPass)

	wrongLogin := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": integrationPass, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if wrongLogin.Code != http.StatusUnauthorized {
		t.Fatalf("unverified login status=%d, want 401", wrongLogin.Code)
	}
	badVerify := f.verify(t, token, "incorrect registration password")
	if badVerify.Code != http.StatusBadRequest {
		t.Fatalf("verify with wrong password status=%d, want 400", badVerify.Code)
	}
	verified := f.verify(t, token, integrationPass)
	if verified.Code != http.StatusNoContent {
		t.Fatalf("verify with correct password status=%d body=%s", verified.Code, verified.Body.String())
	}
	replayed := f.verify(t, token, integrationPass)
	if replayed.Code != http.StatusBadRequest {
		t.Fatalf("replayed verification status=%d, want 400", replayed.Code)
	}
	duplicate := f.request(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "name": "Alice Again", "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if duplicate.Code != http.StatusAccepted {
		t.Fatalf("duplicate registration status=%d, want opaque 202", duplicate.Code)
	}
	badLogin := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": "wrong password", "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password login status=%d, want 401", badLogin.Code)
	}
	cookie, csrf, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK || cookie == nil || csrf == "" {
		t.Fatalf("valid login status=%d body=%s", login.Code, login.Body.String())
	}
	if len(cookie.Value) != 43 || strings.Contains(cookie.Value, email) || !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("session cookie has unsafe or unexpected attributes: %#v", cookie)
	}
	var stored bool
	if err := f.pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM auth.sessions WHERE token_hash=$1)", security.Digest(cookie.Value)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored {
		t.Fatal("session token hash was not stored")
	}
	if strings.Contains(f.mail.messages[0].body.Text, integrationPass) {
		t.Fatal("registration email exposed the password")
	}
	var me struct {
		Account struct {
			Role string `json:"role"`
		} `json:"account"`
	}
	response := f.request(t, http.MethodGet, "/api/v1/me", nil, cookie, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /me status=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Account.Role != "user" {
		t.Fatalf("public signup account role = %q, want user", me.Account.Role)
	}
}

func TestLoginReplacementRevokesOnlyExactPriorCookieAfterSuccessfulAuthentication(t *testing.T) {
	f := newAuthFixture(t)
	const ownerEmail = "prior-cookie-owner@example.test"
	const targetEmail = "prior-cookie-target@example.test"
	f.signup(t, ownerEmail)
	f.signup(t, targetEmail)
	prior, _, priorLogin := f.login(t, ownerEmail, integrationPass)
	other, _, otherLogin := f.login(t, ownerEmail, integrationPass)
	if priorLogin.Code != http.StatusOK || otherLogin.Code != http.StatusOK || prior == nil || other == nil {
		t.Fatalf("create prior sessions: first=%d second=%d", priorLogin.Code, otherLogin.Code)
	}

	failed := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": targetEmail, "password": "incorrect password", "turnstileToken": integrationToken}, prior, "", integrationOrigin, "")
	if failed.Code != http.StatusUnauthorized {
		t.Fatalf("failed account-switch login status=%d, want 401", failed.Code)
	}
	if got := f.request(t, http.MethodGet, "/api/v1/me", nil, prior, "", "", "").Code; got != http.StatusOK {
		t.Fatalf("failed login revoked prior cookie: /me status=%d, want 200", got)
	}

	switched, _, response := f.loginWithCookie(t, targetEmail, integrationPass, prior)
	if response.Code != http.StatusOK || switched == nil {
		t.Fatalf("successful account-switch login status=%d body=%s", response.Code, response.Body.String())
	}
	if got := f.request(t, http.MethodGet, "/api/v1/me", nil, prior, "", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("successful login did not revoke exact prior cookie: status=%d, want 401", got)
	}
	if got := f.request(t, http.MethodGet, "/api/v1/me", nil, other, "", "", "").Code; got != http.StatusOK {
		t.Fatalf("successful login revoked unrelated session: status=%d, want 200", got)
	}
	if got := f.request(t, http.MethodGet, "/api/v1/me", nil, switched, "", "", "").Code; got != http.StatusOK {
		t.Fatalf("replacement session status=%d, want 200", got)
	}
}

func TestIntegrationExpiredVerificationAndOriginCSRFGuards(t *testing.T) {
	f := newAuthFixture(t)
	lockedToken := f.register(t, "locked@example.com", "Locked User", integrationPass)
	for attempt := 0; attempt < 5; attempt++ {
		response := f.verify(t, lockedToken, "incorrect registration password")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("wrong verification attempt %d status=%d, want 400", attempt+1, response.Code)
		}
	}
	locked := f.verify(t, lockedToken, integrationPass)
	if locked.Code != http.StatusBadRequest {
		t.Fatalf("correct password after five failed verification attempts status=%d, want 400", locked.Code)
	}
	token := f.register(t, "guard@example.com", "Guard User", integrationPass)
	if _, err := f.pool.Exec(context.Background(), "UPDATE auth.registration_challenges SET expires_at=now()-interval '1 second' WHERE token_hash=$1", security.Digest(token)); err != nil {
		t.Fatal(err)
	}
	if response := f.verify(t, token, integrationPass); response.Code != http.StatusBadRequest {
		t.Fatalf("expired verification status=%d, want 400", response.Code)
	}
	badOrigin := f.request(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": "blocked@example.com", "name": "Blocked", "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", "https://attacker.example", "")
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin registration status=%d, want 403", badOrigin.Code)
	}
	f.signup(t, "csrf@example.com")
	cookie, csrf, response := f.login(t, "csrf@example.com", integrationPass)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	noCSRF := f.request(t, http.MethodPatch, "/api/v1/me", map[string]string{"name": "Changed Name"}, cookie, "", integrationOrigin, "")
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("PATCH without CSRF status=%d, want 403", noCSRF.Code)
	}
	wrongOrigin := f.request(t, http.MethodPatch, "/api/v1/me", map[string]string{"name": "Changed Name"}, cookie, csrf, "https://attacker.example", "")
	if wrongOrigin.Code != http.StatusForbidden {
		t.Fatalf("PATCH with wrong origin status=%d, want 403", wrongOrigin.Code)
	}
	goodOrigin := f.request(t, http.MethodPatch, "/api/v1/me", map[string]string{"name": "Changed Name"}, cookie, csrf, integrationOrigin, "")
	if goodOrigin.Code != http.StatusOK {
		t.Fatalf("PATCH with valid origin+CSRF status=%d body=%s", goodOrigin.Code, goodOrigin.Body.String())
	}
}

func TestIntegrationSessionIsolationAndLogout(t *testing.T) {
	f := newAuthFixture(t)
	f.signup(t, "alice@example.com")
	f.signup(t, "bob@example.com")
	alice1, aliceCSRF, _ := f.login(t, "alice@example.com", integrationPass)
	alice2, alice2CSRF, _ := f.login(t, "alice@example.com", integrationPass)
	bob, _, _ := f.login(t, "bob@example.com", integrationPass)
	if alice1 == nil || alice2 == nil || bob == nil {
		t.Fatal("expected three authenticated sessions")
	}
	bobID := f.sessionID(t, bob)
	aliceList := f.request(t, http.MethodGet, "/api/v1/me/sessions", nil, alice1, "", "", "")
	if aliceList.Code != http.StatusOK {
		t.Fatalf("Alice session list status=%d", aliceList.Code)
	}
	var aliceSessions struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(aliceList.Body.Bytes(), &aliceSessions); err != nil {
		t.Fatal(err)
	}
	currentCount := 0
	for _, item := range aliceSessions.Sessions {
		if item.ID == bobID {
			t.Fatal("Alice session list disclosed Bob's session")
		}
		if item.Current {
			currentCount++
		}
	}
	if len(aliceSessions.Sessions) != 2 || currentCount != 1 {
		t.Fatalf("Alice sees %d sessions and %d current, want two and one", len(aliceSessions.Sessions), currentCount)
	}
	foreign := f.request(t, http.MethodDelete, "/api/v1/me/sessions/"+bobID, nil, alice1, aliceCSRF, integrationOrigin, "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-account session revoke status=%d, want 404", foreign.Code)
	}
	if stillValid := f.request(t, http.MethodGet, "/api/v1/me", nil, bob, "", "", ""); stillValid.Code != http.StatusOK {
		t.Fatalf("foreign session was affected: GET /me status=%d", stillValid.Code)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE auth.sessions SET expires_at=now()-interval '1 second' WHERE id=$1", bobID); err != nil {
		t.Fatal(err)
	}
	if expired := f.request(t, http.MethodGet, "/api/v1/me", nil, bob, "", "", ""); expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired session GET /me status=%d, want 401", expired.Code)
	}
	aliceID := f.sessionID(t, alice1)
	revoked := f.request(t, http.MethodDelete, "/api/v1/me/sessions/"+aliceID, nil, alice1, aliceCSRF, integrationOrigin, "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("own session revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}
	if stale := f.request(t, http.MethodGet, "/api/v1/me", nil, alice1, "", "", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session GET /me status=%d, want 401", stale.Code)
	}
	all := f.request(t, http.MethodDelete, "/api/v1/me/sessions", nil, alice2, alice2CSRF, integrationOrigin, "")
	if all.Code != http.StatusNoContent {
		t.Fatalf("revoke all sessions status=%d body=%s", all.Code, all.Body.String())
	}
	if stale := f.request(t, http.MethodGet, "/api/v1/me", nil, alice2, "", "", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("revoke all left active session: status=%d", stale.Code)
	}
	logoutCookie, logoutCSRF, _ := f.login(t, "bob@example.com", integrationPass)
	logout := f.request(t, http.MethodPost, "/api/v1/auth/logout", nil, logoutCookie, logoutCSRF, integrationOrigin, "")
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	if stale := f.request(t, http.MethodGet, "/api/v1/me", nil, logoutCookie, "", "", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("logout left session active: status=%d", stale.Code)
	}
}

func TestIntegrationPasswordResetExpiryReplayAndPasswordChange(t *testing.T) {
	f := newAuthFixture(t)
	const email = "password@example.com"
	f.signup(t, email)
	oldSession, _, _ := f.login(t, email, integrationPass)
	requestReset := func() *httptest.ResponseRecorder {
		return f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{"email": email, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	}
	if response := requestReset(); response.Code != http.StatusAccepted {
		t.Fatalf("reset request status=%d", response.Code)
	}
	expiredToken := f.mail.token(t, email, "/reset-password", 0)
	if _, err := f.pool.Exec(context.Background(), "UPDATE auth.password_reset_tokens SET expires_at=now()-interval '1 second' WHERE token_hash=$1", security.Digest(expiredToken)); err != nil {
		t.Fatal(err)
	}
	expired := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/confirm", map[string]string{"token": expiredToken, "password": "new secure password"}, nil, "", integrationOrigin, "")
	if expired.Code != http.StatusBadRequest {
		t.Fatalf("expired reset status=%d, want 400", expired.Code)
	}
	if response := requestReset(); response.Code != http.StatusAccepted {
		t.Fatalf("second reset request status=%d", response.Code)
	}
	resetToken := f.mail.token(t, email, "/reset-password", 1)
	reset := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/confirm", map[string]string{"token": resetToken, "password": "new secure password"}, nil, "", integrationOrigin, "")
	if reset.Code != http.StatusNoContent {
		t.Fatalf("valid password reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	replay := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/confirm", map[string]string{"token": resetToken, "password": "another secure password"}, nil, "", integrationOrigin, "")
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed password reset status=%d, want 400", replay.Code)
	}
	if stale := f.request(t, http.MethodGet, "/api/v1/me", nil, oldSession, "", "", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("password reset left previous session valid: %d", stale.Code)
	}
	if old := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": integrationPass, "turnstileToken": integrationToken}, nil, "", integrationOrigin, ""); old.Code != http.StatusUnauthorized {
		t.Fatalf("old password login after reset status=%d, want 401", old.Code)
	}
	changedCookie, csrf, changedLogin := f.login(t, email, "new secure password")
	if changedLogin.Code != http.StatusOK {
		t.Fatalf("new password login status=%d", changedLogin.Code)
	}
	badChange := f.request(t, http.MethodPost, "/api/v1/auth/password/change", map[string]string{"currentPassword": "wrong current password", "newPassword": "changed secure password"}, changedCookie, csrf, integrationOrigin, "")
	if badChange.Code != http.StatusUnauthorized {
		t.Fatalf("password change with wrong current password status=%d, want 401", badChange.Code)
	}
	goodChange := f.request(t, http.MethodPost, "/api/v1/auth/password/change", map[string]string{"currentPassword": "new secure password", "newPassword": "changed secure password"}, changedCookie, csrf, integrationOrigin, "")
	if goodChange.Code != http.StatusNoContent {
		t.Fatalf("password change status=%d body=%s", goodChange.Code, goodChange.Body.String())
	}
	if stale := f.request(t, http.MethodGet, "/api/v1/me", nil, changedCookie, "", "", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("password change left session valid: %d", stale.Code)
	}
	if old := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": "new secure password", "turnstileToken": integrationToken}, nil, "", integrationOrigin, ""); old.Code != http.StatusUnauthorized {
		t.Fatalf("pre-change password login status=%d, want 401", old.Code)
	}
	if _, _, login := f.login(t, email, "changed secure password"); login.Code != http.StatusOK {
		t.Fatalf("new changed password login status=%d body=%s", login.Code, login.Body.String())
	}
}

func TestIntegrationPasswordResetRacingLoginLeavesNoOldSessionValid(t *testing.T) {
	f := newAuthFixtureWithPoolSize(t, 4)
	const email = "reset-race@example.com"
	f.signup(t, email)
	requestReset := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{"email": email, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if requestReset.Code != http.StatusAccepted {
		t.Fatalf("reset request status=%d", requestReset.Code)
	}
	token := f.mail.token(t, email, "/reset-password", 0)
	results := make(chan struct {
		login *httptest.ResponseRecorder
		reset *httptest.ResponseRecorder
	}, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		login := f.concurrentRequest(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": integrationPass, "turnstileToken": integrationToken}, nil, "")
		results <- struct {
			login *httptest.ResponseRecorder
			reset *httptest.ResponseRecorder
		}{login: login}
	}()
	go func() {
		defer wg.Done()
		reset := f.concurrentRequest(http.MethodPost, "/api/v1/auth/password/reset/confirm", map[string]string{"token": token, "password": "fresh secure password"}, nil, "")
		results <- struct {
			login *httptest.ResponseRecorder
			reset *httptest.ResponseRecorder
		}{reset: reset}
	}()
	wg.Wait()
	close(results)
	var loginResponse, resetResponse *httptest.ResponseRecorder
	for result := range results {
		if result.login != nil {
			loginResponse = result.login
		}
		if result.reset != nil {
			resetResponse = result.reset
		}
	}
	if resetResponse == nil || resetResponse.Code != http.StatusNoContent {
		t.Fatalf("racing reset status=%v, want 204", resetResponse)
	}
	if loginResponse == nil || (loginResponse.Code != http.StatusOK && loginResponse.Code != http.StatusUnauthorized) {
		t.Fatalf("racing old-password login status=%v, want 200 or 401", loginResponse)
	}
	if loginResponse.Code == http.StatusOK {
		var cookie *http.Cookie
		for _, candidate := range loginResponse.Result().Cookies() {
			if candidate.Name == "hc_session" {
				cookie = candidate
			}
		}
		if cookie == nil {
			t.Fatal("successful racing login did not set a session cookie")
		}
		stale := f.request(t, http.MethodGet, "/api/v1/me", nil, cookie, "", "", "")
		if stale.Code != http.StatusUnauthorized {
			t.Fatalf("racing old-password session remained valid: GET /me=%d", stale.Code)
		}
	}
	if _, _, login := f.login(t, email, "fresh secure password"); login.Code != http.StatusOK {
		t.Fatalf("new password login after race status=%d body=%s", login.Code, login.Body.String())
	}
}

func TestIntegrationRateLimitsAndAdminFailClosed(t *testing.T) {
	f := newAuthFixture(t)
	for i := 0; i < 5; i++ {
		response := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{"email": "limited@example.com", "turnstileToken": integrationToken}, nil, "", integrationOrigin, "198.51.100.8")
		if response.Code != http.StatusAccepted {
			t.Fatalf("email rate-limit request %d status=%d", i+1, response.Code)
		}
	}
	perEmail := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{"email": "limited@example.com", "turnstileToken": integrationToken}, nil, "", integrationOrigin, "198.51.100.8")
	if perEmail.Code != http.StatusTooManyRequests {
		t.Fatalf("per-email limit status=%d, want 429", perEmail.Code)
	}
	for i := 0; i < 20; i++ {
		email := fmt.Sprintf("ip-%02d@example.com", i)
		response := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{"email": email, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "198.51.100.9")
		if response.Code != http.StatusAccepted {
			t.Fatalf("IP rate-limit request %d status=%d", i+1, response.Code)
		}
	}
	perIP := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{"email": "ip-over@example.com", "turnstileToken": integrationToken}, nil, "", integrationOrigin, "198.51.100.9")
	if perIP.Code != http.StatusTooManyRequests {
		t.Fatalf("per-IP limit status=%d, want 429", perIP.Code)
	}
	role := f.request(t, http.MethodPost, "/api/v1/auth/register", map[string]string{"email": "self-admin@example.com", "name": "Self Admin", "password": integrationPass, "role": "admin", "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if role.Code != http.StatusBadRequest {
		t.Fatalf("public role assignment status=%d, want 400", role.Code)
	}
	f.signup(t, "admin@example.com")
	var accountID string
	if err := f.pool.QueryRow(context.Background(), "SELECT account_id FROM auth.credentials WHERE email='admin@example.com'").Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE accounts.accounts SET role='admin' WHERE id=$1", accountID); err != nil {
		t.Fatal(err)
	}
	cookie, _, login := f.login(t, "admin@example.com", integrationPass)
	if login.Code != http.StatusOK {
		t.Fatalf("admin fixture login status=%d", login.Code)
	}
	protected := f.module.RequireRole("admin", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.AddCookie(cookie)
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("admin route without MFA status=%d, want 403", response.Code)
	}
	blockedCookie, _, _ := f.login(t, "admin@example.com", integrationPass)
	if _, err := f.pool.Exec(context.Background(), "UPDATE accounts.accounts SET status='blocked' WHERE id=$1", accountID); err != nil {
		t.Fatal(err)
	}
	blockedLogin := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "admin@example.com", "password": integrationPass, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if blockedLogin.Code != http.StatusUnauthorized {
		t.Fatalf("blocked account login status=%d, want 401", blockedLogin.Code)
	}
	blockedMe := f.request(t, http.MethodGet, "/api/v1/me", nil, blockedCookie, "", "", "")
	if blockedMe.Code != http.StatusUnauthorized {
		t.Fatalf("blocked account session status=%d, want 401", blockedMe.Code)
	}
	f.signup(t, "change-limit@example.com")
	changeCookie, changeCSRF, changeLogin := f.login(t, "change-limit@example.com", integrationPass)
	if changeLogin.Code != http.StatusOK {
		t.Fatal(changeLogin.Body.String())
	}
	for attempt := 0; attempt < 10; attempt++ {
		response := f.request(t, http.MethodPost, "/api/v1/auth/password/change", map[string]string{"currentPassword": "incorrect current password", "newPassword": "new safe password"}, changeCookie, changeCSRF, integrationOrigin, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password-change attempt %d status=%d, want 401", attempt+1, response.Code)
		}
	}
	limitedChange := f.request(t, http.MethodPost, "/api/v1/auth/password/change", map[string]string{"currentPassword": "incorrect current password", "newPassword": "new safe password"}, changeCookie, changeCSRF, integrationOrigin, "")
	if limitedChange.Code != http.StatusTooManyRequests {
		t.Fatalf("11th password-change attempt status=%d, want 429", limitedChange.Code)
	}
}

func TestIntegrationConcurrentVerificationCannotCreateDuplicateEmail(t *testing.T) {
	f := newAuthFixtureWithPoolSize(t, 4)
	const email = "race@example.com"
	first := f.register(t, email, "First User", integrationPass)
	second := f.registerOccurrence(t, email, "Second User", "another secure password", 1)
	if first == second {
		t.Fatal("two distinct registration requests returned the same challenge token")
	}
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, item := range []struct{ token, password string }{{first, integrationPass}, {second, "another secure password"}} {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.concurrentVerify(item.token, item.password)
		}()
	}
	wg.Wait()
	close(results)
	successes, invalid := 0, 0
	for status := range results {
		switch status {
		case http.StatusNoContent:
			successes++
		case http.StatusBadRequest:
			invalid++
		default:
			t.Errorf("verification race returned status %d", status)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("verification race successes=%d invalid=%d, want one each", successes, invalid)
	}
	var credentials, accountRows int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.credentials WHERE email=$1", email).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM accounts.accounts").Scan(&accountRows); err != nil {
		t.Fatal(err)
	}
	if credentials != 1 || accountRows != 1 {
		t.Fatalf("race created %d credentials and %d linked account rows for one email, want one each", credentials, accountRows)
	}
}
