package support

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/migrations"
)

const (
	supportTelegramSecret = "telegram-webhook-secret-32-bytes-min"
	supportMAXSecret      = "max-webhook-secret-32-bytes-minimum"
	supportOperatorChat   = int64(-1004317880847)
)

var supportKey = base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))

type supportFixture struct {
	pool *pgxpool.Pool
	cfg  Config
}

func newSupportFixture(t *testing.T) *supportFixture {
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
	dbName := fmt.Sprintf("hc_support_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+dbName); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("create isolated support test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()", dbName); err != nil {
			t.Errorf("terminate isolated support test connections: %v", err)
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName); err != nil {
			t.Errorf("drop isolated support test database: %v", err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})
	u.Path = "/" + dbName
	poolCfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("parse isolated support test database URL: %v", err)
	}
	poolCfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		t.Fatalf("connect to isolated support test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(context.Background(), pool); err != nil {
		t.Fatalf("apply support test migrations: %v", err)
	}
	return &supportFixture{pool: pool, cfg: Config{TelegramWebhookSecret: supportTelegramSecret, MAXWebhookSecret: supportMAXSecret, EncryptionKey: supportKey, InternalChatID: supportOperatorChat, SupportTopicID: 2}}
}

func (f *supportFixture) webhook(t *testing.T, maxChat int64) *Module {
	t.Helper()
	cfg := f.cfg
	cfg.MAXSupportChatID = maxChat
	m, err := NewWebhook(f.pool, cfg)
	if err != nil {
		t.Fatalf("create support webhook module: %v", err)
	}
	return m
}
func (f *supportFixture) worker(t *testing.T, maxChat int64) *Module {
	t.Helper()
	cfg := f.cfg
	cfg.MAXSupportChatID, cfg.TelegramToken, cfg.MAXToken = maxChat, "12345:test-telegram-token", "test-max-token"
	m, err := New(f.pool, cfg)
	if err != nil {
		t.Fatalf("create support worker module: %v", err)
	}
	return m
}
func postSupport(m *Module, provider, secret string, body []byte) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	m.Register(mux)
	path, header := "/webhooks/support/telegram", "X-Telegram-Bot-Api-Secret-Token"
	if provider == "max" {
		path, header = "/webhooks/support/max", "X-Max-Bot-Api-Secret"
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set(header, secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
func telegramPrivate(update, message, sender int64, text string) []byte {
	return []byte(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false},"text":%q}}`, update, message, sender, sender, text))
}

func TestSupportWebhookAuthenticationDedupAndAdmission(t *testing.T) {
	f := newSupportFixture(t)
	m := f.webhook(t, 9001)
	body := telegramPrivate(10, 22, 111, "help")
	if got := postSupport(m, "telegram", "wrong-webhook-secret-which-is-long-enough", body); got.Code != http.StatusUnauthorized {
		t.Fatalf("bad webhook secret status=%d", got.Code)
	}
	if got := postSupport(m, "telegram", supportTelegramSecret, body); got.Code != http.StatusOK {
		t.Fatalf("valid webhook status=%d: %s", got.Code, got.Body.String())
	}
	if got := postSupport(m, "telegram", supportTelegramSecret, body); got.Code != http.StatusOK {
		t.Fatalf("duplicate webhook status=%d", got.Code)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE provider='telegram'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate produced %d inbox rows, want 1", count)
	}
	if got := postSupport(m, "telegram", supportTelegramSecret, telegramPrivate(10, 22, 111, "changed payload")); got.Code != http.StatusConflict {
		t.Fatalf("changed duplicate status=%d, want 409", got.Code)
	}
	for i := int64(0); i < 49; i++ {
		e := event{Provider: "telegram", SenderID: 111, ChatID: 111, MessageID: fmt.Sprint(100 + i), Private: true, Text: "queued"}
		if err := m.admit(context.Background(), "telegram", fmt.Sprint(100+i), e); err != nil {
			t.Fatalf("admit sender message %d: %v", i, err)
		}
	}
	limited := postSupport(m, "telegram", supportTelegramSecret, telegramPrivate(200, 400, 111, "over admission limit"))
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "30" {
		t.Fatalf("sender admission response=%d retry-after=%q", limited.Code, limited.Header().Get("Retry-After"))
	}
	if err := m.admit(context.Background(), "telegram", "100", event{Provider: "telegram", SenderID: 111, ChatID: 111, MessageID: "100", Private: true, Text: "queued"}); err != nil {
		t.Fatalf("same duplicate at capacity should remain idempotent: %v", err)
	}
}

func TestSupportQueueLeaseRetryAndExhaustion(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9001)
	e := event{Provider: "telegram", SenderID: 222, ChatID: 222, MessageID: "lease-message", Private: true, Text: "lease"}
	if err := m.admit(context.Background(), "telegram", "lease-key", e); err != nil {
		t.Fatal(err)
	}
	first, err := m.claim(context.Background())
	if err != nil || first == nil {
		t.Fatalf("claim first job=%+v err=%v", first, err)
	}
	if next, err := m.claim(context.Background()); err != nil || next != nil {
		t.Fatalf("active lease was claimed twice: job=%+v err=%v", next, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET lease_until=now()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := m.claim(context.Background())
	if err != nil || second == nil || second.ID != first.ID || second.Lease == first.Lease || second.Attempts != first.Attempts+1 {
		t.Fatalf("expired lease reclaim first=%+v second=%+v err=%v", first, second, err)
	}
	if err := m.finish(context.Background(), *first, "sent", "", 0, false); err != nil {
		t.Fatal(err)
	}
	var status, token string
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT status,lease_token,attempts FROM support.inbox WHERE id=$1`, second.ID).Scan(&status, &token, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "sending" || token != second.Lease || attempts != second.Attempts {
		t.Fatalf("stale worker changed reclaimed lease: status=%s attempts=%d", status, attempts)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET attempts=8,lease_until=now()-interval '1 second' WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if job, err := m.claim(context.Background()); err != nil || job != nil {
		t.Fatalf("exhausted lease claim=%+v err=%v, want none", job, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM support.inbox WHERE id=$1`, second.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("exhausted row status=%s, want failed", status)
	}
}

func TestSupportTargetRateIsPerDestination(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9001)
	if err := m.reserveTarget(context.Background(), "max", "user_id=100", time.Second); err != nil {
		t.Fatal(err)
	}
	var rate *providerError
	if err := m.reserveTarget(context.Background(), "max", "user_id=100", time.Second); !errors.As(err, &rate) || rate.code != "target_rate" {
		t.Fatalf("repeated destination reservation error=%v, want target_rate", err)
	}
	if err := m.reserveTarget(context.Background(), "max", "user_id=101", time.Second); err != nil {
		t.Fatalf("separate destination rate limit: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{}}
}

func TestSupportMAXAdminPaginationAndPermission(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9900)
	var pages []string
	m.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		pages = append(pages, req.URL.String())
		switch {
		case strings.HasSuffix(req.URL.Path, "/members/me"):
			return jsonResponse(200, `{"user_id":700,"is_bot":true,"is_admin":true}`), nil
		case strings.HasSuffix(req.URL.Path, "/members/admins") && req.URL.Query().Get("marker") == "":
			return jsonResponse(200, `{"members":[{"user_id":55,"is_admin":true}],"marker":12}`), nil
		case strings.HasSuffix(req.URL.Path, "/members/admins") && req.URL.Query().Get("marker") == "12":
			return jsonResponse(200, `{"members":[{"user_id":77,"is_admin":true}],"marker":null}`), nil
		default:
			return jsonResponse(404, `{}`), nil
		}
	})}
	if err := m.maxAuthorized(context.Background(), 77); err != nil {
		t.Fatalf("second-page admin authorization: %v", err)
	}
	if len(pages) != 3 || !strings.Contains(pages[2], "marker=12") {
		t.Fatalf("MAX admin pagination requests=%v", pages)
	}
	if err := m.maxAuthorized(context.Background(), 88); !errors.Is(err, errRejected) {
		t.Fatalf("non-admin authorization=%v, want rejected", err)
	}
	if len(pages) != 6 || !strings.Contains(pages[5], "marker=12") {
		t.Fatalf("non-admin search did not inspect all pages: %v", pages)
	}
}

func TestSupportMAXReplyUsesExactStoredRouteAndNativeMessage(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9900)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO support.message_routes(provider,operator_chat_id,operator_message_id,client_chat_id,client_user_id,client_message_id) VALUES('max',9900,'unrelated-reply',999,999,'unrelated'),('max',8800,'target-reply',111,222,'client-source')`); err != nil {
		t.Fatal(err)
	}
	incoming, _, err := normalize("max", []byte(`{"update_type":"message_created","timestamp":1730000100,"message":{"sender":{"user_id":77,"is_bot":false},"recipient":{"chat_id":9900,"chat_type":"chat","title":"must-not-forward"},"link":{"type":"reply","message":{"mid":"target-reply"}},"body":{"mid":"operator-reply","text":"response","attachments":[{"type":"image","payload":{"token":"native-image-token","url":"https://cdn.example/private-signed-url","chat_id":999}},{"type":"image","payload":{"url":"https://cdn.example/fallback-image","chat_id":998}}]}}}`))
	if err != nil || incoming == nil || incoming.ReplyID != "target-reply" || len(incoming.Attachments) != 2 {
		t.Fatalf("normalize native MAX reply fixture event=%+v err=%v", incoming, err)
	}
	if err := m.reply(context.Background(), job{}, *incoming); !errors.Is(err, errMissingRoute) {
		t.Fatalf("reply to another chat route=%v, want missing route", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO support.message_routes(provider,operator_chat_id,operator_message_id,client_chat_id,client_user_id,client_message_id) VALUES('max',9900,'target-reply',111,222,'client-source')`); err != nil {
		t.Fatal(err)
	}
	if err := m.admit(context.Background(), "max", "operator-reply-key", *incoming); err != nil {
		t.Fatal(err)
	}
	claimed, err := m.claim(context.Background())
	if err != nil || claimed == nil {
		t.Fatalf("claim operator reply=%+v err=%v", claimed, err)
	}
	var postURL string
	var payload map[string]any
	var providerCalls []string
	m.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		providerCalls = append(providerCalls, req.URL.String())
		if strings.HasSuffix(req.URL.Path, "/members/me") {
			return jsonResponse(200, `{"user_id":700,"is_bot":true,"is_admin":true}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/members/admins") {
			return jsonResponse(200, `{"members":[{"user_id":77,"is_admin":true}],"marker":null}`), nil
		}
		postURL = req.URL.String()
		if req.Body == nil {
			return nil, errors.New("MAX message request body missing")
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		return jsonResponse(200, `{"message":{"body":{"mid":"new-max-message"}}}`), nil
	})}
	if err := m.process(context.Background(), *claimed, *incoming); err != nil {
		t.Fatalf("deliver exact reply route: %v", err)
	}
	if postURL != "https://platform-api2.max.ru/messages?user_id=222" {
		t.Fatalf("MAX reply destination=%s, want exact routed user", postURL)
	}
	if len(providerCalls) != 3 {
		t.Fatalf("MAX provider requests=%v, want authorization then one message send", providerCalls)
	}
	if payload["text"] != "response" || len(payload) != 2 {
		t.Fatalf("native MAX payload leaked routing data or changed content: %#v", payload)
	}
	attachments, ok := payload["attachments"].([]any)
	if !ok || len(attachments) != 2 {
		t.Fatalf("native image attachments=%#v", payload["attachments"])
	}
	first := attachments[0].(map[string]any)["payload"].(map[string]any)
	second := attachments[1].(map[string]any)["payload"].(map[string]any)
	if first["token"] != "native-image-token" || len(first) != 1 || second["url"] != "https://cdn.example/fallback-image" || len(second) != 1 {
		t.Fatalf("MAX image token preference or URL fallback failed: %#v", attachments)
	}
}

func TestSupportMAXDiscoveryPrivacyAndZeroChatPending(t *testing.T) {
	f := newSupportFixture(t)
	m := f.webhook(t, 0)
	body := []byte(`{"update_type":"message_created","timestamp":1730000000,"message":{"sender":{"user_id":321,"is_bot":false},"recipient":{"chat_id":555,"chat_type":"chat","title":"private title"},"body":{"mid":"private-message-id","text":"private customer text","attachments":[]}}}`)
	if got := postSupport(m, "max", supportMAXSecret, body); got.Code != http.StatusOK {
		t.Fatalf("unknown MAX chat discovery status=%d: %s", got.Code, got.Body.String())
	}
	rows, err := m.ListDiscovery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Type != "message_created" || rows[0].ChatID != 555 || rows[0].Timestamp != 1730000000 {
		t.Fatalf("discovery result=%+v", rows)
	}
	newer := []byte(`{"update_type":"message_created","timestamp":1730000100,"message":{"sender":{"user_id":444,"is_bot":false},"recipient":{"chat_id":555,"chat_type":"chat","title":"another private title"},"body":{"mid":"new-private-message-id","text":"new private customer text","attachments":[]}}}`)
	if got := postSupport(m, "max", supportMAXSecret, newer); got.Code != http.StatusOK {
		t.Fatalf("coalesced MAX discovery status=%d: %s", got.Code, got.Body.String())
	}
	stale := []byte(`{"update_type":"message_created","timestamp":1729999999,"message":{"sender":{"user_id":999,"is_bot":false},"recipient":{"chat_id":555,"chat_type":"chat"},"body":{"mid":"stale-message-id","text":"stale"}}}`)
	if got := postSupport(m, "max", supportMAXSecret, stale); got.Code != http.StatusOK {
		t.Fatalf("stale MAX discovery status=%d: %s", got.Code, got.Body.String())
	}
	rows, err = m.ListDiscovery(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Timestamp != 1730000100 {
		t.Fatalf("coalesced discovery rows=%+v err=%v", rows, err)
	}
	configured := f.webhook(t, 777)
	if got := postSupport(configured, "max", supportMAXSecret, newer); got.Code != http.StatusOK {
		t.Fatalf("configured MAX webhook rejected unrelated group traffic: %d", got.Code)
	}
	var afterIgnore int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox`).Scan(&afterIgnore); err != nil {
		t.Fatal(err)
	}
	if afterIgnore != 1 {
		t.Fatalf("configured unknown MAX chat created an inbox row; count=%d", afterIgnore)
	}
	if m.cfg.MAXSupportChatID != 0 {
		t.Fatalf("discovery unexpectedly configured MAX chat %d", m.cfg.MAXSupportChatID)
	}
	var kind, status string
	var encrypted []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT event_kind,status,payload FROM support.inbox`).Scan(&kind, &status, &encrypted); err != nil {
		t.Fatal(err)
	}
	if kind != "discovery" || status != "recorded" {
		t.Fatalf("unknown chat row kind=%s status=%s", kind, status)
	}
	key := "discovery:message_created:555"
	plain, err := m.cipher.Open(nil, encrypted[:m.cipher.NonceSize()], encrypted[m.cipher.NonceSize():], []byte("max:"+key))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"444", "another private title", "new private customer text", "new-private-message-id"} {
		if strings.Contains(string(plain), private) {
			t.Fatalf("discovery persisted private field %q in %s", private, plain)
		}
	}
	worker := f.worker(t, 0)
	privateMessage := event{Provider: "max", ChatID: 321, SenderID: 321, MessageID: "private-message-id", Private: true, Text: "pending until configured"}
	if err := worker.admit(context.Background(), "max", "unconfigured-private", privateMessage); err != nil {
		t.Fatal(err)
	}
	if job, err := worker.claim(context.Background()); err != nil || job != nil {
		t.Fatalf("zero-chat worker claimed=%+v err=%v, want pending", job, err)
	}
	var pendingStatus string
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM support.inbox WHERE event_key='unconfigured-private'`).Scan(&pendingStatus, &attempts); err != nil {
		t.Fatal(err)
	}
	if pendingStatus != "pending" || attempts != 0 {
		t.Fatalf("zero-chat MAX message status=%s attempts=%d, want pending/0", pendingStatus, attempts)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET created_at=now()-interval '25 hours' WHERE event_key='unconfigured-private'`); err != nil {
		t.Fatal(err)
	}
	if job, err := worker.claim(context.Background()); err != nil || job != nil {
		t.Fatalf("expired zero-chat MAX message was claimed=%+v err=%v", job, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,attempts FROM support.inbox WHERE event_key='unconfigured-private'`).Scan(&pendingStatus, &attempts); err != nil {
		t.Fatal(err)
	}
	if pendingStatus != "failed" || attempts != 0 {
		t.Fatalf("expired zero-chat MAX message status=%s attempts=%d, want failed/0", pendingStatus, attempts)
	}
}

func TestSupportTelegramGroupAdmissionRequiresSupportTopicAndReply(t *testing.T) {
	f := newSupportFixture(t)
	m := f.webhook(t, 9001)
	update := func(id, thread int64, reply string) []byte {
		return []byte(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"message_thread_id":%d,"chat":{"id":-1004317880847,"type":"supergroup"},"from":{"id":77,"is_bot":false},%s}}`, id, id+100, thread, reply))
	}
	for _, body := range [][]byte{
		update(1, 3, `"reply_to_message":{"message_id":55}`),
		update(2, 2, `"text":"no reply"`),
	} {
		if got := postSupport(m, "telegram", supportTelegramSecret, body); got.Code != http.StatusOK {
			t.Fatalf("irrelevant Telegram group update status=%d", got.Code)
		}
	}
	valid := postSupport(m, "telegram", supportTelegramSecret, update(3, 2, `"reply_to_message":{"message_id":55}`))
	if valid.Code != http.StatusOK {
		t.Fatalf("support-topic Telegram reply status=%d: %s", valid.Code, valid.Body.String())
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE provider='telegram'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("Telegram topic admission created %d rows, want one", count)
	}
}

func TestSupportTelegramNonAdminReplyIsRejectedBeforeDelivery(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9001)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO support.message_routes(provider,operator_chat_id,operator_message_id,client_chat_id,client_user_id,client_message_id) VALUES('telegram',$1,'operator-copy',123,123,'client-message')`, supportOperatorChat); err != nil {
		t.Fatal(err)
	}
	e := event{Provider: "telegram", ChatID: supportOperatorChat, ThreadID: 2, SenderID: 77, MessageID: "operator-reply", ReplyID: "operator-copy", Text: "unauthorized reply"}
	if err := m.admit(context.Background(), "telegram", "telegram-operator-reply", e); err != nil {
		t.Fatal(err)
	}
	claimed, err := m.claim(context.Background())
	if err != nil || claimed == nil {
		t.Fatalf("claim operator reply=%+v err=%v", claimed, err)
	}
	var methods []string
	m.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		methods = append(methods, method)
		switch method {
		case "getMe":
			return jsonResponse(200, `{"ok":true,"result":{"id":700,"is_bot":true}}`), nil
		case "getChatMember":
			var body struct {
				UserID int64 `json:"user_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				return nil, err
			}
			status := "administrator"
			if body.UserID == 77 {
				status = "member"
			}
			return jsonResponse(200, fmt.Sprintf(`{"ok":true,"result":{"status":%q,"user":{"id":%d,"is_bot":false}}}`, status, body.UserID)), nil
		default:
			return jsonResponse(500, `{}`), nil
		}
	})}
	if err := m.process(context.Background(), *claimed, e); !errors.Is(err, errRejected) {
		t.Fatalf("non-admin reply error=%v, want rejected", err)
	}
	if strings.Contains(strings.Join(methods, ","), "copyMessage") {
		t.Fatalf("non-admin reply reached Telegram delivery: %v", methods)
	}
}

func TestSupportProvider429RetryAndLocalTargetRateDoNotBurnAttempts(t *testing.T) {
	t.Run("provider 429 respects body and header retry bounds", func(t *testing.T) {
		f := newSupportFixture(t)
		m := f.worker(t, 9001)
		e := event{Provider: "telegram", SenderID: 321, ChatID: 321, MessageID: "42", Private: true, Text: "429 test"}
		if err := m.admit(context.Background(), "telegram", "429-event-key", e); err != nil {
			t.Fatal(err)
		}
		var sent bool
		m.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
			switch method {
			case "getMe":
				return jsonResponse(200, `{"ok":true,"result":{"id":700,"is_bot":true}}`), nil
			case "getChatMember":
				return jsonResponse(200, `{"ok":true,"result":{"status":"administrator","user":{"id":700,"is_bot":true}}}`), nil
			case "copyMessage":
				sent = true
				response := jsonResponse(http.StatusTooManyRequests, `{"ok":false,"error_code":429,"parameters":{"retry_after":29}}`)
				response.Header.Set("Retry-After", "17")
				return response, nil
			default:
				return jsonResponse(500, `{}`), nil
			}
		})}
		if processed, err := m.ProcessOne(context.Background()); err != nil || !processed {
			t.Fatalf("429 process=%v err=%v", processed, err)
		}
		var status, code string
		var attempts int
		var delayed bool
		if err := f.pool.QueryRow(context.Background(), `SELECT status,last_error,attempts,next_attempt_at>now()+interval '28 seconds' FROM support.inbox WHERE event_key='429-event-key'`).Scan(&status, &code, &attempts, &delayed); err != nil {
			t.Fatal(err)
		}
		if !sent || status != "pending" || code != "provider_http_429" || attempts != 1 || !delayed {
			t.Fatalf("429 retry state sent=%v status=%s code=%s attempts=%d delayed=%v", sent, status, code, attempts, delayed)
		}
	})
	t.Run("local destination cooldown does not consume retry", func(t *testing.T) {
		f := newSupportFixture(t)
		m := f.worker(t, 9001)
		e := event{Provider: "telegram", SenderID: 321, ChatID: 321, MessageID: "43", Private: true, Text: "rate test"}
		if err := m.admit(context.Background(), "telegram", "local-rate-event-key", e); err != nil {
			t.Fatal(err)
		}
		key := m.digest([]byte(fmt.Sprint(supportOperatorChat)))
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO support.target_rates(provider,target_key,next_send_at) VALUES('telegram',$1,now()+interval '1 minute')`, key); err != nil {
			t.Fatal(err)
		}
		var copyCalled bool
		m.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
			if method == "getMe" {
				return jsonResponse(200, `{"ok":true,"result":{"id":700,"is_bot":true}}`), nil
			}
			if method == "getChatMember" {
				return jsonResponse(200, `{"ok":true,"result":{"status":"administrator","user":{"id":700,"is_bot":true}}}`), nil
			}
			if method == "copyMessage" {
				copyCalled = true
			}
			return jsonResponse(500, `{}`), nil
		})}
		if processed, err := m.ProcessOne(context.Background()); err != nil || !processed {
			t.Fatalf("local rate process=%v err=%v", processed, err)
		}
		var status, code string
		var attempts int
		if err := f.pool.QueryRow(context.Background(), `SELECT status,last_error,attempts FROM support.inbox WHERE event_key='local-rate-event-key'`).Scan(&status, &code, &attempts); err != nil {
			t.Fatal(err)
		}
		if copyCalled || status != "pending" || code != "target_rate" || attempts != 0 {
			t.Fatalf("local rate state copy=%v status=%s code=%s attempts=%d", copyCalled, status, code, attempts)
		}
	})
}

func TestSupportUnknownMAXDiscoveryCoalescesAndDropsAtCapacity(t *testing.T) {
	f := newSupportFixture(t)
	m := f.webhook(t, 0)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO support.inbox(provider,event_key,event_kind,sender_key,payload_hash,payload,status,finished_at) SELECT 'max','seed-'||n,'discovery',decode('','hex'),decode('','hex'),decode('','hex'),'recorded',now() FROM generate_series(1,1000) AS gs(n)`); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"update_type":"bot_added","timestamp":1731000000,"chat_id":1234,"title":"private title","access_token":"must-not-persist"}`)
	if got := postSupport(m, "max", supportMAXSecret, body); got.Code != http.StatusOK {
		t.Fatalf("full discovery buffer response=%d: %s", got.Code, got.Body.String())
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE event_kind='discovery'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1000 {
		t.Fatalf("full discovery buffer grew to %d rows", count)
	}
}

func TestSupportMAXForwardWithoutBodyIsAcknowledgedWithoutQueueing(t *testing.T) {
	f := newSupportFixture(t)
	m := f.webhook(t, 9001)
	body := []byte(`{"update_type":"message_created","timestamp":1731000000,"message":{"sender":{"user_id":77},"recipient":{"chat_id":9001,"chat_type":"chat"},"link":{"type":"forward"},"body":null}}`)
	if got := postSupport(m, "max", supportMAXSecret, body); got.Code != http.StatusOK {
		t.Fatalf("forward without body response=%d", got.Code)
	}
	if got := postSupport(m, "max", supportMAXSecret, []byte(`{"update_type":"message_created","timestamp":1731000000,"message":{"sender":{"user_id":77},"recipient":{"chat_id":9001,"chat_type":"chat"},"body":null}}`)); got.Code != http.StatusBadRequest {
		t.Fatalf("body:null non-forward response=%d, want 400", got.Code)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("forward without body created %d inbox rows", count)
	}
}
