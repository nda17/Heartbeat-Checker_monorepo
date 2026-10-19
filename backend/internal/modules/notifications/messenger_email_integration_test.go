package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	incidentcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
)

type emailAddressFixture struct {
	mu        sync.Mutex
	addresses map[string]string
	verified  map[string]bool
}

func (fixture *emailAddressFixture) read(_ context.Context, _ pgx.Tx, accountID string) (string, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !fixture.verified[accountID] {
		return "", pgx.ErrNoRows
	}
	email := fixture.addresses[accountID]
	if email == "" {
		return "", pgx.ErrNoRows
	}
	return email, nil
}

type emailMessengerAPIFixture struct {
	webhookFixture *webhookFixture
	module         *Messenger
	handler        http.Handler
	actors         map[string]messengerTestActor
	monitors       map[string]string
	addresses      *emailAddressFixture
}

func newEmailMessengerAPIFixture(t *testing.T, accountNames ...string) *emailMessengerAPIFixture {
	t.Helper()
	f := newWebhookFixture(t)
	addresses := &emailAddressFixture{addresses: map[string]string{}, verified: map[string]bool{}}
	m, err := NewMessenger(f.pool, integrationWebhookKey, MessengerConfig{
		EmailEnabled:       true,
		EmailAddressReader: addresses.read,
	}, false, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create email-only messenger: %v", err)
	}
	authModule, err := auth.New(f.pool, accounts.New(f.pool), messengerTestCaptcha{}, messengerTestMailer{}, auth.Config{Origin: messengerTestOrigin})
	if err != nil {
		t.Fatalf("create auth module for email messenger API: %v", err)
	}
	mux := http.NewServeMux()
	m.RegisterAPI(mux, authModule.Protect)
	result := &emailMessengerAPIFixture{
		webhookFixture: f,
		module:         m,
		handler:        mux,
		actors:         map[string]messengerTestActor{},
		monitors:       map[string]string{},
		addresses:      addresses,
	}
	for _, name := range accountNames {
		accountID, monitorID := f.addMonitor(t, name)
		result.actors[name] = newMessengerTestActor(t, f, authModule, accountID)
		addresses.addresses[accountID] = "email-" + strings.ReplaceAll(accountID, "-", "") + "@example.test"
		addresses.verified[accountID] = true
		result.monitors[name] = monitorID
	}
	return result
}

func (f *emailMessengerAPIFixture) request(t *testing.T, actor *messengerTestActor, method, path string, body any, csrf, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if actor != nil {
		req.AddCookie(actor.cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func TestMessengerEmailConnectIsAccountScopedIdempotentAndOptIn(t *testing.T) {
	f := newEmailMessengerAPIFixture(t, "email owner", "email other")
	owner := f.actors["email owner"]
	other := f.actors["email other"]
	const path = "/api/v1/messenger-destinations/email"

	if got := f.request(t, nil, http.MethodPost, path, map[string]any{}, "", messengerTestOrigin); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated email connect status=%d, want 401", got.Code)
	}
	if got := f.request(t, &owner, http.MethodPost, path, map[string]any{}, "", messengerTestOrigin); got.Code != http.StatusForbidden {
		t.Fatalf("email connect without CSRF status=%d, want 403", got.Code)
	}
	if got := f.request(t, &owner, http.MethodPost, path, map[string]any{}, owner.csrf, "https://attacker.example"); got.Code != http.StatusForbidden {
		t.Fatalf("email connect with foreign Origin status=%d, want 403", got.Code)
	}

	first := f.request(t, &owner, http.MethodPost, path, map[string]any{}, owner.csrf, messengerTestOrigin)
	if first.Code != http.StatusOK {
		t.Fatalf("first email connect status=%d body=%s, want 200", first.Code, first.Body.String())
	}
	var firstResult struct {
		Destination MessengerDestination `json:"destination"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil || firstResult.Destination.ID == "" || firstResult.Destination.Provider != "email" || firstResult.Destination.Kind != "email" || firstResult.Destination.Email == "" || !firstResult.Destination.Ready {
		t.Fatalf("decode connected email destination: err=%v body=%s", err, first.Body.String())
	}
	if strings.Contains(first.Body.String(), "password") || strings.Contains(first.Body.String(), "token") {
		t.Fatalf("email destination response disclosed unrelated credential material: %s", first.Body.String())
	}
	second := f.request(t, &owner, http.MethodPost, path, map[string]any{}, owner.csrf, messengerTestOrigin)
	var secondResult struct {
		Destination MessengerDestination `json:"destination"`
	}
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &secondResult) != nil || secondResult.Destination.ID != firstResult.Destination.ID {
		t.Fatalf("repeated connect status=%d destination=%+v body=%s, want same destination %s", second.Code, secondResult.Destination, second.Body.String(), firstResult.Destination.ID)
	}

	listed := f.request(t, &owner, http.MethodGet, "/api/v1/messenger-destinations", nil, "", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"enabled":false`) || !strings.Contains(listed.Body.String(), `"emailEnabled":true`) || !strings.Contains(listed.Body.String(), firstResult.Destination.ID) {
		t.Fatalf("email-only destination list status=%d body=%s", listed.Code, listed.Body.String())
	}
	foreign := f.request(t, &other, http.MethodGet, "/api/v1/messenger-destinations", nil, "", "")
	if foreign.Code != http.StatusOK || strings.Contains(foreign.Body.String(), firstResult.Destination.ID) || strings.Contains(foreign.Body.String(), firstResult.Destination.Email) {
		t.Fatalf("another account can see this email destination: status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	var routes int
	if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_routes WHERE account_id=$1`, owner.accountID).Scan(&routes); err != nil || routes != 0 {
		t.Fatalf("connecting email created notification routes=%d err=%v, want none until monitor opt-in", routes, err)
	}

	f.addresses.mu.Lock()
	f.addresses.verified[owner.accountID] = false
	f.addresses.mu.Unlock()
	if got := f.request(t, &owner, http.MethodPost, path, map[string]any{}, owner.csrf, messengerTestOrigin); got.Code != http.StatusNotFound {
		t.Fatalf("connect after address becomes unverified status=%d body=%s, want 404", got.Code, got.Body.String())
	}
}

type capturedEmailDelivery struct {
	to, subject, messageID string
	body                   authcontracts.MailContent
}

type emailDeliverySender struct {
	mu       sync.Mutex
	messages []capturedEmailDelivery
	err      error
}

type smtpFailure struct {
	permanent bool
}

func (failure smtpFailure) Error() string { return "test SMTP failure" }

func (failure smtpFailure) Permanent() bool { return failure.permanent }

func (sender *emailDeliverySender) SendWithID(_ context.Context, to, subject string, body authcontracts.MailContent, messageID string) error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	sender.messages = append(sender.messages, capturedEmailDelivery{to: to, subject: subject, body: body, messageID: messageID})
	return sender.err
}

func (sender *emailDeliverySender) snapshot() []capturedEmailDelivery {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]capturedEmailDelivery(nil), sender.messages...)
}

func newEmailDeliveryFixture(t *testing.T, label string) (*webhookFixture, *Messenger, *emailDeliverySender, string, string, MessengerDestination) {
	t.Helper()
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, label)
	if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "email delivery integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	sender := &emailDeliverySender{}
	const address = "alerts-owner@example.test"
	m, err := NewMessenger(f.pool, integrationWebhookKey, MessengerConfig{
		EmailEnabled: true,
		EmailAddressReader: func(_ context.Context, _ pgx.Tx, requestedAccountID string) (string, error) {
			if requestedAccountID != accountID {
				return "", pgx.ErrNoRows
			}
			return address, nil
		},
		EmailSender: sender,
	}, false, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create email delivery messenger: %v", err)
	}
	destination, err := m.ConnectEmail(context.Background(), accountID)
	if err != nil {
		t.Fatalf("connect test email destination: %v", err)
	}
	return f, m, sender, accountID, monitorID, destination
}

func TestMessengerEmailDeliveryKeepsDownAndRecoveryOrderedWithStableMessageID(t *testing.T) {
	f, m, sender, accountID, monitorID, destination := newEmailDeliveryFixture(t, "email down and recovery")
	if _, err := m.PutRoute(context.Background(), accountID, monitorID, "email", PutMessengerRoute{DestinationID: destination.ID, Enabled: true}); err != nil {
		t.Fatalf("enable monitor email route: %v", err)
	}
	recorder := incidents.New(f.pool, NewRecorder(m))
	downAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	down := incidentcontracts.Observation{Name: "Checkout", URL: "https://shop.example.test/health", Outcome: "DOWN", HTTPStatus: 503, ErrorCode: "unexpected_status", LatencyMS: 237, CheckedAt: downAt, Sequence: 1}
	applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, 1, "DOWN", downAt, down)
	upAt := downAt.Add(30 * time.Second)
	up := incidentcontracts.Observation{Name: "Checkout", URL: "https://shop.example.test/health", Outcome: "UP", HTTPStatus: 200, LatencyMS: 41, CheckedAt: upAt, Sequence: 2}
	applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, 2, "UP", upAt, up)
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}

	downAttempt, err := m.ClaimDelivery(context.Background())
	if err != nil || downAttempt == nil || downAttempt.Kind != "down" {
		t.Fatalf("claim email DOWN: attempt=%+v err=%v", downAttempt, err)
	}
	for range 2 {
		if err := m.sendEmail(context.Background(), *downAttempt); err != nil {
			t.Fatalf("send fake email: %v", err)
		}
	}
	messages := sender.snapshot()
	if len(messages) != 2 || messages[0].messageID == "" || messages[0].messageID != messages[1].messageID || messages[0].messageID != "<"+downAttempt.ID+"@heartbeat-checker.ru>" {
		t.Fatalf("email retries used unstable Message-ID: %+v", messages)
	}
	if messages[0].to != destination.Email || !strings.Contains(messages[0].body.Text, "HTTP: 503") || !strings.Contains(messages[0].body.HTML, "shop.example.test") {
		t.Fatalf("email notification lacks recipient or accepted DOWN details: %+v", messages[0])
	}
	if next, err := m.ClaimDelivery(context.Background()); err != nil || next != nil {
		t.Fatalf("email recovery overtook in-flight DOWN: next=%+v err=%v", next, err)
	}
	if accepted, err := m.CompleteDelivery(context.Background(), *downAttempt, nil); err != nil || !accepted {
		t.Fatalf("complete email DOWN accepted=%t err=%v", accepted, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_target_rates SET next_send_at=now()-interval '1 second' WHERE provider='email'`); err != nil {
		t.Fatal(err)
	}
	upAttempt, err := m.ClaimDelivery(context.Background())
	if err != nil || upAttempt == nil || upAttempt.Kind != "up" || upAttempt.Snapshot.Sequence != 2 {
		t.Fatalf("claim email recovery after DOWN: attempt=%+v err=%v", upAttempt, err)
	}
	if _, err := m.CompleteDelivery(context.Background(), *upAttempt, nil); err != nil {
		t.Fatalf("complete email recovery: %v", err)
	}
}

func TestMessengerEmailRouteDisableAndDestinationDeleteFenceStaleLeases(t *testing.T) {
	t.Run("route disable", func(t *testing.T) {
		f, m, _, accountID, monitorID, destination := newEmailDeliveryFixture(t, "email route disable")
		route, err := m.PutRoute(context.Background(), accountID, monitorID, "email", PutMessengerRoute{DestinationID: destination.ID, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		recorder := incidents.New(f.pool, NewRecorder(m))
		at := time.Now().UTC().Add(-time.Minute)
		observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, CheckedAt: at, Sequence: 1}
		applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, 1, "DOWN", at, observation)
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
			t.Fatal(err)
		}
		attempt, err := m.ClaimDelivery(context.Background())
		if err != nil || attempt == nil {
			t.Fatalf("claim email notification before disable: attempt=%+v err=%v", attempt, err)
		}
		if _, err := m.PutRoute(context.Background(), accountID, monitorID, "email", PutMessengerRoute{DestinationID: destination.ID, Enabled: false, Version: route.Version}); err != nil {
			t.Fatalf("disable email route: %v", err)
		}
		if accepted, err := m.CompleteDelivery(context.Background(), *attempt, errors.New("stale SMTP completion")); err != nil || accepted {
			t.Fatalf("stale completion accepted=%t err=%v after route disable, want fenced", accepted, err)
		}
		var status string
		if err := f.pool.QueryRow(context.Background(), `SELECT status FROM notifications.messenger_deliveries WHERE id=$1`, attempt.ID).Scan(&status); err != nil || status != "cancelled" {
			t.Fatalf("delivery after route disable status=%q err=%v, want cancelled", status, err)
		}
	})

	t.Run("destination deletion", func(t *testing.T) {
		f, m, _, accountID, monitorID, destination := newEmailDeliveryFixture(t, "email destination delete")
		if _, err := m.PutRoute(context.Background(), accountID, monitorID, "email", PutMessengerRoute{DestinationID: destination.ID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		recorder := incidents.New(f.pool, NewRecorder(m))
		at := time.Now().UTC().Add(-time.Minute)
		observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, CheckedAt: at, Sequence: 1}
		applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, 1, "DOWN", at, observation)
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
			t.Fatal(err)
		}
		attempt, err := m.ClaimDelivery(context.Background())
		if err != nil || attempt == nil {
			t.Fatalf("claim email notification before destination deletion: attempt=%+v err=%v", attempt, err)
		}
		if err := m.Disconnect(context.Background(), accountID, destination.ID); err != nil {
			t.Fatalf("delete email destination: %v", err)
		}
		if accepted, err := m.CompleteDelivery(context.Background(), *attempt, nil); err != nil || accepted {
			t.Fatalf("stale completion accepted=%t err=%v after destination deletion, want fenced", accepted, err)
		}
		var status string
		if err := f.pool.QueryRow(context.Background(), `SELECT status FROM notifications.messenger_deliveries WHERE id=$1`, attempt.ID).Scan(&status); err != nil || status != "cancelled" {
			t.Fatalf("delivery after destination deletion status=%q err=%v, want cancelled", status, err)
		}
	})
}

func TestMessengerEmailDeliveryClassifiesPermanentAndRetryableFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		permanent bool
		want      string
	}{
		{name: "retryable SMTP failure", want: "pending"},
		{name: "permanent SMTP failure", permanent: true, want: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _, accountID, monitorID, destination := newEmailDeliveryFixture(t, "email error "+test.name)
			if _, err := m.PutRoute(context.Background(), accountID, monitorID, "email", PutMessengerRoute{DestinationID: destination.ID, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			recorder := incidents.New(f.pool, NewRecorder(m))
			at := time.Now().UTC().Add(-time.Minute)
			observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, CheckedAt: at, Sequence: 1}
			applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, 1, "DOWN", at, observation)
			if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
				t.Fatal(err)
			}
			attempt, err := m.ClaimDelivery(context.Background())
			if err != nil || attempt == nil {
				t.Fatalf("claim email notification: attempt=%+v err=%v", attempt, err)
			}
			if accepted, err := m.CompleteDelivery(context.Background(), *attempt, smtpFailure{permanent: test.permanent}); err != nil || !accepted {
				t.Fatalf("complete SMTP failure accepted=%t err=%v", accepted, err)
			}
			var status, errorCode string
			if err := f.pool.QueryRow(context.Background(), `SELECT status,error_code FROM notifications.messenger_deliveries WHERE id=$1`, attempt.ID).Scan(&status, &errorCode); err != nil {
				t.Fatal(err)
			}
			if status != test.want || errorCode == "" {
				t.Fatalf("SMTP failure state=(%q,%q), want status %q with error code", status, errorCode, test.want)
			}
		})
	}
}

func TestMessengerEmailDeliveryRechecksVerifiedAccountBeforeSend(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(context.Context, *webhookFixture, string) error
	}{
		{name: "verification revoked", mutate: func(ctx context.Context, f *webhookFixture, accountID string) error {
			_, err := f.pool.Exec(ctx, `UPDATE auth.credentials SET email_verified_at=NULL WHERE account_id=$1`, accountID)
			return err
		}},
		{name: "account blocked", mutate: func(ctx context.Context, f *webhookFixture, accountID string) error {
			_, err := f.pool.Exec(ctx, `UPDATE accounts.accounts SET status='blocked' WHERE id=$1`, accountID)
			return err
		}},
		{name: "email changed", mutate: func(ctx context.Context, f *webhookFixture, accountID string) error {
			_, err := f.pool.Exec(ctx, `UPDATE auth.credentials SET email='new-alerts-owner@example.test' WHERE account_id=$1`, accountID)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f := newWebhookFixture(t)
			accountID, monitorID := f.addMonitor(t, "email stale eligibility "+test.name)
			const address = "alerts-owner@example.test"
			if _, err := f.pool.Exec(ctx, `INSERT INTO auth.credentials(account_id,email,password_hash,email_verified_at) VALUES($1,$2,'test-only',now())`, accountID, address); err != nil {
				t.Fatalf("insert verified auth credential: %v", err)
			}
			if err := billing.New(f.pool, true).GrantLocal(ctx, accountID, 5, time.Now().Add(time.Hour), "email stale eligibility test"); err != nil {
				t.Fatalf("grant isolated test entitlement: %v", err)
			}
			sender := &emailDeliverySender{}
			m, err := NewMessenger(f.pool, integrationWebhookKey, MessengerConfig{
				EmailEnabled:       true,
				EmailAddressReader: auth.EmailForNotificationsTx,
				EmailSender:        sender,
			}, false, billing.New(f.pool, true))
			if err != nil {
				t.Fatalf("create email messenger with production auth reader: %v", err)
			}
			destination, err := m.ConnectEmail(ctx, accountID)
			if err != nil {
				t.Fatalf("connect verified test email: %v", err)
			}
			if _, err := m.PutRoute(ctx, accountID, monitorID, "email", PutMessengerRoute{DestinationID: destination.ID, Enabled: true}); err != nil {
				t.Fatalf("enable monitor email route: %v", err)
			}
			recorder := incidents.New(f.pool, NewRecorder(m))
			at := time.Now().UTC().Add(-time.Minute)
			observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, CheckedAt: at, Sequence: 1}
			applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, 1, "DOWN", at, observation)
			if _, err := f.pool.Exec(ctx, `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
				t.Fatal(err)
			}
			attempt, err := m.ClaimDelivery(ctx)
			if err != nil || attempt == nil {
				t.Fatalf("claim verified email delivery: attempt=%+v err=%v", attempt, err)
			}
			if err := test.mutate(ctx, f, accountID); err != nil {
				t.Fatalf("make email eligibility stale after claim: %v", err)
			}
			eligible, err := m.deliveryStillEligible(ctx, *attempt)
			if err != nil || eligible {
				t.Fatalf("stale email delivery eligible=%t err=%v, want false", eligible, err)
			}
			if messages := sender.snapshot(); len(messages) != 0 {
				t.Fatalf("stale email delivery sent despite eligibility change: %+v", messages)
			}
			if accepted, err := m.CompleteDelivery(ctx, *attempt, errMessengerCancelled); err != nil || !accepted {
				t.Fatalf("cancel stale email delivery accepted=%t err=%v", accepted, err)
			}
		})
	}
}
