package notifications

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
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
