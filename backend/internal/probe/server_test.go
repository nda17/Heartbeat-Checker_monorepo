package probe

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testProbeSecret = "01234567890123456789012345678901"

func TestHandlerRequiresStrongSharedSecret(t *testing.T) {
	for _, secret := range []string{"", "short", strings.Repeat("x", 31), strings.Repeat("x", 32) + "\n", " " + strings.Repeat("x", 32)} {
		if _, err := Handler(secret); err == nil {
			t.Errorf("Handler(%q) succeeded, want invalid secret", secret)
		}
	}
	if _, err := Handler(testProbeSecret); err != nil {
		t.Fatalf("Handler(valid secret): %v", err)
	}
}

func TestHandlerHealthIsPublicAndCheckRequiresBearerSecret(t *testing.T) {
	handler, err := Handler(testProbeSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/v1/check", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("unauthenticated check = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated check Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
}

func TestHandlerStrictlyBoundsAndDecodesCheckJSON(t *testing.T) {
	handler, err := Handler(testProbeSecret)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"url":"http://127.0.0.1/","unexpected":true}`},
		{name: "trailing JSON", body: `{"runId":"run","attemptToken":"attempt","configVersion":1,"url":"http://127.0.0.1/","method":"GET","expectedStatus":200,"bodyContains":""} {}`},
		{name: "malformed JSON", body: `{"runId":`},
		{name: "oversized body", body: `{"padding":"` + strings.Repeat("x", 9<<10) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/internal/v1/check", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+testProbeSecret)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"error":"invalid_request"`) {
				t.Fatalf("invalid request = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHandlerMapsUnsupportedTargetToSafeClientError(t *testing.T) {
	handler, err := Handler(testProbeSecret)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"runId": "run", "attemptToken": "attempt", "configVersion": 1,
		"url": "http://127.0.0.1/admin", "method": "GET", "expectedStatus": 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/check", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testProbeSecret)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"error":"unsupported_target"`) {
		t.Fatalf("unsupported target response = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsupported target Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
	if strings.Contains(recorder.Body.String(), "127.0.0.1") {
		t.Fatalf("error response leaked target URL: %s", recorder.Body.String())
	}
}
