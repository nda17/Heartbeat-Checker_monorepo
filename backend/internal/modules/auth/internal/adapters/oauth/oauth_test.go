package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func oauthResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func testVKProvider(transport http.RoundTripper) *Provider {
	p, err := New(Config{Provider: "vk", ClientID: "12345678", RedirectURI: "https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback"})
	if err != nil {
		panic(err)
	}
	p.client = &http.Client{Transport: transport}
	return p
}

func TestVKExchangeRejectsStateAndUserIdentityMismatch(t *testing.T) {
	tests := []struct {
		name, token, info string
	}{
		{"state mismatch", `{"access_token":"token","state":"other","user_id":42}`, `{"user":{"user_id":42,"email":"u@example.test"}}`},
		{"userinfo subject mismatch", `{"access_token":"token","state":"expected","user_id":42}`, `{"user":{"user_id":43,"email":"u@example.test"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testVKProvider(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/oauth2/auth" {
					return oauthResponse(http.StatusOK, tt.token), nil
				}
				return oauthResponse(http.StatusOK, tt.info), nil
			}))
			if _, err := p.Exchange(context.Background(), "code", "verifier", "device", "expected"); err == nil {
				t.Fatal("Exchange() accepted a provider identity mismatch")
			}
		})
	}
}

func TestVKExchangeRejectsMalformedSubjectAndTokenEndpointFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		token  string
	}{
		{"malformed subject", http.StatusOK, `{"access_token":"token","state":"expected","user_id":"NaN"}`},
		{"token endpoint rejected", http.StatusBadGateway, `{"access_token":"token","state":"expected","user_id":42}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testVKProvider(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return oauthResponse(tt.status, tt.token), nil
			}))
			if _, err := p.Exchange(context.Background(), "code", "verifier", "device", "expected"); err == nil {
				t.Fatal("Exchange() accepted malformed or rejected token response")
			}
		})
	}
}

func TestProviderExchangeTimeoutIsBounded(t *testing.T) {
	p := testVKProvider(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	p.client = &http.Client{Transport: p.client.Transport, Timeout: 40 * time.Millisecond}
	started := time.Now()
	_, err := p.Exchange(context.Background(), "code", "verifier", "device", "expected")
	if err == nil {
		t.Fatal("Exchange() succeeded despite a stalled token endpoint")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("stalled provider request took %v, timeout was not bounded", elapsed)
	}
}
