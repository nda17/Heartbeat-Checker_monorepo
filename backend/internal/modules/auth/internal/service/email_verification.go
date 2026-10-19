package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

func (s *Service) RequestEmailVerification(ctx context.Context, p Principal, client Client) error {
	if err := s.allow(ctx, "email_verification_request", p.AccountID, client.IP, 20, 5, time.Hour); err != nil {
		return err
	}
	c, err := s.repo.CredentialByAccount(ctx, p.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	if c.EmailVerified {
		return nil
	}
	if !security.ValidEmail(c.Email) || strings.HasSuffix(c.Email, "@invalid.local") {
		return ErrInvalid
	}
	token, err := security.Token()
	if err != nil {
		return ErrUnavailable
	}
	expires := time.Now().Add(30 * time.Minute)
	subject, content, err := RenderAuthMail("email_verification", AuthMailData{ActionURL: s.cfg.Origin + "/app/settings#verify-email=" + token, ExpiresAt: expires})
	if err != nil {
		return ErrUnavailable
	}
	jobID, err := security.ID()
	if err != nil {
		return ErrUnavailable
	}
	var payload []byte
	if len(s.cfg.MailEncryptionKey) > 0 {
		payload, err = encryptMail(s.cfg.MailEncryptionKey, jobID, "email_verification", mailPayload{Recipient: c.Email, Subject: subject, Body: content.Text, HTML: content.HTML})
		if err != nil {
			return ErrUnavailable
		}
	}
	created, err := s.repo.InsertEmailVerification(ctx, p.AccountID, c.Email, security.Digest(token), expires, payload, jobID)
	if errors.Is(err, postgres.ErrVerificationRateLimited) {
		return ErrRateLimited
	}
	if errors.Is(err, postgres.ErrInvalid) {
		return ErrInvalid
	}
	if err != nil {
		return ErrUnavailable
	}
	if created && len(payload) == 0 {
		if s.mailer == nil || s.mailer.Send(ctx, c.Email, subject, content) != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func (s *Service) ConfirmEmailVerification(ctx context.Context, p Principal, token string, client Client) error {
	if len(token) != 43 {
		return ErrInvalid
	}
	if err := s.allow(ctx, "email_verification_confirm", p.AccountID, client.IP, 20, 10, 15*time.Minute); err != nil {
		return err
	}
	err := s.repo.ConfirmEmailVerification(ctx, p.AccountID, security.Digest(token))
	if errors.Is(err, postgres.ErrInvalid) || errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
