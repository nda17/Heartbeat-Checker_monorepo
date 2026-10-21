package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

type NativeOAuthStartInput struct {
	Provider, Mode, State, CodeChallenge, Password, SessionToken string
	Principal                                                    *Principal
	Client                                                       Client
}
type NativeOAuthStartResult struct {
	FlowID           string    `json:"flowId"`
	AuthorizationURL string    `json:"authorizationUrl"`
	ExpiresAt        time.Time `json:"expiresAt"`
}
type NativeOAuthExchangeInput struct {
	FlowID, State, Code, CodeVerifier, DeviceID string
	Client                                      Client
}
type NativeOAuthExchangeResult struct {
	Status            string                 `json:"status"`
	Login             *LoginResult           `json:"-"`
	Registration      *OAuthRegistrationInfo `json:"registration,omitempty"`
	RegistrationToken string                 `json:"registrationToken,omitempty"`
}

func nativeOAuthProof(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return len(value) == 43 && err == nil && len(decoded) == 32
}

func (s *Service) NativeOAuthStart(ctx context.Context, in NativeOAuthStartInput) (NativeOAuthStartResult, error) {
	provider := s.cfg.OAuthProviders[in.Provider]
	if provider == nil {
		return NativeOAuthStartResult{}, ErrOAuthUnavailable
	}
	if (in.Mode != "login" && in.Mode != "link") || !nativeOAuthProof(in.State) || !nativeOAuthProof(in.CodeChallenge) {
		return NativeOAuthStartResult{}, ErrInvalid
	}
	if err := s.allow(ctx, "oauth_start", "", in.Client.IP, 60, 0, 15*time.Minute); err != nil {
		return NativeOAuthStartResult{}, err
	}
	a := postgres.NativeOAuthAttempt{Provider: in.Provider, Mode: in.Mode, StateHash: security.Digest(in.State),
		PKCEChallenge: in.CodeChallenge, ExpiresAt: time.Now().Add(10 * time.Minute)}
	if in.Mode == "login" && in.Principal != nil {
		return NativeOAuthStartResult{}, ErrOAuthLoginRequiresLogout
	}
	var credential postgres.Credential
	var err error
	if in.Mode == "link" {
		if in.Principal == nil || in.Principal.ClientKind != "android" {
			return NativeOAuthStartResult{}, ErrUnauthorized
		}
		if in.Principal.Role == "admin" {
			return NativeOAuthStartResult{}, ErrMFARequired
		}
		if err := s.allow(ctx, "oauth_link", in.Principal.AccountID, in.Client.IP, 30, 10, 15*time.Minute); err != nil {
			return NativeOAuthStartResult{}, err
		}
		credential, err = s.oauthPassword(ctx, *in.Principal, in.Password)
		if err != nil {
			return NativeOAuthStartResult{}, err
		}
		a.AccountID, a.SessionID, a.CredentialVersion = &credential.AccountID, &in.Principal.SessionID, &credential.Version
		a.SessionTokenHash = security.Digest(in.SessionToken)
	}
	a.ID, err = security.ID()
	if err != nil {
		return NativeOAuthStartResult{}, ErrUnavailable
	}
	if err = s.repo.InsertNativeOAuthAttempt(ctx, a, credential, s.accounts.GetForAuthTx); err != nil {
		return NativeOAuthStartResult{}, ErrUnavailable
	}
	return NativeOAuthStartResult{a.ID, provider.AuthorizationURL(in.State, in.CodeChallenge), a.ExpiresAt}, nil
}

func (s *Service) NativeOAuthCallback(ctx context.Context, provider, state string) (string, bool, bool, error) {
	if !nativeOAuthProof(state) || s.cfg.OAuthProviders[provider] == nil {
		return "", false, false, nil
	}
	a, err := s.repo.NativeOAuthAttemptByState(ctx, provider, security.Digest(state))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, ErrUnavailable
	}
	return a.ID, true, a.ConsumedAt == nil && a.ExpiresAt.After(time.Now()), nil
}

func (s *Service) NativeOAuthExchange(ctx context.Context, in NativeOAuthExchangeInput) (NativeOAuthExchangeResult, error) {
	if !security.ValidID(in.FlowID) || !nativeOAuthProof(in.State) || !nativeOAuthProof(in.CodeVerifier) || in.Code == "" || len(in.Code) > 8192 || len(in.DeviceID) > 4096 {
		return NativeOAuthExchangeResult{}, ErrOAuthState
	}
	if err := s.allow(ctx, "native_oauth_exchange", "", in.Client.IP, 30, 0, 15*time.Minute); err != nil {
		return NativeOAuthExchangeResult{}, err
	}
	challengeHash := sha256.Sum256([]byte(in.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])
	a, err := s.repo.ConsumeNativeOAuthAttempt(ctx, in.FlowID, security.Digest(in.State), challenge)
	if errors.Is(err, pgx.ErrNoRows) {
		return NativeOAuthExchangeResult{}, ErrOAuthState
	}
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	provider := s.cfg.OAuthProviders[a.Provider]
	if provider == nil {
		return NativeOAuthExchangeResult{}, ErrOAuthUnavailable
	}
	profile, err := provider.Exchange(ctx, in.Code, in.CodeVerifier, in.DeviceID, in.State)
	if err != nil || len(profile.Subject) == 0 || len(profile.Subject) > 255 {
		return NativeOAuthExchangeResult{}, ErrOAuthUnavailable
	}
	if a.Mode == "link" {
		return s.nativeOAuthLink(ctx, a, profile, in.Client)
	}
	identity, err := s.repo.OAuthIdentity(ctx, a.Provider, profile.Subject)
	if err == nil {
		login, err := s.nativeOAuthLogin(ctx, identity, in.Client)
		if err != nil {
			return NativeOAuthExchangeResult{}, err
		}
		return NativeOAuthExchangeResult{Status: "authenticated", Login: &login}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	return s.nativeOAuthRegistration(ctx, a.Provider, profile)
}

func (s *Service) nativeOAuthLogin(ctx context.Context, identity postgres.OAuthIdentity, cl Client) (LoginResult, error) {
	credential, err := s.repo.CredentialByAccount(ctx, identity.AccountID)
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	account, err := s.accounts.Get(ctx, credential.AccountID)
	if err != nil || account.Status != "active" {
		return LoginResult{}, ErrCredentials
	}
	session, token, err := oauthSession(cl, s.sessionTTL("android"))
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	revoke, err := security.Token()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	session.ClientKind, session.RevokeHash = "android", security.Digest(revoke)
	if err = s.repo.OAuthLoginSession(ctx, identity, credential, session, s.accounts.GetForAuthTx, nil); err != nil {
		return LoginResult{}, ErrCredentials
	}
	return LoginResult{Account: account, Token: token, SessionID: session.ID, ExpiresAt: session.ExpiresAt, RevokeToken: revoke}, nil
}

func (s *Service) nativeOAuthLink(ctx context.Context, a postgres.NativeOAuthAttempt, profile OAuthIdentityProfile, _ Client) (NativeOAuthExchangeResult, error) {
	if a.AccountID == nil || a.SessionID == nil {
		return NativeOAuthExchangeResult{}, ErrOAuthState
	}
	credential, err := s.repo.CredentialByAccount(ctx, *a.AccountID)
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	account, err := s.accounts.Get(ctx, *a.AccountID)
	if err != nil || account.Status != "active" || account.Role == "admin" {
		return NativeOAuthExchangeResult{}, ErrOAuthState
	}
	id, err := security.ID()
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	token, err := security.Token()
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	if err = s.repo.LinkOAuthKind(ctx, a.WebFence(), credential, id, a.Provider, profile.Subject,
		security.Digest(token), s.accounts.GetForAuthTx, s.cfg.OAuthAudit); err != nil {
		if errors.Is(err, postgres.ErrAlreadyExists) {
			return NativeOAuthExchangeResult{}, ErrOAuthIdentityConflict
		}
		return NativeOAuthExchangeResult{}, ErrOAuthState
	}
	// Linking rotates the exact session in place, retaining its bounded expiry.
	principal, err := s.AuthenticateNative(ctx, token)
	if err != nil || principal.SessionID != *a.SessionID {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	stored, err := s.repo.SessionByToken(ctx, security.Digest(token))
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	login := LoginResult{Account: account, Token: token, SessionID: stored.ID, ExpiresAt: stored.ExpiresAt}
	return NativeOAuthExchangeResult{Status: "authenticated", Login: &login}, nil
}

func (s *Service) nativeOAuthRegistration(ctx context.Context, provider string, profile OAuthIdentityProfile) (NativeOAuthExchangeResult, error) {
	email := security.NormalizeEmail(profile.Email)
	if !security.ValidEmail(email) {
		return NativeOAuthExchangeResult{}, ErrOAuthEmailRequired
	}
	exists, err := s.repo.EmailExists(ctx, email)
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	if exists {
		return NativeOAuthExchangeResult{}, ErrOAuthEmailConflict
	}
	name, err := contracts.CleanName(profile.Name)
	if err != nil {
		name = "Пользователь"
	}
	browser, err := security.Token()
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	token, err := security.Token()
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	id, err := security.ID()
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	registration := postgres.OAuthRegistration{ID: id, Provider: provider, Subject: profile.Subject, Email: email,
		Name: name, ReturnTo: "/android/auth/complete", BrowserHash: security.Digest(browser),
		TokenHash: security.Digest(token), ExpiresAt: time.Now().Add(s.cfg.ChallengeTTL)}
	link := s.cfg.Origin + "/android/auth/verify?oauth=1#token=" + token
	providerName := "Яндекс"
	if provider == "vk" {
		providerName = "VK"
	}
	subject, content, err := RenderAuthMail("oauth_registration", AuthMailData{Name: name, ActionURL: link, ExpiresAt: registration.ExpiresAt, Provider: providerName})
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	jobID, err := security.ID()
	if err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	var payload []byte
	if len(s.cfg.MailEncryptionKey) > 0 {
		payload, err = encryptMail(s.cfg.MailEncryptionKey, jobID, "registration", mailPayload{Recipient: email, Subject: subject, Body: content.Text, HTML: content.HTML})
		if err != nil {
			return NativeOAuthExchangeResult{}, ErrUnavailable
		}
	}
	if err = s.repo.InsertOAuthRegistration(ctx, registration, payload, jobID); err != nil {
		return NativeOAuthExchangeResult{}, ErrUnavailable
	}
	if len(payload) == 0 {
		if err = s.mailer.Send(ctx, email, subject, content); err != nil {
			return NativeOAuthExchangeResult{}, ErrUnavailable
		}
	}
	info := OAuthRegistrationInfo{Provider: provider, Email: email, Name: name, ExpiresAt: registration.ExpiresAt}
	return NativeOAuthExchangeResult{Status: "email_verification_required", Registration: &info, RegistrationToken: browser}, nil
}

func (s *Service) NativeOAuthUnlink(ctx context.Context, p Principal, raw, provider, identityID, password string, cl Client) (LoginResult, error) {
	if p.ClientKind != "android" || p.Role == "admin" {
		return LoginResult{}, ErrMFARequired
	}
	if (provider != "vk" && provider != "yandex") || !security.ValidID(identityID) {
		return LoginResult{}, ErrInvalid
	}
	if err := s.allow(ctx, "oauth_unlink", p.AccountID, cl.IP, 30, 10, 15*time.Minute); err != nil {
		return LoginResult{}, err
	}
	credential, err := s.oauthPassword(ctx, p, password)
	if err != nil {
		return LoginResult{}, err
	}
	account, err := s.accounts.Get(ctx, p.AccountID)
	if err != nil || account.Status != "active" {
		return LoginResult{}, ErrUnauthorized
	}
	a := postgres.OAuthAttempt{AccountID: &p.AccountID, SessionID: &p.SessionID,
		CredentialVersion: &credential.Version, SessionTokenHash: security.Digest(raw)}
	token, err := security.Token()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if err = s.repo.UnlinkOAuthKind(ctx, a, credential, provider, identityID, security.Digest(token),
		s.accounts.GetForAuthTx, s.cfg.OAuthAudit); err != nil {
		if errors.Is(err, postgres.ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
			return LoginResult{}, ErrConflict
		}
		return LoginResult{}, ErrUnavailable
	}
	session, err := s.repo.SessionByToken(ctx, security.Digest(token))
	if err != nil || session.ClientKind != "android" {
		return LoginResult{}, ErrUnavailable
	}
	return LoginResult{Account: account, Token: token, SessionID: session.ID,
		ExpiresAt: session.ExpiresAt}, nil
}
