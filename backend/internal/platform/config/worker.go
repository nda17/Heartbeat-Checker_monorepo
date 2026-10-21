package config

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type WorkerConfig struct {
	IntegrationConfig
	AppEnv                string
	DatabaseURL           string
	DatabaseTimeout       time.Duration
	ProbeURL              string
	ProbeSharedSecret     string
	WebhookEncryptionKey  string
	AuthMailEncryptionKey string
	ProviderASNCacheDir   string
	SMTPAddr              string
	SMTPFrom              string
	SMTPUsername          string
	SMTPPassword          string
	SMTPTLSMode           string
}

func LoadWorker() (WorkerConfig, error) {
	cfg := WorkerConfig{AppEnv: os.Getenv("APP_ENV"), DatabaseURL: os.Getenv("DATABASE_URL"), DatabaseTimeout: 5 * time.Second, ProbeURL: os.Getenv("PROBE_URL"), ProbeSharedSecret: os.Getenv("PROBE_SHARED_SECRET"), WebhookEncryptionKey: os.Getenv("WEBHOOK_ENCRYPTION_KEY")}
	cfg.ProviderASNCacheDir = strings.TrimSpace(os.Getenv("PROVIDER_ASN_CACHE_DIR"))
	if cfg.ProviderASNCacheDir == "" {
		cfg.ProviderASNCacheDir = "/var/lib/heartbeat/asn"
	}
	if cfg.AppEnv == "" {
		cfg.AppEnv = "production"
	}
	if cfg.AppEnv != "local" && cfg.AppEnv != "production" {
		return cfg, errors.New("APP_ENV must be local or production")
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return cfg, errors.New("DATABASE_URL is required")
	}
	if cfg.AppEnv == "local" {
		if err := localDatabase(cfg.DatabaseURL); err != nil {
			return cfg, err
		}
	}
	if cfg.ProbeURL == "" || len(cfg.ProbeSharedSecret) < 32 {
		return cfg, errors.New("PROBE_URL and a PROBE_SHARED_SECRET of at least 32 bytes are required")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(cfg.WebhookEncryptionKey)
	if err != nil || len(key) != 32 {
		return cfg, errors.New("WEBHOOK_ENCRYPTION_KEY must be base64 of 32 bytes")
	}
	cfg.AuthMailEncryptionKey = strings.TrimSpace(os.Getenv("AUTH_MAIL_ENCRYPTION_KEY"))
	if cfg.AuthMailEncryptionKey != "" {
		mailKey, err := base64.StdEncoding.Strict().DecodeString(cfg.AuthMailEncryptionKey)
		if err != nil || len(mailKey) != 32 {
			return cfg, errors.New("AUTH_MAIL_ENCRYPTION_KEY must be base64 of 32 bytes")
		}
	}
	if cfg.AppEnv == "production" {
		cfg.AuthMailEncryptionKey = strings.TrimSpace(os.Getenv("AUTH_MAIL_ENCRYPTION_KEY"))
		mailKey, err := base64.StdEncoding.Strict().DecodeString(cfg.AuthMailEncryptionKey)
		if err != nil || len(mailKey) != 32 {
			return cfg, errors.New("AUTH_MAIL_ENCRYPTION_KEY must be base64 of 32 bytes")
		}
		cfg.SMTPAddr = strings.TrimSpace(os.Getenv("SMTP_ADDR"))
		cfg.SMTPFrom = strings.TrimSpace(os.Getenv("SMTP_FROM"))
		cfg.SMTPUsername = os.Getenv("SMTP_USERNAME")
		cfg.SMTPPassword = os.Getenv("SMTP_PASSWORD")
		cfg.SMTPTLSMode = strings.TrimSpace(os.Getenv("SMTP_TLS_MODE"))
		if cfg.SMTPTLSMode == "" {
			cfg.SMTPTLSMode = "implicit_tls"
		}
		if cfg.SMTPAddr == "" || cfg.SMTPFrom == "" || cfg.SMTPUsername == "" || cfg.SMTPPassword == "" || (cfg.SMTPTLSMode != "implicit_tls" && cfg.SMTPTLSMode != "starttls") {
			return cfg, errors.New("production SMTP configuration is incomplete")
		}
	}
	integrations, err := loadIntegrations(true)
	if err != nil {
		return cfg, err
	}
	cfg.IntegrationConfig = integrations
	if cfg.AppEnv == "local" && (cfg.EmailNotificationsEnabled || cfg.AuthMailEncryptionKey != "") {
		cfg.SMTPAddr = strings.TrimSpace(os.Getenv("SMTP_ADDR"))
		cfg.SMTPFrom = strings.TrimSpace(os.Getenv("SMTP_FROM"))
		if cfg.SMTPAddr == "" || cfg.SMTPFrom == "" {
			return cfg, errors.New("email notifications require local SMTP_ADDR and SMTP_FROM")
		}
	}
	return cfg, nil
}

// LocalGrantDatabase rejects sockets, remote PostgreSQL and other local databases.
// Administrative fixtures must never silently turn into a production entitlement.
func LocalGrantDatabase(raw string) error {
	if os.Getenv("APP_ENV") != "local" {
		return errors.New("local-grant requires APP_ENV=local")
	}
	return localDatabase(raw)
}

// localDatabase accepts only this project's loopback database URL. An exact query
// allowlist prevents PostgreSQL host/service options from overriding that address.
func localDatabase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Path != "/heartbeat_local" && u.Path != "/heartbeat_sdk_local") || u.Port() != "55432" || u.RawQuery != "sslmode=disable" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("APP_ENV=local requires the project loopback PostgreSQL database")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() {
		return errors.New("APP_ENV=local requires the project loopback PostgreSQL database")
	}
	return nil
}

func GrantQuota(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 1000 {
		return 0, errors.New("limit must be between 1 and 1000")
	}
	return n, nil
}
