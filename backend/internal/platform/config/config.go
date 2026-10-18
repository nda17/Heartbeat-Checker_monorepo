package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv               string
	HTTPAddr             string
	DatabaseURL          string
	DatabaseTimeout      time.Duration
	ReadinessTimeout     time.Duration
	ShutdownTimeout      time.Duration
	AuthOrigin           string
	AuthCookieSecure     bool
	AuthSessionTTL       time.Duration
	AuthChallengeTTL     time.Duration
	AuthResetTTL         time.Duration
	AuthRateWindow       time.Duration
	TurnstileSecret      string
	TurnstileHost        string
	TurnstileLocal       bool
	SMTPAddr             string
	SMTPFrom             string
	WebhookEncryptionKey string
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
		{"SMTP_ADDR", &cfg.SMTPAddr},
		{"SMTP_FROM", &cfg.SMTPFrom},
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
	return cfg, nil
}
