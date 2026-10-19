package notifications

import (
	"context"
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
)

type proofFixture struct {
	challenge authcontracts.EmailCodeChallenge
	proof     authcontracts.NotificationEmailProof
	cancelled int
}

func (*proofFixture) PreflightNotificationEmailStart(context.Context, authcontracts.NotificationEmailInput, string) error {
	return nil
}

func (*proofFixture) PreflightNotificationEmailResend(context.Context, string, string, string) error {
	return nil
}

func (*proofFixture) PreflightNotificationEmailConfirm(context.Context, string, string, string, string) error {
	return nil
}

func (f *proofFixture) StartNotificationEmailTx(_ context.Context, _ pgx.Tx, in authcontracts.NotificationEmailInput, _ string) (authcontracts.EmailCodeChallenge, error) {
	f.proof = authcontracts.NotificationEmailProof{Email: in.Email, DestinationID: in.DestinationID, Version: in.Version}
	f.challenge = authcontracts.EmailCodeChallenge{ID: "10000000-0000-4000-8000-000000000001", Email: in.Email, ExpiresAt: time.Now().Add(10 * time.Minute), ResendAvailableAt: time.Now().Add(time.Minute), ServerNow: time.Now()}
	return f.challenge, nil
}

func (f *proofFixture) CancelNotificationEmailTx(context.Context, pgx.Tx, string) error {
	f.cancelled++
	return nil
}

func (f *proofFixture) CurrentNotificationEmail(context.Context, string) (authcontracts.EmailCodeChallenge, error) {
	if f.challenge.ID == "" {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeInvalid
	}
	return f.challenge, nil
}

func (f *proofFixture) ResendNotificationEmailTx(context.Context, pgx.Tx, string, string, string) (authcontracts.EmailCodeChallenge, error) {
	return f.challenge, nil
}

func (f *proofFixture) ConfirmNotificationEmailTx(context.Context, pgx.Tx, string, string, string, string) (authcontracts.NotificationEmailProof, bool, error) {
	return f.proof, true, nil
}

func TestCodeProvedAlternateEmailRemainsEligibleAcrossAllFiveChecks(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, "code proof eligibility")
	const loginEmail = "login-address@example.test"
	const notificationEmail = "alternate-alerts@example.test"
	if _, err := f.pool.Exec(ctx, `INSERT INTO auth.credentials(account_id,email,password_hash,email_verified_at) VALUES($1,$2,'test-only',now())`, accountID, loginEmail); err != nil {
		t.Fatalf("insert verified login email: %v", err)
	}
	if err := billing.New(f.pool, true).GrantLocal(ctx, accountID, 5, time.Now().Add(time.Hour), "code proof integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	m, err := NewMessenger(f.pool, integrationWebhookKey, MessengerConfig{
		EmailEnabled:       true,
		EmailAddressReader: auth.EmailForNotificationsTx,
	}, false, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create code-proof messenger: %v", err)
	}
	proofs := &proofFixture{}
	m.SetEmailCodes(proofs)
	_, challenge, err := m.ConnectEmailAddress(ctx, accountID, notificationEmail, nil, 0, "192.0.2.34", false)
	if err != nil || challenge == nil || challenge.Email != notificationEmail {
		t.Fatalf("start alternate email proof: challenge=%+v err=%v", challenge, err)
	}
	var beforeCredentialEmail string
	var beforeCredentialVersion int64
	if err := f.pool.QueryRow(ctx, `SELECT email,version FROM auth.credentials WHERE account_id=$1`, accountID).Scan(&beforeCredentialEmail, &beforeCredentialVersion); err != nil {
		t.Fatal(err)
	}
	destination, err := m.ConfirmEmail(ctx, accountID, challenge.ID, "123456", "192.0.2.34")
	if err != nil || !destination.Ready || destination.Email != notificationEmail {
		t.Fatalf("confirm alternate email: destination=%+v err=%v", destination, err)
	}
	var proofKind string
	var verifiedAt *time.Time
	if err := f.pool.QueryRow(ctx, `SELECT email_proof_kind,email_verified_at FROM notifications.messenger_destinations WHERE id=$1`, destination.ID).Scan(&proofKind, &verifiedAt); err != nil {
		t.Fatal(err)
	}
	if proofKind != "code" || verifiedAt == nil {
		t.Fatalf("destination proof state kind=%q verifiedAt=%v", proofKind, verifiedAt)
	}
	var afterCredentialEmail string
	var afterCredentialVersion int64
	if err := f.pool.QueryRow(ctx, `SELECT email,version FROM auth.credentials WHERE account_id=$1`, accountID).Scan(&afterCredentialEmail, &afterCredentialVersion); err != nil {
		t.Fatal(err)
	}
	if afterCredentialEmail != beforeCredentialEmail || afterCredentialVersion != beforeCredentialVersion || afterCredentialEmail != loginEmail {
		t.Fatalf("channel proof changed login credentials: before=%q/%d after=%q/%d", beforeCredentialEmail, beforeCredentialVersion, afterCredentialEmail, afterCredentialVersion)
	}
	if proofs.cancelled == 0 {
		t.Fatal("successful proof did not consume/cancel the pending proof")
	}

	// 1. Destination listing trusts the independent code proof even though the
	// current verified login address differs from the alert destination.
	destinations, err := m.ListDestinations(ctx, accountID)
	if err != nil || len(destinations) != 1 || !destinations[0].Ready || destinations[0].Email != notificationEmail {
		t.Fatalf("list alternate destination=%+v err=%v", destinations, err)
	}

	// 2. The ordinary route mutation accepts this ready proof and still leaves
	// monitor opt-in as an explicit separate operation.
	if _, err = m.PutRoute(ctx, accountID, monitorID, "email", PutMessengerRoute{DestinationID: destination.ID, Enabled: true}); err != nil {
		t.Fatalf("put code-proved route: %v", err)
	}
	var routeEnabled bool
	if err = f.pool.QueryRow(ctx, `SELECT enabled FROM notifications.messenger_routes WHERE monitor_id=$1 AND provider='email'`, monitorID).Scan(&routeEnabled); err != nil || !routeEnabled {
		t.Fatalf("code-proved route enabled=%t err=%v", routeEnabled, err)
	}

	// 3. The incident planner's transaction sees the email channel as eligible.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	channels, err := m.EnabledChannelsTx(ctx, tx, accountID, []string{monitorID})
	_ = tx.Rollback(ctx)
	if err != nil || len(channels[monitorID]) != 1 || channels[monitorID][0] != "email" {
		t.Fatalf("enabled channel result=%v err=%v", channels, err)
	}

	// 4. A queued incident can be claimed with the code-proved destination.
	recorder := incidents.New(f.pool, NewRecorder(m))
	at := time.Now().UTC().Add(-time.Minute)
	observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, CheckedAt: at, Sequence: 1}
	applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, 1, "DOWN", at, observation)
	if _, err := f.pool.Exec(ctx, `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	attempt, err := m.ClaimDelivery(ctx)
	if err != nil || attempt == nil || attempt.Target.Email != notificationEmail {
		t.Fatalf("claim code-proved delivery: attempt=%+v err=%v", attempt, err)
	}

	// 5. Recheck immediately before dispatch, after the worker has a lease.
	if _, err := f.pool.Exec(ctx, `UPDATE auth.credentials SET email_verified_at=NULL WHERE account_id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	stillEligible, err := m.deliveryStillEligible(ctx, *attempt)
	if err != nil || !stillEligible {
		t.Fatalf("independent code proof failed final delivery check: eligible=%t err=%v", stillEligible, err)
	}
}

func TestCodeProofConstraintRequiresVerificationTimestamp(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(t)
	accountID, _ := f.addMonitor(t, "proof constraint")
	_, err := f.pool.Exec(ctx, `INSERT INTO notifications.messenger_destinations(id,account_id,provider,kind,recipient_fingerprint,user_fingerprint,encrypted_target,email_proof_kind) VALUES('10000000-0000-4000-8000-000000000002',$1,'email','email',decode(repeat('01',32),'hex'),decode(repeat('01',32),'hex'),decode('01','hex'),'code')`, accountID)
	if err == nil {
		t.Fatal("migration accepted a code proof without its verification timestamp")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		// The expected constraint error is driver-native and is verified by its
		// server SQLSTATE to avoid depending on the exact error wording.
		var pgErr interface{ SQLState() string }
		if !errors.As(err, &pgErr) || pgErr.SQLState() != "23514" {
			t.Fatalf("invalid code-proof insert error=%v, want CHECK violation", err)
		}
	}
}

var _ = accounts.New

func TestDisconnectSerializesWithPendingEmailCodeResend(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(t)
	accountID, _ := f.addMonitor(t, "delete resend race")
	authModule, err := auth.New(f.pool, accounts.New(f.pool), messengerTestCaptcha{}, messengerTestMailer{}, auth.Config{
		Origin: messengerTestOrigin, MailEncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	})
	if err != nil {
		t.Fatalf("create auth module with synthetic mail key: %v", err)
	}
	actor := newMessengerTestActor(t, f, authModule, accountID)
	if _, err := f.pool.Exec(ctx, `UPDATE auth.credentials SET email_verified_at=now() WHERE account_id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	m, err := NewMessenger(f.pool, integrationWebhookKey, MessengerConfig{EmailEnabled: true, EmailAddressReader: auth.EmailForNotificationsTx}, false, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create messenger: %v", err)
	}
	m.SetEmailCodes(authModule)
	destination, err := m.ConnectEmail(ctx, accountID)
	if err != nil {
		t.Fatalf("create current verified destination: %v", err)
	}
	_, challenge, err := m.ConnectEmailAddress(ctx, accountID, "race-alternate@example.test", &destination.ID, destination.Version, "192.0.2.45", false)
	if err != nil || challenge == nil {
		t.Fatalf("start alternate-address proof: challenge=%+v err=%v", challenge, err)
	}
	mux := http.NewServeMux()
	m.RegisterAPI(mux, authModule.Protect)

	// Hold the account guard, then make both the resend endpoint and deletion
	// wait on it. Releasing it exercises either lock ordering while requiring
	// the completed deletion to revoke whichever proof/job version was current.
	guard, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = guard.Exec(ctx, `SELECT id FROM accounts.accounts WHERE id=$1 FOR NO KEY UPDATE`, accountID); err != nil {
		_ = guard.Rollback(ctx)
		t.Fatal(err)
	}
	type operationResult struct {
		resendStatus  int
		disconnectErr error
	}
	results := make(chan operationResult, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messenger-destinations/email/verification/"+challenge.ID+"/resend", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", messengerTestOrigin)
		req.Header.Set("X-CSRF-Token", actor.csrf)
		req.AddCookie(actor.cookie)
		req.RemoteAddr = "192.0.2.45:43125"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		results <- operationResult{resendStatus: rec.Code}
	}()
	deleted := make(chan error, 1)
	go func() { deleted <- m.Disconnect(context.Background(), accountID, destination.ID) }()
	deadline := time.Now().Add(3 * time.Second)
	var bothBlocked bool
	for time.Now().Before(deadline) {
		var blocked int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%accounts.accounts%'`).Scan(&blocked); err != nil {
			_ = guard.Rollback(ctx)
			t.Fatal(err)
		}
		if blocked >= 2 {
			bothBlocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bothBlocked {
		_ = guard.Rollback(ctx)
		t.Fatal("resend and disconnect did not both wait on the shared account lock")
	}
	if err := guard.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var result operationResult
	select {
	case result = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("resend did not finish after releasing the account lock")
	}
	select {
	case result.disconnectErr = <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not finish after releasing the account lock")
	}
	if result.disconnectErr != nil {
		t.Fatalf("disconnect destination: %v", result.disconnectErr)
	}
	if result.resendStatus != http.StatusAccepted && result.resendStatus != http.StatusBadRequest && result.resendStatus != http.StatusNotFound {
		t.Fatalf("resend after delete race status=%d, want serialized success or stale-proof rejection", result.resendStatus)
	}
	current, err := authModule.CurrentNotificationEmail(ctx, accountID)
	if !errors.Is(err, authcontracts.ErrEmailCodeInvalid) {
		t.Fatalf("pending proof survived destination deletion: challenge=%+v err=%v", current, err)
	}
	var activeJobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM auth.mail_jobs WHERE purpose='email_code' AND status IN ('pending','sending') AND token_hash IN (SELECT code_hash FROM auth.email_code_challenges WHERE account_id=$1 AND purpose='notification_email')`, accountID).Scan(&activeJobs); err != nil || activeJobs != 0 {
		t.Fatalf("email-code mail remains sendable after destination deletion: jobs=%d err=%v", activeJobs, err)
	}
}

func TestNotificationEmailStartResendAndConfirmUseOneConnectionPool(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixtureWithPoolSize(t, 1)
	accountID, _ := f.addMonitor(t, "single connection email proof")
	authModule, err := auth.New(f.pool, accounts.New(f.pool), messengerTestCaptcha{}, messengerTestMailer{}, auth.Config{
		Origin: messengerTestOrigin, MailEncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	})
	if err != nil {
		t.Fatalf("create auth module: %v", err)
	}
	actor := newMessengerTestActor(t, f, authModule, accountID)
	if _, err := f.pool.Exec(ctx, `UPDATE auth.credentials SET email_verified_at=now() WHERE account_id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	m, err := NewMessenger(f.pool, integrationWebhookKey, MessengerConfig{EmailEnabled: true, EmailAddressReader: auth.EmailForNotificationsTx}, false, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create messenger: %v", err)
	}
	m.SetEmailCodes(authModule)
	mux := http.NewServeMux()
	m.RegisterAPI(mux, authModule.Protect)
	currentEmail := "alternate-single-connection@example.test"
	_, challenge, err := m.ConnectEmailAddress(ctx, accountID, currentEmail, nil, 0, "192.0.2.46", false)
	if err != nil || challenge == nil {
		t.Fatalf("start notification email proof: challenge=%+v err=%v", challenge, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE auth.email_code_challenges SET resend_available_at=now()-interval '1 second' WHERE id=$1`, challenge.ID); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(reqCtx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", messengerTestOrigin)
		req.Header.Set("X-CSRF-Token", actor.csrf)
		req.AddCookie(actor.cookie)
		req.RemoteAddr = "192.0.2.46:43125"
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, req)
		return recorder
	}
	resend := request(http.MethodPost, "/api/v1/messenger-destinations/email/verification/"+challenge.ID+"/resend", `{}`)
	if resend.Code != http.StatusAccepted {
		t.Fatalf("single-connection notification resend status=%d body=%s", resend.Code, resend.Body.String())
	}
	confirm := request(http.MethodPost, "/api/v1/messenger-destinations/email/verification/"+challenge.ID+"/confirm", `{"code":"123456"}`)
	if confirm.Code != http.StatusBadRequest {
		t.Fatalf("single-connection wrong-code confirm status=%d body=%s", confirm.Code, confirm.Body.String())
	}
	var attempts int
	if err := f.pool.QueryRow(ctx, `SELECT failed_attempts FROM auth.email_code_challenges WHERE id=$1`, challenge.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("single-connection confirmation did not commit failed attempt: attempts=%d err=%v", attempts, err)
	}
}
