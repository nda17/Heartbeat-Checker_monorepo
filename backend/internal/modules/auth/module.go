package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
	authhttp "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/transport/http"
)

type Config struct {
	Origin            string
	MailEncryptionKey string
	MFAEncryptionKey  string
	MFAAudit          postgres.MFAAudit
	CookieSecure      bool
	SessionTTL        time.Duration
	ChallengeTTL      time.Duration
	ResetTTL          time.Duration
	RateLimitWindow   time.Duration
}

type Module struct {
	server *authhttp.Server
	repo   *postgres.Repository
}

type BootstrapInput = service.BootstrapInput

var ErrAlreadyExists = postgres.ErrAlreadyExists
var ErrInvalidBootstrap = service.ErrInvalid

// BootstrapLocal is used by the guarded local CLI, never an HTTP handler.
func BootstrapLocal(ctx context.Context, pool *pgxpool.Pool, input BootstrapInput,
	createAccount func(context.Context, pgx.Tx, string) (string, error),
	grant func(context.Context, pgx.Tx, string) error,
) (string, error) {
	if pool == nil {
		return "", ErrInvalidBootstrap
	}
	return service.BootstrapLocal(ctx, postgres.New(pool), input, createAccount, grant)
}

func New(pool *pgxpool.Pool, accounts contracts.Service, captcha CaptchaVerifier, mailer MailSender, cfg Config) (*Module, error) {
	u, err := url.Parse(cfg.Origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.TrimRight(cfg.Origin, "/") != cfg.Origin {
		return nil, errors.New("AUTH_ORIGIN must be an exact http(s) origin")
	}
	if cfg.CookieSecure && u.Scheme != "https" {
		return nil, errors.New("secure cookies require an HTTPS origin")
	}
	if !cfg.CookieSecure && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return nil, errors.New("insecure cookies are allowed only on localhost")
	}
	if pool == nil || accounts == nil || captcha == nil || (mailer == nil && cfg.MailEncryptionKey == "") {
		return nil, errors.New("auth dependencies are required")
	}
	var mailKey []byte
	if cfg.MailEncryptionKey != "" {
		mailKey, err = service.DecodeMailKey(cfg.MailEncryptionKey)
		if err != nil {
			return nil, err
		}
	}
	var mfaKey []byte
	if cfg.MFAEncryptionKey != "" {
		mfaKey, err = service.DecodeMailKey(cfg.MFAEncryptionKey)
		if err != nil {
			return nil, errors.New("AUTH_MFA_ENCRYPTION_KEY must be base64 of 32 bytes")
		}
		if cfg.MFAAudit == nil {
			return nil, errors.New("MFA audit writer is required")
		}
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.ChallengeTTL == 0 {
		cfg.ChallengeTTL = 30 * time.Minute
	}
	if cfg.ResetTTL == 0 {
		cfg.ResetTTL = 30 * time.Minute
	}
	if cfg.RateLimitWindow == 0 {
		cfg.RateLimitWindow = time.Hour
	}
	if cfg.SessionTTL < time.Second || cfg.ChallengeTTL < time.Second || cfg.ResetTTL < time.Second || cfg.RateLimitWindow < time.Second {
		return nil, errors.New("auth durations must be at least one second")
	}
	repo := postgres.New(pool)
	svc := service.New(repo, accounts, captcha, mailer, service.Config{
		Origin:            cfg.Origin,
		MailEncryptionKey: mailKey,
		MFAEncryptionKey:  mfaKey,
		MFAAudit:          cfg.MFAAudit,
		SessionTTL:        cfg.SessionTTL,
		ChallengeTTL:      cfg.ChallengeTTL,
		ResetTTL:          cfg.ResetTTL,
		RateLimitWindow:   cfg.RateLimitWindow,
	})
	return &Module{server: authhttp.New(svc, cfg.Origin, cfg.CookieSecure, cfg.SessionTTL), repo: repo}, nil
}

func (m *Module) Register(mux *http.ServeMux) { m.server.Register(mux) }
func (m *Module) Handler() http.Handler       { return m.server.Handler() }
func (m *Module) RequireRole(role string, next http.Handler) http.Handler {
	return m.server.RequireRole(role, next)
}

func (m *Module) Protect(next http.Handler) http.Handler { return m.server.Protect(next) }

func (m *Module) VerifyAdminTx(ctx context.Context, tx pgx.Tx, p authcontracts.Principal, mutation bool) error {
	return m.repo.VerifyAdminTx(ctx, tx, p, mutation)
}
func (m *Module) RevokeAccountSessionsTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	return m.repo.RevokeAccountSessionsTx(ctx, tx, accountID)
}
func (m *Module) AdminEmail(ctx context.Context, tx pgx.Tx, accountID string) (string, error) {
	return m.repo.AdminEmail(ctx, tx, accountID)
}
func (m *Module) AdminSessionCount(ctx context.Context, tx pgx.Tx, accountID string) (int, error) {
	return m.repo.AdminSessionCount(ctx, tx, accountID)
}

func Cleanup(ctx context.Context, tx pgx.Tx) error { return postgres.Cleanup(ctx, tx) }
