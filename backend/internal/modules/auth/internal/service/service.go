package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/adapters/turnstile"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

var ErrInvalid = errors.New("invalid input or token")
var ErrCredentials = errors.New("invalid credentials")
var ErrUnauthorized = errors.New("unauthorized")
var ErrNotFound = errors.New("not found")
var ErrRateLimited = errors.New("rate limited")
var ErrChallenge = errors.New("challenge failed")
var ErrUnavailable = errors.New("temporarily unavailable")
var ErrBusy = errors.New("password verification busy")

type Captcha interface {
	Verify(context.Context, string, string, string) error
}
type Mailer interface {
	Send(context.Context, string, string, string) error
}

type Config struct {
	Origin          string
	SessionTTL      time.Duration
	ChallengeTTL    time.Duration
	ResetTTL        time.Duration
	RateLimitWindow time.Duration
}
type Client struct {
	IP        string
	UserAgent string
}
type Principal struct {
	AccountID string
	SessionID string
	Email     string
	Role      string
}
type LoginResult struct {
	Account contracts.Account
	Token   string
	CSRF    string
}
type Profile struct {
	Account contracts.Account `json:"account"`
	Email   string            `json:"email"`
}
type Session struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	Current    bool      `json:"current"`
}

type Service struct {
	repo     *postgres.Repository
	accounts contracts.Service
	captcha  Captcha
	mailer   Mailer
	cfg      Config
	kdfSlots chan struct{}
}

func New(repo *postgres.Repository, accounts contracts.Service, captcha Captcha, mailer Mailer, cfg Config) *Service {
	return &Service{repo: repo, accounts: accounts, captcha: captcha, mailer: mailer, cfg: cfg, kdfSlots: make(chan struct{}, 2)}
}

func (s *Service) passwordWork(work func() (string, bool, error)) (string, bool, error) {
	select {
	case s.kdfSlots <- struct{}{}:
		defer func() { <-s.kdfSlots }()
		return work()
	default:
		return "", false, ErrBusy
	}
}

func (s *Service) hashPassword(password string) (string, error) {
	hash, _, err := s.passwordWork(func() (string, bool, error) {
		h, err := security.HashPassword(password)
		return h, false, err
	})
	return hash, err
}

func (s *Service) checkPassword(hash, password string) (bool, error) {
	_, ok, err := s.passwordWork(func() (string, bool, error) {
		return "", security.CheckPassword(hash, password), nil
	})
	return ok, err
}

func (s *Service) dummyPasswordCheck(password string) error {
	_, _, err := s.passwordWork(func() (string, bool, error) {
		security.DummyPasswordCheck(password)
		return "", false, nil
	})
	return err
}

func (s *Service) allow(ctx context.Context, scope, email, ip string, ipMax, emailMax int, window time.Duration) error {
	allowed, err := s.repo.Allow(ctx, scope+":ip", ip, ipMax, window)
	if err != nil {
		return ErrUnavailable
	}
	if !allowed {
		return ErrRateLimited
	}
	if email != "" {
		allowed, err = s.repo.Allow(ctx, scope+":email", email, emailMax, window)
		if err != nil {
			return ErrUnavailable
		}
		if !allowed {
			return ErrRateLimited
		}
	}
	return nil
}

func (s *Service) captchaCheck(ctx context.Context, token, ip, action string) error {
	if err := s.captcha.Verify(ctx, token, ip, action); err != nil {
		if errors.Is(err, turnstile.ErrUnavailable) {
			return ErrUnavailable
		}
		return ErrChallenge
	}
	return nil
}

func responseFloor(ctx context.Context, start time.Time) {
	remaining := time.Until(start.Add(time.Second))
	if remaining <= 0 {
		return
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (s *Service) Register(ctx context.Context, email, name, password, captchaToken string, client Client) error {
	email = security.NormalizeEmail(email)
	cleanName, nameErr := contracts.CleanName(name)
	if !security.ValidEmail(email) || nameErr != nil || len(password) < 12 || len(password) > 1024 {
		return ErrInvalid
	}
	name = cleanName
	if err := s.allow(ctx, "register", email, client.IP, 20, 5, s.cfg.RateLimitWindow); err != nil {
		return err
	}
	if err := s.captchaCheck(ctx, captchaToken, client.IP, "register"); err != nil {
		return err
	}
	defer responseFloor(ctx, time.Now())
	hash, err := s.hashPassword(password)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return ErrBusy
		}
		return ErrUnavailable
	}
	exists, err := s.repo.EmailExists(ctx, email)
	if err != nil {
		return ErrUnavailable
	}
	if exists {
		return nil
	}
	token, err := security.Token()
	if err != nil {
		return ErrUnavailable
	}
	id, err := security.ID()
	if err != nil {
		return ErrUnavailable
	}
	c := postgres.Challenge{ID: id, Email: email, Name: name, PasswordHash: hash, ExpiresAt: time.Now().Add(s.cfg.ChallengeTTL)}
	if err = s.repo.InsertChallenge(ctx, c, security.Digest(token)); err != nil {
		return ErrUnavailable
	}
	link := s.cfg.Origin + "/verify-email#token=" + token
	if err = s.mailer.Send(ctx, email, "Confirm your Heartbeat Checker email", "Open "+link+" and enter your password to confirm registration."); err != nil {
		slog.Error("registration email delivery failed")
	}
	return nil
}

func (s *Service) Verify(ctx context.Context, token, password string, client Client) error {
	if len(token) != 43 || len(password) < 12 || len(password) > 1024 {
		return ErrInvalid
	}
	if err := s.allow(ctx, "verify", "", client.IP, 20, 0, 15*time.Minute); err != nil {
		return err
	}
	c, err := s.repo.Challenge(ctx, security.Digest(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return ErrUnavailable
	}
	if !c.ExpiresAt.After(time.Now()) || c.Attempts >= 5 {
		return ErrInvalid
	}
	matched, err := s.checkPassword(c.PasswordHash, password)
	if err != nil {
		return err
	}
	err = s.repo.CompleteRegistration(ctx, c, matched, func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
		a, err := s.accounts.Create(ctx, tx, name)
		return a.ID, err
	})
	if errors.Is(err, postgres.ErrInvalid) {
		return ErrInvalid
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) Login(ctx context.Context, email, password, captchaToken string, client Client) (LoginResult, error) {
	email = security.NormalizeEmail(email)
	if !security.ValidEmail(email) || len(password) == 0 || len(password) > 1024 {
		return LoginResult{}, ErrInvalid
	}
	if err := s.allow(ctx, "login", email, client.IP, 100, 10, 15*time.Minute); err != nil {
		return LoginResult{}, err
	}
	if err := s.captchaCheck(ctx, captchaToken, client.IP, "login"); err != nil {
		return LoginResult{}, err
	}
	c, err := s.repo.CredentialByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.dummyPasswordCheck(password); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, ErrCredentials
	}
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	matched, err := s.checkPassword(c.PasswordHash, password)
	if err != nil {
		return LoginResult{}, err
	}
	if !matched {
		return LoginResult{}, ErrCredentials
	}
	// The accounts query is before the auth transaction so a full pool cannot deadlock.
	a, err := s.accounts.Get(ctx, c.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, ErrCredentials
	}
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if a.Status != "active" {
		return LoginResult{}, ErrCredentials
	}
	token, err := security.Token()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	id, err := security.ID()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	agent := []rune(strings.ToValidUTF8(client.UserAgent, ""))
	if len(agent) > 500 {
		agent = agent[:500]
	}
	err = s.repo.LoginSession(ctx, c, id, security.Digest(token), time.Now().Add(s.cfg.SessionTTL), client.IP, string(agent))
	if errors.Is(err, postgres.ErrConflict) {
		return LoginResult{}, ErrCredentials
	}
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	return LoginResult{Account: a, Token: token, CSRF: security.CSRF(token)}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	if len(rawToken) != 43 {
		return Principal{}, ErrUnauthorized
	}
	session, err := s.repo.SessionByToken(ctx, security.Digest(rawToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	a, err := s.accounts.Get(ctx, session.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	if a.Status != "active" {
		return Principal{}, ErrUnauthorized
	}
	if err := s.repo.TouchSession(ctx, session.ID); err != nil {
		return Principal{}, ErrUnavailable
	}
	return Principal{AccountID: session.AccountID, SessionID: session.ID, Email: session.Email, Role: a.Role}, nil
}

func (s *Service) Profile(ctx context.Context, p Principal) (Profile, error) {
	a, err := s.accounts.Get(ctx, p.AccountID)
	if err != nil {
		return Profile{}, ErrUnavailable
	}
	return Profile{Account: a, Email: p.Email}, nil
}

func (s *Service) ChangeName(ctx context.Context, p Principal, name string) (Profile, error) {
	cleanName, err := contracts.CleanName(name)
	if err != nil {
		return Profile{}, ErrInvalid
	}
	a, err := s.accounts.UpdateName(ctx, p.AccountID, cleanName)
	if err != nil {
		return Profile{}, ErrUnavailable
	}
	return Profile{Account: a, Email: p.Email}, nil
}

func (s *Service) Sessions(ctx context.Context, p Principal) ([]Session, error) {
	stored, err := s.repo.Sessions(ctx, p.AccountID)
	if err != nil {
		return nil, ErrUnavailable
	}
	items := make([]Session, 0, len(stored))
	for _, v := range stored {
		items = append(items, Session{ID: v.ID, CreatedAt: v.CreatedAt, LastUsedAt: v.LastUsedAt, ExpiresAt: v.ExpiresAt, IP: v.IP, UserAgent: v.UserAgent, Current: v.ID == p.SessionID})
	}
	return items, nil
}

func (s *Service) RevokeSession(ctx context.Context, p Principal, id string) error {
	if !security.ValidID(id) {
		return ErrInvalid
	}
	changed, err := s.repo.RevokeSession(ctx, p.AccountID, id)
	if err != nil {
		return ErrUnavailable
	}
	if !changed {
		return ErrNotFound
	}
	return nil
}

func (s *Service) RevokeAllSessions(ctx context.Context, p Principal) error {
	if err := s.repo.RevokeAllSessions(ctx, p.AccountID); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) ResetRequest(ctx context.Context, email, captchaToken string, client Client) error {
	email = security.NormalizeEmail(email)
	if !security.ValidEmail(email) {
		return ErrInvalid
	}
	if err := s.allow(ctx, "reset_request", email, client.IP, 20, 5, s.cfg.RateLimitWindow); err != nil {
		return err
	}
	if err := s.captchaCheck(ctx, captchaToken, client.IP, "reset"); err != nil {
		return err
	}
	defer responseFloor(ctx, time.Now())
	c, err := s.repo.CredentialByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	token, err := security.Token()
	if err != nil {
		return ErrUnavailable
	}
	if err = s.repo.InsertReset(ctx, c.AccountID, c.Version, security.Digest(token), time.Now().Add(s.cfg.ResetTTL)); err != nil {
		return ErrUnavailable
	}
	link := s.cfg.Origin + "/reset-password#token=" + token
	if err = s.mailer.Send(ctx, email, "Reset your Heartbeat Checker password", "Open "+link+" to set a new password."); err != nil {
		slog.Error("password reset email delivery failed")
	}
	return nil
}

func (s *Service) ResetConfirm(ctx context.Context, token, newPassword string, client Client) error {
	if len(token) != 43 || len(newPassword) < 12 || len(newPassword) > 1024 {
		return ErrInvalid
	}
	if err := s.allow(ctx, "reset_confirm", "", client.IP, 20, 0, 15*time.Minute); err != nil {
		return err
	}
	accountID, err := s.repo.ResetAccount(ctx, security.Digest(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return ErrUnavailable
	}
	newHash, err := s.hashPassword(newPassword)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return ErrBusy
		}
		return ErrUnavailable
	}
	err = s.repo.CompleteReset(ctx, accountID, security.Digest(token), newHash)
	if errors.Is(err, postgres.ErrInvalid) {
		return ErrInvalid
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) ChangePassword(ctx context.Context, p Principal, current, newPassword string, client Client) error {
	if len(current) == 0 || len(current) > 1024 || len(newPassword) < 12 || len(newPassword) > 1024 {
		return ErrInvalid
	}
	if err := s.allow(ctx, "password_change", p.AccountID, client.IP, 20, 10, 15*time.Minute); err != nil {
		return err
	}
	c, err := s.repo.CredentialByAccount(ctx, p.AccountID)
	if err != nil {
		return ErrUnavailable
	}
	matched, err := s.checkPassword(c.PasswordHash, current)
	if err != nil {
		return err
	}
	if !matched {
		return ErrCredentials
	}
	newHash, err := s.hashPassword(newPassword)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return ErrBusy
		}
		return ErrUnavailable
	}
	err = s.repo.ChangePassword(ctx, c, newHash)
	if errors.Is(err, postgres.ErrConflict) {
		return ErrCredentials
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
