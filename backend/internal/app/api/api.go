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
	oauthProviders := make(map[string]auth.OAuthProvider)
	for _, providerCfg := range []auth.OAuthProviderConfig{{Provider: "yandex", ClientID: cfg.YandexClientID, ClientSecret: cfg.YandexClientSecret, RedirectURI: cfg.YandexRedirectURI, Scopes: cfg.YandexScopes}, {Provider: "vk", ClientID: cfg.VKClientID, RedirectURI: cfg.VKCallbackURL}} {
		if providerCfg.ClientID == "" {
			continue
		}
		if providerCfg.RedirectURI != cfg.AuthOrigin+"/api/v1/auth/oauth/"+providerCfg.Provider+"/callback" {
			return fmt.Errorf("OAuth callback must match AUTH_ORIGIN")
		}
		provider, err := auth.NewOAuthProvider(providerCfg)
		if err != nil {
			return fmt.Errorf("configure %s OAuth: %w", providerCfg.Provider, err)
		}
		oauthProviders[provider.ID()] = provider
	}
	accountModule := accounts.New(pool)
	notificationModule, err := notifications.New(pool, cfg.WebhookEncryptionKey, monitors.NewOwnership(), accountModule)
	if err != nil {
		return fmt.Errorf("configure webhook notifications: %w", err)
	}
	shopID, secretKey := cfg.YooKassaTestShopID, cfg.YooKassaTestSecretKey
	if cfg.YooKassaMode == "production" {
		shopID, secretKey = cfg.YooKassaProductionShopID, cfg.YooKassaProductionSecretKey
	}
	billingModule, err := billing.NewConfigured(pool, cfg.AppEnv == "local", billing.Config{Provider: cfg.BillingProvider, MockAccountIDs: cfg.BillingMockAccountIDs, TestAccountIDs: cfg.BillingTestAccountIDs, Mode: cfg.YooKassaMode, ShopID: shopID, SecretKey: secretKey, TestShopID: cfg.YooKassaTestShopID, TestSecretKey: cfg.YooKassaTestSecretKey, NativeSDKEnabled: cfg.YooKassaNativeSDKEnabled, NativeSDKMode: cfg.YooKassaNativeSDKMode, MailEncryptionKey: cfg.AuthMailEncryptionKey, SessionLease: auth.SessionLeaseTx}, accountModule)
	if err != nil {
		return fmt.Errorf("configure billing: %w", err)
	}
	messengerModule, err := notifications.NewMessenger(pool, cfg.WebhookEncryptionKey, notifications.MessengerConfig{
		EmailEnabled: cfg.EmailNotificationsEnabled, EmailAddressReader: auth.EmailForNotificationsTx,
		Enabled: cfg.MessengerNotificationsEnabled, TelegramWebhookSecret: cfg.TelegramInformerWebhookSecret, MAXWebhookSecret: cfg.MAXInformerWebhookSecret,
		TelegramBotUsername: cfg.TelegramInformerBotUsername, MAXBotURL: cfg.MAXInformerBotURL,
	}, false, billingModule)
	if err != nil {
		return fmt.Errorf("configure messenger notifications: %w", err)
	}
	pushModule, err := notifications.NewPush(pool, cfg.WebhookEncryptionKey, notifications.PushConfig{Enabled: cfg.AndroidPushEnabled, ProjectID: cfg.RuStorePushProjectID, Providers: cfg.PushProviders(), Origin: cfg.AuthOrigin, SessionLease: auth.SessionLeaseTx}, billingModule)
	if err != nil {
		return fmt.Errorf("configure Android push: %w", err)
	}
	monitorModule := monitors.New(pool, billingModule, incidents.New(pool, notifications.NewRecorder(messengerModule).WithPush(pushModule)), accountModule, notifications.Channels{messengerModule, pushModule})
	authModule, err := auth.New(pool, accountModule, captcha, mailer, auth.Config{
		Origin:            cfg.AuthOrigin,
		OAuthProviders:    oauthProviders,
		OAuthAudit:        admin.AuditOAuth,
		MailEncryptionKey: cfg.AuthMailEncryptionKey,
		MFAEncryptionKey:  cfg.AuthMFAEncryptionKey,
		MFAAudit:          admin.AuditMFA,
		CookieSecure:      cfg.AuthCookieSecure,
		SessionTTL:        cfg.AuthSessionTTL,
		ChallengeTTL:      cfg.AuthChallengeTTL,
		ResetTTL:          cfg.AuthResetTTL,
		RateLimitWindow:   cfg.AuthRateWindow,
		AccountDeletion:   accountDeletion{accounts: accountModule, billing: billingModule, monitors: monitorModule},
	})
	if err != nil {
		return fmt.Errorf("configure auth: %w", err)
	}
	messengerModule.SetEmailCodes(authModule)
	handler := newHandler(pool.Ping, cfg.ReadinessTimeout)
	authModule.Register(handler)
	billingModule.Register(handler, authModule.Protect)
	monitorModule.Register(handler, authModule.Protect)
	admin.New(pool, accountModule, authModule, billingModule, monitorModule).Register(handler, func(next http.Handler) http.Handler {
		return authModule.RequireRole("admin", next)
	})
	admin.RegisterDevAPK(handler, cfg.AndroidDevReleaseDir, func(next http.Handler) http.Handler {
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
	pushModule.RegisterAPI(handler, authModule.Protect)

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
