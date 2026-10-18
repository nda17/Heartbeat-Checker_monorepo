package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedClientIPAcceptsSingleCanonicalHeaderFromConfiguredProxy(t *testing.T) {
	for _, test := range []struct{ name, forwarded, want string }{
		{"IPv4", "198.51.100.27", "198.51.100.27:0"},
		{"IPv6", "2001:db8::27", "[2001:db8::27]:0"},
		{"mapped IPv4 canonicalizes", "::ffff:198.51.100.27", "198.51.100.27:0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got string
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr })
			h := trustedClientIP(next, "production", "172.30.0.0/24,fd42:30::/64")
			req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			req.RemoteAddr = net.JoinHostPort("172.30.0.5", "443")
			req.Header.Set("X-Heartbeat-Client-IP", test.forwarded)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK || got != test.want {
				t.Fatalf("status=%d RemoteAddr=%q, want %q", w.Code, got, test.want)
			}
		})
	}
}

func TestTrustedClientIPRejectsSpoofedPeerAndMissingOrMalformedHeader(t *testing.T) {
	tests := []struct {
		name, peer string
		headers    []string
		want       int
	}{
		{"untrusted peer cannot spoof", "198.51.100.8:1234", []string{"203.0.113.7"}, http.StatusForbidden},
		{"missing header", "172.30.0.5:443", nil, http.StatusBadRequest},
		{"duplicate header", "172.30.0.5:443", []string{"198.51.100.1", "198.51.100.2"}, http.StatusBadRequest},
		{"comma chain", "172.30.0.5:443", []string{"198.51.100.1, 198.51.100.2"}, http.StatusBadRequest},
		{"whitespace padded", "172.30.0.5:443", []string{" 198.51.100.1"}, http.StatusBadRequest},
		{"malformed address", "172.30.0.5:443", []string{"not-an-ip"}, http.StatusBadRequest},
		{"zone-scoped IPv6", "172.30.0.5:443", []string{"fe80::1%eth0"}, http.StatusBadRequest},
		{"unspecified address", "172.30.0.5:443", []string{"0.0.0.0"}, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("next handler ran for rejected request") })
			h := trustedClientIP(next, "production", "172.30.0.0/24")
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
			req.RemoteAddr = test.peer
			for _, value := range test.headers {
				req.Header.Add("X-Heartbeat-Client-IP", value)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != test.want {
				t.Fatalf("status=%d, want %d", w.Code, test.want)
			}
		})
	}
}

func TestTrustedClientIPHealthExemptionAndNonProductionFallback(t *testing.T) {
	for _, test := range []struct {
		name, env, path, peer string
		headers               []string
	}{
		{"production health direct", "production", "/healthz", "203.0.113.4:1234", nil},
		{"local API fallback", "local", "/api/v1/me", "203.0.113.4:1234", []string{"spoofed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got string
			h := trustedClientIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }), test.env, "172.30.0.0/24")
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.RemoteAddr = test.peer
			for _, value := range test.headers {
				req.Header.Add("X-Heartbeat-Client-IP", value)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK || got != test.peer {
				t.Fatalf("status=%d RemoteAddr=%q, want passthrough %q", w.Code, got, test.peer)
			}
		})
	}
}
