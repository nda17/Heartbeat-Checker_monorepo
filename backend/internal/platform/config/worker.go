package config

import (
	"errors"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type WorkerConfig struct {
	AppEnv            string
	DatabaseURL       string
	DatabaseTimeout   time.Duration
	ProbeURL          string
	ProbeSharedSecret string
}

func LoadWorker() (WorkerConfig, error) {
	cfg := WorkerConfig{AppEnv: os.Getenv("APP_ENV"), DatabaseURL: os.Getenv("DATABASE_URL"), DatabaseTimeout: 5 * time.Second, ProbeURL: os.Getenv("PROBE_URL"), ProbeSharedSecret: os.Getenv("PROBE_SHARED_SECRET")}
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
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/heartbeat_local" || u.Port() != "55432" || u.RawQuery != "sslmode=disable" || u.Fragment != "" || u.Opaque != "" {
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
