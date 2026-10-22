package monitors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	contracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications"
)

type monitorPageResponse struct {
	Monitors   []contracts.ListedMonitor `json:"monitors"`
	Pagination contracts.Pagination      `json:"pagination"`
	Summary    contracts.MonitorSummary  `json:"summary"`
}

func readMonitorPage(t *testing.T, responseBody []byte) monitorPageResponse {
	t.Helper()
	var result monitorPageResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		t.Fatalf("decode monitor page: %v; body=%s", err, responseBody)
	}
	return result
}

func TestMonitorPagedListSearchOrderingQuotaSummaryAndClamp(t *testing.T) {
	f := newMonitorFixture(t)
	owner := f.newActor(t, "Paged Monitor Owner")
	other := f.newActor(t, "Paged Monitor Other")
	f.grant(t, owner, 10)
	f.grant(t, other, 2)

	names := []string{"Zeta", "Alpha", "Beta", "Gamma", "Special %_ 東京", "Omega"}
	monitors := make([]contracts.Monitor, 0, len(names))
	for i, name := range names {
		m, status := f.create(t, owner, name, fmt.Sprintf("list-%d.example", i), i != 0)
		if status != http.StatusCreated {
			t.Fatalf("create %q status=%d", name, status)
		}
		monitors = append(monitors, m)
	}
	if _, status := f.create(t, other, "Foreign only", "foreign-list.example", true); status != http.StatusCreated {
		t.Fatalf("create foreign monitor status=%d", status)
	}
	base := time.Now().UTC().Add(-5 * time.Minute)
	for i, monitor := range monitors {
		outcome := "UNKNOWN"
		var checked any
		switch monitor.Name {
		case "Alpha":
			outcome, checked = "UP", base.Add(time.Minute)
		case "Beta":
			outcome, checked = "DOWN", base.Add(2*time.Minute)
		case "Gamma":
			outcome, checked = "UP", base.Add(-time.Hour)
		case "Special %_ 東京":
			outcome, checked = "UP", base.Add(4*time.Minute)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE monitors.monitors SET created_at=$1,outcome=$2,last_checked_at=$3 WHERE id=$4`, base.Add(time.Duration(i)*time.Minute), outcome, checked, monitor.ID); err != nil {
			t.Fatalf("set deterministic monitor state for %q: %v", monitor.Name, err)
		}
	}
	// Paused Zeta does not consume an enabled slot. Four enabled monitors fit
	// the quota; the fifth enabled monitor remains visible but out of quota.
	f.grant(t, owner, 4)
	channels := testChannelReader(t, f)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.webhooks(id,account_id,monitor_id,version,enabled,encrypted_url,encrypted_secret,normalized_host) VALUES
	 ('30000000-0000-4000-8000-000000000001',$1,$2,1,true,'private-url-ciphertext-marker','private-secret-ciphertext-marker','receiver.example.test')`, owner.account.ID, monitors[1].ID); err != nil {
		t.Fatalf("seed webhook channel: %v", err)
	}
	destinationID := "40000000-0000-4000-8000-000000000001"
	routeID := "50000000-0000-4000-8000-000000000001"
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_destinations(id,account_id,provider,kind,recipient_fingerprint,user_fingerprint,encrypted_target) VALUES($1,$2,'telegram','private',decode(repeat('11',32),'hex'),decode(repeat('22',32),'hex'),'private-chat-ciphertext-marker')`, destinationID, owner.account.ID); err != nil {
		t.Fatalf("seed messenger destination: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_routes(id,account_id,monitor_id,provider,destination_id,enabled) VALUES($1,$2,$3,'telegram',$4,true)`, routeID, owner.account.ID, monitors[2].ID, destinationID); err != nil {
		t.Fatalf("seed enabled messenger route: %v", err)
	}
	f.handler = testHandlerWithChannels(t, f, channels)

	legacy := f.request(t, owner, http.MethodGet, "/api/v1/monitors", nil, "", "")
	if legacy.Code != http.StatusOK {
		t.Fatalf("legacy list status=%d: %s", legacy.Code, legacy.Body.String())
	}
	var legacyShape map[string]json.RawMessage
	if err := json.Unmarshal(legacy.Body.Bytes(), &legacyShape); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacyShape["pagination"]; ok {
		t.Fatalf("legacy list unexpectedly changed shape: %s", legacy.Body.String())
	}
	var oldList struct {
		Monitors []contracts.Monitor `json:"monitors"`
	}
	if err := json.Unmarshal(legacy.Body.Bytes(), &oldList); err != nil {
		t.Fatal(err)
	}
	if got := monitorNames(oldList.Monitors); !reflect.DeepEqual(got, names) {
		t.Fatalf("legacy order=%v want %v", got, names)
	}

	orders := map[string][]string{
		"created_asc":  {"Zeta", "Alpha", "Beta", "Gamma", "Special %_ 東京", "Omega"},
		"created_desc": {"Omega", "Special %_ 東京", "Gamma", "Beta", "Alpha", "Zeta"},
		"name_asc":     {"Alpha", "Beta", "Gamma", "Omega", "Special %_ 東京", "Zeta"},
		"name_desc":    {"Zeta", "Special %_ 東京", "Omega", "Gamma", "Beta", "Alpha"},
	}
	for sort, expected := range orders {
		path := "/api/v1/monitors?page=1&pageSize=100&sort=" + sort
		response := f.request(t, owner, http.MethodGet, path, nil, "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("sort %s status=%d: %s", sort, response.Code, response.Body.String())
		}
		page := readMonitorPage(t, response.Body.Bytes())
		if got := listedMonitorNames(page.Monitors); !reflect.DeepEqual(got, expected) {
			t.Errorf("sort %s order=%v want %v", sort, got, expected)
		}
		if page.Pagination.Total != len(names) || page.Summary.Total != len(names) {
			t.Errorf("sort %s totals pagination=%d summary=%d", sort, page.Pagination.Total, page.Summary.Total)
		}
		channelMap := map[string][]string{}
		for _, monitor := range page.Monitors {
			channelMap[monitor.Name] = monitor.NotificationChannels
		}
		if !reflect.DeepEqual(channelMap["Alpha"], []string{"webhook"}) || !reflect.DeepEqual(channelMap["Beta"], []string{"telegram"}) || len(channelMap["Gamma"]) != 0 {
			t.Errorf("sort %s active channels=%v", sort, channelMap)
		}
		for _, marker := range []string{"receiver.example.test", "private-secret-ciphertext-marker", "private-chat-ciphertext-marker"} {
			if strings.Contains(response.Body.String(), marker) {
				t.Errorf("paged list leaked notification target material %q", marker)
			}
		}
	}

	filtered := f.request(t, owner, http.MethodGet, "/api/v1/monitors?page=1&pageSize=25&sort=created_desc&q=gamma", nil, "", "")
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered list status=%d: %s", filtered.Code, filtered.Body.String())
	}
	filteredPage := readMonitorPage(t, filtered.Body.Bytes())
	if got := listedMonitorNames(filteredPage.Monitors); !reflect.DeepEqual(got, []string{"Gamma"}) || filteredPage.Pagination.Total != 1 {
		t.Fatalf("filtered page=%+v names=%v", filteredPage.Pagination, got)
	}
	if filteredPage.Monitors[0].State != "UNKNOWN" {
		t.Errorf("stale in-quota monitor state=%q, want UNKNOWN", filteredPage.Monitors[0].State)
	}
	if summary := filteredPage.Summary; summary.Total != 6 || summary.Paused != 1 || summary.Up != 2 || summary.Down != 1 || summary.SubscriptionRequired != 1 || summary.Unknown != 1 {
		t.Errorf("filtered query must retain global status summary: %+v", summary)
	}
	outOfQuota := f.request(t, owner, http.MethodGet, "/api/v1/monitors?q="+url.QueryEscape("Omega"), nil, "", "")
	outOfQuotaPage := readMonitorPage(t, outOfQuota.Body.Bytes())
	if outOfQuota.Code != http.StatusOK || len(outOfQuotaPage.Monitors) != 1 || outOfQuotaPage.Monitors[0].Name != "Omega" || outOfQuotaPage.Monitors[0].State != "SUBSCRIPTION_REQUIRED" {
		t.Fatalf("search match outside global quota rank: status=%d page=%+v", outOfQuota.Code, outOfQuotaPage)
	}
	// Filtering happens after the global quota rank is assigned. Omega has a
	// successful last result, but the four enabled rows before it already use
	// the account's quota, so its visible state is subscription-required.
	if _, err := f.pool.Exec(context.Background(), `UPDATE monitors.monitors SET outcome='UP',last_checked_at=now() WHERE id=$1`, monitors[5].ID); err != nil {
		t.Fatal(err)
	}
	required := f.request(t, owner, http.MethodGet, "/api/v1/monitors?page=1&pageSize=25&status=SUBSCRIPTION_REQUIRED", nil, "", "")
	requiredPage := readMonitorPage(t, required.Body.Bytes())
	if required.Code != http.StatusOK || len(requiredPage.Monitors) != 1 || requiredPage.Monitors[0].Name != "Omega" || requiredPage.Monitors[0].State != "SUBSCRIPTION_REQUIRED" {
		t.Fatalf("global quota rank must precede status filter: status=%d monitors=%+v", required.Code, requiredPage.Monitors)
	}
	if requiredPage.Summary.Total != len(names) || requiredPage.Summary.Up != 2 || requiredPage.Summary.SubscriptionRequired != 1 {
		t.Fatalf("filtered page must retain global summary: %+v", requiredPage.Summary)
	}
	upOnly := f.request(t, owner, http.MethodGet, "/api/v1/monitors?page=1&pageSize=25&status=UP", nil, "", "")
	upPage := readMonitorPage(t, upOnly.Body.Bytes())
	if upOnly.Code != http.StatusOK || len(upPage.Monitors) != 2 || upPage.Pagination.Total != 2 || upPage.Summary.Total != len(names) {
		t.Fatalf("UP filter result=%+v pagination=%+v summary=%+v", upPage.Monitors, upPage.Pagination, upPage.Summary)
	}

	for _, query := range []string{"%25_", "%E6%9D%B1%E4%BA%AC", "%20%E6%9D%B1%E4%BA%AC%20"} {
		response := f.request(t, owner, http.MethodGet, "/api/v1/monitors?q="+query, nil, "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("literal search q=%s status=%d: %s", query, response.Code, response.Body.String())
		}
		page := readMonitorPage(t, response.Body.Bytes())
		if got := listedMonitorNames(page.Monitors); !reflect.DeepEqual(got, []string{"Special %_ 東京"}) {
			t.Errorf("literal search q=%s matched %v", query, got)
		}
	}

	clamped := f.request(t, owner, http.MethodGet, "/api/v1/monitors?page=99&pageSize=2&sort=created_desc", nil, "", "")
	clampedPage := readMonitorPage(t, clamped.Body.Bytes())
	if clamped.Code != http.StatusOK || clampedPage.Pagination.Page != 3 || clampedPage.Pagination.TotalPages != 3 || len(clampedPage.Monitors) != 2 {
		t.Fatalf("oversized page clamp=%d/%d count=%d status=%d", clampedPage.Pagination.Page, clampedPage.Pagination.TotalPages, len(clampedPage.Monitors), clamped.Code)
	}

	foreign := f.request(t, other, http.MethodGet, "/api/v1/monitors?page=1&pageSize=25", nil, "", "")
	foreignPage := readMonitorPage(t, foreign.Body.Bytes())
	if foreign.Code != http.StatusOK || len(foreignPage.Monitors) != 1 || foreignPage.Monitors[0].Name != "Foreign only" || foreignPage.Summary.Total != 1 || len(foreignPage.Monitors[0].NotificationChannels) != 0 {
		t.Fatalf("tenant list isolation status=%d page=%+v summary=%+v", foreign.Code, foreignPage.Pagination, foreignPage.Summary)
	}

	deleted := f.request(t, owner, http.MethodDelete, "/api/v1/monitors/"+monitors[4].ID, nil, owner.csrf, monitorTestOrigin)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete last-created monitor status=%d: %s", deleted.Code, deleted.Body.String())
	}
	afterDelete := f.request(t, owner, http.MethodGet, "/api/v1/monitors?page=3&pageSize=2&sort=created_desc", nil, "", "")
	lastPage := readMonitorPage(t, afterDelete.Body.Bytes())
	if afterDelete.Code != http.StatusOK || lastPage.Pagination.Page != 3 || lastPage.Pagination.TotalPages != 3 || len(lastPage.Monitors) != 1 {
		t.Fatalf("post-delete page clamp page=%d totalPages=%d count=%d status=%d", lastPage.Pagination.Page, lastPage.Pagination.TotalPages, len(lastPage.Monitors), afterDelete.Code)
	}
}

func TestMonitorListQueryValidationAndEmptyPage(t *testing.T) {
	f := newMonitorFixture(t)
	owner := f.newActor(t, "Paged Validation Owner")
	f.grant(t, owner, 1)
	invalid := []string{
		"page=1&page=2",
		"unknown=1",
		"sort=random",
		"page=0",
		"pageSize=101",
		"q=" + url.QueryEscape(strings.Repeat("я", 201)),
	}
	for _, query := range invalid {
		response := f.request(t, owner, http.MethodGet, "/api/v1/monitors?"+query, nil, "", "")
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid query %q status=%d body=%s, want 400", query, response.Code, response.Body.String())
		}
	}
	empty := f.request(t, owner, http.MethodGet, "/api/v1/monitors?page=88&pageSize=10", nil, "", "")
	page := readMonitorPage(t, empty.Body.Bytes())
	if empty.Code != http.StatusOK || page.Pagination.Page != 1 || page.Pagination.TotalPages != 1 || page.Pagination.Total != 0 || len(page.Monitors) != 0 || page.Summary.Total != 0 {
		t.Fatalf("empty page status=%d response=%+v", empty.Code, page)
	}
}

func TestPagedChannelBadgesTrackCurrentRecipientAndWebhookIndependently(t *testing.T) {
	f := newMonitorFixture(t)
	owner := f.newActor(t, "Paged Channels Owner")
	f.grant(t, owner, 2)
	monitor, status := f.create(t, owner, "Channel monitor", "channels.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create monitor status=%d", status)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO notifications.webhooks(id,account_id,monitor_id,version,enabled,encrypted_url,encrypted_secret,normalized_host) VALUES
	 ('30000000-0000-4000-8000-000000000002',$1,$2,1,true,'private-url-ciphertext-marker','private-secret-ciphertext-marker','receiver.example.test')`, owner.account.ID, monitor.ID); err != nil {
		t.Fatalf("seed enabled webhook: %v", err)
	}

	verified := true
	currentEmail := "owner@example.test"
	config := notifications.MessengerConfig{
		Enabled: true, EmailEnabled: true, TelegramBotUsername: "heartbeat_qa_bot", MAXBotURL: "https://max.ru/heartbeat_qa_bot",
		TelegramWebhookSecret: strings.Repeat("t", 32), MAXWebhookSecret: strings.Repeat("m", 32),
		EmailAddressReader: func(_ context.Context, tx pgx.Tx, accountID string) (string, error) {
			if accountID != owner.account.ID {
				return "", errors.New("unexpected account in test email reader")
			}
			if !verified {
				return "", pgx.ErrNoRows
			}
			return currentEmail, nil
		},
	}
	messenger, err := notifications.NewMessenger(f.pool, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", config, false, f.billing)
	if err != nil {
		t.Fatalf("create no-outbound notification channel reader: %v", err)
	}
	destination, err := messenger.ConnectEmail(context.Background(), owner.account.ID)
	if err != nil {
		t.Fatalf("connect synthetic confirmed email destination: %v", err)
	}
	route, err := messenger.PutRoute(context.Background(), owner.account.ID, monitor.ID, "email", notifications.PutMessengerRoute{
		DestinationID: destination.ID, Enabled: true, Version: 0,
	})
	if err != nil {
		t.Fatalf("enable synthetic email route: %v", err)
	}
	f.handler = testHandlerWithChannels(t, f, messenger)
	read := func() []string {
		t.Helper()
		response := f.request(t, owner, http.MethodGet, "/api/v1/monitors?page=1&pageSize=25", nil, "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("paged channel list status=%d: %s", response.Code, response.Body.String())
		}
		page := readMonitorPage(t, response.Body.Bytes())
		if len(page.Monitors) != 1 {
			t.Fatalf("paged channel list returned %d monitors", len(page.Monitors))
		}
		for _, marker := range []string{"owner@example.test", "private-url-ciphertext-marker", "private-secret-ciphertext-marker"} {
			if strings.Contains(response.Body.String(), marker) {
				t.Fatalf("paged channel list leaked private target data %q", marker)
			}
		}
		return page.Monitors[0].NotificationChannels
	}
	assertChannels := func(want ...string) {
		t.Helper()
		if got := read(); !reflect.DeepEqual(got, want) {
			t.Fatalf("active channels=%v want %v", got, want)
		}
	}
	assertChannels("email", "webhook")

	// A verified but changed current account address invalidates the old route
	// for the badge. Reverification failure has the same fail-closed behavior.
	currentEmail = "replacement@example.test"
	assertChannels("webhook")
	currentEmail = "owner@example.test"
	verified = false
	assertChannels("webhook")
	verified = true

	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_destinations SET status='disconnected' WHERE id=$1`, destination.ID); err != nil {
		t.Fatalf("disconnect synthetic email destination: %v", err)
	}
	assertChannels("webhook")
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_destinations SET status='ready' WHERE id=$1`, destination.ID); err != nil {
		t.Fatalf("restore synthetic email destination: %v", err)
	}
	if _, err := messenger.PutRoute(context.Background(), owner.account.ID, monitor.ID, "email", notifications.PutMessengerRoute{
		DestinationID: destination.ID, Enabled: false, Version: route.Version,
	}); err != nil {
		t.Fatalf("disable synthetic email route: %v", err)
	}
	assertChannels("webhook")

	// Messenger feature flags do not disable webhook notifications.
	webhookOnly, err := notifications.NewMessenger(f.pool, "", notifications.MessengerConfig{}, false, f.billing)
	if err != nil {
		t.Fatalf("create webhook-only channel reader: %v", err)
	}
	f.handler = testHandlerWithChannels(t, f, webhookOnly)
	assertChannels("webhook")
}

func monitorNames(monitors []contracts.Monitor) []string {
	names := make([]string, 0, len(monitors))
	for _, monitor := range monitors {
		names = append(names, monitor.Name)
	}
	return names
}

func listedMonitorNames(monitors []contracts.ListedMonitor) []string {
	names := make([]string, 0, len(monitors))
	for _, monitor := range monitors {
		names = append(names, monitor.Name)
	}
	return names
}

func testChannelReader(t *testing.T, f *monitorFixture) *notifications.Messenger {
	t.Helper()
	const testEncryptionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	config := notifications.MessengerConfig{
		Enabled: true, TelegramBotUsername: "heartbeat_qa_bot", MAXBotURL: "https://max.ru/heartbeat_qa_bot",
		TelegramWebhookSecret: strings.Repeat("t", 32), MAXWebhookSecret: strings.Repeat("m", 32),
	}
	messenger, err := notifications.NewMessenger(f.pool, testEncryptionKey, config, false, f.billing)
	if err != nil {
		t.Fatalf("create no-outbound test channel reader: %v", err)
	}
	return messenger
}

func testHandlerWithChannels(t *testing.T, f *monitorFixture, channels *notifications.Messenger) http.Handler {
	t.Helper()
	module := New(f.pool, f.billing, incidents.New(f.pool, notifications.NewRecorder(channels)), f.accounts, channels)
	mux := http.NewServeMux()
	module.Register(mux, f.auth.Protect)
	return mux
}
