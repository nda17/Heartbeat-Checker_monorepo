package notifications

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	incidentcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

const messengerTestOrigin = "http://localhost:3000"

type messengerTestCaptcha struct{}

func (messengerTestCaptcha) Verify(context.Context, string, string, string) error { return nil }

type messengerTestMailer struct{}

func (messengerTestMailer) Send(context.Context, string, string, authcontracts.MailContent) error {
	return nil
}

type messengerTestActor struct {
	accountID string
	cookie    *http.Cookie
	csrf      string
}

type messengerAPIFixture struct {
	webhookFixture *webhookFixture
	module         *Messenger
	handler        http.Handler
	actors         map[string]messengerTestActor
	monitors       map[string]string
}

func newMessengerAPIFixture(t *testing.T, enabled bool, accountNames ...string) *messengerAPIFixture {
	t.Helper()
	f := newWebhookFixture(t)
	m := testMessenger(t, f, enabled)
	authModule, err := auth.New(f.pool, accounts.New(f.pool), messengerTestCaptcha{}, messengerTestMailer{}, auth.Config{Origin: messengerTestOrigin})
	if err != nil {
		t.Fatalf("create auth module for messenger API: %v", err)
	}
	mux := http.NewServeMux()
	m.RegisterAPI(mux, authModule.Protect)
	m.RegisterIngest(mux)
	result := &messengerAPIFixture{webhookFixture: f, module: m, handler: mux, actors: map[string]messengerTestActor{}, monitors: map[string]string{}}
	for _, name := range accountNames {
		accountID, monitorID := f.addMonitor(t, name)
		result.actors[name] = newMessengerTestActor(t, f, authModule, accountID)
		result.monitors[name] = monitorID
	}
	return result
}

func newMessengerTestActor(t *testing.T, f *webhookFixture, authModule *auth.Module, accountID string) messengerTestActor {
	t.Helper()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	sessionID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	email := "messenger-" + strings.ReplaceAll(accountID, "-", "") + "@example.test"
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.credentials(account_id,email,password_hash,version) VALUES($1,$2,'test-only',1)`, accountID, email); err != nil {
		t.Fatalf("insert messenger auth credential: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,user_agent) VALUES($1,$2,$3,1,now()+interval '1 hour','messenger integration test')`, sessionID, accountID, hash[:]); err != nil {
		t.Fatalf("insert messenger auth session: %v", err)
	}
	cookie := &http.Cookie{Name: "hc_session", Value: token, Path: "/"}
	csrfReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	csrfReq.AddCookie(cookie)
	csrfRec := httptest.NewRecorder()
	authModule.Handler().ServeHTTP(csrfRec, csrfReq)
	if csrfRec.Code != http.StatusOK {
		t.Fatalf("fetch messenger test CSRF token: status=%d body=%s", csrfRec.Code, csrfRec.Body.String())
	}
	var csrfResponse struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(csrfRec.Body.Bytes(), &csrfResponse); err != nil || csrfResponse.CSRF == "" {
		t.Fatalf("decode messenger test CSRF token: %v", err)
	}
	return messengerTestActor{accountID: accountID, cookie: cookie, csrf: csrfResponse.CSRF}
}

func (f *messengerAPIFixture) request(t *testing.T, actor *messengerTestActor, method, path string, body any, csrf, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if actor != nil {
		req.AddCookie(actor.cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

type messengerRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn messengerRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func messengerJSONResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{}}
}

func confirmPrivateTelegramTarget(t *testing.T, f *webhookFixture, m *Messenger, accountID string, userID int64) MessengerDestination {
	t.Helper()
	link, token, _, _, err := m.CreateLink(context.Background(), accountID, "telegram", "private")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterIngest(mux)
	body := fmt.Sprintf(`{"update_id":600,"message":{"text":%q,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false}}}`, "/start "+token, userID, userID)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/telegram", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram-webhook-secret-32-bytes")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("private link webhook status=%d: %s", rec.Code, rec.Body.String())
	}
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/getMe") {
			return nil, fmt.Errorf("unexpected Telegram verification request %s", req.URL)
		}
		return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
	})}
	if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
		t.Fatalf("verify private messenger link=%t err=%v", processed, err)
	}
	destination, err := m.ConfirmLink(context.Background(), accountID, link.ID)
	if err != nil {
		t.Fatalf("confirm private messenger link: %v", err)
	}
	return destination
}

func confirmTelegramGroupTarget(t *testing.T, f *webhookFixture, m *Messenger, accountID string, userID, groupID int64) MessengerDestination {
	t.Helper()
	link, _, _, command, err := m.CreateLink(context.Background(), accountID, "telegram", "group")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterIngest(mux)
	body := fmt.Sprintf(`{"update_id":601,"message":{"text":%q,"chat":{"id":%d,"type":"supergroup"},"from":{"id":%d,"is_bot":false}}}`, command, groupID, userID)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/telegram", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram-webhook-secret-32-bytes")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("group link webhook status=%d: %s", rec.Code, rec.Body.String())
	}
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		switch method {
		case "getMe":
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
		case "getChat":
			return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"id":%d,"type":"supergroup","title":"Operations","is_forum":false}}`, groupID)), nil
		case "getChatMember":
			var input struct {
				UserID int64 `json:"user_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				return nil, err
			}
			return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"status":"administrator","user":{"id":%d}}}`, input.UserID)), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram verification method %s", method)
		}
	})}
	if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
		t.Fatalf("verify group link=%t err=%v", processed, err)
	}
	destination, err := m.ConfirmLink(context.Background(), accountID, link.ID)
	if err != nil {
		t.Fatalf("confirm group destination: %v", err)
	}
	return destination
}

func applyMessengerObservation(t *testing.T, pool *pgxpool.Pool, recorder *incidents.Module, accountID, monitorID string, sequence int64, outcome string, at time.Time, observation incidentcontracts.Observation) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE monitors.monitors SET outcome=$2,sequence=$3,last_checked_at=$4,last_http_status=$5,last_latency_ms=$6 WHERE id=$1`, monitorID, outcome, sequence, at, observation.HTTPStatus, observation.LatencyMS); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("store accepted %s sequence %d: %v", outcome, sequence, err)
	}
	if err := recorder.Apply(ctx, tx, monitorID, accountID, sequence, outcome, 1, at, observation); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply accepted %s sequence %d: %v", outcome, sequence, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit accepted %s sequence %d: %v", outcome, sequence, err)
	}
}

func TestMessengerHTTPAPIAuthorizationCSRFAndAccountIsolation(t *testing.T) {
	f := newMessengerAPIFixture(t, true, "messenger api owner", "messenger api other")
	owner := f.actors["messenger api owner"]
	other := f.actors["messenger api other"]
	createPath := "/api/v1/messenger-links"
	body := map[string]string{"provider": "telegram", "kind": "private"}
	if got := f.request(t, nil, http.MethodPost, createPath, body, "", messengerTestOrigin); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated link create status=%d, want 401", got.Code)
	}
	if got := f.request(t, &owner, http.MethodPost, createPath, body, "", messengerTestOrigin); got.Code != http.StatusForbidden {
		t.Fatalf("link create without CSRF status=%d, want 403", got.Code)
	}
	if got := f.request(t, &owner, http.MethodPost, createPath, body, owner.csrf, "https://attacker.example"); got.Code != http.StatusForbidden {
		t.Fatalf("link create with foreign Origin status=%d, want 403", got.Code)
	}
	created := f.request(t, &owner, http.MethodPost, createPath, body, owner.csrf, messengerTestOrigin)
	if created.Code != http.StatusCreated {
		t.Fatalf("authenticated link create status=%d body=%s", created.Code, created.Body.String())
	}
	var result struct {
		Link  MessengerLink `json:"link"`
		Token string        `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Link.ID == "" || result.Token == "" {
		t.Fatalf("decode created link: err=%v body=%s", err, created.Body.String())
	}
	linkPath := "/api/v1/messenger-links/" + result.Link.ID
	if got := f.request(t, &other, http.MethodGet, linkPath, nil, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("foreign account link read status=%d, want 404", got.Code)
	}
	if got := f.request(t, &owner, http.MethodGet, linkPath, nil, "", ""); got.Code != http.StatusOK {
		t.Fatalf("owner link read status=%d: %s", got.Code, got.Body.String())
	}
	if got := f.request(t, &owner, http.MethodPost, linkPath+"/confirm", map[string]any{}, owner.csrf, messengerTestOrigin); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "link_not_ready") {
		t.Fatalf("confirm unverified link status=%d body=%s, want 409 link_not_ready", got.Code, got.Body.String())
	}
	routePath := "/api/v1/monitors/" + f.monitors["messenger api owner"] + "/messenger-routes"
	if got := f.request(t, &other, http.MethodGet, routePath, nil, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("foreign account monitor route read status=%d, want 404", got.Code)
	}
}

func TestMessengerAPIDefaultDisabledIsObservableAndReadOnly(t *testing.T) {
	f := newMessengerAPIFixture(t, false, "disabled messenger")
	actor := f.actors["disabled messenger"]
	destinations := f.request(t, &actor, http.MethodGet, "/api/v1/messenger-destinations", nil, "", "")
	if destinations.Code != http.StatusOK || !strings.Contains(destinations.Body.String(), `"enabled":false`) || !strings.Contains(destinations.Body.String(), `"destinations":[]`) {
		t.Fatalf("disabled destination list status=%d body=%s", destinations.Code, destinations.Body.String())
	}
	routePath := "/api/v1/monitors/" + f.monitors["disabled messenger"] + "/messenger-routes"
	routes := f.request(t, &actor, http.MethodGet, routePath, nil, "", "")
	if routes.Code != http.StatusOK || !strings.Contains(routes.Body.String(), `"enabled":false`) || !strings.Contains(routes.Body.String(), `"routes":[]`) {
		t.Fatalf("disabled route list status=%d body=%s", routes.Code, routes.Body.String())
	}
	write := f.request(t, &actor, http.MethodPost, "/api/v1/messenger-links", map[string]string{"provider": "telegram", "kind": "private"}, actor.csrf, messengerTestOrigin)
	if write.Code != http.StatusServiceUnavailable || !strings.Contains(write.Body.String(), "temporarily_unavailable") {
		t.Fatalf("disabled write status=%d body=%s, want 503", write.Code, write.Body.String())
	}
}

func testMessenger(t *testing.T, f *webhookFixture, enabled bool) *Messenger {
	t.Helper()
	m, err := NewMessenger(f.pool, integrationWebhookKey, MessengerConfig{
		Enabled:               enabled,
		TelegramWebhookSecret: "test-telegram-webhook-secret-32-bytes",
		MAXWebhookSecret:      "test-max-webhook-secret-32-bytes-long",
		TelegramToken:         "123456:test-telegram-token",
		MAXToken:              "test-max-token",
		TelegramBotUsername:   "heartbeat_test_bot",
		MAXBotURL:             "https://max.ru/heartbeat_test_bot",
		TelegramAPIBaseURL:    "https://api.telegram.org",
	}, false, billing.New(f.pool, true))
	if err != nil {
		t.Fatalf("create test messenger: %v", err)
	}
	return m
}

func TestMessengerLinkTokenIsRandomHashedShortLivedAndAccountScoped(t *testing.T) {
	f := newWebhookFixture(t)
	owner, _ := f.addMonitor(t, "messenger owner")
	other, _ := f.addMonitor(t, "messenger other")
	m := testMessenger(t, f, true)

	link, token, startURL, command, err := m.CreateLink(context.Background(), owner, "telegram", "private")
	if err != nil {
		t.Fatalf("create Telegram private link: %v", err)
	}
	if len(token) != 43 || strings.Contains(token, "=") {
		t.Fatalf("link token length/encoding=%q, want 32 random bytes encoded as unpadded base64url", token)
	}
	rawToken, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(rawToken) != 32 {
		t.Fatalf("decode one-time link token bytes=%d err=%v, want 32 bytes", len(rawToken), err)
	}
	if link.Status != "pending" || link.Candidate != nil || link.DestinationID != nil || command != "" {
		t.Fatalf("new link=%+v command=%q, want pending private link without candidate or destination", link, command)
	}
	parsed, err := url.Parse(startURL)
	if err != nil || parsed.Host != "t.me" || parsed.Path != "/heartbeat_test_bot" || parsed.Query().Get("start") != token {
		t.Fatalf("Telegram start URL=%q err=%v", startURL, err)
	}
	var storedHash []byte
	var expires time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT token_hash,expires_at FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&storedHash, &expires); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(token))
	if string(storedHash) != string(wantHash[:]) || strings.Contains(string(storedHash), token) {
		t.Fatalf("stored link token hash does not match one-way SHA-256 token digest")
	}
	remaining := time.Until(expires)
	if remaining < 9*time.Minute || remaining > 10*time.Minute+time.Second {
		t.Fatalf("link expiry remaining=%s, want about 10 minutes", remaining)
	}
	if _, err := m.GetLink(context.Background(), other, link.ID); !errors.Is(err, ErrMessengerNotFound) {
		t.Fatalf("foreign account read link error=%v, want not found", err)
	}
	if _, err := m.ConfirmLink(context.Background(), owner, link.ID); !errors.Is(err, ErrMessengerNotReady) {
		t.Fatalf("confirm pending link error=%v, want link-not-ready", err)
	}
	second, secondToken, _, _, err := m.CreateLink(context.Background(), owner, "telegram", "private")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == link.ID || secondToken == token {
		t.Fatal("successive link creations reused an id or bearer token")
	}
}

func TestMessengerLinkKindsAndDisabledDefault(t *testing.T) {
	f := newWebhookFixture(t)
	owner, _ := f.addMonitor(t, "messenger kinds")
	disabled := testMessenger(t, f, false)
	if _, _, _, _, err := disabled.CreateLink(context.Background(), owner, "telegram", "private"); !errors.Is(err, ErrMessengerUnavailable) {
		t.Fatalf("disabled messenger CreateLink error=%v, want unavailable", err)
	}
	m := testMessenger(t, f, true)
	if _, _, _, _, err := m.CreateLink(context.Background(), owner, "max", "group"); !errors.Is(err, ErrMessengerInvalid) {
		t.Fatalf("MAX group link error=%v, want invalid kind", err)
	}
	link, token, startURL, command, err := m.CreateLink(context.Background(), owner, "telegram", "topic")
	if err != nil {
		t.Fatalf("create Telegram topic link: %v", err)
	}
	if link.Status != "pending" || token == "" || startURL != "" || command != "/connect@heartbeat_test_bot "+token {
		t.Fatalf("Telegram topic link=%+v startURL=%q command=%q", link, startURL, command)
	}
	var destinationCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_destinations`).Scan(&destinationCount); err != nil {
		t.Fatal(err)
	}
	if destinationCount != 0 {
		t.Fatalf("link creation made %d destinations before verification", destinationCount)
	}
}

func TestMessengerTelegramCommandParsingKeepsPrivateGroupAndTopicBoundaries(t *testing.T) {
	validPrivate := []byte(`{"update_id":1,"message":{"text":"/start opaque-token","chat":{"id":321,"type":"private"},"from":{"id":321,"is_bot":false}}}`)
	target, token, err := parseMessengerUpdate("telegram", validPrivate, "heartbeat_test_bot")
	if err != nil || token != "opaque-token" || target.Kind != "private" || target.ChatID != "321" || target.UserID != "321" {
		t.Fatalf("private start target=%+v token=%q err=%v", target, token, err)
	}
	group := []byte(`{"update_id":2,"message":{"text":"/connect@heartbeat_test_bot group-token","chat":{"id":-10001,"type":"supergroup"},"from":{"id":322,"is_bot":false}}}`)
	target, token, err = parseMessengerUpdate("telegram", group, "heartbeat_test_bot")
	if err != nil || token != "group-token" || target.Kind != "group" || target.ChatID != "-10001" || target.UserID != "322" || target.ThreadID != "" {
		t.Fatalf("group command target=%+v token=%q err=%v", target, token, err)
	}
	topic := []byte(`{"update_id":3,"message":{"text":"/connect@heartbeat_test_bot topic-token","message_thread_id":44,"chat":{"id":-10002,"type":"supergroup"},"from":{"id":323,"is_bot":false}}}`)
	target, token, err = parseMessengerUpdate("telegram", topic, "heartbeat_test_bot")
	if err != nil || token != "topic-token" || target.Kind != "topic" || target.ChatID != "-10002" || target.UserID != "323" || target.ThreadID != "44" {
		t.Fatalf("topic command target=%+v token=%q err=%v", target, token, err)
	}
	for name, body := range map[string][]byte{
		"private chat mismatch":           []byte(`{"update_id":4,"message":{"text":"/start token","chat":{"id":999,"type":"private"},"from":{"id":321,"is_bot":false}}}`),
		"group start is not link command": []byte(`{"update_id":5,"message":{"text":"/start token","chat":{"id":-10003,"type":"group"},"from":{"id":324,"is_bot":false}}}`),
		"wrong bot username":              []byte(`{"update_id":6,"message":{"text":"/connect@other_bot token","chat":{"id":-10004,"type":"group"},"from":{"id":325,"is_bot":false}}}`),
		"bot sender":                      []byte(`{"update_id":7,"message":{"text":"/start token","chat":{"id":326,"type":"private"},"from":{"id":326,"is_bot":true}}}`),
		"anonymous sender":                []byte(`{"update_id":8,"message":{"text":"/start token","chat":{"id":327,"type":"private"},"from":{"id":327,"is_bot":false},"sender_chat":{"id":-10005}}}`),
		"missing token":                   []byte(`{"update_id":9,"message":{"text":"/start","chat":{"id":328,"type":"private"},"from":{"id":328,"is_bot":false}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			gotTarget, gotToken, err := parseMessengerUpdate("telegram", body, "heartbeat_test_bot")
			if err != nil || gotToken != "" || gotTarget != (messengerTarget{}) {
				t.Fatalf("invalid command target=%+v token=%q err=%v, want ignored", gotTarget, gotToken, err)
			}
		})
	}
}

func TestMessengerMAXBotStartedUsesUserIdentityDistinctFromChat(t *testing.T) {
	body := []byte(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":9988,"user":{"user_id":321,"is_bot":false},"payload":"one-time-token"}`)
	target, token, err := parseMessengerUpdate("max", body, "heartbeat_test_bot")
	if err != nil || token != "one-time-token" || target.Provider != "max" || target.Kind != "private" || target.ChatID != "9988" || target.UserID != "321" || target.ChatID == target.UserID {
		t.Fatalf("MAX start target=%+v token=%q err=%v", target, token, err)
	}
	bot := []byte(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":9988,"user":{"user_id":321,"is_bot":true},"payload":"one-time-token"}`)
	if ignoredTarget, ignoredToken, err := parseMessengerUpdate("max", bot, "heartbeat_test_bot"); err != nil || ignoredToken != "" || ignoredTarget != (messengerTarget{}) {
		t.Fatalf("MAX bot start target=%+v token=%q err=%v, want ignored", ignoredTarget, ignoredToken, err)
	}
}

func TestMessengerWebhookConsumesExpectedPendingTokenOnce(t *testing.T) {
	f := newWebhookFixture(t)
	owner, _ := f.addMonitor(t, "messenger ingress")
	m := testMessenger(t, f, true)
	link, token, _, _, err := m.CreateLink(context.Background(), owner, "telegram", "private")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterIngest(mux)
	request := func(path, providerSecret, text string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"update_id":20,"message":{"text":%q,"chat":{"id":321,"type":"private"},"from":{"id":321,"is_bot":false}}}`, text)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", providerSecret)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if got := request("/webhooks/notifications/telegram", "wrong-webhook-secret-which-is-long-enough", "/start "+token); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret webhook status=%d", got.Code)
	}
	var status string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("wrong secret changed link status to %s", status)
	}
	groupBody := fmt.Sprintf(`{"update_id":21,"message":{"text":%q,"chat":{"id":-10001,"type":"group"},"from":{"id":321,"is_bot":false}}}`, "/connect@heartbeat_test_bot "+token)
	groupReq := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/telegram", strings.NewReader(groupBody))
	groupReq.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram-webhook-secret-32-bytes")
	groupRec := httptest.NewRecorder()
	mux.ServeHTTP(groupRec, groupReq)
	if groupRec.Code != http.StatusOK {
		t.Fatalf("wrong-kind token webhook status=%d", groupRec.Code)
	}
	maxBody := fmt.Sprintf(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":9988,"user":{"user_id":321,"is_bot":false},"payload":%q}`, token)
	maxReq := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/max", strings.NewReader(maxBody))
	maxReq.Header.Set("X-Max-Bot-Api-Secret", "test-max-webhook-secret-32-bytes-long")
	maxRec := httptest.NewRecorder()
	mux.ServeHTTP(maxRec, maxReq)
	if maxRec.Code != http.StatusOK {
		t.Fatalf("cross-provider token webhook status=%d", maxRec.Code)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("cross-kind/provider token was accepted: status=%s", status)
	}
	if got := request("/webhooks/notifications/telegram", "test-telegram-webhook-secret-32-bytes", "/start "+token); got.Code != http.StatusOK {
		t.Fatalf("valid token webhook status=%d: %s", got.Code, got.Body.String())
	}
	var encrypted []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT status,encrypted_observation FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&status, &encrypted); err != nil {
		t.Fatal(err)
	}
	if status != "observed" || len(encrypted) == 0 || strings.Contains(string(encrypted), token) {
		t.Fatalf("accepted link status=%s encrypted_observation_bytes=%d contains_token=%t", status, len(encrypted), strings.Contains(string(encrypted), token))
	}
	if got := request("/webhooks/notifications/telegram", "test-telegram-webhook-secret-32-bytes", "/start "+token); got.Code != http.StatusOK {
		t.Fatalf("duplicate token webhook status=%d: %s", got.Code, got.Body.String())
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "observed" {
		t.Fatalf("duplicate token changed consumed link status to %s", status)
	}
}

func TestMessengerPrivateLinkCandidateCanBeConfirmedOnlyByOwnerAndTokenIsConsumed(t *testing.T) {
	f := newMessengerAPIFixture(t, true, "private link owner", "private link other")
	owner := f.actors["private link owner"]
	other := f.actors["private link other"]
	m := f.module
	link, token, _, _, err := m.CreateLink(context.Background(), owner.accountID, "telegram", "private")
	if err != nil {
		t.Fatal(err)
	}
	webhook := http.NewServeMux()
	m.RegisterIngest(webhook)
	postStart := func(update int, startToken string) int {
		body := fmt.Sprintf(`{"update_id":%d,"message":{"text":%q,"chat":{"id":321,"type":"private"},"from":{"id":321,"is_bot":false}}}`, update, "/start "+startToken)
		req := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/telegram", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram-webhook-secret-32-bytes")
		rec := httptest.NewRecorder()
		webhook.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := postStart(30, token); code != http.StatusOK {
		t.Fatalf("start webhook status=%d", code)
	}
	if code := postStart(31, token); code != http.StatusOK {
		t.Fatalf("replayed consumed token webhook status=%d", code)
	}
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/getMe") {
			return nil, fmt.Errorf("unexpected Telegram verification request: %s", req.URL)
		}
		return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
	})}
	if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
		t.Fatalf("process private candidate=%t err=%v", processed, err)
	}
	link, err = m.GetLink(context.Background(), owner.accountID, link.ID)
	if err != nil || link.Status != "candidate" || link.Candidate == nil || link.Candidate.ChatID != "321" || link.Candidate.UserID != "321" {
		t.Fatalf("private candidate link=%+v err=%v", link, err)
	}
	if _, err := m.ConfirmLink(context.Background(), other.accountID, link.ID); !errors.Is(err, ErrMessengerNotFound) {
		t.Fatalf("foreign account confirmation error=%v, want not found", err)
	}
	destination, err := m.ConfirmLink(context.Background(), owner.accountID, link.ID)
	if err != nil || !destination.Ready || destination.Provider != "telegram" || destination.Kind != "private" || destination.ChatID != "321" || destination.UserID != "321" {
		t.Fatalf("confirmed private destination=%+v err=%v", destination, err)
	}
	confirmed, err := m.GetLink(context.Background(), owner.accountID, link.ID)
	if err != nil || confirmed.Status != "confirmed" || confirmed.Candidate != nil || confirmed.DestinationID == nil || *confirmed.DestinationID != destination.ID {
		t.Fatalf("confirmed link=%+v err=%v", confirmed, err)
	}
	destinations, err := m.ListDestinations(context.Background(), owner.accountID)
	if err != nil || len(destinations) != 1 || destinations[0].ID != destination.ID {
		t.Fatalf("owner destinations=%+v err=%v, want confirmed private destination", destinations, err)
	}
}

func TestMessengerGroupAndTopicLinksRequireBoundUserAndBothAdmins(t *testing.T) {
	t.Run("group with verified user and bot administrators", func(t *testing.T) {
		f := newWebhookFixture(t)
		owner, _ := f.addMonitor(t, "group link owner")
		m := testMessenger(t, f, true)
		private, privateToken, _, _, err := m.CreateLink(context.Background(), owner, "telegram", "private")
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		m.RegisterIngest(mux)
		post := func(update int, text, chatType string, chatID int64, thread int64) {
			body := fmt.Sprintf(`{"update_id":%d,"message":{"text":%q,"message_thread_id":%d,"chat":{"id":%d,"type":%q},"from":{"id":321,"is_bot":false}}}`, update, text, thread, chatID, chatType)
			req := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/telegram", strings.NewReader(body))
			req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram-webhook-secret-32-bytes")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("link webhook status=%d: %s", rec.Code, rec.Body.String())
			}
		}
		post(40, "/start "+privateToken, "private", 321, 0)
		m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(req.URL.Path, "/getMe") {
				return nil, fmt.Errorf("unexpected private verification request %s", req.URL)
			}
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
		})}
		if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
			t.Fatalf("verify private identity=%t err=%v", processed, err)
		}
		if _, err := m.ConfirmLink(context.Background(), owner, private.ID); err != nil {
			t.Fatalf("confirm private identity required for groups: %v", err)
		}
		group, _, _, command, err := m.CreateLink(context.Background(), owner, "telegram", "group")
		if err != nil {
			t.Fatal(err)
		}
		post(41, command, "supergroup", -10001, 0)
		var methods []string
		m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
			methods = append(methods, method)
			switch method {
			case "getMe":
				return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
			case "getChat":
				return messengerJSONResponse(200, `{"ok":true,"result":{"id":-10001,"type":"supergroup","title":"Operations","is_forum":false}}`), nil
			case "getChatMember":
				var body struct {
					UserID int64 `json:"user_id"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					return nil, err
				}
				return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"status":"administrator","user":{"id":%d}}}`, body.UserID)), nil
			default:
				return nil, fmt.Errorf("unexpected Telegram request %s", req.URL)
			}
		})}
		if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
			t.Fatalf("verify group candidate=%t err=%v", processed, err)
		}
		candidate, err := m.GetLink(context.Background(), owner, group.ID)
		if err != nil || candidate.Status != "candidate" || candidate.Candidate == nil || candidate.Candidate.ChatID != "-10001" || candidate.Candidate.UserID != "321" {
			t.Fatalf("group candidate=%+v err=%v", candidate, err)
		}
		if len(methods) != 4 || methods[0] != "getMe" || methods[1] != "getChat" || methods[2] != "getChatMember" || methods[3] != "getChatMember" {
			t.Fatalf("group verification methods=%v, want bot identity, group, bot admin and sender admin", methods)
		}
	})

	t.Run("group candidate without confirmed private identity is rejected", func(t *testing.T) {
		f := newWebhookFixture(t)
		owner, _ := f.addMonitor(t, "unbound group owner")
		m := testMessenger(t, f, true)
		group, _, _, command, err := m.CreateLink(context.Background(), owner, "telegram", "group")
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		m.RegisterIngest(mux)
		body := fmt.Sprintf(`{"update_id":50,"message":{"text":%q,"chat":{"id":-10002,"type":"group"},"from":{"id":555,"is_bot":false}}}`, command)
		req := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/telegram", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram-webhook-secret-32-bytes")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("group link webhook status=%d", rec.Code)
		}
		m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("provider request must not run before private ownership check")
		})}
		if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
			t.Fatalf("process unbound group link=%t err=%v", processed, err)
		}
		link, err := m.GetLink(context.Background(), owner, group.ID)
		if err != nil || link.Status != "expired" {
			t.Fatalf("unbound group link status=%s err=%v, want expired", link.Status, err)
		}
	})
}

func TestMessengerAcceptedDownAndRecoveryDeliverImmutableSnapshotsInOrder(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, "down and recovery")
	if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "messenger delivery integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	m := testMessenger(t, f, true)
	destination := confirmPrivateTelegramTarget(t, f, m, accountID, 321)
	if _, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{
		DestinationID: destination.ID, Enabled: true, ReminderMinutes: 0,
	}); err != nil {
		t.Fatalf("enable Telegram route: %v", err)
	}
	incidentModule := incidents.New(f.pool, NewRecorder(m))
	downAt := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	down := incidentcontracts.Observation{
		Name: "Checkout", URL: "https://shop.example.test/health", Outcome: "DOWN", HTTPStatus: 503,
		ErrorCode: "unexpected_status", LatencyMS: 237, CheckedAt: downAt, Sequence: 1,
	}
	applyMessengerObservation(t, f.pool, incidentModule, accountID, monitorID, 1, "DOWN", downAt, down)
	upAt := downAt.Add(2 * time.Minute)
	up := incidentcontracts.Observation{
		Name: "Checkout", URL: "https://shop.example.test/health", Outcome: "UP", HTTPStatus: 200,
		LatencyMS: 41, CheckedAt: upAt, Sequence: 2,
	}
	applyMessengerObservation(t, f.pool, incidentModule, accountID, monitorID, 2, "UP", upAt, up)

	// The monitor may change after acceptance; queued notifications must retain
	// the values recorded with their respective results.
	if _, err := f.pool.Exec(context.Background(), `UPDATE monitors.monitors SET name='Renamed',url='https://new.example.test/' WHERE id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_deliveries WHERE monitor_id=$1`, monitorID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("accepted DOWN/UP messenger rows=%d err=%v, want 2", count, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}

	downAttempt, err := m.ClaimDelivery(context.Background())
	if err != nil || downAttempt == nil {
		var state string
		var details string
		_ = f.pool.QueryRow(context.Background(), `SELECT string_agg(kind||':'||status||':'||error_code, ', ' ORDER BY sequence) FROM notifications.messenger_deliveries WHERE monitor_id=$1`, monitorID).Scan(&state)
		_ = f.pool.QueryRow(context.Background(), `SELECT a.status||'/'||m.outcome||'/'||r.enabled||'/'||d.status FROM accounts.accounts a JOIN monitors.monitors m ON m.account_id=a.id JOIN notifications.messenger_routes r ON r.monitor_id=m.id JOIN notifications.messenger_destinations d ON d.id=r.destination_id WHERE m.id=$1`, monitorID).Scan(&details)
		t.Fatalf("claim historical DOWN after recovery: attempt=%+v err=%v delivery=%q account/monitor/route/destination=%q", downAttempt, err, state, details)
	}
	if downAttempt.Kind != "down" || downAttempt.Snapshot.Sequence != 1 || downAttempt.Snapshot.Name != "Checkout" || downAttempt.Snapshot.URL != down.URL || downAttempt.Snapshot.HTTPStatus != 503 || downAttempt.Snapshot.LatencyMS != 237 || downAttempt.Snapshot.ErrorCode != "unexpected_status" {
		t.Fatalf("DOWN snapshot lost accepted result fields: %+v", downAttempt)
	}
	text, buttons := messengerMessage(downAttempt.Snapshot)
	for _, expected := range []string{"Checkout", "shop.example.test/health", "Причина:", "HTTP: 503", "237 мс"} {
		if !strings.Contains(text, expected) {
			t.Errorf("DOWN rendered message does not contain %q: %s", expected, text)
		}
	}
	if len(buttons) != 2 || !strings.Contains(buttons[0].URL, monitorID) || !strings.Contains(buttons[0].URL, downAttempt.IncidentID) || buttons[1].URL != down.URL {
		t.Errorf("DOWN buttons=%+v, want incident and accepted site URLs", buttons)
	}
	if next, err := m.ClaimDelivery(context.Background()); err != nil || next != nil {
		t.Fatalf("UP overtook in-flight DOWN: next=%+v err=%v", next, err)
	}
	if accepted, err := m.CompleteDelivery(context.Background(), *downAttempt, nil); err != nil || !accepted {
		t.Fatalf("complete historical DOWN accepted=%t err=%v", accepted, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_target_rates SET next_send_at=now()-interval '1 second' WHERE provider='telegram'`); err != nil {
		t.Fatal(err)
	}
	upAttempt, err := m.ClaimDelivery(context.Background())
	if err != nil || upAttempt == nil {
		t.Fatalf("claim recovery after DOWN completion: attempt=%+v err=%v", upAttempt, err)
	}
	if upAttempt.Kind != "up" || upAttempt.Snapshot.Sequence != 2 || upAttempt.Snapshot.Name != "Checkout" || upAttempt.Snapshot.URL != up.URL || upAttempt.Snapshot.HTTPStatus != 200 || upAttempt.Snapshot.LatencyMS != 41 {
		t.Fatalf("UP snapshot lost accepted result fields or wrong ordering: %+v", upAttempt)
	}
	if accepted, err := m.CompleteDelivery(context.Background(), *upAttempt, nil); err != nil || !accepted {
		t.Fatalf("complete recovery accepted=%t err=%v", accepted, err)
	}
	var delivered int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_deliveries WHERE monitor_id=$1 AND status='delivered'`, monitorID).Scan(&delivered); err != nil || delivered != 2 {
		t.Fatalf("delivered messenger rows=%d err=%v, want both DOWN and UP", delivered, err)
	}
}

func TestMessengerDueReminderIsQueuedThenCancelledWhenDisabled(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, "reminder cancellation")
	if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "messenger reminder integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	m := testMessenger(t, f, true)
	destination := confirmPrivateTelegramTarget(t, f, m, accountID, 322)
	route, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{
		DestinationID: destination.ID, Enabled: true, ReminderMinutes: 15,
	})
	if err != nil {
		t.Fatalf("enable Telegram route with reminder: %v", err)
	}
	incidentModule := incidents.New(f.pool, NewRecorder(m))
	firstAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	first := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test/health", Outcome: "DOWN", HTTPStatus: 503, ErrorCode: "unexpected_status", LatencyMS: 100, CheckedAt: firstAt, Sequence: 1}
	applyMessengerObservation(t, f.pool, incidentModule, accountID, monitorID, 1, "DOWN", firstAt, first)
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	downAttempt, err := m.ClaimDelivery(context.Background())
	if err != nil || downAttempt == nil || downAttempt.Kind != "down" {
		t.Fatalf("claim initial DOWN: attempt=%+v err=%v", downAttempt, err)
	}
	if accepted, err := m.CompleteDelivery(context.Background(), *downAttempt, nil); err != nil || !accepted {
		t.Fatalf("complete initial DOWN accepted=%t err=%v", accepted, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_routes SET next_due=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	secondAt := firstAt.Add(30 * time.Second)
	second := first
	second.CheckedAt, second.Sequence = secondAt, 2
	applyMessengerObservation(t, f.pool, incidentModule, accountID, monitorID, 2, "DOWN", secondAt, second)
	var reminderID, status string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text,status FROM notifications.messenger_deliveries WHERE monitor_id=$1 AND kind='reminder'`, monitorID).Scan(&reminderID, &status); err != nil || status != "pending" {
		t.Fatalf("due reminder id=%q status=%q err=%v, want queued pending reminder", reminderID, status, err)
	}
	updated, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{
		DestinationID: destination.ID, Enabled: true, ReminderMinutes: 0, Version: route.Version,
	})
	if err != nil || updated.Version != route.Version+1 || updated.ReminderMinutes != 0 {
		t.Fatalf("disable reminders route=%+v err=%v", updated, err)
	}
	var nextDue *time.Time
	var incidentID *string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM notifications.messenger_deliveries WHERE id=$1`, reminderID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("reminder after interval disabled status=%q err=%v, want cancelled", status, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT next_due,reminder_incident_id::text FROM notifications.messenger_routes WHERE monitor_id=$1`, monitorID).Scan(&nextDue, &incidentID); err != nil {
		t.Fatal(err)
	}
	if nextDue != nil || incidentID != nil {
		t.Fatalf("reminder schedule remains after disabling: next_due=%v incident_id=%v", nextDue, incidentID)
	}
}

func TestMessengerReminderRearmsOnlyOnFreshDownAndSchedulesFromNow(t *testing.T) {
	setup := func(t *testing.T, reminderMinutes int, enabled bool) (*webhookFixture, *Messenger, string, string, MessengerDestination, *incidents.Module, MessengerRoute) {
		t.Helper()
		f := newWebhookFixture(t)
		accountID, monitorID := f.addMonitor(t, "reminder rearm")
		if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "messenger reminder rearm integration"); err != nil {
			t.Fatalf("grant isolated test entitlement: %v", err)
		}
		m := testMessenger(t, f, true)
		destination := confirmPrivateTelegramTarget(t, f, m, accountID, 326)
		route, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: destination.ID, Enabled: enabled, ReminderMinutes: reminderMinutes})
		if err != nil {
			t.Fatalf("configure reminder route: %v", err)
		}
		return f, m, accountID, monitorID, destination, incidents.New(f.pool, NewRecorder(m)), route
	}
	accepted := func(t *testing.T, f *webhookFixture, recorder *incidents.Module, accountID, monitorID string, sequence int64, outcome string, at time.Time) {
		t.Helper()
		observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: outcome, HTTPStatus: 503, ErrorCode: "unexpected_status", LatencyMS: 120, CheckedAt: at, Sequence: sequence}
		applyMessengerObservation(t, f.pool, recorder, accountID, monitorID, sequence, outcome, at, observation)
	}

	t.Run("unknown cancels due attempt and next accepted DOWN schedules a full future interval", func(t *testing.T) {
		f, m, accountID, monitorID, _, recorder, _ := setup(t, 15, true)
		downAt := time.Now().UTC().Add(-time.Minute)
		accepted(t, f, recorder, accountID, monitorID, 1, "DOWN", downAt)
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
			t.Fatal(err)
		}
		down, err := m.ClaimDelivery(context.Background())
		if err != nil || down == nil || down.Kind != "down" {
			t.Fatalf("claim initial DOWN: attempt=%+v err=%v", down, err)
		}
		if ok, err := m.CompleteDelivery(context.Background(), *down, nil); err != nil || !ok {
			t.Fatalf("complete initial DOWN accepted=%t err=%v", ok, err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_routes SET next_due=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
			t.Fatal(err)
		}
		accepted(t, f, recorder, accountID, monitorID, 2, "DOWN", downAt.Add(30*time.Second))
		var reminderID string
		if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM notifications.messenger_deliveries WHERE monitor_id=$1 AND kind='reminder' AND status='pending'`, monitorID).Scan(&reminderID); err != nil {
			t.Fatalf("queue due reminder before UNKNOWN: %v", err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, reminderID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE monitors.monitors SET outcome='UNKNOWN',sequence=3,last_checked_at=now() WHERE id=$1`, monitorID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_target_rates SET next_send_at=now()-interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if attempt, err := m.ClaimDelivery(context.Background()); err != nil || attempt != nil {
			t.Fatalf("stale reminder claimed while monitor UNKNOWN: attempt=%+v err=%v", attempt, err)
		}
		var status string
		if err := f.pool.QueryRow(context.Background(), `SELECT status FROM notifications.messenger_deliveries WHERE id=$1`, reminderID).Scan(&status); err != nil || status != "cancelled" {
			t.Fatalf("stale reminder status=%q err=%v, want cancelled", status, err)
		}
		accepted(t, f, recorder, accountID, monitorID, 4, "DOWN", downAt.Add(60*time.Second))
		var nextDue *time.Time
		if err := f.pool.QueryRow(context.Background(), `SELECT next_due FROM notifications.messenger_routes WHERE monitor_id=$1`, monitorID).Scan(&nextDue); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if nextDue == nil || nextDue.Before(now.Add(14*time.Minute)) || nextDue.After(now.Add(16*time.Minute)) {
			t.Fatalf("fresh DOWN rearm due=%v, want a new 15-minute interval from now", nextDue)
		}
		var pendingReminders int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_deliveries WHERE monitor_id=$1 AND kind='reminder' AND status IN ('pending','leased')`, monitorID).Scan(&pendingReminders); err != nil || pendingReminders != 0 {
			t.Fatalf("rearm caught up a reminder immediately: pending=%d err=%v", pendingReminders, err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_routes SET next_due=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
			t.Fatal(err)
		}
		accepted(t, f, recorder, accountID, monitorID, 5, "DOWN", downAt.Add(90*time.Second))
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1 AND kind='reminder' AND status='pending'`, monitorID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_target_rates SET next_send_at=now()-interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		failedReminder, err := m.ClaimDelivery(context.Background())
		if err != nil || failedReminder == nil || failedReminder.Kind != "reminder" {
			t.Fatalf("claim reminder for terminal failure: attempt=%+v err=%v", failedReminder, err)
		}
		if ok, err := m.CompleteDelivery(context.Background(), *failedReminder, messengerProviderError{permanent: true}); err != nil || !ok {
			t.Fatalf("complete terminal reminder failure accepted=%t err=%v", ok, err)
		}
		accepted(t, f, recorder, accountID, monitorID, 6, "DOWN", downAt.Add(120*time.Second))
		if err := f.pool.QueryRow(context.Background(), `SELECT next_due FROM notifications.messenger_routes WHERE monitor_id=$1`, monitorID).Scan(&nextDue); err != nil {
			t.Fatal(err)
		}
		now = time.Now()
		if nextDue == nil || nextDue.Before(now.Add(14*time.Minute)) || nextDue.After(now.Add(16*time.Minute)) {
			t.Fatalf("next accepted DOWN after terminal reminder failure due=%v, want a fresh 15-minute interval", nextDue)
		}
	})

	t.Run("never sent initial DOWN cannot arm a reminder on later DOWN", func(t *testing.T) {
		f, _, accountID, monitorID, _, recorder, _ := setup(t, 15, true)
		now := time.Now().UTC()
		accepted(t, f, recorder, accountID, monitorID, 1, "DOWN", now)
		accepted(t, f, recorder, accountID, monitorID, 2, "UNKNOWN", now.Add(time.Second))
		accepted(t, f, recorder, accountID, monitorID, 3, "DOWN", now.Add(2*time.Second))
		var nextDue *time.Time
		var incidentID *string
		var pendingReminders int
		if err := f.pool.QueryRow(context.Background(), `SELECT next_due,reminder_incident_id::text FROM notifications.messenger_routes WHERE monitor_id=$1`, monitorID).Scan(&nextDue, &incidentID); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_deliveries WHERE monitor_id=$1 AND kind='reminder' AND status IN ('pending','leased')`, monitorID).Scan(&pendingReminders); err != nil {
			t.Fatal(err)
		}
		if nextDue != nil || incidentID != nil || pendingReminders != 0 {
			t.Fatalf("reminder was armed before initial DOWN send: due=%v incident=%v pending=%d", nextDue, incidentID, pendingReminders)
		}
	})

	t.Run("explicitly enabling reminders mid-incident starts a full interval", func(t *testing.T) {
		f, m, accountID, monitorID, destination, recorder, route := setup(t, 0, false)
		now := time.Now().UTC()
		accepted(t, f, recorder, accountID, monitorID, 1, "DOWN", now)
		updated, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: destination.ID, Enabled: true, ReminderMinutes: 15, Version: route.Version})
		if err != nil || updated.Version != route.Version+1 {
			t.Fatalf("enable reminders mid-incident route=%+v err=%v", updated, err)
		}
		var nextDue *time.Time
		var incidentID *string
		if err := f.pool.QueryRow(context.Background(), `SELECT next_due,reminder_incident_id::text FROM notifications.messenger_routes WHERE monitor_id=$1`, monitorID).Scan(&nextDue, &incidentID); err != nil {
			t.Fatal(err)
		}
		wall := time.Now()
		if nextDue == nil || nextDue.Before(wall.Add(14*time.Minute)) || nextDue.After(wall.Add(16*time.Minute)) || incidentID == nil {
			t.Fatalf("manual reminder enable mid-incident due=%v incident=%v, want full interval", nextDue, incidentID)
		}
		var pendingReminders int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_deliveries WHERE monitor_id=$1 AND kind='reminder' AND status IN ('pending','leased')`, monitorID).Scan(&pendingReminders); err != nil || pendingReminders != 0 {
			t.Fatalf("manual configuration created catch-up reminder: pending=%d err=%v", pendingReminders, err)
		}
	})
}

func TestMessengerGroupLossOfAdminDisconnectsWithoutSending(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, "lost group admin")
	if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "messenger group delivery integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	m := testMessenger(t, f, true)
	confirmPrivateTelegramTarget(t, f, m, accountID, 321)
	destination := confirmTelegramGroupTarget(t, f, m, accountID, 321, -10021)
	if _, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: destination.ID, Enabled: true}); err != nil {
		t.Fatalf("enable group route: %v", err)
	}
	checked := time.Now().UTC().Add(-time.Minute)
	observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, ErrorCode: "unexpected_status", LatencyMS: 120, CheckedAt: checked, Sequence: 1}
	applyMessengerObservation(t, f.pool, incidents.New(f.pool, NewRecorder(m)), accountID, monitorID, 1, "DOWN", checked, observation)
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	sends := 0
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		switch method {
		case "getMe":
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
		case "getChat":
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":-10021,"type":"supergroup","title":"Operations","is_forum":false}}`), nil
		case "getChatMember":
			var input struct {
				UserID int64 `json:"user_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				return nil, err
			}
			status := "administrator"
			if input.UserID == 700 {
				status = "member" // the bot lost its admin rights after the route was confirmed
			}
			return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"status":%q,"user":{"id":%d}}}`, status, input.UserID)), nil
		case "sendMessage":
			sends++
			return messengerJSONResponse(200, `{"ok":true,"result":{"message_id":81}}`), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram method %s", method)
		}
	})}
	processed, err := m.ProcessDelivery(context.Background())
	if err != nil || !processed {
		t.Fatalf("process group delivery=%t err=%v", processed, err)
	}
	var destinationStatus, deliveryStatus, routeEnabled string
	if err := f.pool.QueryRow(context.Background(), `SELECT d.status, x.status, r.enabled::text FROM notifications.messenger_destinations d JOIN notifications.messenger_routes r ON r.destination_id=d.id JOIN notifications.messenger_deliveries x ON x.route_id=r.id WHERE d.id=$1`, destination.ID).Scan(&destinationStatus, &deliveryStatus, &routeEnabled); err != nil {
		t.Fatal(err)
	}
	if sends != 0 || destinationStatus != "disconnected" || routeEnabled != "false" || deliveryStatus != "cancelled" {
		t.Fatalf("lost admin sends=%d destination=%s route_enabled=%s delivery=%s, want no send/disconnected/disabled/cancelled", sends, destinationStatus, routeEnabled, deliveryStatus)
	}
}

func TestMessengerRouteRebindDuringGroupVerificationFencesProviderSend(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, "group route rebind")
	if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "messenger group delivery integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	m := testMessenger(t, f, true)
	confirmPrivateTelegramTarget(t, f, m, accountID, 321)
	group := confirmTelegramGroupTarget(t, f, m, accountID, 321, -10022)
	private := confirmPrivateTelegramTarget(t, f, m, accountID, 322)
	route, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: group.ID, Enabled: true})
	if err != nil {
		t.Fatalf("enable group route: %v", err)
	}
	checked := time.Now().UTC().Add(-time.Minute)
	observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, ErrorCode: "unexpected_status", LatencyMS: 120, CheckedAt: checked, Sequence: 1}
	applyMessengerObservation(t, f.pool, incidents.New(f.pool, NewRecorder(m)), accountID, monitorID, 1, "DOWN", checked, observation)
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	rebound := false
	sends := 0
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		switch method {
		case "getMe":
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
		case "getChat":
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":-10022,"type":"supergroup","title":"Operations","is_forum":false}}`), nil
		case "getChatMember":
			var input struct {
				UserID int64 `json:"user_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				return nil, err
			}
			if input.UserID == 321 && !rebound {
				updated, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: private.ID, Enabled: true, Version: route.Version})
				if err != nil || updated.DestinationID != private.ID {
					return nil, fmt.Errorf("rebind during provider verification: route=%+v err=%v", updated, err)
				}
				rebound = true
			}
			return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"status":"administrator","user":{"id":%d}}}`, input.UserID)), nil
		case "sendMessage":
			sends++
			return messengerJSONResponse(200, `{"ok":true,"result":{"message_id":82}}`), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram method %s", method)
		}
	})}
	processed, err := m.ProcessDelivery(context.Background())
	if err != nil || !processed {
		t.Fatalf("process delivery during rebind=%t err=%v", processed, err)
	}
	var status string
	var boundDestination string
	if err := f.pool.QueryRow(context.Background(), `SELECT x.status,r.destination_id::text FROM notifications.messenger_deliveries x JOIN notifications.messenger_routes r ON r.id=x.route_id WHERE x.monitor_id=$1`, monitorID).Scan(&status, &boundDestination); err != nil {
		t.Fatal(err)
	}
	if !rebound || sends != 0 || status != "cancelled" || boundDestination != private.ID {
		t.Fatalf("mid-verification rebind observed=%t sends=%d delivery=%s bound_destination=%s, want rebound/no send/cancelled/private target", rebound, sends, status, boundDestination)
	}
}

func TestMessengerGroupVerificationAndSendShareOneAttemptDeadline(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, "shared send deadline")
	if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "messenger deadline integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	m := testMessenger(t, f, true)
	confirmPrivateTelegramTarget(t, f, m, accountID, 321)
	destination := confirmTelegramGroupTarget(t, f, m, accountID, 321, -10023)
	if _, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: destination.ID, Enabled: true}); err != nil {
		t.Fatalf("enable group route: %v", err)
	}
	checked := time.Now().UTC().Add(-time.Minute)
	observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, ErrorCode: "unexpected_status", LatencyMS: 120, CheckedAt: checked, Sequence: 1}
	applyMessengerObservation(t, f.pool, incidents.New(f.pool, NewRecorder(m)), accountID, monitorID, 1, "DOWN", checked, observation)
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	var deadlines []time.Time
	sends := 0
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			return nil, errors.New("provider request has no bounded attempt deadline")
		}
		deadlines = append(deadlines, deadline)
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		switch method {
		case "getMe":
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
		case "getChat":
			return messengerJSONResponse(200, `{"ok":true,"result":{"id":-10023,"type":"supergroup","title":"Operations","is_forum":false}}`), nil
		case "getChatMember":
			var input struct {
				UserID int64 `json:"user_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				return nil, err
			}
			return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"status":"administrator","user":{"id":%d}}}`, input.UserID)), nil
		case "sendMessage":
			sends++
			return messengerJSONResponse(200, `{"ok":true,"result":{"message_id":83}}`), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram method %s", method)
		}
	})}
	processed, err := m.ProcessDelivery(context.Background())
	if err != nil || !processed {
		t.Fatalf("process bounded group delivery=%t err=%v", processed, err)
	}
	if len(deadlines) != 5 || sends != 1 {
		t.Fatalf("provider calls=%d sends=%d, want getMe/getChat/two members/sendMessage", len(deadlines), sends)
	}
	for i := 1; i < len(deadlines); i++ {
		if !deadlines[i].Equal(deadlines[0]) {
			t.Fatalf("provider call %d has deadline %s, first call deadline %s; one attempt must share its timeout", i, deadlines[i], deadlines[0])
		}
	}
}

func TestMessengerCompletionAndReminderSettingsUpdateDoNotDeadlock(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, monitorID := f.addMonitor(t, "concurrent completion and settings")
	if err := billing.New(f.pool, true).GrantLocal(context.Background(), accountID, 5, time.Now().Add(time.Hour), "messenger concurrency integration"); err != nil {
		t.Fatalf("grant isolated test entitlement: %v", err)
	}
	m := testMessenger(t, f, true)
	destination := confirmPrivateTelegramTarget(t, f, m, accountID, 324)
	route, err := m.PutRoute(context.Background(), accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: destination.ID, Enabled: true})
	if err != nil {
		t.Fatalf("enable Telegram route: %v", err)
	}
	checked := time.Now().UTC().Add(-time.Minute)
	observation := incidentcontracts.Observation{Name: "API", URL: "https://api.example.test", Outcome: "DOWN", HTTPStatus: 503, ErrorCode: "unexpected_status", LatencyMS: 120, CheckedAt: checked, Sequence: 1}
	applyMessengerObservation(t, f.pool, incidents.New(f.pool, NewRecorder(m)), accountID, monitorID, 1, "DOWN", checked, observation)
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_deliveries SET next_attempt_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	attempt, err := m.ClaimDelivery(context.Background())
	if err != nil || attempt == nil {
		t.Fatalf("claim delivery for concurrent update: attempt=%+v err=%v", attempt, err)
	}
	type completion struct {
		accepted bool
		err      error
	}
	start := make(chan struct{})
	completed := make(chan completion, 1)
	updated := make(chan error, 1)
	go func() {
		<-start
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		accepted, err := m.CompleteDelivery(ctx, *attempt, nil)
		completed <- completion{accepted: accepted, err: err}
	}()
	go func() {
		<-start
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := m.PutRoute(ctx, accountID, monitorID, "telegram", PutMessengerRoute{DestinationID: destination.ID, Enabled: true, ReminderMinutes: 15, Version: route.Version})
		updated <- err
	}()
	close(start)
	select {
	case result := <-completed:
		if result.err != nil || !result.accepted {
			t.Fatalf("concurrent delivery completion accepted=%t err=%v", result.accepted, result.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("delivery completion deadlocked with reminder settings update")
	}
	select {
	case err := <-updated:
		if err != nil {
			t.Fatalf("concurrent reminder settings update: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("reminder settings update deadlocked with delivery completion")
	}
}
