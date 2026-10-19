package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

func emailVerificationToken(t *testing.T, mailer *integrationMailer, to string) string {
	t.Helper()
	mailer.mu.Lock()
	defer mailer.mu.Unlock()
	for _, message := range mailer.messages {
		if message.to != to || !strings.Contains(message.subject, "Подтвердите email аккаунта") {
			continue
		}
		for _, field := range strings.Fields(message.body.Text) {
			u, err := url.Parse(strings.TrimSuffix(field, "."))
			if err != nil {
				continue
			}
			fragment, err := url.ParseQuery(u.Fragment)
			if err == nil {
				if token := fragment.Get("verify-email"); token != "" {
					return token
				}
			}
		}
	}
	t.Fatalf("verification email for %q did not include a confirmation token", to)
	return ""
}

func TestEmailVerificationIsAccountScopedExpiresOnceAndKeepsSession(t *testing.T) {
	f := newAuthFixture(t)
	const ownerEmail = "email-verification-owner@example.test"
	const otherEmail = "email-verification-other@example.test"
	ownerRegistration := f.register(t, ownerEmail, "Verification Owner", integrationPass)
	if response := f.verify(t, ownerRegistration, integrationPass); response.Code != http.StatusNoContent {
		t.Fatalf("verify owner registration: status=%d body=%s", response.Code, response.Body.String())
	}
	otherRegistration := f.register(t, otherEmail, "Verification Other", integrationPass)
	if response := f.verify(t, otherRegistration, integrationPass); response.Code != http.StatusNoContent {
		t.Fatalf("verify other registration: status=%d body=%s", response.Code, response.Body.String())
	}
	var ownerRegistrationVerified, otherRegistrationVerified bool
	if err := f.pool.QueryRow(context.Background(), `SELECT email_verified_at IS NOT NULL FROM auth.credentials WHERE email=$1`, ownerEmail).Scan(&ownerRegistrationVerified); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT email_verified_at IS NOT NULL FROM auth.credentials WHERE email=$1`, otherEmail).Scan(&otherRegistrationVerified); err != nil {
		t.Fatal(err)
	}
	if !ownerRegistrationVerified || !otherRegistrationVerified {
		t.Fatalf("successful registration verification markers: owner=%t other=%t", ownerRegistrationVerified, otherRegistrationVerified)
	}

	var ownerID, otherID string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id::text FROM auth.credentials WHERE email=$1`, ownerEmail).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id::text FROM auth.credentials WHERE email=$1`, otherEmail).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.credentials SET email_verified_at=NULL WHERE account_id IN ($1,$2)`, ownerID, otherID); err != nil {
		t.Fatal(err)
	}
	precheck, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, precheckErr := EmailForNotificationsTx(context.Background(), precheck, ownerID)
	_ = precheck.Rollback(context.Background())
	if !errors.Is(precheckErr, ErrEmailUnverified) {
		t.Fatalf("unverified credentials were accepted as notification address: err=%v, want ErrEmailUnverified", precheckErr)
	}
	ownerCookie, ownerCSRF, _ := f.login(t, ownerEmail, integrationPass)
	otherCookie, otherCSRF, _ := f.login(t, otherEmail, integrationPass)
	if ownerCookie == nil || otherCookie == nil {
		t.Fatal("verified registration accounts could not log in after their verification marker was cleared")
	}
	ownerHash := security.Digest(ownerCookie.Value)
	var sessionID string
	var sessionCredentialVersion int64
	var credentialVersion int64
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text,credential_version FROM auth.sessions WHERE token_hash=$1`, ownerHash[:]).Scan(&sessionID, &sessionCredentialVersion); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT version FROM auth.credentials WHERE account_id=$1`, ownerID).Scan(&credentialVersion); err != nil {
		t.Fatal(err)
	}

	request := f.request(t, http.MethodPost, "/api/v1/me/email-verification", map[string]any{}, ownerCookie, ownerCSRF, integrationOrigin, "")
	if request.Code != http.StatusAccepted {
		t.Fatalf("request owner email verification: status=%d body=%s", request.Code, request.Body.String())
	}
	ownerToken := emailVerificationToken(t, f.mail, ownerEmail)
	otherAttempt := f.request(t, http.MethodPost, "/api/v1/me/email-verification/confirm", map[string]string{"token": ownerToken}, otherCookie, otherCSRF, integrationOrigin, "")
	if otherAttempt.Code != http.StatusBadRequest {
		t.Fatalf("foreign account confirmation status=%d body=%s, want 400", otherAttempt.Code, otherAttempt.Body.String())
	}
	var otherVerified bool
	if err := f.pool.QueryRow(context.Background(), `SELECT email_verified_at IS NOT NULL FROM auth.credentials WHERE account_id=$1`, otherID).Scan(&otherVerified); err != nil || otherVerified {
		t.Fatalf("foreign token changed other account verification state=%t err=%v", otherVerified, err)
	}

	ownerConfirm := f.request(t, http.MethodPost, "/api/v1/me/email-verification/confirm", map[string]string{"token": ownerToken}, ownerCookie, ownerCSRF, integrationOrigin, "")
	if ownerConfirm.Code != http.StatusNoContent {
		t.Fatalf("confirm owner email: status=%d body=%s", ownerConfirm.Code, ownerConfirm.Body.String())
	}
	replay := f.request(t, http.MethodPost, "/api/v1/me/email-verification/confirm", map[string]string{"token": ownerToken}, ownerCookie, ownerCSRF, integrationOrigin, "")
	if replay.Code != http.StatusNoContent {
		t.Fatalf("same-account verification replay status=%d body=%s, want idempotent 204", replay.Code, replay.Body.String())
	}
	profile := f.request(t, http.MethodGet, "/api/v1/me", nil, ownerCookie, "", "", "")
	if profile.Code != http.StatusOK || !strings.Contains(profile.Body.String(), `"emailVerified":true`) {
		t.Fatalf("profile after email confirmation status=%d body=%s", profile.Code, profile.Body.String())
	}
	var currentSessionID string
	var currentSessionVersion int64
	var revokedAt *time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text,credential_version,revoked_at FROM auth.sessions WHERE token_hash=$1`, ownerHash[:]).Scan(&currentSessionID, &currentSessionVersion, &revokedAt); err != nil {
		t.Fatal(err)
	}
	var currentCredentialVersion int64
	if err := f.pool.QueryRow(context.Background(), `SELECT version FROM auth.credentials WHERE account_id=$1`, ownerID).Scan(&currentCredentialVersion); err != nil {
		t.Fatal(err)
	}
	if currentSessionID != sessionID || currentSessionVersion != sessionCredentialVersion || currentCredentialVersion != credentialVersion || revokedAt != nil {
		t.Fatalf("email verification rotated or revoked the session/credential: session=%s/%d (was %s/%d), credential=%d (was %d), revoked=%v", currentSessionID, currentSessionVersion, sessionID, sessionCredentialVersion, currentCredentialVersion, credentialVersion, revokedAt)
	}

	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	confirmedEmail, err := EmailForNotificationsTx(context.Background(), tx, ownerID)
	_ = tx.Rollback(context.Background())
	if err != nil || confirmedEmail != ownerEmail {
		t.Fatalf("confirmed email for notifications = %q, err=%v", confirmedEmail, err)
	}

	request = f.request(t, http.MethodPost, "/api/v1/me/email-verification", map[string]any{}, otherCookie, otherCSRF, integrationOrigin, "")
	if request.Code != http.StatusAccepted {
		t.Fatalf("request other email verification for expiry case: status=%d body=%s", request.Code, request.Body.String())
	}
	expiredToken := emailVerificationToken(t, f.mail, otherEmail)
	expiredHash := security.Digest(expiredToken)
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.email_verification_tokens SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, expiredHash[:]); err != nil {
		t.Fatal(err)
	}
	expired := f.request(t, http.MethodPost, "/api/v1/me/email-verification/confirm", map[string]string{"token": expiredToken}, otherCookie, otherCSRF, integrationOrigin, "")
	if expired.Code != http.StatusBadRequest {
		t.Fatalf("expired email verification status=%d body=%s, want 400", expired.Code, expired.Body.String())
	}
	var expiredVerified bool
	if err := f.pool.QueryRow(context.Background(), `SELECT email_verified_at IS NOT NULL FROM auth.credentials WHERE account_id=$1`, otherID).Scan(&expiredVerified); err != nil || expiredVerified {
		t.Fatalf("expired token changed account verification state=%t err=%v", expiredVerified, err)
	}
}
