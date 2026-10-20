package yookassa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

var (
	ErrUnavailable     = errors.New("payment_provider_unavailable")
	ErrInvalidResponse = errors.New("payment_provider_response_invalid")
	ErrNotFound        = errors.New("payment_provider_not_found")
)

// Client uses only the official HTTPS API. Redirects and environment proxies
// must never forward shop credentials to another destination.
type Client struct {
	mode     string
	shopID   string
	secret   string
	endpoint string
	http     *http.Client
}

func New(mode, shopID, secret string) (*Client, error) {
	if (mode != "test" && mode != "production") || shopID == "" || len(shopID) > 32 || strings.Trim(shopID, "0123456789") != "" || len(secret) < 16 || len(secret) > 512 || strings.ContainsAny(secret, " \t\r\n") || (mode == "test" && !strings.HasPrefix(secret, "test_")) || (mode == "production" && strings.HasPrefix(secret, "test_")) {
		return nil, errors.New("invalid YooKassa credentials for selected mode")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{mode: mode, shopID: shopID, secret: secret, endpoint: "https://api.yookassa.ru/v3", http: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) CreatePayment(ctx context.Context, key string, body []byte) (contracts.ProviderPayment, error) {
	if len(key) != 36 || len(body) == 0 || len(body) > 8192 {
		return contracts.ProviderPayment{}, ErrInvalidResponse
	}
	return c.request(ctx, http.MethodPost, "/payments", key, body)
}
func (c *Client) GetPayment(ctx context.Context, id string) (contracts.ProviderPayment, error) {
	if !ValidPaymentID(id) {
		return contracts.ProviderPayment{}, ErrInvalidResponse
	}
	return c.request(ctx, http.MethodGet, "/payments/"+id, "", nil)
}
func ValidPaymentID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
func ValidConfirmationURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 2048 && u.Scheme == "https" && (u.Host == "yoomoney.ru" || u.Host == "yookassa.ru") && u.User == nil && u.Fragment == "" && !strings.ContainsAny(raw, "\\\r\n\t")
}
func (c *Client) request(ctx context.Context, method, path, key string, body []byte) (contracts.ProviderPayment, error) {
	var result contracts.ProviderPayment
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return result, ErrInvalidResponse
	}
	req.SetBasicAuth(c.shopID, c.secret)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotence-Key", key)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return result, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return result, ErrNotFound
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return result, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return result, ErrUnavailable
	}
	if len(data) > 64<<10 {
		return result, ErrInvalidResponse
	}
	var payment paymentResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&payment) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, ErrInvalidResponse
	}
	if !ValidPaymentID(payment.ID) || payment.Test == nil || *payment.Test != (c.mode == "test") || payment.Recipient.AccountID != c.shopID || (payment.Status != "pending" && payment.Status != "waiting_for_capture" && payment.Status != "succeeded" && payment.Status != "canceled") || (payment.Status == "succeeded" && !payment.Paid) || (payment.Confirmation.ConfirmationURL != "" && (payment.Confirmation.Type != "redirect" || !ValidConfirmationURL(payment.Confirmation.ConfirmationURL))) {
		return result, ErrInvalidResponse
	}
	return contracts.ProviderPayment{ID: payment.ID, Status: payment.Status, Paid: payment.Paid, Test: payment.Test, AmountValue: payment.Amount.Value, Currency: payment.Amount.Currency, OrderID: payment.Metadata.OrderID, ShopID: payment.Recipient.AccountID, ConfirmationURL: payment.Confirmation.ConfirmationURL}, nil
}
