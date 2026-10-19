package notificationhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
)

func TestDeleteRequiresStrictJSONIdentity(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{name: "missing JSON body", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing identity", body: `{}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "malformed JSON", body: `{"id":`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"id":"10000000-0000-4000-8000-000000000001","version":1,"extra":true}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			New(nil).Register(mux, func(next http.Handler) http.Handler { return next })
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/monitors/10000000-0000-4000-8000-000000000001/webhook", strings.NewReader(tc.body))
			req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
				AccountID: "10000000-0000-4000-8000-000000000002",
				SessionID: "transport-test",
				Role:      "user",
			}))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, req)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("DELETE status = %d, want %d: %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestSubjectRejectsUUIDWithExtraHyphens(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Register(mux, func(next http.Handler) http.Handler { return next })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitors/00000000-0--0-0000-0000-000000000000/webhook", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		AccountID: "10000000-0000-4000-8000-000000000001",
		SessionID: "transport-test",
		Role:      "user",
	}))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET malformed monitor id status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
}
