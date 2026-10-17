package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

type testResolver map[string][]netip.Addr

func (r testResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	ips, ok := r[host]
	if !ok {
		return nil, errors.New("unexpected DNS lookup")
	}
	return append([]netip.Addr(nil), ips...), nil
}

func testExecutor(resolver resolver, dial func(context.Context, string, string) (net.Conn, error)) *Executor {
	return &Executor{resolver: resolver, dial: dial, global: make(chan struct{}, 4), active: make(map[string]bool)}
}

func testRequest(rawURL string) contracts.Request {
	return contracts.Request{RunID: "run-1", AttemptToken: "attempt-1", ConfigVersion: 1, URL: rawURL, Method: http.MethodGet, ExpectedStatus: http.StatusOK}
}

func responseConn(t *testing.T, response string, captured chan<- *http.Request) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		request, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		if captured != nil {
			captured <- request
		}
		_, _ = io.WriteString(server, response)
	}()
	return client
}

func TestPublicIPRejectsNonPublicAddressRanges(t *testing.T) {
	for _, raw := range []string{
		"0.1.2.3", "10.1.2.3", "100.64.1.2", "127.0.0.1", "169.254.1.2", "172.20.1.2",
		"192.0.0.8", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1", "192.168.1.1",
		"198.18.0.1", "198.51.100.2", "203.0.113.2", "224.0.0.1", "240.0.0.1",
		"::", "::1", "fe80::1", "fc00::1", "ff02::1", "2001:db8::1", "2002:c000:0201::1",
		"64:ff9b::a00:1", "::ffff:192.168.1.1", "3fff::1", "2001::1",
	} {
		t.Run(raw, func(t *testing.T) {
			ip := netip.MustParseAddr(raw)
			if publicIP(ip) {
				t.Fatalf("publicIP(%s) = true, want false", raw)
			}
		})
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if ip := netip.MustParseAddr(raw); !publicIP(ip) {
			t.Errorf("publicIP(%s) = false, want true", raw)
		}
	}
}

func TestCheckRejectsDNSSetContainingAnyNonPublicAddress(t *testing.T) {
	dialCalls := 0
	e := testExecutor(testResolver{"mixed.example": {
		netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("192.168.1.10"),
	}}, func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("dial must not run for mixed DNS")
	})
	req := testRequest("http://mixed.example/health")
	if _, err := e.Check(context.Background(), req); !errors.Is(err, ErrUnsupportedTarget) {
		t.Fatalf("Check error = %v, want ErrUnsupportedTarget", err)
	}
	if dialCalls != 0 {
		t.Fatalf("dial called %d times for mixed public/private DNS", dialCalls)
	}
}

func TestCheckPinsResolvedIPAndPreservesHTTPHost(t *testing.T) {
	captured := make(chan *http.Request, 1)
	var dialAddress string
	e := testExecutor(testResolver{"status.example": {netip.MustParseAddr("93.184.216.34")}}, func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Errorf("dial network = %q, want tcp", network)
		}
		dialAddress = address
		return responseConn(t, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok", captured), nil
	})
	req := testRequest("http://status.example/health?q=ok")
	result, err := e.Check(context.Background(), req)
	if err != nil || result.Outcome != "UP" {
		t.Fatalf("Check = (%+v, %v), want UP", result, err)
	}
	if dialAddress != "93.184.216.34:80" {
		t.Fatalf("dial address = %q, want pinned DNS IP", dialAddress)
	}
	got := <-captured
	if got.Host != "status.example" || got.URL.RequestURI() != "/health?q=ok" {
		t.Fatalf("request host/URI = %q %q", got.Host, got.URL.RequestURI())
	}
}

func TestCheckUsesOriginalHostnameForTLSNameAndPinnedIPForDial(t *testing.T) {
	serverName := make(chan string, 1)
	e := testExecutor(testResolver{"tls.example": {netip.MustParseAddr("93.184.216.34")}}, func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "93.184.216.34:443" {
			t.Errorf("dial = %s %s, want tcp to pinned IP", network, address)
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			tlsServer := tls.Server(server, &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				serverName <- hello.ServerName
				return nil, errors.New("stop after observing ClientHello")
			}})
			_ = tlsServer.Handshake()
		}()
		return client, nil
	})
	_, _ = e.Check(context.Background(), testRequest("https://tls.example/path"))
	select {
	case got := <-serverName:
		if got != "tls.example" {
			t.Fatalf("TLS server name = %q, want original hostname", got)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS client did not send a ClientHello")
	}
}

func TestCheckRedirectResolvesAndRejectsPrivateDestination(t *testing.T) {
	var calls int
	e := testExecutor(testResolver{
		"origin.example":  {netip.MustParseAddr("93.184.216.34")},
		"private.example": {netip.MustParseAddr("127.0.0.1")},
	}, func(_ context.Context, _, address string) (net.Conn, error) {
		calls++
		if address != "93.184.216.34:80" {
			t.Errorf("first dial = %q, want public origin address", address)
		}
		return responseConn(t, "HTTP/1.1 302 Found\r\nLocation: http://private.example/admin\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", nil), nil
	})
	_, err := e.Check(context.Background(), testRequest("http://origin.example/start"))
	if !errors.Is(err, ErrUnsupportedTarget) {
		t.Fatalf("Check error = %v, want private redirect rejection", err)
	}
	if calls != 1 {
		t.Fatalf("dial calls = %d, want only the public origin", calls)
	}
}

func TestCheckRedirectDoesNotForwardSensitiveHeadersAndStopsAtThree(t *testing.T) {
	var mu sync.Mutex
	dialed := make([]string, 0, 5)
	captured := make(chan *http.Request, 4)
	resolver := testResolver{}
	for i := 0; i < 5; i++ {
		host := fmt.Sprintf("redirect-%d.example", i)
		resolver[host] = []netip.Addr{netip.MustParseAddr(fmt.Sprintf("93.184.216.%d", 34+i))}
	}
	e := testExecutor(resolver, func(_ context.Context, _, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		index := len(dialed) - 1
		mu.Unlock()
		if index < 3 {
			nextHost := fmt.Sprintf("redirect-%d.example", index+1)
			return responseConn(t, fmt.Sprintf("HTTP/1.1 302 Found\r\nLocation: http://%s/next\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", nextHost), captured), nil
		}
		return responseConn(t, "HTTP/1.1 302 Found\r\nLocation: http://redirect-4.example/end\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", captured), nil
	})
	req := testRequest("http://redirect-0.example/start")
	result, err := e.Check(context.Background(), req)
	if err != nil || result.Outcome != "DOWN" || result.ErrorCode != "redirect_limit" {
		t.Fatalf("Check = (%+v, %v), want redirect-limit DOWN", result, err)
	}
	if len(dialed) != 4 {
		t.Fatalf("redirect dials = %d, want origin plus three redirects", len(dialed))
	}
	for i := 0; i < 4; i++ {
		got := <-captured
		if got.Header.Get("Authorization") != "" || got.Header.Get("Cookie") != "" || got.Header.Get("Referer") != "" {
			t.Fatalf("redirect request %d forwarded a sensitive header", i)
		}
		if got.Host != fmt.Sprintf("redirect-%d.example", i) {
			t.Fatalf("redirect request %d Host = %q", i, got.Host)
		}
	}
}

func TestCheckFallsBackOnlyBeforeReceivingHTTPResponse(t *testing.T) {
	t.Run("connection establishment failure uses next validated IP", func(t *testing.T) {
		var dialed []string
		captured := make(chan *http.Request, 1)
		e := testExecutor(testResolver{"fallback.example": {
			netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("93.184.216.35"),
		}}, func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			if address == "93.184.216.34:80" {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: nil, Err: errors.New("connection refused")}
			}
			return responseConn(t, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok", captured), nil
		})
		result, err := e.Check(context.Background(), testRequest("http://fallback.example/health"))
		if err != nil || result.Outcome != "UP" {
			t.Fatalf("Check = (%+v, %v), want UP from fallback IP", result, err)
		}
		if strings.Join(dialed, ",") != "93.184.216.34:80,93.184.216.35:80" {
			t.Fatalf("dial order = %v", dialed)
		}
		if request := <-captured; request.Host != "fallback.example" {
			t.Fatalf("fallback request Host = %q, want original hostname", request.Host)
		}
	})

	t.Run("HTTP status response stops fallback", func(t *testing.T) {
		calls := 0
		e := testExecutor(testResolver{"status.example": {
			netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("93.184.216.35"),
		}}, func(context.Context, string, string) (net.Conn, error) {
			calls++
			return responseConn(t, "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", nil), nil
		})
		result, err := e.Check(context.Background(), testRequest("http://status.example/health"))
		if err != nil || result.Outcome != "DOWN" || result.ErrorCode != "unexpected_status" || result.HTTPStatus != http.StatusServiceUnavailable {
			t.Fatalf("Check = (%+v, %v), want HTTP 503 DOWN", result, err)
		}
		if calls != 1 {
			t.Fatalf("dial calls = %d, want no fallback after HTTP response", calls)
		}
	})

	t.Run("body read error after headers stops fallback", func(t *testing.T) {
		calls := 0
		e := testExecutor(testResolver{"truncated.example": {
			netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("93.184.216.35"),
		}}, func(context.Context, string, string) (net.Conn, error) {
			calls++
			return responseConn(t, "HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nshort", nil), nil
		})
		result, err := e.Check(context.Background(), testRequest("http://truncated.example/health"))
		if err != nil || result.Outcome != "DOWN" || result.ErrorCode != "request_failed" {
			t.Fatalf("Check = (%+v, %v), want truncated response DOWN", result, err)
		}
		if calls != 1 {
			t.Fatalf("dial calls = %d, want no fallback after response body error", calls)
		}
	})
}

func TestCheckLimitsFallbackToFourUniqueValidatedIPs(t *testing.T) {
	addresses := []string{"93.184.216.34", "93.184.216.34", "93.184.216.35", "93.184.216.36", "93.184.216.37", "93.184.216.38"}
	ips := make([]netip.Addr, 0, len(addresses))
	for _, raw := range addresses {
		ips = append(ips, netip.MustParseAddr(raw))
	}
	var dialed []string
	e := testExecutor(testResolver{"many.example": ips}, func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, errors.New("connection refused")
	})
	result, err := e.Check(context.Background(), testRequest("http://many.example/"))
	if err != nil || result.Outcome != "DOWN" || result.ErrorCode != "request_failed" {
		t.Fatalf("Check = (%+v, %v), want request_failed DOWN", result, err)
	}
	want := []string{"93.184.216.34:80", "93.184.216.35:80", "93.184.216.36:80", "93.184.216.37:80"}
	if strings.Join(dialed, ",") != strings.Join(want, ",") {
		t.Fatalf("fallback addresses = %v, want first four unique addresses %v", dialed, want)
	}
}

func TestCheckReturnsUnknownWhenPerIPCapacityIsBusy(t *testing.T) {
	sharedIP := netip.MustParseAddr("93.184.216.34")
	e := testExecutor(testResolver{"first.example": {sharedIP}, "second.example": {sharedIP}}, func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	entered := make(chan struct{})
	// Replace the fake dial with a barrier so the first check owns the IP slot.
	e.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = e.Check(ctx, testRequest("http://first.example/"))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("first request did not acquire its IP slot")
	}
	result, err := e.Check(context.Background(), testRequest("http://second.example/"))
	if !errors.Is(err, ErrSaturated) || result.Outcome != "UNKNOWN" {
		t.Fatalf("saturated result = (%+v, %v), want UNKNOWN/ErrSaturated", result, err)
	}
	cancel()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first request did not release its IP slot")
	}
}

func TestCheckEnforcesBodyMatchingLimitAndHEADContract(t *testing.T) {
	t.Run("body match", func(t *testing.T) {
		e := testExecutor(testResolver{"body.example": {netip.MustParseAddr("93.184.216.34")}}, func(context.Context, string, string) (net.Conn, error) {
			return responseConn(t, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello", nil), nil
		})
		req := testRequest("http://body.example/")
		req.BodyContains = "ell"
		result, err := e.Check(context.Background(), req)
		if err != nil || result.Outcome != "UP" {
			t.Fatalf("Check = (%+v, %v), want UP", result, err)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		e := testExecutor(testResolver{"body.example": {netip.MustParseAddr("93.184.216.34")}}, func(context.Context, string, string) (net.Conn, error) {
			return responseConn(t, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello", nil), nil
		})
		req := testRequest("http://body.example/")
		req.BodyContains = "nope"
		result, err := e.Check(context.Background(), req)
		if err != nil || result.Outcome != "DOWN" || result.ErrorCode != "body_mismatch" {
			t.Fatalf("Check = (%+v, %v), want body-mismatch DOWN", result, err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		payload := strings.Repeat("a", maxBodyBytes+1)
		e := testExecutor(testResolver{"body.example": {netip.MustParseAddr("93.184.216.34")}}, func(context.Context, string, string) (net.Conn, error) {
			return responseConn(t, fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(payload), payload), nil), nil
		})
		result, err := e.Check(context.Background(), testRequest("http://body.example/"))
		if err != nil || result.Outcome != "DOWN" || result.ErrorCode != "response_too_large" {
			t.Fatalf("Check = (%+v, %v), want response-too-large DOWN", result, err)
		}
	})
	t.Run("HEAD rejects content matching", func(t *testing.T) {
		req := testRequest("http://body.example/")
		req.Method, req.BodyContains = http.MethodHead, "content"
		if err := validateRequest(req); !errors.Is(err, ErrUnsupportedTarget) {
			t.Fatalf("validateRequest error = %v, want unsupported target", err)
		}
	})
}

func TestCheckReturnsCancelledOutcomeWhenCallerCancels(t *testing.T) {
	entered := make(chan struct{})
	e := testExecutor(testResolver{"cancel.example": {netip.MustParseAddr("93.184.216.34")}}, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan contracts.Result, 1)
	go func() {
		result, _ := e.Check(ctx, testRequest("http://cancel.example/"))
		done <- result
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("check did not reach dial")
	}
	cancel()
	select {
	case result := <-done:
		if result.Outcome != "UNKNOWN" || result.ErrorCode != "check_cancelled" {
			t.Fatalf("cancelled result = %+v, want UNKNOWN/check_cancelled", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled check did not return")
	}
}

func TestCheckHonorsCallerDeadline(t *testing.T) {
	entered := make(chan struct{})
	e := testExecutor(testResolver{"deadline.example": {netip.MustParseAddr("93.184.216.34")}}, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan contracts.Result, 1)
	go func() {
		result, _ := e.Check(ctx, testRequest("http://deadline.example/"))
		done <- result
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("check did not reach dial")
	}
	select {
	case result := <-done:
		if result.Outcome != "DOWN" || result.ErrorCode != "request_failed" {
			t.Fatalf("deadline result = %+v, want DOWN/request_failed", result)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not stop check promptly")
	}
}

func TestExecutorReleasesCapacityAfterConcurrentRequests(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	resolver := testResolver{}
	for i := 0; i < 5; i++ {
		resolver[fmt.Sprintf("capacity-%d.example", i)] = []netip.Addr{netip.MustParseAddr(fmt.Sprintf("93.184.216.%d", 34+i))}
	}
	e := testExecutor(resolver, func(ctx context.Context, _, _ string) (net.Conn, error) {
		entered <- struct{}{}
		<-release
		return responseConn(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", nil), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := testRequest(fmt.Sprintf("http://capacity-%d.example/", i))
			_, _ = e.Check(context.Background(), req)
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			wg.Wait()
			t.Fatal("four concurrent checks did not reach dial")
		}
	}
	if _, err := e.Check(context.Background(), testRequest("http://capacity-4.example/")); !errors.Is(err, ErrSaturated) {
		t.Fatalf("fifth concurrent check error = %v, want ErrSaturated", err)
	}
	close(release)
	wg.Wait()
	result, err := e.Check(context.Background(), testRequest("http://capacity-4.example/"))
	if err != nil || result.Outcome != "UP" {
		t.Fatalf("check after capacity release = (%+v, %v), want UP", result, err)
	}
}
