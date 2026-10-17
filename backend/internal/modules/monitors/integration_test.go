package monitors

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	accountcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
	probecontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/migrations"
)

const monitorTestOrigin = "http://localhost:3000"

type monitorFixture struct {
	pool       *pgxpool.Pool
	module     *Module
	accounts   *accounts.Module
	billing    *billing.Module
	auth       *auth.Module
	handler    http.Handler
	nextEmail  int
	accountMux sync.Mutex
}

type monitorActor struct {
	account accountcontracts.Account
	cookie  *http.Cookie
	csrf    string
}

type monitorCaptcha struct{}

func (monitorCaptcha) Verify(context.Context, string, string, string) error { return nil }

type monitorMailer struct{}

func (monitorMailer) Send(context.Context, string, string, string) error { return nil }

func newMonitorFixture(t *testing.T) *monitorFixture {
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
		t.Fatalf("connect to local test PostgreSQL: %v", err)
	}
	dbName := fmt.Sprintf("hc_monitor_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+dbName); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("create isolated monitor test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()", dbName); err != nil {
			t.Errorf("terminate isolated monitor test connections: %v", err)
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName); err != nil {
			t.Errorf("drop isolated monitor test database %s: %v", dbName, err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})
	u.Path = "/" + dbName
	poolConfig, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("parse isolated monitor test database URL: %v", err)
	}
	poolConfig.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatalf("connect to isolated monitor test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(context.Background(), pool); err != nil {
		t.Fatalf("apply isolated monitor test migrations: %v", err)
	}
	accountModule := accounts.New(pool)
	billingModule := billing.New(pool, true)
	incidentModule := incidents.New(pool)
	authModule, err := auth.New(pool, accountModule, monitorCaptcha{}, monitorMailer{}, auth.Config{Origin: monitorTestOrigin})
	if err != nil {
		t.Fatalf("create auth module for protected monitor routes: %v", err)
	}
	monitorModule := New(pool, billingModule, incidentModule, accountModule)
	mux := http.NewServeMux()
	monitorModule.Register(mux, authModule.Protect)
	return &monitorFixture{pool: pool, module: monitorModule, accounts: accountModule, billing: billingModule, auth: authModule, handler: mux}
}

func (f *monitorFixture) newActor(t *testing.T, name string) monitorActor {
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
	f.accountMux.Lock()
	f.nextEmail++
	seq := f.nextEmail
	f.accountMux.Unlock()
	email := fmt.Sprintf("monitor-%d-%s@example.test", seq, strings.ReplaceAll(account.ID, "-", ""))
	_, err = f.pool.Exec(context.Background(), `INSERT INTO auth.credentials(account_id,email,password_hash,version) VALUES($1,$2,'test-only-hash',1)`, account.ID, email)
	if err != nil {
		t.Fatalf("insert test credential: %v", err)
	}
	_, err = f.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,user_agent)
 VALUES($1,$2,$3,1,now()+interval '1 hour','monitor integration test')`, sessionID, account.ID, tokenHash[:])
	if err != nil {
		t.Fatalf("insert test session: %v", err)
	}
	cookie := &http.Cookie{Name: "hc_session", Value: rawToken, Path: "/"}
	csrfRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	csrfRequest.AddCookie(cookie)
	csrfRecorder := httptest.NewRecorder()
	f.auth.Handler().ServeHTTP(csrfRecorder, csrfRequest)
	if csrfRecorder.Code != http.StatusOK {
		t.Fatalf("fetch test session CSRF token: status=%d body=%s", csrfRecorder.Code, csrfRecorder.Body.String())
	}
	var csrfResponse struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(csrfRecorder.Body.Bytes(), &csrfResponse); err != nil || csrfResponse.CSRFToken == "" {
		t.Fatalf("decode CSRF response: %v", err)
	}
	return monitorActor{account: account, cookie: cookie, csrf: csrfResponse.CSRFToken}
}

func (f *monitorFixture) grant(t *testing.T, actor monitorActor, quota int) {
	t.Helper()
	if err := f.billing.GrantLocal(context.Background(), actor.account.ID, quota, time.Now().Add(time.Hour), "monitor integration test"); err != nil {
		t.Fatalf("grant local test entitlement: %v", err)
	}
}

func (f *monitorFixture) request(t *testing.T, actor monitorActor, method, path string, payload any, csrf, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, body)
	req.AddCookie(actor.cookie)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

func (f *monitorFixture) create(t *testing.T, actor monitorActor, name, target string, enabled bool) (monitorcontracts.Monitor, int) {
	t.Helper()
	input := monitorcontracts.MonitorInput{Name: name, URL: "https://" + target + "/health", Method: http.MethodGet, ExpectedStatus: http.StatusOK, Enabled: enabled}
	recorder := f.request(t, actor, http.MethodPost, "/api/v1/monitors", input, actor.csrf, monitorTestOrigin)
	var response struct {
		Monitor monitorcontracts.Monitor `json:"monitor"`
	}
	if recorder.Code == http.StatusCreated {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode monitor response: %v", err)
		}
	}
	return response.Monitor, recorder.Code
}

func (f *monitorFixture) complete(t *testing.T, attempt monitorcontracts.Attempt, outcome, errorCode string, at time.Time) bool {
	t.Helper()
	status := 0
	if outcome == "UP" {
		status = http.StatusOK
	} else if outcome == "DOWN" {
		status = http.StatusBadGateway
	}
	accepted, err := f.module.Complete(context.Background(), attempt, probecontracts.Result{
		RunID: attempt.Request.RunID, AttemptToken: attempt.Request.AttemptToken, ConfigVersion: attempt.Request.ConfigVersion,
		Outcome: outcome, CheckedAt: at, LatencyMS: 7, HTTPStatus: status, ErrorCode: errorCode,
	})
	if err != nil {
		t.Fatalf("complete monitor run: %v", err)
	}
	return accepted
}

func TestMonitorHTTPQuotaIsolationCSRFAndLeaseInvalidation(t *testing.T) {
	f := newMonitorFixture(t)
	owner := f.newActor(t, "Monitor Owner")
	other := f.newActor(t, "Other Owner")
	for _, rawURL := range []string{"https://port.example:80/health", "http://port.example:443/health"} {
		input := monitorcontracts.MonitorInput{Name: "wrong scheme port", URL: rawURL, Method: http.MethodGet, ExpectedStatus: http.StatusOK, Enabled: true}
		rejected := f.request(t, owner, http.MethodPost, "/api/v1/monitors", input, owner.csrf, monitorTestOrigin)
		if rejected.Code != http.StatusBadRequest {
			t.Errorf("create with wrong scheme-specific port %q status=%d, want 400", rawURL, rejected.Code)
		}
	}
	if _, status := f.create(t, owner, "blocked without quota", "one.example", true); status != http.StatusForbidden {
		t.Fatalf("create without entitlement status=%d, want 403", status)
	}
	f.grant(t, owner, 2)
	f.grant(t, other, 1)
	first, status := f.create(t, owner, "primary", "one.example", true)
	if status != http.StatusCreated || first.ID == "" {
		t.Fatalf("create entitled monitor status=%d monitor=%+v", status, first)
	}
	paused, status := f.create(t, owner, "paused", "two.example", false)
	if status != http.StatusCreated || paused.State != "PAUSED" {
		t.Fatalf("create paused monitor status=%d monitor=%+v", status, paused)
	}
	if _, status := f.create(t, owner, "over quota", "three.example", true); status != http.StatusForbidden {
		t.Fatalf("create over quota status=%d, want 403", status)
	}
	entitlement := f.request(t, owner, http.MethodGet, "/api/v1/entitlement", nil, "", "")
	var entitlementResponse struct {
		Entitlement struct {
			Quota        int `json:"quota"`
			MonitorCount int `json:"monitorCount"`
		} `json:"entitlement"`
	}
	if entitlement.Code != http.StatusOK || json.Unmarshal(entitlement.Body.Bytes(), &entitlementResponse) != nil || entitlementResponse.Entitlement.Quota != 2 || entitlementResponse.Entitlement.MonitorCount != 2 {
		t.Fatalf("entitlement response = %d %s", entitlement.Code, entitlement.Body.String())
	}
	foreign := f.request(t, other, http.MethodGet, "/api/v1/monitors/"+first.ID, nil, "", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign monitor get status=%d, want 404", foreign.Code)
	}
	foreignList := f.request(t, other, http.MethodGet, "/api/v1/monitors", nil, "", "")
	var foreignItems struct {
		Monitors []monitorcontracts.Monitor `json:"monitors"`
	}
	if foreignList.Code != http.StatusOK || json.Unmarshal(foreignList.Body.Bytes(), &foreignItems) != nil || len(foreignItems.Monitors) != 0 {
		t.Fatalf("foreign list = %d %s", foreignList.Code, foreignList.Body.String())
	}
	patchPath := "/api/v1/monitors/" + first.ID
	if noOrigin := f.request(t, owner, http.MethodPatch, patchPath, monitorcontracts.Patch{Version: first.Version}, owner.csrf, "http://attacker.example"); noOrigin.Code != http.StatusForbidden {
		t.Fatalf("patch with foreign Origin status=%d, want 403", noOrigin.Code)
	}
	if noCSRF := f.request(t, owner, http.MethodPatch, patchPath, monitorcontracts.Patch{Version: first.Version}, "", monitorTestOrigin); noCSRF.Code != http.StatusForbidden {
		t.Fatalf("patch without CSRF status=%d, want 403", noCSRF.Code)
	}
	stale := f.request(t, owner, http.MethodPatch, patchPath, monitorcontracts.Patch{Version: first.Version + 1}, owner.csrf, monitorTestOrigin)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale patch status=%d, want 409", stale.Code)
	}
	attempt, err := f.module.Claim(context.Background())
	if err != nil || attempt == nil || attempt.MonitorID != first.ID {
		t.Fatalf("claim before config change = (%+v, %v)", attempt, err)
	}
	newURL := "https://changed.example/health"
	updatedRecorder := f.request(t, owner, http.MethodPatch, patchPath, monitorcontracts.Patch{Version: first.Version, URL: &newURL}, owner.csrf, monitorTestOrigin)
	var updatedResponse struct {
		Monitor monitorcontracts.Monitor `json:"monitor"`
	}
	if updatedRecorder.Code != http.StatusOK || json.Unmarshal(updatedRecorder.Body.Bytes(), &updatedResponse) != nil || updatedResponse.Monitor.Version != first.Version+1 {
		t.Fatalf("config update = %d %s", updatedRecorder.Code, updatedRecorder.Body.String())
	}
	if accepted := f.complete(t, *attempt, "DOWN", "request_failed", time.Now()); accepted {
		t.Fatal("result from lease invalidated by config update was accepted")
	}
	results := f.request(t, owner, http.MethodGet, patchPath+"/results", nil, "", "")
	var resultList struct {
		Results []monitorcontracts.Result `json:"results"`
	}
	if results.Code != http.StatusOK || json.Unmarshal(results.Body.Bytes(), &resultList) != nil || len(resultList.Results) != 0 {
		t.Fatalf("stale attempt produced results: %d %s", results.Code, results.Body.String())
	}
	secondAttempt, err := f.module.Claim(context.Background())
	if err != nil || secondAttempt == nil {
		t.Fatalf("claim for pause invalidation: (%+v, %v)", secondAttempt, err)
	}
	pausedEnabled := false
	pauseResponse := f.request(t, owner, http.MethodPatch, patchPath, monitorcontracts.Patch{Version: updatedResponse.Monitor.Version, Enabled: &pausedEnabled}, owner.csrf, monitorTestOrigin)
	if pauseResponse.Code != http.StatusOK {
		t.Fatalf("pause leased monitor = %d %s", pauseResponse.Code, pauseResponse.Body.String())
	}
	if f.complete(t, *secondAttempt, "UP", "", time.Now()) {
		t.Fatal("result from a paused monitor was accepted")
	}
	pausedEnabled = true
	unpauseResponse := f.request(t, owner, http.MethodPatch, patchPath, monitorcontracts.Patch{Version: updatedResponse.Monitor.Version + 1, Enabled: &pausedEnabled}, owner.csrf, monitorTestOrigin)
	if unpauseResponse.Code != http.StatusOK {
		t.Fatalf("unpause monitor = %d %s", unpauseResponse.Code, unpauseResponse.Body.String())
	}
	deleteAttempt, err := f.module.Claim(context.Background())
	if err != nil || deleteAttempt == nil {
		t.Fatalf("claim for delete invalidation: (%+v, %v)", deleteAttempt, err)
	}
	deleted := f.request(t, owner, http.MethodDelete, patchPath, nil, owner.csrf, monitorTestOrigin)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete leased monitor = %d %s", deleted.Code, deleted.Body.String())
	}
	if f.complete(t, *deleteAttempt, "UP", "", time.Now()) {
		t.Fatal("result from a deleted monitor was accepted")
	}
}

func TestMonitorConcurrentCreateSerializesQuota(t *testing.T) {
	f := newMonitorFixture(t)
	actor := f.newActor(t, "Concurrent Owner")
	f.grant(t, actor, 1)
	const count = 8
	statuses := make(chan int, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, status := f.create(t, actor, fmt.Sprintf("concurrent-%d", i), fmt.Sprintf("c%d.example", i), true)
			statuses <- status
		}(i)
	}
	wg.Wait()
	close(statuses)
	created, rejected := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusForbidden:
			rejected++
		default:
			t.Fatalf("concurrent create returned unexpected HTTP %d", status)
		}
	}
	if created != 1 || rejected != count-1 {
		t.Fatalf("concurrent create outcomes: created=%d rejected=%d, want 1/%d", created, rejected, count-1)
	}
	var stored int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM monitors.monitors WHERE account_id=$1`, actor.account.ID).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored monitor count=%d err=%v, want exactly one", stored, err)
	}
}

func TestMonitorWorkerEnforcesGlobalAccountAndTargetConcurrency(t *testing.T) {
	f := newMonitorFixture(t)
	actors := []monitorActor{f.newActor(t, "Capacity A"), f.newActor(t, "Capacity B"), f.newActor(t, "Capacity C")}
	for _, actor := range actors {
		f.grant(t, actor, 5)
	}
	inputs := []struct {
		actor int
		host  string
	}{
		{0, "shared.example"}, {1, "shared.example"}, {0, "a-one.example"}, {0, "a-two.example"},
		{1, "b-one.example"}, {1, "b-two.example"}, {2, "c-one.example"}, {2, "c-two.example"}, {2, "c-three.example"},
	}
	for i, input := range inputs {
		if _, status := f.create(t, actors[input.actor], fmt.Sprintf("monitor-%d", i), input.host, true); status != http.StatusCreated {
			t.Fatalf("create capacity monitor %d returned %d", i, status)
		}
	}
	const workers = 12
	claimed := make(chan *monitorcontracts.Attempt, workers)
	errChan := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attempt, err := f.module.Claim(context.Background())
			if err != nil {
				errChan <- err
				return
			}
			claimed <- attempt
		}()
	}
	wg.Wait()
	close(claimed)
	close(errChan)
	for err := range errChan {
		t.Fatalf("concurrent worker claim: %v", err)
	}
	accountsByID := make(map[string]int)
	monitorsByTarget := make(map[string]int)
	totalClaims := 0
	for attempt := range claimed {
		if attempt == nil {
			continue
		}
		totalClaims++
		accountsByID[attempt.AccountID]++
		host, err := url.Parse(attempt.Request.URL)
		if err != nil {
			t.Fatal(err)
		}
		monitorsByTarget[host.Host]++
	}
	if len(accountsByID) == 0 {
		t.Fatal("concurrent worker calls did not claim any monitors")
	}
	for accountID, count := range accountsByID {
		if count > 2 {
			t.Errorf("account %s has %d active claims, want at most 2", accountID, count)
		}
	}
	for host, count := range monitorsByTarget {
		if count > 1 {
			t.Errorf("target %s has %d active claims, want at most 1", host, count)
		}
	}
	if totalClaims != 4 {
		t.Fatalf("worker accepted %d simultaneous claims, want global limit exactly 4 with enough due work", totalClaims)
	}
	var active int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM monitors.monitors WHERE lease_until>now()`).Scan(&active); err != nil || active > 4 {
		t.Fatalf("active lease count=%d err=%v, want <=4", active, err)
	}
}

func TestExpiredEntitlementStopsCreationAndWorkerChecks(t *testing.T) {
	f := newMonitorFixture(t)
	actor := f.newActor(t, "Expired Entitlement")
	f.grant(t, actor, 1)
	monitor, status := f.create(t, actor, "expires", "expired.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create monitor status=%d", status)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.entitlements SET valid_until=now()-interval '1 second' WHERE account_id=$1`, actor.account.ID); err != nil {
		t.Fatal(err)
	}
	if attempt, err := f.module.Claim(context.Background()); err != nil || attempt != nil {
		t.Fatalf("expired entitlement worker claim = (%+v, %v), want no lease", attempt, err)
	}
	list := f.request(t, actor, http.MethodGet, "/api/v1/monitors", nil, "", "")
	var response struct {
		Monitors []monitorcontracts.Monitor `json:"monitors"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &response) != nil || len(response.Monitors) != 1 || response.Monitors[0].State != "SUBSCRIPTION_REQUIRED" {
		t.Fatalf("expired entitlement monitor state = %d %s", list.Code, list.Body.String())
	}
	if _, status := f.create(t, actor, "new after expiry", "new-expired.example", true); status != http.StatusForbidden {
		t.Fatalf("create after entitlement expiry status=%d, want 403", status)
	}
	var nextCheck time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT next_check_at FROM monitors.monitors WHERE id=$1`, monitor.ID).Scan(&nextCheck); err != nil {
		t.Fatal(err)
	}
	if nextCheck.After(time.Now().Add(2 * time.Minute)) {
		t.Fatalf("expired monitor was not deferred safely: next_check_at=%s", nextCheck)
	}
}

func TestIncidentCloseReasonsDistinguishConfigurationFromRecovery(t *testing.T) {
	f := newMonitorFixture(t)
	actor := f.newActor(t, "Incident Owner")
	f.grant(t, actor, 1)
	monitor, status := f.create(t, actor, "incident monitor", "incident.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create monitor status=%d", status)
	}
	checked := time.Now().UTC().Add(-10 * time.Second)
	finish := func(outcome string, offset int) {
		t.Helper()
		if err := f.setMonitorDue(monitor.ID); err != nil {
			t.Fatal(err)
		}
		attempt := f.claimMonitor(t, monitor.ID)
		code := "request_failed"
		if outcome == "UP" {
			code = ""
		}
		if !f.complete(t, *attempt, outcome, code, checked.Add(time.Duration(offset)*time.Second)) {
			t.Fatalf("complete %s result was rejected", outcome)
		}
	}
	patch := func(p monitorcontracts.Patch) monitorcontracts.Monitor {
		t.Helper()
		recorder := f.request(t, actor, http.MethodPatch, "/api/v1/monitors/"+monitor.ID, p, actor.csrf, monitorTestOrigin)
		var response struct {
			Monitor monitorcontracts.Monitor `json:"monitor"`
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("patch monitor status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Monitor
	}
	incidents := func() []monitorcontracts.Incident {
		t.Helper()
		recorder := f.request(t, actor, http.MethodGet, "/api/v1/monitors/"+monitor.ID+"/incidents", nil, "", "")
		var response struct {
			Incidents []monitorcontracts.Incident `json:"incidents"`
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("list incidents status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Incidents
	}
	notifications := func() (opened, closed int) {
		t.Helper()
		err := f.pool.QueryRow(context.Background(), `SELECT
 count(*) FILTER (WHERE payload->>'kind'='incident_opened'),
 count(*) FILTER (WHERE payload->>'kind'='incident_closed')
 FROM outbox.events WHERE aggregate_id=$1 AND type='NotificationRequested'`, monitor.ID).Scan(&opened, &closed)
		if err != nil {
			t.Fatal(err)
		}
		return opened, closed
	}

	finish("DOWN", 0)
	finish("DOWN", 1)
	openedIncidents := incidents()
	if len(openedIncidents) != 1 || openedIncidents[0].ClosedAt != nil {
		t.Fatalf("two DOWN checks should open one incident: %+v", openedIncidents)
	}
	newURL := "https://changed-incident.example/health"
	monitor = patch(monitorcontracts.Patch{Version: monitor.Version, URL: &newURL})
	changed := incidents()
	if len(changed) != 1 || changed[0].ClosedAt == nil || changed[0].CloseReason != "config_changed" {
		t.Fatalf("configuration change should close the old incident silently: %+v", changed)
	}
	finish("UP", 2)
	changed = incidents()
	opened, closed := notifications()
	if changed[0].CloseReason != "config_changed" || opened != 1 || closed != 0 {
		t.Fatalf("UP after config change must not report recovery: incidents=%+v opened=%d closed=%d", changed, opened, closed)
	}

	finish("DOWN", 3)
	finish("DOWN", 4)
	secondIncident := incidents()
	if len(secondIncident) != 2 || secondIncident[0].ClosedAt != nil {
		t.Fatalf("new DOWN streak should open a second incident: %+v", secondIncident)
	}
	disabled := false
	_ = patch(monitorcontracts.Patch{Version: 2, Enabled: &disabled})
	paused := incidents()
	if len(paused) != 2 || paused[0].ClosedAt == nil || paused[0].CloseReason != "paused" {
		t.Fatalf("pausing should close the open incident with reason paused: %+v", paused)
	}
	opened, closed = notifications()
	if opened != 2 || closed != 0 {
		t.Fatalf("pause must not emit a recovery notification: opened=%d closed=%d", opened, closed)
	}
	enabled := true
	monitorState := patch(monitorcontracts.Patch{Version: 3, Enabled: &enabled})
	monitor = monitorState
	finish("DOWN", 5)
	finish("DOWN", 6)
	active := incidents()
	if len(active) != 3 || active[0].ClosedAt != nil {
		t.Fatalf("resumed monitor should open a new incident after two DOWN checks: %+v", active)
	}
	name := "renamed without changing the check"
	monitor = patch(monitorcontracts.Patch{Version: monitor.Version, Name: &name})
	active = incidents()
	if len(active) != 3 || active[0].ClosedAt != nil {
		t.Fatalf("name-only update should preserve open incident: %+v", active)
	}
	finish("UP", 7)
	recovered := incidents()
	if len(recovered) != 3 || recovered[0].ClosedAt == nil || recovered[0].CloseReason != "recovered" {
		t.Fatalf("actual UP should recover the preserved incident: %+v", recovered)
	}
	opened, closed = notifications()
	if opened != 3 || closed != 1 {
		t.Fatalf("only actual UP should emit recovery: opened=%d closed=%d", opened, closed)
	}
}

func TestMonitorLeaseReclaimResultOrderingIncidentsAndRetention(t *testing.T) {
	f := newMonitorFixture(t)
	actor := f.newActor(t, "Worker Owner")
	f.grant(t, actor, 2)
	monitor, status := f.create(t, actor, "worker", "worker.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create monitor status=%d", status)
	}
	oldAttempt, err := f.module.Claim(context.Background())
	if err != nil || oldAttempt == nil {
		t.Fatalf("initial claim = (%+v, %v)", oldAttempt, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE monitors.monitors SET lease_until=now()-interval '1 second' WHERE id=$1`, monitor.ID); err != nil {
		t.Fatal(err)
	}
	newAttempt, err := f.module.Claim(context.Background())
	if err != nil || newAttempt == nil || newAttempt.Request.AttemptToken == oldAttempt.Request.AttemptToken {
		t.Fatalf("reclaimed lease = (%+v, %v), want fresh attempt", newAttempt, err)
	}
	base := time.Now().UTC().Add(-10 * time.Second)
	if accepted := f.complete(t, *oldAttempt, "DOWN", "request_failed", base); accepted {
		t.Fatal("late result from expired attempt was accepted")
	}
	if !f.complete(t, *newAttempt, "DOWN", "request_failed", base) {
		t.Fatal("current DOWN result was rejected")
	}
	if f.complete(t, *newAttempt, "DOWN", "request_failed", base.Add(time.Second)) {
		t.Fatal("duplicate completion was accepted twice")
	}
	if err := f.setMonitorDue(monitor.ID); err != nil {
		t.Fatal(err)
	}
	unknownAttempt := f.claimMonitor(t, monitor.ID)
	if !f.complete(t, *unknownAttempt, "UNKNOWN", "probe_unavailable", base.Add(2*time.Second)) {
		t.Fatal("internal probe failure UNKNOWN result was rejected")
	}
	f.assertIncidentCount(t, actor, monitor.ID, 0)
	if err := f.setMonitorDue(monitor.ID); err != nil {
		t.Fatal(err)
	}
	thirdAttempt := f.claimMonitor(t, monitor.ID)
	if !f.complete(t, *thirdAttempt, "DOWN", "request_failed", base.Add(3*time.Second)) {
		t.Fatal("third DOWN result was rejected")
	}
	f.assertIncidentCount(t, actor, monitor.ID, 0)
	if err := f.setMonitorDue(monitor.ID); err != nil {
		t.Fatal(err)
	}
	fourthAttempt := f.claimMonitor(t, monitor.ID)
	if !f.complete(t, *fourthAttempt, "DOWN", "request_failed", base.Add(4*time.Second)) {
		t.Fatal("fourth DOWN result was rejected")
	}
	f.assertIncidentCount(t, actor, monitor.ID, 1)
	if err := f.setMonitorDue(monitor.ID); err != nil {
		t.Fatal(err)
	}
	upAttempt := f.claimMonitor(t, monitor.ID)
	if !f.complete(t, *upAttempt, "UP", "", base.Add(5*time.Second)) {
		t.Fatal("recovery UP result was rejected")
	}
	incidentsRecorder := f.request(t, actor, http.MethodGet, "/api/v1/monitors/"+monitor.ID+"/incidents", nil, "", "")
	var incidentResponse struct {
		Incidents []monitorcontracts.Incident `json:"incidents"`
	}
	if incidentsRecorder.Code != http.StatusOK || json.Unmarshal(incidentsRecorder.Body.Bytes(), &incidentResponse) != nil || len(incidentResponse.Incidents) != 1 || incidentResponse.Incidents[0].ClosedAt == nil {
		t.Fatalf("closed incident response = %d %s", incidentsRecorder.Code, incidentsRecorder.Body.String())
	}
	resultsRecorder := f.request(t, actor, http.MethodGet, "/api/v1/monitors/"+monitor.ID+"/results", nil, "", "")
	var resultsResponse struct {
		Results []monitorcontracts.Result `json:"results"`
	}
	if resultsRecorder.Code != http.StatusOK || json.Unmarshal(resultsRecorder.Body.Bytes(), &resultsResponse) != nil || len(resultsResponse.Results) != 5 {
		t.Fatalf("result list = %d %s", resultsRecorder.Code, resultsRecorder.Body.String())
	}
	var unknownCount, latencyCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT unknown_count,latency_count FROM monitors.daily_statistics WHERE monitor_id=$1`, monitor.ID).Scan(&unknownCount, &latencyCount); err != nil || unknownCount != 1 || latencyCount != 4 {
		t.Fatalf("statistics unknown=%d latency=%d err=%v", unknownCount, latencyCount, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE monitors.results SET accepted_at=now()-interval '8 days' WHERE id=(SELECT id FROM monitors.results WHERE monitor_id=$1 ORDER BY sequence LIMIT 1)`, monitor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE monitors.daily_statistics SET day=(now() AT TIME ZONE 'UTC')::date-100 WHERE monitor_id=$1`, monitor.ID); err != nil {
		t.Fatal(err)
	}
	var outboxIDs [3]string
	for i := range outboxIDs {
		outboxIDs[i], err = outbox.ID()
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, processedAt := range []string{"NULL", "now()-interval '2 days'", "now()-interval '100 days'"} {
		query := `INSERT INTO outbox.events(id,aggregate_id,account_id,type,payload,created_at,processed_at)
 VALUES ($1,$2,$3,'RetentionTest','{}'::jsonb,now()-interval '100 days',` + processedAt + `)`
		if _, err := f.pool.Exec(context.Background(), query, outboxIDs[i], monitor.ID, actor.account.ID); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.module.Cleanup(context.Background(), tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("monitor retention cleanup: %v", err)
	}
	if err := auth.Cleanup(context.Background(), tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("auth cleanup alongside monitoring cleanup: %v", err)
	}
	if err := outbox.Cleanup(context.Background(), tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("outbox retention cleanup: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	var results, statistics, sessions, credentials, monitors int
	queries := []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT count(*) FROM monitors.results WHERE monitor_id=$1`, []any{monitor.ID}, &results},
		{`SELECT count(*) FROM monitors.daily_statistics WHERE monitor_id=$1`, []any{monitor.ID}, &statistics},
		{`SELECT count(*) FROM auth.sessions WHERE account_id=$1 AND revoked_at IS NULL`, []any{actor.account.ID}, &sessions},
		{`SELECT count(*) FROM auth.credentials WHERE account_id=$1`, []any{actor.account.ID}, &credentials},
		{`SELECT count(*) FROM monitors.monitors WHERE id=$1`, []any{monitor.ID}, &monitors},
	}
	for _, query := range queries {
		if err := f.pool.QueryRow(context.Background(), query.query, query.args...).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if results != 4 || statistics != 0 || sessions != 1 || credentials != 1 || monitors != 1 {
		t.Fatalf("post-cleanup counts results=%d stats=%d sessions=%d credentials=%d monitors=%d", results, statistics, sessions, credentials, monitors)
	}
	for i, wantPresent := range []bool{true, true, false} {
		var exists bool
		if err := f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM outbox.events WHERE id=$1)`, outboxIDs[i]).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != wantPresent {
			t.Fatalf("outbox event %s retained=%v, want %v", outboxIDs[i], exists, wantPresent)
		}
	}
}

func (f *monitorFixture) setMonitorDue(id string) error {
	_, err := f.pool.Exec(context.Background(), `UPDATE monitors.monitors SET next_check_at=now()-interval '1 second' WHERE id=$1`, id)
	return err
}

func (f *monitorFixture) claimMonitor(t *testing.T, id string) *monitorcontracts.Attempt {
	t.Helper()
	attempt, err := f.module.Claim(context.Background())
	if err != nil || attempt == nil || attempt.MonitorID != id {
		t.Fatalf("claim monitor %s = (%+v, %v)", id, attempt, err)
	}
	return attempt
}

func (f *monitorFixture) assertIncidentCount(t *testing.T, actor monitorActor, monitorID string, want int) {
	t.Helper()
	recorder := f.request(t, actor, http.MethodGet, "/api/v1/monitors/"+monitorID+"/incidents", nil, "", "")
	var response struct {
		Incidents []monitorcontracts.Incident `json:"incidents"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || len(response.Incidents) != want {
		t.Fatalf("incident count response = %d %s, want %d", recorder.Code, recorder.Body.String(), want)
	}
}
