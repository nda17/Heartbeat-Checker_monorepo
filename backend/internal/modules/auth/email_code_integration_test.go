package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
	authservice "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

const emailCodeTestKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

type emailCodeCaptureSender struct {
	to, subject, messageID string
	body                   contracts.MailContent
}

func (s *emailCodeCaptureSender) SendWithID(_ context.Context, to, subject string, body contracts.MailContent, messageID string) error {
	s.to, s.subject, s.body, s.messageID = to, subject, body, messageID
	return nil
}

func newEmailCodeFixture(t *testing.T) *authFixture {
	return newEmailCodeFixtureWithPoolAndCaptcha(t, 2, integrationCaptcha{})
}

func newEmailCodeFixtureWithPoolAndCaptcha(t *testing.T, maxConns int32, captcha CaptchaVerifier) *authFixture {
	t.Helper()
	f := newAuthFixtureWithPoolSizeAndPrefix(t, maxConns, "hc_auth_code_test_")
	module, err := New(f.pool, accounts.New(f.pool), captcha, nil, Config{
		Origin: integrationOrigin, MailEncryptionKey: emailCodeTestKey,
		ChallengeTTL: time.Hour, ResetTTL: time.Hour, RateLimitWindow: time.Hour,
		AccountDeletion: newTestAccountDeletion(f.pool),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.module, f.h = module, module.Handler()
	return f
}

func (f *authFixture) requestWithTimeout(t *testing.T, timeout time.Duration, method, path string, payload any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var body strings.Builder
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body.String())).WithContext(ctx)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", integrationOrigin)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	req.RemoteAddr = netJoin("192.0.2.10", "43125")
	recorder := httptest.NewRecorder()
	f.h.ServeHTTP(recorder, req)
	return recorder
}

func TestRegistrationEmailCodeIsBrowserBoundEncryptedAndCreatesAccountOnlyOnConfirmation(t *testing.T) {
	f := newEmailCodeFixture(t)
	const email = "registration-code@example.test"
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Registration Code", "email": email, "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start registration status=%d body=%s", start.Code, start.Body.String())
	}
	var challengeResponse struct {
		Challenge struct {
			ID                string    `json:"id"`
			Email             string    `json:"email"`
			ExpiresAt         time.Time `json:"expiresAt"`
			ResendAvailableAt time.Time `json:"resendAvailableAt"`
			ServerNow         time.Time `json:"serverNow"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &challengeResponse); err != nil || challengeResponse.Challenge.ID == "" || challengeResponse.Challenge.Email != email {
		t.Fatalf("decode registration challenge: err=%v body=%s", err, start.Body.String())
	}
	var registrationCookie *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			registrationCookie = cookie
		}
	}
	if registrationCookie == nil || !registrationCookie.HttpOnly || registrationCookie.Path != "/" || registrationCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("registration capability cookie attributes=%+v", registrationCookie)
	}
	state := f.request(t, http.MethodGet, "/api/v1/auth/registration", nil, registrationCookie, "", "", "")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), challengeResponse.Challenge.ID) || strings.Contains(state.Body.String(), integrationPass) || strings.Contains(state.Body.String(), "codeHash") {
		t.Fatalf("registration state exposed wrong data: status=%d body=%s", state.Code, state.Body.String())
	}
	foreign := f.request(t, http.MethodGet, "/api/v1/auth/registration", nil, nil, "", "", "")
	if foreign.Code != http.StatusOK || strings.TrimSpace(foreign.Body.String()) != `{"challenge":null}` {
		t.Fatalf("state without browser capability status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	wrongBrowser := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+challengeResponse.Challenge.ID+"/confirm", map[string]string{"code": "000000", "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if wrongBrowser.Code != http.StatusBadRequest || !strings.Contains(wrongBrowser.Body.String(), "email_code_invalid") {
		t.Fatalf("confirmation without the browser capability status=%d body=%s", wrongBrowser.Code, wrongBrowser.Body.String())
	}
	var accountsBefore int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.credentials WHERE email=$1`, email).Scan(&accountsBefore); err != nil || accountsBefore != 0 {
		t.Fatalf("registration created credential before confirmation: count=%d err=%v", accountsBefore, err)
	}
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT j.payload FROM auth.mail_jobs j JOIN auth.email_code_challenges c ON c.code_hash=j.token_hash WHERE c.id=$1 AND j.purpose='email_code'`, challengeResponse.Challenge.ID).Scan(&payload); err != nil {
		t.Fatalf("load queued registration email: %v", err)
	}
	if len(payload) < 32 || strings.Contains(string(payload), email) || strings.Contains(string(payload), integrationPass) {
		t.Fatalf("email job payload was not opaque ciphertext: size=%d", len(payload))
	}
	sender := &emailCodeCaptureSender{}
	worker, err := NewMailWorker(f.pool, emailCodeTestKey, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process synthetic code email: processed=%t err=%v", processed, err)
	}
	if sender.to != email || sender.messageID == "" || !strings.Contains(sender.subject, "Код регистрации") {
		t.Fatalf("captured code email metadata: to=%q subject=%q messageID=%q", sender.to, sender.subject, sender.messageID)
	}
	code := regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`).FindStringSubmatch(sender.body.Text)
	if len(code) != 2 {
		t.Fatalf("test mail did not contain a six-digit code: %q", sender.body.Text)
	}
	bad := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+challengeResponse.Challenge.ID+"/confirm", map[string]string{"code": "999999", "turnstileToken": integrationToken}, registrationCookie, "", integrationOrigin, "")
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "email_code_invalid") {
		t.Fatalf("wrong code status=%d body=%s", bad.Code, bad.Body.String())
	}
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT failed_attempts FROM auth.email_code_challenges WHERE id=$1`, challengeResponse.Challenge.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("wrong code attempts=%d err=%v, want 1", attempts, err)
	}
	confirmed := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+challengeResponse.Challenge.ID+"/confirm", map[string]string{"code": code[1], "turnstileToken": integrationToken}, registrationCookie, "", integrationOrigin, "")
	if confirmed.Code != http.StatusOK {
		t.Fatalf("correct code did not create a verified account: status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	var accountCount, sessionCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.credentials WHERE email=$1 AND email_verified_at IS NOT NULL`, email).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.sessions s JOIN auth.credentials c ON c.account_id=s.account_id WHERE c.email=$1 AND s.revoked_at IS NULL`, email).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 1 || sessionCount != 1 {
		t.Fatalf("confirmation transaction result: verified accounts=%d active sessions=%d", accountCount, sessionCount)
	}
	var cleared bool
	for _, cookie := range confirmed.Result().Cookies() {
		if cookie.Name == "hc_registration" && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("successful confirmation did not clear the browser-bound registration capability")
	}
	replay := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+challengeResponse.Challenge.ID+"/confirm", map[string]string{"code": code[1], "turnstileToken": integrationToken}, registrationCookie, "", integrationOrigin, "")
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("consumed challenge replay status=%d body=%s, want 400", replay.Code, replay.Body.String())
	}
}

func TestRegistrationCodeRejectsLegacyRegistrationWithExistingSession(t *testing.T) {
	f := newEmailCodeFixture(t)
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{"name": "Active Session", "email": "active-session-owner@example.test", "password": integrationPass, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start initial registration status=%d body=%s", start.Code, start.Body.String())
	}
	var startResult struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &startResult); err != nil {
		t.Fatal(err)
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	sender := &emailCodeCaptureSender{}
	worker, err := NewMailWorker(f.pool, emailCodeTestKey, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process initial code email: processed=%t err=%v", processed, err)
	}
	code := regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`).FindStringSubmatch(sender.body.Text)
	if len(code) != 2 || capability == nil {
		t.Fatal("initial registration did not produce its synthetic code and browser capability")
	}
	confirmed := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+startResult.Challenge.ID+"/confirm", map[string]string{"code": code[1], "turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm initial account: status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	var login loginResponse
	if err := json.Unmarshal(confirmed.Body.Bytes(), &login); err != nil || login.CSRFToken == "" {
		t.Fatalf("decode active session response: err=%v body=%s", err, confirmed.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range confirmed.Result().Cookies() {
		if cookie.Name == "hc_session" {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("confirmation did not issue an authenticated session")
	}
	response := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Second Account", "email": "second-account@example.test", "password": integrationPass, "turnstileToken": integrationToken,
	}, session, login.CSRFToken, integrationOrigin, "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "registration_requires_logout") {
		t.Fatalf("registration with an active session status=%d body=%s", response.Code, response.Body.String())
	}
	var challengeCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.email_code_challenges WHERE email='second-account@example.test'`).Scan(&challengeCount); err != nil || challengeCount != 0 {
		t.Fatalf("active-session registration unexpectedly created challenge: count=%d err=%v", challengeCount, err)
	}
}

func TestConfirmRegistrationRevokesOnlyExplicitPriorCookie(t *testing.T) {
	f := newEmailCodeFixture(t)
	const existingEmail = "prior-registration-owner@example.test"
	accountID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := security.HashPassword(integrationPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO accounts.accounts(id,name) VALUES($1,'Prior Registration Owner')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.credentials(account_id,email,password_hash) VALUES($1,$2,$3)`, accountID, existingEmail, passwordHash); err != nil {
		t.Fatal(err)
	}
	prior, _, priorLogin := f.login(t, existingEmail, integrationPass)
	other, _, otherLogin := f.login(t, existingEmail, integrationPass)
	if priorLogin.Code != http.StatusOK || otherLogin.Code != http.StatusOK || prior == nil || other == nil {
		t.Fatalf("create pre-existing sessions: first=%d second=%d", priorLogin.Code, otherLogin.Code)
	}
	const newEmail = "prior-registration-new@example.test"
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{"name": "New Registration", "email": newEmail, "password": integrationPass, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start new registration: status=%d body=%s", start.Code, start.Body.String())
	}
	var challenge struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &challenge); err != nil || challenge.Challenge.ID == "" {
		t.Fatalf("decode registration id: err=%v body=%s", err, start.Body.String())
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	if capability == nil {
		t.Fatal("registration capability missing")
	}
	sender := &emailCodeCaptureSender{}
	worker, err := NewMailWorker(f.pool, emailCodeTestKey, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process registration code email: processed=%t err=%v", processed, err)
	}
	code := regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`).FindStringSubmatch(sender.body.Text)
	if len(code) != 2 {
		t.Fatal("registration email did not contain its synthetic six-digit code")
	}
	result, err := f.module.service.ConfirmRegistration(context.Background(), challenge.Challenge.ID, capability.Value, code[1], integrationToken, authservice.Client{IP: "192.0.2.10"}, prior.Value)
	if err != nil || result.Token == "" {
		t.Fatalf("service registration confirmation with explicit prior cookie = (%+v, %v)", result, err)
	}
	if _, err := f.module.service.Authenticate(context.Background(), result.Token); err != nil {
		t.Fatalf("new registration session is not valid: %v", err)
	}
	if _, err := f.module.service.Authenticate(context.Background(), prior.Value); !errors.Is(err, authservice.ErrUnauthorized) {
		t.Fatalf("exact prior cookie remains valid after registration: %v", err)
	}
	if _, err := f.module.service.Authenticate(context.Background(), other.Value); err != nil {
		t.Fatalf("unrelated session was revoked by registration: %v", err)
	}
}

func TestRegistrationEmailCodeResendInvalidatesThePreviousCode(t *testing.T) {
	f := newEmailCodeFixture(t)
	const email = "registration-resend@example.test"
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Resend Test", "email": email, "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	var result struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	if capability == nil {
		t.Fatal("registration capability missing")
	}
	var oldHash []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT code_hash FROM auth.email_code_challenges WHERE id=$1`, result.Challenge.ID).Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.email_code_challenges SET resend_available_at=now()-interval '1 second' WHERE id=$1`, result.Challenge.ID); err != nil {
		t.Fatal(err)
	}
	resend := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+result.Challenge.ID+"/resend", map[string]string{"turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
	if resend.Code != http.StatusAccepted {
		t.Fatalf("resend status=%d body=%s", resend.Code, resend.Body.String())
	}
	var newHash []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT code_hash FROM auth.email_code_challenges WHERE id=$1`, result.Challenge.ID).Scan(&newHash); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldHash, newHash) || len(oldHash) != 32 || len(newHash) != 32 {
		t.Fatal("resend retained the previous verifier hash")
	}
	var oldJobs int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.mail_jobs WHERE purpose='email_code' AND token_hash=$1 AND status IN ('pending','sending')`, oldHash).Scan(&oldJobs); err != nil || oldJobs != 0 {
		t.Fatalf("old code mail remained sendable: jobs=%d err=%v", oldJobs, err)
	}
	wrong := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+result.Challenge.ID+"/confirm", map[string]string{"code": "000000", "turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("old/unrelated code status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT failed_attempts FROM auth.email_code_challenges WHERE id=$1`, result.Challenge.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("resend reset the failed-attempt counter: attempts=%d err=%v", attempts, err)
	}
}

func TestRegistrationEmailCodeLocksAfterFiveWrongAttempts(t *testing.T) {
	f := newEmailCodeFixture(t)
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Locked Registration", "email": "registration-locked@example.test", "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	var result struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	if capability == nil {
		t.Fatal("registration capability missing")
	}
	// Obtain the generated value so the deliberately wrong value is guaranteed
	// not to collide with the random six-digit code.
	sender := &emailCodeCaptureSender{}
	worker, err := NewMailWorker(f.pool, emailCodeTestKey, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process code email: processed=%t err=%v", processed, err)
	}
	actual := regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`).FindStringSubmatch(sender.body.Text)
	if len(actual) != 2 {
		t.Fatalf("mail body contains no six-digit code: %q", sender.body.Text)
	}
	wrongCode := "000000"
	if wrongCode == actual[1] {
		wrongCode = "000001"
	}
	for attempt := 1; attempt <= 5; attempt++ {
		response := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+result.Challenge.ID+"/confirm", map[string]string{"code": wrongCode, "turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "email_code_invalid") {
			t.Fatalf("wrong attempt %d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	var attempts int
	var mailJobs int
	if err := f.pool.QueryRow(context.Background(), `SELECT failed_attempts FROM auth.email_code_challenges WHERE id=$1`, result.Challenge.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.mail_jobs WHERE purpose='email_code' AND token_hash=(SELECT code_hash FROM auth.email_code_challenges WHERE id=$1) AND status IN ('pending','sending')`, result.Challenge.ID).Scan(&mailJobs); err != nil {
		t.Fatal(err)
	}
	if attempts != 5 || mailJobs != 0 {
		t.Fatalf("challenge after fifth wrong attempt: attempts=%d activeMailJobs=%d", attempts, mailJobs)
	}
	locked := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+result.Challenge.ID+"/confirm", map[string]string{"code": actual[1], "turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
	if locked.Code != http.StatusBadRequest || !strings.Contains(locked.Body.String(), "email_code_invalid") {
		t.Fatalf("locked challenge accepted the correct code: status=%d body=%s", locked.Code, locked.Body.String())
	}
	var credentials int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.credentials WHERE email='registration-locked@example.test'`).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("locked challenge created an account: credentials=%d err=%v", credentials, err)
	}
}

type countingCaptcha struct{ calls int }

func (c *countingCaptcha) Verify(_ context.Context, token, _ string, _ string) error {
	c.calls++
	if token != integrationToken {
		return errors.New("unexpected test captcha token")
	}
	return nil
}

func TestRegistrationCodeIPBudgetsRejectBeforeCaptchaProvider(t *testing.T) {
	captcha := &countingCaptcha{}
	f := newEmailCodeFixtureWithPoolAndCaptcha(t, 2, captcha)
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Preflight Test", "email": "registration-preflight@example.test", "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted || captcha.calls != 1 {
		t.Fatalf("initial registration status=%d captchaCalls=%d body=%s", start.Code, captcha.calls, start.Body.String())
	}
	var challenge struct {
		ID string `json:"id"`
	}
	var response struct {
		Challenge json.RawMessage `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &response); err != nil || json.Unmarshal(response.Challenge, &challenge) != nil || challenge.ID == "" {
		t.Fatalf("decode registration challenge: err=%v body=%s", err, start.Body.String())
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	if capability == nil {
		t.Fatal("registration capability missing")
	}
	clientIP := security.Digest("192.0.2.10")
	for _, scope := range []string{"register:ip", "registration_code_confirm:ip"} {
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.rate_limits(scope,key_hash,count,window_end) VALUES($1,$2,20,now()+interval '15 minutes') ON CONFLICT(scope,key_hash) DO UPDATE SET count=20,window_end=excluded.window_end`, scope, clientIP); err != nil {
			t.Fatalf("seed rate gate %s: %v", scope, err)
		}
	}
	resend := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+challenge.ID+"/resend", map[string]string{"turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
	if resend.Code != http.StatusTooManyRequests || resend.Header().Get("Retry-After") != "60" || captcha.calls != 1 {
		t.Fatalf("rate-limited resend status=%d retryAfter=%q captchaCalls=%d body=%s", resend.Code, resend.Header().Get("Retry-After"), captcha.calls, resend.Body.String())
	}
	confirm := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+challenge.ID+"/confirm", map[string]string{"code": "123456", "turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
	if confirm.Code != http.StatusTooManyRequests || confirm.Header().Get("Retry-After") != "60" || captcha.calls != 1 {
		t.Fatalf("rate-limited confirm status=%d retryAfter=%q captchaCalls=%d body=%s", confirm.Code, confirm.Header().Get("Retry-After"), captcha.calls, confirm.Body.String())
	}
}

func TestRegistrationResendAndConfirmUseOneConnectionPool(t *testing.T) {
	f := newEmailCodeFixtureWithPoolAndCaptcha(t, 1, integrationCaptcha{})
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Single Connection", "email": "registration-single-connection@example.test", "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	var result struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	if capability == nil {
		t.Fatal("registration capability missing")
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.email_code_challenges SET resend_available_at=now()-interval '1 second' WHERE id=$1`, result.Challenge.ID); err != nil {
		t.Fatal(err)
	}
	resend := f.requestWithTimeout(t, 5*time.Second, http.MethodPost, "/api/v1/auth/registration/"+result.Challenge.ID+"/resend", map[string]string{"turnstileToken": integrationToken}, capability)
	if resend.Code != http.StatusAccepted {
		t.Fatalf("single-connection resend status=%d body=%s", resend.Code, resend.Body.String())
	}
	sender := &emailCodeCaptureSender{}
	worker, err := NewMailWorker(f.pool, emailCodeTestKey, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process latest synthetic email: processed=%t err=%v", processed, err)
	}
	code := regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`).FindStringSubmatch(sender.body.Text)
	if len(code) != 2 {
		t.Fatalf("mail body contains no six-digit code: %q", sender.body.Text)
	}
	confirmed := f.requestWithTimeout(t, 5*time.Second, http.MethodPost, "/api/v1/auth/registration/"+result.Challenge.ID+"/confirm", map[string]string{"code": code[1], "turnstileToken": integrationToken}, capability)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("single-connection confirm status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
}

func TestRegistrationDatabaseFailuresAreUnavailableNotInvalidChallenges(t *testing.T) {
	f := newEmailCodeFixture(t)
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Database Failure", "email": "registration-db-failure@example.test", "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	var result struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	if capability == nil {
		t.Fatal("registration capability missing")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE auth.email_code_challenges RENAME TO email_code_challenges_fault_injection`); err != nil {
		t.Fatal(err)
	}
	state := f.request(t, http.MethodGet, "/api/v1/auth/registration", nil, capability, "", "", "")
	if state.Code != http.StatusServiceUnavailable || !strings.Contains(state.Body.String(), "temporarily_unavailable") {
		t.Fatalf("challenge lookup database failure status=%d body=%s; database failures must not be reported as bad/expired codes", state.Code, state.Body.String())
	}
}

func TestRegistrationConfirmationRollsBackAccountWhenSessionInsertFails(t *testing.T) {
	f := newEmailCodeFixture(t)
	const email = "registration-atomic-session@example.test"
	start := f.request(t, http.MethodPost, "/api/v1/auth/registration", map[string]string{
		"name": "Atomic Registration", "email": email, "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	var result struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var capability *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_registration" {
			capability = cookie
		}
	}
	if capability == nil {
		t.Fatal("registration capability missing")
	}
	sender := &emailCodeCaptureSender{}
	worker, err := NewMailWorker(f.pool, emailCodeTestKey, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process code email: processed=%t err=%v", processed, err)
	}
	code := regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`).FindStringSubmatch(sender.body.Text)
	if len(code) != 2 {
		t.Fatalf("mail body contains no six-digit code: %q", sender.body.Text)
	}
	_, err = f.pool.Exec(context.Background(), `CREATE FUNCTION auth.fail_test_session_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic session insert failure'; END $$`)
	if err != nil {
		t.Fatalf("install atomicity fault: %v", err)
	}
	if _, err = f.pool.Exec(context.Background(), `CREATE TRIGGER fail_test_session_insert BEFORE INSERT ON auth.sessions FOR EACH ROW EXECUTE FUNCTION auth.fail_test_session_insert()`); err != nil {
		t.Fatalf("install session fault trigger: %v", err)
	}
	response := f.request(t, http.MethodPost, "/api/v1/auth/registration/"+result.Challenge.ID+"/confirm", map[string]string{"code": code[1], "turnstileToken": integrationToken}, capability, "", integrationOrigin, "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "temporarily_unavailable") {
		t.Fatalf("synthetic session insert failure status=%d body=%s, want unavailable", response.Code, response.Body.String())
	}
	var credentials, sessions int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.credentials WHERE email=$1`, email).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.sessions s JOIN auth.credentials c ON c.account_id=s.account_id WHERE c.email=$1`, email).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT failed_attempts FROM auth.email_code_challenges WHERE id=$1`, result.Challenge.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if credentials != 0 || sessions != 0 || attempts != 0 {
		t.Fatalf("failed session insert left partial registration: credentials=%d sessions=%d attempts=%d", credentials, sessions, attempts)
	}
}
