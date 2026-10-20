package notifications

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	incidentcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type pushTestActor struct {
	accountID string
	sessionID string
	monitorID string
}

type pushIntegrationFixture struct {
	webhook *webhookFixture
	push    *Push
}

type pushSenderFunc func(context.Context, string, map[string]string, time.Duration) error

func (f pushSenderFunc) Send(ctx context.Context, token string, data map[string]string, ttl time.Duration) error {
	return f(ctx, token, data, ttl)
}

func newPushIntegrationFixture(t *testing.T) *pushIntegrationFixture {
	t.Helper()
	f := newWebhookFixture(t)
	push, err := NewPush(f.pool, integrationWebhookKey, PushConfig{
		Enabled:   true,
		ProjectID: "push-integration",
		Origin:    "https://heartbeat.example.test",
		SessionLease: func(ctx context.Context, tx pgx.Tx, accountID, sessionID string) (time.Time, error) {
			return auth.SessionLeaseTx(ctx, tx, accountID, sessionID)
		},
	}, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create push service: %v", err)
	}
	return &pushIntegrationFixture{webhook: f, push: push}
}

func (f *pushIntegrationFixture) addActor(t *testing.T, label string, admin bool) pushTestActor {
	t.Helper()
	accountID, monitorID := f.webhook.addMonitor(t, "Push "+label)
	if admin {
		if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE accounts.accounts SET role='admin' WHERE id=$1`, accountID); err != nil {
			t.Fatalf("make push test actor an admin: %v", err)
		}
	}
	_, err := f.webhook.pool.Exec(context.Background(), `INSERT INTO auth.credentials(account_id,email,password_hash,version) VALUES($1,$2,'test-only',1)`, accountID, "push-"+label+"-"+accountID+"@example.test")
	if err != nil {
		t.Fatalf("insert push test credentials: %v", err)
	}
	sessionID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(token[:])
	if _, err := f.webhook.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,user_agent) VALUES($1,$2,$3,1,now()+interval '1 hour','push integration test')`, sessionID, accountID, hash[:]); err != nil {
		t.Fatalf("insert push test session: %v", err)
	}
	return pushTestActor{accountID: accountID, sessionID: sessionID, monitorID: monitorID}
}

func newPushVerifier(t *testing.T) (verifier, challenge string) {
	t.Helper()
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(bytes[:])
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:])
}

func (f *pushIntegrationFixture) createPairing(t *testing.T, bearer, ip string) (PushPairingResult, string) {
	t.Helper()
	verifier, challenge := newPushVerifier(t)
	result, err := f.push.CreatePairing(context.Background(), bearer, ip, PushPairingInput{
		CodeChallenge: challenge,
		DeviceLabel:   "Pixel test device",
		AppVersion:    "1.2.3",
	})
	if err != nil {
		t.Fatalf("create push pairing: %v", err)
	}
	return result, verifier
}

func (f *pushIntegrationFixture) confirmAndExchange(t *testing.T, actor pushTestActor, pair PushPairingResult, verifier, token string) PushInstallationStatus {
	t.Helper()
	ctx := context.Background()
	if err := f.push.ConfirmPairing(ctx, actor.accountID, actor.sessionID, pair.Pairing.ID); err != nil {
		t.Fatalf("confirm push pairing: %v", err)
	}
	result, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{
		CodeVerifier: verifier,
		Provider:     "rustore",
		Token:        token,
		Permission:   "granted",
		AppVersion:   "1.2.3",
	})
	if err != nil {
		t.Fatalf("exchange push pairing: %v", err)
	}
	return result
}

func TestPushPairingPKCEOwnershipExpiryAndReplay(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "pair-owner", false)
	foreign := f.addActor(t, "pair-foreign", false)
	ctx := context.Background()

	pair, verifier := f.createPairing(t, "", "198.51.100.10")
	if _, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("unconfirmed exchange error = %v, want conflict", err)
	}
	if err := f.push.ConfirmPairing(ctx, owner.accountID, owner.sessionID, pair.Pairing.ID); err != nil {
		t.Fatalf("owner confirm: %v", err)
	}
	if _, err := f.push.Pairing(ctx, foreign.accountID, pair.Pairing.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign account pairing read error = %v, want not found after owner confirmation", err)
	}
	if err := f.push.ConfirmPairing(ctx, foreign.accountID, foreign.sessionID, pair.Pairing.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign account confirm error = %v, want not found after owner confirmation", err)
	}
	wrong := verifier[:len(verifier)-1] + "x"
	if _, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: wrong, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushInvalid) {
		t.Fatalf("wrong PKCE verifier error = %v, want invalid", err)
	}
	result, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"})
	if err != nil || result.Status != "ready" || result.BindingGeneration != 1 || result.RegistrationVersion != 2 {
		t.Fatalf("valid exchange = (%+v, %v), want ready generation=1 registration=2", result, err)
	}
	if _, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushPairingConsumed) {
		t.Fatalf("replay exchange error = %v, want consumed", err)
	}

	expired, expiredVerifier := f.createPairing(t, "", "198.51.100.11")
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE notifications.push_pairings SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.Pairing.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.push.ConfirmPairing(ctx, owner.accountID, owner.sessionID, expired.Pairing.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("expired confirm error = %v, want not found", err)
	}
	if _, err := f.push.ExchangePairing(ctx, expired.Installation.Credential, expired.Pairing.ID, PushExchangeInput{CodeVerifier: expiredVerifier, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("expired exchange error = %v, want conflict", err)
	}
}

func TestPushSessionLeaseRejectsStaleSourceWithoutExtendingSession(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "session-lease-admin", true)
	ctx := context.Background()
	var beforeExpires, beforeUsed time.Time
	if err := f.webhook.pool.QueryRow(ctx, `SELECT expires_at,last_used_at FROM auth.sessions WHERE id=$1`, actor.sessionID).Scan(&beforeExpires, &beforeUsed); err != nil {
		t.Fatal(err)
	}
	tx, err := f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := auth.SessionLeaseTx(ctx, tx, actor.accountID, actor.sessionID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("ordinary admin session without MFA should be a valid lease: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !lease.Equal(beforeExpires) {
		t.Errorf("lease expiry = %s, want original %s", lease, beforeExpires)
	}
	var afterExpires, afterUsed time.Time
	if err := f.webhook.pool.QueryRow(ctx, `SELECT expires_at,last_used_at FROM auth.sessions WHERE id=$1`, actor.sessionID).Scan(&afterExpires, &afterUsed); err != nil {
		t.Fatal(err)
	}
	if !afterExpires.Equal(beforeExpires) || !afterUsed.Equal(beforeUsed) {
		t.Fatalf("lease extended or touched session: expires %s -> %s, lastUsed %s -> %s", beforeExpires, afterExpires, beforeUsed, afterUsed)
	}

	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	tx, err = f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.SessionLeaseTx(ctx, tx, actor.accountID, actor.sessionID)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, authcontractsErrSessionRevoked()) {
		t.Fatalf("revoked lease error = %v, want ErrSessionRevoked", err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET revoked_at=NULL,expires_at=now()-interval '1 second' WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	tx, err = f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.SessionLeaseTx(ctx, tx, actor.accountID, actor.sessionID)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, authcontractsErrSessionExpired()) {
		t.Fatalf("expired lease error = %v, want ErrSessionExpired", err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET expires_at=now()+interval '1 hour',credential_version=credential_version+1 WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	tx, err = f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.SessionLeaseTx(ctx, tx, actor.accountID, actor.sessionID)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, authcontractsErrSessionRevoked()) {
		t.Fatalf("credential-version-mismatched lease error = %v, want ErrSessionRevoked", err)
	}
	otherAccount, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	tx, err = f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.SessionLeaseTx(ctx, tx, otherAccount, actor.sessionID)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, authcontractsErrSessionRevoked()) {
		t.Fatalf("foreign account session lease error = %v, want ErrSessionRevoked", err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET credential_version=1 WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE accounts.accounts SET status='blocked' WHERE id=$1`, actor.accountID); err != nil {
		t.Fatal(err)
	}
	tx, err = f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.SessionLeaseTx(ctx, tx, actor.accountID, actor.sessionID)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, authcontractsErrSessionRevoked()) {
		t.Fatalf("blocked account session lease error = %v, want ErrSessionRevoked", err)
	}
}

// These accessors keep the test's assertions tied to the exported auth error
// values while avoiding coupling the helpers above to the HTTP transport.
func authcontractsErrSessionRevoked() error { return authcontracts.ErrSessionRevoked }
func authcontractsErrSessionExpired() error { return authcontracts.ErrSessionExpired }

func TestPushRegistrationAndRouteCASAndForeignOwnership(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "route-owner", false)
	foreign := f.addActor(t, "route-foreign", false)
	pair, verifier := f.createPairing(t, "", "198.51.100.20")
	status := f.confirmAndExchange(t, owner, pair, verifier, "first-token")
	ctx := context.Background()
	if _, err := f.push.PutPushRoute(ctx, foreign.accountID, foreign.sessionID, foreign.monitorID, status.ID, PutPushRoute{Enabled: true, Version: 0}); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign route mutation error = %v, want not found", err)
	}
	if _, err := f.push.PutPushRoute(ctx, owner.accountID, owner.sessionID, owner.monitorID, status.ID, PutPushRoute{Enabled: true, Version: 0}); err != nil {
		t.Fatalf("create first push route: %v", err)
	}
	routes, err := f.push.ListPushRoutes(ctx, owner.accountID, owner.monitorID)
	if err != nil || len(routes) != 1 || !routes[0].Enabled || !routes[0].Ready || routes[0].Version != 1 {
		t.Fatalf("route list = (%+v, %v), want one ready enabled version 1", routes, err)
	}
	if _, err := f.push.PutPushRoute(ctx, owner.accountID, owner.sessionID, owner.monitorID, status.ID, PutPushRoute{Enabled: false, Version: 0}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("stale route version error = %v, want conflict", err)
	}
	newToken := "rotated-token"
	rotated, err := f.push.UpdateRegistration(ctx, pair.Installation.Credential, PushRegistrationInput{RegistrationVersion: status.RegistrationVersion, Token: &newToken, Permission: "granted"})
	if err != nil || rotated.RegistrationVersion != status.RegistrationVersion+1 {
		t.Fatalf("registration rotation = (%+v, %v), want version increment", rotated, err)
	}
	if _, err := f.push.UpdateRegistration(ctx, pair.Installation.Credential, PushRegistrationInput{RegistrationVersion: status.RegistrationVersion, Token: &newToken, Permission: "granted"}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("stale registration version error = %v, want conflict", err)
	}
	var encrypted []byte
	if err := f.webhook.pool.QueryRow(ctx, `SELECT encrypted_token FROM notifications.push_installations WHERE id=$1`, status.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	plain, err := f.push.open(status.ID, "token", encrypted)
	if err != nil || string(plain) != newToken {
		t.Fatalf("persisted rotated registration token = (%q, %v), want new token", plain, err)
	}
	var browserJSON []byte
	browserJSON, err = json.Marshal(PushInstallationStatus{ID: status.ID, DeviceLabel: "Pixel", Provider: "rustore", Status: "ready", BindingGeneration: rotated.BindingGeneration, RegistrationVersion: rotated.RegistrationVersion})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{pair.Installation.Credential, newToken, owner.accountID, owner.sessionID} {
		if bytes.Contains(browserJSON, []byte(secret)) {
			t.Errorf("browser installation JSON leaked sensitive value %q", secret)
		}
	}
}

func TestPushMonitorListReportsOnlyCurrentlyReadyEnabledRoutes(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "monitor-channels", false)
	pair, verifier := f.createPairing(t, "", "198.51.100.74")
	status := f.confirmAndExchange(t, owner, pair, verifier, "monitor-list-token")
	ctx := context.Background()
	if _, err := f.push.PutPushRoute(ctx, owner.accountID, owner.sessionID, owner.monitorID, status.ID, PutPushRoute{Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	module := monitors.New(f.webhook.pool, billing.New(f.webhook.pool, true), incidents.New(f.webhook.pool), accounts.New(f.webhook.pool), f.push)
	page, err := module.ListPage(ctx, owner.accountID, monitorcontracts.ListOptions{Page: 1, PageSize: 20, Sort: "created_desc"})
	if err != nil || len(page.Monitors) != 1 || len(page.Monitors[0].NotificationChannels) != 1 || page.Monitors[0].NotificationChannels[0] != "push" {
		t.Fatalf("monitor notification channels = (%+v, %v), want current ready push route", page.Monitors, err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE id=$1`, owner.sessionID); err != nil {
		t.Fatal(err)
	}
	page, err = module.ListPage(ctx, owner.accountID, monitorcontracts.ListOptions{Page: 1, PageSize: 20, Sort: "created_desc"})
	if err != nil || len(page.Monitors) != 1 || len(page.Monitors[0].NotificationChannels) != 0 {
		t.Fatalf("monitor notification channels after source session revoke = (%+v, %v), want no push channel", page.Monitors, err)
	}
}

func TestPushDuplicateProviderTokenCannotRebindAnotherInstallation(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "duplicate-token", false)
	first, firstVerifier := f.createPairing(t, "", "198.51.100.70")
	firstStatus := f.confirmAndExchange(t, owner, first, firstVerifier, "provider-token-unique")
	second, secondVerifier := f.createPairing(t, "", "198.51.100.71")
	if err := f.push.ConfirmPairing(context.Background(), owner.accountID, owner.sessionID, second.Pairing.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.push.ExchangePairing(context.Background(), second.Installation.Credential, second.Pairing.ID, PushExchangeInput{CodeVerifier: secondVerifier, Provider: "rustore", Token: "provider-token-unique", Permission: "granted", AppVersion: "1.2.3"})
	if !errors.Is(err, ErrPushConflict) {
		t.Fatalf("duplicate provider token exchange error = %v, want 409 conflict", err)
	}
	firstCurrent, err := f.push.Installation(context.Background(), first.Installation.Credential)
	if err != nil || firstCurrent.ID != firstStatus.ID || firstCurrent.Status != "ready" || firstCurrent.BindingGeneration != firstStatus.BindingGeneration {
		t.Fatalf("original token owner changed after duplicate exchange: (%+v, %v)", firstCurrent, err)
	}
	secondCurrent, err := f.push.Installation(context.Background(), second.Installation.Credential)
	if err != nil || secondCurrent.Status != "pending" || secondCurrent.BindingGeneration != 0 {
		t.Fatalf("duplicate token took over second installation: (%+v, %v)", secondCurrent, err)
	}
}

func TestPushDuplicateProviderTokenRegistrationRotationPreservesBothOwners(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "duplicate-rotation", false)
	first, firstVerifier := f.createPairing(t, "", "198.51.100.72")
	firstStatus := f.confirmAndExchange(t, owner, first, firstVerifier, "provider-token-current")
	second, secondVerifier := f.createPairing(t, "", "198.51.100.73")
	secondStatus := f.confirmAndExchange(t, owner, second, secondVerifier, "provider-token-other")
	ctx := context.Background()
	duplicate := "provider-token-current"
	if _, err := f.push.UpdateRegistration(ctx, second.Installation.Credential, PushRegistrationInput{RegistrationVersion: secondStatus.RegistrationVersion, Token: &duplicate, Permission: "granted"}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("duplicate token rotation error = %v, want conflict", err)
	}
	firstCurrent, err := f.push.Installation(ctx, first.Installation.Credential)
	if err != nil || firstCurrent.ID != firstStatus.ID || firstCurrent.RegistrationVersion != firstStatus.RegistrationVersion || firstCurrent.Status != "ready" {
		t.Fatalf("original token owner changed after duplicate rotation: (%+v, %v)", firstCurrent, err)
	}
	secondCurrent, err := f.push.Installation(ctx, second.Installation.Credential)
	if err != nil || secondCurrent.ID != secondStatus.ID || secondCurrent.RegistrationVersion != secondStatus.RegistrationVersion || secondCurrent.Status != "ready" {
		t.Fatalf("duplicate rotation changed second installation: (%+v, %v)", secondCurrent, err)
	}
}

func TestPushHTTPNativeAuthAndBrowserSafeResponses(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "http-owner", false)
	mux := http.NewServeMux()
	f.push.RegisterAPI(mux, func(next http.Handler) http.Handler { return next })
	call := func(method, path string, body any, bearer string, browser bool) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.RemoteAddr = "198.51.100.50:34000"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if browser {
			req = req.WithContext(authcontracts.WithPrincipal(req.Context(), authcontracts.Principal{AccountID: actor.accountID, SessionID: actor.sessionID, Role: "user"}))
		}
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		return out
	}
	verifier, challenge := newPushVerifier(t)
	unauthorized := call(http.MethodGet, "/api/v1/push/installation", nil, "bad.bearer", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("invalid native bearer status = %d, want 401", unauthorized.Code)
	}
	created := call(http.MethodPost, "/api/v1/push/pairings", PushPairingInput{CodeChallenge: challenge, DeviceLabel: "HTTP Pixel", AppVersion: "1.2.3"}, "", false)
	if created.Code != http.StatusCreated {
		t.Fatalf("create pairing status = %d body=%s", created.Code, created.Body.String())
	}
	var pairResult PushPairingResult
	if err := json.Unmarshal(created.Body.Bytes(), &pairResult); err != nil || pairResult.Pairing.ID == "" || pairResult.Installation.Credential == "" {
		t.Fatalf("decode native pairing result = (%+v, %v)", pairResult, err)
	}
	browserPair := call(http.MethodGet, "/api/v1/push/pairings/"+pairResult.Pairing.ID, nil, "", true)
	if browserPair.Code != http.StatusOK {
		t.Fatalf("browser pairing status = %d body=%s", browserPair.Code, browserPair.Body.String())
	}
	if strings.Contains(browserPair.Body.String(), pairResult.Installation.Credential) || strings.Contains(browserPair.Body.String(), "provider-private-token") {
		t.Fatalf("browser pairing response exposed native secret material: %s", browserPair.Body.String())
	}
	if err := f.push.ConfirmPairing(context.Background(), actor.accountID, actor.sessionID, pairResult.Pairing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.push.ExchangePairing(context.Background(), pairResult.Installation.Credential, pairResult.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, Provider: "rustore", Token: "provider-private-token", Permission: "granted", AppVersion: "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	installation := call(http.MethodGet, "/api/v1/push/installation", nil, pairResult.Installation.Credential, false)
	if installation.Code != http.StatusOK {
		t.Fatalf("native installation status = %d body=%s", installation.Code, installation.Body.String())
	}
	for _, required := range []string{`"leaseExpiresAt":`, `"lastPairingId":`, `"provider":"rustore"`} {
		if !strings.Contains(installation.Body.String(), required) {
			t.Errorf("native installation omitted %s: %s", required, installation.Body.String())
		}
	}
	browserList := call(http.MethodGet, "/api/v1/push/installations", nil, "", true)
	if browserList.Code != http.StatusOK {
		t.Fatalf("browser installations status = %d body=%s", browserList.Code, browserList.Body.String())
	}
	for _, secret := range []string{pairResult.Installation.Credential, "provider-private-token", actor.accountID, actor.sessionID} {
		if strings.Contains(browserList.Body.String(), secret) {
			t.Errorf("browser installations leaked private value %q", secret)
		}
	}
}

func TestPushCrossAccountRebindRemovesOldRoutesAndDeliveries(t *testing.T) {
	f := newPushIntegrationFixture(t)
	first := f.addActor(t, "rebind-first", false)
	second := f.addActor(t, "rebind-second", false)
	pair, verifier := f.createPairing(t, "", "198.51.100.30")
	status := f.confirmAndExchange(t, first, pair, verifier, "old-token")
	ctx := context.Background()
	if _, err := f.push.PutPushRoute(ctx, first.accountID, first.sessionID, first.monitorID, status.ID, PutPushRoute{Enabled: true, Version: 0}); err != nil {
		t.Fatalf("create old account route: %v", err)
	}
	route, err := f.push.ListPushRoutes(ctx, first.accountID, first.monitorID)
	if err != nil || len(route) != 1 {
		t.Fatalf("load old route: %+v, %v", route, err)
	}
	incidentID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at) VALUES($1,$2,$3,now(),now())`, incidentID, first.monitorID, first.accountID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = f.push.Enqueue(ctx, tx, incidentcontracts.Notification{EventID: eventID, AccountID: first.accountID, MonitorID: first.monitorID, IncidentID: incidentID, Kind: "incident_opened", Sequence: 1, At: time.Now().UTC(), Observation: incidentcontracts.Observation{Name: "Private monitor"}})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("enqueue initial push delivery: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var deliveryCount int
	if err := f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_deliveries WHERE installation_id=$1`, status.ID).Scan(&deliveryCount); err != nil || deliveryCount != 1 {
		t.Fatalf("old delivery count = %d, %v, want one", deliveryCount, err)
	}

	rebind, rebindVerifier := f.createPairing(t, pair.Installation.Credential, "198.51.100.31")
	if rebind.Pairing.ID == "" || rebind.Installation.Credential != "" {
		t.Fatalf("existing installation pairing returned unexpected credential: %+v", rebind.Installation)
	}
	if err := f.push.ConfirmPairing(ctx, second.accountID, second.sessionID, rebind.Pairing.ID); err != nil {
		t.Fatalf("confirm rebind under second account: %v", err)
	}
	result, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, rebind.Pairing.ID, PushExchangeInput{CodeVerifier: rebindVerifier, Provider: "rustore", Token: "new-account-token", Permission: "granted", AppVersion: "1.2.3"})
	if err != nil || result.BindingGeneration != status.BindingGeneration+2 {
		t.Fatalf("rebind exchange = (%+v, %v), want generation increment for pairing and binding", result, err)
	}
	var oldRoutes, oldDeliveries int
	if err := f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_routes WHERE installation_id=$1`, status.ID).Scan(&oldRoutes); err != nil {
		t.Fatal(err)
	}
	if err := f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_deliveries WHERE installation_id=$1`, status.ID).Scan(&oldDeliveries); err != nil {
		t.Fatal(err)
	}
	if oldRoutes != 0 || oldDeliveries != 0 {
		t.Fatalf("cross-account rebind retained old state: routes=%d deliveries=%d", oldRoutes, oldDeliveries)
	}
}

func TestPushSessionLeaseStatusesAndCredentialFence(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "installation-expiry", false)
	pair, verifier := f.createPairing(t, "", "198.51.100.40")
	status := f.confirmAndExchange(t, actor, pair, verifier, "session-token")
	ctx := context.Background()
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	state, err := f.push.Installation(ctx, pair.Installation.Credential)
	if err != nil || state.Status != "session_expired" {
		t.Fatalf("expired installation state = (%+v, %v), want session_expired", state, err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET expires_at=now()+interval '1 hour',revoked_at=now() WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	state, err = f.push.Installation(ctx, pair.Installation.Credential)
	if err != nil || state.Status != "session_revoked" {
		t.Fatalf("revoked installation state = (%+v, %v), want session_revoked", state, err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET revoked_at=NULL,credential_version=credential_version+1 WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	state, err = f.push.Installation(ctx, pair.Installation.Credential)
	if err != nil || state.Status != "session_revoked" {
		t.Fatalf("credential-stale installation state = (%+v, %v), want session_revoked", state, err)
	}
	if err := f.push.RevokeInstallation(ctx, pair.Installation.Credential); err != nil {
		t.Fatalf("native revoke after source session invalidation should revoke installation credential: %v", err)
	}
	if _, err := f.push.Installation(ctx, pair.Installation.Credential); !errors.Is(err, ErrPushUnauthorized) {
		t.Fatalf("revoked installation credential error = %v, want unauthorized", err)
	}
	_ = status
}

func (f *pushIntegrationFixture) addDeliveryEvent(t *testing.T, actor pushTestActor, installationID, kind string, sequence int64, eventName string) string {
	t.Helper()
	ctx := context.Background()
	incidentID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	if kind == "up" {
		if _, err := f.webhook.pool.Exec(ctx, `UPDATE incidents.incidents SET closed_at=now(),close_reason='recovered' WHERE monitor_id=$1 AND closed_at IS NULL`, actor.monitorID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.webhook.pool.Exec(ctx, `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at,closed_at,close_reason) VALUES($1,$2,$3,now(),now(),CASE WHEN $4='up' THEN now() ELSE NULL END,CASE WHEN $4='up' THEN 'recovered' ELSE '' END)`, incidentID, actor.monitorID, actor.accountID, kind); err != nil {
		t.Fatalf("insert %s test incident: %v", kind, err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE monitors.monitors SET sequence=$2 WHERE id=$1`, actor.monitorID, sequence); err != nil {
		t.Fatal(err)
	}
	tx, err := f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	contractKind := "incident_opened"
	if kind == "up" {
		contractKind = "incident_closed"
	}
	err = f.push.Enqueue(ctx, tx, incidentcontracts.Notification{EventID: eventID, AccountID: actor.accountID, MonitorID: actor.monitorID, IncidentID: incidentID, Kind: contractKind, Sequence: sequence, At: time.Now().UTC(), Observation: incidentcontracts.Observation{Name: eventName}})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("enqueue %s test event: %v", kind, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var deliveryID string
	if err := f.webhook.pool.QueryRow(ctx, `SELECT id::text FROM notifications.push_deliveries WHERE event_id=$1`, eventID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE notifications.push_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, deliveryID); err != nil {
		t.Fatal(err)
	}
	_ = installationID
	return deliveryID
}

func (f *pushIntegrationFixture) addDeliveryOwner(t *testing.T, label string) (pushTestActor, PushPairingResult, PushInstallationStatus) {
	t.Helper()
	actor := f.addActor(t, label, false)
	if err := billing.New(f.webhook.pool, true).GrantLocal(context.Background(), actor.accountID, 5, time.Now().Add(24*time.Hour), "push delivery integration"); err != nil {
		t.Fatalf("grant test push entitlement: %v", err)
	}
	pair, verifier := f.createPairing(t, "", "198.51.100.60")
	status := f.confirmAndExchange(t, actor, pair, verifier, "worker-token")
	if _, err := f.push.PutPushRoute(context.Background(), actor.accountID, actor.sessionID, actor.monitorID, status.ID, PutPushRoute{Enabled: true, Version: 0}); err != nil {
		t.Fatalf("enable test push route: %v", err)
	}
	return actor, pair, status
}

func TestPushDownUpDeliveriesStayOrderedWithSyntheticSender(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor, pair, installation := f.addDeliveryOwner(t, "down-up-order")
	downID := f.addDeliveryEvent(t, actor, installation.ID, "down", 1, "Example monitor")
	upID := f.addDeliveryEvent(t, actor, installation.ID, "up", 2, "Example monitor")
	sent := []string{}
	f.push.cfg.Sender = pushSenderFunc(func(ctx context.Context, token string, data map[string]string, _ time.Duration) error {
		if token != "worker-token" {
			t.Errorf("worker sent unexpected provider token %q", token)
		}
		sent = append(sent, data["deliveryId"])
		return nil
	})
	processed, err := f.push.ProcessDelivery(context.Background())
	if err != nil || !processed || len(sent) != 1 || sent[0] != downID {
		var state, reason string
		_ = f.webhook.pool.QueryRow(context.Background(), `SELECT status,error_code FROM notifications.push_deliveries WHERE id=$1`, downID).Scan(&state, &reason)
		t.Fatalf("process DOWN = (processed=%t sent=%v err=%v state=%q reason=%q), want DOWN first", processed, sent, err, state, reason)
	}
	down, err := f.push.DeliveryContent(context.Background(), pair.Installation.Credential, downID)
	if err != nil || down.Kind != "down" || down.Title != "Example monitor" {
		t.Fatalf("fetch DOWN content = (%+v, %v)", down, err)
	}
	processed, err = f.push.ProcessDelivery(context.Background())
	if err != nil || !processed || len(sent) != 2 || sent[1] != upID {
		t.Fatalf("process UP = (processed=%t sent=%v err=%v), want UP after DOWN accepted", processed, sent, err)
	}
	up, err := f.push.DeliveryContent(context.Background(), pair.Installation.Credential, upID)
	if err != nil || up.Kind != "up" || up.Title != "Example monitor" {
		t.Fatalf("fetch UP content = (%+v, %v)", up, err)
	}
}

func TestPushAcceptedProviderTimeoutCanStillFetchAttemptedContent(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor, pair, installation := f.addDeliveryOwner(t, "accepted-timeout")
	deliveryID := f.addDeliveryEvent(t, actor, installation.ID, "down", 1, "Timeout monitor")
	f.push.cfg.Sender = pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error {
		// Simulate RuStore accepting the opaque message while the caller sees a
		// transport timeout and therefore retries the same delivery.
		return context.DeadlineExceeded
	})
	processed, err := f.push.ProcessDelivery(context.Background())
	if err != nil || !processed {
		t.Fatalf("process timed-out accepted send = (%t, %v)", processed, err)
	}
	var status string
	var attempts int
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT status,attempts FROM notifications.push_deliveries WHERE id=$1`, deliveryID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 {
		t.Fatalf("timed-out send state = (%s, %d attempts), want retryable attempted state", status, attempts)
	}
	content, err := f.push.DeliveryContent(context.Background(), pair.Installation.Credential, deliveryID)
	if err != nil || content.Kind != "down" || content.Title != "Timeout monitor" {
		t.Fatalf("fetch after provider-accepted timeout = (%+v, %v), want content after a valid attempt", content, err)
	}
	if _, err := f.push.DeliveryContent(context.Background(), pair.Installation.Credential, "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("unattempted/unknown content error = %v, want not found", err)
	}
}

func TestPushStaleAttemptAndInvalidProviderTokenAreFenced(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor, pair, installation := f.addDeliveryOwner(t, "stale-attempt")
	deliveryID := f.addDeliveryEvent(t, actor, installation.ID, "down", 1, "Fenced monitor")
	f.push.cfg.Sender = pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil })
	attempt, err := f.push.ClaimPush(context.Background())
	if err != nil || attempt == nil || attempt.ID != deliveryID {
		t.Fatalf("claim push delivery = (%+v, %v)", attempt, err)
	}
	stale := *attempt
	stale.Token = "00000000-0000-4000-8000-000000000001"
	if err := f.push.CompletePush(context.Background(), stale, nil); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT status FROM notifications.push_deliveries WHERE id=$1`, deliveryID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "leased" {
		t.Fatalf("stale attempt token changed delivery state to %q, want leased", status)
	}
	if valid, err := f.push.beforePush(context.Background(), *attempt); err != nil || !valid {
		t.Fatalf("current attempt pre-send guard = (%t, %v), want valid", valid, err)
	}
	newToken := "rotated-worker-token"
	if _, err := f.push.UpdateRegistration(context.Background(), pair.Installation.Credential, PushRegistrationInput{RegistrationVersion: installation.RegistrationVersion, Token: &newToken, Permission: "granted"}); err != nil {
		t.Fatalf("rotate provider token during attempt: %v", err)
	}
	if err := f.push.CompletePush(context.Background(), *attempt, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT status FROM notifications.push_deliveries WHERE id=$1`, deliveryID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("completion after token rotation state = %q, want pending for retry with current token", status)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, deliveryID); err != nil {
		t.Fatal(err)
	}
	second, err := f.push.ClaimPush(context.Background())
	if err != nil || second == nil || second.RegistrationToken != newToken {
		t.Fatalf("claim after token rotation = (%+v, %v), want new token", second, err)
	}
	if err := f.push.CompletePush(context.Background(), *second, pushProviderError{code: "invalid_token", permanent: true, invalidToken: true}); err != nil {
		t.Fatal(err)
	}
	state, err := f.push.Installation(context.Background(), pair.Installation.Credential)
	if err != nil || state.Status != "invalid_token" {
		t.Fatalf("provider invalid-token state = (%+v, %v), want invalid_token", state, err)
	}
}

func TestPushCleanupSkipsRowsHeldByConcurrentCleaner(t *testing.T) {
	f := newPushIntegrationFixture(t)
	_, _ = f.createPairing(t, "", "198.51.100.90")
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_pairings SET expires_at=now()-interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(ctx)
	if err := f.push.Cleanup(ctx, first); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	second, err := f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback(ctx)
	done := make(chan error, 1)
	go func() { done <- f.push.Cleanup(ctx, second) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent cleanup: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = first.Rollback(ctx)
		t.Fatal("concurrent cleanup blocked on a row owned by another cleanup transaction")
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatalf("commit first cleanup: %v", err)
	}
	if err := second.Commit(ctx); err != nil {
		t.Fatalf("commit second cleanup: %v", err)
	}
}

func TestPushCleanupAndRePairRaceKeepsNewPairing(t *testing.T) {
	f := newPushIntegrationFixture(t)
	viewer := f.addActor(t, "cleanup-viewer", false)
	initial, _ := f.createPairing(t, "", "198.51.100.91")
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_pairings SET expires_at=now()-interval '11 minutes' WHERE id=$1`, initial.Pairing.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cleaner, err := f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.push.Cleanup(ctx, cleaner); err != nil {
		_ = cleaner.Rollback(ctx)
		t.Fatalf("cleanup stale pairing: %v", err)
	}
	_, challenge := newPushVerifier(t)
	started := make(chan struct{})
	repaired := make(chan struct {
		result PushPairingResult
		err    error
	}, 1)
	go func() {
		close(started)
		result, err := f.push.CreatePairing(ctx, initial.Installation.Credential, "198.51.100.92", PushPairingInput{CodeChallenge: challenge, DeviceLabel: "Pixel", AppVersion: "1.0.0"})
		repaired <- struct {
			result PushPairingResult
			err    error
		}{result, err}
	}()
	<-started
	select {
	case result := <-repaired:
		_ = cleaner.Rollback(ctx)
		t.Fatalf("re-pair completed while cleanup held its installation lock: (%+v, %v)", result.result, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := cleaner.Commit(ctx); err != nil {
		t.Fatalf("commit cleanup transaction: %v", err)
	}
	select {
	case result := <-repaired:
		if result.err != nil || result.result.Pairing.ID == "" {
			t.Fatalf("re-pair after cleanup = (%+v, %v), want successful new pairing", result.result, result.err)
		}
		stored, err := f.push.Pairing(ctx, viewer.accountID, result.result.Pairing.ID)
		if err != nil || stored.ID != result.result.Pairing.ID || stored.Status != "pending" {
			t.Fatalf("new pairing after cleanup = (%+v, %v), want persisted pending pairing", stored, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-pair did not resume after cleanup committed")
	}
}
