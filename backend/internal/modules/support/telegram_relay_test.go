package support

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type telegramRoundTripFunc func(*http.Request) (*http.Response, error)

func (f telegramRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTelegramMethodsUseConfiguredRelayPath(t *testing.T) {
	const baseURL = "https://messengers-proxy.heartbeat-checker.ru/telegram-api"
	var paths []string
	client := &http.Client{Transport: telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.EscapedPath())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`)), Request: req}, nil
	})}
	module := &Module{cfg: Config{TelegramToken: "123:secret", TelegramAPIBaseURL: baseURL}, client: client}
	for _, method := range []string{"getChatMember", "copyMessage", "sendMessage"} {
		if err := module.telegram(context.Background(), method, map[string]any{}, &map[string]any{}); err != nil {
			t.Fatalf("telegram(%q): %v", method, err)
		}
	}
	want := []string{
		"/telegram-api/bot123:secret/getChatMember",
		"/telegram-api/bot123:secret/copyMessage",
		"/telegram-api/bot123:secret/sendMessage",
	}
	if len(paths) != len(want) {
		t.Fatalf("requests = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("request path %d = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestSupportRejectsUnapprovedTelegramBaseAndPrivatePin(t *testing.T) {
	base := Config{EncryptionKey: supportKey, TelegramToken: "123:secret", MAXToken: "max-token", InternalChatID: supportOperatorChat, SupportTopicID: 4}
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{name: "unapproved base", edit: func(cfg *Config) { cfg.TelegramAPIBaseURL = "https://127.0.0.1/telegram-api" }},
		{name: "private pinned address", edit: func(cfg *Config) { cfg.TelegramAPIIP = "127.0.0.1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			if _, err := New(&pgxpool.Pool{}, cfg); err == nil {
				t.Fatal("New() accepted an unsafe Telegram destination")
			}
		})
	}
}
