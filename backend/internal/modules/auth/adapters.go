package auth

import (
	"context"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/adapters/smtp"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/adapters/turnstile"
)

type CaptchaVerifier interface {
	Verify(context.Context, string, string, string) error
}

type MailSender interface {
	Send(context.Context, string, string, string) error
}

func NewTurnstile(secret, hostname string, local bool) (CaptchaVerifier, error) {
	return turnstile.New(secret, hostname, local)
}

func NewLocalMailer(addr, from string) (MailSender, error) {
	return smtp.New(addr, from)
}
