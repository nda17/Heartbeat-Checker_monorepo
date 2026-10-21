package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	YandexClientID     string
	YandexClientSecret string
	YandexRedirectURI  string
	YandexScopes       string
	VKClientID         string
	VKCallbackURL      string
	IntegrationConfig
	AppEnv                string
	HTTPAddr              string
	DatabaseURL           string
	DatabaseTimeout       time.Duration
	ReadinessTimeout      time.Duration
	ShutdownTimeout       time.Duration
	AuthOrigin            string
	AuthCookieSecure      bool
	AuthSessionTTL        time.Duration
	AuthChallengeTTL      time.Duration
	AuthResetTTL          time.Duration
	AuthRateWindow        time.Duration
	TurnstileSecret       string
	TurnstileHost         string
	TurnstileLocal        bool
	SMTPAddr              string
	SMTPFrom              string
	AuthMailEncryptionKey string
	AuthMFAEncryptionKey  string
	TrustedProxyCIDRs     string
	WebhookEncryptionKey  string
	AndroidDevReleaseDir  string
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:           "production",
		HTTPAddr:         "127.0.0.1:8080",
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		DatabaseTimeout:  5 * time.Second,
		ReadinessTimeout: 2 * time.Second,
		ShutdownTimeout:  10 * time.Second,
	}
	cfg.YandexClientID = strings.TrimSpace(os.Getenv("YANDEX_CLIENT_ID"))
	cfg.YandexClientSecret = strings.TrimSpace(os.Getenv("YANDEX_CLIENT_SECRET"))
	cfg.YandexRedirectURI = strings.TrimSpace(os.Getenv("YANDEX_REDIRECT_URI"))
	cfg.YandexScopes = strings.TrimSpace(os.Getenv("YANDEX_SCOPES"))
	cfg.VKClientID = strings.TrimSpace(os.Getenv("VK_CLIENT_ID"))
	cfg.VKCallbackURL = strings.TrimSpace(os.Getenv("VK_CALLBACK_URL"))
	if (cfg.YandexClientID != "" || cfg.YandexClientSecret != "" || cfg.YandexRedirectURI != "") && (cfg.YandexClientID == "" || cfg.YandexClientSecret == "" || cfg.YandexRedirectURI == "") {
		return Config{}, fmt.Errorf("complete YANDEX OAuth configuration is required")
	}
	if (cfg.VKClientID != "" || cfg.VKCallbackURL != "") && (cfg.VKClientID == "" || cfg.VKCallbackURL == "") {
		return Config{}, fmt.Errorf("complete VK OAuth configuration is required")
	}
	if value, ok := os.LookupEnv("APP_ENV"); ok {
		cfg.AppEnv = value
	}
	if cfg.AppEnv != "local" && cfg.AppEnv != "production" {
		return Config{}, fmt.Errorf("APP_ENV must be local or production")
	}
	if value, ok := os.LookupEnv("HTTP_ADDR"); ok {
		cfg.HTTPAddr = value
	}
	listenHost, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be a host:port address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDR must use a port between 1 and 65535")
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.AppEnv == "local" {
		if err := localDatabase(cfg.DatabaseURL); err != nil {
			return Config{}, err
		}
	}
	for _, setting := range []struct {
		name   string
		target *string
	}{
		{"AUTH_ORIGIN", &cfg.AuthOrigin},
		{"TURNSTILE_SECRET_KEY", &cfg.TurnstileSecret},
		{"TURNSTILE_HOSTNAME", &cfg.TurnstileHost},
		{"WEBHOOK_ENCRYPTION_KEY", &cfg.WebhookEncryptionKey},
	} {
		value := strings.TrimSpace(os.Getenv(setting.name))
		if value == "" {
			return Config{}, fmt.Errorf("%s is required", setting.name)
		}
		*setting.target = value
	}
	key, err := base64.StdEncoding.Strict().DecodeString(cfg.WebhookEncryptionKey)
	if err != nil || len(key) != 32 {
		return Config{}, fmt.Errorf("WEBHOOK_ENCRYPTION_KEY must be base64 of 32 bytes")
	}
	cfg.AuthMFAEncryptionKey = strings.TrimSpace(os.Getenv("AUTH_MFA_ENCRYPTION_KEY"))
	if cfg.AuthMFAEncryptionKey != "" {
		mfaKey, err := base64.StdEncoding.Strict().DecodeString(cfg.AuthMFAEncryptionKey)
		if err != nil || len(mfaKey) != 32 {
			return Config{}, fmt.Errorf("AUTH_MFA_ENCRYPTION_KEY must be base64 of 32 bytes")
		}
		if cfg.AuthMFAEncryptionKey == cfg.WebhookEncryptionKey {
			return Config{}, fmt.Errorf("AUTH_MFA_ENCRYPTION_KEY must be distinct from WEBHOOK_ENCRYPTION_KEY")
		}
	}
	cfg.AuthMailEncryptionKey = strings.TrimSpace(os.Getenv("AUTH_MAIL_ENCRYPTION_KEY"))
	cfg.AndroidDevReleaseDir = strings.TrimSpace(os.Getenv("ANDROID_DEV_RELEASE_DIR"))
	if cfg.AndroidDevReleaseDir != "" && (!filepath.IsAbs(cfg.AndroidDevReleaseDir) || filepath.Clean(cfg.AndroidDevReleaseDir) != cfg.AndroidDevReleaseDir || (cfg.AppEnv == "production" && cfg.AndroidDevReleaseDir != "/run/android-dev")) {
		return Config{}, fmt.Errorf("ANDROID_DEV_RELEASE_DIR must be the private API artifact mount")
	}
	if cfg.AuthMailEncryptionKey != "" {
		mailKey, err := base64.StdEncoding.Strict().DecodeString(cfg.AuthMailEncryptionKey)
		if err != nil || len(mailKey) != 32 {
			return Config{}, fmt.Errorf("AUTH_MAIL_ENCRYPTION_KEY must be base64 of 32 bytes")
		}
		if cfg.AuthMFAEncryptionKey != "" && cfg.AuthMFAEncryptionKey == cfg.AuthMailEncryptionKey {
			return Config{}, fmt.Errorf("AUTH_MFA_ENCRYPTION_KEY must be distinct from AUTH_MAIL_ENCRYPTION_KEY")
		}
	}
	if cfg.AppEnv == "local" {
		cfg.SMTPAddr = strings.TrimSpace(os.Getenv("SMTP_ADDR"))
		cfg.SMTPFrom = strings.TrimSpace(os.Getenv("SMTP_FROM"))
		if cfg.SMTPAddr == "" || cfg.SMTPFrom == "" {
			return Config{}, fmt.Errorf("SMTP_ADDR and SMTP_FROM are required locally")
		}
	} else {
		cfg.AuthMailEncryptionKey = strings.TrimSpace(os.Getenv("AUTH_MAIL_ENCRYPTION_KEY"))
		mailKey, err := base64.StdEncoding.Strict().DecodeString(cfg.AuthMailEncryptionKey)
		if err != nil || len(mailKey) != 32 {
			return Config{}, fmt.Errorf("AUTH_MAIL_ENCRYPTION_KEY must be base64 of 32 bytes")
		}
		if cfg.AuthMFAEncryptionKey != "" && cfg.AuthMFAEncryptionKey == cfg.AuthMailEncryptionKey {
			return Config{}, fmt.Errorf("AUTH_MFA_ENCRYPTION_KEY must be distinct from AUTH_MAIL_ENCRYPTION_KEY")
		}
		cfg.TrustedProxyCIDRs = strings.TrimSpace(os.Getenv("TRUSTED_PROXY_CIDRS"))
		if cfg.TrustedProxyCIDRs == "" {
			return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS is required")
		}
		for _, raw := range strings.Split(cfg.TrustedProxyCIDRs, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil || !prefix.IsValid() || prefix.Addr().IsUnspecified() {
				return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS must contain valid IP prefixes")
			}
		}
	}
	for _, setting := range []struct {
		name   string
		target *bool
	}{
		{"AUTH_COOKIE_SECURE", &cfg.AuthCookieSecure},
		{"TURNSTILE_LOCAL_TEST_MODE", &cfg.TurnstileLocal},
	} {
		value, ok := os.LookupEnv(setting.name)
		if !ok {
			return Config{}, fmt.Errorf("%s is required", setting.name)
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a boolean", setting.name)
		}
		*setting.target = parsed
	}
	if cfg.AppEnv == "local" || cfg.TurnstileLocal {
		mode := "TURNSTILE_LOCAL_TEST_MODE"
		if cfg.AppEnv == "local" {
			mode = "APP_ENV=local"
		}
		listenIP, err := netip.ParseAddr(listenHost)
		if err != nil || !listenIP.IsLoopback() {
			return Config{}, fmt.Errorf("%s requires a literal loopback HTTP_ADDR", mode)
		}
		origin, err := url.Parse(cfg.AuthOrigin)
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
			return Config{}, fmt.Errorf("%s requires a loopback AUTH_ORIGIN", mode)
		}
		host := origin.Hostname()
		if host != "localhost" {
			originIP, err := netip.ParseAddr(host)
			if err != nil || !originIP.IsLoopback() {
				return Config{}, fmt.Errorf("%s requires a loopback AUTH_ORIGIN", mode)
			}
		}
	}

	for _, setting := range []struct {
		name   string
		target *time.Duration
		max    time.Duration
	}{
		{"DATABASE_TIMEOUT", &cfg.DatabaseTimeout, 30 * time.Second},
		{"READINESS_TIMEOUT", &cfg.ReadinessTimeout, 5 * time.Second},
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout, time.Minute},
		{"AUTH_SESSION_TTL", &cfg.AuthSessionTTL, 90 * 24 * time.Hour},
		{"AUTH_CHALLENGE_TTL", &cfg.AuthChallengeTTL, 24 * time.Hour},
		{"AUTH_RESET_TTL", &cfg.AuthResetTTL, 24 * time.Hour},
		{"AUTH_RATE_LIMIT_WINDOW", &cfg.AuthRateWindow, 24 * time.Hour},
	} {
		value, ok := os.LookupEnv(setting.name)
		if !ok {
			continue
		}
		duration, err := time.ParseDuration(value)
		minimum := time.Duration(1)
		if strings.HasPrefix(setting.name, "AUTH_") {
			minimum = time.Second
		}
		if err != nil || duration < minimum || duration > setting.max {
			return Config{}, fmt.Errorf("%s must be a duration greater than zero and no more than %s", setting.name, setting.max)
		}
		*setting.target = duration
	}
	integrations, err := loadIntegrations(false)
	if err != nil {
		return cfg, err
	}
	cfg.IntegrationConfig = integrations
	return cfg, nil
}
