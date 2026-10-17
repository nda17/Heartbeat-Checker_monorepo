package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/postgres"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DatabaseTimeout)
	if err != nil {
		return err
	}
	defer pool.Close()
	captcha, err := auth.NewTurnstile(cfg.TurnstileSecret, cfg.TurnstileHost, cfg.TurnstileLocal)
	if err != nil {
		return fmt.Errorf("configure Turnstile: %w", err)
	}
	mailer, err := auth.NewLocalMailer(cfg.SMTPAddr, cfg.SMTPFrom)
	if err != nil {
		return fmt.Errorf("configure local SMTP: %w", err)
	}
	authModule, err := auth.New(pool, accounts.New(pool), captcha, mailer, auth.Config{
		Origin:          cfg.AuthOrigin,
		CookieSecure:    cfg.AuthCookieSecure,
		SessionTTL:      cfg.AuthSessionTTL,
		ChallengeTTL:    cfg.AuthChallengeTTL,
		ResetTTL:        cfg.AuthResetTTL,
		RateLimitWindow: cfg.AuthRateWindow,
	})
	if err != nil {
		return fmt.Errorf("configure auth: %w", err)
	}
	handler := newHandler(pool.Ping, cfg.ReadinessTimeout)
	authModule.Register(handler)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	defer server.Close()

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	logger.Info("API listening", "address", listener.Addr().String())
	return serve(ctx, server, listener, cfg.ShutdownTimeout)
}

func serve(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP: %w", err)
		}
		return nil
	}
}
