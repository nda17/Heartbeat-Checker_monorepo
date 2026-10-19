package billing

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

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

const testProviderID = "payment1234567890123456"

type fakeYooKassa struct {
	mu           sync.Mutex
	createKeys   []string
	createBodies [][]byte
	getIDs       []string
	create       func(string, []byte) (contracts.ProviderPayment, error)
	get          func(string) (contracts.ProviderPayment, error)
}

func (p *fakeYooKassa) CreatePayment(_ context.Context, key string, body []byte) (contracts.ProviderPayment, error) {
	p.mu.Lock()
	p.createKeys = append(p.createKeys, key)
	p.createBodies = append(p.createBodies, append([]byte(nil), body...))
	fn := p.create
	p.mu.Unlock()
	if fn != nil {
		return fn(key, body)
	}
	var request struct {
		Amount struct {
			Value    string `json:"value"`
			Currency string `json:"currency"`
		} `json:"amount"`
		Metadata struct {
			OrderID string `json:"order_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return contracts.ProviderPayment{}, err
	}
	testMode := true
	return contracts.ProviderPayment{
		ID: testProviderID, Status: "pending", AmountValue: request.Amount.Value,
		Currency: request.Amount.Currency, OrderID: request.Metadata.OrderID,
		ShopID: "123456", ConfirmationURL: "https://yookassa.ru/checkout/" + testProviderID,
		Test: &testMode,
	}, nil
}

func (p *fakeYooKassa) GetPayment(_ context.Context, id string) (contracts.ProviderPayment, error) {
	p.mu.Lock()
	p.getIDs = append(p.getIDs, id)
	fn := p.get
	p.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	testMode := true
	return contracts.ProviderPayment{
		ID: id, Status: "pending", AmountValue: "29.00", Currency: "RUB",
		OrderID: "", ShopID: "123456", Test: &testMode,
	}, nil
}

func (p *fakeYooKassa) createCalls() (keys []string, bodies [][]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys = append([]string(nil), p.createKeys...)
	for _, body := range p.createBodies {
		bodies = append(bodies, append([]byte(nil), body...))
	}
	return keys, bodies
}

func (p *fakeYooKassa) getCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.getIDs...)
}

func (f *billingFixture) configureYooKassa(t *testing.T, owner billingActor, provider *fakeYooKassa) {
	t.Helper()
	module, err := NewConfigured(f.pool, false, Config{
		Provider: "yookassa", Mode: "test", ShopID: "123456",
		TestAccountIDs: []string{owner.id}, PaymentProvider: provider,
	}, f.accounts)
	if err != nil {
		t.Fatalf("configure test YooKassa provider: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module, f.handler = module, mux
}

func createYooKassaOrder(t *testing.T, f *billingFixture, owner billingActor, key string, count int) contracts.Order {
	t.Helper()
	rec := f.request(t, owner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": count, "idempotencyKey": key,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create YooKassa order status=%d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Order.ID == "" {
		t.Fatalf("decode pending payment order: %v; body=%s", err, rec.Body.String())
	}
	return result.Order
}

func runPaymentWorker(t *testing.T, f *billingFixture) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return f.module.ProcessOnePayment(ctx)
}

func TestYooKassaOrderIsDurableBeforeProviderAndUsesImmutableProviderKey(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "YooKassa checkout owner")
	provider := &fakeYooKassa{}
	f.configureYooKassa(t, owner, provider)

	key := "b9f02cf9-dbf8-4a6a-bff8-18a5d8974f82"
	order := createYooKassaOrder(t, f, owner, key, 2)
	if order.Status != "pending" || order.Provider != "yookassa" || order.ExpiresAt != nil || order.ReconciliationStatus != "pending" {
		t.Fatalf("new order must be durable, pending, and unexpired: %+v", order)
	}
	if keys, _ := provider.createCalls(); len(keys) != 0 {
		t.Fatalf("HTTP create called provider synchronously: keys=%v", keys)
	}
	retry := createYooKassaOrder(t, f, owner, key, 2)
	if retry.ID != order.ID {
		t.Fatalf("same idempotency key returned a different order: %s != %s", retry.ID, order.ID)
	}
	conflict := f.request(t, owner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": 3, "idempotencyKey": key,
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed same-key request status=%d, want 409: %s", conflict.Code, conflict.Body.String())
	}

	processed, err := runPaymentWorker(t, f)
	if err != nil || !processed {
		t.Fatalf("worker process=(%t,%v), want one durable payment attempt", processed, err)
	}
	keys, bodies := provider.createCalls()
	if len(keys) != 1 || keys[0] != order.ID || len(bodies) != 1 {
		t.Fatalf("provider create calls keys=%v bodies=%d, want order UUID as one fixed provider key", keys, len(bodies))
	}
	var request struct {
		Amount struct {
			Value    string `json:"value"`
			Currency string `json:"currency"`
		} `json:"amount"`
		Capture      bool `json:"capture"`
		Confirmation struct {
			ReturnURL string `json:"return_url"`
		} `json:"confirmation"`
		Metadata struct {
			OrderID string `json:"order_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(bodies[0], &request); err != nil {
		t.Fatal(err)
	}
	if request.Amount.Value != "58.00" || request.Amount.Currency != "RUB" || !request.Capture || request.Metadata.OrderID != order.ID || !strings.HasSuffix(request.Confirmation.ReturnURL, "?order="+order.ID) {
		t.Fatalf("provider request changed: %+v", request)
	}
	if len(provider.getCalls()) != 0 {
		t.Fatal("worker unexpectedly used GET after create returned the authoritative pending payment")
	}

	got := f.request(t, owner, http.MethodGet, "/api/v1/billing/orders/"+order.ID, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("owner order status=%d: %s", got.Code, got.Body.String())
	}
	var fetched struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.Order.ConfirmationURL != "https://yookassa.ru/checkout/"+testProviderID || fetched.Order.ExpiresAt != nil {
		t.Fatalf("owner GET order=%+v", fetched.Order)
	}
	other := f.newActor(t, "Other billing owner")
	if got = f.request(t, other, http.MethodGet, "/api/v1/billing/orders/"+order.ID, nil); got.Code != http.StatusNotFound {
		t.Fatalf("foreign owner GET status=%d, want 404", got.Code)
	}
	if got = f.request(t, owner, http.MethodGet, "/api/v1/billing/orders/8b9d2155-3d8a-44f0-a2a0-0fc3a8d2c8b4", nil); got.Code != http.StatusNotFound {
		t.Fatalf("unknown owner order GET status=%d, want 404", got.Code)
	}
	unauthenticated := httptest.NewRecorder()
	f.handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/v1/billing/orders/"+order.ID, nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated order GET status=%d, want 401", unauthenticated.Code)
	}
}

func TestYooKassaWebhookIsOnlyAHintAndFulfillmentIsAtomicAndIdempotent(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "YooKassa webhook owner")
	provider := &fakeYooKassa{}
	var orderID string
	provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
		var create struct {
			Amount struct{ Value, Currency string } `json:"amount"`
		}
		if err := json.Unmarshal(body, &create); err != nil {
			return contracts.ProviderPayment{}, err
		}
		mode := true
		return contracts.ProviderPayment{ID: testProviderID, Status: "pending", AmountValue: create.Amount.Value, Currency: create.Amount.Currency, OrderID: key, ShopID: "123456", ConfirmationURL: "https://yookassa.ru/checkout/" + testProviderID, Test: &mode}, nil
	}
	provider.get = func(id string) (contracts.ProviderPayment, error) {
		mode := true
		metadataOrder := orderID
		if id != testProviderID {
			metadataOrder = "9d8d3c2a-5b6e-4f70-8b9c-0d1e2f3a4b5c"
		}
		return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "29.00", Currency: "RUB", OrderID: metadataOrder, ShopID: "123456", Test: &mode}, nil
	}
	f.configureYooKassa(t, owner, provider)
	order := createYooKassaOrder(t, f, owner, "49a42c71-4f4f-4f9c-bae6-8050ee137305", 1)
	orderID = order.ID

	foreignPaymentID := "foreign12345678901"
	notices := []string{
		`{"type":"notification","event":"payment.succeeded","object":{"id":"` + foreignPaymentID + `","metadata":{"order_id":"` + order.ID + `"}}}`,
		`{"type":"notification","event":"payment.succeeded","object":{"id":"` + testProviderID + `","metadata":{"order_id":"` + order.ID + `"}},"payment_method":{"card":{"number":"synthetic-card-data-must-not-persist"}}}`,
		`{"type":"notification","event":"payment.succeeded","object":{"id":"` + testProviderID + `","metadata":{"order_id":"` + order.ID + `"}}}`,
	}
	for i, notice := range notices {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/webhooks/billing/yookassa", strings.NewReader(notice))
		req.Header.Set("Content-Type", "application/json")
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook %d status=%d: %s", i, rec.Code, rec.Body.String())
		}
	}
	var inboxCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.webhook_inbox WHERE order_id=$1 AND event='payment.succeeded'`, order.ID).Scan(&inboxCount); err != nil || inboxCount != 2 {
		t.Fatalf("webhook inbox count=%d error=%v, want canonical and foreign hint", inboxCount, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("foreign hint GET worker=(%t,%v)", processed, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("authoritative GET worker=(%t,%v)", processed, err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || processed {
		t.Fatalf("replay worker=(%t,%v), want no duplicate fulfillment", processed, err)
	}
	var status, fulfillment, source string
	var quota, reportCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT status,fulfillment_status FROM billing.orders WHERE id=$1`, order.ID).Scan(&status, &fulfillment); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT source,monitor_limit FROM billing.entitlements WHERE account_id=$1`, owner.id).Scan(&source, &quota); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.payment_reports WHERE order_id=$1`, order.ID).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || fulfillment != "fulfilled" || source != "yookassa_test" || quota != 1 || reportCount != 1 {
		t.Fatalf("fulfillment status=%s/%s entitlement=%s/%d reports=%d", status, fulfillment, source, quota, reportCount)
	}
	var acceptedHints, rejectedHints, unfinishedHints int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE result='accepted'),count(*) FILTER (WHERE result='rejected'),count(*) FILTER (WHERE processed_at IS NULL) FROM billing.webhook_inbox WHERE order_id=$1`, order.ID).Scan(&acceptedHints, &rejectedHints, &unfinishedHints); err != nil || acceptedHints != 1 || rejectedHints != 1 || unfinishedHints != 0 {
		t.Fatalf("terminal hint results accepted=%d rejected=%d unfinished=%d error=%v", acceptedHints, rejectedHints, unfinishedHints, err)
	}
	var serializedInbox string
	if err := f.pool.QueryRow(context.Background(), `SELECT row_to_json(w)::text FROM billing.webhook_inbox w WHERE order_id=$1`, order.ID).Scan(&serializedInbox); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(serializedInbox, "synthetic-card-data") {
		t.Fatalf("raw card data was persisted in webhook inbox: %s", serializedInbox)
	}
	late := `{"type":"notification","event":"payment.canceled","object":{"id":"` + testProviderID + `","metadata":{"order_id":"` + order.ID + `"}}}`
	lateResponse := httptest.NewRecorder()
	lateRequest := httptest.NewRequest(http.MethodPost, "/webhooks/billing/yookassa", strings.NewReader(late))
	lateRequest.Header.Set("Content-Type", "application/json")
	f.handler.ServeHTTP(lateResponse, lateRequest)
	if lateResponse.Code != http.StatusOK {
		t.Fatalf("late terminal webhook status=%d: %s", lateResponse.Code, lateResponse.Body.String())
	}
	var lateProcessed, lateRejected int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE processed_at IS NOT NULL),count(*) FILTER (WHERE result='rejected') FROM billing.webhook_inbox WHERE order_id=$1 AND event='payment.canceled'`, order.ID).Scan(&lateProcessed, &lateRejected); err != nil || lateProcessed != 1 || lateRejected != 1 {
		t.Fatalf("late terminal hint processed=%d rejected=%d error=%v", lateProcessed, lateRejected, err)
	}
}

func TestYooKassaExpiredCreateIdempotencyWindowNeedsManualReviewWithoutCancelOrRecreate(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "YooKassa uncertain create owner")
	provider := &fakeYooKassa{}
	f.configureYooKassa(t, owner, provider)
	order := createYooKassaOrder(t, f, owner, "4b9e2a66-d75c-46a0-9b77-2a036110fb1d", 1)
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET first_attempt_at=now()-interval '24 hours',idempotency_deadline=now()-interval '1 hour',next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatalf("age first provider attempt before it starts: %v", err)
	}
	processed, err := runPaymentWorker(t, f)
	if err != nil || processed {
		t.Fatalf("expired create window worker=(%t,%v), want stop before provider call", processed, err)
	}
	var status, reconciliation string
	var expiresAt *time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,expires_at FROM billing.orders WHERE id=$1`, order.ID).Scan(&status, &reconciliation, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || reconciliation != "manual_review" || expiresAt != nil {
		t.Fatalf("aged uncertain order status=%s reconciliation=%s expires=%v", status, reconciliation, expiresAt)
	}
	if keys, _ := provider.createCalls(); len(keys) != 0 {
		t.Fatalf("provider create was repeated after immutable-key window: %v", keys)
	}
	retried := createYooKassaOrder(t, f, owner, "4b9e2a66-d75c-46a0-9b77-2a036110fb1d", 1)
	if retried.ID != order.ID {
		t.Fatalf("idempotent retry changed order %s to %s", order.ID, retried.ID)
	}
}

func TestYooKassaCreateTransportFailureRetriesSameImmutableRequest(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "YooKassa ambiguous create owner")
	provider := &fakeYooKassa{}
	first := true
	provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
		if first {
			first = false
			return contracts.ProviderPayment{}, errors.New("response body ended after partial read")
		}
		var request struct {
			Amount struct {
				Value    string `json:"value"`
				Currency string `json:"currency"`
			} `json:"amount"`
			Metadata struct {
				OrderID string `json:"order_id"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return contracts.ProviderPayment{}, err
		}
		mode := true
		return contracts.ProviderPayment{
			ID: testProviderID, Status: "pending", AmountValue: request.Amount.Value,
			Currency: request.Amount.Currency, OrderID: request.Metadata.OrderID,
			ShopID: "123456", Test: &mode,
		}, nil
	}
	f.configureYooKassa(t, owner, provider)
	order := createYooKassaOrder(t, f, owner, "34168925-2ac4-42e4-8b43-4e261dd6352e", 1)
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("first ambiguous create worker=(%t,%v)", processed, err)
	}
	var status, reconciliation, lastError string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,last_error_code FROM billing.orders WHERE id=$1`, order.ID).Scan(&status, &reconciliation, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || reconciliation != "pending" || lastError != "payment_provider_unavailable" {
		t.Fatalf("ambiguous create state=%s/%s/%s, want pending retry", status, reconciliation, lastError)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("retry create worker=(%t,%v)", processed, err)
	}
	keys, bodies := provider.createCalls()
	if len(keys) != 2 || keys[0] != order.ID || keys[1] != order.ID || len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("retry changed immutable provider request: keys=%v bodies=%q", keys, bodies)
	}
}

func TestYooKassaSuccessDoesNotOverwriteAdminGrantOrReviveDeletedAccount(t *testing.T) {
	t.Run("admin grant remains unchanged", func(t *testing.T) {
		f := newBillingFixture(t)
		owner := f.newActor(t, "YooKassa admin-grant owner")
		provider := &fakeYooKassa{}
		var orderID string
		provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
			var req struct {
				Amount struct{ Value, Currency string } `json:"amount"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				return contracts.ProviderPayment{}, err
			}
			mode := true
			return contracts.ProviderPayment{ID: testProviderID, Status: "pending", AmountValue: req.Amount.Value, Currency: req.Amount.Currency, OrderID: key, ShopID: "123456", Test: &mode}, nil
		}
		provider.get = func(id string) (contracts.ProviderPayment, error) {
			mode := true
			return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "145.00", Currency: "RUB", OrderID: orderID, ShopID: "123456", Test: &mode}, nil
		}
		f.configureYooKassa(t, owner, provider)
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		grantUntil := time.Now().Add(12 * time.Hour).Truncate(time.Microsecond)
		grant, err := f.module.GrantAdminTx(context.Background(), tx, owner.id, 0, 3, grantUntil, "approved test grant")
		if err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatal(err)
		}
		if err := tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		order := createYooKassaOrder(t, f, owner, "ce703244-c641-4a5a-9637-98681e00c57b", 5)
		orderID = order.ID
		if _, err := runPaymentWorker(t, f); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := runPaymentWorker(t, f); err != nil {
			t.Fatal(err)
		}
		var status, fulfillment, reason string
		var activeUntil *time.Time
		if err := f.pool.QueryRow(context.Background(), `SELECT status,fulfillment_status,fulfillment_reason,active_until FROM billing.orders WHERE id=$1`, order.ID).Scan(&status, &fulfillment, &reason, &activeUntil); err != nil {
			t.Fatal(err)
		}
		var current contracts.Entitlement
		if err := f.pool.QueryRow(context.Background(), `SELECT monitor_limit,valid_until,source,version FROM billing.entitlements WHERE account_id=$1`, owner.id).Scan(&current.Quota, &current.ActiveUntil, &current.Source, &current.Version); err != nil {
			t.Fatal(err)
		}
		if status != "succeeded" || fulfillment != "blocked" || reason != "admin_grant" || activeUntil != nil || current.Quota != grant.Quota || current.Source != "admin_grant" || current.Version != grant.Version || current.ActiveUntil == nil || !current.ActiveUntil.Equal(grantUntil) {
			t.Fatalf("paid order/grant state order=%s/%s/%s until=%v grant=%+v current=%+v", status, fulfillment, reason, activeUntil, grant, current)
		}
	})

	t.Run("expired admin grant no longer blocks fulfillment", func(t *testing.T) {
		f := newBillingFixture(t)
		owner := f.newActor(t, "YooKassa expired grant owner")
		provider := &fakeYooKassa{}
		var orderID string
		provider.get = func(id string) (contracts.ProviderPayment, error) {
			mode := true
			return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "29.00", Currency: "RUB", OrderID: orderID, ShopID: "123456", Test: &mode}, nil
		}
		f.configureYooKassa(t, owner, provider)
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.module.GrantAdminTx(context.Background(), tx, owner.id, 0, 5, time.Now().Add(24*time.Hour), "temporary test grant"); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatal(err)
		}
		if err := tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE billing.entitlements SET valid_until=now()-interval '1 second' WHERE account_id=$1`, owner.id); err != nil {
			t.Fatal(err)
		}
		order := createYooKassaOrder(t, f, owner, "e1327648-ae48-468e-9ca9-8f02ec939596", 1)
		orderID = order.ID
		if processed, err := runPaymentWorker(t, f); err != nil || !processed {
			t.Fatalf("create worker=(%t,%v)", processed, err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
			t.Fatal(err)
		}
		if processed, err := runPaymentWorker(t, f); err != nil || !processed {
			t.Fatalf("success worker=(%t,%v)", processed, err)
		}
		var status, fulfillment, source string
		if err := f.pool.QueryRow(context.Background(), `SELECT o.status,o.fulfillment_status,e.source FROM billing.orders o JOIN billing.entitlements e USING(account_id) WHERE o.id=$1`, order.ID).Scan(&status, &fulfillment, &source); err != nil {
			t.Fatal(err)
		}
		if status != "succeeded" || fulfillment != "fulfilled" || source != "yookassa_test" {
			t.Fatalf("expired grant payment state=%s/%s entitlement=%s", status, fulfillment, source)
		}
	})

	t.Run("known payment reconciles after deletion without restoring access", func(t *testing.T) {
		f := newBillingFixture(t)
		owner := f.newActor(t, "YooKassa deleted owner")
		provider := &fakeYooKassa{}
		var orderID string
		provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
			var req struct {
				Amount struct{ Value, Currency string } `json:"amount"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				return contracts.ProviderPayment{}, err
			}
			mode := true
			return contracts.ProviderPayment{ID: testProviderID, Status: "pending", AmountValue: req.Amount.Value, Currency: req.Amount.Currency, OrderID: key, ShopID: "123456", Test: &mode}, nil
		}
		provider.get = func(id string) (contracts.ProviderPayment, error) {
			mode := true
			return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "29.00", Currency: "RUB", OrderID: orderID, ShopID: "123456", Test: &mode}, nil
		}
		f.configureYooKassa(t, owner, provider)
		order := createYooKassaOrder(t, f, owner, "4d4158c1-e576-4d06-9902-9fd4fcb19d42", 1)
		orderID = order.ID
		if _, err := runPaymentWorker(t, f); err != nil {
			t.Fatal(err)
		}
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(context.Background(), `UPDATE accounts.accounts SET status='deleted',deleted_at=now(),version=version+1 WHERE id=$1`, owner.id); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatal(err)
		}
		if err = f.module.DeleteAccountTx(context.Background(), tx, owner.id); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatal(err)
		}
		if err = tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
			t.Fatal(err)
		}
		if processed, err := runPaymentWorker(t, f); err != nil || !processed {
			t.Fatalf("known payment after deletion worker=(%t,%v)", processed, err)
		}
		var status, fulfillment, reason string
		if err := f.pool.QueryRow(context.Background(), `SELECT status,fulfillment_status,fulfillment_reason FROM billing.orders WHERE id=$1`, order.ID).Scan(&status, &fulfillment, &reason); err != nil {
			t.Fatal(err)
		}
		var entitlementCount int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.entitlements WHERE account_id=$1 AND valid_until>now()`, owner.id).Scan(&entitlementCount); err != nil {
			t.Fatal(err)
		}
		if status != "succeeded" || fulfillment != "blocked" || reason != "account_deleted" || entitlementCount != 0 {
			t.Fatalf("deleted paid account state=%s/%s/%s active entitlements=%d", status, fulfillment, reason, entitlementCount)
		}
	})
}

func TestYooKassaWebhookRejectsOversizedBodyAndUnknownEvents(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "YooKassa webhook request owner")
	f.configureYooKassa(t, owner, &fakeYooKassa{})

	request := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/webhooks/billing/yookassa", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		f.handler.ServeHTTP(rec, req)
		return rec
	}
	if got := request(`{"type":"notification","event":"refund.succeeded","object":{"id":"` + testProviderID + `"}}`); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown webhook event status=%d, want 400", got.Code)
	}
	if got := request(`{"type":"notification","event":"payment.succeeded","object":{"id":"` + testProviderID + `"},"extra":"` + strings.Repeat("x", 66<<10) + `"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("oversized webhook status=%d, want 400", got.Code)
	}
}
