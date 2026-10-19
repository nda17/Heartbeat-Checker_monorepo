package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

var ErrConflict = errors.New("conflict")
var ErrMFAEnrollmentRequired = errors.New("mfa enrollment required")
var ErrMFARequired = errors.New("mfa required")
var ErrMFAStepUpRequired = errors.New("mfa step-up required")

type MFAStatus struct {
	Enabled                bool       `json:"enabled"`
	VerifiedUntil          *time.Time `json:"verifiedUntil"`
	StepUpUntil            *time.Time `json:"stepUpUntil"`
	RecoveryCodesRemaining int        `json:"recoveryCodesRemaining"`
}

type MFAEnrollment struct {
	EnrollmentID string    `json:"enrollmentId"`
	Secret       string    `json:"secret"`
	OTPAuthURI   string    `json:"otpauthUri"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type MFAVerification struct {
	Token         string    `json:"-"`
	CSRF          string    `json:"csrfToken"`
	VerifiedUntil time.Time `json:"verifiedUntil"`
	StepUpUntil   time.Time `json:"stepUpUntil"`
}

type MFAConfirmation struct {
	MFAVerification
	RecoveryCodes []string `json:"recoveryCodes"`
}

func (s *Service) MFAStatus(ctx context.Context, p Principal) (MFAStatus, error) {
	if p.Role != "admin" {
		return MFAStatus{}, ErrUnauthorized
	}
	if len(s.cfg.MFAEncryptionKey) != 32 {
		return MFAStatus{}, ErrUnavailable
	}
	count, err := s.repo.MFACodesRemaining(ctx, p.AccountID)
	if err != nil {
		return MFAStatus{}, ErrUnavailable
	}
	return MFAStatus{Enabled: p.MFAEnabled, VerifiedUntil: p.MFAVerifiedUntil, StepUpUntil: p.MFAStepUpUntil, RecoveryCodesRemaining: count}, nil
}

func (s *Service) mfaPassword(ctx context.Context, p Principal, password string, client Client, scope string) (postgres.Credential, error) {
	if p.Role != "admin" {
		return postgres.Credential{}, ErrUnauthorized
	}
	if len(s.cfg.MFAEncryptionKey) != 32 {
		return postgres.Credential{}, ErrUnavailable
	}
	if password == "" || len(password) > 1024 {
		return postgres.Credential{}, ErrInvalid
	}
	if err := s.allow(ctx, scope, p.AccountID, client.IP, 20, 10, 15*time.Minute); err != nil {
		return postgres.Credential{}, err
	}
	c, err := s.repo.CredentialByAccount(ctx, p.AccountID)
	if err != nil {
		return postgres.Credential{}, ErrUnavailable
	}
	matched, err := s.checkPassword(c.PasswordHash, password)
	if err != nil {
		return postgres.Credential{}, err
	}
	if !matched {
		return postgres.Credential{}, ErrCredentials
	}
	return c, nil
}

func (s *Service) StartMFAEnrollment(ctx context.Context, p Principal, password string, client Client) (MFAEnrollment, error) {
	c, err := s.mfaPassword(ctx, p, password, client, "mfa_enroll")
	if err != nil {
		return MFAEnrollment{}, err
	}
	if p.MFAEnabled && (p.MFAStepUpUntil == nil || !p.MFAStepUpUntil.After(time.Now())) {
		return MFAEnrollment{}, ErrMFAStepUpRequired
	}
	version := int64(1)
	f, err := s.repo.MFAFactor(ctx, p.AccountID)
	if err == nil {
		version = f.Version + 1
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return MFAEnrollment{}, ErrUnavailable
	}
	secret, encoded, err := security.NewTOTPSecret()
	if err != nil {
		return MFAEnrollment{}, ErrUnavailable
	}
	cipher, err := security.EncryptMFA(s.cfg.MFAEncryptionKey, p.AccountID, version, secret)
	if err != nil {
		return MFAEnrollment{}, ErrUnavailable
	}
	id, err := security.ID()
	if err != nil {
		return MFAEnrollment{}, ErrUnavailable
	}
	expires := time.Now().Add(10 * time.Minute)
	if err := s.repo.StartMFAEnrollment(ctx, c, p.SessionID, id, cipher, version, expires, s.cfg.MFAAudit); err != nil {
		if errors.Is(err, postgres.ErrConflict) {
			return MFAEnrollment{}, ErrConflict
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return MFAEnrollment{}, ErrUnauthorized
		}
		return MFAEnrollment{}, ErrUnavailable
	}
	uri := "otpauth://totp/" + url.PathEscape("Heartbeat Checker:"+p.Email) + "?secret=" + encoded + "&issuer=" + url.QueryEscape("Heartbeat Checker") + "&algorithm=SHA1&digits=6&period=30"
	return MFAEnrollment{EnrollmentID: id, Secret: encoded, OTPAuthURI: uri, ExpiresAt: expires}, nil
}

func newRecoveryCodes() ([]string, [][]byte, error) {
	codes := make([]string, 10)
	for i := range codes {
		code, err := security.Token()
		if err != nil {
			return nil, nil, err
		}
		codes[i] = code
	}
	return codes, postgres.RecoveryHashes(codes), nil
}

func newMFAVerification() (MFAVerification, error) {
	token, err := security.Token()
	if err != nil {
		return MFAVerification{}, err
	}
	now := time.Now()
	return MFAVerification{Token: token, CSRF: security.CSRF(token), VerifiedUntil: now.Add(12 * time.Hour), StepUpUntil: now.Add(5 * time.Minute)}, nil
}

func (s *Service) ConfirmMFAEnrollment(ctx context.Context, p Principal, enrollmentID, code string, client Client) (MFAConfirmation, error) {
	if p.Role != "admin" || len(s.cfg.MFAEncryptionKey) != 32 {
		return MFAConfirmation{}, ErrUnauthorized
	}
	if !security.ValidID(enrollmentID) {
		return MFAConfirmation{}, ErrInvalid
	}
	if err := s.allow(ctx, "mfa_enroll_confirm", p.AccountID, client.IP, 20, 10, 15*time.Minute); err != nil {
		return MFAConfirmation{}, err
	}
	e, err := s.repo.MFAEnrollment(ctx, p.AccountID, enrollmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MFAConfirmation{}, ErrConflict
	}
	if err != nil {
		return MFAConfirmation{}, ErrUnavailable
	}
	if e.SessionID != p.SessionID || !e.ExpiresAt.After(time.Now()) {
		return MFAConfirmation{}, ErrConflict
	}
	secret, err := security.DecryptMFA(s.cfg.MFAEncryptionKey, p.AccountID, e.FactorVersion, e.Cipher)
	if err != nil {
		return MFAConfirmation{}, ErrUnavailable
	}
	counter, valid := security.TOTP(secret, code, time.Now(), -1)
	if !valid {
		return MFAConfirmation{}, ErrCredentials
	}
	c, err := s.repo.CredentialByAccount(ctx, p.AccountID)
	if err != nil {
		return MFAConfirmation{}, ErrUnavailable
	}
	verification, err := newMFAVerification()
	if err != nil {
		return MFAConfirmation{}, ErrUnavailable
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return MFAConfirmation{}, ErrUnavailable
	}
	if err := s.repo.ConfirmMFAEnrollment(ctx, c, e, p.SessionID, security.Digest(verification.Token), counter, hashes, s.cfg.MFAAudit); err != nil {
		if errors.Is(err, postgres.ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
			return MFAConfirmation{}, ErrConflict
		}
		return MFAConfirmation{}, ErrUnavailable
	}
	return MFAConfirmation{MFAVerification: verification, RecoveryCodes: codes}, nil
}

func (s *Service) verifyTOTP(ctx context.Context, accountID, code string) (postgres.MFAFactor, int64, error) {
	f, err := s.repo.MFAFactor(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, 0, ErrMFAEnrollmentRequired
	}
	if err != nil {
		return f, 0, ErrUnavailable
	}
	secret, err := security.DecryptMFA(s.cfg.MFAEncryptionKey, accountID, f.Version, f.Cipher)
	if err != nil {
		return f, 0, ErrUnavailable
	}
	counter, valid := security.TOTP(secret, code, time.Now(), f.LastCounter)
	if !valid {
		return f, 0, ErrCredentials
	}
	return f, counter, nil
}

func (s *Service) VerifyMFA(ctx context.Context, p Principal, password, code string, client Client) (MFAVerification, error) {
	c, err := s.mfaPassword(ctx, p, password, client, "mfa_verify")
	if err != nil {
		return MFAVerification{}, err
	}
	f, counter, err := s.verifyTOTP(ctx, p.AccountID, code)
	if err != nil {
		return MFAVerification{}, err
	}
	v, err := newMFAVerification()
	if err != nil {
		return MFAVerification{}, ErrUnavailable
	}
	if err := s.repo.VerifyMFA(ctx, c, p.SessionID, f, counter, security.Digest(v.Token), s.cfg.MFAAudit); err != nil {
		if errors.Is(err, postgres.ErrConflict) {
			return MFAVerification{}, ErrCredentials
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return MFAVerification{}, ErrUnauthorized
		}
		return MFAVerification{}, ErrUnavailable
	}
	return v, nil
}

func (s *Service) RecoverMFA(ctx context.Context, p Principal, password, code string, client Client) (MFAVerification, error) {
	c, err := s.mfaPassword(ctx, p, password, client, "mfa_recover")
	if err != nil {
		return MFAVerification{}, err
	}
	if len(code) != 43 || strings.ContainsAny(code, " \t\r\n") {
		return MFAVerification{}, ErrCredentials
	}
	v, err := newMFAVerification()
	if err != nil {
		return MFAVerification{}, ErrUnavailable
	}
	if err := s.repo.RecoverMFA(ctx, c, p.SessionID, security.Digest(code), security.Digest(v.Token), s.cfg.MFAAudit); err != nil {
		if errors.Is(err, postgres.ErrInvalid) || errors.Is(err, postgres.ErrConflict) {
			return MFAVerification{}, ErrCredentials
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return MFAVerification{}, ErrUnauthorized
		}
		return MFAVerification{}, ErrUnavailable
	}
	return v, nil
}

func (s *Service) ReplaceMFARecoveryCodes(ctx context.Context, p Principal, password, code string, client Client) ([]string, error) {
	c, err := s.mfaPassword(ctx, p, password, client, "mfa_codes")
	if err != nil {
		return nil, err
	}
	f, counter, err := s.verifyTOTP(ctx, p.AccountID, code)
	if err != nil {
		return nil, err
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := s.repo.ReplaceRecoveryCodes(ctx, c, p.SessionID, f, counter, hashes, s.cfg.MFAAudit); err != nil {
		if errors.Is(err, postgres.ErrConflict) {
			return nil, ErrCredentials
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnauthorized
		}
		return nil, ErrUnavailable
	}
	return codes, nil
}
