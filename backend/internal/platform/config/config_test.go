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
	if cfg.AuthOrigin != "http://localhost:3000" || cfg.AuthCookieSecure || cfg.TurnstileSecret != "1x0000000000000000000000000000000AA" || !cfg.TurnstileLocal || cfg.SMTPAddr != "127.0.0.1:51025" {
		t.Fatalf("unexpected auth/provider config: %+v", cfg)
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
