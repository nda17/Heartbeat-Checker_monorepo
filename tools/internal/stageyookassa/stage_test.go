package stageyookassa

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

func yookassaFixture(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	workdir := filepath.Join(dir, "backend")
	if err := os.Mkdir(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("DATABASE_URL=test\nBILLING_PROVIDER=stripe\nKEEP_EXISTING=one\n")
	if err := os.WriteFile(filepath.Join(workdir, ".env"), original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workdir)
	return workdir, original
}

type yookassaTransport struct {
	status        int
	body          string
	request       *http.Request
	authorization string
}

func (f *yookassaTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.request = request.Clone(request.Context())
	f.authorization = request.Header.Get("Authorization")
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	body := f.body
	if body == "" {
		body = `{"account_id":"123456","test":false,"status":"enabled"}`
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestStageTestModeScopesSandboxAndPreservesOtherEnvBytes(t *testing.T) {
	workdir, original := yookassaFixture(t)
	input := `{"shopId":"123456","secretKey":"test_synthetic_secret_123"}`
	if err := Stage(context.Background(), workdir, "test", strings.NewReader(input), nil); err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(filepath.Join(workdir, ".env.next"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"BILLING_PROVIDER=yookassa", "YOOKASSA_MODE=test", "BILLING_TEST_ACCOUNT_IDS=51ec7d19-80c4-4fb6-8edb-79dc7abe3120", "YOOKASSA_TEST_SHOP_ID=123456", "YOOKASSA_TEST_SECRET_KEY='test_synthetic_secret_123'", "DATABASE_URL=test", "KEEP_EXISTING=one"} {
		if !bytes.Contains(candidate, []byte(value)) {
			t.Fatalf("sandbox candidate missing %q: %s", value, candidate)
		}
	}
	if got, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("source billing env changed: %v", err)
	}
}

func TestStageReplacesOnlySelectedBillingKeysAndPreservesOtherBytes(t *testing.T) {
	workdir, _ := yookassaFixture(t)
	original := []byte("# billing owner scope\nDATABASE_URL='postgres://user:pass@db/app?sslmode=disable'\nYOOKASSA_MODE=production\nBILLING_PROVIDER=stripe\nKEEP_EXISTING=$$HOME\nYOOKASSA_PRODUCTION_SECRET_KEY='production_existing_secret'\nBILLING_TEST_ACCOUNT_IDS=old-owner\nYOOKASSA_TEST_SHOP_ID=old-shop\nYOOKASSA_TEST_SECRET_KEY='test_old_secret'\n")
	if err := os.WriteFile(filepath.Join(workdir, ".env"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Stage(context.Background(), workdir, "test", strings.NewReader(`{"shopId":"123456","secretKey":"test_synthetic_secret_123"}`), nil); err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(filepath.Join(workdir, ".env.next"))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("BILLING_PROVIDER=yookassa\nYOOKASSA_MODE=test\nBILLING_TEST_ACCOUNT_IDS=" + deploydata.OwnerID + "\nYOOKASSA_TEST_SHOP_ID=123456\nYOOKASSA_TEST_SECRET_KEY='test_synthetic_secret_123'\n# billing owner scope\nDATABASE_URL='postgres://user:pass@db/app?sslmode=disable'\nKEEP_EXISTING=$$HOME\nYOOKASSA_PRODUCTION_SECRET_KEY='production_existing_secret'\n")
	if !bytes.Equal(candidate, want) {
		t.Fatalf("candidate bytes changed outside selected billing keys\nwant: %q\ngot:  %q", want, candidate)
	}
}

func TestStageProductionVerifiesShopAndPreservesTestCredentials(t *testing.T) {
	workdir, original := yookassaFixture(t)
	production := []byte("YOOKASSA_MODE=test\nYOOKASSA_TEST_SHOP_ID=654321\nYOOKASSA_TEST_SECRET_KEY='test_existing_sandbox_secret'\n" + string(original))
	if err := os.WriteFile(filepath.Join(workdir, ".env"), production, 0600); err != nil {
		t.Fatal(err)
	}
	transport := &yookassaTransport{}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	if err := Stage(context.Background(), workdir, "production", strings.NewReader(`{"shopId":"123456","secretKey":"production_synthetic_secret_123"}`), client); err != nil {
		t.Fatal(err)
	}
	if transport.request == nil || transport.request.URL.String() != "https://api.yookassa.ru/v3/me" {
		t.Fatalf("wrong shop verification request: %#v", transport.request)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("123456:production_synthetic_secret_123"))
	if transport.authorization != wantAuth {
		t.Fatalf("shop credentials not scoped to verification request: %q", transport.authorization)
	}
	candidate, err := os.ReadFile(filepath.Join(workdir, ".env.next"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"YOOKASSA_TEST_SHOP_ID=654321", "YOOKASSA_TEST_SECRET_KEY='test_existing_sandbox_secret'", "YOOKASSA_PRODUCTION_SHOP_ID=123456", "YOOKASSA_PRODUCTION_SECRET_KEY='production_synthetic_secret_123'"} {
		if !bytes.Contains(candidate, []byte(line)) {
			t.Fatalf("production stage failed to preserve selected credentials: %s", candidate)
		}
	}
}

func TestStageNativeTestPreservesHostedProductionAndStagesOnlyOwnerSDKProfile(t *testing.T) {
	workdir, _ := yookassaFixture(t)
	original := []byte("BILLING_PROVIDER=yookassa\nYOOKASSA_MODE=production\nYOOKASSA_PRODUCTION_SHOP_ID=654321\nYOOKASSA_PRODUCTION_SECRET_KEY='production_existing_secret'\nKEEP_EXISTING=byte-exact\n")
	if err := os.WriteFile(filepath.Join(workdir, ".env"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Stage(context.Background(), workdir, "native-test", strings.NewReader(`{"shopId":"123456","secretKey":"test_synthetic_secret_123"}`), nil); err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(filepath.Join(workdir, ".env.next"))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("BILLING_TEST_ACCOUNT_IDS=" + deploydata.OwnerID + "\nYOOKASSA_TEST_SHOP_ID=123456\nYOOKASSA_TEST_SECRET_KEY='test_synthetic_secret_123'\nYOOKASSA_NATIVE_SDK_ENABLED=true\nYOOKASSA_NATIVE_SDK_MODE=test\nBILLING_PROVIDER=yookassa\nYOOKASSA_MODE=production\nYOOKASSA_PRODUCTION_SHOP_ID=654321\nYOOKASSA_PRODUCTION_SECRET_KEY='production_existing_secret'\nKEEP_EXISTING=byte-exact\n")
	if !bytes.Equal(candidate, want) {
		t.Fatalf("native SDK staging changed hosted or unrelated environment bytes\nwant: %q\ngot:  %q", want, candidate)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("stage mutated source env: %v", err)
	}
}

func TestStageRejectsMalformedOversizedLiveKeysAndInvalidProviderResponse(t *testing.T) {
	tests := []struct {
		name, mode, payload, response string
		status                        int
	}{
		{"unknown field", "test", `{"shopId":"123","secretKey":"test_synthetic_secret_123","extra":true}`, ``, 200},
		{"duplicate field", "test", `{"shopId":"123","shopId":"124","secretKey":"test_synthetic_secret_123"}`, ``, 200},
		{"oversized", "test", `{"shopId":"123","secretKey":"test_` + strings.Repeat("x", 2048) + `"}`, ``, 200},
		{"live key in test mode", "test", `{"shopId":"123","secretKey":"live_synthetic_secret_123"}`, ``, 200},
		{"test key in production", "production", `{"shopId":"123","secretKey":"test_synthetic_secret_123"}`, `{"account_id":"123","test":false,"status":"enabled"}`, 200},
		{"wrong shop response", "production", `{"shopId":"123","secretKey":"production_synthetic_secret_123"}`, `{"account_id":"999","test":false,"status":"enabled"}`, 200},
		{"sandbox account response", "production", `{"shopId":"123","secretKey":"production_synthetic_secret_123"}`, `{"account_id":"123","test":true,"status":"enabled"}`, 200},
		{"disabled account", "production", `{"shopId":"123","secretKey":"production_synthetic_secret_123"}`, `{"account_id":"123","test":false,"status":"disabled"}`, 200},
		{"non-success response", "production", `{"shopId":"123","secretKey":"production_synthetic_secret_123"}`, `{"account_id":"123","test":false,"status":"enabled"}`, http.StatusFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workdir, _ := yookassaFixture(t)
			transport := &yookassaTransport{status: tc.status, body: tc.response}
			client := &http.Client{Transport: transport}
			if err := Stage(context.Background(), workdir, tc.mode, strings.NewReader(tc.payload), client); err == nil {
				t.Fatal("invalid YooKassa stage accepted")
			}
			if _, err := os.Lstat(filepath.Join(workdir, ".env.next")); !os.IsNotExist(err) {
				t.Fatalf("invalid stage created candidate: %v", err)
			}
		})
	}
}

func TestStageRejectsInvalidModeAndNoncanonicalProjectValues(t *testing.T) {
	workdir, _ := yookassaFixture(t)
	if err := Stage(context.Background(), workdir, "unexpected", strings.NewReader(`{"shopId":"123","secretKey":"test_synthetic_secret_123"}`), nil); err == nil {
		t.Fatal("unknown billing mode accepted")
	}
	for _, raw := range []string{`{"shopId":"1.2","secretKey":"test_synthetic_secret_123"}`, `{"shopId":"123","secretKey":""}`, `{"shopId":123,"secretKey":"test_synthetic_secret_123"}`} {
		if err := Stage(context.Background(), workdir, "test", strings.NewReader(raw), nil); err == nil {
			t.Fatalf("noncanonical shop value accepted: %s", raw)
		}
	}
}

func TestVerificationRequestDoesNotFollowRedirect(t *testing.T) {
	workdir, _ := yookassaFixture(t)
	transport := &yookassaTransport{status: http.StatusFound}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	if err := Stage(context.Background(), workdir, "production", strings.NewReader(`{"shopId":"123","secretKey":"production_synthetic_secret_123"}`), client); err == nil {
		t.Fatal("redirecting provider response accepted")
	}
	if transport.request != nil {
		parsed, err := url.Parse(transport.request.URL.String())
		if err != nil || parsed.Host != "api.yookassa.ru" {
			t.Fatalf("credential verification target changed: %v %v", parsed, err)
		}
	}
}
