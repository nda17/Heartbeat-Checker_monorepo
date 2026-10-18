package probe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

func TestDeliverWebhookUsesPinnedHTTPSAndReturnsWithoutReadingResponseBody(t *testing.T) {
	rootPEM, certificate := makeWebhookTestCertificate(t)
	caFile := filepath.Join(t.TempDir(), "webhook-test-root.pem")
	if err := os.WriteFile(caFile, rootPEM, 0600); err != nil {
		t.Fatal(err)
	}
	oldCertFile, hadCertFile := os.LookupEnv("SSL_CERT_FILE")
	if err := os.Setenv("SSL_CERT_FILE", caFile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadCertFile {
			_ = os.Setenv("SSL_CERT_FILE", oldCertFile)
		} else {
			_ = os.Unsetenv("SSL_CERT_FILE")
		}
	})

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan *http.Request, 1)
	releaseBody := make(chan struct{})
	var redirectTargetCalls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/retry" {
			w.Header().Set("Retry-After", "17")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.URL.Path == "/target" {
			redirectTargetCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		received <- r
		w.WriteHeader(http.StatusAccepted)
		w.(http.Flusher).Flush()
		<-releaseBody
		_, _ = w.Write([]byte("response body deliberately held open"))
	})}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		close(releaseBody)
		_ = server.Close()
		<-serveDone
	})

	e := testExecutor(testResolver{
		"hooks.example.test": {netip.MustParseAddr("93.184.216.34")},
		"wrong.example.test": {netip.MustParseAddr("93.184.216.34")},
	}, func(ctx context.Context, network, _ string) (net.Conn, error) {
		if network != "tcp" {
			t.Errorf("dial network = %q, want tcp", network)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	})
	input := validWebhookRequest()
	resultChannel := make(chan struct {
		result contracts.WebhookResult
		err    error
	}, 1)
	go func() {
		result, err := e.DeliverWebhook(context.Background(), input)
		resultChannel <- struct {
			result contracts.WebhookResult
			err    error
		}{result, err}
	}()
	select {
	case got := <-resultChannel:
		if got.err != nil || got.result.HTTPStatus != http.StatusAccepted || got.result.ErrorCode != "" {
			t.Fatalf("DeliverWebhook() = (%+v, %v), want accepted response", got.result, got.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("DeliverWebhook waited for the webhook response body after receiving response headers")
	}
	select {
	case request := <-received:
		if request.Host != "hooks.example.test" || request.URL.RequestURI() != "/events" {
			t.Fatalf("request host/URI = %q %q", request.Host, request.URL.RequestURI())
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || string(body) != string(input.Body) {
			t.Fatalf("request body = %q, error %v; want exact bytes %q", body, err, input.Body)
		}
		if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("X-Heartbeat-Event-Id") != input.EventID || request.Header.Get("X-Heartbeat-Timestamp") != input.Timestamp || request.Header.Get("X-Heartbeat-Signature") != input.Signature {
			t.Fatalf("signed webhook headers missing or altered: %+v", request.Header)
		}
		for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if request.Header.Get(name) != "" {
				t.Errorf("webhook forwarded forbidden header %s", name)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("HTTPS receiver did not receive webhook")
	}
	redirect := input
	redirect.URL = "https://hooks.example.test/redirect"
	redirectResult, err := e.DeliverWebhook(context.Background(), redirect)
	if err != nil || redirectResult.HTTPStatus != http.StatusTemporaryRedirect || redirectResult.ErrorCode != "http_status" {
		t.Fatalf("redirect result = (%+v, %v), want the original 307 response", redirectResult, err)
	}
	if got := redirectTargetCalls.Load(); got != 0 {
		t.Fatalf("redirect target called %d times, want no follow", got)
	}
	retry := input
	retry.URL = "https://hooks.example.test/retry"
	retryResult, err := e.DeliverWebhook(context.Background(), retry)
	if err != nil || retryResult.HTTPStatus != http.StatusTooManyRequests || retryResult.ErrorCode != "http_status" || retryResult.RetryAfterSeconds != 17 {
		t.Fatalf("retry response = (%+v, %v), want HTTP 429 with Retry-After 17", retryResult, err)
	}
	tlsFailure := input
	tlsFailure.URL = "https://wrong.example.test/events"
	failureResult, err := e.DeliverWebhook(context.Background(), tlsFailure)
	if err != nil || failureResult.HTTPStatus != 0 || failureResult.ErrorCode != "request_failed" {
		t.Fatalf("TLS hostname mismatch result = (%+v, %v), want bounded request_failed", failureResult, err)
	}
}

func makeWebhookTestCertificate(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "hooks.example.test"},
		DNSNames: []string{"hooks.example.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}))
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), certificate
}
