package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

func webhookClientRequest() contracts.WebhookRequest {
	return contracts.WebhookRequest{
		URL: "https://hooks.example.test/notify", Body: []byte("{ \"type\" : \"monitor.down\",\"sequence\":3 }"),
		EventID: "7dd7cc82-717d-48b5-b160-ff57c23a97d8", Timestamp: "1792353600", Signature: "v1=" + strings.Repeat("a", 64),
	}
}

func TestDeliverWebhookSendsExactBodyBytesAndAcceptsRetryMetadata(t *testing.T) {
	input := webhookClientRequest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/webhook" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testSecret || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing probe authentication or JSON content type")
		}
		var got contracts.WebhookRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode webhook request: %v", err)
		}
		if string(got.Body) != string(input.Body) || got.EventID != input.EventID || got.Timestamp != input.Timestamp || got.Signature != input.Signature || got.URL != input.URL {
			t.Errorf("webhook request = %+v, body bytes %q; want input %+v, body bytes %q", got, got.Body, input, input.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"eventId":%q,"httpStatus":429,"errorCode":"http_status","retryAfterSeconds":30}`, input.EventID)
	}))
	defer server.Close()
	client, err := New(server.URL, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DeliverWebhook(context.Background(), input)
	if err != nil {
		t.Fatalf("DeliverWebhook: %v", err)
	}
	if result.EventID != input.EventID || result.HTTPStatus != http.StatusTooManyRequests || result.ErrorCode != "http_status" || result.RetryAfterSeconds != 30 {
		t.Fatalf("result = %+v", result)
	}

}

func TestDeliverWebhookRejectsInvalidAndOversizedProbeResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "mismatched event", body: `{"eventId":"other","httpStatus":200}`},
		{name: "unknown field", body: `{"eventId":"7dd7cc82-717d-48b5-b160-ff57c23a97d8","httpStatus":200,"extra":true}`},
		{name: "success with retry status", body: `{"eventId":"7dd7cc82-717d-48b5-b160-ff57c23a97d8","httpStatus":503}`},
		{name: "oversized response", body: strings.Repeat("x", 8<<10+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, test.body) }))
			defer server.Close()
			client, err := New(server.URL, testSecret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.DeliverWebhook(context.Background(), webhookClientRequest()); err == nil {
				t.Fatal("DeliverWebhook accepted invalid probe response")
			}
		})
	}
}

func TestDeliverWebhookRejectsNonJSONAndOversizedRequestBeforeNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid webhook request reached probe") }))
	defer server.Close()
	client, err := New(server.URL, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{nil, []byte("not-json"), []byte(`{"padding":"` + strings.Repeat("x", 16<<10) + `"}`)} {
		request := webhookClientRequest()
		request.Body = body
		if _, err := client.DeliverWebhook(context.Background(), request); err == nil {
			t.Fatalf("DeliverWebhook accepted invalid body of length %d", len(body))
		}
	}
}
