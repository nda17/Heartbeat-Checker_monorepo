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
		ClientState:   verifier,
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
	confirmation, err := f.push.ConfirmPairing(ctx, actor.accountID, actor.sessionID, pair.Pairing.ID)
	if err != nil {
		t.Fatalf("confirm push pairing: %v", err)
	}
	if confirmation.ClientState != verifier || confirmation.PairingID != pair.Pairing.ID || confirmation.CompletionCode == "" {
		t.Fatalf("confirmation payload = %+v, want matching state, pairing and completion code", confirmation)
	}
	result, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{
		CodeVerifier:   verifier,
		CompletionCode: confirmation.CompletionCode,
		Provider:       "rustore",
		Token:          token,
		Permission:     "granted",
		AppVersion:     "1.2.3",
	})
	if err != nil {
		t.Fatalf("exchange push pairing: %v", err)
	}
	return result
}

func (f *pushIntegrationFixture) confirmPairing(t *testing.T, actor pushTestActor, pair PushPairingResult) PushPairingConfirmation {
	t.Helper()
	confirmation, err := f.push.ConfirmPairing(context.Background(), actor.accountID, actor.sessionID, pair.Pairing.ID)
	if err != nil {
		t.Fatalf("confirm push pairing: %v", err)
	}
	if confirmation.PairingID != pair.Pairing.ID || confirmation.ClientState == "" || len(confirmation.CompletionCode) != 43 {
		t.Fatalf("invalid confirmation result: %+v", confirmation)
	}
	return confirmation
}

func TestPushPairingPKCEOwnershipExpiryAndReplay(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "pair-owner", false)
	foreign := f.addActor(t, "pair-foreign", false)
	ctx := context.Background()

	pair, verifier := f.createPairing(t, "", "198.51.100.10")
	unknownCode, _ := newPushVerifier(t)
	if _, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, CompletionCode: unknownCode, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("unconfirmed exchange error = %v, want conflict", err)
	}
	confirmation := f.confirmPairing(t, owner, pair)
	if _, err := f.push.ConfirmPairing(ctx, owner.accountID, owner.sessionID, pair.Pairing.ID); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("repeated confirmation error = %v, want conflict", err)
	}
	if _, err := f.push.Pairing(ctx, foreign.accountID, pair.Pairing.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign account pairing read error = %v, want not found after owner confirmation", err)
	}
	if _, err := f.push.ConfirmPairing(ctx, foreign.accountID, foreign.sessionID, pair.Pairing.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign account confirm error = %v, want not found after owner confirmation", err)
	}
	wrong := verifier[:len(verifier)-1] + "x"
	if _, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: wrong, CompletionCode: confirmation.CompletionCode, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushInvalid) {
		t.Fatalf("wrong PKCE verifier error = %v, want invalid", err)
	}
	var defaultEnabled bool
	if err := f.webhook.pool.QueryRow(ctx, `SELECT default_enabled FROM notifications.push_account_settings WHERE account_id=$1`, owner.accountID).Scan(&defaultEnabled); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("failed proof wrote account default: enabled=%t err=%v", defaultEnabled, err)
	}
	wrongCode, _ := newPushVerifier(t)
	for wrongCode == confirmation.CompletionCode {
		wrongCode, _ = newPushVerifier(t)
	}
	if _, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, CompletionCode: wrongCode, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushInvalid) {
		t.Fatalf("wrong completion code error = %v, want invalid", err)
	}
	if err := f.webhook.pool.QueryRow(ctx, `SELECT default_enabled FROM notifications.push_account_settings WHERE account_id=$1`, owner.accountID).Scan(&defaultEnabled); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong completion code wrote account default: enabled=%t err=%v", defaultEnabled, err)
	}
	result, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, CompletionCode: confirmation.CompletionCode, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"})
	if err != nil || result.Status != "ready" || result.BindingGeneration != 1 || result.RegistrationVersion != 2 {
		t.Fatalf("valid exchange = (%+v, %v), want ready generation=1 registration=2", result, err)
	}
	if _, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, pair.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, CompletionCode: confirmation.CompletionCode, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushPairingConsumed) {
		t.Fatalf("replay exchange error = %v, want consumed", err)
	}

	expired, expiredVerifier := f.createPairing(t, "", "198.51.100.11")
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE notifications.push_pairings SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.Pairing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.push.ConfirmPairing(ctx, owner.accountID, owner.sessionID, expired.Pairing.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("expired confirm error = %v, want not found", err)
	}
	expiredCode, _ := newPushVerifier(t)
	if _, err := f.push.ExchangePairing(ctx, expired.Installation.Credential, expired.Pairing.ID, PushExchangeInput{CodeVerifier: expiredVerifier, CompletionCode: expiredCode, Provider: "rustore", Token: "push-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushConflict) {
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

func TestPushRegistrationAndMonitorSettingsCASAndForeignOwnership(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "route-owner", false)
	foreign := f.addActor(t, "route-foreign", false)
	pair, verifier := f.createPairing(t, "", "198.51.100.20")
	status := f.confirmAndExchange(t, owner, pair, verifier, "first-token")
	ctx := context.Background()
	if _, err := f.push.PutMonitorPush(ctx, foreign.accountID, foreign.sessionID, owner.monitorID, PutPushMonitorSettings{Enabled: false, Version: 0}); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign monitor settings mutation error = %v, want not found", err)
	}
	initial, err := f.push.MonitorPush(ctx, owner.accountID, owner.monitorID)
	if err != nil || !initial.Enabled || initial.Version != 0 {
		t.Fatalf("inherited account setting = (%+v, %v), want enabled version 0", initial, err)
	}
	created, err := f.push.PutMonitorPush(ctx, owner.accountID, owner.sessionID, owner.monitorID, PutPushMonitorSettings{Enabled: true, Version: 0})
	if err != nil || !created.Enabled || created.Version != 1 {
		t.Fatalf("create explicit monitor override = (%+v, %v), want enabled version 1", created, err)
	}
	if _, err := f.push.PutMonitorPush(ctx, owner.accountID, owner.sessionID, owner.monitorID, PutPushMonitorSettings{Enabled: false, Version: 0}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("stale monitor settings version error = %v, want conflict", err)
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

func TestPushMonitorListReportsOnlyCurrentlyReadyEnabledSettings(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "monitor-channels", false)
	pair, verifier := f.createPairing(t, "", "198.51.100.74")
	f.confirmAndExchange(t, owner, pair, verifier, "monitor-list-token")
	ctx := context.Background()
	module := monitors.New(f.webhook.pool, billing.New(f.webhook.pool, true), incidents.New(f.webhook.pool), accounts.New(f.webhook.pool), f.push)
	page, err := module.ListPage(ctx, owner.accountID, monitorcontracts.ListOptions{Page: 1, PageSize: 20, Sort: "created_desc"})
	if err != nil || len(page.Monitors) != 1 || len(page.Monitors[0].NotificationChannels) != 1 || page.Monitors[0].NotificationChannels[0] != "push" {
		t.Fatalf("monitor notification channels = (%+v, %v), want inherited enabled push channel", page.Monitors, err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE id=$1`, owner.sessionID); err != nil {
		t.Fatal(err)
	}
	page, err = module.ListPage(ctx, owner.accountID, monitorcontracts.ListOptions{Page: 1, PageSize: 20, Sort: "created_desc"})
	if err != nil || len(page.Monitors) != 1 || len(page.Monitors[0].NotificationChannels) != 0 {
		t.Fatalf("monitor notification channels after source session revoke = (%+v, %v), want no push channel", page.Monitors, err)
	}
}

func TestPushAccountDefaultAndExplicitMonitorOffCoverFutureMonitorsAndDevices(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "account-default-fanout", false)
	ctx := context.Background()
	initial, err := f.push.MonitorPush(ctx, actor.accountID, actor.monitorID)
	if err != nil || initial.Enabled || initial.Version != 0 {
		t.Fatalf("initial missing account setting = (%+v, %v), want disabled/version 0", initial, err)
	}

	first, firstVerifier := f.createPairing(t, "", "198.51.100.75")
	firstStatus := f.confirmAndExchange(t, actor, first, firstVerifier, "default-first-device")
	var accountDefault bool
	if err := f.webhook.pool.QueryRow(ctx, `SELECT default_enabled FROM notifications.push_account_settings WHERE account_id=$1`, actor.accountID).Scan(&accountDefault); err != nil || !accountDefault {
		t.Fatalf("successful authorized exchange default = (%t, %v), want true", accountDefault, err)
	}
	inherited, err := f.push.MonitorPush(ctx, actor.accountID, actor.monitorID)
	if err != nil || !inherited.Enabled || inherited.Version != 0 {
		t.Fatalf("existing monitor inherited setting = (%+v, %v), want enabled/version 0", inherited, err)
	}

	explicitOff, err := f.push.PutMonitorPush(ctx, actor.accountID, actor.sessionID, actor.monitorID, PutPushMonitorSettings{Enabled: false, Version: inherited.Version})
	if err != nil || explicitOff.Enabled || explicitOff.Version != 1 {
		t.Fatalf("first explicit off = (%+v, %v), want false/version 1", explicitOff, err)
	}
	var generation int64
	if err := f.webhook.pool.QueryRow(ctx, `SELECT delivery_generation FROM notifications.push_monitor_settings WHERE monitor_id=$1`, actor.monitorID).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("first override generation = (%d, %v), want 1", generation, err)
	}
	unchangedOff, err := f.push.PutMonitorPush(ctx, actor.accountID, actor.sessionID, actor.monitorID, PutPushMonitorSettings{Enabled: false, Version: explicitOff.Version})
	if err != nil || unchangedOff.Enabled || unchangedOff.Version != 2 {
		t.Fatalf("same-value explicit off update = (%+v, %v), want false/version 2", unchangedOff, err)
	}
	if err := f.webhook.pool.QueryRow(ctx, `SELECT delivery_generation FROM notifications.push_monitor_settings WHERE monitor_id=$1`, actor.monitorID).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("same-value override changed generation = (%d, %v), want 1", generation, err)
	}

	futureMonitorID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.webhook.pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status) VALUES($1,$2,'Future monitor','https://future.example.test/health','future.example.test:443','GET',200)`, futureMonitorID, actor.accountID); err != nil {
		t.Fatalf("create future monitor: %v", err)
	}
	future, err := f.push.MonitorPush(ctx, actor.accountID, futureMonitorID)
	if err != nil || !future.Enabled || future.Version != 0 {
		t.Fatalf("future monitor inherited setting = (%+v, %v), want enabled/version 0", future, err)
	}

	second, secondVerifier := f.createPairing(t, "", "198.51.100.76")
	secondStatus := f.confirmAndExchange(t, actor, second, secondVerifier, "default-second-device")
	if secondStatus.Status != "ready" || firstStatus.Status != "ready" {
		t.Fatalf("authorized devices are not ready: first=%+v second=%+v", firstStatus, secondStatus)
	}
	stillOff, err := f.push.MonitorPush(ctx, actor.accountID, actor.monitorID)
	if err != nil || stillOff.Enabled || stillOff.Version != unchangedOff.Version {
		t.Fatalf("explicit off after future device exchange = (%+v, %v), want unchanged false override", stillOff, err)
	}

	var futureEventID string
	for _, monitorID := range []string{actor.monitorID, futureMonitorID} {
		incidentID, e := outbox.ID()
		if e != nil {
			t.Fatal(e)
		}
		eventID, e := outbox.ID()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.webhook.pool.Exec(ctx, `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at) VALUES($1,$2,$3,now(),now())`, incidentID, monitorID, actor.accountID); e != nil {
			t.Fatal(e)
		}
		tx, e := f.webhook.pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		e = f.push.Enqueue(ctx, tx, incidentcontracts.Notification{EventID: eventID, AccountID: actor.accountID, MonitorID: monitorID, IncidentID: incidentID, Kind: "incident_opened", Sequence: 1, At: time.Now().UTC(), Observation: incidentcontracts.Observation{Name: "Policy fanout"}})
		if e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		var deliveries int
		if e = f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_deliveries WHERE event_id=$1`, eventID).Scan(&deliveries); e != nil {
			t.Fatal(e)
		}
		want := 2
		if monitorID == actor.monitorID {
			want = 0
		}
		if deliveries != want {
			t.Errorf("monitor %s delivery fanout = %d, want %d", monitorID, deliveries, want)
		}
		if monitorID == futureMonitorID {
			futureEventID = eventID
			var policyGeneration int64
			if e = f.webhook.pool.QueryRow(ctx, `SELECT min(policy_generation) FROM notifications.push_deliveries WHERE event_id=$1`, eventID).Scan(&policyGeneration); e != nil || policyGeneration != 0 {
				t.Errorf("inherited account policy snapshot generation = (%d, %v), want 0", policyGeneration, e)
			}
		}
	}
	changed, err := f.push.PutMonitorPush(ctx, actor.accountID, actor.sessionID, futureMonitorID, PutPushMonitorSettings{Enabled: false, Version: future.Version})
	if err != nil || changed.Enabled || changed.Version != 1 {
		t.Fatalf("disable future monitor with pending deliveries = (%+v, %v), want false/version 1", changed, err)
	}
	var cancelled int
	if err := f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_deliveries WHERE event_id=$1 AND status='cancelled' AND error_code='policy_changed'`, futureEventID).Scan(&cancelled); err != nil || cancelled != 2 {
		t.Fatalf("policy change retained old-generation deliveries: cancelled=%d err=%v, want 2", cancelled, err)
	}
}

func TestPushSameAccountSessionReauthenticationPreservesBindingGeneration(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "same-session-reauth", false)
	firstPair, firstVerifier := f.createPairing(t, "", "198.51.100.77")
	first := f.confirmAndExchange(t, actor, firstPair, firstVerifier, "first-device-token")

	secondPair, secondVerifier := f.createPairing(t, firstPair.Installation.Credential, "198.51.100.78")
	stillReady, err := f.push.Installation(context.Background(), firstPair.Installation.Credential)
	if err != nil || stillReady.Status != "ready" || stillReady.BindingGeneration != first.BindingGeneration {
		t.Fatalf("same-account bootstrap changed existing binding: %+v, %v", stillReady, err)
	}
	confirmation := f.confirmPairing(t, actor, secondPair)
	second, err := f.push.ExchangePairing(context.Background(), firstPair.Installation.Credential, secondPair.Pairing.ID, PushExchangeInput{CodeVerifier: secondVerifier, CompletionCode: confirmation.CompletionCode, Provider: "rustore", Token: "rotated-same-session-token", Permission: "granted", AppVersion: "1.2.3"})
	if err != nil || second.Status != "ready" || second.BindingGeneration != first.BindingGeneration || second.RegistrationVersion != first.RegistrationVersion+1 {
		t.Fatalf("same-session reauthorization = (%+v, %v), want unchanged binding generation and rotated registration", second, err)
	}
}

func TestPushDuplicateProviderTokenCannotRebindAnotherInstallation(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "duplicate-token", false)
	first, firstVerifier := f.createPairing(t, "", "198.51.100.70")
	firstStatus := f.confirmAndExchange(t, owner, first, firstVerifier, "provider-token-unique")
	second, secondVerifier := f.createPairing(t, "", "198.51.100.71")
	confirmation := f.confirmPairing(t, owner, second)
	_, err := f.push.ExchangePairing(context.Background(), second.Installation.Credential, second.Pairing.ID, PushExchangeInput{CodeVerifier: secondVerifier, CompletionCode: confirmation.CompletionCode, Provider: "rustore", Token: "provider-token-unique", Permission: "granted", AppVersion: "1.2.3"})
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
	f.push.cfg.Providers = []string{"rustore", "fcm"}
	capabilities := call(http.MethodGet, "/api/v1/push/capabilities", nil, "", false)
	var capabilityDTO struct {
		ProtocolVersion int      `json:"protocolVersion"`
		Providers       []string `json:"providers"`
	}
	if capabilities.Code != http.StatusOK || json.Unmarshal(capabilities.Body.Bytes(), &capabilityDTO) != nil || capabilityDTO.ProtocolVersion != 2 || strings.Join(capabilityDTO.Providers, ",") != "fcm,rustore" {
		t.Fatalf("public push capabilities = %d %s, want protocol 2 with configured providers", capabilities.Code, capabilities.Body.String())
	}
	verifier, challenge := newPushVerifier(t)
	unauthorized := call(http.MethodGet, "/api/v1/push/installation", nil, "bad.bearer", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("invalid native bearer status = %d, want 401", unauthorized.Code)
	}
	created := call(http.MethodPost, "/api/v1/push/pairings", PushPairingInput{CodeChallenge: challenge, ClientState: verifier, DeviceLabel: "HTTP Pixel", AppVersion: "1.2.3"}, "", false)
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
	if strings.Contains(browserPair.Body.String(), "completionCode") || strings.Contains(browserPair.Body.String(), "clientState") {
		t.Fatalf("pairing metadata exposed completion material: %s", browserPair.Body.String())
	}
	confirmed := call(http.MethodPost, "/api/v1/push/pairings/"+pairResult.Pairing.ID+"/confirm", map[string]any{}, "", true)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm pairing status = %d body=%s", confirmed.Code, confirmed.Body.String())
	}
	var confirmation PushPairingConfirmation
	if err := json.Unmarshal(confirmed.Body.Bytes(), &confirmation); err != nil || confirmation.PairingID != pairResult.Pairing.ID || confirmation.ClientState != verifier || len(confirmation.CompletionCode) != 43 {
		t.Fatalf("decode browser-only confirmation = (%+v, %v)", confirmation, err)
	}
	pollAfterConfirm := call(http.MethodGet, "/api/v1/push/pairings/"+pairResult.Pairing.ID, nil, "", true)
	if pollAfterConfirm.Code != http.StatusOK || strings.Contains(pollAfterConfirm.Body.String(), confirmation.CompletionCode) || strings.Contains(pollAfterConfirm.Body.String(), "completionCode") {
		t.Fatalf("pairing poll exposed or changed the one-time completion code: %d %s", pollAfterConfirm.Code, pollAfterConfirm.Body.String())
	}
	repeatConfirm := call(http.MethodPost, "/api/v1/push/pairings/"+pairResult.Pairing.ID+"/confirm", map[string]any{}, "", true)
	if repeatConfirm.Code != http.StatusConflict {
		t.Fatalf("repeat HTTP confirmation = %d %s, want 409", repeatConfirm.Code, repeatConfirm.Body.String())
	}
	if _, err := f.push.ExchangePairing(context.Background(), pairResult.Installation.Credential, pairResult.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, CompletionCode: confirmation.CompletionCode, Provider: "huawei", Token: "provider-private-token", Permission: "granted", AppVersion: "1.2.3"}); !errors.Is(err, ErrPushInvalid) {
		t.Fatalf("Huawei pairing exchange with FCM/RuStore-only config = %v, want invalid provider", err)
	}
	if _, err := f.push.ExchangePairing(context.Background(), pairResult.Installation.Credential, pairResult.Pairing.ID, PushExchangeInput{CodeVerifier: verifier, CompletionCode: confirmation.CompletionCode, Provider: "rustore", Token: "provider-private-token", Permission: "granted", AppVersion: "1.2.3"}); err != nil {
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
	monitorSettings := call(http.MethodGet, "/api/v1/monitors/"+actor.monitorID+"/push", nil, "", true)
	if monitorSettings.Code != http.StatusOK || strings.TrimSpace(monitorSettings.Body.String()) != `{"enabled":true,"version":0}` {
		t.Fatalf("monitor push GET = %d %s, want direct inherited {enabled:true,version:0}", monitorSettings.Code, monitorSettings.Body.String())
	}
	monitorSaved := call(http.MethodPut, "/api/v1/monitors/"+actor.monitorID+"/push", map[string]any{"enabled": false, "version": 0}, "", true)
	if monitorSaved.Code != http.StatusOK || strings.TrimSpace(monitorSaved.Body.String()) != `{"enabled":false,"version":1}` {
		t.Fatalf("monitor push PUT = %d %s, want direct {enabled:false,version:1}", monitorSaved.Code, monitorSaved.Body.String())
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

func TestPushCrossAccountRebindKeepsAccountPolicyAndRemovesOldDeliveries(t *testing.T) {
	f := newPushIntegrationFixture(t)
	first := f.addActor(t, "rebind-first", false)
	second := f.addActor(t, "rebind-second", false)
	pair, verifier := f.createPairing(t, "", "198.51.100.30")
	status := f.confirmAndExchange(t, first, pair, verifier, "old-token")
	ctx := context.Background()
	if initialPolicy, err := f.push.MonitorPush(ctx, first.accountID, first.monitorID); err != nil || !initialPolicy.Enabled {
		t.Fatalf("load inherited old account policy: %+v, %v", initialPolicy, err)
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
	stillReady, err := f.push.Installation(ctx, pair.Installation.Credential)
	if err != nil || stillReady.Status != "ready" || stillReady.BindingGeneration != status.BindingGeneration {
		t.Fatalf("bootstrap demoted existing binding before confirmation: state=%+v err=%v", stillReady, err)
	}
	var pendingDeliveries int
	if err := f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_deliveries WHERE installation_id=$1 AND status='pending'`, status.ID).Scan(&pendingDeliveries); err != nil || pendingDeliveries != 1 {
		t.Fatalf("bootstrap cancelled existing delivery: pending=%d err=%v", pendingDeliveries, err)
	}
	confirmation := f.confirmPairing(t, second, rebind)
	result, err := f.push.ExchangePairing(ctx, pair.Installation.Credential, rebind.Pairing.ID, PushExchangeInput{CodeVerifier: rebindVerifier, CompletionCode: confirmation.CompletionCode, Provider: "rustore", Token: "new-account-token", Permission: "granted", AppVersion: "1.2.3"})
	if err != nil || result.BindingGeneration != status.BindingGeneration+1 {
		t.Fatalf("rebind exchange = (%+v, %v), want one binding generation increment", result, err)
	}
	var oldDeliveries, cancelledDeliveries int
	if err := f.webhook.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('pending','leased')),count(*) FILTER (WHERE status='cancelled') FROM notifications.push_deliveries WHERE installation_id=$1`, status.ID).Scan(&oldDeliveries, &cancelledDeliveries); err != nil {
		t.Fatal(err)
	}
	oldPolicy, err := f.push.MonitorPush(ctx, first.accountID, first.monitorID)
	if err != nil || !oldPolicy.Enabled || oldPolicy.Version != 0 || oldDeliveries != 0 || cancelledDeliveries != 1 {
		t.Fatalf("cross-account rebind changed old account state: policy=%+v err=%v pending=%d cancelled=%d", oldPolicy, err, oldDeliveries, cancelledDeliveries)
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

func TestPushSessionRetentionFencesBoundAndConfirmedPairingsBeforeBootstrap(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "retention-race", false)
	firstPair, firstVerifier := f.createPairing(t, "", "198.51.100.121")
	installation := f.confirmAndExchange(t, actor, firstPair, firstVerifier, "retention-token")

	confirmedSessionID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	confirmedHash := sha256.Sum256([]byte("retention-confirmed-session-token"))
	if _, err := f.webhook.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,user_agent)
		VALUES($1,$2,$3,1,now()+interval '1 hour','retention pairing test')`, confirmedSessionID, actor.accountID, confirmedHash[:]); err != nil {
		t.Fatalf("insert second active session: %v", err)
	}
	confirmedPair, confirmedVerifier := f.createPairing(t, firstPair.Installation.Credential, "198.51.100.122")
	confirmation, err := f.push.ConfirmPairing(context.Background(), actor.accountID, confirmedSessionID, confirmedPair.Pairing.ID)
	if err != nil || confirmation.PairingID != confirmedPair.Pairing.ID {
		t.Fatalf("confirm pairing from second session = (%+v, %v)", confirmation, err)
	}
	if confirmation.ClientState != confirmedVerifier {
		t.Fatalf("confirmed pair client state=%q, want %q", confirmation.ClientState, confirmedVerifier)
	}
	if _, err = f.webhook.pool.Exec(context.Background(), `UPDATE auth.sessions SET expires_at=now()-interval '1 second' WHERE id=ANY($1::uuid[])`, []string{actor.sessionID, confirmedSessionID}); err != nil {
		t.Fatalf("expire the installation and confirmation sessions: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retention, err := f.webhook.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := auth.LockExpiredSessionBatchTx(ctx, retention)
	if err != nil {
		_ = retention.Rollback(context.Background())
		t.Fatalf("lock expired session batch: %v", err)
	}
	if len(ids) != 2 {
		_ = retention.Rollback(context.Background())
		t.Fatalf("retention selected %d sessions, want both installation and confirmed-pair sessions", len(ids))
	}
	if err = FenceSessionDeletionTx(ctx, retention, ids); err != nil {
		_ = retention.Rollback(context.Background())
		t.Fatalf("fence installation referenced by expired sessions: %v", err)
	}
	if err = auth.DeleteExpiredSessionBatchTx(ctx, retention, ids); err != nil {
		_ = retention.Rollback(context.Background())
		t.Fatalf("delete expired, fenced sessions: %v", err)
	}

	bootstrapStarted := make(chan struct{})
	bootstrapDone := make(chan struct {
		result PushPairingResult
		err    error
	}, 1)
	verifier, challenge := newPushVerifier(t)
	go func() {
		close(bootstrapStarted)
		result, createErr := f.push.CreatePairing(ctx, firstPair.Installation.Credential, "198.51.100.123", PushPairingInput{
			CodeChallenge: challenge,
			ClientState:   verifier,
			DeviceLabel:   "Pixel retention race",
			AppVersion:    "1.2.3",
		})
		bootstrapDone <- struct {
			result PushPairingResult
			err    error
		}{result: result, err: createErr}
	}()
	<-bootstrapStarted
	select {
	case outcome := <-bootstrapDone:
		_ = retention.Rollback(context.Background())
		t.Fatalf("bootstrap crossed session-retention installation fence: (%+v, %v)", outcome.result, outcome.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err = retention.Commit(ctx); err != nil {
		t.Fatalf("commit fenced session retention: %v", err)
	}
	var bootstrapped struct {
		result PushPairingResult
		err    error
	}
	select {
	case bootstrapped = <-bootstrapDone:
	case <-ctx.Done():
		t.Fatal("bootstrap did not resume after session-retention commit")
	}
	if bootstrapped.err != nil || bootstrapped.result.Pairing.ID == "" {
		t.Fatalf("bootstrap after retention = (%+v, %v)", bootstrapped.result, bootstrapped.err)
	}
	var remainingSessions, pendingPairings, currentBinding int
	if err = f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM auth.sessions WHERE id=ANY($1::uuid[])`, ids).Scan(&remainingSessions); err != nil {
		t.Fatal(err)
	}
	if err = f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_pairings WHERE installation_id=$1 AND id=$2 AND expires_at>now() AND consumed_at IS NULL AND account_id IS NULL AND session_id IS NULL`, installation.ID, bootstrapped.result.Pairing.ID).Scan(&pendingPairings); err != nil {
		t.Fatal(err)
	}
	if err = f.webhook.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.push_installations WHERE id=$1 AND status='ready' AND account_id=$2 AND session_id IS NULL`, installation.ID, actor.accountID).Scan(&currentBinding); err != nil {
		t.Fatal(err)
	}
	if remainingSessions != 0 || pendingPairings != 1 || currentBinding != 1 {
		t.Fatalf("retention/bootstrap outcome sessions=%d newPending=%d readyBinding=%d, want 0/1/1", remainingSessions, pendingPairings, currentBinding)
	}
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
	if status != "accepted" {
		t.Fatalf("completion after token rotation state = %q, want accepted for the already attempted destination", status)
	}
	state, err := f.push.Installation(context.Background(), pair.Installation.Credential)
	if err != nil || state.Status != "ready" || state.RegistrationVersion <= installation.RegistrationVersion {
		t.Fatalf("current registration after accepted stale-route completion = (%+v, %v), want rotated ready token", state, err)
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
	verifier, challenge := newPushVerifier(t)
	started := make(chan struct{})
	repaired := make(chan struct {
		result PushPairingResult
		err    error
	}, 1)
	go func() {
		close(started)
		result, err := f.push.CreatePairing(ctx, initial.Installation.Credential, "198.51.100.92", PushPairingInput{CodeChallenge: challenge, ClientState: verifier, DeviceLabel: "Pixel", AppVersion: "1.0.0"})
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
