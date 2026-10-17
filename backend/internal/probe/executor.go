package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

var (
	ErrUnsupportedTarget = errors.New("unsupported_target")
	ErrSaturated         = errors.New("probe_saturated")
)

const maxBodyBytes = 1 << 20

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Executor has no access to application credentials or persistence.
type Executor struct {
	resolver resolver
	dial     func(context.Context, string, string) (net.Conn, error)
	global   chan struct{}
	mu       sync.Mutex
	active   map[string]bool
}

func NewExecutor() *Executor {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &Executor{resolver: net.DefaultResolver, dial: dialer.DialContext, global: make(chan struct{}, 4), active: make(map[string]bool)}
}

func validateRequest(req contracts.Request) error {
	if req.RunID == "" || len(req.RunID) > 128 || req.AttemptToken == "" || len(req.AttemptToken) > 256 || req.ConfigVersion < 1 || req.ExpectedStatus < 100 || req.ExpectedStatus > 599 || len(req.BodyContains) > 4096 || (req.Method != http.MethodGet && req.Method != http.MethodHead) || (req.Method == http.MethodHead && req.BodyContains != "") {
		return ErrUnsupportedTarget
	}
	_, err := parseTarget(req.URL)
	return err
}

func parseTarget(raw string) (*url.URL, error) {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\\\r\n\t") {
		return nil, ErrUnsupportedTarget
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.Hostname() == "" || strings.Contains(u.Hostname(), "%") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, ErrUnsupportedTarget
	}
	port := u.Port()
	if (port != "" && u.Scheme == "http" && port != "80") || (port != "" && u.Scheme == "https" && port != "443") {
		return nil, ErrUnsupportedTarget
	}
	return u, nil
}

// Explicit exclusions supplement IsGlobalUnicast, which includes private and
// documentation addresses. IPv6 is restricted to current global allocations.
var excludedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"), netip.MustParsePrefix("3fff::/20"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip)) {
		return false
	}
	for _, prefix := range excludedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func (e *Executor) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicIP(ip) {
			return nil, ErrUnsupportedTarget
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := e.resolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("dns_no_addresses")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, ErrUnsupportedTarget
		}
	}
	return ips, nil
}

func (e *Executor) acquire(keys ...string) (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, key := range keys {
		if e.active[key] {
			return nil, ErrSaturated
		}
	}
	for _, key := range keys {
		e.active[key] = true
	}
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, key := range keys {
			delete(e.active, key)
		}
	}, nil
}

func (e *Executor) Check(ctx context.Context, req contracts.Request) (contracts.Result, error) {
	result := contracts.Result{RunID: req.RunID, AttemptToken: req.AttemptToken, ConfigVersion: req.ConfigVersion, Outcome: "UNKNOWN", CheckedAt: time.Now().UTC()}
	if err := validateRequest(req); err != nil {
		return result, err
	}
	select {
	case e.global <- struct{}{}:
		defer func() { <-e.global }()
	default:
		return result, ErrSaturated
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	u, _ := parseTarget(req.URL)
	for redirects := 0; ; redirects++ {
		response, body, err := e.request(ctx, u, req.Method)
		result.LatencyMS = time.Since(started).Milliseconds()
		if err != nil {
			if errors.Is(err, ErrUnsupportedTarget) || errors.Is(err, ErrSaturated) {
				return result, err
			}
			if resourceFailure(err) {
				result.ErrorCode = "probe_unavailable"
				return result, nil
			}
			if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.ErrorCode = "check_cancelled"
				return result, nil
			}
			result.Outcome, result.ErrorCode = "DOWN", "request_failed"
			return result, nil
		}
		result.HTTPStatus = response.StatusCode
		if response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308 {
			if redirects >= 3 {
				result.Outcome, result.ErrorCode = "DOWN", "redirect_limit"
				return result, nil
			}
			location, err := response.Location()
			if err != nil {
				result.Outcome, result.ErrorCode = "DOWN", "invalid_redirect"
				return result, nil
			}
			u, err = parseTarget(location.String())
			if err != nil {
				return result, ErrUnsupportedTarget
			}
			continue
		}
		if len(body) > maxBodyBytes {
			result.Outcome, result.ErrorCode = "DOWN", "response_too_large"
			return result, nil
		}
		if response.StatusCode != req.ExpectedStatus {
			result.Outcome, result.ErrorCode = "DOWN", "unexpected_status"
			return result, nil
		}
		if req.BodyContains != "" && !bytes.Contains(body, []byte(req.BodyContains)) {
			result.Outcome, result.ErrorCode = "DOWN", "body_mismatch"
			return result, nil
		}
		result.Outcome = "UP"
		return result, nil
	}
}

func (e *Executor) request(ctx context.Context, u *url.URL, method string) (*http.Response, []byte, error) {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	releaseHost, err := e.acquire("host:" + host)
	if err != nil {
		return nil, nil, err
	}
	defer releaseHost()
	ips, err := e.resolve(ctx, host)
	if err != nil {
		return nil, nil, err
	}
	// Resolve once and validate every answer before trying a bounded set. Only
	// connection establishment failures permit another address: an HTTP response
	// or a body/header failure must keep the original check outcome.
	seen := make(map[netip.Addr]bool)
	var lastErr error
	saturated := false
	for _, ip := range ips {
		ip = ip.Unmap()
		if seen[ip] {
			continue
		}
		if len(seen) == 4 {
			break
		}
		seen[ip] = true
		response, body, err := e.requestIP(ctx, u, method, ip)
		if errors.Is(err, ErrSaturated) {
			saturated = true
			continue
		}
		var establishmentErr *establishmentError
		if !errors.As(err, &establishmentErr) || ctx.Err() != nil || resourceFailure(err) {
			return response, body, err
		}
		lastErr = err
	}
	if saturated {
		return nil, nil, ErrSaturated
	}
	return nil, nil, lastErr
}

type establishmentError struct{ err error }

func (e *establishmentError) Error() string { return "connection establishment failed" }
func (e *establishmentError) Unwrap() error { return e.err }

func resourceFailure(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.ENOMEM) || errors.Is(err, syscall.EADDRNOTAVAIL)
}

func (e *Executor) requestIP(ctx context.Context, u *url.URL, method string, ip netip.Addr) (*http.Response, []byte, error) {
	releaseIP, err := e.acquire("ip:" + ip.String())
	if err != nil {
		return nil, nil, err
	}
	defer releaseIP()
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := e.dial(dialCtx, network, net.JoinHostPort(ip.String(), port))
		if err != nil {
			return nil, &establishmentError{err}
		}
		if u.Scheme != "https" {
			return conn, nil
		}
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			_ = conn.Close()
			return nil, &establishmentError{err}
		}
		return tlsConn, nil
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dial,
		DialTLSContext:         dial,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    3 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DisableKeepAlives:      true,
		DisableCompression:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, nil, ErrUnsupportedTarget
	}
	request.Header.Set("User-Agent", "Heartbeat-Checker/0.2")
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	return response, body, err
}
