package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

func validWebhookRequest() contracts.WebhookRequest {
	return contracts.WebhookRequest{
		URL: "https://hooks.example.test/events", Body: []byte("{ \"type\" : \"monitor.down\",\"n\":1 }"),
		EventID: "7dd7cc82-717d-48b5-b160-ff57c23a97d8", Timestamp: "1792353600",
		Signature: "v1=" + strings.Repeat("a", 64),
	}
}

func TestValidateWebhookEnvelopeRejectsMalformedAndOversizedInput(t *testing.T) {
	base := validWebhookRequest()
	for _, test := range []struct {
		name   string
		mutate func(*contracts.WebhookRequest)
	}{
		{name: "empty body", mutate: func(r *contracts.WebhookRequest) { r.Body = nil }},
		{name: "invalid JSON", mutate: func(r *contracts.WebhookRequest) { r.Body = []byte(`{"type":`) }},
		{name: "oversized body", mutate: func(r *contracts.WebhookRequest) {
			r.Body = []byte(`{"p":"` + strings.Repeat("x", maxWebhookBodyBytes) + `"}`)
		}},
		{name: "invalid event id", mutate: func(r *contracts.WebhookRequest) { r.EventID = "event-1" }},
		{name: "nondecimal timestamp", mutate: func(r *contracts.WebhookRequest) { r.Timestamp = "1e6" }},
		{name: "invalid signature hex", mutate: func(r *contracts.WebhookRequest) { r.Signature = "v1=" + strings.Repeat("z", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Body = append([]byte(nil), base.Body...)
			test.mutate(&request)
			if err := validateWebhookEnvelope(request); !errors.Is(err, ErrUnsupportedTarget) {
				t.Fatalf("validateWebhookEnvelope() = %v, want ErrUnsupportedTarget", err)
			}
		})
	}
	if err := validateWebhookEnvelope(base); err != nil {
		t.Fatalf("validateWebhookEnvelope(valid): %v", err)
	}
}

func TestParseWebhookTargetRequiresHTTPSAndPublicHostSyntax(t *testing.T) {
	for _, raw := range []string{
		"http://hooks.example.test/events", "https://user@hooks.example.test/events", "https://hooks.example.test:444/events",
	} {
		if _, err := parseWebhookTarget(raw); !errors.Is(err, ErrUnsupportedTarget) {
			t.Errorf("parseWebhookTarget(%q) error = %v, want ErrUnsupportedTarget", raw, err)
		}
	}
	if _, err := parseWebhookTarget("https://hooks.example.test/events"); err != nil {
		t.Fatalf("parseWebhookTarget(valid HTTPS URL): %v", err)
	}
}

func TestRetryAfterSecondsParsesAndBoundsBothForms(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		value string
		want  int
	}{
		{"15", 15}, {"-1", 0}, {"90000", 86400},
		{now.Add(12 * time.Second).Format(http.TimeFormat), 12},
		{now.Add(48 * time.Hour).Format(http.TimeFormat), 86400},
		{"invalid", 0},
	} {
		if got := retryAfterSeconds(test.value, now); got != test.want {
			t.Errorf("retryAfterSeconds(%q) = %d, want %d", test.value, got, test.want)
		}
	}
}

func TestDeliverWebhookBlocksPrivateDNSAndHonorsRequestDeadline(t *testing.T) {
	t.Run("private DNS answer never dials", func(t *testing.T) {
		dials := 0
		e := testExecutor(testResolver{"hooks.example.test": {netip.MustParseAddr("10.0.0.7")}}, func(context.Context, string, string) (net.Conn, error) {
			dials++
			return nil, errors.New("unexpected dial")
		})
		result, err := e.DeliverWebhook(context.Background(), validWebhookRequest())
		if err != nil || result.ErrorCode != "blocked_target" {
			t.Fatalf("DeliverWebhook() = (%+v, %v), want blocked_target", result, err)
		}
		if dials != 0 {
			t.Fatalf("dialed %d times for private DNS answer", dials)
		}
	})

	t.Run("context timeout maps to bounded result", func(t *testing.T) {
		e := testExecutor(testResolver{"hooks.example.test": {netip.MustParseAddr("93.184.216.34")}}, func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		result, err := e.DeliverWebhook(ctx, validWebhookRequest())
		if err != nil || result.ErrorCode != "request_failed" || result.HTTPStatus != 0 {
			t.Fatalf("DeliverWebhook() = (%+v, %v), want retryable bounded timeout result", result, err)
		}
	})
}
