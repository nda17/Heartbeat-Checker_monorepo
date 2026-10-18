package support

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	supportWelcomeText = "Вас приветствует служба технической поддержки. Вы можете задать интересующий вас вопрос в данный чат, и мы ответим вам в ближайшее время."
	supportAckText     = "Ваш вопрос отправлен специалисту поддержки. Ожидайте ответа в текущем чате."
)

func TestSupportStartNormalizationBoundariesAndLegacyTelegramShape(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		chat  string
		start bool
	}{
		{name: "command", text: "/start", chat: "private", start: true},
		{name: "command with payload", text: "/start continue=1", chat: "private", start: true},
		{name: "prefix is not command", text: "/starter", chat: "private"},
		{name: "longer prefix is not command", text: "/startx", chat: "private"},
		{name: "group command is not a welcome", text: "/start", chat: "supergroup"},
		{name: "ordinary message", text: "Need help", chat: "private"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := telegramPrivate(int64(100+index), int64(200+index), 321, tc.text)
			if tc.chat != "private" {
				body = []byte(strings.Replace(string(body), `"type":"private"`, `"type":"supergroup"`, 1))
			}
			e, key, err := normalize("telegram", body)
			if err != nil || e == nil {
				t.Fatalf("normalize Telegram update: event=%+v key=%q err=%v", e, key, err)
			}
			if e.Start != tc.start {
				t.Fatalf("start=%t for text %q in %s, want %t", e.Start, tc.text, tc.chat, tc.start)
			}
			if e.Text != "" {
				t.Fatalf("Telegram text leaked into durable event: %q", e.Text)
			}
			if e.ContentDigest == "" || key == "" {
				t.Fatalf("legacy Telegram dedupe fields lost: digest=%q key=%q", e.ContentDigest, key)
			}
		})
	}
}

func TestSupportInternalAcknowledgementsDoNotConsumeCustomerHourlyAdmission(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9001)
	ctx := context.Background()
	for i := 1; i <= 120; i++ {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = m.enqueueAck(ctx, tx, job{ID: int64(i), Provider: "telegram"}, route{Provider: "telegram", ChatID: 111, UserID: 111})
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("enqueue internal acknowledgement %d: %v", i, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// The acknowledgements have already been delivered, but their durable
	// dedupe markers remain in the one-hour audit window.
	if _, err := f.pool.Exec(ctx, `UPDATE support.inbox SET status='sent',finished_at=now(),payload=decode('','hex') WHERE event_kind='ack'`); err != nil {
		t.Fatal(err)
	}
	var acknowledgements int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM support.inbox WHERE event_kind='ack' AND sender_key=$1 AND created_at>now()-interval '1 hour'`, m.digest([]byte("telegram:111"))).Scan(&acknowledgements); err != nil || acknowledgements != 120 {
		t.Fatalf("seeded acknowledgements=%d err=%v, want 120 recent internal rows", acknowledgements, err)
	}
	if err := m.admit(ctx, "telegram", "after-ack-backlog", event{Provider: "telegram", SenderID: 111, ChatID: 111, MessageID: "after-ack-backlog", Private: true, Text: "customer question"}); err != nil {
		t.Fatalf("internal acknowledgements consumed the customer's hourly admission budget: %v", err)
	}
}

func TestSupportMAXBotStartedNormalizationRequiresARealUser(t *testing.T) {
	body := []byte(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":1234,"user":{"user_id":321,"is_bot":false}}`)
	e, _, err := normalize("max", body)
	if err != nil || e == nil || !e.Start || e.SenderID != 321 || e.ChatID != 1234 || !e.Private || e.Bot {
		t.Fatalf("normalize MAX bot_started event=%+v err=%v", e, err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":1234}`),
		[]byte(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":1234,"user":{"user_id":0,"is_bot":false}}`),
	} {
		if event, _, err := normalize("max", invalid); err == nil || event != nil {
			t.Errorf("invalid MAX bot_started normalized event=%+v err=%v, want rejection", event, err)
		}
	}
}

func TestSupportWebhookDoesNotEnqueueGroupBotOrAnonymousStarts(t *testing.T) {
	f := newSupportFixture(t)
	webhook := f.webhook(t, 9001)
	group := strings.Replace(string(telegramPrivate(1, 1, 321, "/start")), `"type":"private"`, `"type":"supergroup"`, 1)
	bot := strings.Replace(string(telegramPrivate(2, 2, 322, "/start")), `"is_bot":false`, `"is_bot":true`, 1)
	anonymous := strings.Replace(string(telegramPrivate(3, 3, 323, "/start")), `"from":{"id":323,"is_bot":false}`, `"from":{"id":323,"is_bot":false},"sender_chat":{"id":-100}`, 1)
	maxBot := []byte(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":1234,"user":{"user_id":324,"is_bot":true}}`)
	for name, body := range map[string][]byte{
		"telegram group":     []byte(group),
		"telegram bot":       []byte(bot),
		"telegram anonymous": []byte(anonymous),
		"MAX bot":            maxBot,
	} {
		provider := "telegram"
		secret := supportTelegramSecret
		if name == "MAX bot" {
			provider, secret = "max", supportMAXSecret
		}
		if got := postSupport(webhook, provider, secret, body); got.Code != http.StatusOK {
			t.Errorf("%s webhook status=%d: %s", name, got.Code, got.Body.String())
		}
	}
	var queued int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("group/bot/anonymous starts enqueued %d inbox rows", queued)
	}
}

func supportTelegramClient(t *testing.T, handle func(method string, payload map[string]any) (int, string)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		payload := map[string]any{}
		if req.Body != nil {
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				return nil, err
			}
		}
		status, body := 200, `{"ok":true,"result":{}}`
		switch method {
		case "getMe":
			body = `{"ok":true,"result":{"id":700,"is_bot":true}}`
		case "getChatMember":
			userID, _ := payload["user_id"].(float64)
			body = fmt.Sprintf(`{"ok":true,"result":{"status":"administrator","user":{"id":%d,"is_bot":false}}}`, int64(userID))
		case "copyMessage":
			body = `{"ok":true,"result":{"message_id":501}}`
		case "sendMessage":
			body = `{"ok":true,"result":{"message_id":601}}`
		}
		if handle != nil {
			if overrideStatus, overrideBody := handle(method, payload); overrideBody != "" {
				status, body = overrideStatus, overrideBody
			}
		}
		return jsonResponse(status, body), nil
	})}
}

func postSupportOnMux(mux *http.ServeMux, provider, secret string, body []byte) *httptest.ResponseRecorder {
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

func TestSupportTelegramWelcomeIsDeduplicatedAcrossParallelStartsAndRestart(t *testing.T) {
	f := newSupportFixture(t)
	webhook := f.webhook(t, 9001)
	mux := http.NewServeMux()
	webhook.Register(mux)

	const senderID = int64(321)
	const workers = 12
	statuses := make([]int, workers)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := telegramPrivate(int64(1000+i), int64(2000+i), senderID, "/start payload")
			statuses[i] = postSupportOnMux(mux, "telegram", supportTelegramSecret, body).Code
		}(i)
	}
	wg.Wait()
	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("parallel /start webhook %d status=%d, want 200", i, status)
		}
	}
	// An exact webhook replay and later start command must resolve to the same
	// per-user marker despite changing update/message IDs and command payload.
	for _, body := range [][]byte{
		telegramPrivate(1000, 2000, senderID, "/start payload"),
		telegramPrivate(3000, 4000, senderID, "/start again"),
	} {
		if got := postSupportOnMux(mux, "telegram", supportTelegramSecret, body); got.Code != http.StatusOK {
			t.Fatalf("replayed /start status=%d: %s", got.Code, got.Body.String())
		}
	}

	wantKey := "welcome:" + hex.EncodeToString(webhook.digest([]byte("telegram:321")))
	var welcomeCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE provider='telegram' AND event_kind='welcome'`).Scan(&welcomeCount); err != nil {
		t.Fatal(err)
	}
	if welcomeCount != 1 {
		t.Fatalf("parallel starts created %d welcome markers, want one", welcomeCount)
	}
	var storedKey string
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT event_key,payload FROM support.inbox WHERE provider='telegram' AND event_kind='welcome'`).Scan(&storedKey, &payload); err != nil {
		t.Fatal(err)
	}
	if storedKey != wantKey || strings.Contains(storedKey, "321") {
		t.Fatalf("welcome key=%q, want stable HMAC key %q without raw user ID", storedKey, wantKey)
	}
	if len(payload) < webhook.cipher.NonceSize() {
		t.Fatalf("welcome payload is not encrypted: %d bytes", len(payload))
	}
	plain, err := webhook.cipher.Open(nil, payload[:webhook.cipher.NonceSize()], payload[webhook.cipher.NonceSize():], []byte("telegram:"+wantKey))
	if err != nil {
		t.Fatalf("decrypt stable welcome recipient: %v", err)
	}
	var recipient map[string]json.RawMessage
	if err := json.Unmarshal(plain, &recipient); err != nil {
		t.Fatalf("decode stable welcome recipient: %v", err)
	}
	allowedRecipientFields := map[string]bool{"provider": true, "chatId": true, "senderId": true, "private": true}
	for field := range recipient {
		if !allowedRecipientFields[field] {
			t.Fatalf("welcome recipient retained non-recipient field %q: %s", field, plain)
		}
	}
	if !strings.Contains(string(plain), `"provider":"telegram"`) || !strings.Contains(string(plain), `"chatId":321`) || !strings.Contains(string(plain), `"senderId":321`) {
		t.Fatalf("welcome payload did not contain stable recipient identifiers: %s", plain)
	}
	if strings.Contains(string(plain), "/start") || strings.Contains(string(plain), "payload") || strings.Contains(string(plain), "2000") {
		t.Fatalf("welcome recipient retained transient command data: %s", plain)
	}

	var sentTexts []string
	var sendCount int
	worker := f.worker(t, 9001)
	worker.client = supportTelegramClient(t, func(method string, payload map[string]any) (int, string) {
		if method == "sendMessage" {
			sendCount++
			if text, ok := payload["text"].(string); ok {
				sentTexts = append(sentTexts, text)
			}
		}
		return 200, ""
	})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process first welcome processed=%t err=%v", processed, err)
	}
	if sendCount != 1 || len(sentTexts) != 1 || sentTexts[0] != supportWelcomeText {
		t.Fatalf("welcome send count=%d texts=%q, want one exact welcome", sendCount, sentTexts)
	}
	var status string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM support.inbox WHERE event_key=$1`, wantKey).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "sent" {
		t.Fatalf("welcome marker status=%s, want sent", status)
	}
	var storedPayload []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT payload FROM support.inbox WHERE event_key=$1`, wantKey).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	if len(storedPayload) != 0 {
		t.Fatalf("sent welcome retained encrypted recipient payload (%d bytes)", len(storedPayload))
	}

	restarted := f.worker(t, 9001)
	restarted.client = supportTelegramClient(t, func(method string, payload map[string]any) (int, string) {
		if method == "sendMessage" {
			sendCount++
		}
		return 200, ""
	})
	if processed, err := restarted.ProcessOne(context.Background()); err != nil || processed {
		t.Fatalf("restart reprocessed sent welcome processed=%t err=%v", processed, err)
	}
	if sendCount != 1 {
		t.Fatalf("restart repeated welcome send; send count=%d", sendCount)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET finished_at=now()-interval '31 days' WHERE event_key=$1`, wantKey); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Cleanup(context.Background(), tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,octet_length(payload) FROM support.inbox WHERE event_key=$1`, wantKey).Scan(&status, &welcomeCount); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || welcomeCount != 0 {
		t.Fatalf("retained welcome after cleanup status=%s payload_bytes=%d, want sent/0", status, welcomeCount)
	}
}

func TestSupportTelegramAckRetryDoesNotRepeatForwardAfterRestart(t *testing.T) {
	f := newSupportFixture(t)
	worker := f.worker(t, 9001)
	e := event{Provider: "telegram", SenderID: 321, ChatID: 321, MessageID: "42", Private: true, Text: "please help"}
	if err := worker.admit(context.Background(), "telegram", "customer-question", e); err != nil {
		t.Fatal(err)
	}
	var copies, acks int
	ackFails := true
	worker.client = supportTelegramClient(t, func(method string, payload map[string]any) (int, string) {
		switch method {
		case "copyMessage":
			copies++
		case "sendMessage":
			acks++
			if ackFails {
				return http.StatusInternalServerError, `{"ok":false,"error_code":500,"description":"temporary outage"}`
			}
			if payload["text"] != supportAckText || payload["chat_id"] != float64(321) {
				t.Errorf("ack payload=%v, want exact customer acknowledgement", payload)
			}
		}
		return 200, ""
	})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("forward customer question processed=%t err=%v", processed, err)
	}
	var sourceID int64
	var sourceStatus string
	if err := f.pool.QueryRow(context.Background(), `SELECT id,status FROM support.inbox WHERE event_key='customer-question'`).Scan(&sourceID, &sourceStatus); err != nil {
		t.Fatal(err)
	}
	if sourceStatus != "sent" || copies != 1 {
		t.Fatalf("source status=%s copies=%d, want sent/1", sourceStatus, copies)
	}
	var ackKey, ackStatus string
	var ackPayload []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT event_key,status,payload FROM support.inbox WHERE event_kind='ack' AND provider='telegram'`).Scan(&ackKey, &ackStatus, &ackPayload); err != nil {
		t.Fatal(err)
	}
	if ackKey != fmt.Sprintf("ack:%d", sourceID) || ackStatus != "pending" {
		t.Fatalf("ack key=%q status=%s, want ack:%d/pending", ackKey, ackStatus, sourceID)
	}
	ackPlain, err := worker.cipher.Open(nil, ackPayload[:worker.cipher.NonceSize()], ackPayload[worker.cipher.NonceSize():], []byte("telegram:"+ackKey))
	if err != nil {
		t.Fatalf("decrypt stable acknowledgement recipient: %v", err)
	}
	var ackRecipient map[string]json.RawMessage
	if err := json.Unmarshal(ackPlain, &ackRecipient); err != nil {
		t.Fatalf("decode acknowledgement recipient: %v", err)
	}
	if len(ackRecipient) != 4 || !strings.Contains(string(ackPlain), `"chatId":321`) || !strings.Contains(string(ackPlain), `"senderId":321`) {
		t.Fatalf("ack payload should contain only stable recipient fields: %s", ackPlain)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("first acknowledgement attempt processed=%t err=%v", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM support.inbox WHERE event_key=$1`, ackKey).Scan(&ackStatus); err != nil {
		t.Fatal(err)
	}
	if ackStatus != "pending" || copies != 1 || acks != 1 {
		t.Fatalf("failed ack status=%s copies=%d sends=%d, want pending/1/1", ackStatus, copies, acks)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET next_attempt_at=now() WHERE event_key=$1`, ackKey); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM support.target_rates`); err != nil {
		t.Fatal(err)
	}
	ackFails = false
	restarted := f.worker(t, 9001)
	restarted.client = worker.client
	if processed, err := restarted.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("retry acknowledgement after restart processed=%t err=%v", processed, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT status,octet_length(payload) FROM support.inbox WHERE event_key=$1`, ackKey).Scan(&ackStatus, &sourceID); err != nil {
		t.Fatal(err)
	}
	if ackStatus != "sent" || sourceID != 0 || copies != 1 || acks != 2 {
		t.Fatalf("completed ack status=%s payload_bytes=%d copies=%d sends=%d, want sent/0/1/2", ackStatus, sourceID, copies, acks)
	}
}

func TestSupportFailedForwardAndUnsupportedMessageDoNotEnqueueSuccessAck(t *testing.T) {
	t.Run("provider forward failure", func(t *testing.T) {
		f := newSupportFixture(t)
		m := f.worker(t, 9001)
		if err := m.admit(context.Background(), "telegram", "forward-failure", event{Provider: "telegram", ChatID: 321, SenderID: 321, MessageID: "70", Private: true, Text: "question"}); err != nil {
			t.Fatal(err)
		}
		var sends int
		m.client = supportTelegramClient(t, func(method string, payload map[string]any) (int, string) {
			if method == "copyMessage" {
				sends++
				return http.StatusInternalServerError, `{"ok":false,"error_code":500}`
			}
			return 200, ""
		})
		if processed, err := m.ProcessOne(context.Background()); err != nil || !processed {
			t.Fatalf("failed forward processed=%t err=%v", processed, err)
		}
		var ackCount, routeCount int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE event_kind='ack'`).Scan(&ackCount); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.message_routes`).Scan(&routeCount); err != nil {
			t.Fatal(err)
		}
		if sends != 1 || ackCount != 0 || routeCount != 0 {
			t.Fatalf("failed forward sends=%d ack rows=%d routes=%d", sends, ackCount, routeCount)
		}
	})

	t.Run("unsupported customer message", func(t *testing.T) {
		f := newSupportFixture(t)
		m := f.worker(t, 9001)
		e := event{Provider: "telegram", ChatID: 321, SenderID: 321, MessageID: "71", Private: true, Unsupported: true}
		if err := m.admit(context.Background(), "telegram", "unsupported-customer", e); err != nil {
			t.Fatal(err)
		}
		var copied, sentText int
		m.client = supportTelegramClient(t, func(method string, payload map[string]any) (int, string) {
			switch method {
			case "copyMessage":
				copied++
				return http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"unsupported message"}`
			case "sendMessage":
				sentText++
				if payload["text"] == supportAckText {
					t.Errorf("unsupported message received success acknowledgement")
				}
			}
			return 200, ""
		})
		if processed, err := m.ProcessOne(context.Background()); err != nil || !processed {
			t.Fatalf("unsupported message processed=%t err=%v", processed, err)
		}
		var ackCount int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE event_kind='ack'`).Scan(&ackCount); err != nil {
			t.Fatal(err)
		}
		if copied != 1 || sentText != 1 || ackCount != 0 {
			t.Fatalf("unsupported delivery copy=%d text notices=%d ack rows=%d", copied, sentText, ackCount)
		}
	})
}

func TestSupportStaleForwardLeaseCannotCommitRouteOrAck(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9001)
	if err := m.admit(context.Background(), "telegram", "stale-forward", event{Provider: "telegram", ChatID: 321, SenderID: 321, MessageID: "80", Private: true, Text: "question"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := m.claim(context.Background())
	if err != nil || claimed == nil {
		t.Fatalf("claim source=%+v err=%v", claimed, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET lease_until=now()-interval '1 second' WHERE id=$1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	err = m.saveRoute(context.Background(), *claimed, "forwarded-80", route{Provider: "telegram", ChatID: 321, UserID: 321, MessageID: "80"}, true)
	if !errors.Is(err, errRejected) {
		t.Fatalf("stale saveRoute error=%v, want lease rejection", err)
	}
	var routes, acknowledgements int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.message_routes`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE event_kind='ack'`).Scan(&acknowledgements); err != nil {
		t.Fatal(err)
	}
	if routes != 0 || acknowledgements != 0 {
		t.Fatalf("stale lease committed route=%d ack=%d", routes, acknowledgements)
	}
}

func TestSupportMAXBotStartedQueuesAndSendsWelcome(t *testing.T) {
	f := newSupportFixture(t)
	webhook := f.webhook(t, 9001)
	body := []byte(`{"update_type":"bot_started","timestamp":1730000000,"chat_id":1234,"user":{"user_id":321,"is_bot":false}}`)
	for i := 0; i < 2; i++ {
		if got := postSupport(webhook, "max", supportMAXSecret, body); got.Code != http.StatusOK {
			t.Fatalf("MAX bot_started attempt %d status=%d: %s", i, got.Code, got.Body.String())
		}
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM support.inbox WHERE provider='max' AND event_kind='welcome'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("MAX bot_started created %d welcome rows, want one", count)
	}
	m := f.worker(t, 9001)
	var target string
	var notice string
	m.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "platform-api2.max.ru" || req.URL.Path != "/messages" {
			return nil, fmt.Errorf("unexpected MAX request %s %s", req.Method, req.URL)
		}
		target = req.URL.Query().Get("user_id")
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		notice, _ = body["text"].(string)
		return jsonResponse(200, `{"message":{"body":{"mid":"welcome-1"}}}`), nil
	})}
	if processed, err := m.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process MAX welcome=%t err=%v", processed, err)
	}
	if target != "321" || notice != supportWelcomeText {
		t.Fatalf("MAX welcome target=%q notice=%q", target, notice)
	}
}

func TestSupportWelcomeProviderFailureIsTerminalAcrossRestart(t *testing.T) {
	f := newSupportFixture(t)
	webhook := f.webhook(t, 9001)
	if got := postSupport(webhook, "telegram", supportTelegramSecret, telegramPrivate(55, 66, 444, "/start")); got.Code != http.StatusOK {
		t.Fatalf("/start status=%d", got.Code)
	}
	sends := 0
	worker := f.worker(t, 9001)
	worker.client = supportTelegramClient(t, func(method string, payload map[string]any) (int, string) {
		if method == "sendMessage" {
			sends++
			return http.StatusForbidden, `{"ok":false,"error_code":403,"description":"blocked"}`
		}
		return 200, ""
	})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("failed welcome processed=%t err=%v", processed, err)
	}
	var status string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM support.inbox WHERE event_kind='welcome'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || sends != 1 {
		t.Fatalf("welcome status=%s sends=%d, want failed/1", status, sends)
	}
	restarted := f.worker(t, 9001)
	restarted.client = worker.client
	if processed, err := restarted.ProcessOne(context.Background()); err != nil || processed {
		t.Fatalf("restart retried terminal welcome processed=%t err=%v", processed, err)
	}
	if sends != 1 {
		t.Fatalf("terminal welcome retried %d times", sends)
	}
}

func TestSupportQueueExpiryAndAttemptLimitCoverAllDeliverableKinds(t *testing.T) {
	f := newSupportFixture(t)
	m := f.worker(t, 9001)
	if err := m.admit(context.Background(), "telegram", "source-for-ack", event{Provider: "telegram", ChatID: 321, SenderID: 321, MessageID: "90", Private: true, Text: "source"}); err != nil {
		t.Fatal(err)
	}
	source, err := m.claim(context.Background())
	if err != nil || source == nil {
		t.Fatalf("claim ack source=%+v err=%v", source, err)
	}
	if err := m.saveRoute(context.Background(), *source, "operator-copy-90", route{Provider: "telegram", ChatID: 321, UserID: 321, MessageID: "90"}, true); err != nil {
		t.Fatalf("save source route and enqueue ack: %v", err)
	}
	if err := m.admit(context.Background(), "telegram", "expiring-message", event{Provider: "telegram", ChatID: 654, SenderID: 654, MessageID: "91", Private: true, Text: "expire"}); err != nil {
		t.Fatal(err)
	}
	if err := m.admit(context.Background(), "telegram", "expiring-start", event{Provider: "telegram", ChatID: 777, SenderID: 777, Private: true, Start: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET created_at=now()-interval '25 hours' WHERE event_kind IN ('message','welcome','ack') AND status='pending'`); err != nil {
		t.Fatal(err)
	}
	if claimed, err := m.claim(context.Background()); err != nil || claimed != nil {
		t.Fatalf("24-hour-expired row was claimed=%+v err=%v", claimed, err)
	}
	for _, kind := range []string{"message", "welcome", "ack"} {
		var status string
		if err := f.pool.QueryRow(context.Background(), `SELECT status FROM support.inbox WHERE event_kind=$1 AND status<>'sent'`, kind).Scan(&status); err != nil {
			t.Fatalf("load expired %s row: %v", kind, err)
		}
		if status != "failed" {
			t.Errorf("expired %s row status=%s, want failed", kind, status)
		}
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE support.inbox SET status='pending',finished_at=NULL,created_at=now(),attempts=8 WHERE event_kind IN ('message','welcome','ack') AND status='failed'`); err != nil {
		t.Fatal(err)
	}
	if claimed, err := m.claim(context.Background()); err != nil || claimed != nil {
		t.Fatalf("eight-attempt row was claimed=%+v err=%v", claimed, err)
	}
	var retryable, failed int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE status='pending'),count(*) FILTER (WHERE status='failed') FROM support.inbox WHERE event_kind IN ('message','welcome','ack') AND attempts=8`).Scan(&retryable, &failed); err != nil {
		t.Fatal(err)
	}
	if retryable != 0 || failed != 3 {
		t.Fatalf("eight-attempt rows pending=%d failed=%d, want 0/3", retryable, failed)
	}
}
