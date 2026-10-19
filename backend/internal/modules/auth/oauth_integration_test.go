package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

type fakeOAuthProvider struct {
	profile  OAuthIdentityProfile
	profiles map[string]OAuthIdentityProfile
}

func (p fakeOAuthProvider) ID() string { return "yandex" }
func (p fakeOAuthProvider) AuthorizationURL(state, challenge string) string {
	return "https://provider.example/authorize?state=" + url.QueryEscape(state) + "&code_challenge=" + url.QueryEscape(challenge)
}
func (p fakeOAuthProvider) Exchange(_ context.Context, code, verifier, deviceID, state string) (OAuthIdentityProfile, error) {
	if code != "fake-code" || verifier == "" || deviceID != "fake-device" || state == "" {
		if verifier == "" || deviceID != "fake-device" || state == "" {
			return OAuthIdentityProfile{}, context.Canceled
		}
		profile, ok := p.profiles[code]
		if !ok {
			return OAuthIdentityProfile{}, context.Canceled
		}
		return profile, nil
	}
	return p.profile, nil
}

func oauthFixture(t *testing.T, profile OAuthIdentityProfile) *authFixture {
	return oauthProfilesFixture(t, map[string]OAuthIdentityProfile{"fake-code": profile})
}

func oauthProfilesFixture(t *testing.T, profiles map[string]OAuthIdentityProfile) *authFixture {
	return oauthProfilesFixtureWithPoolSize(t, profiles, 1)
}

func oauthProfilesFixtureWithPoolSize(t *testing.T, profiles map[string]OAuthIdentityProfile, maxConns int32) *authFixture {
	t.Helper()
	f := newAuthFixtureWithPoolSize(t, maxConns)
	audit := postgres.OAuthAudit(func(context.Context, pgx.Tx, string, string, string, string, string) error { return nil })
	provider := fakeOAuthProvider{profiles: profiles}
	if profile, ok := profiles["fake-code"]; ok {
		provider.profile = profile
	}
	module, err := New(f.pool, accounts.New(f.pool), integrationCaptcha{}, f.mail, Config{
		Origin: integrationOrigin, ChallengeTTL: time.Hour, ResetTTL: time.Hour, RateLimitWindow: time.Hour,
		OAuthProviders: map[string]OAuthProvider{"yandex": provider}, OAuthAudit: audit,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.module, f.h = module, module.Handler()
	return f
}

func oauthStart(t *testing.T, f *authFixture, mode, returnTo, password string, session *http.Cookie) (string, *http.Cookie) {
	t.Helper()
	start := f.request(t, http.MethodPost, "/api/v1/auth/oauth/yandex/start", map[string]string{"mode": mode, "returnTo": returnTo, "password": password}, session, csrfFor(session), integrationOrigin, "")
	if start.Code != http.StatusOK {
		t.Fatalf("OAuth %s start status=%d body=%s", mode, start.Code, start.Body.String())
	}
	var body struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(body.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	var attempt *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == "hc_oauth_attempt" {
			attempt = c
		}
	}
	if attempt == nil || u.Query().Get("state") == "" {
		t.Fatalf("OAuth attempt missing cookie/state: %v %q", attempt, u.Query().Get("state"))
	}
	return u.Query().Get("state"), attempt
}

func (f *authFixture) oauthCallback(t *testing.T, state string, attempt, session *http.Cookie) *httptest.ResponseRecorder {
	return f.oauthCallbackCode(t, state, attempt, session, "fake-code")
}

func (f *authFixture) oauthCallbackCode(t *testing.T, state string, attempt, session *http.Cookie, code string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/yandex/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state)+"&device_id=fake-device", nil)
	req.RemoteAddr = netJoin("192.0.2.10", "43125")
	if attempt != nil {
		req.AddCookie(attempt)
	}
	if session != nil {
		req.AddCookie(session)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	return w
}

func csrfFor(cookie *http.Cookie) string {
	if cookie == nil {
		return ""
	}
	return security.CSRF(cookie.Value)
}

func TestOAuthRegistrationRequiresStateBrowserProofAndEmailConfirmation(t *testing.T) {
	const email = "oauth-new@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-new-1", Email: email, Name: "OAuth User"})
	start := f.request(t, http.MethodPost, "/api/v1/auth/oauth/yandex/start", map[string]string{"mode": "login", "returnTo": "/app/settings"}, nil, "", integrationOrigin, "")
	if start.Code != http.StatusOK {
		t.Fatalf("OAuth start status=%d body=%s", start.Code, start.Body.String())
	}
	var startBody struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &startBody); err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(startBody.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorize.Query().Get("state")
	var attempt *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "hc_oauth_attempt" {
			attempt = cookie
		}
	}
	if attempt == nil || state == "" {
		t.Fatalf("OAuth attempt cookie/state missing: cookie=%v state=%q", attempt, state)
	}
	callback := f.request(t, http.MethodGet, "/api/v1/auth/oauth/yandex/callback?code=fake-code&state="+url.QueryEscape(state)+"&device_id=fake-device", nil, attempt, "", "", "")
	if callback.Code != http.StatusSeeOther {
		t.Fatalf("OAuth callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	var registration *http.Cookie
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == "hc_oauth_registration" {
			registration = cookie
		}
	}
	if registration == nil || registration.Value == "" {
		t.Fatalf("registration browser proof missing: %v", callback.Result().Cookies())
	}
	// The attempt is one-use even when the provider callback is replayed.
	replay := f.request(t, http.MethodGet, "/api/v1/auth/oauth/yandex/callback?code=fake-code&state="+url.QueryEscape(state)+"&device_id=fake-device", nil, attempt, "", "", "")
	if replay.Code != http.StatusSeeOther || !strings.Contains(replay.Header().Get("Location"), "oauth_state_invalid") {
		t.Fatalf("replayed OAuth callback status=%d location=%q", replay.Code, replay.Header().Get("Location"))
	}
	token := f.mail.token(t, email, "verify-email?oauth=1", 0)
	wrongBrowser := &http.Cookie{Name: registration.Name, Value: strings.Repeat("x", 43)}
	wrongBrowserConfirm := f.request(t, http.MethodPost, "/api/v1/auth/oauth/registration/confirm", map[string]string{"token": token}, wrongBrowser, "", integrationOrigin, "")
	if wrongBrowserConfirm.Code == http.StatusOK {
		t.Fatal("OAuth token was accepted without the browser proof cookie")
	}
	var consumedBeforeProof int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.registration_challenges WHERE oauth_subject='subject-new-1' AND consumed_at IS NOT NULL").Scan(&consumedBeforeProof); err != nil || consumedBeforeProof != 0 {
		t.Fatalf("wrong browser proof consumed registration: count=%d err=%v", consumedBeforeProof, err)
	}
	confirm := f.request(t, http.MethodPost, "/api/v1/auth/oauth/registration/confirm", map[string]string{"token": token}, registration, "", integrationOrigin, "")
	if confirm.Code != http.StatusOK {
		t.Fatalf("OAuth confirmation status=%d body=%s", confirm.Code, confirm.Body.String())
	}
	var accounts int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_identities WHERE provider='yandex' AND subject='subject-new-1'").Scan(&accounts); err != nil || accounts != 1 {
		t.Fatalf("OAuth identity count=%d err=%v", accounts, err)
	}
}

func TestOAuthCallbackRejectsDuplicateFieldsWithoutConsumingAttemptOrClearingCookie(t *testing.T) {
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-query-guard", Email: "query-guard@example.test", Name: "Query Guard"})
	state, attempt := oauthStart(t, f, "login", "/app", "", nil)
	for _, rawQuery := range []string{
		"code=fake-code&state=" + url.QueryEscape(state) + "&state=" + url.QueryEscape(state) + "&device_id=fake-device",
		"code=fake-code&code=fake-code&state=" + url.QueryEscape(state) + "&device_id=fake-device",
		"code=fake-code&state=" + url.QueryEscape(state) + "&device_id=fake-device&unknown=a&unknown=b",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/yandex/callback?"+rawQuery, nil)
		req.AddCookie(attempt)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "oauth_state_invalid") {
			t.Fatalf("malformed callback %q status=%d location=%q", rawQuery, w.Code, w.Header().Get("Location"))
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == attempt.Name && c.MaxAge < 0 {
				t.Fatalf("malformed callback cleared active attempt cookie: %#v", c)
			}
		}
	}
	var consumed int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_attempts WHERE state_hash=$1 AND consumed_at IS NOT NULL", security.Digest(state)).Scan(&consumed); err != nil || consumed != 0 {
		t.Fatalf("malformed callback consumed valid attempt: count=%d err=%v", consumed, err)
	}
	valid := f.oauthCallback(t, state, attempt, nil)
	if valid.Code != http.StatusSeeOther || strings.Contains(valid.Header().Get("Location"), "oauth_state_invalid") {
		t.Fatalf("valid callback after malformed requests failed: status=%d location=%q", valid.Code, valid.Header().Get("Location"))
	}
}

func TestOAuthForeignStatePreservesActiveAttemptAndValidCancellationConsumesIt(t *testing.T) {
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-cancel", Email: "cancel@example.test", Name: "Cancel User"})
	state, attempt := oauthStart(t, f, "login", "/app", "", nil)
	foreignState := strings.Repeat("x", 43)
	foreign := f.oauthCallback(t, foreignState, attempt, nil)
	if foreign.Code != http.StatusSeeOther || !strings.Contains(foreign.Header().Get("Location"), "oauth_state_invalid") {
		t.Fatalf("foreign-state callback status=%d location=%q", foreign.Code, foreign.Header().Get("Location"))
	}
	for _, c := range foreign.Result().Cookies() {
		if c.Name == attempt.Name && c.MaxAge < 0 {
			t.Fatalf("foreign state cleared active attempt cookie: %#v", c)
		}
	}
	var consumed int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_attempts WHERE state_hash=$1 AND consumed_at IS NOT NULL", security.Digest(state)).Scan(&consumed); err != nil || consumed != 0 {
		t.Fatalf("foreign state consumed active attempt: count=%d err=%v", consumed, err)
	}
	cancel := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/yandex/callback?error=access_denied&state="+url.QueryEscape(state), nil)
	cancel.AddCookie(attempt)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, cancel)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "oauth_cancelled") {
		t.Fatalf("valid provider cancellation status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_attempts WHERE state_hash=$1 AND consumed_at IS NOT NULL", security.Digest(state)).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("valid cancellation did not consume attempt exactly once: count=%d err=%v", consumed, err)
	}
}

func completeOAuthSignup(t *testing.T, f *authFixture, state string, attempt *http.Cookie, email string) *http.Cookie {
	t.Helper()
	callback := f.oauthCallback(t, state, attempt, nil)
	var registration *http.Cookie
	for _, c := range callback.Result().Cookies() {
		if c.Name == "hc_oauth_registration" {
			registration = c
		}
	}
	if registration == nil {
		t.Fatalf("OAuth registration proof missing: %v", callback.Result().Cookies())
	}
	token := f.mail.token(t, email, "verify-email?oauth=1", 0)
	confirm := f.request(t, http.MethodPost, "/api/v1/auth/oauth/registration/confirm", map[string]string{"token": token}, registration, "", integrationOrigin, "")
	if confirm.Code != http.StatusOK {
		t.Fatalf("OAuth registration confirmation status=%d body=%s", confirm.Code, confirm.Body.String())
	}
	for _, c := range confirm.Result().Cookies() {
		if c.Name == "hc_session" {
			return c
		}
	}
	t.Fatalf("OAuth registration confirmation has no session cookie: %v", confirm.Result().Cookies())
	return nil
}

func TestOAuthExistingEmailConflictsWithoutIdentityLink(t *testing.T) {
	const email = "oauth-conflict@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-email-conflict", Email: email, Name: "Existing User"})
	f.signup(t, email)
	state, attempt := oauthStart(t, f, "login", "/app", "", nil)
	callback := f.oauthCallback(t, state, attempt, nil)
	if !strings.Contains(callback.Header().Get("Location"), "oauth_email_conflict") {
		t.Fatalf("existing-email OAuth callback location=%q", callback.Header().Get("Location"))
	}
	var identityCount, accountCount int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_identities WHERE provider='yandex' AND subject='subject-email-conflict'").Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.credentials WHERE email=$1", email).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if identityCount != 0 || accountCount != 1 {
		t.Fatalf("existing-email conflict changed identity/account rows: identities=%d accounts=%d", identityCount, accountCount)
	}
}

func TestOAuthOnlyAccountCannotLinkOrUnlinkWithoutPasswordSetup(t *testing.T) {
	const email = "oauth-only@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-oauth-only", Email: email, Name: "OAuth Only"})
	state, attempt := oauthStart(t, f, "login", "/app", "", nil)
	session := completeOAuthSignup(t, f, state, attempt, email)
	link := f.request(t, http.MethodPost, "/api/v1/auth/oauth/yandex/start", map[string]string{"mode": "link", "returnTo": "/app/settings", "password": ""}, session, csrfFor(session), integrationOrigin, "")
	if link.Code == http.StatusOK {
		t.Fatal("OAuth-only account was allowed to start linking without a password")
	}
	var identityCount, attemptCount int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_identities WHERE account_id=(SELECT account_id FROM auth.credentials WHERE email=$1)", email).Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_attempts WHERE mode='link'").Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if identityCount != 1 || attemptCount != 0 {
		t.Fatalf("password setup guard changed links/attempts: identities=%d link_attempts=%d", identityCount, attemptCount)
	}
}

func TestAdminOAuthLinkRequiresMFAEnrollmentAndOAuthLoginDoesNotGrantMFA(t *testing.T) {
	const email = "oauth-admin-mfa@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-admin-mfa", Email: email, Name: "Admin MFA"})
	state, attempt := oauthStart(t, f, "login", "/app", "", nil)
	session := completeOAuthSignup(t, f, state, attempt, email)
	accountID := f.mailAccountID(t, email)
	if _, err := f.pool.Exec(context.Background(), `UPDATE accounts.accounts SET role='admin' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	logout := f.request(t, http.MethodPost, "/api/v1/auth/logout", nil, session, csrfFor(session), integrationOrigin, "")
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	state, attempt = oauthStart(t, f, "login", "/app", "", nil)
	callback := f.oauthCallback(t, state, attempt, nil)
	var adminSession *http.Cookie
	for _, c := range callback.Result().Cookies() {
		if c.Name == "hc_session" {
			adminSession = c
		}
	}
	if adminSession == nil {
		t.Fatalf("OAuth login did not set a session cookie: %v", callback.Result().Cookies())
	}
	var mfaVersion *int64
	var verifiedUntil, stepUpUntil *time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT mfa_version,mfa_verified_until,mfa_step_up_until FROM auth.sessions WHERE token_hash=$1`, security.Digest(adminSession.Value)).Scan(&mfaVersion, &verifiedUntil, &stepUpUntil); err != nil {
		t.Fatal(err)
	}
	if mfaVersion != nil || verifiedUntil != nil || stepUpUntil != nil {
		t.Fatalf("OAuth login unexpectedly granted MFA: version=%v verified=%v stepup=%v", mfaVersion, verifiedUntil, stepUpUntil)
	}
	link := f.request(t, http.MethodPost, "/api/v1/auth/oauth/yandex/start", map[string]string{"mode": "link", "returnTo": "/app/settings", "password": integrationPass}, adminSession, csrfFor(adminSession), integrationOrigin, "")
	if link.Code == http.StatusOK {
		t.Fatal("admin without MFA enrollment was allowed to link an OAuth identity")
	}
	var linkAttempts int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_attempts WHERE mode='link'").Scan(&linkAttempts); err != nil || linkAttempts != 0 {
		t.Fatalf("admin MFA gate left link attempts=%d err=%v", linkAttempts, err)
	}
}

func TestConcurrentOAuthConfirmationsForSameProviderSubjectCreateOneAccount(t *testing.T) {
	const subject = "subject-concurrent-confirm"
	f := oauthProfilesFixtureWithPoolSize(t, map[string]OAuthIdentityProfile{
		"first-code":  {Subject: subject, Email: "oauth-race-one@example.test", Name: "Race One"},
		"second-code": {Subject: subject, Email: "oauth-race-two@example.test", Name: "Race Two"},
	}, 4)
	type registration struct {
		email   string
		browser *http.Cookie
		token   string
	}
	registrations := make([]registration, 0, 2)
	for i, code := range []string{"first-code", "second-code"} {
		state, attempt := oauthStart(t, f, "login", "/app", "", nil)
		callback := f.oauthCallbackCode(t, state, attempt, nil, code)
		if callback.Code != http.StatusSeeOther {
			t.Fatalf("callback %d status=%d location=%q", i, callback.Code, callback.Header().Get("Location"))
		}
		var browser *http.Cookie
		for _, c := range callback.Result().Cookies() {
			if c.Name == "hc_oauth_registration" {
				browser = c
			}
		}
		if browser == nil {
			t.Fatalf("registration %d has no browser proof", i)
		}
		email := fmt.Sprintf("oauth-race-%s@example.test", []string{"one", "two"}[i])
		registrations = append(registrations, registration{email: email, browser: browser, token: f.mail.token(t, email, "verify-email?oauth=1", 0)})
	}
	start := make(chan struct{})
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i, reg := range registrations {
		wg.Add(1)
		go func(i int, reg registration) {
			defer wg.Done()
			<-start
			out := f.request(t, http.MethodPost, "/api/v1/auth/oauth/registration/confirm", map[string]string{"token": reg.token}, reg.browser, "", integrationOrigin, "")
			statuses[i] = out.Code
		}(i, reg)
	}
	close(start)
	wg.Wait()
	successes, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("concurrent OAuth confirm statuses=%v", statuses)
		}
	}
	var identityCount, accountCount int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_identities WHERE provider='yandex' AND subject=$1", subject).Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.credentials WHERE email IN ($1,$2)", registrations[0].email, registrations[1].email).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || conflicts != 1 || identityCount != 1 || accountCount != 1 {
		t.Fatalf("duplicate subject race results statuses=%v successes=%d conflicts=%d identities=%d accounts=%d", statuses, successes, conflicts, identityCount, accountCount)
	}
}

func TestOAuthLinkRotatesCurrentSessionRevokesOthersAndRejectsStaleIdentityUnlink(t *testing.T) {
	const email = "oauth-link@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-link-one", Email: "unused@example.test", Name: "Linked User"})
	f.signup(t, email)
	current, _, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d", login.Code)
	}
	other, _, otherLogin := f.login(t, email, integrationPass)
	if otherLogin.Code != http.StatusOK {
		t.Fatalf("second login status=%d", otherLogin.Code)
	}
	state, attempt := oauthStart(t, f, "link", "/app/settings", integrationPass, current)
	callback := f.oauthCallback(t, state, attempt, current)
	if callback.Code != http.StatusSeeOther {
		t.Fatalf("OAuth link callback status=%d location=%q", callback.Code, callback.Header().Get("Location"))
	}
	var rotated *http.Cookie
	for _, c := range callback.Result().Cookies() {
		if c.Name == "hc_session" && c.Value != current.Value {
			rotated = c
		}
	}
	if rotated == nil {
		t.Fatalf("link callback did not rotate current session: %v", callback.Result().Cookies())
	}
	if got := f.request(t, http.MethodGet, "/api/v1/me", nil, current, "", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("old current cookie status=%d, want 401", got)
	}
	if got := f.request(t, http.MethodGet, "/api/v1/me", nil, other, "", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("other session status=%d, want 401 after credential-version bump", got)
	}
	var identityID string
	if err := f.pool.QueryRow(context.Background(), "SELECT id FROM auth.oauth_identities WHERE provider='yandex' AND subject='subject-link-one'").Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := f.pool.QueryRow(context.Background(), "SELECT version FROM auth.credentials WHERE email=$1", email).Scan(&version); err != nil || version != 2 {
		t.Fatalf("credential version=%d err=%v, want 2 after link rotation", version, err)
	}
	stale := f.request(t, http.MethodDelete, "/api/v1/me/identities/yandex", map[string]string{"identityId": "00000000-0000-4000-8000-000000000001", "password": integrationPass}, rotated, csrfFor(rotated), integrationOrigin, "")
	if stale.Code == http.StatusNoContent {
		t.Fatal("unlink with a stale identity ID succeeded")
	}
	var remaining int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_identities WHERE id=$1", identityID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("stale unlink changed identity row: count=%d err=%v", remaining, err)
	}
	unlinked := f.request(t, http.MethodDelete, "/api/v1/me/identities/yandex", map[string]string{"identityId": identityID, "password": integrationPass}, rotated, csrfFor(rotated), integrationOrigin, "")
	if unlinked.Code != http.StatusNoContent {
		t.Fatalf("valid unlink status=%d body=%s", unlinked.Code, unlinked.Body.String())
	}
	var left int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth.oauth_identities WHERE id=$1", identityID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("valid unlink left identity: count=%d err=%v", left, err)
	}
	if got := f.request(t, http.MethodGet, "/api/v1/me", nil, rotated, "", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("pre-unlink current cookie remains valid after rotation: status=%d", got)
	}
}

func TestOAuthLinkCallbackIsFencedAfterLogoutResetOrAccountBlock(t *testing.T) {
	for _, action := range []string{"logout", "password reset", "account block"} {
		t.Run(action, func(t *testing.T) {
			const email = "oauth-fence@example.test"
			f := oauthFixture(t, OAuthIdentityProfile{Subject: "subject-fence-" + strings.ReplaceAll(action, " ", "-"), Email: "ignored@example.test", Name: "Fence User"})
			f.signup(t, email)
			cookie, csrf, login := f.login(t, email, integrationPass)
			if login.Code != http.StatusOK {
				t.Fatalf("login status=%d", login.Code)
			}
			state, attempt := oauthStart(t, f, "link", "/app/settings", integrationPass, cookie)
			accountID := f.mailAccountID(t, email)
			switch action {
			case "logout":
				out := f.request(t, http.MethodPost, "/api/v1/auth/logout", nil, cookie, csrf, integrationOrigin, "")
				if out.Code != http.StatusNoContent {
					t.Fatalf("logout status=%d body=%s", out.Code, out.Body.String())
				}
			case "password reset":
				out := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{"email": email, "turnstileToken": integrationToken}, nil, "", integrationOrigin, "")
				if out.Code != http.StatusAccepted {
					t.Fatalf("reset request status=%d body=%s", out.Code, out.Body.String())
				}
				token := f.mail.token(t, email, "/reset-password", 0)
				out = f.request(t, http.MethodPost, "/api/v1/auth/password/reset/confirm", map[string]string{"token": token, "password": "new secure password"}, nil, "", integrationOrigin, "")
				if out.Code != http.StatusNoContent {
					t.Fatalf("reset confirm status=%d body=%s", out.Code, out.Body.String())
				}
			case "account block":
				if _, err := f.pool.Exec(context.Background(), `UPDATE accounts.accounts SET status='blocked' WHERE id=$1`, accountID); err != nil {
					t.Fatal(err)
				}
			}
			callback := f.oauthCallback(t, state, attempt, cookie)
			if !strings.Contains(callback.Header().Get("Location"), "oauth_state_invalid") && action != "account block" {
				t.Fatalf("stale link callback location=%q", callback.Header().Get("Location"))
			}
			var identities, activeSessions int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.oauth_identities WHERE account_id=$1`, accountID).Scan(&identities); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.sessions WHERE account_id=$1 AND revoked_at IS NULL`, accountID).Scan(&activeSessions); err != nil {
				t.Fatal(err)
			}
			if identities != 0 || action != "account block" && activeSessions != 0 {
				t.Fatalf("fenced callback changed protected rows: identities=%d active_sessions=%d", identities, activeSessions)
			}
		})
	}
}

func (f *authFixture) mailAccountID(t *testing.T, email string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id FROM auth.credentials WHERE email=$1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
