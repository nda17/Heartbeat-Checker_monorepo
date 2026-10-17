package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
	authhttp "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/transport/http"
)

type Config struct {
	Origin          string
	CookieSecure    bool
	SessionTTL      time.Duration
	ChallengeTTL    time.Duration
	ResetTTL        time.Duration
	RateLimitWindow time.Duration
}

type Module struct{ server *authhttp.Server }

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
	if pool == nil || accounts == nil || captcha == nil || mailer == nil {
		return nil, errors.New("auth dependencies are required")
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
	svc := service.New(postgres.New(pool), accounts, captcha, mailer, service.Config{
		Origin:          cfg.Origin,
		SessionTTL:      cfg.SessionTTL,
		ChallengeTTL:    cfg.ChallengeTTL,
		ResetTTL:        cfg.ResetTTL,
		RateLimitWindow: cfg.RateLimitWindow,
	})
	return &Module{server: authhttp.New(svc, cfg.Origin, cfg.CookieSecure, cfg.SessionTTL)}, nil
}

func (m *Module) Register(mux *http.ServeMux) { m.server.Register(mux) }
func (m *Module) Handler() http.Handler       { return m.server.Handler() }
func (m *Module) RequireRole(role string, next http.Handler) http.Handler {
	return m.server.RequireRole(role, next)
}
