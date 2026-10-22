package notifications

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

func nativePushCredential(t *testing.T) string {
	t.Helper()
	id, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	return id + "." + base64.RawURLEncoding.EncodeToString(secret[:])
}

func nativePushReceiptToken(t *testing.T) string {
	t.Helper()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(token[:])
}

func makePushActorNative(t *testing.T, f *pushIntegrationFixture, actor pushTestActor) authcontracts.Principal {
	t.Helper()
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE auth.sessions SET client_kind='android',revoke_token_hash=decode(repeat('00',32),'hex') WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	return authcontracts.Principal{AccountID: actor.accountID, SessionID: actor.sessionID, ClientKind: "android", Role: "user"}
}

func TestNativePushBindIsRetrySafeAndRejectsStaleOrForeignCredentials(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner := f.addActor(t, "native-owner", false)
	ownerPrincipal := makePushActorNative(t, f, owner)
	credential := nativePushCredential(t)
	in := NativePushBindInput{InstallationCredential: credential, Provider: "rustore", Token: "native-push-provider-token",
		Permission: "granted", DeviceLabel: "Pixel native", AppVersion: "1.2.3"}
	first, err := f.push.BindNative(context.Background(), ownerPrincipal, in)
	if err != nil {
		t.Fatalf("initial native bind: %v", err)
	}
	if first.Installation.Status != "ready" || first.Installation.BindingGeneration != 1 || first.Installation.RegistrationVersion != 1 || first.InstallationCredential != "" {
		t.Fatalf("initial native bind result leaked or omitted expected state: %+v", first)
	}
	retry, err := f.push.BindNative(context.Background(), ownerPrincipal, in)
	if err != nil || retry.Installation.ID != first.Installation.ID || retry.Installation.RegistrationVersion != 1 {
		t.Fatalf("exact initial retry = (%+v, %v), want idempotent existing bind", retry, err)
	}
	denied, err := f.push.UpdateRegistration(context.Background(), credential, PushRegistrationInput{
		RegistrationVersion: first.Installation.RegistrationVersion, Provider: "rustore", Permission: "denied",
	})
	if err != nil || denied.Status != "permission_denied" {
		t.Fatalf("disable permission state = (%+v, %v), want permission_denied", denied, err)
	}
	permissionRetry, err := f.push.BindNative(context.Background(), ownerPrincipal, in)
	if err != nil || permissionRetry.Installation.Status != "permission_denied" {
		t.Fatalf("exact retry after permission denial = (%+v, %v), want current permission_denied state", permissionRetry, err)
	}
	if _, err := f.push.NativeInstallation(context.Background(), ownerPrincipal, credential); err != nil {
		t.Fatalf("owner cannot read own installation: %v", err)
	}
	updatedToken := "native-push-provider-token-rotated"
	in.Token = updatedToken
	in.BindingGeneration = &first.Installation.BindingGeneration
	in.RegistrationVersion = &denied.RegistrationVersion
	updated, err := f.push.BindNative(context.Background(), ownerPrincipal, in)
	if err != nil || updated.Installation.RegistrationVersion != 3 || updated.Installation.BindingGeneration != 1 {
		t.Fatalf("versioned token refresh = (%+v, %v), want registration 3 and binding generation 1", updated, err)
	}
	if _, err := f.push.BindNative(context.Background(), ownerPrincipal, NativePushBindInput{
		InstallationCredential: credential, Provider: "rustore", Token: "native-push-provider-token", Permission: "granted",
		DeviceLabel: "Pixel native", AppVersion: "1.2.3", BindingGeneration: &first.Installation.BindingGeneration,
		RegistrationVersion: &first.Installation.RegistrationVersion,
	}); !errors.Is(err, ErrPushConflict) {
		t.Fatalf("stale bind replay error=%v, want conflict", err)
	}
	otherSession := f.addActor(t, "native-other-session", false)
	otherPrincipal := makePushActorNative(t, f, otherSession)
	if _, err := f.push.NativeInstallation(context.Background(), otherPrincipal, credential); !errors.Is(err, ErrPushReconnect) {
		t.Fatalf("other account installation lookup error=%v, want reconnect requirement", err)
	}
	if _, err := f.push.BindNative(context.Background(), otherPrincipal, NativePushBindInput{
		InstallationCredential: credential, Provider: "rustore", Token: "other-account-token", Permission: "granted",
		DeviceLabel: "Other device", AppVersion: "1.2.3", BindingGeneration: &updated.Installation.BindingGeneration,
		RegistrationVersion: &updated.Installation.RegistrationVersion,
	}); !errors.Is(err, ErrPushAccountChange) {
		t.Fatalf("cross-account rebind error=%v, want explicit account-change denial", err)
	}
	stillOwned, err := f.push.NativeInstallation(context.Background(), ownerPrincipal, credential)
	if err != nil || stillOwned.AccountID != owner.accountID || stillOwned.SessionID != owner.sessionID || stillOwned.Installation.RegistrationVersion != 3 {
		t.Fatalf("foreign bind changed owner state: result=%+v err=%v", stillOwned, err)
	}
}

func TestNativePushTestUsesSeparateWorkerSenderAndMonotonicDisplayReceipt(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "push-test-no-quota", false)
	principal := makePushActorNative(t, f, actor)
	credential := nativePushCredential(t)
	bound, err := f.push.BindNative(context.Background(), principal, NativePushBindInput{
		InstallationCredential: credential, Provider: "rustore", Token: "synthetic-test-provider-token",
		Permission: "granted", DeviceLabel: "Synthetic test phone", AppVersion: "2.1.0",
	})
	if err != nil {
		t.Fatalf("bind native installation with API service that has no sender: %v", err)
	}

	requestID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	created, isNew, err := f.push.CreatePushTest(context.Background(), principal, credential, PushTestInput{RequestID: requestID})
	if err != nil || !isNew || created.State != "queued" {
		t.Fatalf("create push test without monitor quota or API sender = (%+v, new=%t, err=%v)", created, isNew, err)
	}
	retry, isNew, err := f.push.CreatePushTest(context.Background(), principal, credential, PushTestInput{RequestID: requestID})
	if err != nil || isNew || retry.ID != created.ID {
		t.Fatalf("exact idempotent create retry = (%+v, new=%t, err=%v)", retry, isNew, err)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_test_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatalf("make the isolated worker item unambiguously due: %v", err)
	}

	var sent int
	var receiptToken string
	wrongReceipt := nativePushReceiptToken(t)
	worker := *f.push
	worker.cfg.Sender = pushSenderFunc(func(_ context.Context, token string, data map[string]string, ttl time.Duration) error {
		sent++
		if token != "synthetic-test-provider-token" || ttl <= 0 {
			t.Errorf("worker route token=%q TTL=%s", token, ttl)
		}
		if len(data) != 5 || data["v"] != "2" || data["deliveryId"] != created.ID ||
			data["installationId"] != bound.Installation.ID || data["bindingGeneration"] != "1" || data["expiresAt"] == "" {
			t.Errorf("test notification must use the exact data-only v2 envelope: %#v", data)
		}
		content, fetchErr := f.push.PushTestContent(context.Background(), credential, created.ID)
		if fetchErr != nil {
			t.Errorf("fetch synthetic content from worker callback: %v", fetchErr)
			return fetchErr
		}
		receiptToken = content.ReceiptToken
		beforeReceipt, statusErr := f.push.PushTest(context.Background(), principal, credential, created.ID)
		if statusErr != nil || beforeReceipt.ReceivedAt != nil {
			t.Errorf("fetching content alone must not count as device receipt: status=%+v err=%v", beforeReceipt, statusErr)
			return statusErr
		}
		if receiptErr := f.push.RecordPushTestReceipt(context.Background(), credential, created.ID, PushTestReceipt{
			ReceiptToken: wrongReceipt, Stage: "received",
		}); !errors.Is(receiptErr, ErrPushNotFound) {
			t.Errorf("a well-formed but incorrect receipt capability must be rejected: %v", receiptErr)
			return receiptErr
		}
		for _, stage := range []string{"display_failed", "display_submitted"} {
			if receiptErr := f.push.RecordPushTestReceipt(context.Background(), credential, created.ID, PushTestReceipt{
				ReceiptToken: content.ReceiptToken, Stage: stage,
			}); receiptErr != nil {
				t.Errorf("record %s receipt: %v", stage, receiptErr)
				return receiptErr
			}
		}
		// Model the provider accepting the request while its response is lost.
		return context.DeadlineExceeded
	})
	processed, err := worker.processTestDelivery(context.Background())
	if err != nil || !processed || sent != 1 {
		var state, code string
		var attempts int
		_ = f.webhook.pool.QueryRow(context.Background(), `SELECT status,error_code,attempts FROM notifications.push_test_deliveries WHERE id=$1`, created.ID).Scan(&state, &code, &attempts)
		var next time.Time
		var due bool
		_ = f.webhook.pool.QueryRow(context.Background(), `SELECT next_attempt_at, next_attempt_at<=now() FROM notifications.push_test_deliveries WHERE id=$1`, created.ID).Scan(&next, &due)
		t.Fatalf("synthetic worker delivery = (processed=%t sent=%d err=%v state=%s/%s attempts=%d workerSender=%t next=%s due=%t)", processed, sent, err, state, code, attempts, worker.cfg.Sender != nil, next, due)
	}

	status, err := f.push.PushTest(context.Background(), principal, credential, created.ID)
	if err != nil || status.State != "display_submitted" || status.ErrorCode != "" || status.ReceivedAt == nil || status.DisplaySubmittedAt == nil {
		t.Fatalf("phone receipt must outrank ambiguous provider failure: status=%+v err=%v", status, err)
	}
	var received, displayed, failed *time.Time
	var providerState, errorCode string
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT status,error_code,received_at,display_submitted_at,display_failed_at
	 FROM notifications.push_test_deliveries WHERE id=$1`, created.ID).Scan(&providerState, &errorCode, &received, &displayed, &failed); err != nil {
		t.Fatal(err)
	}
	if providerState != "accepted" || errorCode != "" || received == nil || displayed == nil || failed == nil {
		t.Fatalf("receipt monotonicity/provider completion state=%q error=%q received=%v submitted=%v failed=%v", providerState, errorCode, received, displayed, failed)
	}
	foreignActor := f.addActor(t, "push-test-foreign", false)
	foreignPrincipal := makePushActorNative(t, f, foreignActor)
	foreignCredential := nativePushCredential(t)
	if _, err := f.push.BindNative(context.Background(), foreignPrincipal, NativePushBindInput{
		InstallationCredential: foreignCredential, Provider: "rustore", Token: "synthetic-foreign-provider-token",
		Permission: "granted", DeviceLabel: "Other synthetic phone", AppVersion: "2.1.0",
	}); err != nil {
		t.Fatalf("bind valid foreign installation: %v", err)
	}
	if _, err := f.push.PushTestContent(context.Background(), foreignCredential, created.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign bound installation content fetch error=%v, want not found", err)
	}
	if err := f.push.RecordPushTestReceipt(context.Background(), foreignCredential, created.ID, PushTestReceipt{
		ReceiptToken: receiptToken, Stage: "received",
	}); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("foreign bound installation receipt error=%v, want not found", err)
	}
}

func TestNativePushTestThrottleSerializesConcurrentRequests(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "push-test-throttle", false)
	principal := makePushActorNative(t, f, actor)
	credential := nativePushCredential(t)
	if _, err := f.push.BindNative(context.Background(), principal, NativePushBindInput{
		InstallationCredential: credential, Provider: "rustore", Token: "synthetic-throttle-provider-token",
		Permission: "granted", DeviceLabel: "Synthetic throttle phone", AppVersion: "2.1.0",
	}); err != nil {
		t.Fatalf("bind throttle installation: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestID, err := outbox.ID()
			if err != nil {
				errs <- err
				return
			}
			<-start
			_, _, err = f.push.CreatePushTest(context.Background(), principal, credential, PushTestInput{RequestID: requestID})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var created, limited int
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrPushRateLimited):
			limited++
		default:
			t.Errorf("concurrent push test create error=%v", err)
		}
	}
	if created != 1 || limited != 1 {
		t.Fatalf("per-installation throttle must serialize concurrent requests: created=%d limited=%d", created, limited)
	}
}

func TestNativePushTestAccountLimitCountsAcrossInstallations(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "push-test-account-limit", false)
	principal := makePushActorNative(t, f, actor)
	ctx := context.Background()
	for index := 0; index < 6; index++ {
		credential := nativePushCredential(t)
		if _, err := f.push.BindNative(ctx, principal, NativePushBindInput{
			InstallationCredential: credential, Provider: "rustore", Token: fmt.Sprintf("synthetic-account-limit-token-%d", index),
			Permission: "granted", DeviceLabel: fmt.Sprintf("Synthetic limit phone %d", index), AppVersion: "2.1.0",
		}); err != nil {
			t.Fatalf("bind installation %d: %v", index, err)
		}
		requestID, err := outbox.ID()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = f.push.CreatePushTest(ctx, principal, credential, PushTestInput{RequestID: requestID})
		if index < 5 && err != nil {
			t.Fatalf("create account test %d of 5: %v", index+1, err)
		}
		if index == 5 && !errors.Is(err, ErrPushRateLimited) {
			t.Fatalf("sixth test across distinct installations error=%v, want account throttle", err)
		}
	}
}

func TestNativePushTestRevokedSessionAfterClaimBlocksSendAndReceipt(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "push-test-revoked-after-claim", false)
	principal := makePushActorNative(t, f, actor)
	credential := nativePushCredential(t)
	if _, err := f.push.BindNative(context.Background(), principal, NativePushBindInput{
		InstallationCredential: credential, Provider: "rustore", Token: "synthetic-revocation-provider-token",
		Permission: "granted", DeviceLabel: "Synthetic revocation phone", AppVersion: "2.1.0",
	}); err != nil {
		t.Fatalf("bind revocation test installation: %v", err)
	}
	requestID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := f.push.CreatePushTest(context.Background(), principal, credential, PushTestInput{RequestID: requestID})
	if err != nil {
		t.Fatalf("create revocation test item: %v", err)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_test_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	var sent int
	worker := *f.push
	worker.cfg.Sender = pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error {
		sent++
		return nil
	})
	attempt, err := worker.claimPushTest(context.Background())
	if err != nil || attempt == nil || attempt.ID != created.ID {
		t.Fatalf("claim before session revocation = (%+v, %v)", attempt, err)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE auth.sessions SET revoked_at=now() WHERE id=$1`, actor.sessionID); err != nil {
		t.Fatal(err)
	}
	valid, err := worker.beforePushTest(context.Background(), *attempt)
	if err != nil || valid {
		t.Fatalf("worker must reject revoked session before provider send: valid=%t err=%v", valid, err)
	}
	if sent != 0 {
		t.Fatalf("revoked session reached synthetic sender %d times", sent)
	}
	if _, err := f.push.PushTestContent(context.Background(), credential, created.ID); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("revoked source session content fetch error=%v, want not found", err)
	}
	if err := f.push.RecordPushTestReceipt(context.Background(), credential, created.ID, PushTestReceipt{
		ReceiptToken: nativePushReceiptToken(t), Stage: "received",
	}); !errors.Is(err, ErrPushNotFound) {
		t.Fatalf("revoked source session receipt error=%v, want not found", err)
	}
}

func TestNativePushTestFreezesRouteAndStopsAfterThreeAttempts(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "push-test-frozen-route", false)
	principal := makePushActorNative(t, f, actor)
	credential := nativePushCredential(t)
	bound, err := f.push.BindNative(context.Background(), principal, NativePushBindInput{
		InstallationCredential: credential, Provider: "rustore", Token: "synthetic-original-route-token",
		Permission: "granted", DeviceLabel: "Synthetic frozen route phone", AppVersion: "2.1.0",
	})
	if err != nil {
		t.Fatalf("bind frozen-route installation: %v", err)
	}
	requestID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := f.push.CreatePushTest(context.Background(), principal, credential, PushTestInput{RequestID: requestID})
	if err != nil {
		t.Fatalf("create frozen-route item: %v", err)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_test_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	worker := *f.push
	worker.cfg.Sender = pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil })
	for index := 1; index <= 3; index++ {
		attempt, err := worker.claimPushTest(context.Background())
		if err != nil || attempt == nil || attempt.ID != created.ID {
			t.Fatalf("claim attempt %d = (%+v, %v)", index, attempt, err)
		}
		if attempt.RegistrationToken != "synthetic-original-route-token" || attempt.RegistrationVersion != bound.Installation.RegistrationVersion {
			t.Fatalf("attempt %d changed its frozen provider route: provider=%s token=%q registration=%d", index,
				attempt.Provider, attempt.RegistrationToken, attempt.RegistrationVersion)
		}
		if valid, err := worker.beforePushTest(context.Background(), *attempt); err != nil || !valid {
			t.Fatalf("before-send guard for attempt %d = (%t, %v)", index, valid, err)
		}
		if err := worker.completePushTest(context.Background(), *attempt, errors.New("synthetic transient provider failure")); err != nil {
			t.Fatalf("complete failed attempt %d: %v", index, err)
		}
		if index == 1 {
			generation := bound.Installation.BindingGeneration
			registration := bound.Installation.RegistrationVersion
			if _, err := f.push.BindNative(context.Background(), principal, NativePushBindInput{
				InstallationCredential: credential, Provider: "rustore", Token: "synthetic-newer-route-token",
				Permission: "granted", DeviceLabel: "Synthetic frozen route phone", AppVersion: "2.1.0",
				BindingGeneration: &generation, RegistrationVersion: &registration,
			}); err != nil {
				t.Fatalf("rotate installation route while test retries: %v", err)
			}
		}
		if index < 3 {
			if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_test_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
				t.Fatalf("make retry %d due: %v", index+1, err)
			}
		}
	}
	var state, code string
	var attempts int
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT status,error_code,attempts FROM notifications.push_test_deliveries WHERE id=$1`, created.ID).Scan(&state, &code, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != "request_failed" || attempts != 3 {
		t.Fatalf("three transient failures must close the test: state=%s code=%s attempts=%d", state, code, attempts)
	}
	if attempt, err := worker.claimPushTest(context.Background()); err != nil || attempt != nil {
		t.Fatalf("exhausted test cannot claim a fourth attempt: attempt=%+v err=%v", attempt, err)
	}
}

func TestNativePushTestExpiresBeforeProviderSend(t *testing.T) {
	f := newPushIntegrationFixture(t)
	actor := f.addActor(t, "push-test-expiry", false)
	principal := makePushActorNative(t, f, actor)
	credential := nativePushCredential(t)
	if _, err := f.push.BindNative(context.Background(), principal, NativePushBindInput{
		InstallationCredential: credential, Provider: "rustore", Token: "synthetic-expired-provider-token",
		Permission: "granted", DeviceLabel: "Synthetic expired phone", AppVersion: "2.1.0",
	}); err != nil {
		t.Fatalf("bind expiry installation: %v", err)
	}
	requestID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := f.push.CreatePushTest(context.Background(), principal, credential, PushTestInput{RequestID: requestID})
	if err != nil {
		t.Fatalf("create expiring test: %v", err)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_test_deliveries SET deadline_at=now()-interval '1 second',next_attempt_at=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	var sent int
	worker := *f.push
	worker.cfg.Sender = pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { sent++; return nil })
	if _, err := worker.processTestDelivery(context.Background()); err != nil {
		t.Fatalf("expired due item processing: %v", err)
	}
	if sent != 0 {
		t.Fatalf("expired challenge reached synthetic sender %d times", sent)
	}
	status, err := f.push.PushTest(context.Background(), principal, credential, created.ID)
	if err != nil || status.State != "expired" || status.ErrorCode != "deadline_exceeded" {
		t.Fatalf("expired status = (%+v, %v)", status, err)
	}
}

func TestNativePushTestUsesTheSharedTwoDeliveryWorkerCap(t *testing.T) {
	f := newPushIntegrationFixture(t)
	makeOwner := func(label, ip, token string) (pushTestActor, PushInstallationStatus) {
		actor := f.addActor(t, label, false)
		if err := billing.New(f.webhook.pool, true).GrantLocal(context.Background(), actor.accountID, 5,
			time.Now().Add(24*time.Hour), "shared push worker cap test"); err != nil {
			t.Fatalf("grant isolated delivery quota: %v", err)
		}
		pair, verifier := f.createPairing(t, "", ip)
		return actor, f.confirmAndExchange(t, actor, pair, verifier, token)
	}
	firstActor, firstInstall := makeOwner("shared-worker-cap-one", "198.51.100.61", "synthetic-shared-cap-token-one")
	secondActor, secondInstall := makeOwner("shared-worker-cap-two", "198.51.100.62", "synthetic-shared-cap-token-two")
	firstID := f.addDeliveryEvent(t, firstActor, firstInstall.ID, "down", 1, "Shared worker cap one")
	secondID := f.addDeliveryEvent(t, secondActor, secondInstall.ID, "down", 1, "Shared worker cap two")
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_deliveries SET next_attempt_at=now()-interval '2 seconds' WHERE id=$1`, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, secondID); err != nil {
		t.Fatal(err)
	}

	nativeActor := f.addActor(t, "shared-worker-cap-native", false)
	nativePrincipal := makePushActorNative(t, f, nativeActor)
	nativeCredential := nativePushCredential(t)
	if _, err := f.push.BindNative(context.Background(), nativePrincipal, NativePushBindInput{
		InstallationCredential: nativeCredential, Provider: "rustore", Token: "synthetic-cap-test-token",
		Permission: "granted", DeviceLabel: "Synthetic cap phone", AppVersion: "2.1.0",
	}); err != nil {
		t.Fatalf("bind native cap installation: %v", err)
	}
	requestID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := f.push.CreatePushTest(context.Background(), nativePrincipal, nativeCredential, PushTestInput{RequestID: requestID})
	if err != nil {
		t.Fatalf("create native cap item: %v", err)
	}
	f.push.cfg.Sender = pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil })

	first, err := f.push.ClaimPush(context.Background())
	if err != nil || first == nil || first.ID != firstID && first.ID != secondID {
		t.Fatalf("first regular delivery claim = (%+v, %v), want one seeded delivery", first, err)
	}
	second, err := f.push.ClaimPush(context.Background())
	if err != nil || second == nil || second.ID == first.ID || second.ID != firstID && second.ID != secondID {
		t.Fatalf("second regular delivery claim = (%+v, %v), want the other seeded delivery", second, err)
	}
	worker := *f.push
	worker.cfg.Sender = pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil })
	if attempt, err := worker.claimPushTest(context.Background()); err != nil || attempt != nil {
		t.Fatalf("test delivery must wait while two regular pushes hold leases: attempt=%+v err=%v", attempt, err)
	}
	var attempts int
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT attempts FROM notifications.push_test_deliveries WHERE id=$1`, queued.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("shared worker cap should defer native test before attempt, attempts=%d", attempts)
	}
}
