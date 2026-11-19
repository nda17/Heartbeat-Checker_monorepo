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
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/service"
)

const testProviderID = "payment1234567890123456"

type fakeYooKassa struct {
	mu                sync.Mutex
	mode              string
	shopID            string
	paymentMethodType string
	createKeys        []string
	createBodies      [][]byte
	getIDs            []string
	create            func(string, []byte) (contracts.ProviderPayment, error)
	get               func(string) (contracts.ProviderPayment, error)
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
	testMode := p.mode != "production"
	shopID := p.shop()
	return contracts.ProviderPayment{
		ID: testProviderID, Status: "pending", AmountValue: request.Amount.Value,
		Currency: request.Amount.Currency, OrderID: request.Metadata.OrderID,
		ShopID: shopID, ConfirmationURL: "https://yookassa.ru/checkout/" + testProviderID,
		Test: &testMode, PaymentMethodType: p.paymentMethodType,
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
	testMode := p.mode != "production"
	return contracts.ProviderPayment{
		ID: id, Status: "pending", AmountValue: "29.00", Currency: "RUB",
		OrderID: "", ShopID: p.shop(), Test: &testMode,
	}, nil
}

func (p *fakeYooKassa) shop() string {
	if p.shopID != "" {
		return p.shopID
	}
	return "123456"
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
	f.configureYooKassaMode(t, owner, provider, "test", "123456")
}

func (f *billingFixture) configureYooKassaMode(t *testing.T, owner billingActor, provider *fakeYooKassa, mode, shop string) {
	t.Helper()
	config := Config{Provider: "yookassa", Mode: mode, ShopID: shop, PaymentProvider: provider}
	if mode == "test" {
		config.TestAccountIDs = []string{owner.id}
	}
	module, err := NewConfigured(f.pool, false, config, f.accounts)
	if err != nil {
		t.Fatalf("configure test YooKassa provider: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module, f.handler = module, mux
}

func TestYooKassaProductionCheckoutIsAvailableToAllActiveAccountsAndPersistsImmutableModeShop(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Production billing account")
	other := f.newActor(t, "Another production billing account")
	provider := &fakeYooKassa{mode: "production", shopID: "654321"}
	f.configureYooKassaMode(t, owner, provider, "production", "654321")

	for _, account := range []billingActor{owner, other} {
		response := f.request(t, account, http.MethodGet, "/api/v1/billing", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("production overview status=%d: %s", response.Code, response.Body.String())
		}
		var payload contracts.Billing
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.CanPay || payload.Test {
			t.Fatalf("production account %s overview must enable paid checkout without test mode: %+v", account.id, payload)
		}
	}

	key := "c11ab93f-8056-4c86-9b84-443c36fa0f89"
	order := createYooKassaOrder(t, f, other, key, 5800)
	if order.Test {
		t.Fatalf("production order was serialized as test: %+v", order)
	}
	retry := createYooKassaOrder(t, f, other, key, 5800)
	if retry.ID != order.ID {
		t.Fatalf("idempotent retry changed order ID %s to %s", order.ID, retry.ID)
	}
	conflict := f.request(t, other, http.MethodPost, "/api/v1/billing/topups", map[string]any{
		"amountMinor": 8700, "idempotencyKey": key,
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed same-key order status=%d, want 409", conflict.Code)
	}
	var mode, shop string
	var amount int
	var request []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT provider_mode,provider_shop_id,amount_minor,provider_request FROM billing.orders WHERE id=$1`, order.ID).Scan(&mode, &shop, &amount, &request); err != nil {
		t.Fatal(err)
	}
	if mode != "production" || shop != "654321" || amount != 5800 || !strings.Contains(string(request), `"value":"58.00"`) {
		t.Fatalf("immutable order fields mode=%s shop=%s amount=%d request=%s", mode, shop, amount, request)
	}

	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("production worker=(%t,%v)", processed, err)
	}
	if keys, _ := provider.createCalls(); len(keys) != 1 || keys[0] != order.ID {
		t.Fatalf("production provider calls=%v, want immutable order UUID once", keys)
	}
}

func TestYooKassaProductionSuccessAddsWalletCreditAndSurvivesDisabledCheckout(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Production payment owner")
	provider := &fakeYooKassa{mode: "production", shopID: "654321"}
	var orderID string
	provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
		var request struct {
			Amount struct{ Value, Currency string } `json:"amount"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return contracts.ProviderPayment{}, err
		}
		mode := false
		return contracts.ProviderPayment{ID: testProviderID, Status: "pending", AmountValue: request.Amount.Value, Currency: request.Amount.Currency, OrderID: key, ShopID: "654321", ConfirmationURL: "https://yookassa.ru/checkout/" + testProviderID, Test: &mode}, nil
	}
	provider.get = func(id string) (contracts.ProviderPayment, error) {
		mode := false
		return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "58.00", Currency: "RUB", OrderID: orderID, ShopID: "654321", Test: &mode}, nil
	}
	f.configureYooKassaMode(t, owner, provider, "production", "654321")
	creditPaymentFixture(t, f, owner, 2000)
	order := createYooKassaOrder(t, f, owner, "20bff0f2-670f-4d5c-a5ad-52c54a64b538", 5800)
	orderID = order.ID
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("production create worker=(%t,%v)", processed, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("production success worker=(%t,%v)", processed, err)
	}
	var status, fulfillment, kind string
	var amount, reports int
	if err := f.pool.QueryRow(context.Background(), `SELECT o.status,o.fulfillment_status,l.kind,l.amount_minor,
        (SELECT count(*) FROM billing.payment_reports r WHERE r.order_id=o.id)
        FROM billing.orders o JOIN billing.wallet_ledger l ON l.order_id=o.id WHERE o.id=$1`, order.ID).Scan(&status, &fulfillment, &kind, &amount, &reports); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || fulfillment != "fulfilled" || kind != "top_up" || amount != 5800 || reports != 1 {
		t.Fatalf("production order=%s/%s credit=%s/%d reports=%d", status, fulfillment, kind, amount, reports)
	}

	disabled, err := NewConfigured(f.pool, false, Config{Provider: "disabled"}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.service.CreateTopup(context.Background(), owner.id, 2600, "bd5c7a69-6b9e-48ea-9672-8001f54b09d5", "web"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("disabled checkout created a top-up: %v", err)
	}
	balance, err := disabled.Balance(context.Background(), owner.id)
	if err != nil || balance.AvailableMinor != 7800 {
		t.Fatalf("disabled checkout changed wallet: %+v error=%v", balance, err)
	}
}

func creditPaymentFixture(t *testing.T, f *billingFixture, owner billingActor, amount int) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, applied, err := f.module.AdminCreditTx(ctx, tx, owner.id, owner.id, amount,
		"da9d24b6-bdf2-46a7-9c09-fdbc5068f066", "existing fixture credit"); err != nil || !applied {
		t.Fatalf("seed wallet credit applied=%t err=%v", applied, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLateTestPaymentDoesNotCreditWallet(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Late test payment owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456"}
	f.configureYooKassaMode(t, owner, provider, "test", "123456")
	order := createYooKassaOrder(t, f, owner, "f8028a04-a97f-4426-8345-b788cc696975", 5800)
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("test payment create worker=(%t,%v)", processed, err)
	}
	creditPaymentFixture(t, f, owner, 2000)
	provider.get = func(id string) (contracts.ProviderPayment, error) {
		mode := true
		return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "58.00", Currency: "RUB", OrderID: order.ID, ShopID: "123456", Test: &mode}, nil
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("late test payment reconciliation=(%t,%v)", processed, err)
	}
	var status, fulfillment string
	var paymentCredits int
	if err := f.pool.QueryRow(context.Background(), `SELECT o.status,o.fulfillment_status,
        (SELECT count(*) FROM billing.wallet_ledger l WHERE l.order_id=o.id) FROM billing.orders o WHERE o.id=$1`, order.ID).Scan(&status, &fulfillment, &paymentCredits); err != nil {
		t.Fatal(err)
	}
	balance, err := f.module.Balance(context.Background(), owner.id)
	if status != "succeeded" || fulfillment != "not_required" || paymentCredits != 0 || err != nil || balance.AvailableMinor != 2000 {
		t.Fatalf("test order=%s/%s credits=%d wallet=%+v err=%v", status, fulfillment, paymentCredits, balance, err)
	}
}

func TestYooKassaReconciliationRequiresImmutableModeAndShopMatch(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Mode and shop reconciliation owner")
	production := &fakeYooKassa{mode: "production", shopID: "654321"}
	f.configureYooKassaMode(t, owner, production, "production", "654321")
	order := createYooKassaOrder(t, f, owner, "4d814e2e-4ec6-4d5d-9e15-56dfdb83b5aa", 2900)

	testProvider := &fakeYooKassa{mode: "test", shopID: "123456"}
	f.configureYooKassaMode(t, owner, testProvider, "test", "123456")
	if processed, err := runPaymentWorker(t, f); err != nil || processed {
		t.Fatalf("test-mode worker claimed production order=(%t,%v)", processed, err)
	}
	if keys, _ := testProvider.createCalls(); len(keys) != 0 {
		t.Fatalf("test-mode provider received production order: %v", keys)
	}

	wrongShop := &fakeYooKassa{mode: "production", shopID: "999999"}
	f.configureYooKassaMode(t, owner, wrongShop, "production", "999999")
	if processed, err := runPaymentWorker(t, f); err != nil || processed {
		t.Fatalf("wrong-shop worker claimed production order=(%t,%v)", processed, err)
	}
	if keys, _ := wrongShop.createCalls(); len(keys) != 0 {
		t.Fatalf("wrong-shop provider received production order: %v", keys)
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	f.configureYooKassaMode(t, owner, production, "production", "654321")
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("matching production worker did not claim order=(%t,%v)", processed, err)
	}
	if keys, _ := production.createCalls(); len(keys) != 1 || keys[0] != order.ID {
		t.Fatalf("matching production provider calls=%v, want order UUID", keys)
	}
}

func createYooKassaOrder(t *testing.T, f *billingFixture, owner billingActor, key string, amountMinor int) contracts.Order {
	t.Helper()
	rec := f.request(t, owner, http.MethodPost, "/api/v1/billing/topups", map[string]any{
		"amountMinor": amountMinor, "idempotencyKey": key,
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

func TestHistoricalBlockedOrderRemainsReadOnlyAfterLateWebhook(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Historical Blocked Order Owner")
	provider := &fakeYooKassa{mode: "production", shopID: "654321"}
	f.configureYooKassaMode(t, owner, provider, "production", "654321")
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	const orderID = "ae4727f4-0c31-4364-a95d-f5ac202d8c75"
	if _, err := tx.Exec(ctx, `ALTER TABLE billing.orders DISABLE TRIGGER orders_topup_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,provider_mode,provider_shop_id,provider_payment_id,
        provider_request,monitor_count,amount_minor,status,completed_at,fulfillment_status,fulfillment_reason,reconciliation_status,purchase_kind)
        VALUES($1,$2,$1,'yookassa','production','654321',$3,convert_to('{}','UTF8'),1,2600,'succeeded',now(),'blocked','admin_grant','complete','purchase')`,
		orderID, owner.id, testProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE billing.orders ENABLE TRIGGER orders_topup_only`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var notice contracts.ProviderNotification
	notice.Type, notice.Event, notice.Object.ID, notice.Object.Metadata.OrderID = "notification", "payment.succeeded", testProviderID, orderID
	if err := f.module.service.ReceiveNotification(ctx, notice); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || processed || len(provider.getCalls()) != 0 {
		t.Fatalf("archived payment worker=(%t,%v) calls=%v", processed, err, provider.getCalls())
	}
	order, err := f.module.service.GetOrder(ctx, owner.id, orderID)
	if err != nil || order.FulfillmentStatus != "blocked" || order.FulfillmentReason != "admin_grant" || order.AmountMinor != 2600 {
		t.Fatalf("archived order=%+v err=%v", order, err)
	}
	balance, err := f.module.Balance(ctx, owner.id)
	if err != nil || balance.AvailableMinor != 0 {
		t.Fatalf("archived payment changed wallet: %+v err=%v", balance, err)
	}
	overview := f.request(t, owner, http.MethodGet, "/api/v1/billing", nil)
	var payload contracts.Billing
	if err := json.Unmarshal(overview.Body.Bytes(), &payload); overview.Code != http.StatusOK || err != nil || len(payload.Orders) != 0 {
		t.Fatalf("archive appeared in active checkout: status=%d payload=%+v err=%v", overview.Code, payload, err)
	}
	history := f.request(t, owner, http.MethodGet, "/api/v1/billing/orders", nil)
	var page contracts.OrderPage
	if err := json.Unmarshal(history.Body.Bytes(), &page); history.Code != http.StatusOK || err != nil || len(page.Orders) != 1 || page.Orders[0].ID != orderID {
		t.Fatalf("archive lost from order history: status=%d page=%+v err=%v", history.Code, page, err)
	}
}

func TestYooKassaOrderIsDurableBeforeProviderAndUsesImmutableProviderKey(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "YooKassa checkout owner")
	provider := &fakeYooKassa{}
	f.configureYooKassa(t, owner, provider)

	key := "b9f02cf9-dbf8-4a6a-bff8-18a5d8974f82"
	order := createYooKassaOrder(t, f, owner, key, 5800)
	if order.Status != "pending" || order.Provider != "yookassa" || order.ExpiresAt != nil || order.ReconciliationStatus != "pending" {
		t.Fatalf("new order must be durable, pending, and unexpired: %+v", order)
	}
	if keys, _ := provider.createCalls(); len(keys) != 0 {
		t.Fatalf("HTTP create called provider synchronously: keys=%v", keys)
	}
	retry := createYooKassaOrder(t, f, owner, key, 5800)
	if retry.ID != order.ID {
		t.Fatalf("same idempotency key returned a different order: %s != %s", retry.ID, order.ID)
	}
	conflict := f.request(t, owner, http.MethodPost, "/api/v1/billing/topups", map[string]any{
		"amountMinor": 8700, "idempotencyKey": key,
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
	if request.Amount.Value != "58.00" || request.Amount.Currency != "RUB" || !request.Capture || request.Metadata.OrderID != order.ID || request.Confirmation.ReturnURL != "https://heartbeat-checker.ru/app" {
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
	provider := &fakeYooKassa{mode: "production", shopID: "123456"}
	var orderID string
	provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
		var create struct {
			Amount struct{ Value, Currency string } `json:"amount"`
		}
		if err := json.Unmarshal(body, &create); err != nil {
			return contracts.ProviderPayment{}, err
		}
		mode := false
		return contracts.ProviderPayment{ID: testProviderID, Status: "pending", AmountValue: create.Amount.Value, Currency: create.Amount.Currency, OrderID: key, ShopID: "123456", ConfirmationURL: "https://yookassa.ru/checkout/" + testProviderID, Test: &mode}, nil
	}
	provider.get = func(id string) (contracts.ProviderPayment, error) {
		mode := false
		metadataOrder := orderID
		if id != testProviderID {
			metadataOrder = "9d8d3c2a-5b6e-4f70-8b9c-0d1e2f3a4b5c"
		}
		return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "29.00", Currency: "RUB", OrderID: metadataOrder, ShopID: "123456", Test: &mode}, nil
	}
	f.configureYooKassaMode(t, owner, provider, "production", "123456")
	order := createYooKassaOrder(t, f, owner, "49a42c71-4f4f-4f9c-bae6-8050ee137305", 2900)
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
	var status, fulfillment, kind string
	var amount, reportCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT status,fulfillment_status FROM billing.orders WHERE id=$1`, order.ID).Scan(&status, &fulfillment); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT kind,amount_minor FROM billing.wallet_ledger WHERE order_id=$1`, order.ID).Scan(&kind, &amount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.payment_reports WHERE order_id=$1`, order.ID).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || fulfillment != "fulfilled" || kind != "top_up" || amount != 2900 || reportCount != 1 {
		t.Fatalf("fulfillment status=%s/%s credit=%s/%d reports=%d", status, fulfillment, kind, amount, reportCount)
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
	order := createYooKassaOrder(t, f, owner, "4b9e2a66-d75c-46a0-9b77-2a036110fb1d", 2900)
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
	retried := createYooKassaOrder(t, f, owner, "4b9e2a66-d75c-46a0-9b77-2a036110fb1d", 2900)
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
	order := createYooKassaOrder(t, f, owner, "34168925-2ac4-42e4-8b43-4e261dd6352e", 2900)
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

func TestYooKassaPaymentAfterDeletionDoesNotRestoreWallet(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "YooKassa deleted owner")
	provider := &fakeYooKassa{mode: "production", shopID: "654321"}
	ctx := context.Background()
	f.configureYooKassaMode(t, owner, provider, "production", "654321")
	creditPaymentFixture(t, f, owner, 2000)
	order := createYooKassaOrder(t, f, owner, "4d4158c1-e576-4d06-9902-9fd4fcb19d42", 2900)
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("before deletion worker=(%t,%v)", processed, err)
	}
	provider.get = func(id string) (contracts.ProviderPayment, error) {
		mode := false
		return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "29.00", Currency: "RUB", OrderID: order.ID, ShopID: "654321", Test: &mode}, nil
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := f.module.Lock(ctx, tx, owner.id); err != nil {
		t.Fatal(err)
	}
	if err := f.module.DeleteAccountTx(ctx, tx, owner.id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts.accounts SET status='deleted',deleted_at=now(),version=version+1 WHERE id=$1`, owner.id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("known payment after deletion worker=(%t,%v)", processed, err)
	}
	var status, fulfillment, reason string
	var wallet, originalCredits, paymentCredits int
	if err := f.pool.QueryRow(ctx, `SELECT status,fulfillment_status,fulfillment_reason,
        (SELECT count(*) FROM billing.wallets WHERE account_id=$2),
        (SELECT count(*) FROM billing.wallet_ledger WHERE account_id=$2 AND kind='admin_credit'),
        (SELECT count(*) FROM billing.wallet_ledger WHERE order_id=$1)
        FROM billing.orders WHERE id=$1`, order.ID, owner.id).
		Scan(&status, &fulfillment, &reason, &wallet, &originalCredits, &paymentCredits); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || fulfillment != "blocked" || reason != "account_deleted" || wallet != 0 || originalCredits != 1 || paymentCredits != 0 {
		t.Fatalf("order=%s/%s/%s wallet=%d historical credit=%d payment credit=%d", status, fulfillment, reason, wallet, originalCredits, paymentCredits)
	}
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
