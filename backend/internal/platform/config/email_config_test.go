package config

import (
	"os"
	"testing"
)

func TestLoadIntegrationsEmailOnlyDoesNotRequireMessengerBots(t *testing.T) {
	previous, existed := os.LookupEnv("EMAIL_NOTIFICATIONS_ENABLED")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("EMAIL_NOTIFICATIONS_ENABLED", previous)
		} else {
			_ = os.Unsetenv("EMAIL_NOTIFICATIONS_ENABLED")
		}
	})

	clearConfigEnv(t)
	_ = os.Unsetenv("MESSENGER_NOTIFICATIONS_ENABLED")
	_ = os.Unsetenv("EMAIL_NOTIFICATIONS_ENABLED")

	for _, worker := range []bool{false, true} {
		cfg, err := loadIntegrations(worker)
		if err != nil {
			t.Fatalf("loadIntegrations(worker=%t) with notifications disabled: %v", worker, err)
		}
		if cfg.EmailNotificationsEnabled || cfg.MessengerNotificationsEnabled {
			t.Fatalf("notifications defaulted on: email=%t messenger=%t", cfg.EmailNotificationsEnabled, cfg.MessengerNotificationsEnabled)
		}

		_ = os.Setenv("EMAIL_NOTIFICATIONS_ENABLED", "true")
		cfg, err = loadIntegrations(worker)
		if err != nil {
			t.Fatalf("loadIntegrations(worker=%t) with email-only enabled and no bot credentials: %v", worker, err)
		}
		if !cfg.EmailNotificationsEnabled || cfg.MessengerNotificationsEnabled {
			t.Fatalf("email-only configuration = email:%t messenger:%t", cfg.EmailNotificationsEnabled, cfg.MessengerNotificationsEnabled)
		}
		if cfg.TelegramInformerToken != "" || cfg.MAXInformerToken != "" || cfg.TelegramInformerWebhookSecret != "" || cfg.MAXInformerWebhookSecret != "" {
			t.Fatalf("email-only configuration unexpectedly loaded messenger credentials: %+v", cfg)
		}
		_ = os.Unsetenv("EMAIL_NOTIFICATIONS_ENABLED")
	}
}
