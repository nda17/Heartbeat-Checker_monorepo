package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	incidentcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	webhookcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/migrations"
)

const integrationWebhookKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

type webhookFixture struct {
	pool    *pgxpool.Pool
	module  *Module
	handler http.Handler
}

func newWebhookFixture(t *testing.T) *webhookFixture {
	return newWebhookFixtureWithPoolSize(t, 12)
}

func newWebhookFixtureWithPoolSize(t *testing.T, maxConns int32) *webhookFixture {
	t.Helper()
	baseURL := os.Getenv("HEARTBEAT_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set HEARTBEAT_TEST_DATABASE_URL to the isolated local PostgreSQL server URL")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host != "127.0.0.1:55432" {
		t.Fatal("HEARTBEAT_TEST_DATABASE_URL must target local Compose PostgreSQL at 127.0.0.1:55432")
	}
	admin, err := pgx.Connect(context.Background(), baseURL)
	if err != nil {
		t.Fatal("connect to local test PostgreSQL")
	}
	dbName := fmt.Sprintf("heartbeat_webhook_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+dbName); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("create isolated webhook test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()", dbName); err != nil {
			t.Errorf("terminate isolated webhook test connections: %v", err)
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName); err != nil {
			t.Errorf("drop isolated webhook test database: %v", err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})
	u.Path = "/" + dbName
	poolConfig, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("parse isolated webhook test database URL: %v", err)
	}
	poolConfig.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal("connect to isolated webhook test database")
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(context.Background(), pool); err != nil {
		t.Fatalf("apply isolated webhook test migrations: %v", err)
	}
	module, err := New(pool, integrationWebhookKey, monitors.NewOwnership(), accounts.New(pool))
	if err != nil {
		t.Fatalf("create notifications module: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, func(next http.Handler) http.Handler { return next })
	return &webhookFixture{pool: pool, module: module, handler: mux}
}

func (f *webhookFixture) addMonitor(t *testing.T, label string) (string, string) {
	t.Helper()
	ctx := context.Background()
	accountID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,$2)`, accountID, "Webhook "+label)
	if err != nil {
		t.Fatalf("create test account: %v", err)
	}
	_, err = f.pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status) VALUES($1,$2,$3,'https://status.example.test/health','status.example.test:443','GET',200)`, monitorID, accountID, "Monitor "+label)
	if err != nil {
		t.Fatalf("create test monitor: %v", err)
	}
	return accountID, monitorID
}

func (f *webhookFixture) request(t *testing.T, accountID, method, monitorID string, body any, authenticate bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	return f.requestRaw(t, accountID, method, monitorID, raw, contentType, authenticate)
}

func (f *webhookFixture) requestRaw(t *testing.T, accountID, method, monitorID string, body []byte, contentType string, authenticate bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/monitors/"+monitorID+"/webhook", bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authenticate {
		principal := authcontracts.Principal{AccountID: accountID, SessionID: "test-session", Role: "user"}
		req = req.WithContext(authcontracts.WithPrincipal(req.Context(), principal))
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

func (f *webhookFixture) put(t *testing.T, accountID, monitorID string, input webhookcontracts.PutWebhook) (int, string) {
	t.Helper()
	recorder := f.request(t, accountID, http.MethodPut, monitorID, input, true)
	var response struct {
		Webhook webhookcontracts.Webhook `json:"webhook"`
		Secret  string                   `json:"secret"`
		Error   string                   `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode webhook response: %v (%s)", err, recorder.Body.String())
	}
	return recorder.Code, response.Secret
}

func enqueueNotification(t *testing.T, f *webhookFixture, accountID, monitorID string, sequence int64) string {
	t.Helper()
	eventID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = NewRecorder().Enqueue(context.Background(), tx, incidentcontracts.Notification{
		EventID: eventID, AccountID: accountID, MonitorID: monitorID, IncidentID: eventID,
		Kind: "incident_opened", Sequence: sequence, At: time.Now().UTC(),
	})
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("enqueue test notification: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit test notification: %v", err)
	}
	// The worker also checks due/deadline timestamps against its process clock;
	// make this test event safely overdue even if PostgreSQL runs slightly ahead.
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.deliveries SET next_attempt_at=now()-interval '1 second' WHERE event_id=$1`, eventID); err != nil {
		t.Fatalf("make test notification immediately due: %v", err)
	}
	return eventID
}

func TestWebhookAPIAuthOwnershipAndVersionedSecretLifecycle(t *testing.T) {
	f := newWebhookFixture(t)
	owner, monitorID := f.addMonitor(t, "owner")
	other, _ := f.addMonitor(t, "other")
	if got := f.request(t, owner, http.MethodGet, monitorID, nil, false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET status = %d, want 401", got)
	}
	if got := f.request(t, other, http.MethodGet, monitorID, nil, true).Code; got != http.StatusNotFound {
		t.Fatalf("foreign monitor GET status = %d, want 404", got)
	}
	code, secret := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/first", Enabled: true})
	if code != http.StatusOK || secret == "" {
		t.Fatalf("first PUT = (status %d, secret present %t), want 200 and generated secret", code, secret != "")
	}
	if strings := f.request(t, owner, http.MethodGet, monitorID, nil, true).Body.String(); containsString(strings, secret) {
		t.Fatal("GET disclosed webhook signing secret")
	}
	if code, secret = f.put(t, owner, monitorID, webhookcontracts.PutWebhook{Version: 0, URL: "https://hooks.example.test/stale", Enabled: true}); code != http.StatusConflict || secret != "" {
		t.Fatalf("stale version PUT = (%d, secret present %t), want 409 without secret", code, secret != "")
	}
	if code, rotated := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{Version: 1, URL: "https://hooks.example.test/first", Enabled: true, RotateSecret: true}); code != http.StatusOK || rotated == "" || rotated == secret {
		t.Fatalf("secret rotation = (status %d, new secret present %t, changed %t)", code, rotated != "", rotated != secret)
	}
}

func TestWebhookAPIDeleteAndPutFenceEndpointIdentity(t *testing.T) {
	f := newWebhookFixture(t)
	owner, monitorID := f.addMonitor(t, "identity-guard")
	other, _ := f.addMonitor(t, "identity-guard-foreign")
	get := func() webhookcontracts.Webhook {
		t.Helper()
		recorder := f.request(t, owner, http.MethodGet, monitorID, nil, true)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET webhook status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Webhook webhookcontracts.Webhook `json:"webhook"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode webhook: %v (%s)", err, recorder.Body.String())
		}
		return response.Webhook
	}

	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/first", Enabled: true}); code != http.StatusOK {
		t.Fatalf("create webhook status = %d, want 200", code)
	}
	first := get()
	if first.ID == "" || first.Version != 1 {
		t.Fatalf("first webhook = (id %q, version %d), want a generated id at version 1", first.ID, first.Version)
	}

	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{ID: "10000000-0000-4000-8000-000000000001", Version: first.Version, URL: "https://hooks.example.test/stale", Enabled: true}); code != http.StatusConflict {
		t.Fatalf("PUT with stale endpoint id status = %d, want 409", code)
	}
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{Version: first.Version, URL: "https://hooks.example.test/legacy-update", Enabled: true}); code != http.StatusOK {
		t.Fatalf("legacy PUT without id status = %d, want 200", code)
	}
	updated := get()
	if updated.ID != first.ID || updated.Version != first.Version+1 || updated.URL != "https://hooks.example.test:443/legacy-update" {
		t.Fatalf("legacy PUT changed unexpected endpoint state: %+v", updated)
	}

	remove := func(accountID, id string, version int64) int {
		t.Helper()
		body, err := json.Marshal(webhookcontracts.DeleteWebhook{ID: id, Version: version})
		if err != nil {
			t.Fatal(err)
		}
		return f.requestRaw(t, accountID, http.MethodDelete, monitorID, body, "application/json", true).Code
	}
	if got := remove(other, first.ID, updated.Version); got != http.StatusNotFound {
		t.Fatalf("foreign DELETE status = %d, want 404", got)
	}
	if got := remove(owner, first.ID, first.Version); got != http.StatusConflict {
		t.Fatalf("stale-version DELETE status = %d, want 409", got)
	}
	if got := remove(owner, first.ID, updated.Version); got != http.StatusNoContent {
		t.Fatalf("DELETE current webhook status = %d, want 204", got)
	}
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/recreated", Enabled: true}); code != http.StatusOK {
		t.Fatalf("recreate webhook status = %d, want 200", code)
	}
	recreated := get()
	if recreated.ID == first.ID || recreated.Version != 1 {
		t.Fatalf("recreated webhook = (id %q, version %d), want a new id at version 1", recreated.ID, recreated.Version)
	}
	if got := remove(owner, first.ID, updated.Version); got != http.StatusConflict {
		t.Fatalf("stale DELETE after recreation status = %d, want 409", got)
	}
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{ID: first.ID, Version: recreated.Version, URL: "https://hooks.example.test/aba", Enabled: true}); code != http.StatusConflict {
		t.Fatalf("stale PUT after recreation status = %d, want 409", code)
	}
	if afterStale := get(); afterStale.ID != recreated.ID || afterStale.URL != recreated.URL || afterStale.Version != recreated.Version {
		t.Fatalf("stale requests changed recreated webhook: %+v", afterStale)
	}
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{Version: recreated.Version, URL: "https://hooks.example.test/legacy-after-recreate", Enabled: true}); code != http.StatusOK {
		t.Fatalf("legacy PUT after recreation status = %d, want 200", code)
	}
}

func TestWebhookUpdateCancelsPendingAndLeasedDeliveries(t *testing.T) {
	f := newWebhookFixture(t)
	owner, monitorID := f.addMonitor(t, "cancel")
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/first", Enabled: true}); code != http.StatusOK {
		t.Fatalf("create webhook status = %d", code)
	}
	firstEvent := enqueueNotification(t, f, owner, monitorID, 1)
	attempt, err := f.module.Claim(context.Background())
	if err != nil || attempt == nil || attempt.EventID != firstEvent {
		t.Fatalf("claim first notification = (%+v, %v); row: %s", attempt, err, deliveryDiagnostic(t, f, firstEvent))
	}
	secondEvent := enqueueNotification(t, f, owner, monitorID, 2)
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{Version: 1, URL: "https://hooks.example.test/second", Enabled: true}); code != http.StatusOK {
		t.Fatalf("update webhook status = %d", code)
	}
	var cancelled int
	err = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.deliveries WHERE event_id IN ($1,$2) AND status='cancelled' AND lease_until IS NULL AND attempt_token IS NULL`, firstEvent, secondEvent).Scan(&cancelled)
	if err != nil || cancelled != 2 {
		t.Fatalf("cancelled deliveries = %d, error %v; want pending and leased rows cancelled", cancelled, err)
	}
	if accepted, err := f.module.Complete(context.Background(), *attempt, webhookcontracts.Result{HTTPStatus: http.StatusOK}); err != nil || accepted {
		t.Fatalf("completion after endpoint update = (%t, %v), want rejected", accepted, err)
	}
}

func TestRecorderEnqueueIsAtomicOrderedAndIdempotent(t *testing.T) {
	f := newWebhookFixture(t)
	owner, monitorID := f.addMonitor(t, "recorder")
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/recorder", Enabled: true}); code != http.StatusOK {
		t.Fatalf("create webhook status = %d", code)
	}
	incidentModule := incidents.New(f.pool, NewRecorder())
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := incidentModule.Apply(ctx, tx, monitorID, owner, 1, "DOWN", 1, time.Now().UTC(), incidentcontracts.Observation{Outcome: "DOWN", CheckedAt: time.Now().UTC(), Sequence: 1}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply incident inside transaction: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.deliveries WHERE monitor_id=$1`, monitorID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deliveries after rollback = %d, error %v; want zero", count, err)
	}
	tx, err = f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := incidentModule.Apply(ctx, tx, monitorID, owner, 1, "DOWN", 1, time.Now().UTC(), incidentcontracts.Observation{Outcome: "DOWN", CheckedAt: time.Now().UTC(), Sequence: 1}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply committed incident: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := f.pool.QueryRow(ctx, `SELECT event_id::text FROM notifications.deliveries WHERE monitor_id=$1`, monitorID).Scan(&eventID); err != nil {
		t.Fatalf("read recorded delivery: %v", err)
	}
	// Replaying an already processed sequence must not create a duplicate request.
	tx, err = f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := incidentModule.Apply(ctx, tx, monitorID, owner, 1, "DOWN", 1, time.Now().UTC(), incidentcontracts.Observation{Outcome: "DOWN", CheckedAt: time.Now().UTC(), Sequence: 1}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("replay duplicate sequence: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM notifications.deliveries WHERE monitor_id=$1`, monitorID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("deliveries after duplicate sequence = %d, error %v; want exactly one", count, err)
	}
	var processed bool
	if err := f.pool.QueryRow(ctx, `SELECT processed_at IS NOT NULL FROM outbox.events WHERE id=$1`, eventID).Scan(&processed); err != nil || !processed {
		t.Fatalf("outbox processed = %t, error %v; want processed with delivery", processed, err)
	}
}

func TestClaimPreservesEndpointOrderRetriesAndRejectsStaleLease(t *testing.T) {
	f := newWebhookFixture(t)
	owner, monitorID := f.addMonitor(t, "queue")
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/queue", Enabled: true}); code != http.StatusOK {
		t.Fatalf("create webhook status = %d", code)
	}
	firstEvent := enqueueNotification(t, f, owner, monitorID, 1)
	// Duplicate event enqueue is idempotent at the unique (event, endpoint) boundary.
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := NewRecorder().Enqueue(context.Background(), tx, incidentcontracts.Notification{EventID: firstEvent, AccountID: owner, MonitorID: monitorID, IncidentID: firstEvent, Kind: "incident_opened", Sequence: 1, At: time.Now().UTC()}); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondEvent := enqueueNotification(t, f, owner, monitorID, 2)
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.deliveries WHERE monitor_id=$1`, monitorID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("deliveries after duplicate enqueue = %d, error %v; want two distinct events", count, err)
	}
	first, err := f.module.Claim(context.Background())
	if err != nil || first == nil || first.EventID != firstEvent {
		t.Fatalf("first claim = (%+v, %v), want earliest endpoint event", first, err)
	}
	if next, err := f.module.Claim(context.Background()); err != nil || next != nil {
		t.Fatalf("claim with earlier event leased = (%+v, %v), want no later event", next, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.deliveries SET lease_until=now()-interval '1 second' WHERE event_id=$1`, firstEvent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.reservations SET expires_at=now()-interval '1 second' WHERE attempt_token=$1`, first.Token); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := f.module.Claim(context.Background())
	if err != nil || reclaimed == nil || reclaimed.ID != first.ID || reclaimed.Token == first.Token {
		t.Fatalf("reclaimed lease = (%+v, %v), want same delivery with fresh token", reclaimed, err)
	}
	if accepted, err := f.module.Complete(context.Background(), *first, webhookcontracts.Result{HTTPStatus: http.StatusOK}); err != nil || accepted {
		t.Fatalf("stale completion = (%t, %v), want rejected", accepted, err)
	}
	if accepted, err := f.module.Complete(context.Background(), *reclaimed, webhookcontracts.Result{HTTPStatus: http.StatusOK}); err != nil || !accepted {
		t.Fatalf("current completion = (%t, %v), want accepted", accepted, err)
	}
	second, err := f.module.Claim(context.Background())
	if err != nil || second == nil || second.EventID != secondEvent {
		t.Fatalf("second endpoint claim = (%+v, %v), want next event after success", second, err)
	}
	if accepted, err := f.module.Complete(context.Background(), *second, webhookcontracts.Result{HTTPStatus: http.StatusTooManyRequests, ErrorCode: "http_status", RetryAfterSeconds: 30}); err != nil || !accepted {
		t.Fatalf("retryable completion = (%t, %v), want accepted", accepted, err)
	}
	var status string
	var attempts int
	var lastCode string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts,last_error_code FROM notifications.deliveries WHERE event_id=$1`, secondEvent).Scan(&status, &attempts, &lastCode); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || lastCode != "http_status" {
		t.Fatalf("retry state = %q attempts=%d error=%q", status, attempts, lastCode)
	}
}

func TestRetryAfterReachingDeliveryDeadlineMarksDeliveryFailed(t *testing.T) {
	f := newWebhookFixture(t)
	owner, monitorID := f.addMonitor(t, "retry-deadline")
	if code, _ := f.put(t, owner, monitorID, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/deadline", Enabled: true}); code != http.StatusOK {
		t.Fatalf("create webhook status = %d", code)
	}
	eventID := enqueueNotification(t, f, owner, monitorID, 1)
	attempt, err := f.module.Claim(context.Background())
	if err != nil || attempt == nil || attempt.EventID != eventID {
		t.Fatalf("claim delivery = (%+v, %v)", attempt, err)
	}
	if accepted, err := f.module.Complete(context.Background(), *attempt, webhookcontracts.Result{HTTPStatus: http.StatusTooManyRequests, ErrorCode: "http_status", RetryAfterSeconds: 86400}); err != nil || !accepted {
		t.Fatalf("complete retryable delivery = (%t, %v)", accepted, err)
	}
	var status, errorCode string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,last_error_code FROM notifications.deliveries WHERE event_id=$1`, eventID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errorCode != "deadline_exceeded" {
		t.Fatalf("delivery state after 24h Retry-After = %q/%q, want failed/deadline_exceeded", status, errorCode)
	}
}

func TestDeletingMonitorKeepsInflightReservationUntilProbeReturns(t *testing.T) {
	f := newWebhookFixture(t)
	ownerA, monitorA := f.addMonitor(t, "inflight-a")
	ownerB, monitorB := f.addMonitor(t, "inflight-b")
	sharedURL := "https://hooks.example.test/shared"
	if code, _ := f.put(t, ownerA, monitorA, webhookcontracts.PutWebhook{URL: sharedURL, Enabled: true}); code != http.StatusOK {
		t.Fatalf("create first webhook status = %d", code)
	}
	if code, _ := f.put(t, ownerB, monitorB, webhookcontracts.PutWebhook{URL: sharedURL, Enabled: true}); code != http.StatusOK {
		t.Fatalf("create second webhook status = %d", code)
	}
	eventA := enqueueNotification(t, f, ownerA, monitorA, 1)
	attemptA, err := f.module.Claim(context.Background())
	if err != nil || attemptA == nil || attemptA.EventID != eventA {
		t.Fatalf("claim first account event = (%+v, %v)", attemptA, err)
	}
	eventB := enqueueNotification(t, f, ownerB, monitorB, 1)
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM monitors.monitors WHERE id=$1`, monitorA); err != nil {
		t.Fatalf("delete first monitor during in-flight delivery: %v", err)
	}
	if next, err := f.module.Claim(context.Background()); err != nil || next != nil {
		t.Fatalf("claim while old HTTPS request is in flight = (%+v, %v), want host reserved", next, err)
	}
	if accepted, err := f.module.Complete(context.Background(), *attemptA, webhookcontracts.Result{HTTPStatus: http.StatusOK}); err != nil || accepted {
		t.Fatalf("completion after monitor deletion = (%t, %v), want stale attempt rejected", accepted, err)
	}
	next, err := f.module.Claim(context.Background())
	if err != nil || next == nil || next.EventID != eventB {
		t.Fatalf("claim after old request returns = (%+v, %v), want second account event %s", next, err, eventB)
	}
}

func TestClaimDoesNotLetBlockedEndpointPrefixStarveAnotherReadyEndpoint(t *testing.T) {
	f := newWebhookFixture(t)
	ownerA, monitorA := f.addMonitor(t, "blocked-prefix")
	ownerB, monitorB := f.addMonitor(t, "ready-head")
	if code, _ := f.put(t, ownerA, monitorA, webhookcontracts.PutWebhook{URL: "https://hooks.example.test/blocked", Enabled: true}); code != http.StatusOK {
		t.Fatalf("create first webhook status = %d", code)
	}
	if code, _ := f.put(t, ownerB, monitorB, webhookcontracts.PutWebhook{URL: "https://hooks.other.test/ready", Enabled: true}); code != http.StatusOK {
		t.Fatalf("create second webhook status = %d", code)
	}
	var endpointA, endpointB string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM notifications.webhooks WHERE monitor_id=$1`, monitorA).Scan(&endpointA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM notifications.webhooks WHERE monitor_id=$1`, monitorB).Scan(&endpointB); err != nil {
		t.Fatal(err)
	}
	firstEvent, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.deliveries(id,event_id,account_id,monitor_id,endpoint_id,endpoint_version,sequence,body,status,next_attempt_at,deadline_at) VALUES($1,$2,$3,$4,$5,1,1,'{}','pending',now()+interval '1 hour',now()+interval '24 hours')`, mustID(t), firstEvent, ownerA, monitorA, endpointA); err != nil {
		t.Fatal(err)
	}
	// More than the worker's current LIMIT 100 successors are due but blocked by
	// the future retry at sequence 1. Their old timestamps place them before B.
	for seq := int64(2); seq <= 102; seq++ {
		eventID, err := outbox.ID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.deliveries(id,event_id,account_id,monitor_id,endpoint_id,endpoint_version,sequence,body,status,next_attempt_at,deadline_at) VALUES($1,$2,$3,$4,$5,1,$6,'{}','pending',now()-interval '1 minute',now()+interval '24 hours')`, mustID(t), eventID, ownerA, monitorA, endpointA, seq); err != nil {
			t.Fatalf("insert blocked successor %d: %v", seq, err)
		}
	}
	readyEvent, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.deliveries(id,event_id,account_id,monitor_id,endpoint_id,endpoint_version,sequence,body,status,next_attempt_at,deadline_at) VALUES($1,$2,$3,$4,$5,1,1,'{}','pending',now()-interval '1 second',now()+interval '24 hours')`, mustID(t), readyEvent, ownerB, monitorB, endpointB); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.module.Claim(context.Background())
	if err != nil || claimed == nil || claimed.EventID != readyEvent {
		t.Fatalf("claim behind blocked prefix = (%+v, %v), want ready other endpoint event %s; ready row: %s", claimed, err, readyEvent, deliveryDiagnostic(t, f, readyEvent))
	}
}

func deliveryDiagnostic(t *testing.T, f *webhookFixture, eventID string) string {
	t.Helper()
	var status, accountStatus string
	var due, deadlineValid, endpointEnabled bool
	var attempts int
	var endpointVersion, currentVersion int64
	err := f.pool.QueryRow(context.Background(), `SELECT d.status,d.attempts,d.next_attempt_at<=now(),d.deadline_at>now(),a.status,w.enabled,d.endpoint_version,w.version
		FROM notifications.deliveries d JOIN accounts.accounts a ON a.id=d.account_id JOIN notifications.webhooks w ON w.id=d.endpoint_id WHERE d.event_id=$1`, eventID).
		Scan(&status, &attempts, &due, &deadlineValid, &accountStatus, &endpointEnabled, &endpointVersion, &currentVersion)
	if err != nil {
		return "query error: " + err.Error()
	}
	var eligible int
	err = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.deliveries d WHERE d.status IN ('pending','leased') AND (d.status='pending' AND d.next_attempt_at<=now() OR d.status='leased' AND d.lease_until<=now()) AND (d.next_attempt_at,d.id)>(to_timestamp(0),'00000000-0000-0000-0000-000000000000'::uuid) AND NOT EXISTS (SELECT 1 FROM notifications.deliveries prior WHERE prior.endpoint_id=d.endpoint_id AND prior.sequence<d.sequence AND prior.status IN ('pending','leased'))`).Scan(&eligible)
	if err != nil {
		return "" + fmt.Sprintf("status=%s eligible-query-error=%v", status, err)
	}
	var reservations int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.reservations WHERE expires_at>now()`).Scan(&reservations); err != nil {
		return fmt.Sprintf("status=%s reservation-query-error=%v", status, err)
	}
	var exactEligible int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.deliveries d WHERE d.status IN ('pending','leased') AND (d.status='pending' AND d.next_attempt_at<=now() OR d.status='leased' AND d.lease_until<=now()) AND (d.next_attempt_at,d.id)>( $1::timestamptz,$2::uuid) AND NOT EXISTS (SELECT 1 FROM notifications.deliveries prior WHERE prior.endpoint_id=d.endpoint_id AND prior.sequence<d.sequence AND prior.status IN ('pending','leased'))`, time.Unix(0, 0).UTC(), "00000000-0000-0000-0000-000000000000").Scan(&exactEligible); err != nil {
		return fmt.Sprintf("status=%s exact-query-error=%v", status, err)
	}
	var accountID, monitorID, endpointID string
	var locks string
	if err := f.pool.QueryRow(context.Background(), `SELECT d.account_id::text,d.monitor_id::text,d.endpoint_id::text FROM notifications.deliveries d WHERE d.event_id=$1`, eventID).Scan(&accountID, &monitorID, &endpointID); err == nil {
		tx, err := f.pool.Begin(context.Background())
		if err == nil {
			var account string
			accountErr := tx.QueryRow(context.Background(), `SELECT status FROM accounts.accounts WHERE id=$1 FOR SHARE SKIP LOCKED`, accountID).Scan(&account)
			var monitor string
			monitorErr := tx.QueryRow(context.Background(), `SELECT id::text FROM monitors.monitors WHERE id=$1 AND account_id=$2 FOR UPDATE SKIP LOCKED`, monitorID, accountID).Scan(&monitor)
			var endpoint string
			endpointErr := tx.QueryRow(context.Background(), `SELECT id::text FROM notifications.webhooks WHERE id=$1 AND account_id=$2 AND monitor_id=$3 FOR UPDATE SKIP LOCKED`, endpointID, accountID, monitorID).Scan(&endpoint)
			var delivery string
			deliveryErr := tx.QueryRow(context.Background(), `SELECT id::text FROM notifications.deliveries WHERE event_id=$1 FOR UPDATE SKIP LOCKED`, eventID).Scan(&delivery)
			_ = tx.Rollback(context.Background())
			locks = fmt.Sprintf(" locks account=%t monitor=%t endpoint=%t delivery=%t", accountErr == nil, monitorErr == nil, endpointErr == nil, deliveryErr == nil)
		}
	}
	return fmt.Sprintf("status=%s attempts=%d due=%t deadline_valid=%t account=%s enabled=%t versions=%d/%d eligible=%d exact_eligible=%d reservations=%d%s", status, attempts, due, deadlineValid, accountStatus, endpointEnabled, endpointVersion, currentVersion, eligible, exactEligible, reservations, locks)
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func containsString(haystack, needle string) bool {
	return needle != "" && bytes.Contains([]byte(haystack), []byte(needle))
}
