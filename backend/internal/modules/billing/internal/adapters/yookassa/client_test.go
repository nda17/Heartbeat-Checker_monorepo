package yookassa

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type responseRoundTripper func(*http.Request) (*http.Response, error)

func (rt responseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return rt(request)
}

type partialErrorReader struct{ sent bool }

func (r *partialErrorReader) Read(buffer []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(buffer, `{"id":"partial`), nil
}

func (*partialErrorReader) Close() error { return nil }

func testClient(server *httptest.Server) *Client {
	client, err := New("123456", "test_secret_123456789")
	if err != nil {
		panic(err)
	}
	client.endpoint = server.URL
	client.http = server.Client()
	return client
}

func TestCreatePaymentSendsImmutableKeyAndMapsOnlyReconciliationFields(t *testing.T) {
	const body = `{"amount":{"value":"29.00","currency":"RUB"},"metadata":{"order_id":"a454ad6a-8509-4cc7-908a-fdad7f2c85b3"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/payments" {
			t.Errorf("request = %s %s, want POST /payments", r.Method, r.URL.Path)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "123456" || password != "test_secret_123456789" {
			t.Errorf("unexpected basic auth credentials")
		}
		if got := r.Header.Get("Idempotence-Key"); got != "a454ad6a-8509-4cc7-908a-fdad7f2c85b3" {
			t.Errorf("Idempotence-Key = %q", got)
		}
		if got, _ := io.ReadAll(r.Body); string(got) != body {
			t.Errorf("request body = %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"pay123456789012345","status":"pending","paid":false,"test":true,"amount":{"value":"29.00","currency":"RUB"},"recipient":{"account_id":"123456"},"metadata":{"order_id":"a454ad6a-8509-4cc7-908a-fdad7f2c85b3"},"confirmation":{"type":"redirect","confirmation_url":"https://yookassa.ru/checkout/pay_123"},"payment_method":{"card":{"number":"4111111111111111"}}}`)
	}))
	defer server.Close()

	payment, err := testClient(server).CreatePayment(context.Background(), "a454ad6a-8509-4cc7-908a-fdad7f2c85b3", []byte(body))
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if payment.ID != "pay123456789012345" || payment.Status != "pending" || payment.Paid || payment.Test == nil || !*payment.Test || payment.ShopID != "123456" || payment.AmountValue != "29.00" || payment.Currency != "RUB" || payment.OrderID != "a454ad6a-8509-4cc7-908a-fdad7f2c85b3" || payment.ConfirmationURL != "https://yookassa.ru/checkout/pay_123" {
		t.Fatalf("mapped provider payment = %+v", payment)
	}
	encoded, err := json.Marshal(payment)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "4111111111111111") || strings.Contains(string(encoded), "card") {
		t.Fatalf("provider card fields escaped into internal payment DTO: %s", encoded)
	}
}

func TestGetPaymentUsesEncodedValidatedIDAndOmitsIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/payments/pay123456789012345" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotence-Key") != "" {
			t.Error("GET payment unexpectedly sent an idempotence key")
		}
		_, _ = io.WriteString(w, `{"id":"pay123456789012345","status":"succeeded","paid":true,"test":true,"amount":{"value":"29.00","currency":"RUB"},"recipient":{"account_id":"123456"},"metadata":{"order_id":"a454ad6a-8509-4cc7-908a-fdad7f2c85b3"}}`)
	}))
	defer server.Close()

	payment, err := testClient(server).GetPayment(context.Background(), "pay123456789012345")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if payment.Status != "succeeded" || !payment.Paid {
		t.Fatalf("authoritative payment = %+v", payment)
	}
	if _, err := testClient(server).GetPayment(context.Background(), "../other-shop"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("invalid payment ID error = %v", err)
	}
}

func TestProviderResponseMustBeExplicitTestPaymentForConfiguredShop(t *testing.T) {
	base := `{"id":"pay123456789012345","status":"pending","paid":false,"test":true,"amount":{"value":"29.00","currency":"RUB"},"recipient":{"account_id":"123456"},"metadata":{"order_id":"a454ad6a-8509-4cc7-908a-fdad7f2c85b3"}}`
	cases := []struct {
		name string
		body string
	}{
		{name: "missing test flag", body: `{"id":"pay123456789012345","status":"pending","amount":{"value":"29.00","currency":"RUB"},"recipient":{"account_id":"123456"}}`},
		{name: "live payment", body: strings.Replace(base, `"test":true`, `"test":false`, 1)},
		{name: "wrong shop", body: strings.Replace(base, `"account_id":"123456"`, `"account_id":"654321"`, 1)},
		{name: "unknown status", body: strings.Replace(base, `"status":"pending"`, `"status":"refunded"`, 1)},
		{name: "succeeded but unpaid", body: strings.Replace(base, `"status":"pending"`, `"status":"succeeded"`, 1)},
		{name: "untrusted confirmation origin", body: strings.Replace(base, `"metadata":`, `"confirmation":{"type":"redirect","confirmation_url":"https://attacker.example/pay"},"metadata":`, 1)},
		{name: "trailing JSON", body: base + ` {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := testClient(server).GetPayment(context.Background(), "pay123456789012345")
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("GetPayment() error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestResponseBodyReadFailureIsRetryable(t *testing.T) {
	client, err := New("123456", "test_secret_123456789")
	if err != nil {
		t.Fatal(err)
	}
	client.http = &http.Client{Transport: responseRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &partialErrorReader{},
		}, nil
	})}
	_, err = client.CreatePayment(context.Background(), "a454ad6a-8509-4cc7-908a-fdad7f2c85b3", []byte(`{"amount":{}}`))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CreatePayment() read error = %v, want retryable ErrUnavailable", err)
	}
}
