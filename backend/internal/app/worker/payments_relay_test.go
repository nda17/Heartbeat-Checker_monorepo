package worker

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
)

type paymentRoundTripFunc func(*http.Request) (*http.Response, error)

func (f paymentRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPaymentReportUsesConfiguredRelayPath(t *testing.T) {
	const baseURL = "https://messengers-proxy.heartbeat-checker.ru/telegram-api"
	var gotPath string
	reporter := newPaymentReporter(config.IntegrationConfig{
		TelegramAPIBaseURL:      baseURL,
		TelegramInformerToken:   "123:secret",
		TelegramInternalChatID:  -1001,
		TelegramPaymentsTopicID: 8,
	})
	reporter.client.Transport = paymentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotPath = req.URL.EscapedPath()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":17}}`)), Request: req}, nil
	})
	err := reporter.SendPaymentReport(context.Background(), contracts.PaymentReport{
		Test: true, Currency: "RUB", OrderID: "order-1", AccountID: "account-1", MonitorCount: 2,
		AmountMinor: 2400, CompletedAt: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("SendPaymentReport() error = %v", err)
	}
	if want := "/telegram-api/bot123:secret/sendMessage"; gotPath != want {
		t.Fatalf("request path = %q, want %q", gotPath, want)
	}
}
