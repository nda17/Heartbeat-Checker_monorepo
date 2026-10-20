package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	accountcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications"
)

type testAccountDeletion struct {
	accounts   *accounts.Module
	billing    *billing.Module
	monitors   *monitors.Module
	afterFence func()
}

func newTestAccountDeletion(pool *pgxpool.Pool) testAccountDeletion {
	accountModule := accounts.New(pool)
	billingModule := billing.New(pool, false)
	return testAccountDeletion{
		accounts: accountModule,
		billing:  billingModule,
		monitors: monitors.New(pool, billingModule, incidents.New(pool), accountModule),
	}
}

func (d testAccountDeletion) LockAccountTx(ctx context.Context, tx pgx.Tx, accountID string) (accountcontracts.Account, error) {
	if _, err := d.billing.Lock(ctx, tx, accountID); err != nil {
		return accountcontracts.Account{}, err
	}
	return d.accounts.LockAdminAccount(ctx, tx, accountID)
}

func (d testAccountDeletion) FenceAccountDeletionTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	if err := notifications.FenceAccountDeletionTx(ctx, tx, accountID); err != nil {
		return err
	}
	if d.afterFence != nil {
		d.afterFence()
	}
	return nil
}

func (d testAccountDeletion) ScrubAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	if err := d.billing.DeleteAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	if err := d.monitors.DeleteAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	if err := notifications.DeleteAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE admin.audit_events SET reason='',before_state='{}'::jsonb,
		after_state='{}'::jsonb,metadata='{}'::jsonb,operation_id=NULL,
		actor_session_id=NULL,target_id=NULL WHERE actor_account_id=$1 OR target_account_id=$1`, accountID); err != nil {
		return err
	}
	return d.accounts.TombstoneTx(ctx, tx, accountID)
}

func deletionAccountID(t *testing.T, f *authFixture, email string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id FROM auth.credentials WHERE email=$1`, email).Scan(&id); err != nil {
		t.Fatalf("find account for %q: %v", email, err)
	}
	return id
}

func deletionCode(t *testing.T, response interface{ Result() *http.Response }) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode API error: %v", err)
	}
	return body.Error
}

func TestDeleteAccountRequiresOriginCSRFAndCurrentAccount(t *testing.T) {
	f := newAuthFixture(t)
	const email = "delete-guards@example.test"
	f.signup(t, email)
	cookie, csrf, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	accountID := deletionAccountID(t, f, email)
	payload := map[string]string{"currentPassword": integrationPass, "expectedAccountId": accountID}

	noCSRF := f.request(t, http.MethodDelete, "/api/v1/me", payload, cookie, "", integrationOrigin, "")
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("DELETE without CSRF status=%d body=%s", noCSRF.Code, noCSRF.Body.String())
	}
	wrongOrigin := f.request(t, http.MethodDelete, "/api/v1/me", payload, cookie, csrf, "http://attacker.example", "")
	if wrongOrigin.Code != http.StatusForbidden {
		t.Fatalf("DELETE with wrong Origin status=%d body=%s", wrongOrigin.Code, wrongOrigin.Body.String())
	}
	payload["expectedAccountId"] = "00000000-0000-4000-8000-000000000001"
	staleAccount := f.request(t, http.MethodDelete, "/api/v1/me", payload, cookie, csrf, integrationOrigin, "")
	if staleAccount.Code != http.StatusConflict || deletionCode(t, staleAccount) != "conflict" {
		t.Fatalf("DELETE with stale account id status=%d body=%s", staleAccount.Code, staleAccount.Body.String())
	}
	stillValid := f.request(t, http.MethodGet, "/api/v1/me", nil, cookie, "", "", "")
	if stillValid.Code != http.StatusOK {
		t.Fatalf("guarded deletion invalidated session: status=%d body=%s", stillValid.Code, stillValid.Body.String())
	}
}

func TestDeleteAccountRejectsWrongPasswordAndAdminSelfDeletion(t *testing.T) {
	f := newAuthFixture(t)
	const userEmail = "delete-password@example.test"
	f.signup(t, userEmail)
	userCookie, userCSRF, login := f.login(t, userEmail, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	userID := deletionAccountID(t, f, userEmail)
	wrong := f.request(t, http.MethodDelete, "/api/v1/me", map[string]string{
		"currentPassword": "incorrect password", "expectedAccountId": userID,
	}, userCookie, userCSRF, integrationOrigin, "")
	if wrong.Code != http.StatusUnauthorized || deletionCode(t, wrong) != "invalid_credentials" {
		t.Fatalf("DELETE with wrong password status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	if me := f.request(t, http.MethodGet, "/api/v1/me", nil, userCookie, "", "", ""); me.Code != http.StatusOK {
		t.Fatalf("wrong password invalidated account session: status=%d", me.Code)
	}

	const adminEmail = "delete-admin@example.test"
	f.signup(t, adminEmail)
	adminID := deletionAccountID(t, f, adminEmail)
	if _, err := f.pool.Exec(context.Background(), `UPDATE accounts.accounts SET role='admin' WHERE id=$1`, adminID); err != nil {
		t.Fatal(err)
	}
	adminCookie, adminCSRF, adminLogin := f.login(t, adminEmail, integrationPass)
	if adminLogin.Code != http.StatusOK {
		t.Fatal(adminLogin.Body.String())
	}
	adminDelete := f.request(t, http.MethodDelete, "/api/v1/me", map[string]string{
		"currentPassword": integrationPass, "expectedAccountId": adminID,
	}, adminCookie, adminCSRF, integrationOrigin, "")
	if adminDelete.Code != http.StatusForbidden || deletionCode(t, adminDelete) != "admin_transfer_required" {
		t.Fatalf("admin self-delete status=%d body=%s", adminDelete.Code, adminDelete.Body.String())
	}
	var status string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM accounts.accounts WHERE id=$1`, adminID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("admin self-delete changed lifecycle state: status=%q err=%v", status, err)
	}
}

func TestDeleteOAuthOnlyAccountRequiresPasswordSetup(t *testing.T) {
	const email = "delete-oauth-only@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "delete-oauth-only-subject", Email: email, Name: "OAuth Only"})
	state, attempt := oauthStart(t, f, "login", "/app", "", nil)
	cookie := completeOAuthSignup(t, f, state, attempt, email)
	accountID := deletionAccountID(t, f, email)
	response := f.request(t, http.MethodDelete, "/api/v1/me", map[string]string{
		"currentPassword": "password is not configured", "expectedAccountId": accountID,
	}, cookie, csrfFor(cookie), integrationOrigin, "")
	if response.Code != http.StatusConflict || deletionCode(t, response) != "password_setup_required" {
		t.Fatalf("OAuth-only DELETE status=%d body=%s", response.Code, response.Body.String())
	}
	if me := f.request(t, http.MethodGet, "/api/v1/me", nil, cookie, "", "", ""); me.Code != http.StatusOK {
		t.Fatalf("OAuth-only guard invalidated account session: status=%d", me.Code)
	}
}

func TestDeleteAccountRateLimitSurvivesSessionRotation(t *testing.T) {
	f := newAuthFixture(t)
	const email = "delete-rate-limit@example.test"
	f.signup(t, email)
	cookie, csrf, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	accountID := deletionAccountID(t, f, email)
	for attempt := 0; attempt < 5; attempt++ {
		response := f.request(t, http.MethodDelete, "/api/v1/me", map[string]string{
			"currentPassword": "incorrect password", "expectedAccountId": accountID,
		}, cookie, csrf, integrationOrigin, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("wrong-password attempt %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	newCookie, newCSRF, newLogin := f.login(t, email, integrationPass)
	if newLogin.Code != http.StatusOK {
		t.Fatalf("new session login status=%d body=%s", newLogin.Code, newLogin.Body.String())
	}
	limited := f.request(t, http.MethodDelete, "/api/v1/me", map[string]string{
		"currentPassword": integrationPass, "expectedAccountId": accountID,
	}, newCookie, newCSRF, integrationOrigin, "")
	if limited.Code != http.StatusTooManyRequests || deletionCode(t, limited) != "rate_limited" {
		t.Fatalf("delete request after session rotation status=%d body=%s", limited.Code, limited.Body.String())
	}
	if me := f.request(t, http.MethodGet, "/api/v1/me", nil, newCookie, "", "", ""); me.Code != http.StatusOK {
		t.Fatalf("rate limit invalidated account session: status=%d", me.Code)
	}
}

func TestDeleteAccountRacingLoginDoesNotDeadlockOrReturnServerError(t *testing.T) {
	f := newAuthFixtureWithPoolSize(t, 4)
	const email = "delete-login-race@example.test"
	f.signup(t, email)
	deleteCookie, deleteCSRF, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	accountID := deletionAccountID(t, f, email)
	start := make(chan struct{})
	type result struct {
		name string
		resp *httptest.ResponseRecorder
	}
	results := make(chan result, 2)
	go func() {
		<-start
		results <- result{name: "delete", resp: f.concurrentRequest(http.MethodDelete, "/api/v1/me", map[string]string{
			"currentPassword": integrationPass, "expectedAccountId": accountID,
		}, deleteCookie, deleteCSRF)}
	}()
	go func() {
		<-start
		results <- result{name: "login", resp: f.concurrentRequest(http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": email, "password": integrationPass, "turnstileToken": integrationToken,
		}, nil, "")}
	}()
	close(start)
	responses := map[string]*httptest.ResponseRecorder{}
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			responses[got.name] = got.resp
		case <-time.After(20 * time.Second):
			t.Fatal("login/delete race did not complete; possible database lock deadlock")
		}
	}
	deleteStatus, loginStatus := responses["delete"].Code, responses["login"].Code
	if deleteStatus != http.StatusNoContent && deleteStatus != http.StatusConflict {
		t.Fatalf("racing DELETE status=%d body=%s", deleteStatus, responses["delete"].Body.String())
	}
	if loginStatus != http.StatusOK && loginStatus != http.StatusUnauthorized {
		t.Fatalf("racing login status=%d body=%s", loginStatus, responses["login"].Body.String())
	}
}

func TestDeleteAccountPurgesWorkspaceAndAllowsFreshRegistration(t *testing.T) {
	const email = "delete-purge@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "linked-provider-subject", Email: "unused@example.test", Name: "Unused"})
	f.signup(t, email)
	cookie, csrf, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	accountID := deletionAccountID(t, f, email)
	otherCookie, _, otherLogin := f.login(t, email, integrationPass)
	if otherLogin.Code != http.StatusOK {
		t.Fatal(otherLogin.Body.String())
	}
	reset := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{
		"email": email, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if reset.Code != http.StatusAccepted {
		t.Fatalf("password reset request status=%d body=%s", reset.Code, reset.Body.String())
	}
	state, attempt := oauthStart(t, f, "link", "/app/settings", integrationPass, cookie)
	if state == "" || attempt == nil {
		t.Fatal("link OAuth flow was not created")
	}
	const monitorID = "10000000-0000-4000-8000-000000000011"
	const orderID = "10000000-0000-4000-8000-000000000012"
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.oauth_identities(id,account_id,provider,subject)
		VALUES ('10000000-0000-4000-8000-000000000013',$1,'yandex','linked-provider-subject')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO monitors.monitors
		(id,account_id,name,url,target_key,method,expected_status)
		VALUES ($1,$2,'Private service name','https://private.example/path','private.example','GET',200)`, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.webhooks
		(id,account_id,monitor_id,enabled,encrypted_url,encrypted_secret,normalized_host)
		VALUES ('10000000-0000-4000-8000-000000000021',$1,$2,true,'encrypted private url','encrypted webhook secret','private.example')`, accountID, monitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.deliveries
		(id,event_id,account_id,monitor_id,endpoint_id,endpoint_version,sequence,body,status,deadline_at)
		VALUES ('10000000-0000-4000-8000-000000000022','10000000-0000-4000-8000-000000000023',$1,$2,
		'10000000-0000-4000-8000-000000000021',1,1,'private webhook payload','pending',now()+interval '1 hour')`, accountID, monitorID); err != nil {
		t.Fatal(err)
	}
	fingerprint := make([]byte, 32)
	for i := range fingerprint {
		fingerprint[i] = byte(i + 1)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_destinations
		(id,account_id,provider,kind,recipient_fingerprint,user_fingerprint,encrypted_target)
		VALUES ('10000000-0000-4000-8000-000000000024',$1,'telegram','private',$2,$2,'encrypted messenger recipient')`, accountID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_routes
		(id,account_id,monitor_id,provider,destination_id,enabled)
		VALUES ('10000000-0000-4000-8000-000000000025',$1,$2,'telegram','10000000-0000-4000-8000-000000000024',true)`, accountID, monitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_deliveries
		(id,event_id,account_id,monitor_id,route_id,destination_id,incident_id,route_generation,sequence,kind,encrypted_snapshot)
		VALUES ('10000000-0000-4000-8000-000000000026','10000000-0000-4000-8000-000000000027',$1,$2,
		'10000000-0000-4000-8000-000000000025','10000000-0000-4000-8000-000000000024',
		'10000000-0000-4000-8000-000000000019',1,1,'down','encrypted private messenger snapshot')`, accountID, monitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_links
		(id,account_id,provider,kind,token_hash,expires_at)
		VALUES ('10000000-0000-4000-8000-000000000028',$1,'telegram','private',$2,now()+interval '1 hour')`, accountID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	pushCredentialHash := sha256.Sum256([]byte("synthetic account deletion installation credential"))
	pushTokenFingerprint := sha256.Sum256([]byte("synthetic account deletion provider token"))
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.push_installations
		(id,credential_hash,account_id,device_label,app_version,encrypted_token,token_fingerprint,permission,status)
		VALUES ('10000000-0000-4000-8000-000000000031',$1,$2,'Deletion test phone','1.0.0','encrypted token',$3,'granted','ready')`,
		pushCredentialHash[:], accountID, pushTokenFingerprint[:]); err != nil {
		t.Fatalf("insert push installation: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.push_pairings
		(id,installation_id,code_challenge,client_state,completion_code_hash,request_fingerprint,account_id,expires_at)
		VALUES ('10000000-0000-4000-8000-000000000032','10000000-0000-4000-8000-000000000031',repeat('A',43),repeat('A',43),$1,$1,$2,now()+interval '1 hour')`,
		make([]byte, 32), accountID); err != nil {
		t.Fatalf("insert push pairing: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.push_account_settings(account_id,default_enabled) VALUES($1,true)`, accountID); err != nil {
		t.Fatalf("insert push account setting: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.push_monitor_settings(monitor_id,account_id,enabled) VALUES($1,$2,false)`, monitorID, accountID); err != nil {
		t.Fatalf("insert push monitor override: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO monitors.results
		(id,monitor_id,account_id,run_id,attempt_token,config_version,sequence,outcome,checked_at,latency_ms)
		VALUES ('10000000-0000-4000-8000-000000000014',$1,$2,'10000000-0000-4000-8000-000000000015',
		'10000000-0000-4000-8000-000000000016',1,1,'UP',now(),17)`, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO monitors.daily_statistics(monitor_id,account_id,day,up_count)
		VALUES ($1,$2,current_date,1)`, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO incidents.monitor_state(monitor_id,last_sequence,failure_count)
		VALUES ($1,1,0)`, monitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at)
		VALUES ('10000000-0000-4000-8000-000000000019',$1,$2,now()-interval '1 hour',now()-interval '50 minutes')`, monitorID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason)
		VALUES ($1,3,now()+interval '1 day','local_test','private entitlement reason')`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor)
		VALUES ($1,$2,'10000000-0000-4000-8000-000000000017','mock',1,2400)`, orderID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO billing.payment_reports(order_id) VALUES ($1)`, orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO outbox.events(id,aggregate_id,account_id,type,payload)
		VALUES ('10000000-0000-4000-8000-000000000018',$1,$1,'NotificationRequested',
		'{"url":"https://private.example/path","email":"delete-purge@example.test"}')`, accountID); err != nil {
		t.Fatal(err)
	}
	deleteResponse := f.request(t, http.MethodDelete, "/api/v1/me", map[string]string{
		"currentPassword": integrationPass, "expectedAccountId": accountID,
	}, cookie, csrf, integrationOrigin, "")
	if deleteResponse.Code != http.StatusNoContent || deleteResponse.Body.Len() != 0 {
		t.Fatalf("DELETE status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	clearedCookie := false
	for _, responseCookie := range deleteResponse.Result().Cookies() {
		if responseCookie.Name == "hc_session" && responseCookie.MaxAge < 0 {
			clearedCookie = true
		}
	}
	if !clearedCookie {
		t.Fatalf("successful DELETE did not clear session cookie: %#v", deleteResponse.Result().Cookies())
	}
	for _, oldSession := range []*http.Cookie{cookie, otherCookie} {
		if me := f.request(t, http.MethodGet, "/api/v1/me", nil, oldSession, "", "", ""); me.Code != http.StatusUnauthorized {
			t.Fatalf("old session remains valid after deletion: status=%d", me.Code)
		}
	}
	var status string
	var deletedAt *time.Time
	var storedName string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,deleted_at,name FROM accounts.accounts WHERE id=$1`, accountID).Scan(&status, &deletedAt, &storedName); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || deletedAt == nil || storedName == "Test User" {
		t.Fatalf("account was not tombstoned and anonymized: status=%q deleted_at=%v name=%q", status, deletedAt, storedName)
	}
	var surviving int
	for _, table := range []string{
		"auth.sessions", "auth.password_reset_tokens", "auth.oauth_identities", "auth.oauth_attempts",
		"monitors.monitors", "monitors.results", "monitors.daily_statistics", "incidents.incidents",
		"notifications.webhooks", "notifications.deliveries", "notifications.messenger_links",
		"notifications.messenger_destinations", "notifications.messenger_routes", "notifications.messenger_deliveries",
		"notifications.push_installations", "notifications.push_pairings", "notifications.push_account_settings", "notifications.push_monitor_settings",
		"outbox.events",
	} {
		if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE account_id=$1", accountID).Scan(&surviving); err != nil {
			if table == "auth.oauth_attempts" {
				continue
			}
			t.Fatalf("count remaining rows in %s: %v", table, err)
		}
		if surviving != 0 {
			t.Fatalf("deleted account retains %d rows in %s", surviving, table)
		}
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM incidents.monitor_state WHERE monitor_id=$1`, monitorID).Scan(&surviving); err != nil || surviving != 0 {
		t.Fatalf("deleted account retains monitor incident state: count=%d err=%v", surviving, err)
	}
	var entitlementLimit int
	var entitlementUntil time.Time
	var entitlementReason string
	if err := f.pool.QueryRow(context.Background(), `SELECT monitor_limit,valid_until,reason FROM billing.entitlements WHERE account_id=$1`, accountID).
		Scan(&entitlementLimit, &entitlementUntil, &entitlementReason); err != nil {
		t.Fatalf("read invalidated entitlement: %v", err)
	}
	if entitlementLimit != 3 || entitlementUntil.After(time.Now().Add(time.Second)) || entitlementReason == "private entitlement reason" {
		t.Fatalf("deleted account entitlement remains usable or identifiable: limit=%d until=%v reason=%q", entitlementLimit, entitlementUntil, entitlementReason)
	}
	var orderStatus, reportStatus string
	if err := f.pool.QueryRow(context.Background(), `SELECT o.status,p.status FROM billing.orders o JOIN billing.payment_reports p ON p.order_id=o.id WHERE o.id=$1`, orderID).
		Scan(&orderStatus, &reportStatus); err != nil {
		t.Fatalf("read canceled mock order/report: %v", err)
	}
	if orderStatus != "canceled" || reportStatus != "failed" {
		t.Fatalf("deleted account mock payment work remains active: order=%q report=%q", orderStatus, reportStatus)
	}
	var oldCredentialCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.credentials WHERE email=$1`, email).Scan(&oldCredentialCount); err != nil || oldCredentialCount != 0 {
		t.Fatalf("deleted email remains a credential: count=%d err=%v", oldCredentialCount, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.registration_challenges WHERE email=$1`, email).Scan(&surviving); err != nil || surviving != 0 {
		t.Fatalf("deleted email retains registration challenges: count=%d err=%v", surviving, err)
	}
	oldLogin := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": integrationPass, "turnstileToken": integrationToken,
	}, nil, "", integrationOrigin, "")
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("deleted email login status=%d, want 401", oldLogin.Code)
	}
	newVerification := f.registerOccurrence(t, email, "Re-registered User", integrationPass, 1)
	verified := f.verify(t, newVerification, integrationPass)
	if verified.Code != http.StatusNoContent {
		t.Fatalf("re-registration verification status=%d body=%s", verified.Code, verified.Body.String())
	}
	newAccountID := deletionAccountID(t, f, email)
	if newAccountID == accountID {
		t.Fatal("re-registration reused the deleted account UUID")
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing.entitlements WHERE account_id=$1`, newAccountID).Scan(&surviving); err != nil || surviving != 0 {
		t.Fatalf("new account inherited quota: count=%d err=%v", surviving, err)
	}
	newCookie, _, newLogin := f.login(t, email, integrationPass)
	if newLogin.Code != http.StatusOK {
		t.Fatalf("login after re-registration status=%d body=%s", newLogin.Code, newLogin.Body.String())
	}
	linkState, linkAttempt := oauthStart(t, f, "link", "/app/settings", integrationPass, newCookie)
	linked := f.oauthCallback(t, linkState, linkAttempt, newCookie)
	if linked.Code != http.StatusSeeOther {
		t.Fatalf("link deleted provider identity to new UUID status=%d location=%q", linked.Code, linked.Header().Get("Location"))
	}
	var linkedAccount string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id FROM auth.oauth_identities WHERE provider='yandex' AND subject='linked-provider-subject'`).Scan(&linkedAccount); err != nil || linkedAccount != newAccountID {
		t.Fatalf("old OAuth subject was not attachable to new account: account=%q err=%v", linkedAccount, err)
	}
}

func TestDeleteAccountFencesConfirmedPairingBeforeBootstrapCanRebindIt(t *testing.T) {
	fenced := make(chan struct{})
	release := make(chan struct{})
	f := newAuthFixtureWithPoolSizeAndPrefixAndDeletionFence(t, 8, "hc_auth_push_delete_race_", func() {
		close(fenced)
		<-release
	})

	const accountAEmail = "push-pair-owner-delete@example.test"
	const accountBEmail = "push-installation-owner@example.test"
	f.signup(t, accountAEmail)
	f.signup(t, accountBEmail)
	cookieA, csrfA, loginA := f.login(t, accountAEmail, integrationPass)
	cookieB, _, loginB := f.login(t, accountBEmail, integrationPass)
	if loginA.Code != http.StatusOK || loginB.Code != http.StatusOK {
		t.Fatalf("account logins failed: A=%d B=%d", loginA.Code, loginB.Code)
	}
	accountA := deletionAccountID(t, f, accountAEmail)
	accountB := deletionAccountID(t, f, accountBEmail)
	var sessionA, sessionB string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM auth.sessions WHERE account_id=$1 AND revoked_at IS NULL`, accountA).Scan(&sessionA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM auth.sessions WHERE account_id=$1 AND revoked_at IS NULL`, accountB).Scan(&sessionB); err != nil {
		t.Fatal(err)
	}

	const installationID = "10000000-0000-4000-8000-000000000031"
	const confirmedPairingID = "10000000-0000-4000-8000-000000000032"
	const secret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	credential := installationID + "." + secret
	credentialHash := sha256.Sum256([]byte(credential))
	tokenFingerprint := sha256.Sum256([]byte("synthetic provider token"))
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.push_installations
		(id,credential_hash,account_id,session_id,device_label,app_version,encrypted_token,token_fingerprint,
		 registration_version,binding_generation,permission,status)
		VALUES($1,$2,$3,$4,'Synthetic device','1.0.0','encrypted token',$5,2,1,'granted','ready')`,
		installationID, credentialHash[:], accountB, sessionB, tokenFingerprint[:]); err != nil {
		t.Fatalf("insert account B installation: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.push_pairings
		(id,installation_id,code_challenge,client_state,completion_code_hash,request_fingerprint,
		 account_id,session_id,expires_at)
		VALUES($1,$2,$3,$3,$4,$4,$5,$6,now()+interval '5 minutes')`,
		confirmedPairingID, installationID, secret, tokenFingerprint[:], accountA, sessionA); err != nil {
		t.Fatalf("insert pending account A confirmation for account B installation: %v", err)
	}

	pushService, err := notifications.NewPush(f.pool, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", notifications.PushConfig{
		Enabled:   true,
		ProjectID: "account-deletion-race-test",
		Origin:    integrationOrigin,
		SessionLease: func(ctx context.Context, tx pgx.Tx, accountID, sessionID string) (time.Time, error) {
			return SessionLeaseTx(ctx, tx, accountID, sessionID)
		},
	}, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create push service: %v", err)
	}

	deleteDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		deleteDone <- f.concurrentRequest(http.MethodDelete, "/api/v1/me", map[string]string{
			"currentPassword": integrationPass, "expectedAccountId": accountA,
		}, cookieA, csrfA)
	}()
	select {
	case <-fenced:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("account deletion did not reach the push installation fence")
	}

	bootstrapDone := make(chan struct {
		pair notifications.PushPairingResult
		err  error
	}, 1)
	go func() {
		challenge := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		pair, createErr := pushService.CreatePairing(context.Background(), credential, "198.51.100.121", notifications.PushPairingInput{
			CodeChallenge: challenge,
			ClientState:   challenge,
			DeviceLabel:   "Synthetic device",
			AppVersion:    "1.0.0",
		})
		bootstrapDone <- struct {
			pair notifications.PushPairingResult
			err  error
		}{pair, createErr}
	}()
	select {
	case result := <-bootstrapDone:
		close(release)
		t.Fatalf("bootstrap passed the account deletion installation fence: pair=%+v err=%v", result.pair, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	var deleteResponse *httptest.ResponseRecorder
	select {
	case deleteResponse = <-deleteDone:
	case <-time.After(10 * time.Second):
		t.Fatal("account deletion did not finish after releasing the test fence")
	}
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("account A deletion status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var created struct {
		pair notifications.PushPairingResult
		err  error
	}
	select {
	case created = <-bootstrapDone:
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap did not resume after account deletion committed")
	}
	if created.err != nil || created.pair.Pairing.ID == "" || created.pair.Installation.ID != installationID {
		t.Fatalf("post-delete bootstrap = (%+v, %v), want a fresh pending pairing on account B installation", created.pair, created.err)
	}

	var installationOwner, installationSession string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id::text,session_id::text FROM notifications.push_installations WHERE id=$1`, installationID).Scan(&installationOwner, &installationSession); err != nil || installationOwner != accountB || installationSession != sessionB {
		t.Fatalf("account B installation changed during account A deletion: owner=%q session=%q err=%v", installationOwner, installationSession, err)
	}
	var oldPairings, newOwner, accountASessions int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.push_pairings WHERE id=$1`, confirmedPairingID).Scan(&oldPairings); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.push_pairings WHERE id=$1 AND account_id IS NULL AND session_id IS NULL`, created.pair.Pairing.ID).Scan(&newOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.sessions WHERE account_id=$1`, accountA).Scan(&accountASessions); err != nil {
		t.Fatal(err)
	}
	if oldPairings != 0 || newOwner != 1 || accountASessions != 0 {
		t.Fatalf("deletion/bootstrap race left unsafe state: oldConfirmedPairings=%d newPairingWithoutOwner=%d deletedAccountSessions=%d", oldPairings, newOwner, accountASessions)
	}
	if me := f.request(t, http.MethodGet, "/api/v1/me", nil, cookieB, "", "", ""); me.Code != http.StatusOK {
		t.Fatalf("account B session was affected by account A deletion: status=%d", me.Code)
	}
}

func TestDeleteAccountRollsBackAllChangesWhenTransactionFails(t *testing.T) {
	f := newAuthFixture(t)
	const email = "delete-rollback@example.test"
	f.signup(t, email)
	cookie, csrf, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	accountID := deletionAccountID(t, f, email)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO monitors.monitors
		(id,account_id,name,url,target_key,method,expected_status)
		VALUES ('10000000-0000-4000-8000-000000000001',$1,'Rollback monitor','https://example.test','example.test','GET',200)`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_account_deletion() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.status='deleted' THEN RAISE EXCEPTION 'synthetic account update failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_account_deletion BEFORE UPDATE ON accounts.accounts FOR EACH ROW EXECUTE FUNCTION reject_account_deletion()`); err != nil {
		t.Fatal(err)
	}
	response := f.request(t, http.MethodDelete, "/api/v1/me", map[string]string{
		"currentPassword": integrationPass, "expectedAccountId": accountID,
	}, cookie, csrf, integrationOrigin, "")
	if response.Code == http.StatusNoContent {
		t.Fatal("DELETE succeeded despite a database failure in the lifecycle transaction")
	}
	var status string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM accounts.accounts WHERE id=$1`, accountID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("failed deletion did not roll back account state: status=%q err=%v", status, err)
	}
	var monitors int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM monitors.monitors WHERE account_id=$1`, accountID).Scan(&monitors); err != nil || monitors != 1 {
		t.Fatalf("failed deletion did not restore monitor rows: count=%d err=%v", monitors, err)
	}
	if me := f.request(t, http.MethodGet, "/api/v1/me", nil, cookie, "", "", ""); me.Code != http.StatusOK {
		t.Fatalf("failed deletion invalidated session: status=%d body=%s", me.Code, me.Body.String())
	}
}
