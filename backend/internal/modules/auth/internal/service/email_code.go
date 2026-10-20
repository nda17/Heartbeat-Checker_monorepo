package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	accountcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

const emailCodeTTL = 10 * time.Minute
const emailCodeResendDelay = time.Minute

func codeView(c postgres.EmailCodeChallenge) authcontracts.EmailCodeChallenge {
	return authcontracts.EmailCodeChallenge{ID: c.ID, Email: c.Email, ExpiresAt: c.ExpiresAt, ResendAvailableAt: c.ResendAvailableAt, ServerNow: c.DBNow}
}

func (s *Service) emailCodeHash(id, purpose, code string) []byte {
	derive := hmac.New(sha256.New, s.cfg.MailEncryptionKey)
	derive.Write([]byte("heartbeat-email-code-key-v1"))
	h := hmac.New(sha256.New, derive.Sum(nil))
	h.Write([]byte("heartbeat-email-code-v1\x00" + id + "\x00" + purpose + "\x00" + code))
	return h.Sum(nil)
}

func (s *Service) newEmailCodeMail(c *postgres.EmailCodeChallenge) ([]byte, string, error) {
	if len(s.cfg.MailEncryptionKey) != 32 {
		return nil, "", ErrUnavailable
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return nil, "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	for hmac.Equal(c.CodeHash, s.emailCodeHash(c.ID, c.Purpose, code)) {
		n, err = rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			return nil, "", err
		}
		code = fmt.Sprintf("%06d", n.Int64())
	}
	c.CodeHash = s.emailCodeHash(c.ID, c.Purpose, code)
	jobID, err := security.ID()
	if err != nil {
		return nil, "", err
	}
	title := "Код регистрации"
	intro := "Введите этот код в том же браузере, где начали регистрацию. Аккаунт будет создан после подтверждения."
	if c.Purpose == "notification_email" {
		title = "Код подтверждения email"
		intro = "Введите этот код в настройках уведомлений Heartbeat Checker. Адрес входа в аккаунт не изменится."
	}
	expiry := c.ExpiresAt.UTC().Format("02.01.2006 15:04")
	body := fmt.Sprintf("Heartbeat Checker\n%s\n\n%s\n\n%s\n\nКод действителен до %s (UTC).\nЕсли вы не запрашивали код, просто проигнорируйте письмо.\n\nhttps://heartbeat-checker.ru", title, intro, code, expiry)
	markup := `<!doctype html><html lang="ru"><body style="font-family:Arial,sans-serif;color:#173b3d;background:#f2f7f7;padding:24px"><div style="max-width:560px;margin:auto;background:#fff;padding:32px;border-radius:20px"><p style="color:#087f86;font-weight:bold">Heartbeat Checker</p><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(intro) + `</p><p style="font-size:36px;letter-spacing:8px;font-weight:bold">` + code + `</p><p>Код действителен до ` + expiry + ` (UTC).</p><p>Если вы не запрашивали код, просто проигнорируйте письмо.</p></div></body></html>`
	payload, err := encryptMail(s.cfg.MailEncryptionKey, jobID, "email_code", mailPayload{Recipient: c.Email, Subject: title + " — Heartbeat Checker", Body: body, HTML: markup})
	return payload, jobID, err
}

func codeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrInvalid), errors.Is(err, ErrInvalid):
		return authcontracts.ErrEmailCodeInvalid
	case errors.Is(err, ErrRateLimited):
		return authcontracts.ErrEmailCodeRateLimited
	default:
		return authcontracts.ErrEmailCodeUnavailable
	}
}

func validCode(c postgres.EmailCodeChallenge) bool {
	return c.ConsumedAt == nil && c.ExpiresAt.After(c.DBNow) && c.Attempts < 5
}
func sixDigits(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *Service) registrationEmailBudget(ctx context.Context, email string) error {
	for _, limit := range []struct {
		scope  string
		max    int
		window time.Duration
	}{
		{"register:email", 5, s.cfg.RateLimitWindow}, {"register:email:day", 20, 24 * time.Hour},
	} {
		allowed, err := s.repo.Allow(ctx, limit.scope, email, limit.max, limit.window)
		if err != nil {
			return ErrUnavailable
		}
		if !allowed {
			return ErrRateLimited
		}
	}
	return nil
}
func (s *Service) registrationBudget(ctx context.Context, email, ip string) error {
	if err := s.allow(ctx, "register", "", ip, 20, 0, s.cfg.RateLimitWindow); err != nil {
		return err
	}
	return s.registrationEmailBudget(ctx, email)
}
func registrationError(err error) error {
	if errors.Is(err, ErrInvalid) || errors.Is(err, postgres.ErrInvalid) {
		return ErrInvalid
	}
	return ErrUnavailable
}
func (s *Service) registrationPreflight(ctx context.Context, id, capability, captchaToken, action string, cl Client) (postgres.EmailCodeChallenge, error) {
	capID, browser, ok := registrationCapability(capability)
	if !ok || id != capID {
		return postgres.EmailCodeChallenge{}, ErrInvalid
	}
	scope, window := "register", s.cfg.RateLimitWindow
	if action == "registration_confirm" {
		scope, window = "registration_code_confirm", 15*time.Minute
	}
	if err := s.allow(ctx, scope, "", cl.IP, 20, 0, window); err != nil {
		return postgres.EmailCodeChallenge{}, err
	}
	c, err := s.repo.EmailCode(ctx, id, "registration", "")
	if err != nil {
		return c, registrationError(err)
	}
	if !hmac.Equal(c.BrowserHash, security.Digest(browser)) || !validCode(c) {
		return c, ErrInvalid
	}
	if err = s.captchaCheck(ctx, captchaToken, cl.IP, action); err != nil {
		return c, err
	}
	if action == "registration_resend" {
		if err = s.registrationEmailBudget(ctx, c.Email); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (s *Service) StartRegistration(ctx context.Context, email, name, password, captchaToken string, cl Client) (authcontracts.EmailCodeChallenge, string, error) {
	email = security.NormalizeEmail(email)
	clean, err := accountcontracts.CleanName(name)
	if !security.ValidEmail(email) || err != nil || len(password) < 12 || len(password) > 1024 {
		return authcontracts.EmailCodeChallenge{}, "", ErrInvalid
	}
	if err = s.registrationBudget(ctx, email, cl.IP); err != nil {
		return authcontracts.EmailCodeChallenge{}, "", err
	}
	if err = s.captchaCheck(ctx, captchaToken, cl.IP, "register"); err != nil {
		return authcontracts.EmailCodeChallenge{}, "", err
	}
	defer responseFloor(ctx, time.Now())
	hash, err := s.hashPassword(password)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, "", err
	}
	exists, err := s.repo.EmailExists(ctx, email)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	id, err := security.ID()
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	browser, err := security.Token()
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	tx, err := s.repo.Begin(ctx)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	defer tx.Rollback(ctx)
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	c := postgres.EmailCodeChallenge{ID: id, Purpose: "registration", Email: email, Name: clean, PasswordHash: hash, BrowserHash: security.Digest(browser), Deliverable: !exists, DBNow: now, ExpiresAt: now.Add(emailCodeTTL), ResendAvailableAt: now.Add(emailCodeResendDelay)}
	payload, jobID, err := s.newEmailCodeMail(&c)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	if err = postgres.InsertEmailCodeTx(ctx, tx, c, payload, jobID); err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return authcontracts.EmailCodeChallenge{}, "", ErrUnavailable
	}
	return codeView(c), id + "." + browser, nil
}

func registrationCapability(capability string) (string, string, bool) {
	parts := strings.Split(capability, ".")
	if len(parts) != 2 || !security.ValidID(parts[0]) || len(parts[1]) != 43 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *Service) registrationTx(ctx context.Context, tx pgx.Tx, id, capability string) (postgres.EmailCodeChallenge, error) {
	capID, browser, ok := registrationCapability(capability)
	if !ok || capID != id {
		return postgres.EmailCodeChallenge{}, ErrInvalid
	}
	c, err := postgres.EmailCodeTx(ctx, tx, id, "registration", "")
	if err != nil {
		return c, err
	}
	if !hmac.Equal(c.BrowserHash, security.Digest(browser)) || !validCode(c) {
		return c, ErrInvalid
	}
	return c, nil
}

func (s *Service) Registration(ctx context.Context, capability string) (authcontracts.EmailCodeChallenge, error) {
	id, _, ok := registrationCapability(capability)
	if !ok {
		return authcontracts.EmailCodeChallenge{}, ErrInvalid
	}
	tx, err := s.repo.Begin(ctx)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	c, err := postgres.EmailCodeTx(ctx, tx, id, "registration", "")
	if errors.Is(err, postgres.ErrInvalid) {
		return authcontracts.EmailCodeChallenge{}, ErrInvalid
	}
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, ErrUnavailable
	}
	_, browser, _ := registrationCapability(capability)
	if !hmac.Equal(c.BrowserHash, security.Digest(browser)) {
		return authcontracts.EmailCodeChallenge{}, ErrInvalid
	}
	view := codeView(c)
	view.Status = "pending"
	if c.ConsumedAt != nil {
		view.Status = "completed"
	} else if c.Attempts >= 5 {
		view.Status = "locked"
	} else if !c.ExpiresAt.After(c.DBNow) {
		view.Status = "expired"
	}
	return view, nil
}

func (s *Service) resendCodeTx(ctx context.Context, tx pgx.Tx, c *postgres.EmailCodeChallenge) error {
	if !validCode(*c) {
		return ErrInvalid
	}
	if c.ResendAvailableAt.After(c.DBNow) {
		return ErrRateLimited
	}
	oldHash := c.CodeHash
	payload, jobID, err := s.newEmailCodeMail(c)
	if err != nil {
		return err
	}
	c.ResendAvailableAt = c.DBNow.Add(emailCodeResendDelay)
	if err = postgres.CancelEmailCodeMailTx(ctx, tx, oldHash); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.email_code_challenges SET code_hash=$2,resend_available_at=$3 WHERE id=$1`, c.ID, c.CodeHash, c.ResendAvailableAt); err != nil {
		return err
	}
	return postgres.InsertEmailCodeMailTx(ctx, tx, *c, payload, jobID)
}

func (s *Service) ResendRegistration(ctx context.Context, id, capability, captchaToken string, cl Client) (authcontracts.EmailCodeChallenge, error) {
	if _, err := s.registrationPreflight(ctx, id, capability, captchaToken, "registration_resend", cl); err != nil {
		return authcontracts.EmailCodeChallenge{}, err
	}
	tx, err := s.repo.Begin(ctx)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	c, err := s.registrationTx(ctx, tx, id, capability)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, registrationError(err)
	}
	if err = s.resendCodeTx(ctx, tx, &c); err != nil {
		if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrInvalid) {
			return authcontracts.EmailCodeChallenge{}, err
		}
		return authcontracts.EmailCodeChallenge{}, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return authcontracts.EmailCodeChallenge{}, ErrUnavailable
	}
	return codeView(c), nil
}

func (s *Service) confirmCodeTx(ctx context.Context, tx pgx.Tx, c postgres.EmailCodeChallenge, code string) (bool, error) {
	if !validCode(c) {
		return false, ErrInvalid
	}
	if !sixDigits(code) || !hmac.Equal(c.CodeHash, s.emailCodeHash(c.ID, c.Purpose, code)) || !c.Deliverable {
		_, err := tx.Exec(ctx, `UPDATE auth.email_code_challenges SET failed_attempts=failed_attempts+1 WHERE id=$1`, c.ID)
		if err == nil && c.Attempts == 4 {
			err = postgres.CancelEmailCodeMailTx(ctx, tx, c.CodeHash)
		}
		return false, err
	}
	return true, nil
}

func (s *Service) ConfirmRegistration(ctx context.Context, id, capability, code, captchaToken string, cl Client, previousToken string) (LoginResult, error) {
	if !security.ValidID(id) || len(code) > 64 {
		return LoginResult{}, ErrInvalid
	}
	if _, err := s.registrationPreflight(ctx, id, capability, captchaToken, "registration_confirm", cl); err != nil {
		return LoginResult{}, err
	}
	tx, err := s.repo.Begin(ctx)
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	c, err := s.registrationTx(ctx, tx, id, capability)
	if err != nil {
		return LoginResult{}, registrationError(err)
	}
	matched, err := s.confirmCodeTx(ctx, tx, c, code)
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if !matched {
		if err = tx.Commit(ctx); err != nil {
			return LoginResult{}, ErrUnavailable
		}
		return LoginResult{}, ErrInvalid
	}
	var occupied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.credentials WHERE email=$1)`, c.Email).Scan(&occupied); err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if occupied {
		return LoginResult{}, ErrInvalid
	}
	// The email unique constraint is the final guard against competing proofs,
	// legacy links and OAuth creating the same credential concurrently.
	a, err := s.accounts.Create(ctx, tx, c.Name)
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.credentials(account_id,email,password_hash,email_verified_at) VALUES($1,$2,$3,now())`, a.ID, c.Email, c.PasswordHash); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return LoginResult{}, ErrInvalid
		}
		return LoginResult{}, ErrUnavailable
	}
	token, err := security.Token()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	sessionID, err := security.ID()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	agent := []rune(strings.ToValidUTF8(cl.UserAgent, ""))
	if len(agent) > 500 {
		agent = agent[:500]
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,ip,user_agent) VALUES($1,$2,$3,1,$4,NULLIF($5,'')::inet,$6)`, sessionID, a.ID, security.Digest(token), c.DBNow.Add(s.cfg.SessionTTL), cl.IP, string(agent)); err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.email_code_challenges SET consumed_at=now() WHERE id=$1`, c.ID); err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if err = postgres.CancelEmailCodeMailTx(ctx, tx, c.CodeHash); err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if err = postgres.RevokeReplacedSessionTx(ctx, tx, previousSessionHash(previousToken)); err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return LoginResult{}, ErrUnavailable
	}
	return LoginResult{Account: a, Token: token, CSRF: security.CSRF(token)}, nil
}

func (s *Service) notificationBudget(ctx context.Context, accountID, email, ip string) error {
	for _, limit := range []struct {
		scope, key string
		max        int
		window     time.Duration
	}{
		{"notification_email:ip", ip, 20, time.Hour},
		{"notification_email:account:hour", accountID, 5, time.Hour},
		{"notification_email:account:day", accountID, 20, 24 * time.Hour},
		{"notification_email:target:hour", email, 5, time.Hour},
		{"notification_email:target:day", email, 20, 24 * time.Hour},
	} {
		allowed, err := s.repo.Allow(ctx, limit.scope, limit.key, limit.max, limit.window)
		if err != nil {
			return ErrUnavailable
		}
		if !allowed {
			return ErrRateLimited
		}
	}
	return nil
}

func (s *Service) StartNotificationEmailTx(ctx context.Context, tx pgx.Tx, in authcontracts.NotificationEmailInput, ip string) (authcontracts.EmailCodeChallenge, error) {
	in.Email = security.NormalizeEmail(in.Email)
	if !security.ValidID(in.AccountID) || !security.ValidEmail(in.Email) || (in.DestinationID == nil && in.Version != 0) || (in.DestinationID != nil && (!security.ValidID(*in.DestinationID) || in.Version < 1)) {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeInvalid
	}
	id, err := security.ID()
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeUnavailable
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeUnavailable
	}
	c := postgres.EmailCodeChallenge{ID: id, Purpose: "notification_email", Email: in.Email, AccountID: in.AccountID, DestinationID: in.DestinationID, DestinationVersion: in.Version, Deliverable: true, DBNow: now, ExpiresAt: now.Add(emailCodeTTL), ResendAvailableAt: now.Add(emailCodeResendDelay)}
	payload, jobID, err := s.newEmailCodeMail(&c)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeUnavailable
	}
	// The active-account lock serializes all starts for this owner. Invalidate
	// previous proofs and their mail without changing the current destination.
	if _, err = tx.Exec(ctx, `UPDATE auth.mail_jobs SET status='expired',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error='code_replaced' WHERE purpose='email_code' AND status IN ('pending','sending') AND token_hash IN (SELECT code_hash FROM auth.email_code_challenges WHERE account_id=$1 AND purpose='notification_email' AND consumed_at IS NULL)`, in.AccountID); err != nil {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.email_code_challenges SET consumed_at=now() WHERE account_id=$1 AND purpose='notification_email' AND consumed_at IS NULL`, in.AccountID); err != nil {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeUnavailable
	}
	if err = postgres.InsertEmailCodeTx(ctx, tx, c, payload, jobID); err != nil {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeUnavailable
	}
	return codeView(c), nil
}

func (s *Service) CurrentNotificationEmail(ctx context.Context, accountID string) (authcontracts.EmailCodeChallenge, error) {
	if !security.ValidID(accountID) {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeInvalid
	}
	c, err := s.repo.CurrentNotificationEmail(ctx, accountID)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, codeError(err)
	}
	return codeView(c), nil
}

func (s *Service) ResendNotificationEmailTx(ctx context.Context, tx pgx.Tx, accountID, id, ip string) (authcontracts.EmailCodeChallenge, error) {
	if !security.ValidID(accountID) || !security.ValidID(id) {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeInvalid
	}
	c, err := postgres.EmailCodeTx(ctx, tx, id, "notification_email", accountID)
	if err != nil {
		return authcontracts.EmailCodeChallenge{}, codeError(err)
	}
	if !validCode(c) {
		return authcontracts.EmailCodeChallenge{}, authcontracts.ErrEmailCodeInvalid
	}
	if err = s.resendCodeTx(ctx, tx, &c); err != nil {
		return authcontracts.EmailCodeChallenge{}, codeError(err)
	}
	return codeView(c), nil
}

func (s *Service) ConfirmNotificationEmailTx(ctx context.Context, tx pgx.Tx, accountID, id, code, ip string) (authcontracts.NotificationEmailProof, bool, error) {
	if !security.ValidID(accountID) || !security.ValidID(id) || len(code) > 64 {
		return authcontracts.NotificationEmailProof{}, false, authcontracts.ErrEmailCodeInvalid
	}
	c, err := postgres.EmailCodeTx(ctx, tx, id, "notification_email", accountID)
	if err != nil {
		return authcontracts.NotificationEmailProof{}, false, codeError(err)
	}
	valid, err := s.confirmCodeTx(ctx, tx, c, code)
	if err != nil {
		return authcontracts.NotificationEmailProof{}, false, codeError(err)
	}
	if !valid {
		return authcontracts.NotificationEmailProof{}, false, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.email_code_challenges SET consumed_at=now() WHERE id=$1`, c.ID); err != nil {
		return authcontracts.NotificationEmailProof{}, false, codeError(err)
	}
	if err = postgres.CancelEmailCodeMailTx(ctx, tx, c.CodeHash); err != nil {
		return authcontracts.NotificationEmailProof{}, false, codeError(err)
	}
	return authcontracts.NotificationEmailProof{Email: c.Email, DestinationID: c.DestinationID, Version: c.DestinationVersion}, true, nil
}

func (s *Service) CancelNotificationEmailTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	if _, err := tx.Exec(ctx, `UPDATE auth.mail_jobs SET status='expired',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error='code_replaced' WHERE purpose='email_code' AND status IN ('pending','sending') AND token_hash IN (SELECT code_hash FROM auth.email_code_challenges WHERE account_id=$1 AND purpose='notification_email' AND consumed_at IS NULL)`, accountID); err != nil {
		return authcontracts.ErrEmailCodeUnavailable
	}
	_, err := tx.Exec(ctx, `UPDATE auth.email_code_challenges SET consumed_at=now() WHERE account_id=$1 AND purpose='notification_email' AND consumed_at IS NULL`, accountID)
	return codeError(err)
}

// Preflight methods spend durable budgets before a caller opens its transaction.
// Tx methods never borrow a second pool connection and always recheck proofs.
func (s *Service) PreflightNotificationEmailStart(ctx context.Context, in authcontracts.NotificationEmailInput, ip string) error {
	in.Email = security.NormalizeEmail(in.Email)
	if !security.ValidID(in.AccountID) || !security.ValidEmail(in.Email) || (in.DestinationID == nil && in.Version != 0) || (in.DestinationID != nil && (!security.ValidID(*in.DestinationID) || in.Version < 1)) {
		return authcontracts.ErrEmailCodeInvalid
	}
	return codeError(s.notificationBudget(ctx, in.AccountID, in.Email, ip))
}
func (s *Service) PreflightNotificationEmailResend(ctx context.Context, accountID, id, ip string) error {
	if !security.ValidID(accountID) || !security.ValidID(id) {
		return authcontracts.ErrEmailCodeInvalid
	}
	c, err := s.repo.EmailCode(ctx, id, "notification_email", accountID)
	if err != nil {
		return codeError(err)
	}
	if !validCode(c) {
		return authcontracts.ErrEmailCodeInvalid
	}
	return codeError(s.notificationBudget(ctx, accountID, c.Email, ip))
}
func (s *Service) PreflightNotificationEmailConfirm(ctx context.Context, accountID, id, code, ip string) error {
	if !security.ValidID(accountID) || !security.ValidID(id) || len(code) > 64 {
		return authcontracts.ErrEmailCodeInvalid
	}
	return codeError(s.allow(ctx, "notification_email_confirm", accountID, ip, 20, 20, 15*time.Minute))
}
