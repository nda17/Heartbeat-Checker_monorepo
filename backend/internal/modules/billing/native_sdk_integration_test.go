package billing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/adapters/yookassa"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/service"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

type sdkActor struct {
	billingActor
	bearer    string
	sessionID string
}

func newSDKActor(t *testing.T, f *billingFixture, name string) sdkActor {
	t.Helper()
	base := f.newActor(t, name)
	var accessBytes, revokeBytes [32]byte
	if _, err := rand.Read(accessBytes[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(revokeBytes[:]); err != nil {
		t.Fatal(err)
	}
	bearer := base64.RawURLEncoding.EncodeToString(accessBytes[:])
	revoke := base64.RawURLEncoding.EncodeToString(revokeBytes[:])
	sessionID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessHash, revokeHash := sha256.Sum256([]byte(bearer)), sha256.Sum256([]byte(revoke))
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,revoke_token_hash,credential_version,expires_at,user_agent,client_kind)
		VALUES($1,$2,$3,$4,1,now()+interval '1 hour','native SDK integration test','android')`, sessionID, base.id, accessHash[:], revokeHash[:]); err != nil {
		t.Fatalf("create Android billing session: %v", err)
	}
	return sdkActor{billingActor: base, bearer: bearer, sessionID: sessionID}
}

func (f *billingFixture) configureYooKassaSDK(t *testing.T, owner sdkActor, provider *fakeYooKassa) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cfg := Config{Provider: "yookassa", Mode: provider.mode, ShopID: provider.shop(),
		PaymentProvider: provider, NativeSDKEnabled: true, MailEncryptionKey: key, SessionLease: auth.SessionLeaseTx}
	if cfg.Mode == "test" {
		cfg.TestAccountIDs = []string{owner.id}
	}
	module, err := NewConfigured(f.pool, false, cfg, f.accounts)
	if err != nil {
		t.Fatalf("configure native YooKassa SDK: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module, f.handler = module, mux
}

func (f *billingFixture) sdkRequest(t *testing.T, actor sdkActor, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+actor.bearer)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

type nativeOrderResponse struct {
	Order contracts.Order    `json:"order"`
	SDK   contracts.SDKQuote `json:"sdk"`
}

// Expiry paths use a past timestamp without waiting 15/55 minutes. The table's
// production trigger intentionally makes quote deadlines immutable, so disable
// it only inside the isolated fixture database transaction for this clock setup.
func forceSDKExpiry(t *testing.T, f *billingFixture, orderID string, request bool) {
	t.Helper()
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `ALTER TABLE billing.sdk_payment_requests DISABLE TRIGGER sdk_request_immutable`); err != nil {
		t.Fatal(err)
	}
	query := `UPDATE billing.sdk_payment_requests SET draft_expires_at=now()-interval '1 second',request_expires_at=now()+interval '39 minutes 59 seconds' WHERE order_id=$1`
	if request {
		query = `UPDATE billing.sdk_payment_requests SET draft_expires_at=now()-interval '40 minutes 1 second',request_expires_at=now()-interval '1 second' WHERE order_id=$1`
	}
	if _, err := tx.Exec(context.Background(), query, orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `ALTER TABLE billing.sdk_payment_requests ENABLE TRIGGER sdk_request_immutable`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func createNativeOrder(t *testing.T, f *billingFixture, actor sdkActor, key string, amountMinor int) nativeOrderResponse {
	t.Helper()
	rec := f.sdkRequest(t, actor, http.MethodPost, "/api/v1/billing/native/topups", map[string]any{
		"amountMinor": amountMinor, "idempotencyKey": key,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create native order status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result nativeOrderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Order.ID == "" {
		t.Fatalf("decode native order: %v body=%s", err, rec.Body.String())
	}
	return result
}

func attachNativeToken(t *testing.T, f *billingFixture, actor sdkActor, orderID, token string) *httptest.ResponseRecorder {
	t.Helper()
	return f.sdkRequest(t, actor, http.MethodPost, "/api/v1/billing/native/orders/"+orderID+"/token", map[string]string{
		"paymentToken": token, "paymentMethodType": "BANK_CARD",
	})
}

func TestNativeSDKQuoteIsStableIdempotentAndBoundToCurrentAndroidSession(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native SDK owner")
	other := newSDKActor(t, f, "Other native SDK owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456"}
	f.configureYooKassaSDK(t, owner, provider)
	key := "bc3d01d0-52c7-4518-9eca-886db22c90f0"
	first := createNativeOrder(t, f, owner, key, 5800)
	if first.Order.Provider != "yookassa" || first.Order.PurchaseKind != "topup" || first.Order.CheckoutKind != "android_sdk" || first.Order.ReturnTarget != "android" || first.Order.AmountMinor != 5800 || first.Order.ExpiresAt != nil {
		t.Fatalf("native quote did not persist expected immutable checkout: %+v", first.Order)
	}
	if first.SDK.ShopID != "123456" || first.SDK.AmountMinor != 5800 || first.SDK.Currency != "RUB" || first.SDK.SavePaymentMethod != "off" || len(first.SDK.AllowedPaymentMethods) != 1 || first.SDK.AllowedPaymentMethods[0] != "BANK_CARD" {
		t.Fatalf("unexpected SDK quote: %+v", first.SDK)
	}
	if first.Order.SDK == nil || first.Order.SDK.State != "awaiting_token" || first.Order.SDK.RequestExpiresAt.Sub(first.Order.SDK.DraftExpiresAt) != 40*time.Minute {
		t.Fatalf("draft/request window contract mismatch: %+v", first.Order.SDK)
	}
	var draft, requestExpiry time.Time
	var databaseDraftRemainingSeconds int64
	if err := f.pool.QueryRow(context.Background(), `SELECT draft_expires_at,request_expires_at,extract(epoch FROM draft_expires_at-clock_timestamp())::bigint FROM billing.sdk_payment_requests WHERE order_id=$1`, first.Order.ID).Scan(&draft, &requestExpiry, &databaseDraftRemainingSeconds); err != nil {
		t.Fatal(err)
	}
	if databaseDraftRemainingSeconds < 14*60 || databaseDraftRemainingSeconds > 15*60 || requestExpiry.Sub(draft) != 40*time.Minute {
		t.Fatalf("database quote windows remaining=%ds draft=%s request=%s", databaseDraftRemainingSeconds, draft, requestExpiry)
	}
	retry := createNativeOrder(t, f, owner, key, 5800)
	if retry.Order.ID != first.Order.ID || retry.SDK.DraftExpiresAt != first.SDK.DraftExpiresAt || retry.Order.SDK == nil || retry.Order.SDK.DraftExpiresAt != first.Order.SDK.DraftExpiresAt {
		t.Fatalf("idempotent quote changed stored order/deadline: first=%+v retry=%+v", first, retry)
	}
	changed := f.sdkRequest(t, owner, http.MethodPost, "/api/v1/billing/native/topups", map[string]any{
		"amountMinor": 8700, "idempotencyKey": key,
	})
	if changed.Code != http.StatusConflict {
		t.Fatalf("same-key changed quote status=%d want 409: %s", changed.Code, changed.Body.String())
	}
	if got := f.sdkRequest(t, other, http.MethodPost, "/api/v1/billing/native/orders/"+first.Order.ID+"/cancel", map[string]any{}); got.Code != http.StatusNotFound {
		t.Fatalf("foreign account reached native order cancel: status=%d body=%s", got.Code, got.Body.String())
	}
	if got := f.request(t, owner.billingActor, http.MethodPost, "/api/v1/billing/native/topups", map[string]any{"amountMinor": 2900, "idempotencyKey": "3c8a392a-2a87-460e-a684-212f7f580b4f"}); got.Code != http.StatusForbidden {
		t.Fatalf("web-cookie session reached SDK-only route: status=%d body=%s", got.Code, got.Body.String())
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, owner.sessionID); err != nil {
		t.Fatal(err)
	}
	if got := attachNativeToken(t, f, owner, first.Order.ID, "sdk-token-session-fenced"); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked Android bearer attached token: status=%d body=%s", got.Code, got.Body.String())
	}
}

func TestNativeSDKTokenAttachIsEncryptedImmutableAndReplaySafe(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native token attach owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456", paymentMethodType: "bank_card"}
	f.configureYooKassaSDK(t, owner, provider)
	quote := createNativeOrder(t, f, owner, "45b3a272-0b6d-433c-9d25-6c2d75687c79", 2900)
	secretToken := "synthetic-one-time-yookassa-token-DoNotPersist"
	attached := attachNativeToken(t, f, owner, quote.Order.ID, secretToken)
	if attached.Code != http.StatusOK {
		t.Fatalf("attach token status=%d body=%s", attached.Code, attached.Body.String())
	}
	var result struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(attached.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Order.ID != quote.Order.ID || result.Order.SDK == nil || result.Order.SDK.State != "processing" || result.Order.SDK.PaymentMethodType != "BANK_CARD" {
		t.Fatalf("attach response did not advance quote: %+v", result.Order)
	}
	var tokenHash, requestHash, encrypted []byte
	var method string
	if err := f.pool.QueryRow(context.Background(), `SELECT token_hash,request_hash,encrypted_request,payment_method_type FROM billing.sdk_payment_requests WHERE order_id=$1`, quote.Order.ID).Scan(&tokenHash, &requestHash, &encrypted, &method); err != nil {
		t.Fatal(err)
	}
	expectedTokenHash := sha256.Sum256([]byte(secretToken))
	if !bytes.Equal(tokenHash, expectedTokenHash[:]) || len(requestHash) != sha256.Size || len(encrypted) < 29 || method != "BANK_CARD" || bytes.Contains(encrypted, []byte(secretToken)) {
		t.Fatalf("attached request is not hash-addressed/encrypted: tokenHash=%x requestHash=%d ciphertext=%d method=%s", tokenHash, len(requestHash), len(encrypted), method)
	}
	replay := attachNativeToken(t, f, owner, quote.Order.ID, secretToken)
	if replay.Code != http.StatusOK {
		t.Fatalf("exact token replay should return existing attempt, got %d %s", replay.Code, replay.Body.String())
	}
	conflict := attachNativeToken(t, f, owner, quote.Order.ID, "different-one-time-token-should-not-replace")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "sdk_token_conflict") {
		t.Fatalf("changed token replaced immutable payment request: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	var orderRequest []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT provider_request FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&orderRequest); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(orderRequest, []byte(secretToken)) {
		t.Fatalf("base order request leaked SDK token: %s", orderRequest)
	}
	canceled := f.sdkRequest(t, owner, http.MethodPost, "/api/v1/billing/native/orders/"+quote.Order.ID+"/cancel", map[string]any{})
	if canceled.Code != http.StatusConflict || !strings.Contains(canceled.Body.String(), "sdk_checkout_started") {
		t.Fatalf("attached payment attempt was canceled before reconciliation: %d %s", canceled.Code, canceled.Body.String())
	}
}

func TestNativeSDKAttachedTokenReachesProviderOnlyForAuthoritativeCreateAndSuccess(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native SDK provider flow owner")
	provider := &fakeYooKassa{mode: "production", shopID: "123456", paymentMethodType: "bank_card"}
	var orderID string
	provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
		var input struct {
			Amount struct {
				Value    string `json:"value"`
				Currency string `json:"currency"`
			} `json:"amount"`
			Metadata struct {
				OrderID string `json:"order_id"`
			} `json:"metadata"`
			PaymentToken string `json:"payment_token"`
		}
		if err := json.Unmarshal(body, &input); err != nil {
			return contracts.ProviderPayment{}, err
		}
		if key != orderID || input.Metadata.OrderID != orderID || input.PaymentToken != "provider-only-token-for-test" {
			return contracts.ProviderPayment{}, fmt.Errorf("unexpected SDK create request identity or token")
		}
		mode := false
		return contracts.ProviderPayment{ID: testProviderID, Status: "pending", AmountValue: input.Amount.Value,
			Currency: input.Amount.Currency, OrderID: orderID, ShopID: "123456", Test: &mode,
			PaymentMethodType: "bank_card"}, nil
	}
	provider.get = func(id string) (contracts.ProviderPayment, error) {
		mode := false
		if id != testProviderID {
			return contracts.ProviderPayment{}, fmt.Errorf("unexpected payment lookup id")
		}
		return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "29.00", Currency: "RUB",
			OrderID: orderID, ShopID: "123456", Test: &mode, PaymentMethodType: "bank_card"}, nil
	}
	f.configureYooKassaSDK(t, owner, provider)
	quote := createNativeOrder(t, f, owner, "50f2b86d-98ed-46e5-b69c-b5d62d2deca7", 2900)
	orderID = quote.Order.ID
	if got := attachNativeToken(t, f, owner, quote.Order.ID, "provider-only-token-for-test"); got.Code != http.StatusOK {
		t.Fatalf("attach=%d %s", got.Code, got.Body.String())
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("provider SDK create=(%t,%v)", processed, err)
	}
	keys, bodies := provider.createCalls()
	if len(keys) != 1 || keys[0] != orderID || len(bodies) != 1 || !bytes.Contains(bodies[0], []byte(`"payment_token":"provider-only-token-for-test"`)) {
		t.Fatalf("provider did not receive the immutable SDK request once: keys=%v bodies=%q", keys, bodies)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("authoritative SDK success lookup=(%t,%v)", processed, err)
	}
	var status, fulfillment, kind string
	var amount, reports int
	if err := f.pool.QueryRow(context.Background(), `SELECT o.status,o.fulfillment_status,l.kind,l.amount_minor,(SELECT count(*) FROM billing.payment_reports r WHERE r.order_id=o.id)
		FROM billing.orders o JOIN billing.wallet_ledger l ON l.order_id=o.id WHERE o.id=$1`, orderID).Scan(&status, &fulfillment, &kind, &amount, &reports); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || fulfillment != "fulfilled" || kind != "top_up" || amount != 2900 || reports != 1 {
		t.Fatalf("authoritative SDK success did not fulfill exactly once: %s/%s %s/%d reports=%d", status, fulfillment, kind, amount, reports)
	}
	var encrypted []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT encrypted_request FROM billing.sdk_payment_requests WHERE order_id=$1`, orderID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted != nil {
		t.Fatal("reconciled payment retained encrypted single-use request")
	}
}

func TestNativeSDKTestProfileCoexistsWithHostedProductionAndRoutesPersistedOrders(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Owner with live balance")
	liveProvider := &fakeYooKassa{mode: "production", shopID: "654321"}
	testProvider := &fakeYooKassa{mode: "test", shopID: "123456", paymentMethodType: "bank_card"}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	module, err := NewConfigured(f.pool, false, Config{
		Provider: "yookassa", Mode: "production", ShopID: "654321", SecretKey: "production_synthetic_secret_123",
		PaymentProvider: liveProvider, TestAccountIDs: []string{owner.id}, TestShopID: "123456",
		TestSecretKey: "test_synthetic_secret_123", TestProvider: testProvider,
		NativeSDKEnabled: true, NativeSDKMode: "test", MailEncryptionKey: key, SessionLease: auth.SessionLeaseTx,
	}, f.accounts)
	if err != nil {
		t.Fatalf("configure hosted-live + native-test profiles: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module, f.handler = module, mux

	creditPaymentFixture(t, f, owner.billingActor, 1500)
	outsider := newSDKActor(t, f, "Non-allowlisted native SDK account")
	outsiderQuote := f.sdkRequest(t, outsider, http.MethodPost, "/api/v1/billing/native/topups", map[string]any{
		"amountMinor": 2900, "idempotencyKey": "e0189fae-d65b-4cc6-932e-7cb91933ab86",
	})
	if outsiderQuote.Code != http.StatusConflict || !strings.Contains(outsiderQuote.Body.String(), "sdk_unavailable") {
		t.Fatalf("non-allowlisted native account reached test SDK checkout: status=%d body=%s", outsiderQuote.Code, outsiderQuote.Body.String())
	}
	var outsiderOrders int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.orders WHERE account_id=$1`, outsider.id).Scan(&outsiderOrders); err != nil || outsiderOrders != 0 {
		t.Fatalf("test profile was applied to non-allowlisted account: orders=%d err=%v", outsiderOrders, err)
	}
	quoteResponse := f.sdkRequest(t, owner, http.MethodPost, "/api/v1/billing/native/topups", map[string]any{
		"amountMinor": 2900, "idempotencyKey": "3f49718b-59d1-40fc-a3d1-3a9dbe213149",
	})
	if quoteResponse.Code != http.StatusCreated {
		t.Fatalf("create native test top-up while a funded wallet exists: status=%d body=%s", quoteResponse.Code, quoteResponse.Body.String())
	}
	var quote nativeOrderResponse
	if err := json.Unmarshal(quoteResponse.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	var orderMode, orderShop string
	if err := f.pool.QueryRow(context.Background(), `SELECT provider_mode,provider_shop_id FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&orderMode, &orderShop); err != nil {
		t.Fatal(err)
	}
	if orderMode != "test" || orderShop != "123456" || quote.SDK.ShopID != "123456" || quote.SDK.Environment != "test" {
		t.Fatalf("native quote did not persist the test profile independently: order=%+v sdk=%+v", quote.Order, quote.SDK)
	}
	if quote.Order.PurchaseKind != "topup" {
		t.Fatalf("the native quote is not a test top-up: %+v", quote.Order)
	}
	orderID := quote.Order.ID
	if got := attachNativeToken(t, f, owner, orderID, "dual-profile-one-time-token"); got.Code != http.StatusOK {
		t.Fatalf("attach native test token status=%d: %s", got.Code, got.Body.String())
	}
	hostedReplay := f.request(t, owner.billingActor, http.MethodPost, "/api/v1/billing/topups", map[string]any{
		"amountMinor": 2900, "idempotencyKey": "3f49718b-59d1-40fc-a3d1-3a9dbe213149",
	})
	if hostedReplay.Code != http.StatusConflict {
		t.Fatalf("hosted checkout replayed a native order: status=%d body=%s", hostedReplay.Code, hostedReplay.Body.String())
	}
	var seenCreateOrder string
	testProvider.create = func(_ string, body []byte) (contracts.ProviderPayment, error) {
		var request struct {
			Amount   struct{ Value, Currency string } `json:"amount"`
			Metadata struct {
				OrderID string `json:"order_id"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return contracts.ProviderPayment{}, err
		}
		seenCreateOrder = request.Metadata.OrderID
		isTest := true
		return contracts.ProviderPayment{ID: testProviderID, Status: "pending", AmountValue: request.Amount.Value, Currency: request.Amount.Currency,
			OrderID: request.Metadata.OrderID, ShopID: "123456", Test: &isTest, PaymentMethodType: "bank_card"}, nil
	}
	testProvider.get = func(id string) (contracts.ProviderPayment, error) {
		isTest := true
		return contracts.ProviderPayment{ID: id, Status: "succeeded", Paid: true, AmountValue: "29.00", Currency: "RUB", OrderID: orderID,
			ShopID: "123456", Test: &isTest, PaymentMethodType: "bank_card"}, nil
	}

	// Change the active SDK profile after the quote/token are durable. The retry,
	// encrypted request AAD and worker route must remain attached to the order's
	// immutable test profile rather than following current configuration.
	if err := f.module.service.ConfigureNativeProfile("production", "654321"); err != nil {
		t.Fatalf("switch active SDK profile: %v", err)
	}
	replay := createNativeOrder(t, f, owner, "3f49718b-59d1-40fc-a3d1-3a9dbe213149", 2900)
	if replay.Order.ID != orderID || replay.SDK.Environment != "test" || replay.SDK.ShopID != "123456" {
		t.Fatalf("idempotent replay followed changed configuration: %+v", replay)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("persisted test profile CREATE=(%t,%v)", processed, err)
	}
	liveCreateKeys, _ := liveProvider.createCalls()
	if seenCreateOrder != orderID || len(liveCreateKeys) != 0 {
		t.Fatalf("CREATE routed by mutable config: test order=%q live calls=%v", seenCreateOrder, liveCreateKeys)
	}
	var hint contracts.ProviderNotification
	hint.Type, hint.Event, hint.Object.ID, hint.Object.Metadata.OrderID = "notification", "payment.succeeded", testProviderID, orderID
	if err := f.module.service.ReceiveNotification(context.Background(), hint); err != nil {
		t.Fatalf("ingest test-profile webhook hint after configuration switch: %v", err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("webhook-triggered persisted test profile GET=(%t,%v)", processed, err)
	}
	if len(testProvider.getCalls()) != 1 || len(liveProvider.getCalls()) != 0 {
		t.Fatalf("authoritative GET did not use persisted test profile: test=%v live=%v", testProvider.getCalls(), liveProvider.getCalls())
	}

	var status, fulfillment string
	var testCredits int
	if err := f.pool.QueryRow(context.Background(), `SELECT o.status,o.fulfillment_status,
        (SELECT count(*) FROM billing.wallet_ledger l WHERE l.order_id=o.id) FROM billing.orders o WHERE o.id=$1`, orderID).Scan(&status, &fulfillment, &testCredits); err != nil {
		t.Fatal(err)
	}
	balance, err := f.module.Balance(context.Background(), owner.id)
	if status != "succeeded" || fulfillment != "not_required" || testCredits != 0 || err != nil || balance.AvailableMinor != 1500 {
		t.Fatalf("test SDK order=%s/%s credits=%d wallet=%+v error=%v", status, fulfillment, testCredits, balance, err)
	}
	hosted := createYooKassaOrder(t, f, owner.billingActor, "a1f50295-7ecf-4c40-8d35-bc0fd886eb44", 2900)
	if hosted.Test || hosted.CheckoutKind != "redirect" || hosted.AmountMinor != 2900 || hosted.Status != "pending" {
		t.Fatalf("native test profile affected hosted live checkout: %+v", hosted)
	}
}

func TestNativeSDKDraftCancellationAndExpiredDraftCannotAttach(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native draft owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456"}
	f.configureYooKassaSDK(t, owner, provider)
	canceled := createNativeOrder(t, f, owner, "92454173-02c6-4317-8c0a-e6bb56f43e85", 2900)
	for i := 0; i < 2; i++ {
		response := f.sdkRequest(t, owner, http.MethodPost, "/api/v1/billing/native/orders/"+canceled.Order.ID+"/cancel", map[string]any{})
		if response.Code != http.StatusOK {
			t.Fatalf("cancel draft retry %d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	if got := attachNativeToken(t, f, owner, canceled.Order.ID, "cancelled-draft-token"); got.Code != http.StatusConflict {
		t.Fatalf("canceled quote accepted a token: %d %s", got.Code, got.Body.String())
	}
	expired := createNativeOrder(t, f, owner, "aa8c4df1-5604-4faa-9a59-648954c9fd35", 2900)
	forceSDKExpiry(t, f, expired.Order.ID, false)
	got := attachNativeToken(t, f, owner, expired.Order.ID, "too-late-token")
	if got.Code != http.StatusGone || !strings.Contains(got.Body.String(), "sdk_draft_expired") {
		t.Fatalf("expired draft accepted token: status=%d body=%s", got.Code, got.Body.String())
	}
	if processed, err := runPaymentWorker(t, f); err != nil || processed {
		t.Fatalf("worker for expired unattached draft=(%t,%v), expected no provider request", processed, err)
	}
	var status, reconciliation string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status FROM billing.orders WHERE id=$1`, expired.Order.ID).Scan(&status, &reconciliation); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" || reconciliation != "complete" {
		t.Fatalf("expired unstarted draft state=%s/%s", status, reconciliation)
	}
}

func TestNativeSDKRequestCiphertextCleanupCoversPendingManualAndTerminalOrders(t *testing.T) {
	f := newBillingFixture(t)
	owners := []sdkActor{
		newSDKActor(t, f, "SDK cleanup pending owner"),
		newSDKActor(t, f, "SDK cleanup manual owner"),
		newSDKActor(t, f, "SDK cleanup terminal owner"),
	}
	provider := &fakeYooKassa{mode: "production", shopID: "654321", paymentMethodType: "bank_card"}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	module, err := NewConfigured(f.pool, false, Config{Provider: "yookassa", Mode: "production", ShopID: "654321",
		PaymentProvider: provider, NativeSDKEnabled: true, MailEncryptionKey: key, SessionLease: auth.SessionLeaseTx}, f.accounts)
	if err != nil {
		t.Fatalf("configure production-mode fake SDK: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module, f.handler = module, mux
	orders := make([]contracts.Order, len(owners))
	for i, owner := range owners {
		quote := createNativeOrder(t, f, owner, fmt.Sprintf("9d8d3c2a-5b6e-4f70-8b9c-0d1e2f3a4b%02x", i+1), 2900)
		orders[i] = quote.Order
		if got := attachNativeToken(t, f, owner, quote.Order.ID, fmt.Sprintf("cleanup-token-unique-%d", i)); got.Code != http.StatusOK {
			t.Fatalf("attach cleanup order %d status=%d body=%s", i, got.Code, got.Body.String())
		}
		forceSDKExpiry(t, f, quote.Order.ID, true)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET reconciliation_status='pending',next_reconcile_at=now() WHERE id=$1`, orders[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET reconciliation_status='manual_review',last_error_code='payment_create_uncertain' WHERE id=$1`, orders[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete' WHERE id=$1`, orders[2].ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || processed {
		t.Fatalf("expiry cleanup worker=(%t,%v), no remote request should be eligible", processed, err)
	}
	for _, order := range orders {
		var retained bool
		if err := f.pool.QueryRow(context.Background(), `SELECT encrypted_request IS NOT NULL FROM billing.sdk_payment_requests WHERE order_id=$1`, order.ID).Scan(&retained); err != nil {
			t.Fatal(err)
		}
		if retained {
			t.Fatalf("expired SDK ciphertext retained for order %s", order.ID)
		}
	}
	var pendingStatus, manualState, terminalStatus, terminalReconciliation string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status FROM billing.orders WHERE id=$1`, orders[0].ID).Scan(&pendingStatus, &manualState); err != nil {
		t.Fatal(err)
	}
	if pendingStatus != "canceled" || manualState != "complete" {
		t.Fatalf("expired no-attempt order not terminally canceled: %s/%s", pendingStatus, manualState)
	}
	var manualStatus, manualReconciliation string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status FROM billing.orders WHERE id=$1`, orders[1].ID).Scan(&manualStatus, &manualReconciliation); err != nil {
		t.Fatal(err)
	}
	if manualStatus != "pending" || manualReconciliation != "manual_review" {
		t.Fatalf("cipher cleanup changed existing manual review: %s/%s", manualStatus, manualReconciliation)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status FROM billing.orders WHERE id=$1`, orders[2].ID).Scan(&terminalStatus, &terminalReconciliation); err != nil {
		t.Fatal(err)
	}
	if terminalStatus != "canceled" || terminalReconciliation != "complete" {
		t.Fatalf("cipher cleanup changed terminal order: %s/%s", terminalStatus, terminalReconciliation)
	}
	if keys, _ := provider.createCalls(); len(keys) != 0 {
		t.Fatalf("expired request unexpectedly reached provider: %v", keys)
	}
}

func TestNativeSDKAmbiguousCreateStopsAtRequestCutoffAndLateWebhookCannotRevive(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native uncertain checkout owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456", paymentMethodType: "bank_card"}
	provider.create = func(key string, body []byte) (contracts.ProviderPayment, error) {
		return contracts.ProviderPayment{}, errors.New("connection dropped after provider may have accepted request")
	}
	f.configureYooKassaSDK(t, owner, provider)
	quote := createNativeOrder(t, f, owner, "c723d2e1-55bf-4a28-aaf9-21f031105ff0", 2900)
	if got := attachNativeToken(t, f, owner, quote.Order.ID, "uncertain-sdk-attempt-token"); got.Code != http.StatusOK {
		t.Fatalf("attach=%d %s", got.Code, got.Body.String())
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("first uncertain create=(%t,%v)", processed, err)
	}
	var status, reconciliation, lastError string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,last_error_code FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&status, &reconciliation, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || reconciliation != "pending" || lastError != "payment_provider_unavailable" {
		t.Fatalf("uncertain first attempt state=%s/%s/%s", status, reconciliation, lastError)
	}
	forceSDKExpiry(t, f, quote.Order.ID, true)
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, quote.Order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || processed {
		t.Fatalf("expired request worker=(%t,%v), should stop before provider call", processed, err)
	}
	var createCalls int
	if keys, _ := provider.createCalls(); len(keys) > createCalls {
		createCalls = len(keys)
	}
	if createCalls != 1 {
		t.Fatalf("expired single-use SDK request was resent %d times", createCalls)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,last_error_code FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&status, &reconciliation, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || reconciliation != "manual_review" || lastError != "payment_create_uncertain" {
		t.Fatalf("expired ambiguous request was canceled/retried: %s/%s/%s", status, reconciliation, lastError)
	}
	var ciphertext []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT encrypted_request FROM billing.sdk_payment_requests WHERE order_id=$1`, quote.Order.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if ciphertext != nil {
		t.Fatal("expired request ciphertext retained beyond provider idempotency window")
	}
	// A webhook is only a hint: the provider's current payment lookup is still authoritative.
	provider.get = func(id string) (contracts.ProviderPayment, error) {
		mode := true
		return contracts.ProviderPayment{ID: id, Status: "pending", AmountValue: "29.00", Currency: "RUB", OrderID: quote.Order.ID, ShopID: "123456", Test: &mode, PaymentMethodType: "bank_card"}, nil
	}
	notice := fmt.Sprintf(`{"type":"notification","event":"payment.succeeded","object":{"id":"%s","metadata":{"order_id":"%s"}}}`, testProviderID, quote.Order.ID)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/billing/yookassa", strings.NewReader(notice))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("late SDK webhook hint status=%d body=%s", rec.Code, rec.Body.String())
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("late webhook provider lookup=(%t,%v)", processed, err)
	}
	if got := provider.getCalls(); len(got) != 1 || got[0] != testProviderID {
		t.Fatalf("late hint did not trigger provider lookup: %v", got)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,last_error_code FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&status, &reconciliation, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || reconciliation != "pending" || lastError != "" {
		t.Fatalf("authoritative pending lookup did not bind the discovered payment: %s/%s/%s", status, reconciliation, lastError)
	}
	var paymentID string
	if err := f.pool.QueryRow(context.Background(), `SELECT provider_payment_id FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if paymentID != testProviderID {
		t.Fatalf("untrusted webhook ID was not validated/bound by provider lookup: %q", paymentID)
	}
	var walletCredits int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.wallet_ledger WHERE account_id=$1 AND kind='top_up'`, owner.id).Scan(&walletCredits); err != nil {
		t.Fatal(err)
	}
	if walletCredits != 0 {
		t.Fatalf("webhook hint fulfilled an order despite provider reporting pending: %d", walletCredits)
	}
}

func TestNativeSDKDefinitiveProviderRejectionCancelsOnlyFirstAttempt(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native rejected token owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456", paymentMethodType: "bank_card"}
	provider.create = func(string, []byte) (contracts.ProviderPayment, error) {
		return contracts.ProviderPayment{}, yookassa.ErrPaymentTokenRejected
	}
	f.configureYooKassaSDK(t, owner, provider)
	quote := createNativeOrder(t, f, owner, "0db2fa7f-c78f-4e1e-a0d7-b31e441ec999", 2900)
	if got := attachNativeToken(t, f, owner, quote.Order.ID, "definitively-rejected-token"); got.Code != http.StatusOK {
		t.Fatalf("attach=%d %s", got.Code, got.Body.String())
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("rejected create=(%t,%v)", processed, err)
	}
	var status, reconciliation, lastError string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,last_error_code FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&status, &reconciliation, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" || reconciliation != "complete" || lastError != "payment_token_rejected" {
		t.Fatalf("definitive first 4xx was not terminal: %s/%s/%s", status, reconciliation, lastError)
	}
	var ciphertext []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT encrypted_request FROM billing.sdk_payment_requests WHERE order_id=$1`, quote.Order.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if ciphertext != nil {
		t.Fatal("rejected token request was retained after definite provider rejection")
	}
	if got := attachNativeToken(t, f, owner, quote.Order.ID, "replacement-token-after-terminal-rejection"); got.Code != http.StatusConflict {
		t.Fatalf("terminal token rejection was reopened via attach: %d %s", got.Code, got.Body.String())
	}
}

func TestNativeSDKDefinitive4xxAfterAmbiguousAttemptRequiresManualReview(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native uncertain then rejected owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456", paymentMethodType: "bank_card"}
	provider.create = func(string, []byte) (contracts.ProviderPayment, error) {
		return contracts.ProviderPayment{}, errors.New("response lost")
	}
	f.configureYooKassaSDK(t, owner, provider)
	quote := createNativeOrder(t, f, owner, "a9a9db42-6dbc-4a8a-81af-2bc066ab2087", 2900)
	if got := attachNativeToken(t, f, owner, quote.Order.ID, "one-shot-token-with-ambiguous-first-result"); got.Code != http.StatusOK {
		t.Fatalf("attach=%d %s", got.Code, got.Body.String())
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("ambiguous create=(%t,%v)", processed, err)
	}
	provider.create = func(string, []byte) (contracts.ProviderPayment, error) {
		return contracts.ProviderPayment{}, yookassa.ErrRequestRejected
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, quote.Order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("second create 4xx=(%t,%v)", processed, err)
	}
	var status, reconciliation, lastError string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,last_error_code FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&status, &reconciliation, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || reconciliation != "manual_review" || lastError != "payment_create_uncertain" {
		t.Fatalf("4xx after ambiguous first attempt erased uncertainty: %s/%s/%s", status, reconciliation, lastError)
	}
	var closedCiphertext []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT encrypted_request FROM billing.sdk_payment_requests WHERE order_id=$1`, quote.Order.ID).Scan(&closedCiphertext); err != nil {
		t.Fatal(err)
	}
	if closedCiphertext != nil {
		t.Fatal("manual review retained the one-time SDK create body")
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, quote.Order.ID); err != nil {
		t.Fatal(err)
	}
	postTimeout := true
	provider.get = func(string) (contracts.ProviderPayment, error) {
		if postTimeout {
			postTimeout = false
			return contracts.ProviderPayment{}, context.DeadlineExceeded
		}
		return contracts.ProviderPayment{}, yookassa.ErrNotFound
	}
	notice := fmt.Sprintf(`{"type":"notification","event":"payment.succeeded","object":{"id":"%s","metadata":{"order_id":"%s"}}}`, testProviderID, quote.Order.ID)
	sendSDKWebhookHint(t, f, notice)
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("hinted provider timeout=(%t,%v)", processed, err)
	}
	assertSDKManualReview := func(label string) {
		t.Helper()
		if err := f.pool.QueryRow(context.Background(), `SELECT status,reconciliation_status,last_error_code FROM billing.orders WHERE id=$1`, quote.Order.ID).Scan(&status, &reconciliation, &lastError); err != nil {
			t.Fatal(err)
		}
		if status != "pending" || reconciliation != "manual_review" || lastError != "payment_create_uncertain" {
			t.Fatalf("%s left quarantined SDK order: %s/%s/%s", label, status, reconciliation, lastError)
		}
	}
	assertSDKManualReview("transient provider GET")
	// A second, distinct event is another untrusted hint; a provider 404 must reject
	// that evidence and keep CREATE closed rather than reconstructing a quote body.
	notice = fmt.Sprintf(`{"type":"notification","event":"payment.canceled","object":{"id":"%s","metadata":{"order_id":"%s"}}}`, testProviderID, quote.Order.ID)
	sendSDKWebhookHint(t, f, notice)
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET next_reconcile_at=now() WHERE id=$1`, quote.Order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := runPaymentWorker(t, f); err != nil || !processed {
		t.Fatalf("rejected hinted provider lookup=(%t,%v)", processed, err)
	}
	assertSDKManualReview("rejected provider hint")
	keys, bodies := provider.createCalls()
	if len(keys) != 2 || keys[0] != quote.Order.ID || keys[1] != quote.Order.ID || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("closed SDK request was recreated or mutated after webhook retries: keys=%v bodies=%q", keys, bodies)
	}
	var rejectedHints, unresolvedHints int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE result='rejected'),count(*) FILTER (WHERE processed_at IS NULL) FROM billing.webhook_inbox WHERE order_id=$1`, quote.Order.ID).Scan(&rejectedHints, &unresolvedHints); err != nil {
		t.Fatal(err)
	}
	if rejectedHints != 2 || unresolvedHints != 0 {
		t.Fatalf("provider rejected hints not quarantined: rejected=%d pending=%d", rejectedHints, unresolvedHints)
	}
}

func sendSDKWebhookHint(t *testing.T, f *billingFixture, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/billing/yookassa", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("SDK webhook hint status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNativeSDKRevokedSessionCannotAttachEvenWithValidOrder(t *testing.T) {
	f := newBillingFixture(t)
	owner := newSDKActor(t, f, "Native session revocation owner")
	provider := &fakeYooKassa{mode: "test", shopID: "123456"}
	f.configureYooKassaSDK(t, owner, provider)
	quote := createNativeOrder(t, f, owner, "0eebdce9-1cba-458f-8c6e-3cf89181bfbf", 2900)
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, owner.sessionID); err != nil {
		t.Fatal(err)
	}
	if got := attachNativeToken(t, f, owner, quote.Order.ID, "post-revocation-token"); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked native session attached to payment: status=%d body=%s", got.Code, got.Body.String())
	}
	// Also exercise the service-side lease fence against a Principal captured before revocation.
	principal := authcontracts.Principal{AccountID: owner.id, SessionID: owner.sessionID, ClientKind: "android", TokenHash: func() []byte { sum := sha256.Sum256([]byte(owner.bearer)); return sum[:] }()}
	_, err := f.module.service.AttachNativeToken(context.Background(), principal, quote.Order.ID, "captured-principal-token", "BANK_CARD")
	if !errors.Is(err, service.ErrNativeSession) {
		t.Fatalf("revoked captured principal error=%v, want native session fence", err)
	}
}
