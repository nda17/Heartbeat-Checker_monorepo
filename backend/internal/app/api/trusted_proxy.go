package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// trustedClientIP accepts the dedicated edge header only from the backend
// reverse proxy. Public health endpoints remain available to direct probes.
func trustedClientIP(next http.Handler, appEnv, rawCIDRs string) http.Handler {
	if appEnv != "production" {
		return next
	}
	var trusted []netip.Prefix
	for _, raw := range strings.Split(rawCIDRs, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err == nil {
			trusted = append(trusted, prefix)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		peer, parseErr := netip.ParseAddr(host)
		allowed := false
		if err == nil && parseErr == nil {
			peer = peer.Unmap()
			for _, prefix := range trusted {
				if prefix.Contains(peer) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		values := r.Header.Values("X-Heartbeat-Client-IP")
		if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] || strings.ContainsAny(values[0], ",%[]") {
			http.Error(w, "invalid client IP", http.StatusBadRequest)
			return
		}
		ip, err := netip.ParseAddr(values[0])
		if err != nil || ip.Zone() != "" || !ip.IsValid() || ip.IsUnspecified() {
			http.Error(w, "invalid client IP", http.StatusBadRequest)
			return
		}
		clone := r.Clone(r.Context())
		clone.RemoteAddr = net.JoinHostPort(ip.Unmap().String(), "0")
		next.ServeHTTP(w, clone)
	})
}
