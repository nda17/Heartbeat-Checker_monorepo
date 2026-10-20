package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
)

type paymentReporter struct {
	token   string
	chatID  int64
	topicID int64
	client  *http.Client
	baseURL string
}

func newPaymentReporter(cfg config.IntegrationConfig) *paymentReporter {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	baseURL := cfg.TelegramAPIBaseURL
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	baseHost := "api.telegram.org"
	if baseURL == "https://messengers-proxy.heartbeat-checker.ru/telegram-api" {
		baseHost = "messengers-proxy.heartbeat-checker.ru"
	}
	if cfg.TelegramAPIIP != "" {
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || host != baseHost || port != "443" {
				return nil, errors.New("unexpected payment report destination")
			}
			dialer := net.Dialer{Timeout: 10 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(cfg.TelegramAPIIP, port))
		}
	}
	return &paymentReporter{token: cfg.TelegramInformerToken, chatID: cfg.TelegramInternalChatID, topicID: cfg.TelegramPaymentsTopicID, baseURL: baseURL, client: &http.Client{Timeout: 20 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *paymentReporter) SendPaymentReport(ctx context.Context, report contracts.PaymentReport) error {
	if report.Currency != "RUB" || (report.Provider != "mock" && report.Provider != "yookassa") || (report.Provider == "mock" && !report.Test) {
		return &contracts.ReportSendError{Code: "report_invalid"}
	}
	title, notice, amountLabel := "TEST · Имитация оплаты", "Деньги не списывались.\n", "Условная сумма"
	if report.Provider == "yookassa" {
		if report.Test {
			title = "TEST · ЮKassa · Платёж подтверждён"
		} else {
			title, notice, amountLabel = "ЮKassa · Платёж подтверждён", "", "Сумма"
		}
	}
	access := ""
	if report.FulfillmentStatus == "blocked" {
		access = "\nКвота и срок доступа не изменены."
	}
	text := fmt.Sprintf("%s\n%sЗаказ: %s\nАккаунт: %s\nМониторов: %d\n%s: %d.%02d RUB\nПодтверждён: %s", title, notice, report.OrderID, report.AccountID, report.MonitorCount, amountLabel, report.AmountMinor/100, report.AmountMinor%100, report.CompletedAt.UTC().Format(time.RFC3339)) + access
	body, err := json.Marshal(map[string]any{"chat_id": p.chatID, "message_thread_id": p.topicID, "text": text})
	if err != nil {
		return &contracts.ReportSendError{Code: "encode_failed"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/bot"+p.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return &contracts.ReportSendError{Code: "request_invalid"}
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return &contracts.ReportSendError{Code: "provider_unavailable"}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &contracts.ReportSendError{Code: "provider_http", HTTPStatus: response.StatusCode}
		if response.StatusCode == http.StatusTooManyRequests {
			failure.RetryAfter = paymentRetryAfter(response.Header.Get("Retry-After"), data)
		}
		return failure
	}
	var result struct {
		OK         bool `json:"ok"`
		ErrorCode  int  `json:"error_code"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if readErr != nil || len(data) > 64<<10 || json.Unmarshal(data, &result) != nil {
		return &contracts.ReportSendError{Code: "provider_response_invalid", HTTPStatus: response.StatusCode}
	}
	if !result.OK {
		failure := &contracts.ReportSendError{Code: "provider_rejected"}
		if result.ErrorCode >= 100 && result.ErrorCode <= 599 {
			failure.HTTPStatus = result.ErrorCode
		}
		if result.ErrorCode == http.StatusTooManyRequests {
			failure.RetryAfter = paymentRetryAfter(response.Header.Get("Retry-After"), data)
		}
		return failure
	}
	if result.Result.MessageID <= 0 {
		return &contracts.ReportSendError{Code: "provider_response_invalid", HTTPStatus: response.StatusCode}
	}
	return nil
}

func paymentRetryAfter(header string, body []byte) time.Duration {
	var delay time.Duration
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds > 0 && seconds <= 86400 {
		delay = time.Duration(seconds) * time.Second
	} else if until, err := http.ParseTime(header); err == nil {
		delay = time.Until(until)
	}
	var result struct {
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &result) == nil && result.Parameters.RetryAfter > 0 && result.Parameters.RetryAfter <= 86400 {
		fromBody := time.Duration(result.Parameters.RetryAfter) * time.Second
		if fromBody > delay {
			delay = fromBody
		}
	}
	if delay < 0 {
		return 0
	}
	if delay > 24*time.Hour {
		return 24 * time.Hour
	}
	return delay
}
