package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthzDoesNotPingAndRejectsPost(t *testing.T) {
	calls := 0
	handler := newHandler(func(context.Context) error {
		calls++
		return errors.New("database unavailable")
	}, time.Second)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("GET /healthz = %d %q", response.Code, response.Body.String())
	}
	if calls != 0 {
		t.Fatalf("health check called ping %d times", calls)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if calls != 0 {
		t.Fatalf("POST /healthz called ping %d times", calls)
	}
}

func TestReadyzSuccessAndFailureAreSanitized(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		handler := newHandler(func(context.Context) error { return nil }, time.Second)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusOK || response.Body.String() != "ready\n" {
			t.Fatalf("GET /readyz = %d %q", response.Code, response.Body.String())
		}
	})

	t.Run("failure hides database error", func(t *testing.T) {
		const secret = "postgres://user:private-password@db/internal"
		handler := newHandler(func(context.Context) error { return errors.New(secret) }, time.Second)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "not ready") {
			t.Fatalf("GET /readyz = %d %q", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "private-password") {
			t.Fatalf("response leaks DB error: %q", response.Body.String())
		}
	})
}

func TestReadyzBoundsPingContextByConfiguredTimeout(t *testing.T) {
	var remaining time.Duration
	handler := newHandler(func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("ping context has no deadline")
		}
		remaining = time.Until(deadline)
		return nil
	}, 300*time.Millisecond)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /readyz status = %d", response.Code)
	}
	if remaining <= 0 || remaining > 300*time.Millisecond {
		t.Fatalf("ping deadline remaining = %v, want positive and at most 300ms", remaining)
	}
}

func TestReadyzPropagatesRequestCancellationToPing(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	handler := newHandler(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}, time.Second)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(requestCtx)
	finished := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ping did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("ping context was not canceled with request")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after request cancellation")
	}
}

func TestReadyzRejectsPost(t *testing.T) {
	calls := 0
	handler := newHandler(func(context.Context) error {
		calls++
		return nil
	}, time.Second)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/readyz", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /readyz status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if calls != 0 {
		t.Fatalf("POST /readyz called ping %d times", calls)
	}
}
