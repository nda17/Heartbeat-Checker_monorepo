package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/service"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/migrations"
)

const billingTestOrigin = "http://localhost:3000"

type billingFixture struct {
	pool     *pgxpool.Pool
	accounts *accounts.Module
	module   *Module
	handler  http.Handler
}

type billingActor struct {
	id     string
	cookie *http.Cookie
	csrf   string
}

type billingCaptcha struct{}

func (billingCaptcha) Verify(context.Context, string, string, string) error { return nil }

type billingMailer struct{}

func (billingMailer) Send(context.Context, string, string, authcontracts.MailContent) error {
	return nil
}

func newBillingFixture(t *testing.T) *billingFixture {
	t.Helper()
	adminURL := os.Getenv("HEARTBEAT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set HEARTBEAT_TEST_DATABASE_URL to the isolated local PostgreSQL server URL")
	}
	u, err := url.Parse(adminURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host != "127.0.0.1:55432" {
		t.Fatal("HEARTBEAT_TEST_DATABASE_URL must target local Compose PostgreSQL at 127.0.0.1:55432")
	}
	admin, err := pgx.Connect(context.Background(), adminURL)
	if err != nil {
		t.Fatalf("connect to local test PostgreSQL: %v", err)
	}
	dbName := fmt.Sprintf("hc_billing_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+dbName); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("create isolated billing test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()", dbName); err != nil {
			t.Errorf("terminate isolated billing test connections: %v", err)
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName); err != nil {
			t.Errorf("drop isolated billing test database: %v", err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})
	u.Path = "/" + dbName
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("parse isolated billing test database URL: %v", err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect to isolated billing test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(context.Background(), pool); err != nil {
		t.Fatalf("apply billing test migrations: %v", err)
	}
	accountModule := accounts.New(pool)
	module, err := NewConfigured(pool, false, Config{Provider: "mock"}, accountModule)
	if err != nil {
		t.Fatalf("create billing module: %v", err)
	}
	authModule, err := auth.New(pool, accountModule, billingCaptcha{}, billingMailer{}, auth.Config{Origin: billingTestOrigin})
	if err != nil {
		t.Fatalf("create auth module: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authModule.Protect)
	return &billingFixture{pool: pool, accounts: accountModule, module: module, handler: mux}
}

func (f *billingFixture) newActor(t *testing.T, name string) billingActor {
	t.Helper()
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	account, err := f.accounts.Create(context.Background(), tx, name)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("create test account: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit test account: %v", err)
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		t.Fatal(err)
	}
	rawToken := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	tokenHash := sha256.Sum256([]byte(rawToken))
	sessionID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(context.Background(), `INSERT INTO auth.credentials(account_id,email,password_hash,version) VALUES($1,$2,'test-only-hash',1)`, account.ID, "billing-"+account.ID+"@example.test")
	if err != nil {
		t.Fatalf("insert test credential: %v", err)
	}
	_, err = f.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,user_agent) VALUES($1,$2,$3,1,now()+interval '1 hour','billing integration test')`, sessionID, account.ID, tokenHash[:])
	if err != nil {
		t.Fatalf("insert test session: %v", err)
	}
	cookie := &http.Cookie{Name: "hc_session", Value: rawToken, Path: "/"}
	return billingActor{id: account.ID, cookie: cookie, csrf: csrfToken(t, f.pool, account.ID, rawToken)}
}

func csrfToken(t *testing.T, pool *pgxpool.Pool, accountID, rawToken string) string {
	t.Helper()
	// A same-origin protected request validates the token against this session's
	// derived CSRF value; obtain it through the auth endpoint on a small module.
	accountsModule := accounts.New(pool)
	authModule, err := auth.New(pool, accountsModule, billingCaptcha{}, billingMailer{}, auth.Config{Origin: billingTestOrigin})
	if err != nil {
		t.Fatalf("create auth module for CSRF token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	req.AddCookie(&http.Cookie{Name: "hc_session", Value: rawToken, Path: "/"})
	rec := httptest.NewRecorder()
	authModule.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch billing test CSRF token: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.CSRFToken == "" {
		t.Fatalf("decode CSRF token: %v", err)
	}
	return body.CSRFToken
}

func (f *billingFixture) request(t *testing.T, actor billingActor, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytesReader(encoded))
	req.AddCookie(actor.cookie)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", billingTestOrigin)
		req.Header.Set("X-CSRF-Token", actor.csrf)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *billingFixture) requestNative(t *testing.T, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func nativeBillingToken(t *testing.T, f *billingFixture, accountID string) string {
	t.Helper()
	var tokenBytes [32]byte
	var revokeBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(revokeBytes[:]); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	tokenHash := sha256.Sum256([]byte(token))
	revokeToken := base64.RawURLEncoding.EncodeToString(revokeBytes[:])
	revokeHash := sha256.Sum256([]byte(revokeToken))
	sessionID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.sessions
		(id,account_id,token_hash,credential_version,expires_at,user_agent,client_kind,revoke_token_hash)
		VALUES($1,$2,$3,1,now()+interval '1 hour','billing native history test','android',$4)`,
		sessionID, accountID, tokenHash[:], revokeHash[:]); err != nil {
		t.Fatalf("insert native billing history session: %v", err)
	}
	return token
}

func seedBillingHistory(t *testing.T, f *billingFixture, accountID string, count int) {
	t.Helper()
	base := time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC)
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO billing.orders
			(id,account_id,idempotency_key,provider,status,monitor_count,amount_minor,currency,
			 created_at,expires_at,completed_at,provider_mode,reconciliation_status,
			 fulfillment_status,purchase_kind,return_target,checkout_kind)
		SELECT md5($1::text || ':order:' || n)::uuid, $1::uuid,
			md5($1::text || ':key:' || n)::uuid, 'mock', 'canceled', 1, 2400, 'RUB',
			$2::timestamptz - ((n - 1) / 25) * interval '1 second',
			$2::timestamptz + interval '30 minutes',
			$2::timestamptz - ((n - 1) / 25) * interval '1 second',
			'test', 'complete', 'not_required', 'legacy', 'web', 'redirect'
		FROM generate_series(1, $3::integer) AS n`, accountID, base, count)
	if err != nil {
		t.Fatalf("seed synthetic billing history for %s: %v", accountID, err)
	}
}

func billingOrderIDs(page contracts.OrderPage) []string {
	ids := make([]string, len(page.Orders))
	for i, order := range page.Orders {
		ids[i] = order.ID
	}
	return ids
}

func decodeBillingOrderPage(t *testing.T, response *httptest.ResponseRecorder) contracts.OrderPage {
	t.Helper()
	var result contracts.OrderPage
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode billing order page: %v (%s)", err, response.Body.String())
	}
	return result
}

func TestBillingOrderHistoryPaginationIsOrderedBoundedAndAccountScoped(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Billing history owner")
	other := f.newActor(t, "Billing history other")
	empty := f.newActor(t, "Billing history empty")
	seedBillingHistory(t, f, owner.id, 1003)
	seedBillingHistory(t, f, other.id, 3)
	nativeToken := nativeBillingToken(t, f, owner.id)

	firstResponse := f.request(t, owner, http.MethodGet, "/api/v1/billing/orders?page=1&pageSize=10", nil)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first order page: %d %s", firstResponse.Code, firstResponse.Body.String())
	}
	first := decodeBillingOrderPage(t, firstResponse)
	if first.Pagination != (contracts.Pagination{Page: 1, PageSize: 10, Total: 1003, TotalPages: 101}) {
		t.Fatalf("unexpected first page metadata: %+v", first.Pagination)
	}
	if len(first.Orders) != 10 {
		t.Fatalf("first page has %d orders, want 10", len(first.Orders))
	}

	seen := make(map[string]bool, 1003)
	all := make([]contracts.Order, 0, 1003)
	crossPageTie := false
	var previous contracts.Order
	for pageNumber := 1; pageNumber <= first.Pagination.TotalPages; pageNumber++ {
		response := firstResponse
		if pageNumber > 1 {
			response = f.request(t, owner, http.MethodGet,
				fmt.Sprintf("/api/v1/billing/orders?page=%d&pageSize=10", pageNumber), nil)
		}
		if response.Code != http.StatusOK {
			t.Fatalf("order page %d: %d %s", pageNumber, response.Code, response.Body.String())
		}
		current := decodeBillingOrderPage(t, response)
		if current.Pagination.Page != pageNumber || current.Pagination.PageSize != 10 || current.Pagination.Total != 1003 {
			t.Fatalf("page %d has inconsistent metadata: %+v", pageNumber, current.Pagination)
		}
		if pageNumber < current.Pagination.TotalPages && len(current.Orders) != 10 {
			t.Fatalf("non-final page %d has %d rows", pageNumber, len(current.Orders))
		}
		for _, order := range current.Orders {
			if seen[order.ID] {
				t.Fatalf("order %s appeared on multiple pages", order.ID)
			}
			seen[order.ID] = true
			if len(all) > 0 {
				if previous.CreatedAt.Before(order.CreatedAt) {
					t.Fatalf("createdAt order increased at boundary: %s then %s", previous.CreatedAt, order.CreatedAt)
				}
				if previous.CreatedAt.Equal(order.CreatedAt) {
					crossPageTie = crossPageTie || len(all)%10 == 0
					if previous.ID <= order.ID {
						t.Fatalf("tied createdAt rows are not ID-descending: %s then %s", previous.ID, order.ID)
					}
				}
			}
			previous = order
			all = append(all, order)
		}
	}
	if len(all) != 1003 || len(seen) != 1003 {
		t.Fatalf("collected %d unique orders, want 1003", len(seen))
	}
	if !crossPageTie {
		t.Fatal("fixture did not exercise equal timestamps across a page boundary")
	}

	// Both web-cookie and Android-bearer sessions must see the same owner-bound page.
	nativeResponse := f.requestNative(t, nativeToken, "/api/v1/billing/orders?page=1&pageSize=10")
	if nativeResponse.Code != http.StatusOK {
		t.Fatalf("native order page: %d %s", nativeResponse.Code, nativeResponse.Body.String())
	}
	if native := decodeBillingOrderPage(t, nativeResponse); strings.Join(billingOrderIDs(native), ",") != strings.Join(billingOrderIDs(first), ",") {
		t.Fatalf("native page differs from the web owner's page: native=%v web=%v", billingOrderIDs(native), billingOrderIDs(first))
	}

	otherResponse := f.request(t, other, http.MethodGet, "/api/v1/billing/orders?page=1&pageSize=10", nil)
	if otherResponse.Code != http.StatusOK {
		t.Fatalf("other account order page: %d %s", otherResponse.Code, otherResponse.Body.String())
	}
	otherPage := decodeBillingOrderPage(t, otherResponse)
	if otherPage.Pagination.Total != 3 || len(otherPage.Orders) != 3 {
		t.Fatalf("other account sees total=%d rows=%d, want 3", otherPage.Pagination.Total, len(otherPage.Orders))
	}
	for _, order := range otherPage.Orders {
		if seen[order.ID] {
			t.Fatalf("other account received owner order %s", order.ID)
		}
	}

	maxResponse := f.request(t, owner, http.MethodGet, "/api/v1/billing/orders?page=1&pageSize=100", nil)
	if maxResponse.Code != http.StatusOK {
		t.Fatalf("maximum page size: %d %s", maxResponse.Code, maxResponse.Body.String())
	}
	maxPage := decodeBillingOrderPage(t, maxResponse)
	if len(maxPage.Orders) != 100 || maxPage.Pagination.PageSize != 100 || maxPage.Pagination.TotalPages != 11 {
		t.Fatalf("unexpected maximum page size response: rows=%d pagination=%+v", len(maxPage.Orders), maxPage.Pagination)
	}
	clampedResponse := f.request(t, owner, http.MethodGet, "/api/v1/billing/orders?page=9999&pageSize=10", nil)
	if clampedResponse.Code != http.StatusOK {
		t.Fatalf("out-of-range page is clamped: %d %s", clampedResponse.Code, clampedResponse.Body.String())
	}
	clamped := decodeBillingOrderPage(t, clampedResponse)
	if clamped.Pagination.Page != 101 || len(clamped.Orders) != 3 {
		t.Fatalf("unexpected clamped page: pagination=%+v rows=%d", clamped.Pagination, len(clamped.Orders))
	}

	emptyResponse := f.request(t, empty, http.MethodGet, "/api/v1/billing/orders?page=50&pageSize=10", nil)
	if emptyResponse.Code != http.StatusOK {
		t.Fatalf("empty history page: %d %s", emptyResponse.Code, emptyResponse.Body.String())
	}
	emptyPage := decodeBillingOrderPage(t, emptyResponse)
	if emptyPage.Pagination != (contracts.Pagination{Page: 1, PageSize: 10, Total: 0, TotalPages: 1}) || len(emptyPage.Orders) != 0 {
		t.Fatalf("unexpected empty page: %+v rows=%d", emptyPage.Pagination, len(emptyPage.Orders))
	}

	for _, query := range []string{
		"page=0", "page=-1", "page=nope", "page=1&page=2", "pageSize=0",
		"pageSize=101", "pageSize=999999999999999999999", "unused=1",
		"page=9223372036854775807&pageSize=100",
	} {
		response := f.request(t, owner, http.MethodGet, "/api/v1/billing/orders?"+query, nil)
		if response.Code != http.StatusBadRequest {
			t.Errorf("query %q returned %d, want 400: %s", query, response.Code, response.Body.String())
		}
	}

	unauthenticated := httptest.NewRecorder()
	f.handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/v1/billing/orders", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history read returned %d, want 401", unauthenticated.Code)
	}

	legacyResponse := f.request(t, owner, http.MethodGet, "/api/v1/billing", nil)
	if legacyResponse.Code != http.StatusOK {
		t.Fatalf("legacy billing overview: %d %s", legacyResponse.Code, legacyResponse.Body.String())
	}
	var legacy struct {
		Orders []contracts.Order `json:"orders"`
	}
	if err := json.Unmarshal(legacyResponse.Body.Bytes(), &legacy); err != nil {
		t.Fatalf("decode legacy overview: %v", err)
	}
	if len(legacy.Orders) != 100 || legacy.Orders[0].ID != first.Orders[0].ID {
		t.Fatalf("legacy overview shape/order changed: %d orders, first=%q", len(legacy.Orders), legacy.Orders[0].ID)
	}
}

func bytesReader(b []byte) *strings.Reader { return strings.NewReader(string(b)) }

func configureMockBilling(t *testing.T, f *billingFixture, owner billingActor) *Module {
	t.Helper()
	module, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatalf("configure mock billing for account: %v", err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module, f.handler = module, mux
	return module
}

func currentBillingEntitlement(t *testing.T, f *billingFixture, owner billingActor) contracts.Entitlement {
	t.Helper()
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	entitlement, err := f.module.service.Lock(context.Background(), tx, owner.id)
	if err != nil {
		t.Fatalf("read current entitlement: %v", err)
	}
	return entitlement
}

func billingPackage(t *testing.T, entitlement contracts.Entitlement, id string) contracts.Subscription {
	t.Helper()
	for _, part := range entitlement.Packages {
		if part.ID == id {
			return part
		}
	}
	t.Fatalf("package %q missing from entitlement %+v", id, entitlement.Packages)
	return contracts.Subscription{}
}

func TestBillingMockOrderOwnershipAllowlistAndAuth(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Billing Owner")
	other := f.newActor(t, "Other Billing Owner")
	key := "b4a2d060-379b-4c95-9c7e-04db177835c7"
	create := func(actor billingActor, count int, idem string) *httptest.ResponseRecorder {
		return f.request(t, actor, http.MethodPost, "/api/v1/billing/orders", map[string]any{"monitorCount": count, "idempotencyKey": idem, "priceVersion": 1})
	}
	if got := create(owner, 1, key); got.Code != http.StatusForbidden {
		t.Fatalf("empty allowlist create status=%d, want 403: %s", got.Code, got.Body.String())
	}
	if got := f.request(t, owner, http.MethodGet, "/api/v1/billing", nil); got.Code != http.StatusOK {
		t.Fatalf("authenticated overview status=%d", got.Code)
	}
	unauth := httptest.NewRecorder()
	f.handler.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/v1/billing", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated overview status=%d, want 401", unauth.Code)
	}
	allowed, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id, other.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	allowed.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module = allowed
	f.handler = mux
	created := create(owner, 2, key)
	if created.Code != http.StatusCreated {
		t.Fatalf("allowlisted order status=%d: %s", created.Code, created.Body.String())
	}
	var payload struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil || payload.Order.ID == "" {
		t.Fatalf("decode created order: %v", err)
	}
	foreign := f.request(t, other, http.MethodPost, "/api/v1/billing/orders/"+payload.Order.ID+"/simulate", map[string]string{"outcome": "succeeded"})
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign order simulation status=%d, want 404", foreign.Code)
	}
	if _, err := f.module.service.Simulate(context.Background(), other.id, payload.Order.ID, "succeeded"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("foreign account simulation error=%v, want not found", err)
	}
}

func authProtect(t *testing.T, pool *pgxpool.Pool, accountModule *accounts.Module) func(http.Handler) http.Handler {
	t.Helper()
	m, err := auth.New(pool, accountModule, billingCaptcha{}, billingMailer{}, auth.Config{Origin: billingTestOrigin})
	if err != nil {
		t.Fatalf("create auth middleware: %v", err)
	}
	return m.Protect
}

func TestBillingConcurrentIdempotencySingleExtensionAndReport(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Concurrent Billing Owner")
	module, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	key := "6c1492bd-84d9-4018-8c5e-629feae06d75"
	const n = 12
	orders := make([]contracts.Order, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range orders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			orders[i], errs[i] = module.service.CreateOrder(context.Background(), owner.id, 3, key, 1)
		}(i)
	}
	wg.Wait()
	orderID := orders[0].ID
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent create %d: %v", i, err)
		}
		if orders[i].ID != orderID {
			t.Fatalf("concurrent create %d returned order %s, want %s", i, orders[i].ID, orderID)
		}
	}
	var sims sync.WaitGroup
	simErrors := make([]error, n)
	for i := range simErrors {
		sims.Add(1)
		go func(i int) {
			defer sims.Done()
			_, simErrors[i] = module.service.Simulate(context.Background(), owner.id, orderID, "succeeded")
		}(i)
	}
	sims.Wait()
	for i, err := range simErrors {
		if err != nil {
			t.Fatalf("concurrent simulation %d: %v", i, err)
		}
	}
	var version, reportCount, orderCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT version FROM billing.entitlements WHERE account_id=$1`, owner.id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.payment_reports WHERE order_id=$1`, orderID).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.orders WHERE account_id=$1`, owner.id).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if version != 1 || reportCount != 1 || orderCount != 1 {
		t.Fatalf("after concurrent completion entitlement version=%d report rows=%d orders=%d; want 1/1/1", version, reportCount, orderCount)
	}
	if _, err := module.service.CreateOrder(context.Background(), owner.id, 4, key, 1); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("idempotency key with changed amount error=%v, want conflict", err)
	}
}

func TestBillingReturnTargetIsIdempotentAndImmutable(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Return Target Owner")
	module := configureMockBilling(t, f, owner)
	ctx := context.Background()
	web, err := module.service.CreateOrder(ctx, owner.id, 1, "bf66cf6c-8867-4a8f-a8e8-050073ca1f0a", 1)
	if err != nil {
		t.Fatalf("create default-target order: %v", err)
	}
	if web.ReturnTarget != "web" {
		t.Fatalf("omitted return target = %q, want web", web.ReturnTarget)
	}
	androidOwner := f.newActor(t, "Android Return Target Owner")
	androidModule, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{androidOwner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	const androidKey = "e5d3b609-836c-46f3-92eb-0debedb1ab4a"
	android, err := androidModule.service.CreateSelectedOrderWithTarget(ctx, androidOwner.id, 2, androidKey, 1, "", "", "android")
	if err != nil {
		t.Fatalf("create Android-target order: %v", err)
	}
	if android.ReturnTarget != "android" {
		t.Fatalf("Android order return target = %q", android.ReturnTarget)
	}
	retry, err := androidModule.service.CreateSelectedOrderWithTarget(ctx, androidOwner.id, 2, androidKey, 1, "", "", "android")
	if err != nil || retry.ID != android.ID || retry.ReturnTarget != "android" {
		t.Fatalf("identical Android retry = (%+v, %v), want same immutable order", retry, err)
	}
	if _, err := androidModule.service.CreateSelectedOrderWithTarget(ctx, androidOwner.id, 2, androidKey, 1, "", "", "web"); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("changing return target under same idempotency key error=%v, want conflict", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE billing.orders SET return_target='web' WHERE id=$1`, android.ID); err == nil {
		t.Fatal("database allowed return target to change after order creation")
	}
}

func TestBillingActiveAccountExpiredOrderAndLocalTestIsolation(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Blocked Billing Owner")
	if _, err := f.pool.Exec(context.Background(), `UPDATE accounts.accounts SET status='blocked' WHERE id=$1`, owner.id); err != nil {
		t.Fatal(err)
	}
	module, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.CreateOrder(context.Background(), owner.id, 1, "a3afeb3c-3959-4f9b-96d1-9cb16c910f3f", 1); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("blocked account create error=%v, want forbidden", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE accounts.accounts SET status='active' WHERE id=$1`, owner.id); err != nil {
		t.Fatal(err)
	}
	order, err := module.service.CreateOrder(context.Background(), owner.id, 1, "a3afeb3c-3959-4f9b-96d1-9cb16c910f3f", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.orders SET expires_at=now()-interval '1 second' WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	result, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded")
	if err != nil || result.Status != "canceled" {
		t.Fatalf("expired order result=%+v error=%v, want canceled", result, err)
	}
	local, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(f.pool, true).GrantLocal(context.Background(), owner.id, 5, time.Now().Add(time.Hour), "billing integration"); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entitlement, err := local.service.Lock(context.Background(), tx, owner.id)
	_ = tx.Rollback(context.Background())
	if err != nil || entitlement.Quota != 0 {
		t.Fatalf("production mock local_test quota=%d err=%v, want 0", entitlement.Quota, err)
	}
}

func TestAdminGrantVersionAndPendingMockOrderCannotReplaceGrant(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Administrative Grant Billing Owner")
	module, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	order, err := module.service.CreateOrder(context.Background(), owner.id, 1, "cf8f7971-d583-4f06-9056-5a754de130cf", 1)
	if err != nil {
		t.Fatalf("create pending mock order before grant: %v", err)
	}

	grantUntil := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := module.GrantAdminTx(context.Background(), tx, owner.id, 0, 7, grantUntil, "approved administrative entitlement")
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("grant entitlement at initial version: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if grant.Source != "admin_grant" || grant.Quota != 7 || grant.Version != 1 {
		t.Fatalf("grant=%+v, want active admin_grant quota 7 version 1", grant)
	}

	// Mock billing stays blocked for admin-grant accounts. A delayed test
	// payment cannot replace or alter the administrative decision.
	if _, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("pending mock payment with admin grant error=%v, want forbidden", err)
	}
	var orderStatus, source string
	var quota, reportCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM billing.orders WHERE id=$1`, order.ID).Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT monitor_limit FROM billing.admin_grants WHERE account_id=$1`, owner.id).Scan(&quota); err != nil {
		t.Fatal(err)
	}
	source = "admin_grant"
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.payment_reports WHERE order_id=$1`, order.ID).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if orderStatus != "pending" || source != "admin_grant" || quota != 7 || reportCount != 0 {
		t.Fatalf("blocked mock payment changed state: order=%s source=%s quota=%d reports=%d", orderStatus, source, quota, reportCount)
	}
	if _, err := module.service.CreateOrder(context.Background(), owner.id, 7, "09f96250-84ca-447a-9a67-27db06fb6750", 1); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("mock order while admin grant active error=%v, want forbidden", err)
	}
	tx, err = f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = module.GrantAdminTx(context.Background(), tx, owner.id, 0, 9, grantUntil, "stale replacement")
	_ = tx.Rollback(context.Background())
	if !errors.Is(err, service.ErrAdminGrantConflict) {
		t.Fatalf("stale grant version error=%v, want conflict", err)
	}
	tx, err = f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := module.ExpireAdminTx(context.Background(), tx, owner.id, grant.Version, "grant period ended"); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("expire current admin grant: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	tx, err = f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = module.ExpireAdminTx(context.Background(), tx, owner.id, grant.Version, "duplicate stale revoke")
	_ = tx.Rollback(context.Background())
	if !errors.Is(err, service.ErrAdminGrantConflict) {
		t.Fatalf("stale revoke version error=%v, want conflict", err)
	}
	var expired bool
	if err := f.pool.QueryRow(context.Background(), `SELECT valid_until<=now() FROM billing.admin_grants WHERE account_id=$1`, owner.id).Scan(&expired); err != nil || !expired {
		t.Fatalf("admin grant expiry state=%t err=%v", expired, err)
	}
}

func TestConcurrentAdminGrantReplacementUsesAggregateVersionCAS(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Concurrent Administrative Grant Owner")
	until := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	initial, err := f.module.GrantAdminTx(context.Background(), tx, owner.id, 0, 3, until, "initial grant")
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	type result struct {
		quota int
		err   error
	}
	results := make(chan result, 2)
	for _, quota := range []int{5, 7} {
		go func(quota int) {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				results <- result{quota: quota, err: err}
				return
			}
			defer tx.Rollback(ctx)
			_, err = f.module.GrantAdminTx(ctx, tx, owner.id, initial.Version, quota, until.Add(time.Duration(quota)*time.Hour), "concurrent replacement")
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- result{quota: quota, err: err}
		}(quota)
	}
	close(start)

	var succeeded, conflicted int
	winnerQuota := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			winnerQuota = result.quota
		case errors.Is(result.err, service.ErrAdminGrantConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent admin grant result for quota %d: %v", result.quota, result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("CAS winners=%d conflicts=%d, want 1/1", succeeded, conflicted)
	}
	var quota int
	var version int64
	if err := f.pool.QueryRow(context.Background(), `SELECT monitor_limit,version FROM billing.admin_grants WHERE account_id=$1`, owner.id).Scan(&quota, &version); err != nil {
		t.Fatal(err)
	}
	if quota != winnerQuota || version != initial.Version+1 {
		t.Fatalf("stored grant quota/version=%d/%d, winner=%d initial=%+v", quota, version, winnerQuota, initial)
	}
}

func TestBillingPriceChangeKeepsHistoricalOrderAmountOnIdempotentReplay(t *testing.T) {
	f := newBillingFixture(t)
	quoteOwner := f.newActor(t, "Billing Quote Owner")
	singleMonitorOwner := f.newActor(t, "Single Monitor Buyer")
	multipleMonitorOwner := f.newActor(t, "Multiple Monitor Buyer")
	module, err := NewConfigured(f.pool, false, Config{
		Provider:       "mock",
		MockAccountIDs: []string{quoteOwner.id, singleMonitorOwner.id, multipleMonitorOwner.id},
	}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module = module
	f.handler = mux
	publicPricing := httptest.NewRecorder()
	f.handler.ServeHTTP(publicPricing, httptest.NewRequest(http.MethodGet, "/api/v1/billing/pricing", nil))
	if publicPricing.Code != http.StatusOK || publicPricing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public pricing status=%d cache-control=%q body=%s", publicPricing.Code, publicPricing.Header().Get("Cache-Control"), publicPricing.Body.String())
	}
	var publicQuote struct {
		Pricing contracts.Pricing `json:"pricing"`
	}
	if err := json.Unmarshal(publicPricing.Body.Bytes(), &publicQuote); err != nil || publicQuote.Pricing.PricePerMonitorMinor != 2900 || publicQuote.Pricing.Version != 1 || publicQuote.Pricing.Currency != "RUB" || publicQuote.Pricing.Period != "month" {
		t.Fatalf("public pricing=%+v decode_error=%v", publicQuote.Pricing, err)
	}

	quoteResponse := f.request(t, quoteOwner, http.MethodGet, "/api/v1/billing", nil)
	if quoteResponse.Code != http.StatusOK {
		t.Fatalf("billing quote status=%d: %s", quoteResponse.Code, quoteResponse.Body.String())
	}
	var quote contracts.Billing
	if err := json.Unmarshal(quoteResponse.Body.Bytes(), &quote); err != nil {
		t.Fatalf("decode billing quote: %v", err)
	}
	if quote.PricePerMonitorMinor != 2900 {
		t.Fatalf("price per monitor=%d, want 2900 minor units", quote.PricePerMonitorMinor)
	}
	if quote.PriceVersion != 1 {
		t.Fatalf("initial price version=%d, want 1", quote.PriceVersion)
	}

	for _, tc := range []struct {
		name   string
		actor  billingActor
		count  int
		amount int
		key    string
	}{
		{name: "one monitor", actor: singleMonitorOwner, count: 1, amount: 2900, key: "5ab34714-1e0d-4984-b8dd-4d89c9fd725e"},
		{name: "three monitors", actor: multipleMonitorOwner, count: 3, amount: 8700, key: "53c944c3-0297-4090-84d2-9bbab3e635c2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := f.request(t, tc.actor, http.MethodPost, "/api/v1/billing/orders", map[string]any{
				"monitorCount": tc.count, "idempotencyKey": tc.key, "priceVersion": quote.PriceVersion,
			})
			if response.Code != http.StatusCreated {
				t.Fatalf("create order status=%d: %s", response.Code, response.Body.String())
			}
			var body struct {
				Order contracts.Order `json:"order"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode order: %v", err)
			}
			if body.Order.MonitorCount != tc.count || body.Order.AmountMinor != tc.amount {
				t.Fatalf("created order count=%d amount=%d, want count=%d amount=%d", body.Order.MonitorCount, body.Order.AmountMinor, tc.count, tc.amount)
			}
		})
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.pricing SET price_per_monitor_minor=2400,version=2,updated_at=clock_timestamp() WHERE singleton=1 AND version=1`); err != nil {
		t.Fatalf("update test price to 2400 minor units: %v", err)
	}
	updatedPricing := httptest.NewRecorder()
	f.handler.ServeHTTP(updatedPricing, httptest.NewRequest(http.MethodGet, "/api/v1/billing/pricing", nil))
	if updatedPricing.Code != http.StatusOK {
		t.Fatalf("read updated public price status=%d body=%s", updatedPricing.Code, updatedPricing.Body.String())
	}
	var currentQuote struct {
		Pricing contracts.Pricing `json:"pricing"`
	}
	if err := json.Unmarshal(updatedPricing.Body.Bytes(), &currentQuote); err != nil || currentQuote.Pricing.PricePerMonitorMinor != 2400 || currentQuote.Pricing.Version != 2 {
		t.Fatalf("updated public pricing=%+v decode_error=%v", currentQuote.Pricing, err)
	}
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM billing.pricing WHERE singleton=1`); err != nil {
		t.Fatalf("remove current quote for unavailable-price overview test: %v", err)
	}
	retained := f.request(t, multipleMonitorOwner, http.MethodGet, "/api/v1/billing", nil)
	if retained.Code != http.StatusOK {
		t.Fatalf("billing overview without current pricing status=%d body=%s", retained.Code, retained.Body.String())
	}
	var unavailableOverview contracts.Billing
	if err := json.Unmarshal(retained.Body.Bytes(), &unavailableOverview); err != nil {
		t.Fatalf("decode overview without current quote: %v", err)
	}
	if unavailableOverview.PricePerMonitorMinor != 0 || unavailableOverview.PriceVersion != 0 || len(unavailableOverview.Orders) != 1 || unavailableOverview.Orders[0].AmountMinor != 8700 {
		t.Fatalf("unavailable quote hid or changed the existing order: price=%d version=%d orders=%+v", unavailableOverview.PricePerMonitorMinor, unavailableOverview.PriceVersion, unavailableOverview.Orders)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO billing.pricing(singleton,price_per_monitor_minor,version,updated_at) VALUES(1,2400,2,clock_timestamp())`); err != nil {
		t.Fatalf("restore current quote after unavailable-price test: %v", err)
	}
	stale := f.request(t, quoteOwner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": 3, "idempotencyKey": "d19544b7-b4b5-4caa-88f0-d0ac45e90401", "priceVersion": quote.PriceVersion,
	})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"error":"price_changed"`) {
		t.Fatalf("new order with stale quote status=%d body=%s, want price_changed", stale.Code, stale.Body.String())
	}
	missingVersion := f.request(t, quoteOwner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": 1, "idempotencyKey": "d19544b7-b4b5-4caa-88f0-d0ac45e90402",
	})
	if missingVersion.Code != http.StatusConflict || !strings.Contains(missingVersion.Body.String(), `"error":"price_changed"`) {
		t.Fatalf("new order without price version status=%d body=%s, want price_changed", missingVersion.Code, missingVersion.Body.String())
	}
	var staleKeyRows int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.orders WHERE idempotency_key IN ('d19544b7-b4b5-4caa-88f0-d0ac45e90401','d19544b7-b4b5-4caa-88f0-d0ac45e90402')`).Scan(&staleKeyRows); err != nil || staleKeyRows != 0 {
		t.Fatalf("stale quote inserted orders=%d err=%v", staleKeyRows, err)
	}
	oldReplay := f.request(t, multipleMonitorOwner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": 3, "idempotencyKey": "53c944c3-0297-4090-84d2-9bbab3e635c2", "priceVersion": quote.PriceVersion,
	})
	if oldReplay.Code != http.StatusCreated {
		t.Fatalf("replay old order with stale price version status=%d body=%s", oldReplay.Code, oldReplay.Body.String())
	}
	var oldOrder struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(oldReplay.Body.Bytes(), &oldOrder); err != nil || oldOrder.Order.AmountMinor != 8700 || oldOrder.Order.MonitorCount != 3 {
		t.Fatalf("old order replay=%+v decode_error=%v, want original 8700 minor units", oldOrder.Order, err)
	}
	canceled := f.request(t, multipleMonitorOwner, http.MethodPost, "/api/v1/billing/orders/"+oldOrder.Order.ID+"/simulate", map[string]any{"outcome": "canceled"})
	if canceled.Code != http.StatusOK {
		t.Fatalf("cancel old mock order status=%d body=%s", canceled.Code, canceled.Body.String())
	}
	newOrderResponse := f.request(t, multipleMonitorOwner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": 3, "idempotencyKey": "0af4294d-85c3-4d2c-9830-a4d2cd1964dc", "priceVersion": currentQuote.Pricing.Version,
	})
	if newOrderResponse.Code != http.StatusCreated {
		t.Fatalf("new order with refreshed quote status=%d body=%s", newOrderResponse.Code, newOrderResponse.Body.String())
	}
	var newOrder struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(newOrderResponse.Body.Bytes(), &newOrder); err != nil || newOrder.Order.AmountMinor != 7200 || newOrder.Order.MonitorCount != 3 {
		t.Fatalf("new order=%+v decode_error=%v, want 7200 minor units", newOrder.Order, err)
	}
	var storedOldAmount int
	if err := f.pool.QueryRow(context.Background(), `SELECT amount_minor FROM billing.orders WHERE id=$1`, oldOrder.Order.ID).Scan(&storedOldAmount); err != nil || storedOldAmount != 8700 {
		t.Fatalf("historical order amount=%d err=%v, want unchanged 8700", storedOldAmount, err)
	}

	const historicalOrderID = "7b3435e4-6a73-4f47-9d14-9f9b19a33a0c"
	const historicalIdempotencyKey = "4dbcc840-f3f5-4a9e-94b0-5e71e9f3049b"
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor)
		VALUES($1,$2,$3,'mock',1,2400)
	`, historicalOrderID, quoteOwner.id, historicalIdempotencyKey); err != nil {
		t.Fatalf("insert historical 2400-minor order: %v", err)
	}
	confirmed, err := module.service.Simulate(context.Background(), quoteOwner.id, historicalOrderID, "succeeded")
	if err != nil {
		t.Fatalf("confirm historical order: %v", err)
	}
	if confirmed.Status != "succeeded" || confirmed.AmountMinor != 2400 {
		t.Fatalf("confirmed historical order status=%s amount=%d, want succeeded/2400", confirmed.Status, confirmed.AmountMinor)
	}
	confirmedReplay, err := module.service.Simulate(context.Background(), quoteOwner.id, historicalOrderID, "succeeded")
	if err != nil || confirmedReplay.Status != "succeeded" || confirmedReplay.AmountMinor != 2400 {
		t.Fatalf("idempotent confirmation replay=%+v error=%v, want succeeded/2400", confirmedReplay, err)
	}
	replayed, err := module.service.CreateOrder(context.Background(), quoteOwner.id, 1, historicalIdempotencyKey, 0)
	if err != nil {
		t.Fatalf("replay historical order: %v", err)
	}
	if replayed.ID != historicalOrderID || replayed.AmountMinor != 2400 {
		t.Fatalf("historical idempotent replay id=%s amount=%d, want id=%s amount=2400", replayed.ID, replayed.AmountMinor, historicalOrderID)
	}
	sender := &reportRecorder{}
	worker, err := NewReportWorker(f.pool, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("historical payment report processed=%t error=%v", processed, err)
	}
	sender.mu.Lock()
	reports := append([]contracts.PaymentReport(nil), sender.reports...)
	sender.mu.Unlock()
	if len(reports) != 1 || reports[0].OrderID != historicalOrderID || reports[0].AmountMinor != 2400 {
		t.Fatalf("historical payment reports=%+v, want one report with original amount 2400", reports)
	}
	var storedAmount int
	if err := f.pool.QueryRow(context.Background(), `SELECT amount_minor FROM billing.orders WHERE id=$1`, historicalOrderID).Scan(&storedAmount); err != nil {
		t.Fatal(err)
	}
	if storedAmount != 2400 {
		t.Fatalf("historical amount after replay=%d, want unchanged 2400", storedAmount)
	}
}

func TestBillingPriceCapUsesIntegerAmountAndDoesNotRejectHistoricalReplay(t *testing.T) {
	f := newBillingFixture(t)
	atCap := f.newActor(t, "Maximum Billing Order")
	overCap := f.newActor(t, "Over Maximum Billing Order")
	legacy := f.newActor(t, "Legacy Oversized Billing Order")
	module, err := NewConfigured(f.pool, false, Config{
		Provider:       "mock",
		MockAccountIDs: []string{atCap.id, overCap.id, legacy.id},
	}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	module.Register(mux, authProtect(t, f.pool, f.accounts))
	f.module = module
	f.handler = mux
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.pricing SET price_per_monitor_minor=1000000,version=2 WHERE singleton=1`); err != nil {
		t.Fatalf("set maximum allowed quote: %v", err)
	}
	quote := f.request(t, atCap, http.MethodGet, "/api/v1/billing", nil)
	if quote.Code != http.StatusOK || !strings.Contains(quote.Body.String(), `"priceVersion":2`) {
		t.Fatalf("maximum quote status=%d body=%s", quote.Code, quote.Body.String())
	}

	allowedKey := "7db00000-0000-4000-8000-000000000001"
	allowed := f.request(t, atCap, http.MethodPost, "/api/v1/billing/orders", map[string]any{"monitorCount": 35, "idempotencyKey": allowedKey, "priceVersion": 2})
	if allowed.Code != http.StatusCreated {
		t.Fatalf("maximum 350000 RUB order status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	var allowedOrder struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(allowed.Body.Bytes(), &allowedOrder); err != nil || allowedOrder.Order.AmountMinor != 35_000_000 {
		t.Fatalf("maximum order=%+v decode_error=%v, want exact 35000000 minor units", allowedOrder.Order, err)
	}

	overKey := "7db00000-0000-4000-8000-000000000002"
	over := f.request(t, overCap, http.MethodPost, "/api/v1/billing/orders", map[string]any{"monitorCount": 36, "idempotencyKey": overKey, "priceVersion": 2})
	if over.Code != http.StatusBadRequest || !strings.Contains(over.Body.String(), `"error":"order_amount_exceeded"`) {
		t.Fatalf("over-cap 360000 RUB order status=%d body=%s, want order_amount_exceeded", over.Code, over.Body.String())
	}
	var overRows int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.orders WHERE idempotency_key=$1`, overKey).Scan(&overRows); err != nil || overRows != 0 {
		t.Fatalf("over-cap order inserted rows=%d err=%v", overRows, err)
	}

	const legacyOrderID = "7dc00000-0000-4000-8000-000000000003"
	const legacyKey = "7dc00000-0000-4000-8000-000000000004"
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor) VALUES($1,$2,$3,'mock',36,36000000)`, legacyOrderID, legacy.id, legacyKey); err != nil {
		t.Fatalf("insert existing order outside current amount cap: %v", err)
	}
	replay := f.request(t, legacy, http.MethodPost, "/api/v1/billing/orders", map[string]any{"monitorCount": 36, "idempotencyKey": legacyKey, "priceVersion": 0})
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay historical order above current cap status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayedOrder struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &replayedOrder); err != nil || replayedOrder.Order.ID != legacyOrderID || replayedOrder.Order.AmountMinor != 36_000_000 {
		t.Fatalf("historical order replay=%+v decode_error=%v, want original 36000000 minor units", replayedOrder.Order, err)
	}
}

type reportRecorder struct {
	mu       sync.Mutex
	failures int
	failure  error
	reports  []contracts.PaymentReport
}

func (r *reportRecorder) SendPaymentReport(_ context.Context, report contracts.PaymentReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	if r.failures > 0 {
		r.failures--
		if r.failure != nil {
			return r.failure
		}
		return errors.New("temporary report failure")
	}
	return nil
}

func TestBillingReportRetryLeaseAndTerminalExhaustion(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Report Billing Owner")
	module, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	order, err := module.service.CreateOrder(context.Background(), owner.id, 1, "75393bd8-6d7f-4c21-8b18-a5d6d25e4552", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	sender := &reportRecorder{failures: 1}
	worker, err := NewReportWorker(f.pool, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("first report processed=%v err=%v", processed, err)
	}
	var status string
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, order.ID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 {
		t.Fatalf("after retryable error status=%s attempts=%d", status, attempts)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET next_attempt_at=now() WHERE order_id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("retry report processed=%v err=%v", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, order.ID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 2 {
		t.Fatalf("after successful retry status=%s attempts=%d", status, attempts)
	}
	sender.mu.Lock()
	gotReports := append([]contracts.PaymentReport(nil), sender.reports...)
	sender.mu.Unlock()
	if len(gotReports) != 2 || gotReports[0].OrderID != order.ID || !gotReports[0].Test {
		t.Fatalf("retry sender calls=%+v", gotReports)
	}
	second, err := module.service.CreateOrder(context.Background(), owner.id, 1, "22453bd0-73b7-482d-abed-cb409ff5a390", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, second.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET status='sending',attempts=1,attempt_token='bf7cd67d-3a8b-4cff-beda-9e9343e557db',lease_until=now()+interval '1 minute' WHERE order_id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || processed {
		t.Fatalf("active report lease processed=%v err=%v, want no delivery", processed, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET lease_until=now()-interval '1 second' WHERE order_id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("expired report lease processed=%v err=%v, want reclaim", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, second.ID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 2 {
		t.Fatalf("reclaimed lease status=%s attempts=%d, want sent/2", status, attempts)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET status='sending',attempts=8,attempt_token='bf7cd67d-3a8b-4cff-beda-9e9343e557db',lease_until=now()-interval '1 second',finished_at=NULL WHERE order_id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || processed {
		t.Fatalf("exhausted report processed=%v err=%v, want no delivery", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, second.ID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 8 {
		t.Fatalf("exhausted lease status=%s attempts=%d, want failed/8", status, attempts)
	}
}

func TestBillingRenewalAnchorClampsMonthEndInMoscow(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Month End Billing Owner")
	module, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason,renewal_anchor_day) VALUES($1,1,$2,'mock_test','month end test',31)`, owner.id, start); err != nil {
		t.Fatal(err)
	}
	wantDates := []string{"2026-11-30", "2026-12-31", "2027-01-31", "2027-02-28", "2027-03-31"}
	moscow := time.FixedZone("MSK", 3*60*60)
	for index, wantDate := range wantDates {
		key := fmt.Sprintf("a4c1a100-0000-4000-8000-%012d", index+1)
		order, err := module.service.CreateOrder(context.Background(), owner.id, 1, key, 1)
		if err != nil {
			t.Fatalf("create renewal %d: %v", index+1, err)
		}
		completed, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded")
		if err != nil {
			t.Fatalf("simulate renewal %d: %v", index+1, err)
		}
		if completed.ActiveUntil == nil || completed.ActiveUntil.In(moscow).Format("2006-01-02") != wantDate {
			t.Fatalf("renewal %d active until=%v, want Moscow date %s", index+1, completed.ActiveUntil, wantDate)
		}
	}
}

func TestBillingReportRecordsBoundedFailureAndTerminatesAfterEighthAttempt(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Payment Report Failure Owner")
	module, err := NewConfigured(f.pool, false, Config{Provider: "mock", MockAccountIDs: []string{owner.id}}, f.accounts)
	if err != nil {
		t.Fatal(err)
	}
	order, err := module.service.CreateOrder(context.Background(), owner.id, 1, "b6a5f902-d5b9-4f06-a083-01004733bac5", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	sender := &reportRecorder{failures: 1, failure: &contracts.ReportSendError{Code: "provider_http", HTTPStatus: http.StatusTooManyRequests, RetryAfter: 45 * time.Second}}
	worker, err := NewReportWorker(f.pool, sender)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("429 report processed=%v err=%v", processed, err)
	}
	var status, code string
	var httpStatus, attempts int
	var retryBounded bool
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts,last_error_code,last_http_status,next_attempt_at>now()+interval '44 seconds' FROM billing.payment_reports WHERE order_id=$1`, order.ID).Scan(&status, &attempts, &code, &httpStatus, &retryBounded); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || code != "provider_http" || httpStatus != http.StatusTooManyRequests || !retryBounded {
		t.Fatalf("429 report state status=%s attempts=%d code=%s http=%d retry_bounded=%v", status, attempts, code, httpStatus, retryBounded)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET attempts=7,next_attempt_at=now() WHERE order_id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	sender.failures, sender.failure = 1, &contracts.ReportSendError{Code: "provider_rejected", HTTPStatus: http.StatusBadRequest}
	sender.mu.Unlock()
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("terminal report processed=%v err=%v", processed, err)
	}
	var finished bool
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts,last_error_code,last_http_status,finished_at IS NOT NULL FROM billing.payment_reports WHERE order_id=$1`, order.ID).Scan(&status, &attempts, &code, &httpStatus, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 8 || code != "provider_rejected" || httpStatus != http.StatusBadRequest || !finished {
		t.Fatalf("terminal report state status=%s attempts=%d code=%s http=%d finished=%v", status, attempts, code, httpStatus, finished)
	}
}

func TestIndependentMockPackagesAccumulateAndExpireSeparately(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Independent Packages Billing Owner")
	module := configureMockBilling(t, f, owner)

	empty := currentBillingEntitlement(t, f, owner)
	if empty.Packages == nil || len(empty.Packages) != 0 {
		t.Fatalf("empty package collection=%+v, want non-nil empty slice", empty.Packages)
	}

	createAndComplete := func(key string, count int) contracts.Order {
		t.Helper()
		order, err := module.service.CreateSelectedOrder(context.Background(), owner.id, count, key, 1, "purchase", "")
		if err != nil {
			t.Fatalf("create independent purchase: %v", err)
		}
		if order.PurchaseKind != "purchase" || order.PackageID != nil || order.FulfilledPackageID != nil {
			t.Fatalf("new purchase order selection=%+v", order)
		}
		completed, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded")
		if err != nil {
			t.Fatalf("complete independent purchase: %v", err)
		}
		if completed.FulfilledPackageID == nil || *completed.FulfilledPackageID != order.ID {
			t.Fatalf("purchase result package=%v, want package id %s", completed.FulfilledPackageID, order.ID)
		}
		return completed
	}

	first := createAndComplete("1e943c3a-976d-4b2b-b463-50bc2a900001", 2)
	firstUntil := time.Now().AddDate(0, 0, 7).UTC().Truncate(time.Microsecond)
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.entitlements SET valid_until=$2 WHERE id=$1`, first.ID, firstUntil); err != nil {
		t.Fatalf("set first package expiry fixture: %v", err)
	}
	second := createAndComplete("1e943c3a-976d-4b2b-b463-50bc2a900002", 2)
	if first.ID == second.ID {
		t.Fatal("separate purchases reused one package id")
	}

	combined := currentBillingEntitlement(t, f, owner)
	if len(combined.Packages) != 2 || combined.Quota != 4 || combined.Version != 2 || combined.Paid == nil || combined.Paid.Quota != 4 || combined.Paid.Version != 2 || !combined.Paid.Active {
		t.Fatalf("two packages did not add into paid entitlement: %+v", combined)
	}
	firstPackage := billingPackage(t, combined, first.ID)
	secondPackage := billingPackage(t, combined, second.ID)
	if !firstPackage.Active || !secondPackage.Active || firstPackage.Quota != 2 || secondPackage.Quota != 2 || firstPackage.ActiveUntil.Equal(*secondPackage.ActiveUntil) {
		t.Fatalf("package dates or activity were collapsed: first=%+v second=%+v", firstPackage, secondPackage)
	}
	if combined.ActiveUntil == nil || !combined.ActiveUntil.Equal(*firstPackage.ActiveUntil) {
		t.Fatalf("combined activeUntil=%v, want earliest package expiry %v", combined.ActiveUntil, firstPackage.ActiveUntil)
	}

	if _, err := module.service.Simulate(context.Background(), owner.id, first.ID, "succeeded"); err != nil {
		t.Fatalf("duplicate mock completion: %v", err)
	}
	var packages, reports int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.entitlements WHERE account_id=$1`, owner.id).Scan(&packages); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.payment_reports WHERE order_id IN ($1,$2)`, first.ID, second.ID).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if packages != 2 || reports != 2 {
		t.Fatalf("duplicate completion produced packages=%d reports=%d, want exactly 2/2", packages, reports)
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.entitlements SET valid_until=clock_timestamp()-interval '1 minute' WHERE id=$1`, first.ID); err != nil {
		t.Fatalf("expire first package fixture: %v", err)
	}
	afterExpiry := currentBillingEntitlement(t, f, owner)
	expired := billingPackage(t, afterExpiry, first.ID)
	remaining := billingPackage(t, afterExpiry, second.ID)
	if expired.Active || !remaining.Active || afterExpiry.Quota != 2 || afterExpiry.Paid == nil || afterExpiry.Paid.Quota != 2 || afterExpiry.Version != 2 {
		t.Fatalf("one package expiry changed the other component: entitlement=%+v expired=%+v remaining=%+v", afterExpiry, expired, remaining)
	}
}

func TestRenewingAnExpiredPackageExtendsOnlyItsSelectedPackage(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Selected Package Renewal Owner")
	module := configureMockBilling(t, f, owner)

	createAndComplete := func(key string, count int) contracts.Order {
		t.Helper()
		order, err := module.service.CreateSelectedOrder(context.Background(), owner.id, count, key, 1, "purchase", "")
		if err != nil {
			t.Fatalf("create purchase: %v", err)
		}
		completed, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded")
		if err != nil {
			t.Fatalf("complete purchase: %v", err)
		}
		return completed
	}
	selected := createAndComplete("2e943c3a-976d-4b2b-b463-50bc2a900001", 2)
	other := createAndComplete("2e943c3a-976d-4b2b-b463-50bc2a900002", 3)
	selectedUntil := time.Now().AddDate(0, 0, -45).UTC().Truncate(time.Microsecond)
	otherUntil := time.Now().AddDate(0, 0, 55).UTC().Truncate(time.Microsecond)
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.entitlements SET valid_until=$2 WHERE id=$1`, selected.ID, selectedUntil); err != nil {
		t.Fatalf("expire selected package fixture: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.entitlements SET valid_until=$2 WHERE id=$1`, other.ID, otherUntil); err != nil {
		t.Fatalf("set other package expiry fixture: %v", err)
	}
	before := currentBillingEntitlement(t, f, owner)
	selectedBefore := billingPackage(t, before, selected.ID)
	otherBefore := billingPackage(t, before, other.ID)

	renewal, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, "2e943c3a-976d-4b2b-b463-50bc2a900003", 1, "renew", selected.ID)
	if err != nil {
		t.Fatalf("create selected renewal for expired package: %v", err)
	}
	if renewal.PurchaseKind != "renew" || renewal.PackageID == nil || *renewal.PackageID != selected.ID || renewal.FulfilledPackageID != nil {
		t.Fatalf("selected renewal order=%+v", renewal)
	}
	completed, err := module.service.Simulate(context.Background(), owner.id, renewal.ID, "succeeded")
	if err != nil {
		t.Fatalf("fulfill selected renewal: %v", err)
	}
	if completed.FulfilledPackageID == nil || *completed.FulfilledPackageID != selected.ID {
		t.Fatalf("renewal fulfilled package=%v, want selected %s", completed.FulfilledPackageID, selected.ID)
	}

	after := currentBillingEntitlement(t, f, owner)
	renewed := billingPackage(t, after, selected.ID)
	unchanged := billingPackage(t, after, other.ID)
	if !renewed.Active || renewed.Quota != selectedBefore.Quota || renewed.Version != selectedBefore.Version+1 || renewed.ActiveUntil == nil || renewed.ActiveUntil.Before(time.Now().AddDate(0, 0, 27)) {
		t.Fatalf("expired selected package did not receive a fresh full month: before=%+v after=%+v", selectedBefore, renewed)
	}
	if unchanged.ActiveUntil == nil || !unchanged.ActiveUntil.Equal(*otherBefore.ActiveUntil) || unchanged.Version != otherBefore.Version || after.Quota != 5 || after.Version != 3 {
		t.Fatalf("renewal changed other package or aggregate: entitlement=%+v other-before=%+v other-after=%+v", after, otherBefore, unchanged)
	}
}

func TestLegacyOrderRequestRequiresExplicitSelectionWhenMultiplePackagesAreActive(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Legacy Checkout Intent Owner")
	module := configureMockBilling(t, f, owner)
	order, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, "3e943c3a-976d-4b2b-b463-50bc2a900001", 1, "purchase", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	second, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, "3e943c3a-976d-4b2b-b463-50bc2a900003", 1, "purchase", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, second.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	legacy := f.request(t, owner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": 2, "idempotencyKey": "3e943c3a-976d-4b2b-b463-50bc2a900002", "priceVersion": 1,
	})
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(legacy.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode legacy order response: %v", err)
	}
	if legacy.Code != http.StatusConflict || response.Error != "billing_selection_required" {
		t.Fatalf("legacy request status=%d body=%s, want 409 billing_selection_required", legacy.Code, legacy.Body.String())
	}
	var orders int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.orders WHERE account_id=$1`, owner.id).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if orders != 2 {
		t.Fatalf("ambiguous legacy request created an order; count=%d", orders)
	}
}

func TestLegacyOrderRequestRenewsUnambiguousPackageWithoutAddingCapacity(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Legacy Single Package Owner")
	module := configureMockBilling(t, f, owner)
	initial, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, "4e943c3a-976d-4b2b-b463-50bc2a900001", 1, "purchase", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, initial.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	legacy := f.request(t, owner, http.MethodPost, "/api/v1/billing/orders", map[string]any{
		"monitorCount": 2, "idempotencyKey": "4e943c3a-976d-4b2b-b463-50bc2a900002", "priceVersion": 1,
	})
	if legacy.Code != http.StatusCreated {
		t.Fatalf("unambiguous legacy renewal status=%d body=%s", legacy.Code, legacy.Body.String())
	}
	var response struct {
		Order contracts.Order `json:"order"`
	}
	if err := json.Unmarshal(legacy.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode legacy renewal order: %v", err)
	}
	if response.Order.PurchaseKind != "renew" || response.Order.PackageID == nil || *response.Order.PackageID != initial.ID || response.Order.FulfilledPackageID != nil {
		t.Fatalf("legacy request did not select the matching existing package: %+v", response.Order)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, response.Order.ID, "succeeded"); err != nil {
		t.Fatalf("complete legacy renewal: %v", err)
	}
	entitlement := currentBillingEntitlement(t, f, owner)
	if len(entitlement.Packages) != 1 || entitlement.Quota != 2 || entitlement.Packages[0].Version != 2 || entitlement.Packages[0].ID != initial.ID {
		t.Fatalf("legacy renewal created extra package/capacity: %+v", entitlement)
	}
}

func TestIndependentPackageTotalMayExceedPerOrderLimit(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Independent Package Aggregate Limit Owner")
	module := configureMockBilling(t, f, owner)
	for _, purchase := range []struct {
		key   string
		count int
	}{
		{key: "5e943c3a-976d-4b2b-b463-50bc2a900001", count: 1000},
		{key: "5e943c3a-976d-4b2b-b463-50bc2a900002", count: 1},
	} {
		order, err := module.service.CreateSelectedOrder(context.Background(), owner.id, purchase.count, purchase.key, 1, "purchase", "")
		if err != nil {
			t.Fatalf("create %d-place package: %v", purchase.count, err)
		}
		if _, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded"); err != nil {
			t.Fatalf("complete %d-place package: %v", purchase.count, err)
		}
	}
	entitlement := currentBillingEntitlement(t, f, owner)
	if len(entitlement.Packages) != 2 || entitlement.Quota != 1001 {
		t.Fatalf("independent package total=%d across %d packages, want 1001 across two: %+v", entitlement.Quota, len(entitlement.Packages), entitlement)
	}
}

func TestRenewalRejectsForeignPackageAndIdempotencyIntentChange(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Package Target Owner")
	stranger := f.newActor(t, "Package Target Stranger")
	module := configureMockBilling(t, f, owner)
	strangerModule := configureMockBilling(t, f, stranger)
	foreignPurchase, err := strangerModule.service.CreateSelectedOrder(context.Background(), stranger.id, 2, "6e943c3a-976d-4b2b-b463-50bc2a900001", 1, "purchase", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strangerModule.service.Simulate(context.Background(), stranger.id, foreignPurchase.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, "6e943c3a-976d-4b2b-b463-50bc2a900002", 1, "renew", foreignPurchase.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("renewal of another account's package error=%v, want owner-scoped not found", err)
	}

	ownedPurchase, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, "6e943c3a-976d-4b2b-b463-50bc2a900003", 1, "purchase", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.Simulate(context.Background(), owner.id, ownedPurchase.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	key := "6e943c3a-976d-4b2b-b463-50bc2a900004"
	original, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, key, 1, "purchase", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, key, 1, "renew", ownedPurchase.ID); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("same-key intent change error=%v, want conflict", err)
	}
	replay, err := module.service.CreateSelectedOrder(context.Background(), owner.id, 2, key, 1, "purchase", "")
	if err != nil || replay.ID != original.ID || replay.PurchaseKind != "purchase" {
		t.Fatalf("same-intent replay=(%+v,%v), want original purchase %s", replay, err, original.ID)
	}
}

func TestAccountDeletionExpiresLinkedPackagesAndRetainsBillingHistory(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Linked Packages Deletion Owner")
	module := configureMockBilling(t, f, owner)
	var packageOrders []contracts.Order
	for i, count := range []int{2, 3} {
		key := fmt.Sprintf("7e943c3a-976d-4b2b-b463-50bc2a90000%d", i+1)
		order, err := module.service.CreateSelectedOrder(context.Background(), owner.id, count, key, 1, "purchase", "")
		if err != nil {
			t.Fatalf("create package %d: %v", i+1, err)
		}
		completed, err := module.service.Simulate(context.Background(), owner.id, order.ID, "succeeded")
		if err != nil {
			t.Fatalf("complete package %d: %v", i+1, err)
		}
		packageOrders = append(packageOrders, completed)
	}
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE accounts.accounts SET status='deleted',deleted_at=clock_timestamp(),version=version+1 WHERE id=$1`, owner.id); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := module.DeleteAccountTx(context.Background(), tx, owner.id); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	var packages int
	var active int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*),count(*) FILTER (WHERE valid_until>clock_timestamp()) FROM billing.entitlements WHERE account_id=$1`, owner.id).Scan(&packages, &active); err != nil {
		t.Fatal(err)
	}
	if packages != 2 || active != 0 {
		t.Fatalf("deleted account packages retained=%d active=%d, want two expired rows", packages, active)
	}
	for _, order := range packageOrders {
		var status, fulfillment string
		var originOrder, linkedPackage, fulfilledPackage string
		if err := f.pool.QueryRow(context.Background(), `SELECT e.origin_order_id::text,e.id::text,o.status,o.fulfillment_status,o.fulfilled_package_id::text
			FROM billing.entitlements e JOIN billing.orders o ON o.id=e.origin_order_id WHERE e.id=$1`, *order.FulfilledPackageID).
			Scan(&originOrder, &linkedPackage, &status, &fulfillment, &fulfilledPackage); err != nil {
			t.Fatal(err)
		}
		if originOrder != order.ID || linkedPackage != order.ID || fulfilledPackage != order.ID || status != "succeeded" || fulfillment != "fulfilled" {
			t.Fatalf("package/order history changed after soft delete: package=%s origin=%s result=%s status=%s fulfillment=%s", linkedPackage, originOrder, fulfilledPackage, status, fulfillment)
		}
	}
}
