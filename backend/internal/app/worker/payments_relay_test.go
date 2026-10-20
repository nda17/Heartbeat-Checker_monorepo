package worker

import (
	"context"
	"encoding/json"
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
		Test: true, Provider: "yookassa", Currency: "RUB", OrderID: "order-1", AccountID: "account-1", MonitorCount: 2,
		AmountMinor: 2400, CompletedAt: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("SendPaymentReport() error = %v", err)
	}
	if want := "/telegram-api/bot123:secret/sendMessage"; gotPath != want {
		t.Fatalf("request path = %q, want %q", gotPath, want)
	}
}

func TestPaymentReportUsesImmutableProviderModeForFinancialCopy(t *testing.T) {
	tests := []struct {
		name     string
		report   contracts.PaymentReport
		contains []string
		excludes []string
	}{
		{
			name:     "production payment",
			report:   contracts.PaymentReport{Provider: "yookassa", Currency: "RUB", OrderID: "order-prod", AccountID: "account-1", MonitorCount: 2, AmountMinor: 5800},
			contains: []string{"ЮKassa · Платёж подтверждён", "Сумма: 58.00 RUB"},
			excludes: []string{"Деньги не списывались", "Условная сумма", "TEST"},
		},
		{
			name:     "test payment",
			report:   contracts.PaymentReport{Provider: "yookassa", Test: true, Currency: "RUB", OrderID: "order-test", AccountID: "account-1", MonitorCount: 2, AmountMinor: 5800},
			contains: []string{"TEST · ЮKassa · Платёж подтверждён", "Деньги не списывались", "Условная сумма: 58.00 RUB"},
			excludes: []string{"ЮKassa · Платёж подтверждён\nЗаказ:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var message string
			reporter := newPaymentReporter(config.IntegrationConfig{
				TelegramAPIBaseURL:     "https://messengers-proxy.heartbeat-checker.ru/telegram-api",
				TelegramInformerToken:  "123:secret",
				TelegramInternalChatID: -1001,
			})
			reporter.client.Transport = paymentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				var payload struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatalf("decode report request: %v", err)
				}
				message = payload.Text
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":17}}`)), Request: req}, nil
			})
			if err := reporter.SendPaymentReport(context.Background(), tt.report); err != nil {
				t.Fatalf("SendPaymentReport() error = %v", err)
			}
			for _, value := range tt.contains {
				if !strings.Contains(message, value) {
					t.Errorf("report %q missing %q", message, value)
				}
			}
			for _, value := range tt.excludes {
				if strings.Contains(message, value) {
					t.Errorf("report %q unexpectedly contains %q", message, value)
				}
			}
		})
	}
}
