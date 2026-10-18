package probe

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

// Handler exposes only the isolated check and webhook interfaces and process health.
func Handler(secret string) (http.Handler, error) {
	if len(secret) < 32 || strings.TrimSpace(secret) != secret || strings.ContainsAny(secret, "\r\n") {
		return nil, errors.New("PROBE_SHARED_SECRET must contain at least 32 random bytes")
	}
	executor := NewExecutor()
	want := sha256.Sum256([]byte("Bearer " + secret))
	mux := http.NewServeMux()
	for _, path := range []string{"GET /healthz", "GET /readyz"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	mux.HandleFunc("POST /internal/v1/check", func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var request contracts.Request
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		result, err := executor.Check(r.Context(), request)
		if err != nil {
			if errors.Is(err, ErrUnsupportedTarget) {
				writeError(w, http.StatusBadRequest, "unsupported_target")
				return
			}
			writeError(w, http.StatusServiceUnavailable, "probe_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("POST /internal/v1/webhook", func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var request contracts.WebhookRequest
		r.Body = http.MaxBytesReader(w, r.Body, 24<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		result, err := executor.DeliverWebhook(r.Context(), request)
		if err != nil {
			if errors.Is(err, ErrUnsupportedTarget) {
				writeError(w, http.StatusBadRequest, "invalid_request")
				return
			}
			writeError(w, http.StatusServiceUnavailable, "probe_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	})
	return mux, nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func Run(ctx context.Context, addr, secret string, logger *slog.Logger) error {
	handler, err := Handler(secret)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	defer server.Close()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen probe: %w", err)
	}
	logger.Info("probe listening", "address", listener.Addr().String())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("probe HTTP server failed")
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return errors.New("probe shutdown failed")
		}
		return nil
	}
}
