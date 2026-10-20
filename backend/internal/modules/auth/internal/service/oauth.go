package service

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

var ErrOAuthCancelled = errors.New("oauth_cancelled")
var ErrOAuthEmailConflict = errors.New("oauth_email_conflict")
var ErrOAuthEmailRequired = errors.New("oauth_email_required")
var ErrOAuthLoginRequiresLogout = errors.New("oauth_login_requires_logout")
var ErrPasswordSetupRequired = errors.New("password_setup_required")
var ErrOAuthUnavailable = errors.New("oauth_provider_unavailable")
var ErrOAuthState = errors.New("oauth_state_invalid")
var ErrOAuthIdentityConflict = errors.New("oauth_identity_conflict")
var ErrOAuthRegistrationExpired = errors.New("oauth_registration_expired")

type OAuthIdentityProfile struct{ Subject, Email, Name string }
type OAuthProvider interface {
	ID() string
	AuthorizationURL(state, challenge string) string
	Exchange(context.Context, string, string, string, string) (OAuthIdentityProfile, error) // code, verifier, deviceID, state
}
type OAuthProviderInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}
type OAuthStartInput struct {
	Provider, Mode, ReturnTo, Password, PreviousCookie, SessionToken string
	Principal                                                        *Principal
	Client                                                           Client
}
type OAuthStartResult struct {
	AuthorizationURL string `json:"authorizationUrl"`
	Cookie           string `json:"-"`
}
type OAuthCallbackInput struct {
	Provider, Code, State, DeviceID, Cookie, SessionToken, ProviderError string
	Client                                                               Client
}
type OAuthCallbackResult struct {
	Consumed                     bool
	Mode                         string
	Login                        *LoginResult
	ReturnTo, RegistrationCookie string
	RetryReturnTo                string
}
type OAuthRegistrationInfo struct {
	Provider  string    `json:"provider"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type OAuthConfirmResult struct {
	Login    LoginResult
	ReturnTo string
}
type OAuthIdentity = postgres.OAuthIdentity

func (s *Service) OAuthProviders() []OAuthProviderInfo {
	out := make([]OAuthProviderInfo, 0, 2)
	for _, p := range []struct{ id, name string }{{"yandex", "Яндекс"}, {"vk", "VK"}} {
		out = append(out, OAuthProviderInfo{p.id, p.name, s.cfg.OAuthProviders[p.id] != nil})
	}
	return out
}
func oauthReturnTo(v string) string {
	switch v {
	case "/app", "/app/admin", "/app/settings":
		return v
	}
	if !strings.HasPrefix(v, "/app?") && !strings.HasPrefix(v, "/app/settings?") {
		return "/app"
	}
	u, err := url.Parse(v)
	if err != nil || u.Host != "" || u.Scheme != "" || u.Fragment != "" {
		return "/app"
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "/app"
	}
	if u.Path == "/app/settings" {
		if len(q) != 1 || len(q["pushPair"]) != 1 || !security.ValidID(strings.ToLower(q.Get("pushPair"))) {
			return "/app"
		}
		return "/app/settings?pushPair=" + url.QueryEscape(q.Get("pushPair"))
	}
	if u.Path != "/app" || len(q) != 2 || len(q["monitor"]) != 1 || len(q["incident"]) != 1 {
		return "/app"
	}
	monitor, incident := q.Get("monitor"), q.Get("incident")
	if !security.ValidID(strings.ToLower(monitor)) || !security.ValidID(strings.ToLower(incident)) {
		return "/app"
	}
	return "/app?monitor=" + url.QueryEscape(monitor) + "&incident=" + url.QueryEscape(incident)
}
func parseOAuthCookie(v string) (string, string, bool) {
	a, b, ok := strings.Cut(v, ".")
	return a, b, ok && len(a) == 43 && len(b) == 43 && !strings.Contains(b, ".")
}
func (s *Service) OAuthStart(ctx context.Context, in OAuthStartInput) (OAuthStartResult, error) {
	p := s.cfg.OAuthProviders[in.Provider]
	if p == nil {
		return OAuthStartResult{}, ErrOAuthUnavailable
	}
	if in.Mode != "login" && in.Mode != "link" {
		return OAuthStartResult{}, ErrInvalid
	}
	if err := s.allow(ctx, "oauth_start", "", in.Client.IP, 60, 0, 15*time.Minute); err != nil {
		return OAuthStartResult{}, err
	}
	if in.Mode == "login" && in.Principal != nil {
		return OAuthStartResult{}, ErrOAuthLoginRequiresLogout
	}
	var c postgres.Credential
	var err error
	a := postgres.OAuthAttempt{Provider: in.Provider, Mode: in.Mode, ReturnTo: oauthReturnTo(in.ReturnTo), ExpiresAt: time.Now().Add(10 * time.Minute)}
	if in.Mode == "link" {
		if in.Principal == nil {
			return OAuthStartResult{}, ErrUnauthorized
		}
		if err := s.allow(ctx, "oauth_link", in.Principal.AccountID, in.Client.IP, 30, 10, 15*time.Minute); err != nil {
			return OAuthStartResult{}, err
		}
		c, err = s.oauthPassword(ctx, *in.Principal, in.Password)
		if err != nil {
			return OAuthStartResult{}, err
		}
		a.ReturnTo = "/app/settings"
		a.AccountID = &c.AccountID
		a.SessionID = &in.Principal.SessionID
		a.CredentialVersion = &c.Version
		a.SessionTokenHash = security.Digest(in.SessionToken)
	}
	state, err := security.Token()
	if err != nil {
		return OAuthStartResult{}, ErrUnavailable
	}
	verifier, err := security.Token()
	if err != nil {
		return OAuthStartResult{}, ErrUnavailable
	}
	a.StateHash = security.Digest(state)
	a.PKCEChallenge = security.Digest(verifier)
	var previous []byte
	if old, _, ok := parseOAuthCookie(in.PreviousCookie); ok {
		previous = security.Digest(old)
	}
	if err = s.repo.InsertOAuthAttempt(ctx, a, previous, c, s.accounts.GetForAuthTx); err != nil {
		return OAuthStartResult{}, ErrUnavailable
	}
	return OAuthStartResult{p.AuthorizationURL(state, base64.RawURLEncoding.EncodeToString(a.PKCEChallenge)), state + "." + verifier}, nil
}
func (s *Service) oauthPassword(ctx context.Context, p Principal, password string) (postgres.Credential, error) {
	if p.Role == "admin" {
		if !p.MFAEnabled {
			return postgres.Credential{}, ErrMFAEnrollmentRequired
		}
		if p.MFAVerifiedUntil == nil || !p.MFAVerifiedUntil.After(time.Now()) {
			return postgres.Credential{}, ErrMFARequired
		}
		if p.MFAStepUpUntil == nil || !p.MFAStepUpUntil.After(time.Now()) {
			return postgres.Credential{}, ErrMFAStepUpRequired
		}
	}
	c, err := s.repo.CredentialByAccount(ctx, p.AccountID)
	if err != nil {
		return c, ErrUnavailable
	}
	if c.PasswordHash == "" {
		return c, ErrPasswordSetupRequired
	}
	if len(password) == 0 || len(password) > 1024 {
		return c, ErrCredentials
	}
	ok, err := s.checkPassword(c.PasswordHash, password)
	if err != nil {
		return c, err
	}
	if !ok {
		return c, ErrCredentials
	}
	return c, nil
}
func oauthSession(client Client, ttl time.Duration) (postgres.OAuthSession, string, error) {
	token, err := security.Token()
	if err != nil {
		return postgres.OAuthSession{}, "", err
	}
	id, err := security.ID()
	if err != nil {
		return postgres.OAuthSession{}, "", err
	}
	agent := []rune(strings.ToValidUTF8(client.UserAgent, ""))
	if len(agent) > 500 {
		agent = agent[:500]
	}
	return postgres.OAuthSession{ID: id, Hash: security.Digest(token), IP: client.IP, Agent: string(agent), ExpiresAt: time.Now().Add(ttl)}, token, nil
}
func (s *Service) OAuthCallback(ctx context.Context, in OAuthCallbackInput) (out OAuthCallbackResult, callbackErr error) {
	p := s.cfg.OAuthProviders[in.Provider]
	if p == nil {
		return OAuthCallbackResult{}, ErrOAuthUnavailable
	}
	state, verifier, ok := parseOAuthCookie(in.Cookie)
	if !ok || state != in.State {
		return OAuthCallbackResult{}, ErrOAuthState
	}
	a, err := s.repo.ConsumeOAuthAttempt(ctx, in.Provider, security.Digest(state), security.Digest(verifier))
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthCallbackResult{}, ErrOAuthState
	}
	if err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	// Only a successfully consumed database attempt may clear the browser cookie.
	defer func() {
		out.Consumed = true
		out.Mode = a.Mode
		if callbackErr != nil && a.Mode == "login" {
			out.RetryReturnTo = oauthReturnTo(a.ReturnTo)
		}
	}()
	if in.ProviderError != "" {
		if in.ProviderError == "access_denied" {
			return OAuthCallbackResult{}, ErrOAuthCancelled
		}
		return OAuthCallbackResult{}, ErrOAuthUnavailable
	}
	profile, err := p.Exchange(ctx, in.Code, verifier, in.DeviceID, state)
	if err != nil {
		return OAuthCallbackResult{}, ErrOAuthUnavailable
	}
	if len(profile.Subject) == 0 || len(profile.Subject) > 255 {
		return OAuthCallbackResult{}, ErrOAuthUnavailable
	}
	if a.Mode == "link" {
		if a.AccountID == nil || a.SessionID == nil {
			return OAuthCallbackResult{}, ErrOAuthState
		}
		current, err := s.Authenticate(ctx, in.SessionToken)
		if err != nil || current.AccountID != *a.AccountID || current.SessionID != *a.SessionID {
			return OAuthCallbackResult{}, ErrOAuthState
		}
		c, err := s.repo.CredentialByAccount(ctx, current.AccountID)
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		// MFA freshness is rechecked after the external round trip, without asking
		// for the password again. Its credential version is fenced in the transaction.
		if err = s.oauthAdminFresh(current); err != nil {
			return OAuthCallbackResult{}, err
		}
		id, err := security.ID()
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		newToken, err := security.Token()
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		err = s.repo.LinkOAuth(ctx, a, c, id, in.Provider, profile.Subject, security.Digest(newToken), s.accounts.GetForAuthTx, s.cfg.OAuthAudit)
		if errors.Is(err, postgres.ErrAlreadyExists) {
			return OAuthCallbackResult{}, ErrOAuthIdentityConflict
		}
		if errors.Is(err, postgres.ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
			return OAuthCallbackResult{}, ErrOAuthState
		}
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		account, err := s.accounts.Get(ctx, c.AccountID)
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		login := LoginResult{Account: account, Token: newToken, CSRF: security.CSRF(newToken)}
		return OAuthCallbackResult{ReturnTo: "/app/settings?oauth=linked&provider=" + url.QueryEscape(in.Provider), Login: &login}, nil
	}
	if in.SessionToken != "" {
		if _, err = s.Authenticate(ctx, in.SessionToken); err == nil {
			return OAuthCallbackResult{}, ErrOAuthLoginRequiresLogout
		} else if !errors.Is(err, ErrUnauthorized) {
			return OAuthCallbackResult{}, err
		}
	}
	identity, err := s.repo.OAuthIdentity(ctx, in.Provider, profile.Subject)
	if err == nil {
		c, err := s.repo.CredentialByAccount(ctx, identity.AccountID)
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		account, err := s.accounts.Get(ctx, c.AccountID)
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		if account.Status != "active" {
			return OAuthCallbackResult{}, ErrCredentials
		}
		session, token, err := oauthSession(in.Client, s.cfg.SessionTTL)
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
		if err = s.repo.OAuthLoginSession(ctx, identity, c, session, s.accounts.GetForAuthTx, previousSessionHash(in.SessionToken)); err != nil {
			return OAuthCallbackResult{}, ErrCredentials
		}
		login := LoginResult{Account: account, Token: token, CSRF: security.CSRF(token)}
		return OAuthCallbackResult{Login: &login, ReturnTo: a.ReturnTo}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	email := security.NormalizeEmail(profile.Email)
	if !security.ValidEmail(email) {
		return OAuthCallbackResult{}, ErrOAuthEmailRequired
	}
	exists, err := s.repo.EmailExists(ctx, email)
	if err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	if exists {
		return OAuthCallbackResult{}, ErrOAuthEmailConflict
	}
	name, err := contracts.CleanName(profile.Name)
	if err != nil {
		name = "Пользователь"
	}
	browser, err := security.Token()
	if err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	token, err := security.Token()
	if err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	id, err := security.ID()
	if err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	c := postgres.OAuthRegistration{ID: id, Provider: in.Provider, Subject: profile.Subject, Email: email, Name: name, ReturnTo: a.ReturnTo, BrowserHash: security.Digest(browser), TokenHash: security.Digest(token), ExpiresAt: time.Now().Add(s.cfg.ChallengeTTL)}
	link := s.cfg.Origin + "/verify-email?oauth=1#token=" + token
	providerName := "Яндекс"
	if in.Provider == "vk" {
		providerName = "VK"
	}
	subject, content, err := RenderAuthMail("oauth_registration", AuthMailData{Name: name, ActionURL: link, ExpiresAt: c.ExpiresAt, Provider: providerName})
	if err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	jobID, err := security.ID()
	if err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	var payload []byte
	if len(s.cfg.MailEncryptionKey) > 0 {
		payload, err = encryptMail(s.cfg.MailEncryptionKey, jobID, "registration", mailPayload{Recipient: email, Subject: subject, Body: content.Text, HTML: content.HTML})
		if err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
	}
	if err = s.repo.InsertOAuthRegistration(ctx, c, payload, jobID); err != nil {
		return OAuthCallbackResult{}, ErrUnavailable
	}
	if len(payload) == 0 {
		if err = s.mailer.Send(ctx, email, subject, content); err != nil {
			return OAuthCallbackResult{}, ErrUnavailable
		}
	}
	return OAuthCallbackResult{ReturnTo: "/verify-email?oauth=1", RegistrationCookie: browser}, nil
}
func (s *Service) oauthAdminFresh(p Principal) error {
	if p.Role != "admin" {
		return nil
	}
	if !p.MFAEnabled {
		return ErrMFAEnrollmentRequired
	}
	if p.MFAVerifiedUntil == nil || !p.MFAVerifiedUntil.After(time.Now()) {
		return ErrMFARequired
	}
	if p.MFAStepUpUntil == nil || !p.MFAStepUpUntil.After(time.Now()) {
		return ErrMFAStepUpRequired
	}
	return nil
}
func (s *Service) OAuthRegistration(ctx context.Context, browser string) (OAuthRegistrationInfo, error) {
	if len(browser) != 43 {
		return OAuthRegistrationInfo{}, ErrOAuthRegistrationExpired
	}
	c, err := s.repo.OAuthRegistration(ctx, security.Digest(browser))
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthRegistrationInfo{}, ErrOAuthRegistrationExpired
	}
	if err != nil {
		return OAuthRegistrationInfo{}, ErrUnavailable
	}
	return OAuthRegistrationInfo{c.Provider, c.Email, c.Name, c.ExpiresAt}, nil
}
func (s *Service) OAuthConfirm(ctx context.Context, browser, token string, client Client, previousToken string) (OAuthConfirmResult, error) {
	if len(browser) != 43 || len(token) != 43 {
		return OAuthConfirmResult{}, ErrOAuthRegistrationExpired
	}
	if err := s.allow(ctx, "oauth_confirm", "", client.IP, 30, 0, 15*time.Minute); err != nil {
		return OAuthConfirmResult{}, err
	}
	session, raw, err := oauthSession(client, s.cfg.SessionTTL)
	if err != nil {
		return OAuthConfirmResult{}, ErrUnavailable
	}
	id, err := security.ID()
	if err != nil {
		return OAuthConfirmResult{}, ErrUnavailable
	}
	accountID, returnTo, err := s.repo.CompleteOAuthRegistration(ctx, security.Digest(token), security.Digest(browser), id, session, func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
		a, err := s.accounts.Create(ctx, tx, name)
		return a.ID, err
	}, s.cfg.OAuthAudit, previousSessionHash(previousToken))
	if errors.Is(err, postgres.ErrAlreadyExists) {
		return OAuthConfirmResult{}, ErrOAuthEmailConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthConfirmResult{}, ErrOAuthRegistrationExpired
	}
	if err != nil {
		return OAuthConfirmResult{}, ErrUnavailable
	}
	account, err := s.accounts.Get(ctx, accountID)
	if err != nil {
		return OAuthConfirmResult{}, ErrUnavailable
	}
	return OAuthConfirmResult{LoginResult{Account: account, Token: raw, CSRF: security.CSRF(raw)}, returnTo}, nil
}
func (s *Service) OAuthIdentities(ctx context.Context, p Principal) ([]OAuthIdentity, error) {
	v, err := s.repo.OAuthIdentities(ctx, p.AccountID)
	if err != nil {
		return nil, ErrUnavailable
	}
	return v, nil
}
func (s *Service) OAuthUnlink(ctx context.Context, p Principal, raw, provider, id, password string, client Client) (string, error) {
	if (provider != "vk" && provider != "yandex") || !security.ValidID(id) {
		return "", ErrInvalid
	}
	if err := s.allow(ctx, "oauth_unlink", p.AccountID, client.IP, 30, 10, 15*time.Minute); err != nil {
		return "", err
	}
	c, err := s.oauthPassword(ctx, p, password)
	if err != nil {
		return "", err
	}
	a := postgres.OAuthAttempt{AccountID: &p.AccountID, SessionID: &p.SessionID, CredentialVersion: &c.Version, SessionTokenHash: security.Digest(raw)}
	newToken, err := security.Token()
	if err != nil {
		return "", ErrUnavailable
	}
	err = s.repo.UnlinkOAuth(ctx, a, c, provider, id, security.Digest(newToken), s.accounts.GetForAuthTx, s.cfg.OAuthAudit)
	if errors.Is(err, postgres.ErrConflict) {
		return "", ErrConflict
	}
	if err != nil {
		return "", ErrUnavailable
	}
	return newToken, nil
}
