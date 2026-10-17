package turnstile

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func clientForTest(t *testing.T, secret, hostname string, local bool, transport roundTripFunc) *Client {
	t.Helper()
	client, err := New(secret, hostname, local)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport
	return client
}

func TestNewEnforcesTestingSecretModeAndProductionHostname(t *testing.T) {
	tests := []struct {
		name, secret, hostname string
		local                  bool
	}{
		{"missing secret", "", "example.com", false},
		{"testing secret outside local mode", "1x0000000000000000000000000000000AA", "example.com", false},
		{"non-testing secret in local mode", "production-secret", "", true},
		{"missing production hostname", "production-secret", "", false},
		{"invalid production hostname", "production-secret", "https://example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.secret, tt.hostname, tt.local); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
	if _, err := New("1x0000000000000000000000000000000AA", "", true); err != nil {
		t.Fatalf("New() with official test secret and local mode: %v", err)
	}
}

func TestVerifyPostsExpectedFormAndAcceptsMatchingProductionResponse(t *testing.T) {
	client := clientForTest(t, "production-secret", "Example.com", false, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != siteverifyURL {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL, siteverifyURL)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm(): %v", err)
		}
		if got := r.PostForm.Get("secret"); got != "production-secret" {
			t.Errorf("secret = %q", got)
		}
		if got := r.PostForm.Get("response"); got != "token with spaces" {
			t.Errorf("response = %q", got)
		}
		if got := r.PostForm.Get("remoteip"); got != "192.0.2.10" {
			t.Errorf("remoteip = %q", got)
		}
		return response(http.StatusOK, `{"success":true,"hostname":"example.COM","action":"register"}`), nil
	})
	if err := client.Verify(context.Background(), "token with spaces", "192.0.2.10", "register"); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestVerifyRejectsInvalidInputsWithoutCallingProvider(t *testing.T) {
	calls := 0
	client := clientForTest(t, "production-secret", "example.com", false, func(*http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusOK, `{"success":true,"hostname":"example.com","action":"register"}`), nil
	})
	for _, tt := range []struct{ token, ip, action string }{
		{"", "", "register"},
		{strings.Repeat("a", 2049), "", "register"},
		{"token", "", ""},
		{"token", "not-an-ip", "register"},
	} {
		if err := client.Verify(context.Background(), tt.token, tt.ip, tt.action); !errors.Is(err, ErrInvalid) {
			t.Errorf("Verify(%q, %q, %q) = %v, want ErrInvalid", tt.token, tt.ip, tt.action, err)
		}
	}
	if calls != 0 {
		t.Fatalf("provider called %d times for invalid input", calls)
	}
}

func TestVerifyRejectsMismatchedHostnameOrAction(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"hostname":"attacker.example","action":"register"}`,
		`{"success":true,"hostname":"example.com","action":"login"}`,
	} {
		client := clientForTest(t, "production-secret", "example.com", false, func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, body), nil
		})
		if err := client.Verify(context.Background(), "token", "", "register"); !errors.Is(err, ErrInvalid) {
			t.Errorf("Verify() = %v, want ErrInvalid for %s", err, body)
		}
	}
}

func TestVerifyMapsProviderResponses(t *testing.T) {
	tests := []struct {
		name string
		resp *http.Response
		err  error
		want error
	}{
		{"provider rejection", response(200, `{"success":false,"error-codes":["invalid-input-response"]}`), nil, ErrInvalid},
		{"provider internal failure", response(200, `{"success":false,"error-codes":["internal-error"]}`), nil, ErrUnavailable},
		{"HTTP failure", response(503, `unavailable`), nil, ErrUnavailable},
		{"malformed JSON", response(200, `{`), nil, ErrUnavailable},
		{"oversized JSON", response(200, strings.Repeat("x", maxResponse+1)), nil, ErrUnavailable},
		{"transport failure", nil, errors.New("offline"), ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := clientForTest(t, "production-secret", "example.com", false, func(*http.Request) (*http.Response, error) { return tt.resp, tt.err })
			if err := client.Verify(context.Background(), "token", "", "register"); !errors.Is(err, tt.want) {
				t.Fatalf("Verify() error = %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}
}

func TestLocalModeRequiresDummyTokenAndTestingMetadata(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       error
	}{
		{"official testing response with absent hostname and action", `{"success":true,"hostname":"example.com","metadata":{"result_with_testing_key":true}}`, nil},
		{"missing testing metadata", `{"success":true,"metadata":{"result_with_testing_key":false}}`, ErrInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := clientForTest(t, "1x0000000000000000000000000000000AA", "", true, func(*http.Request) (*http.Response, error) { return response(200, tt.body), nil })
			if tt.want == nil {
				if err := client.Verify(context.Background(), dummyToken, "", "register"); err != nil {
					t.Fatalf("Verify() error = %v", err)
				}
				return
			}
			if err := client.Verify(context.Background(), dummyToken, "", "register"); !errors.Is(err, tt.want) {
				t.Fatalf("Verify() error = %v, want %v", err, tt.want)
			}
		})
	}
	client := clientForTest(t, "1x0000000000000000000000000000000AA", "", true, func(*http.Request) (*http.Response, error) {
		t.Fatal("provider called for non-dummy local token")
		return nil, nil
	})
	if err := client.Verify(context.Background(), "arbitrary-token", "", "register"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() error = %v, want ErrInvalid", err)
	}
}

func TestVerifyDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	client := clientForTest(t, "production-secret", "example.com", false, func(*http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			return response(http.StatusOK, `{"success":true,"hostname":"example.com","action":"register"}`), nil
		}
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://attacker.example"}}, Body: io.NopCloser(strings.NewReader("redirect"))}, nil
	})
	if err := client.Verify(context.Background(), "token", "", "register"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Verify() error = %v, want ErrUnavailable", err)
	}
	if calls != 1 {
		t.Fatalf("provider transport called %d times, want one request and no redirected secret disclosure", calls)
	}
}
