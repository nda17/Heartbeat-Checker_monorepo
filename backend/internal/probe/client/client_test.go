package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

const testSecret = "01234567890123456789012345678901"

func clientRequest() contracts.Request {
	return contracts.Request{RunID: "run-1", AttemptToken: "attempt-1", ConfigVersion: 4, URL: "https://example.com/health", Method: http.MethodGet, ExpectedStatus: http.StatusOK}
}

func responseFor(request contracts.Request) contracts.Result {
	return contracts.Result{
		RunID: request.RunID, AttemptToken: request.AttemptToken, ConfigVersion: request.ConfigVersion,
		Outcome: "UP", CheckedAt: time.Now().UTC(), LatencyMS: 1, HTTPStatus: http.StatusOK,
	}
}

func TestClientCheckSendsAuthenticatedRequestAndAcceptsCorrelatedResult(t *testing.T) {
	request := clientRequest()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/check" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testSecret || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing probe authentication or content type")
		}
		var got contracts.Request
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if got != request {
			t.Errorf("request DTO = %+v, want %+v", got, request)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseFor(request))
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := New(server.URL, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(context.Background(), request)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Outcome != "UP" || result.RunID != request.RunID || result.AttemptToken != request.AttemptToken || result.ConfigVersion != request.ConfigVersion {
		t.Fatalf("result = %+v", result)
	}
}

func TestClientDoesNotFollowRedirect(t *testing.T) {
	var redirectTargetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetCalls.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer origin.Close()
	client, err := New(origin.URL, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Check(context.Background(), clientRequest()); err == nil {
		t.Fatal("Check followed or accepted a redirect")
	}
	if got := redirectTargetCalls.Load(); got != 0 {
		t.Fatalf("redirect target was called %d times", got)
	}
}

func TestClientRejectsOversizedAndNonOKResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", 8<<10+1)},
		{name: "unexpected status", status: http.StatusServiceUnavailable, body: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client, err := New(server.URL, testSecret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Check(context.Background(), clientRequest()); err == nil {
				t.Fatal("Check accepted an invalid response")
			}
		})
	}
}

func TestClientRejectsMismatchedOrInvalidResultEnvelope(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*contracts.Result)
		append string
	}{
		{name: "wrong run id", mutate: func(r *contracts.Result) { r.RunID = "other" }},
		{name: "wrong attempt", mutate: func(r *contracts.Result) { r.AttemptToken = "other" }},
		{name: "wrong config version", mutate: func(r *contracts.Result) { r.ConfigVersion++ }},
		{name: "unknown outcome", mutate: func(r *contracts.Result) { r.Outcome = "MAYBE" }},
		{name: "future timestamp", mutate: func(r *contracts.Result) { r.CheckedAt = time.Now().Add(2 * time.Minute) }},
		{name: "negative latency", mutate: func(r *contracts.Result) { r.LatencyMS = -1 }},
		{name: "invalid up status", mutate: func(r *contracts.Result) { r.HTTPStatus = http.StatusCreated }},
		{name: "unknown error code", mutate: func(r *contracts.Result) { r.ErrorCode = "secret raw error" }},
		{name: "unknown field", append: `,"extra":"no"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := clientRequest()
			result := responseFor(request)
			if test.mutate != nil {
				test.mutate(&result)
			}
			payload, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if test.append != "" {
				payload = append(payload[:len(payload)-1], []byte(test.append+"}")...)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
			defer server.Close()
			client, err := New(server.URL, testSecret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Check(context.Background(), request); err == nil {
				t.Fatal("Check accepted invalid result envelope")
			}
		})
	}
}

func TestClientRejectsUnsafeEndpointAndOversizedRequest(t *testing.T) {
	for _, endpoint := range []string{
		"https://example.com", "http://192.0.2.1", "http://user@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?query=1",
	} {
		if _, err := New(endpoint, testSecret); err == nil {
			t.Errorf("New(%q) succeeded, want rejection", endpoint)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("oversized request reached probe server") }))
	defer server.Close()
	client, err := New(server.URL, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	request := clientRequest()
	request.BodyContains = strings.Repeat("x", 9<<10)
	if _, err := client.Check(context.Background(), request); err == nil {
		t.Fatal("Check accepted an oversized request")
	}
}
