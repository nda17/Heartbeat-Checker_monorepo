package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"APP_ENV", "HTTP_ADDR", "DATABASE_URL", "DATABASE_TIMEOUT", "READINESS_TIMEOUT", "SHUTDOWN_TIMEOUT",
		"AUTH_ORIGIN", "AUTH_COOKIE_SECURE", "AUTH_SESSION_TTL", "AUTH_CHALLENGE_TTL", "AUTH_RESET_TTL", "AUTH_RATE_LIMIT_WINDOW",
		"TURNSTILE_SECRET_KEY", "TURNSTILE_HOSTNAME", "TURNSTILE_LOCAL_TEST_MODE", "SMTP_ADDR", "SMTP_FROM",
		"SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_TLS_MODE", "AUTH_MAIL_ENCRYPTION_KEY", "TRUSTED_PROXY_CIDRS",
		"WEBHOOK_ENCRYPTION_KEY", "PROBE_URL", "PROBE_SHARED_SECRET",
		"BILLING_PROVIDER", "BILLING_MOCK_ACCOUNT_IDS", "BILLING_TEST_ACCOUNT_IDS",
		"YOOKASSA_MODE", "YOOKASSA_TEST_SHOP_ID", "YOOKASSA_TEST_SECRET_KEY",
		"YOOKASSA_PRODUCTION_SHOP_ID", "YOOKASSA_PRODUCTION_SECRET_KEY",
	} {
		old, existed := os.LookupEnv(key)
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		})
		_ = os.Unsetenv(key)
	}
}

func TestLoadIntegrationsKeepsYooKassaExplicitlyInTestModeForOneOwner(t *testing.T) {
	const owner = "51ec7d19-80c4-4fb6-8edb-79dc7abe3120"
	const mockAccount = "61ec7d19-80c4-4fb6-8edb-79dc7abe3120"
	set := func(t *testing.T, mode, shop, secret, ownerIDs string) {
		t.Helper()
		clearConfigEnv(t)
		_ = os.Setenv("BILLING_PROVIDER", "yookassa")
		_ = os.Setenv("BILLING_MOCK_ACCOUNT_IDS", mockAccount)
		_ = os.Setenv("BILLING_TEST_ACCOUNT_IDS", ownerIDs)
		_ = os.Setenv("YOOKASSA_MODE", mode)
		_ = os.Setenv("YOOKASSA_TEST_SHOP_ID", shop)
		_ = os.Setenv("YOOKASSA_TEST_SECRET_KEY", secret)
	}

	t.Run("test sandbox configuration is shared by API and worker and preserves mock IDs", func(t *testing.T) {
		set(t, "test", "123456", "test_synthetic_secret_123", owner)
		for _, worker := range []bool{false, true} {
			cfg, err := loadIntegrations(worker)
			if err != nil {
				t.Fatalf("loadIntegrations(worker=%t): %v", worker, err)
			}
			if cfg.BillingProvider != "yookassa" || cfg.YooKassaMode != "test" || cfg.YooKassaTestShopID != "123456" || cfg.YooKassaTestSecretKey != "test_synthetic_secret_123" || len(cfg.BillingTestAccountIDs) != 1 || cfg.BillingTestAccountIDs[0] != owner {
				t.Fatalf("unexpected YooKassa configuration: %+v", cfg)
			}
			if len(cfg.BillingMockAccountIDs) != 1 || cfg.BillingMockAccountIDs[0] != mockAccount {
				t.Fatalf("existing mock account config was not preserved: %+v", cfg.BillingMockAccountIDs)
			}
		}
	})

	for _, test := range []struct {
		name, mode, shop, secret, ownerIDs string
	}{
		{"production mode", "live", "123456", "test_synthetic_secret_123", owner},
		{"live credential", "test", "123456", "live_synthetic_secret_123", owner},
		{"missing owner", "test", "123456", "test_synthetic_secret_123", ""},
		{"multiple owners", "test", "123456", "test_synthetic_secret_123", owner + "," + mockAccount},
		{"non-numeric shop", "test", "shop", "test_synthetic_secret_123", owner},
	} {
		t.Run(test.name+" is rejected", func(t *testing.T) {
			set(t, test.mode, test.shop, test.secret, test.ownerIDs)
			if _, err := loadIntegrations(false); err == nil {
				t.Fatal("loadIntegrations() succeeded with invalid YooKassa configuration")
			}
		})
	}
}

func TestLoadIntegrationsSelectsIndependentProductionCredentialsWithoutOwnerGate(t *testing.T) {
	const mockAccount = "61ec7d19-80c4-4fb6-8edb-79dc7abe3120"
	clearConfigEnv(t)
	_ = os.Setenv("BILLING_PROVIDER", "yookassa")
	_ = os.Setenv("BILLING_MOCK_ACCOUNT_IDS", mockAccount)
	_ = os.Setenv("YOOKASSA_MODE", "production")
	_ = os.Setenv("YOOKASSA_TEST_SHOP_ID", "123456")
	_ = os.Setenv("YOOKASSA_TEST_SECRET_KEY", "test_synthetic_secret_123")
	_ = os.Setenv("YOOKASSA_PRODUCTION_SHOP_ID", "654321")
	_ = os.Setenv("YOOKASSA_PRODUCTION_SECRET_KEY", "production_synthetic_secret_123")

	for _, worker := range []bool{false, true} {
		cfg, err := loadIntegrations(worker)
		if err != nil {
			t.Fatalf("loadIntegrations(worker=%t): %v", worker, err)
		}
		if cfg.YooKassaMode != "production" || cfg.YooKassaProductionShopID != "654321" || cfg.YooKassaProductionSecretKey != "production_synthetic_secret_123" {
			t.Fatalf("production credentials were not selected for worker=%t: %+v", worker, cfg)
		}
		if len(cfg.BillingTestAccountIDs) != 0 {
			t.Fatalf("production mode unexpectedly has a test allowlist: %+v", cfg.BillingTestAccountIDs)
		}
		if len(cfg.BillingMockAccountIDs) != 1 || cfg.BillingMockAccountIDs[0] != mockAccount {
			t.Fatalf("existing mock account config was not preserved: %+v", cfg.BillingMockAccountIDs)
		}
	}
}

func TestLoadIntegrationsRejectsIncompleteOrCrossModeProductionCredentials(t *testing.T) {
	tests := []struct {
		name   string
		shop   string
		secret string
	}{
		{name: "missing shop", shop: "", secret: "production_synthetic_secret_123"},
		{name: "non-numeric shop", shop: "shop", secret: "production_synthetic_secret_123"},
		{name: "missing secret", shop: "654321", secret: ""},
		{name: "test secret in production", shop: "654321", secret: "test_synthetic_secret_123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearConfigEnv(t)
			_ = os.Setenv("BILLING_PROVIDER", "yookassa")
			_ = os.Setenv("YOOKASSA_MODE", "production")
			_ = os.Setenv("YOOKASSA_PRODUCTION_SHOP_ID", tc.shop)
			_ = os.Setenv("YOOKASSA_PRODUCTION_SECRET_KEY", tc.secret)
			if _, err := loadIntegrations(false); err == nil {
				t.Fatal("loadIntegrations() succeeded with invalid production credentials")
			}
		})
	}
}

func TestAppEnvLocalAlwaysRequiresProjectDatabaseAndLoopbackEndpoints(t *testing.T) {
	validDatabase := "postgres://user:secret@127.0.0.1:55432/heartbeat_local?sslmode=disable"
	tests := []struct {
		name, database, httpAddr, origin string
		wantErr                          bool
	}{
		{"exact local configuration with live Turnstile", validDatabase, "127.0.0.1:8080", "http://localhost:3000", false},
		{"DNS database hostname", "postgres://user:secret@localhost:55432/heartbeat_local?sslmode=disable", "127.0.0.1:8080", "http://localhost:3000", true},
		{"remote database", "postgres://user:secret@192.0.2.10:55432/heartbeat_local?sslmode=disable", "127.0.0.1:8080", "http://localhost:3000", true},
		{"wrong database port", "postgres://user:secret@127.0.0.1:5432/heartbeat_local?sslmode=disable", "127.0.0.1:8080", "http://localhost:3000", true},
		{"wrong database name", "postgres://user:secret@127.0.0.1:55432/heartbeat_local_test?sslmode=disable", "127.0.0.1:8080", "http://localhost:3000", true},
		{"database host override query", validDatabase + "&host=192.0.2.10", "127.0.0.1:8080", "http://localhost:3000", true},
		{"wildcard API bind", validDatabase, "0.0.0.0:8080", "http://localhost:3000", true},
		{"DNS API bind", validDatabase, "localhost:8080", "http://localhost:3000", true},
		{"external origin", validDatabase, "127.0.0.1:8080", "https://app.example.com", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredConfigEnv(t)
			_ = os.Setenv("APP_ENV", "local")
			_ = os.Setenv("TURNSTILE_LOCAL_TEST_MODE", "false")
			_ = os.Setenv("DATABASE_URL", tt.database)
			_ = os.Setenv("HTTP_ADDR", tt.httpAddr)
			_ = os.Setenv("AUTH_ORIGIN", tt.origin)
			_, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func setRequiredConfigEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"DATABASE_URL":              "postgres://user:secret@localhost:5432/app",
		"AUTH_ORIGIN":               "http://localhost:3000",
		"AUTH_COOKIE_SECURE":        "false",
		"TURNSTILE_SECRET_KEY":      "1x0000000000000000000000000000000AA",
		"TURNSTILE_HOSTNAME":        "localhost",
		"TURNSTILE_LOCAL_TEST_MODE": "true",
		"SMTP_ADDR":                 "127.0.0.1:51025",
		"SMTP_FROM":                 "noreply@example.com",
		"SMTP_USERNAME":             "test-user",
		"SMTP_PASSWORD":             "test-password",
		"SMTP_TLS_MODE":             "implicit_tls",
		"AUTH_MAIL_ENCRYPTION_KEY":  "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"TRUSTED_PROXY_CIDRS":       "172.30.0.0/24,fd42:30::/64",
		"PROBE_URL":                 "http://probe:8090",
		"PROBE_SHARED_SECRET":       "0123456789abcdef0123456789abcdef",
		"WEBHOOK_ENCRYPTION_KEY":    "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	} {
		_ = os.Setenv(key, value)
	}
}

func TestLoadLocalModeRequiresLoopbackAPIAndOrigin(t *testing.T) {
	tests := []struct {
		name, httpAddr, origin string
		wantErr                bool
	}{
		{"localhost", "127.0.0.1:8080", "http://localhost:3000", false},
		{"IPv6 loopback", "[::1]:8080", "http://[::1]:3000", false},
		{"wildcard API listener", "0.0.0.0:8080", "http://localhost:3000", true},
		{"external API listener", "192.0.2.10:8080", "http://localhost:3000", true},
		{"external origin", "127.0.0.1:8080", "https://app.example.com", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredConfigEnv(t)
			_ = os.Setenv("APP_ENV", "local")
			_ = os.Setenv("DATABASE_URL", "postgres://user:secret@127.0.0.1:55432/heartbeat_local?sslmode=disable")
			_ = os.Setenv("HTTP_ADDR", tt.httpAddr)
			_ = os.Setenv("AUTH_ORIGIN", tt.origin)
			_, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDefaultsAndEnvironmentOverrides(t *testing.T) {
	clearConfigEnv(t)
	setRequiredConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" || cfg.DatabaseTimeout != 5*time.Second || cfg.ReadinessTimeout != 2*time.Second || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.DatabaseURL != "postgres://user:secret@localhost:5432/app" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.AuthOrigin != "http://localhost:3000" || cfg.AuthCookieSecure || cfg.TurnstileSecret != "1x0000000000000000000000000000000AA" || !cfg.TurnstileLocal || cfg.SMTPAddr != "" || cfg.SMTPFrom != "" {
		t.Fatalf("unexpected auth/provider config: %+v", cfg)
	}
	if cfg.AuthMailEncryptionKey != "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" || cfg.TrustedProxyCIDRs != "172.30.0.0/24,fd42:30::/64" {
		t.Fatalf("production mail/proxy config was not loaded: %+v", cfg)
	}
	if cfg.AuthSessionTTL != 0 || cfg.AuthChallengeTTL != 0 || cfg.AuthResetTTL != 0 || cfg.AuthRateWindow != 0 {
		t.Fatalf("unset auth TTLs should remain zero for module defaults: %+v", cfg)
	}

	_ = os.Setenv("HTTP_ADDR", "[::1]:9090")
	_ = os.Setenv("DATABASE_TIMEOUT", "3s")
	_ = os.Setenv("READINESS_TIMEOUT", "1500ms")
	_ = os.Setenv("SHUTDOWN_TIMEOUT", "45s")
	_ = os.Setenv("AUTH_SESSION_TTL", "12h")
	_ = os.Setenv("AUTH_CHALLENGE_TTL", "20m")
	_ = os.Setenv("AUTH_RESET_TTL", "30m")
	_ = os.Setenv("AUTH_RATE_LIMIT_WINDOW", "2h")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() with overrides error = %v", err)
	}
	if cfg.HTTPAddr != "[::1]:9090" || cfg.DatabaseTimeout != 3*time.Second || cfg.ReadinessTimeout != 1500*time.Millisecond || cfg.ShutdownTimeout != 45*time.Second {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if cfg.AuthSessionTTL != 12*time.Hour || cfg.AuthChallengeTTL != 20*time.Minute || cfg.AuthResetTTL != 30*time.Minute || cfg.AuthRateWindow != 2*time.Hour {
		t.Fatalf("auth duration overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValuesWithoutEchoingSecrets(t *testing.T) {
	tests := []struct {
		name string
		set  map[string]string
	}{
		{"missing database URL", map[string]string{"DATABASE_URL": ""}},
		{"missing auth origin", map[string]string{"AUTH_ORIGIN": ""}},
		{"invalid HTTP address", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "HTTP_ADDR": "bad:address:secret"}},
		{"invalid HTTP port", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "HTTP_ADDR": "localhost:70000"}},
		{"invalid database duration", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "DATABASE_TIMEOUT": "secret-duration"}},
		{"zero readiness duration", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "READINESS_TIMEOUT": "0s"}},
		{"too large shutdown duration", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "SHUTDOWN_TIMEOUT": "61s"}},
		{"invalid auth boolean", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "AUTH_COOKIE_SECURE": "sometimes"}},
		{"zero auth session duration", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "AUTH_SESSION_TTL": "0s"}},
		{"too large auth reset duration", map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/db", "AUTH_RESET_TTL": "25h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredConfigEnv(t)
			for key, value := range tt.set {
				_ = os.Setenv(key, value)
			}
			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want invalid configuration error")
			}
			for _, secret := range []string{"secret", "secret-duration"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposes sensitive input %q: %v", secret, err)
				}
			}
		})
	}
}

func TestLoadRequiresValidWebhookEncryptionKey(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
	}{
		{name: "missing"},
		{name: "not base64", key: "not-base64"},
		{name: "wrong decoded length", key: "c2hvcnQ="},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredConfigEnv(t)
			if test.key == "" {
				_ = os.Unsetenv("WEBHOOK_ENCRYPTION_KEY")
			} else {
				_ = os.Setenv("WEBHOOK_ENCRYPTION_KEY", test.key)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WEBHOOK_ENCRYPTION_KEY") {
				t.Fatalf("Load() error = %v, want encryption key validation error", err)
			}
		})
	}

	t.Run("valid 32 byte key", func(t *testing.T) {
		clearConfigEnv(t)
		setRequiredConfigEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() with valid key: %v", err)
		}
		if cfg.WebhookEncryptionKey != "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" {
			t.Fatal("Load() did not preserve configured webhook encryption key")
		}
	})
}

func TestProductionLoadRequiresMailEncryptionAndTrustedProxyConfig(t *testing.T) {
	for _, test := range []struct {
		name, key, cidrs, want string
	}{
		{"missing mail key", "", "172.30.0.0/24", "AUTH_MAIL_ENCRYPTION_KEY"},
		{"malformed mail key", "not-base64", "172.30.0.0/24", "AUTH_MAIL_ENCRYPTION_KEY"},
		{"missing proxy CIDRs", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", "", "TRUSTED_PROXY_CIDRS"},
		{"malformed proxy CIDR", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", "0.0.0.0/0,not-a-prefix", "TRUSTED_PROXY_CIDRS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredConfigEnv(t)
			_ = os.Setenv("APP_ENV", "production")
			if test.key == "" {
				_ = os.Unsetenv("AUTH_MAIL_ENCRYPTION_KEY")
			} else {
				_ = os.Setenv("AUTH_MAIL_ENCRYPTION_KEY", test.key)
			}
			_ = os.Setenv("TRUSTED_PROXY_CIDRS", test.cidrs)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %s validation error", err, test.want)
			}
		})
	}
}
