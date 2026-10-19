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
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/admin"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/support"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/postgres"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if cfg.AppEnv == "production" && cfg.AuthMFAEncryptionKey == "" {
		return errors.New("AUTH_MFA_ENCRYPTION_KEY is required by API")
	}
	pool, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DatabaseTimeout)
	if err != nil {
		return err
	}
	defer pool.Close()
	captcha, err := auth.NewTurnstile(cfg.TurnstileSecret, cfg.TurnstileHost, cfg.TurnstileLocal)
	if err != nil {
		return fmt.Errorf("configure Turnstile: %w", err)
	}
	var mailer auth.MailSender
	if cfg.AppEnv == "local" {
		mailer, err = auth.NewLocalMailer(cfg.SMTPAddr, cfg.SMTPFrom)
		if err != nil {
			return fmt.Errorf("configure local SMTP: %w", err)
		}
	}
	authModule, err := auth.New(pool, accounts.New(pool), captcha, mailer, auth.Config{
		Origin:            cfg.AuthOrigin,
		MailEncryptionKey: cfg.AuthMailEncryptionKey,
		MFAEncryptionKey:  cfg.AuthMFAEncryptionKey,
		MFAAudit:          admin.AuditMFA,
		CookieSecure:      cfg.AuthCookieSecure,
		SessionTTL:        cfg.AuthSessionTTL,
		ChallengeTTL:      cfg.AuthChallengeTTL,
		ResetTTL:          cfg.AuthResetTTL,
		RateLimitWindow:   cfg.AuthRateWindow,
	})
	if err != nil {
		return fmt.Errorf("configure auth: %w", err)
	}
	handler := newHandler(pool.Ping, cfg.ReadinessTimeout)
	authModule.Register(handler)
	accountModule := accounts.New(pool)
	notificationModule, err := notifications.New(pool, cfg.WebhookEncryptionKey, monitors.NewOwnership(), accountModule)
	if err != nil {
		return fmt.Errorf("configure webhook notifications: %w", err)
	}
	billingModule, err := billing.NewConfigured(pool, cfg.AppEnv == "local", billing.Config{Provider: cfg.BillingProvider, MockAccountIDs: cfg.BillingMockAccountIDs}, accountModule)
	if err != nil {
		return fmt.Errorf("configure billing: %w", err)
	}
	messengerModule, err := notifications.NewMessenger(pool, cfg.WebhookEncryptionKey, notifications.MessengerConfig{
		Enabled: cfg.MessengerNotificationsEnabled, TelegramWebhookSecret: cfg.TelegramInformerWebhookSecret, MAXWebhookSecret: cfg.MAXInformerWebhookSecret,
		TelegramBotUsername: cfg.TelegramInformerBotUsername, MAXBotURL: cfg.MAXInformerBotURL,
	}, false, billingModule)
	if err != nil {
		return fmt.Errorf("configure messenger notifications: %w", err)
	}
	billingModule.Register(handler, authModule.Protect)
	monitorModule := monitors.New(pool, billingModule, incidents.New(pool, notifications.NewRecorder(messengerModule)), accountModule)
	monitorModule.Register(handler, authModule.Protect)
	admin.New(pool, accountModule, authModule, billingModule, monitorModule).Register(handler, func(next http.Handler) http.Handler {
		return authModule.RequireRole("admin", next)
	})
	if cfg.SupportEnabled {
		supportModule, err := support.NewWebhook(pool, support.Config{EncryptionKey: cfg.SupportEncryptionKey, TelegramWebhookSecret: cfg.TelegramSupportWebhookSecret, MAXWebhookSecret: cfg.MAXSupportWebhookSecret, InternalChatID: cfg.TelegramInternalChatID, SupportTopicID: cfg.TelegramSupportTopicID, MAXSupportChatID: cfg.MAXSupportChatID})
		if err != nil {
			return fmt.Errorf("configure support webhooks: %w", err)
		}
		supportModule.Register(handler)
	}
	notificationModule.Register(handler, authModule.Protect)
	messengerModule.RegisterAPI(handler, authModule.Protect)
	messengerModule.RegisterIngest(handler)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           trustedClientIP(handler, cfg.AppEnv, cfg.TrustedProxyCIDRs),
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
