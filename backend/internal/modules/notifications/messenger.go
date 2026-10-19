package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	billingcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

var (
	ErrMessengerInvalid     = errors.New("invalid messenger request")
	ErrMessengerNotFound    = errors.New("messenger resource not found")
	ErrMessengerConflict    = errors.New("messenger version conflict")
	ErrMessengerNotReady    = errors.New("messenger link not ready")
	ErrMessengerUnavailable = errors.New("messenger notifications unavailable")
)

type MessengerConfig struct {
	Enabled            bool
	EmailEnabled       bool
	EmailAddressReader func(context.Context, pgx.Tx, string) (string, error)
	EmailSender        interface {
		SendWithID(context.Context, string, string, authcontracts.MailContent, string) error
	}
	TelegramWebhookSecret string
	MAXWebhookSecret      string
	TelegramToken         string
	MAXToken              string
	TelegramBotUsername   string
	MAXBotURL             string
	TelegramAPIBaseURL    string
	TelegramAPIIP         string
	MAXRootCAFile         string
}

type Messenger struct {
	pool        *pgxpool.Pool
	cfg         MessengerConfig
	box         cipher.AEAD
	hashKey     []byte
	client      *http.Client
	entitlement billingcontracts.Service
}

type MessengerCandidate struct {
	Label    string  `json:"label"`
	ChatID   string  `json:"chatId"`
	UserID   string  `json:"userId"`
	ThreadID *string `json:"threadId"`
}

type MessengerLink struct {
	ID            string              `json:"id"`
	Provider      string              `json:"provider"`
	Kind          string              `json:"kind"`
	Status        string              `json:"status"`
	ExpiresAt     time.Time           `json:"expiresAt"`
	Candidate     *MessengerCandidate `json:"candidate"`
	DestinationID *string             `json:"destinationId"`
}

type MessengerDestination struct {
	ID       string  `json:"id"`
	Provider string  `json:"provider"`
	Kind     string  `json:"kind"`
	Label    string  `json:"label"`
	ChatID   string  `json:"chatId"`
	UserID   string  `json:"userId"`
	ThreadID *string `json:"threadId"`
	Ready    bool    `json:"ready"`
	Version  int64   `json:"version"`
	Email    string  `json:"email,omitempty"`
}

type MessengerRoute struct {
	Provider        string `json:"provider"`
	DestinationID   string `json:"destinationId"`
	Enabled         bool   `json:"enabled"`
	ReminderMinutes int    `json:"reminderMinutes"`
	Version         int64  `json:"version"`
}

type PutMessengerRoute struct {
	DestinationID   string `json:"destinationId"`
	Enabled         bool   `json:"enabled"`
	ReminderMinutes int    `json:"reminderMinutes"`
	Version         int64  `json:"version"`
}

func NewMessenger(pool *pgxpool.Pool, encodedKey string, cfg MessengerConfig, outbound bool, entitlement billingcontracts.Service) (*Messenger, error) {
	m := &Messenger{pool: pool, cfg: cfg, entitlement: entitlement}
	if !cfg.Enabled && !cfg.EmailEnabled {
		return m, nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != 32 || pool == nil {
		return nil, errors.New("messenger notifications require a database and WEBHOOK_ENCRYPTION_KEY")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	m.box, err = cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	m.hashKey = key
	if cfg.EmailEnabled && (cfg.EmailAddressReader == nil || outbound && cfg.EmailSender == nil) {
		return nil, errors.New("email notifications require an account email reader and a worker sender")
	}
	if outbound && entitlement == nil {
		return nil, errors.New("notification worker requires entitlement guard")
	}
	if !cfg.Enabled {
		return m, nil
	}
	if !validMessengerBotName(cfg.TelegramBotUsername) || !validMAXBotURL(cfg.MAXBotURL) {
		return nil, errors.New("messenger public bot links are required")
	}
	if outbound {
		if entitlement == nil {
			return nil, errors.New("messenger worker requires entitlement guard")
		}
		if !strings.Contains(cfg.TelegramToken, ":") || strings.ContainsAny(cfg.TelegramToken, "/\r\n?# ") || cfg.MAXToken == "" || strings.ContainsAny(cfg.MAXToken, "\r\n") {
			return nil, errors.New("messenger bot tokens are required in worker")
		}
		m.client, err = messengerClient(cfg)
		if err != nil {
			return nil, err
		}
	} else if len(cfg.TelegramWebhookSecret) < 32 || len(cfg.MAXWebhookSecret) < 32 {
		return nil, errors.New("messenger webhook secrets are required in API")
	}
	return m, nil
}

func (m *Messenger) enabled() error {
	if !m.cfg.Enabled && !m.cfg.EmailEnabled {
		return ErrMessengerUnavailable
	}
	return nil
}

func (m *Messenger) providerEnabled(provider string) bool {
	return provider == "email" && m.cfg.EmailEnabled || messengerProvider(provider) && m.cfg.Enabled
}

func (m *Messenger) seal(accountID, id, field string, value []byte) ([]byte, error) {
	nonce := make([]byte, m.box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return m.box.Seal(nonce, nonce, value, []byte(fmt.Sprintf("messenger:%s:%s:%s", accountID, id, field))), nil
}

func (m *Messenger) open(accountID, id, field string, value []byte) ([]byte, error) {
	if len(value) < m.box.NonceSize() {
		return nil, errors.New("invalid messenger ciphertext")
	}
	plain, err := m.box.Open(nil, value[:m.box.NonceSize()], value[m.box.NonceSize():], []byte(fmt.Sprintf("messenger:%s:%s:%s", accountID, id, field)))
	if err != nil {
		return nil, errors.New("invalid messenger ciphertext")
	}
	return plain, nil
}

func (m *Messenger) fingerprint(parts ...string) []byte {
	h := hmac.New(sha256.New, m.hashKey)
	h.Write([]byte("messenger-recipient-v1"))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	return h.Sum(nil)
}

func (m *Messenger) recipientFingerprint(target messengerTarget) []byte {
	if target.Provider == "email" {
		return m.fingerprint("email", "email", target.Email)
	}
	if target.Kind == "private" {
		return m.fingerprint(target.Provider, "private", target.UserID)
	}
	return m.fingerprint(target.Provider, target.Kind, target.ChatID, target.ThreadID)
}

func (m *Messenger) sendFingerprint(target messengerTarget) []byte {
	if target.Provider == "email" {
		return m.recipientFingerprint(target)
	}
	if target.Provider == "telegram" && target.Kind != "private" {
		return m.fingerprint("telegram", "chat", target.ChatID)
	}
	return m.fingerprint(target.Provider, "private", target.UserID)
}

func messengerToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256([]byte(base64.RawURLEncoding.EncodeToString(raw)))
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

func messengerValidID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

func messengerProvider(provider string) bool { return provider == "telegram" || provider == "max" }
func validMessengerBotName(value string) bool {
	if len(value) < 5 || len(value) > 32 {
		return false
	}
	for _, c := range value {
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func validMAXBotURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "max.ru" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == u.Path || strings.Contains(name, "/") {
		return false
	}
	return validMessengerBotName(name)
}
func messengerKind(provider, kind string) bool {
	return messengerProvider(provider) && (kind == "private" || provider == "telegram" && (kind == "group" || kind == "topic"))
}

func (m *Messenger) Cleanup(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE notifications.messenger_links SET status='expired',encrypted_observation=NULL,encrypted_candidate=NULL,attempt_token=NULL,lease_until=NULL WHERE (expires_at<=now() AND status IN ('pending','observed','candidate')) OR (status='observed' AND attempts>=8 AND lease_until<=now())`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM notifications.messenger_links WHERE status='expired' AND expires_at<now()-interval '1 day' OR status='confirmed' AND created_at<now()-interval '30 days'`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM notifications.messenger_deliveries WHERE id IN (SELECT id FROM notifications.messenger_deliveries WHERE completed_at<now()-interval '30 days' ORDER BY completed_at LIMIT 500)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM notifications.messenger_reservations WHERE expires_at<now()`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM notifications.messenger_target_rates WHERE next_send_at<now()-interval '1 day'`)
	return err
}
