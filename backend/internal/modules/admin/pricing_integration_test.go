package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAdminPricingRequiresMFAAndCSRFAndUsesVersionedAuditCAS(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.seedActor(t, "a9000000-0000-4000-8000-000000000001", "b9000000-0000-4000-8000-000000000001", strings.Repeat("A", 43), "Pricing Admin", "pricing-admin@example.test", "admin", true)
	adminWithoutMFA := f.seedActor(t, "a9000000-0000-4000-8000-000000000002", "b9000000-0000-4000-8000-000000000002", strings.Repeat("B", 43), "Admin Without MFA", "pricing-no-mfa@example.test", "admin", false)
	user := f.seedActor(t, "a9000000-0000-4000-8000-000000000003", "b9000000-0000-4000-8000-000000000003", strings.Repeat("C", 43), "Pricing User", "pricing-user@example.test", "user", false)
	const endpoint = "/api/v1/admin/billing/pricing"

	get := f.request(t, admin, http.MethodGet, endpoint, nil)
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("admin pricing GET status=%d cache-control=%q body=%s", get.Code, get.Header().Get("Cache-Control"), get.Body.String())
	}
	var initial struct {
		Pricing struct {
			PricePerMonitorMinor int    `json:"pricePerMonitorMinor"`
			Currency             string `json:"currency"`
			Period               string `json:"period"`
			Version              int64  `json:"version"`
		} `json:"pricing"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &initial); err != nil || initial.Pricing.PricePerMonitorMinor != 2900 || initial.Pricing.Currency != "RUB" || initial.Pricing.Period != "month" || initial.Pricing.Version != 1 {
		t.Fatalf("initial admin pricing=%+v decode_error=%v", initial.Pricing, err)
	}
	if got := f.request(t, user, http.MethodGet, endpoint, nil); got.Code != http.StatusForbidden {
		t.Fatalf("regular user pricing GET status=%d body=%s, want forbidden", got.Code, got.Body.String())
	}
	if got := f.request(t, adminWithoutMFA, http.MethodGet, endpoint, nil); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "mfa_enrollment_required") {
		t.Fatalf("admin without MFA pricing GET status=%d body=%s", got.Code, got.Body.String())
	}
	if got := f.request(t, adminWithoutMFA, http.MethodPut, endpoint, map[string]any{"pricePerMonitorMinor": 2400, "version": 1, "reason": "update monthly monitor price"}); got.Code != http.StatusForbidden {
		t.Fatalf("admin without MFA pricing PUT status=%d body=%s", got.Code, got.Body.String())
	}
	noCSRFRequest := mustJSONRequest(t, http.MethodPut, endpoint, map[string]any{"pricePerMonitorMinor": 2400, "version": 1, "reason": "update monthly monitor price"})
	noCSRFRequest.AddCookie(&http.Cookie{Name: "hc_session", Value: admin.token, Path: "/"})
	noCSRFRequest.Header.Set("Origin", adminTestOrigin)
	noCSRFRecorder := mustServe(f, noCSRFRequest)
	if noCSRFRecorder.Code != http.StatusForbidden {
		t.Fatalf("pricing PUT without CSRF status=%d body=%s", noCSRFRecorder.Code, noCSRFRecorder.Body.String())
	}

	const reason = "approved monthly price adjustment"
	input := func(version int64, price int, why string) map[string]any {
		return map[string]any{"pricePerMonitorMinor": price, "version": version, "reason": why}
	}
	updated := f.request(t, admin, http.MethodPut, endpoint, input(1, 2400, reason))
	if updated.Code != http.StatusOK {
		t.Fatalf("pricing update status=%d body=%s", updated.Code, updated.Body.String())
	}
	var result struct {
		Pricing struct {
			PricePerMonitorMinor int   `json:"pricePerMonitorMinor"`
			Version              int64 `json:"version"`
		} `json:"pricing"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &result); err != nil || result.Pricing.PricePerMonitorMinor != 2400 || result.Pricing.Version != 2 {
		t.Fatalf("updated pricing=%+v decode_error=%v", result.Pricing, err)
	}
	var audit struct {
		Action      string          `json:"action"`
		Reason      string          `json:"reason"`
		TargetID    *string         `json:"targetId"`
		BeforeState json.RawMessage `json:"beforeState"`
		AfterState  json.RawMessage `json:"afterState"`
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT action,reason,target_id,before_state,after_state FROM admin.audit_events WHERE action='billing.pricing_updated'`).Scan(&audit.Action, &audit.Reason, &audit.TargetID, &audit.BeforeState, &audit.AfterState); err != nil {
		t.Fatalf("read pricing audit: %v", err)
	}
	var beforeState, afterState map[string]any
	if err := json.Unmarshal(audit.BeforeState, &beforeState); err != nil {
		t.Fatalf("decode pricing audit before state: %v", err)
	}
	if err := json.Unmarshal(audit.AfterState, &afterState); err != nil {
		t.Fatalf("decode pricing audit after state: %v", err)
	}
	if audit.Action != "billing.pricing_updated" || audit.Reason != reason || audit.TargetID != nil || beforeState["pricePerMonitorMinor"] != float64(2900) || afterState["pricePerMonitorMinor"] != float64(2400) {
		t.Fatalf("pricing audit=%+v", audit)
	}
	if got := f.request(t, admin, http.MethodPut, endpoint, input(1, 2500, "stale price editor")); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "version_conflict") {
		t.Fatalf("stale pricing edit status=%d body=%s", got.Code, got.Body.String())
	}
	var currentPrice, currentVersion int
	if err := f.pool.QueryRow(context.Background(), `SELECT price_per_monitor_minor,version FROM billing.pricing WHERE singleton=1`).Scan(&currentPrice, &currentVersion); err != nil || currentPrice != 2400 || currentVersion != 2 {
		t.Fatalf("stale edit changed pricing=%d version=%d err=%v", currentPrice, currentVersion, err)
	}
	var auditCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin.audit_events WHERE action='billing.pricing_updated'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("stale update audit rows=%d err=%v, want one committed change", auditCount, err)
	}

	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION public.reject_pricing_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='billing.pricing_updated' THEN RAISE EXCEPTION 'synthetic pricing audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_pricing_audit BEFORE INSERT ON admin.audit_events FOR EACH ROW EXECUTE FUNCTION public.reject_pricing_audit()`); err != nil {
		t.Fatal(err)
	}
	failedAudit := f.request(t, admin, http.MethodPut, endpoint, input(2, 2300, "must roll back with audit"))
	if failedAudit.Code < 500 {
		t.Fatalf("audit-failing pricing update status=%d body=%s, want 5xx", failedAudit.Code, failedAudit.Body.String())
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT price_per_monitor_minor,version FROM billing.pricing WHERE singleton=1`).Scan(&currentPrice, &currentVersion); err != nil || currentPrice != 2400 || currentVersion != 2 {
		t.Fatalf("audit failure did not roll back pricing=%d version=%d err=%v", currentPrice, currentVersion, err)
	}
}

func TestAdminPricingConcurrentCASCommitsOneVersionAndAudit(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.seedActor(t, "a9100000-0000-4000-8000-000000000001", "b9100000-0000-4000-8000-000000000001", strings.Repeat("D", 43), "Concurrent Pricing Admin", "pricing-race@example.test", "admin", true)
	endpoint := "/api/v1/admin/billing/pricing"
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i, price := range []int{2400, 2500} {
		wg.Add(1)
		go func(i, price int) {
			defer wg.Done()
			<-start
			response := f.request(t, admin, http.MethodPut, endpoint, map[string]any{"pricePerMonitorMinor": price, "version": 1, "reason": fmt.Sprintf("concurrent update %d", i)})
			results <- response.Code
		}(i, price)
	}
	close(start)
	wg.Wait()
	close(results)
	var succeeded, conflicted int
	for status := range results {
		switch status {
		case http.StatusOK:
			succeeded++
		case http.StatusConflict:
			conflicted++
		default:
			t.Fatalf("concurrent pricing update status=%d, want one 200 and one 409", status)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent CAS results success=%d conflict=%d, want one each", succeeded, conflicted)
	}
	var version, auditCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT version FROM billing.pricing WHERE singleton=1`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("concurrent pricing version=%d err=%v, want version 2", version, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin.audit_events WHERE action='billing.pricing_updated'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("concurrent pricing audit count=%d err=%v, want one", auditCount, err)
	}
}

func TestAdminPricingEnforcesProviderPriceBounds(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.seedActor(t, "a9200000-0000-4000-8000-000000000001", "b9200000-0000-4000-8000-000000000001", strings.Repeat("E", 43), "Price Bounds Admin", "pricing-bounds@example.test", "admin", true)
	endpoint := "/api/v1/admin/billing/pricing"
	put := func(price int, version int64) *httptest.ResponseRecorder {
		return f.request(t, admin, http.MethodPut, endpoint, map[string]any{"pricePerMonitorMinor": price, "version": version, "reason": "verify payment provider bounds"})
	}
	if response := put(99, 1); response.Code != http.StatusBadRequest {
		t.Fatalf("below-minimum price update status=%d body=%s, want 400", response.Code, response.Body.String())
	}
	if response := put(100, 1); response.Code != http.StatusOK {
		t.Fatalf("minimum provider price update status=%d body=%s, want 1 RUB accepted", response.Code, response.Body.String())
	}
	if response := put(1_000_000, 2); response.Code != http.StatusOK {
		t.Fatalf("maximum provider price update status=%d body=%s, want 10000 RUB accepted", response.Code, response.Body.String())
	}
	if response := put(1_000_001, 3); response.Code != http.StatusBadRequest {
		t.Fatalf("above-maximum price update status=%d body=%s, want 400", response.Code, response.Body.String())
	}
	var price int
	var version int64
	if err := f.pool.QueryRow(context.Background(), `SELECT price_per_monitor_minor,version FROM billing.pricing WHERE singleton=1`).Scan(&price, &version); err != nil || price != 1_000_000 || version != 3 {
		t.Fatalf("boundary rejection changed stored price=%d version=%d err=%v", price, version, err)
	}
}

func mustJSONRequest(t *testing.T, method, path string, value any) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, path, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	return request
}

func mustServe(f *adminFixture, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}
