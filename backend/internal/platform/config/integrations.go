package config

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// IntegrationConfig is loaded independently by API and worker so both apply the
// same explicit mock entitlement policy. Bot credentials are worker-only.
type IntegrationConfig struct {
	PushConfig
	BillingProvider               string
	BillingMockAccountIDs         []string
	BillingTestAccountIDs         []string
	YooKassaMode                  string
	YooKassaTestShopID            string
	YooKassaTestSecretKey         string
	BillingReportsEnabled         bool
	MessengerNotificationsEnabled bool
	EmailNotificationsEnabled     bool
	TelegramInformerToken         string
	MAXInformerToken              string
	TelegramInformerBotUsername   string
	MAXInformerBotURL             string
	TelegramInformerWebhookSecret string
	MAXInformerWebhookSecret      string
	InformerMAXRootCAFile         string
	TelegramInternalChatID        int64
	TelegramPaymentsTopicID       int64
	SupportEnabled                bool
	SupportEncryptionKey          string
	TelegramSupportToken          string
	TelegramSupportWebhookSecret  string
	MAXSupportToken               string
	MAXSupportWebhookSecret       string
	TelegramSupportTopicID        int64
	TelegramAPIIP                 string
	TelegramAPIBaseURL            string
	MAXSupportChatID              int64
	SupportMAXRootCAFile          string
}

func loadIntegrations(worker bool) (IntegrationConfig, error) {
	cfg := IntegrationConfig{BillingProvider: strings.TrimSpace(os.Getenv("BILLING_PROVIDER"))}
	push, err := loadPush(worker)
	if err != nil {
		return cfg, err
	}
	cfg.PushConfig = push
	if cfg.BillingProvider == "" {
		cfg.BillingProvider = "disabled"
	}
	if cfg.BillingProvider != "disabled" && cfg.BillingProvider != "mock" && cfg.BillingProvider != "yookassa" {
		return cfg, errors.New("BILLING_PROVIDER must be disabled, mock or yookassa")
	}
	raw := strings.TrimSpace(os.Getenv("BILLING_MOCK_ACCOUNT_IDS"))
	if raw != "" {
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			var parsed pgtype.UUID
			if len(id) != 36 || parsed.Scan(id) != nil || !parsed.Valid {
				return cfg, errors.New("BILLING_MOCK_ACCOUNT_IDS must contain comma-separated UUIDs")
			}
			cfg.BillingMockAccountIDs = append(cfg.BillingMockAccountIDs, parsed.String())
		}
	}
	if cfg.BillingProvider == "yookassa" {
		cfg.YooKassaMode = os.Getenv("YOOKASSA_MODE")
		cfg.YooKassaTestShopID = os.Getenv("YOOKASSA_TEST_SHOP_ID")
		cfg.YooKassaTestSecretKey = os.Getenv("YOOKASSA_TEST_SECRET_KEY")
		if cfg.YooKassaMode != "test" || cfg.YooKassaTestShopID == "" || len(cfg.YooKassaTestShopID) > 32 || strings.Trim(cfg.YooKassaTestShopID, "0123456789") != "" || !strings.HasPrefix(cfg.YooKassaTestSecretKey, "test_") || len(cfg.YooKassaTestSecretKey) < 16 || len(cfg.YooKassaTestSecretKey) > 512 || strings.ContainsAny(cfg.YooKassaTestSecretKey, " \t\r\n") {
			return cfg, errors.New("complete YooKassa test configuration is required")
		}
		id := strings.TrimSpace(os.Getenv("BILLING_TEST_ACCOUNT_IDS"))
		var parsed pgtype.UUID
		if len(id) != 36 || parsed.Scan(id) != nil || !parsed.Valid {
			return cfg, errors.New("BILLING_TEST_ACCOUNT_IDS must contain exactly one owner UUID")
		}
		cfg.BillingTestAccountIDs = []string{parsed.String()}
	}
	if cfg.SupportEnabled, err = integrationBool("SUPPORT_ENABLED"); err != nil {
		return cfg, err
	}
	if cfg.BillingReportsEnabled, err = integrationBool("BILLING_REPORTS_ENABLED"); err != nil {
		return cfg, err
	}
	if cfg.MessengerNotificationsEnabled, err = integrationBool("MESSENGER_NOTIFICATIONS_ENABLED"); err != nil {
		return cfg, err
	}
	if cfg.EmailNotificationsEnabled, err = integrationBool("EMAIL_NOTIFICATIONS_ENABLED"); err != nil {
		return cfg, err
	}
	if !cfg.SupportEnabled && !cfg.BillingReportsEnabled && !cfg.MessengerNotificationsEnabled {
		return cfg, nil
	}
	cfg.TelegramAPIBaseURL = strings.TrimSpace(os.Getenv("TELEGRAM_API_BASE_URL"))
	if cfg.TelegramAPIBaseURL == "" {
		cfg.TelegramAPIBaseURL = "https://api.telegram.org"
	}
	if cfg.TelegramAPIBaseURL != "https://api.telegram.org" && cfg.TelegramAPIBaseURL != "https://messengers-proxy.heartbeat-checker.ru/telegram-api" {
		return cfg, errors.New("TELEGRAM_API_BASE_URL must be an approved HTTPS Telegram endpoint")
	}
	if cfg.SupportEnabled || cfg.BillingReportsEnabled {
		cfg.TelegramInternalChatID, err = strconv.ParseInt(os.Getenv("TELEGRAM_INTERNAL_CHAT_ID"), 10, 64)
		if err != nil || cfg.TelegramInternalChatID >= 0 {
			return cfg, errors.New("TELEGRAM_INTERNAL_CHAT_ID must identify an internal group")
		}
	}
	cfg.TelegramAPIIP = strings.TrimSpace(os.Getenv("TELEGRAM_API_PROXY_IP"))
	if cfg.TelegramAPIIP != "" {
		ip, err := netip.ParseAddr(cfg.TelegramAPIIP)
		if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return cfg, errors.New("TELEGRAM_API_PROXY_IP must be a public literal IP")
		}
	}
	if cfg.BillingReportsEnabled {
		cfg.TelegramPaymentsTopicID, err = strconv.ParseInt(os.Getenv("TELEGRAM_PAYMENTS_TOPIC_ID"), 10, 64)
		if err != nil || cfg.TelegramPaymentsTopicID <= 0 {
			return cfg, errors.New("TELEGRAM_PAYMENTS_TOPIC_ID must be positive")
		}
		if worker {
			cfg.TelegramInformerToken = os.Getenv("TELEGRAM_BOT_INFORMER_TOKEN")
			if !telegramToken(cfg.TelegramInformerToken) {
				return cfg, errors.New("TELEGRAM_BOT_INFORMER_TOKEN is required for payment reports")
			}
		}
	}
	if cfg.MessengerNotificationsEnabled {
		cfg.TelegramInformerBotUsername = strings.TrimPrefix(strings.TrimSpace(os.Getenv("TELEGRAM_INFORMER_BOT_USERNAME")), "@")
		cfg.MAXInformerBotURL = strings.TrimRight(strings.TrimSpace(os.Getenv("MAX_INFORMER_BOT_URL")), "/")
		cfg.InformerMAXRootCAFile = strings.TrimSpace(os.Getenv("SUPPORT_MAX_ROOT_CA_FILE"))
		if cfg.TelegramInformerBotUsername == "" || strings.ContainsAny(cfg.TelegramInformerBotUsername, " /?&#\r\n") || !strings.HasPrefix(cfg.MAXInformerBotURL, "https://max.ru/") {
			return cfg, errors.New("messenger bot public links are required")
		}
		if worker {
			if cfg.TelegramInformerToken == "" {
				cfg.TelegramInformerToken = os.Getenv("TELEGRAM_BOT_INFORMER_TOKEN")
			}
			cfg.MAXInformerToken = os.Getenv("MAX_BOT_INFORMER_TOKEN")
			if !telegramToken(cfg.TelegramInformerToken) || cfg.MAXInformerToken == "" || strings.ContainsAny(cfg.MAXInformerToken, "\r\n") {
				return cfg, errors.New("messenger bot tokens are required")
			}
		} else {
			cfg.TelegramInformerWebhookSecret = os.Getenv("TELEGRAM_INFORMER_WEBHOOK_SECRET")
			cfg.MAXInformerWebhookSecret = os.Getenv("MAX_INFORMER_WEBHOOK_SECRET")
			if !webhookSecret(cfg.TelegramInformerWebhookSecret) || !webhookSecret(cfg.MAXInformerWebhookSecret) {
				return cfg, errors.New("messenger webhook secrets must be 32-256 URL-safe characters")
			}
		}
	}
	if cfg.SupportEnabled {
		if raw := strings.TrimSpace(os.Getenv("MAX_SUPPORT_CHAT_ID")); raw != "" {
			cfg.MAXSupportChatID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return cfg, errors.New("MAX_SUPPORT_CHAT_ID must be a numeric chat ID or 0 for discovery")
			}
		}
		cfg.SupportEncryptionKey = strings.TrimSpace(os.Getenv("SUPPORT_ENCRYPTION_KEY"))
		key, err := base64.StdEncoding.Strict().DecodeString(cfg.SupportEncryptionKey)
		if err != nil || len(key) != 32 {
			return cfg, errors.New("SUPPORT_ENCRYPTION_KEY must be base64 of 32 bytes")
		}
		if !worker {
			cfg.TelegramSupportWebhookSecret = os.Getenv("TELEGRAM_SUPPORT_WEBHOOK_SECRET")
			cfg.MAXSupportWebhookSecret = os.Getenv("MAX_SUPPORT_WEBHOOK_SECRET")
			if !webhookSecret(cfg.TelegramSupportWebhookSecret) || !webhookSecret(cfg.MAXSupportWebhookSecret) {
				return cfg, errors.New("support webhook secrets must be 32-256 URL-safe characters")
			}
		}
		cfg.TelegramSupportTopicID, err = strconv.ParseInt(os.Getenv("TELEGRAM_SUPPORT_TOPIC_ID"), 10, 64)
		if err != nil || cfg.TelegramSupportTopicID <= 0 {
			return cfg, errors.New("TELEGRAM_SUPPORT_TOPIC_ID must be positive")
		}
		cfg.SupportMAXRootCAFile = strings.TrimSpace(os.Getenv("SUPPORT_MAX_ROOT_CA_FILE"))
		if worker {
			cfg.TelegramSupportToken = os.Getenv("TELEGRAM_BOT_SUPPORT_TOKEN")
			cfg.MAXSupportToken = os.Getenv("MAX_BOT_SUPPORT_TOKEN")
			if !telegramToken(cfg.TelegramSupportToken) || cfg.MAXSupportToken == "" || strings.ContainsAny(cfg.MAXSupportToken, "\r\n") {
				return cfg, errors.New("dedicated support bot tokens are required")
			}
		}
	}
	return cfg, nil
}

func integrationBool(name string) (bool, error) {
	raw, exists := os.LookupEnv(name)
	if !exists {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New(name + " must be a boolean")
	}
	return value, nil
}
func telegramToken(value string) bool {
	return strings.Contains(value, ":") && !strings.ContainsAny(value, "/\r\n?# ")
}
func webhookSecret(value string) bool {
	if len(value) < 32 || len(value) > 256 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
