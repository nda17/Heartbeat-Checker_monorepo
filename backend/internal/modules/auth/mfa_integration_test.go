package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
)

func mfaAdminFixture(t *testing.T, email string) (*authFixture, *http.Cookie, string) {
	t.Helper()
	f := newAuthFixture(t)
	f.signup(t, email)
	var accountID string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id FROM auth.credentials WHERE email=$1`, email).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE accounts.accounts SET role='admin' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	module, err := New(f.pool, accounts.New(f.pool), integrationCaptcha{}, f.mail, Config{
		Origin: integrationOrigin, ChallengeTTL: time.Hour, ResetTTL: time.Hour,
		RateLimitWindow: time.Hour, MFAEncryptionKey: key, MFAAudit: testMFAAudit,
	})
	if err != nil {
		t.Fatalf("create MFA-enabled auth module: %v", err)
	}
	f.module, f.h = module, module.Handler()
	cookie, csrf, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatalf("login admin fixture status=%d body=%s", login.Code, login.Body.String())
	}
	return f, cookie, csrf
}

func testMFAAudit(ctx context.Context, tx pgx.Tx, accountID, sessionID, action, method string, factorVersion int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO admin.audit_events(actor_account_id,actor_session_id,target_account_id,target_id,action,after_state,metadata)
		VALUES($1,$2,$1,$1,$3,jsonb_build_object('factorVersion',$4::bigint),jsonb_build_object('method',$5::text))`, accountID, sessionID, action, factorVersion, method)
	return err
}

func testTOTPAtCounter(secret []byte, counter int64) string {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(counter))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(encoded[:])
	digest := mac.Sum(nil)
	offset := int(digest[len(digest)-1] & 0x0f)
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

func TestMFAEnrollmentRotatesSessionAndTOTPIsSingleUseUnderConcurrency(t *testing.T) {
	f, cookie, csrf := mfaAdminFixture(t, "mfa-admin@example.test")
	wrongPassword := f.request(t, http.MethodPost, "/api/v1/auth/mfa/enrollment", map[string]string{"password": "wrong password"}, cookie, csrf, integrationOrigin, "")
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("enrollment with wrong password status=%d body=%s", wrongPassword.Code, wrongPassword.Body.String())
	}
	start := f.request(t, http.MethodPost, "/api/v1/auth/mfa/enrollment", map[string]string{"password": integrationPass}, cookie, csrf, integrationOrigin, "")
	if start.Code != http.StatusOK {
		t.Fatalf("start enrollment status=%d body=%s", start.Code, start.Body.String())
	}
	var enrollment struct {
		EnrollmentID string    `json:"enrollmentId"`
		Secret       string    `json:"secret"`
		OTPAuthURI   string    `json:"otpauthUri"`
		ExpiresAt    time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	if enrollment.EnrollmentID == "" || enrollment.Secret == "" || enrollment.OTPAuthURI == "" || !enrollment.ExpiresAt.After(time.Now()) {
		t.Fatalf("invalid enrollment response: %+v", enrollment)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil || len(secret) != 20 {
		t.Fatalf("decode enrollment secret size=%d err=%v", len(secret), err)
	}
	counter := time.Now().Unix() / 30
	code := testTOTPAtCounter(secret, counter)
	wrongCode := "000000"
	if wrongCode == code {
		wrongCode = "000001"
	}
	confirmation := f.request(t, http.MethodPost, "/api/v1/auth/mfa/enrollment/confirm", map[string]string{
		"enrollmentId": enrollment.EnrollmentID, "code": wrongCode,
	}, cookie, csrf, integrationOrigin, "")
	if confirmation.Code != http.StatusUnauthorized {
		t.Fatalf("invalid enrollment code status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	confirmation = f.request(t, http.MethodPost, "/api/v1/auth/mfa/enrollment/confirm", map[string]string{
		"enrollmentId": enrollment.EnrollmentID, "code": code,
	}, cookie, csrf, integrationOrigin, "")
	if confirmation.Code != http.StatusOK {
		t.Fatalf("confirm enrollment status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	var confirmed struct {
		CSRFToken     string    `json:"csrfToken"`
		VerifiedUntil time.Time `json:"verifiedUntil"`
		StepUpUntil   time.Time `json:"stepUpUntil"`
		RecoveryCodes []string  `json:"recoveryCodes"`
	}
	if err := json.Unmarshal(confirmation.Body.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}
	if len(confirmed.RecoveryCodes) != 10 || confirmed.CSRFToken == "" {
		t.Fatalf("confirmation did not return recovery credentials and CSRF token: %+v", confirmed)
	}
	if remaining := time.Until(confirmed.VerifiedUntil); remaining < 11*time.Hour || remaining > 12*time.Hour+time.Minute {
		t.Fatalf("MFA session verification TTL=%v, want about 12 hours", remaining)
	}
	if remaining := time.Until(confirmed.StepUpUntil); remaining < 4*time.Minute || remaining > 5*time.Minute+time.Minute {
		t.Fatalf("MFA step-up TTL=%v, want about 5 minutes", remaining)
	}
	var rotated *http.Cookie
	for _, candidate := range confirmation.Result().Cookies() {
		if candidate.Name == "hc_session" {
			rotated = candidate
		}
	}
	if rotated == nil || rotated.Value == cookie.Value {
		t.Fatal("enrollment confirmation did not rotate the session token")
	}
	if old := f.request(t, http.MethodGet, "/api/v1/auth/mfa", nil, cookie, "", "", ""); old.Code != http.StatusUnauthorized {
		t.Fatalf("pre-confirmation session remains valid: status=%d body=%s", old.Code, old.Body.String())
	}
	status := f.request(t, http.MethodGet, "/api/v1/auth/mfa", nil, rotated, "", "", "")
	if status.Code != http.StatusOK {
		t.Fatalf("new MFA session status=%d body=%s", status.Code, status.Body.String())
	}
	var auditCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin.audit_events WHERE action IN ('mfa.enrollment_started','mfa.enrolled')`).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("MFA enrollment audit count=%d err=%v, want start+confirmation", auditCount, err)
	}
	var auditData string
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(string_agg(after_state::text || metadata::text, ''),'') FROM admin.audit_events WHERE action LIKE 'mfa.%'`).Scan(&auditData); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditData, enrollment.Secret) || strings.Contains(auditData, code) || strings.Contains(auditData, "recoveryCodes") {
		t.Fatalf("MFA audit leaked secret/code material: %s", auditData)
	}

	// Two separately authenticated sessions racing with one fresh counter must
	// yield one rotated session and one rejected replay.
	firstCookie, firstCSRF, firstLogin := f.login(t, "mfa-admin@example.test", integrationPass)
	secondCookie, secondCSRF, secondLogin := f.login(t, "mfa-admin@example.test", integrationPass)
	if firstLogin.Code != http.StatusOK || secondLogin.Code != http.StatusOK {
		t.Fatalf("fresh logins failed: %d / %d", firstLogin.Code, secondLogin.Code)
	}
	futureCounter := time.Now().Unix()/30 + 1
	futureCode := testTOTPAtCounter(secret, futureCounter)
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, credentials := range []struct {
		cookie *http.Cookie
		csrf   string
	}{{firstCookie, firstCSRF}, {secondCookie, secondCSRF}} {
		go func(c *http.Cookie, token string) {
			responses <- f.request(t, http.MethodPost, "/api/v1/auth/mfa/verify", map[string]string{
				"password": integrationPass, "code": futureCode,
			}, c, token, integrationOrigin, "")
		}(credentials.cookie, credentials.csrf)
	}
	first, second := <-responses, <-responses
	if !((first.Code == http.StatusOK && second.Code == http.StatusUnauthorized) ||
		(first.Code == http.StatusUnauthorized && second.Code == http.StatusOK)) {
		t.Fatalf("concurrent TOTP replay statuses=%d/%d, want exactly one 200 and one 401", first.Code, second.Code)
	}
}

func TestMFARecoveryCodeIsSingleUseAndAdminWritesRequireFreshStepUp(t *testing.T) {
	f, cookie, csrf := mfaAdminFixture(t, "mfa-recovery-admin@example.test")
	start := f.request(t, http.MethodPost, "/api/v1/auth/mfa/enrollment", map[string]string{"password": integrationPass}, cookie, csrf, integrationOrigin, "")
	if start.Code != http.StatusOK {
		t.Fatalf("start enrollment status=%d body=%s", start.Code, start.Body.String())
	}
	var enrollment struct {
		EnrollmentID string `json:"enrollmentId"`
		Secret       string `json:"secret"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	confirm := f.request(t, http.MethodPost, "/api/v1/auth/mfa/enrollment/confirm", map[string]string{
		"enrollmentId": enrollment.EnrollmentID, "code": testTOTPAtCounter(secret, time.Now().Unix()/30),
	}, cookie, csrf, integrationOrigin, "")
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm enrollment status=%d body=%s", confirm.Code, confirm.Body.String())
	}
	var body struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	if err := json.Unmarshal(confirm.Body.Bytes(), &body); err != nil || len(body.RecoveryCodes) != 10 {
		t.Fatalf("decode recovery codes len=%d err=%v", len(body.RecoveryCodes), err)
	}
	var enrolledCookie *http.Cookie
	for _, candidate := range confirm.Result().Cookies() {
		if candidate.Name == "hc_session" {
			enrolledCookie = candidate
		}
	}
	if enrolledCookie == nil {
		t.Fatal("MFA enrollment did not rotate session")
	}
	// Login creates a fresh, unverified session. Recovery consumes exactly one
	// stored code and rotates/revokes sessions in the same transaction.
	recoveryCookie, recoveryCSRF, login := f.login(t, "mfa-recovery-admin@example.test", integrationPass)
	if login.Code != http.StatusOK {
		t.Fatalf("login before recovery status=%d", login.Code)
	}
	recovered := f.request(t, http.MethodPost, "/api/v1/auth/mfa/recovery", map[string]string{
		"password": integrationPass, "recoveryCode": body.RecoveryCodes[0],
	}, recoveryCookie, recoveryCSRF, integrationOrigin, "")
	if recovered.Code != http.StatusOK {
		t.Fatalf("first recovery status=%d body=%s", recovered.Code, recovered.Body.String())
	}
	var recoverySession *http.Cookie
	for _, candidate := range recovered.Result().Cookies() {
		if candidate.Name == "hc_session" {
			recoverySession = candidate
		}
	}
	if recoverySession == nil || recoverySession.Value == recoveryCookie.Value {
		t.Fatal("MFA recovery did not rotate the session token")
	}
	replayCookie, replayCSRF, replayLogin := f.login(t, "mfa-recovery-admin@example.test", integrationPass)
	if replayLogin.Code != http.StatusOK {
		t.Fatalf("login before recovery replay status=%d", replayLogin.Code)
	}
	replayed := f.request(t, http.MethodPost, "/api/v1/auth/mfa/recovery", map[string]string{
		"password": integrationPass, "recoveryCode": body.RecoveryCodes[0],
	}, replayCookie, replayCSRF, integrationOrigin, "")
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("reused recovery code status=%d body=%s, want 401", replayed.Code, replayed.Body.String())
	}
}
