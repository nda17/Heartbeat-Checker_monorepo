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
	module, err := NewConfigured(pool, false, Config{Provider: "disabled"}, accountModule)
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
			 fulfillment_status,purchase_kind,return_target,checkout_kind,provider_shop_id,provider_request)
		SELECT md5($1::text || ':order:' || n)::uuid, $1::uuid,
			md5($1::text || ':key:' || n)::uuid, 'yookassa', 'canceled', 0, 2400, 'RUB',
			$2::timestamptz - ((n - 1) / 25) * interval '1 second',
			$2::timestamptz + interval '30 minutes',
			$2::timestamptz - ((n - 1) / 25) * interval '1 second',
			'test', 'complete', 'not_required', 'topup', 'web', 'redirect', '123456', '{}'::bytea
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

func authProtect(t *testing.T, pool *pgxpool.Pool, accountModule *accounts.Module) func(http.Handler) http.Handler {
	t.Helper()
	m, err := auth.New(pool, accountModule, billingCaptcha{}, billingMailer{}, auth.Config{Origin: billingTestOrigin})
	if err != nil {
		t.Fatalf("create auth middleware: %v", err)
	}
	return m.Protect
}

// This fixture starts after provider reconciliation has committed a top-up
// and queued its report. Reconciliation itself has separate provider tests.
func seedReport(t *testing.T, f *billingFixture, accountID string) string {
	t.Helper()
	var orderID string
	err := f.pool.QueryRow(context.Background(), `WITH paid AS (
		INSERT INTO billing.orders(id,account_id,idempotency_key,provider,status,monitor_count,amount_minor,
			completed_at,provider_mode,provider_shop_id,provider_request,fulfillment_status,fulfilled_at,purchase_kind)
		VALUES(gen_random_uuid(),$1,gen_random_uuid(),'yookassa','succeeded',0,5555,
			now(),'production','123456','{}'::bytea,'fulfilled',now(),'topup') RETURNING id)
		INSERT INTO billing.payment_reports(order_id) SELECT id FROM paid RETURNING order_id::text`, accountID).Scan(&orderID)
	if err != nil {
		t.Fatal(err)
	}
	return orderID
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
	orderID := seedReport(t, f, owner.id)
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
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, orderID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 {
		t.Fatalf("after retryable error status=%s attempts=%d", status, attempts)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET next_attempt_at=now() WHERE order_id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("retry report processed=%v err=%v", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, orderID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 2 {
		t.Fatalf("after successful retry status=%s attempts=%d", status, attempts)
	}
	sender.mu.Lock()
	gotReports := append([]contracts.PaymentReport(nil), sender.reports...)
	sender.mu.Unlock()
	if len(gotReports) != 2 || gotReports[0].OrderID != orderID || gotReports[0].Test || gotReports[0].AmountMinor != 5555 || gotReports[0].MonitorCount != 0 {
		t.Fatalf("retry sender calls=%+v", gotReports)
	}
	secondID := seedReport(t, f, owner.id)
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET status='sending',attempts=1,attempt_token='bf7cd67d-3a8b-4cff-beda-9e9343e557db',lease_until=now()+interval '1 minute' WHERE order_id=$1`, secondID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || processed {
		t.Fatalf("active report lease processed=%v err=%v, want no delivery", processed, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET lease_until=now()-interval '1 second' WHERE order_id=$1`, secondID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("expired report lease processed=%v err=%v, want reclaim", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, secondID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 2 {
		t.Fatalf("reclaimed lease status=%s attempts=%d, want sent/2", status, attempts)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET status='sending',attempts=8,attempt_token='bf7cd67d-3a8b-4cff-beda-9e9343e557db',lease_until=now()-interval '1 second',finished_at=NULL WHERE order_id=$1`, secondID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || processed {
		t.Fatalf("exhausted report processed=%v err=%v, want no delivery", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM billing.payment_reports WHERE order_id=$1`, secondID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 8 {
		t.Fatalf("exhausted lease status=%s attempts=%d, want failed/8", status, attempts)
	}
}

func TestBillingReportRecordsBoundedFailureAndTerminatesAfterEighthAttempt(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Payment Report Failure Owner")
	orderID := seedReport(t, f, owner.id)
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
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts,last_error_code,last_http_status,next_attempt_at>now()+interval '44 seconds' FROM billing.payment_reports WHERE order_id=$1`, orderID).Scan(&status, &attempts, &code, &httpStatus, &retryBounded); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || code != "provider_http" || httpStatus != http.StatusTooManyRequests || !retryBounded {
		t.Fatalf("429 report state status=%s attempts=%d code=%s http=%d retry_bounded=%v", status, attempts, code, httpStatus, retryBounded)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.payment_reports SET attempts=7,next_attempt_at=now() WHERE order_id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	sender.failures, sender.failure = 1, &contracts.ReportSendError{Code: "provider_rejected", HTTPStatus: http.StatusBadRequest}
	sender.mu.Unlock()
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("terminal report processed=%v err=%v", processed, err)
	}
	var finished bool
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts,last_error_code,last_http_status,finished_at IS NOT NULL FROM billing.payment_reports WHERE order_id=$1`, orderID).Scan(&status, &attempts, &code, &httpStatus, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 8 || code != "provider_rejected" || httpStatus != http.StatusBadRequest || !finished {
		t.Fatalf("terminal report state status=%s attempts=%d code=%s http=%d finished=%v", status, attempts, code, httpStatus, finished)
	}
}

func historicalMeter(t *testing.T, f *billingFixture, owner billingActor, start time.Time, amountMinor int) {
	t.Helper()
	ctx := context.Background()
	creditPaymentFixture(t, f, owner, amountMinor)
	if _, err := f.pool.Exec(ctx, `INSERT INTO monitors.monitors(id,account_id,name,url,target_key,method,expected_status,enabled,outcome)
		VALUES(gen_random_uuid(),$1,'Metered','https://meter.example','meter.example:443','GET',200,true,'DOWN'),
		      (gen_random_uuid(),$1,'Paused','https://paused.example','paused.example:443','GET',200,false,'UP')`, owner.id); err != nil {
		t.Fatal(err)
	}
	// Replay the first worker run after a known historical interval without
	// sleeping or depending on today's calendar month.
	if _, err := f.pool.Exec(ctx, `UPDATE billing.wallets SET settled_at=$2,billed_monitors=1,next_settlement_at=$2 WHERE account_id=$1`, owner.id, start); err != nil {
		t.Fatal(err)
	}
}

func assertMeterStoppedAt(t *testing.T, f *billingFixture, owner billingActor, start, end time.Time, hours, cost int) {
	t.Helper()
	ctx := context.Background()
	processed, err := f.module.ProcessOneWallet(ctx)
	if err != nil || !processed {
		t.Fatalf("settle: processed=%t err=%v", processed, err)
	}
	// The balance reads use the metering path again. Neither should debit more.
	for range 2 {
		b, err := f.module.Balance(ctx, owner.id)
		if err != nil || b.AvailableMinor != 0 || b.PositiveBelowCent || !b.Suspended || b.FundedUntil != nil || b.EnabledMonitors != 1 {
			t.Fatalf("post-cutoff balance=%+v err=%v", b, err)
		}
	}
	var from, to time.Time
	var count, spent int
	err = f.pool.QueryRow(ctx, `SELECT min(period_start),max(period_end),count(*),-sum(amount_minor)::integer
		FROM billing.wallet_ledger WHERE account_id=$1 AND kind='usage'`, owner.id).Scan(&from, &to, &count, &spent)
	if err != nil || !from.Equal(start) || !to.Equal(end) || count != hours || spent != cost {
		t.Fatalf("charged from %v to %v in %d hours for %d minor; want %v to %v in %d hours for %d: %v", from, to, count, spent, start, end, hours, cost, err)
	}
}

func TestBillingCalendarMonthCostsExactlyMonthlyPrice(t *testing.T) {
	f := newBillingFixture(t)
	ctx := context.Background()
	// Backdate the initial rate as well as each wallet's last settlement.
	if _, err := f.pool.Exec(ctx, `UPDATE billing.wallet_tariffs SET price_per_monitor_minor=2600,effective_at='2020-01-01T00:00:00Z';
		UPDATE billing.pricing SET price_per_monitor_minor=2600,updated_at='2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	msk := time.FixedZone("MSK", 3*60*60)
	for _, month := range []time.Time{
		time.Date(2023, 2, 1, 0, 0, 0, 0, msk), time.Date(2024, 2, 1, 0, 0, 0, 0, msk),
		time.Date(2024, 4, 1, 0, 0, 0, 0, msk), time.Date(2024, 5, 1, 0, 0, 0, 0, msk),
	} {
		t.Run(month.Format("2006-01"), func(t *testing.T) {
			owner := f.newActor(t, "Calendar billing fixture")
			historicalMeter(t, f, owner, month, 2600)
			end := month.AddDate(0, 1, 0)
			assertMeterStoppedAt(t, f, owner, month, end, int(end.Sub(month)/time.Hour), 2600)
		})
	}
}

func TestBillingPartialHourUsesHistoricalRatesAndResumesFromTopup(t *testing.T) {
	f := newBillingFixture(t)
	ctx := context.Background()
	owner := f.newActor(t, "Partial hour owner")
	other := f.newActor(t, "Separate wallet")
	start := time.Date(2024, 4, 15, 9, 0, 0, 0, time.UTC)
	// April has 720 hours. The first hour costs 100 kopecks, then the
	// rate becomes 120. A 160-kopeck opening balance buys 90 minutes.
	if _, err := f.pool.Exec(ctx, `UPDATE billing.wallet_tariffs SET price_per_monitor_minor=72000,effective_at='2024-03-31T21:00:00Z';
		INSERT INTO billing.wallet_tariffs(version,effective_at,price_per_monitor_minor) VALUES(2,'2024-04-15T10:00:00Z',86400);
		UPDATE billing.pricing SET version=2,price_per_monitor_minor=86400,updated_at='2024-04-15T10:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	historicalMeter(t, f, owner, start, 160)
	assertMeterStoppedAt(t, f, owner, start, start.Add(90*time.Minute), 2, 160)
	token := nativeBillingToken(t, f, owner.id)
	for i, wantDebit := range []int64{-60, -100} {
		// The opening credit was recorded now and historical usage was posted
		// at the end of each old hour. Both platforms see the same entries.
		path := fmt.Sprintf("/api/v1/billing/ledger?page=%d&pageSize=1", i+2)
		for _, response := range []*httptest.ResponseRecorder{f.request(t, owner, http.MethodGet, path, nil), f.requestNative(t, token, path)} {
			var ledger contracts.LedgerPage
			if err := json.Unmarshal(response.Body.Bytes(), &ledger); err != nil || response.Code != 200 || ledger.Pagination.Total != 3 || len(ledger.Entries) != 1 {
				t.Fatalf("ledger page %d: %d %s", i+2, response.Code, response.Body.String())
			}
			e := ledger.Entries[0]
			if e.Kind != "usage" || e.AmountMinor != wantDebit || e.PeriodStart == nil || e.PeriodEnd == nil {
				t.Fatalf("ledger page %d entry=%+v", i+2, e)
			}
		}
	}
	res := f.request(t, other, http.MethodGet, "/api/v1/billing/ledger", nil)
	var otherLedger contracts.LedgerPage
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &otherLedger) != nil || len(otherLedger.Entries) != 0 || otherLedger.Pagination.Total != 0 {
		t.Fatalf("other wallet ledger: %d %s", res.Code, res.Body.String())
	}

	// Many simultaneous identical requests can restore monitoring only once.
	const workers = 6
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	applied := make(chan bool, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer tx.Rollback(ctx)
			_, _, did, err := f.module.AdminCreditTx(ctx, tx, owner.id, owner.id, 100, "cd2c10e6-ad73-4a74-a277-4f0930ff5485", "resume monitoring")
			if err == nil {
				err = tx.Commit(ctx)
			}
			if err != nil {
				errs <- err
				return
			}
			applied <- did
		}()
	}
	wg.Wait()
	close(errs)
	close(applied)
	for err := range errs {
		t.Fatal(err)
	}
	n := 0
	for a := range applied {
		if a {
			n++
		}
	}
	b, err := f.module.Balance(ctx, owner.id)
	if err != nil || n != 1 || b.AvailableMinor < 99 || b.AvailableMinor > 100 || b.Suspended || b.FundedUntil == nil || !b.FundedUntil.After(b.AsOf) || b.ActiveMonitors != 1 {
		t.Fatalf("wallet after concurrent credits: %+v applied=%d err=%v", b, n, err)
	}
}

func TestBillingHostedTopupBoundsAndImmutableReturnTarget(t *testing.T) {
	f := newBillingFixture(t)
	owner := f.newActor(t, "Hosted topup owner")
	p := &fakeYooKassa{mode: "production", shopID: "654321"}
	f.configureYooKassaMode(t, owner, p, "production", "654321")
	const route = "/api/v1/billing/topups"
	const key = "767082ae-bbf9-4629-8fbf-8f35a06d6b3a"
	post := func(amount int, target string) *httptest.ResponseRecorder {
		return f.request(t, owner, http.MethodPost, route, map[string]any{"amountMinor": amount, "idempotencyKey": key, "returnTarget": target})
	}
	for _, amount := range []int{-1, 0, 99, 35000001} {
		if got := post(amount, "android"); got.Code != http.StatusBadRequest {
			t.Fatalf("invalid amount %d returned %d", amount, got.Code)
		}
	}
	if got := post(100, "https://external.example"); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown return target: %d", got.Code)
	}
	first := post(35000000, "android")
	if first.Code != http.StatusCreated {
		t.Fatalf("maximum allowed topup: %d %s", first.Code, first.Body.String())
	}
	if got := post(35000000, "web"); got.Code != http.StatusConflict {
		t.Fatalf("changed return target accepted: %d", got.Code)
	}
	if got := post(35000000, "android"); got.Code != http.StatusCreated || got.Body.String() != first.Body.String() {
		t.Fatalf("idempotent topup result: %d %s", got.Code, got.Body.String())
	}
	var orders int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.orders WHERE account_id=$1`, owner.id).Scan(&orders); err != nil || orders != 1 {
		t.Fatalf("stored topups=%d err=%v", orders, err)
	}

	another := f.newActor(t, "Minimum topup owner")
	minOrder := createYooKassaOrder(t, f, another, key, 100)
	if minOrder.AmountMinor != 100 {
		t.Fatalf("minimum topup: %d", minOrder.AmountMinor)
	}
	for _, route := range []string{"/api/v1/billing/ledger", "/api/v1/billing/balance"} {
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d", route, response.Code)
		}
	}
}
