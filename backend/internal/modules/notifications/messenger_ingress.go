package notifications

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type messengerTarget struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	ChatID   string `json:"chatId"`
	UserID   string `json:"userId"`
	ThreadID string `json:"threadId,omitempty"`
}

type messengerRecipient struct {
	Target messengerTarget `json:"target"`
	Label  string          `json:"label"`
}

func (r messengerRecipient) publicCandidate() *MessengerCandidate {
	c := &MessengerCandidate{Label: r.Label, ChatID: r.Target.ChatID, UserID: r.Target.UserID}
	if r.Target.ThreadID != "" {
		c.ThreadID = &r.Target.ThreadID
	}
	return c
}

func (m *Messenger) readRecipient(accountID, id, field string, encrypted []byte) (messengerRecipient, error) {
	plain, err := m.open(accountID, id, field, encrypted)
	if err != nil {
		return messengerRecipient{}, err
	}
	var recipient messengerRecipient
	if err = json.Unmarshal(plain, &recipient); err != nil {
		return messengerRecipient{}, err
	}
	if err = recipient.Target.decodeMust(); err != nil || recipient.Label == "" || len([]rune(recipient.Label)) > 120 {
		return messengerRecipient{}, ErrMessengerInvalid
	}
	return recipient, nil
}

func (t messengerTarget) decodeMust() error {
	body, err := json.Marshal(t)
	if err != nil {
		return err
	}
	copy := t
	return copy.decode(body)
}

func (t messengerTarget) threadString() string { return t.ThreadID }
func (t *messengerTarget) decode(data []byte) error {
	if err := json.Unmarshal(data, t); err != nil {
		return err
	}
	if !messengerKind(t.Provider, t.Kind) || t.ChatID == "" || t.UserID == "" {
		return ErrMessengerInvalid
	}
	if _, err := strconv.ParseInt(t.ChatID, 10, 64); err != nil {
		return ErrMessengerInvalid
	}
	if user, err := strconv.ParseInt(t.UserID, 10, 64); err != nil || user <= 0 {
		return ErrMessengerInvalid
	}
	if t.Kind == "topic" {
		thread, err := strconv.ParseInt(t.ThreadID, 10, 64)
		if err != nil || thread <= 0 {
			return ErrMessengerInvalid
		}
	}
	if t.Kind != "topic" && t.ThreadID != "" {
		return ErrMessengerInvalid
	}
	return nil
}

func (m *Messenger) RegisterIngest(mux *http.ServeMux) {
	if !m.cfg.Enabled {
		return
	}
	mux.HandleFunc("POST /webhooks/notifications/telegram", m.ingestHandler("telegram", m.cfg.TelegramWebhookSecret, "X-Telegram-Bot-Api-Secret-Token"))
	mux.HandleFunc("POST /webhooks/notifications/max", m.ingestHandler("max", m.cfg.MAXWebhookSecret, "X-Max-Bot-Api-Secret"))
}

func (m *Messenger) ingestHandler(provider, secret, header string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(secret), []byte(r.Header.Get(header))) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid body", http.StatusRequestEntityTooLarge)
			return
		}
		target, token, err := parseMessengerUpdate(provider, body, m.cfg.TelegramBotUsername)
		if err != nil {
			http.Error(w, "invalid update", http.StatusBadRequest)
			return
		}
		if token == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err = m.observeLink(r.Context(), target, token); err != nil {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func parseMessengerUpdate(provider string, body []byte, username string) (messengerTarget, string, error) {
	if provider == "telegram" {
		var update struct {
			ID      *int64 `json:"update_id"`
			Message *struct {
				Text             string          `json:"text"`
				ThreadID         int64           `json:"message_thread_id"`
				ForwardOrigin    json.RawMessage `json:"forward_origin"`
				ForwardFrom      json.RawMessage `json:"forward_from"`
				ForwardFromChat  json.RawMessage `json:"forward_from_chat"`
				AutomaticForward bool            `json:"is_automatic_forward"`
				Chat             struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
				From struct {
					ID  int64 `json:"id"`
					Bot bool  `json:"is_bot"`
				} `json:"from"`
				SenderChat json.RawMessage `json:"sender_chat"`
			} `json:"message"`
		}
		if json.Unmarshal(body, &update) != nil || update.ID == nil || *update.ID < 0 {
			return messengerTarget{}, "", ErrMessengerInvalid
		}
		if update.Message == nil {
			return messengerTarget{}, "", nil
		}
		msg := update.Message
		if msg.From.ID <= 0 || msg.From.Bot || msg.AutomaticForward || len(msg.ForwardOrigin) > 0 && string(msg.ForwardOrigin) != "null" || len(msg.ForwardFrom) > 0 && string(msg.ForwardFrom) != "null" || len(msg.ForwardFromChat) > 0 && string(msg.ForwardFromChat) != "null" || len(msg.SenderChat) > 0 && string(msg.SenderChat) != "null" {
			return messengerTarget{}, "", nil
		}
		parts := strings.Fields(msg.Text)
		if len(parts) != 2 {
			return messengerTarget{}, "", nil
		}
		if msg.Chat.Type == "private" && msg.Chat.ID == msg.From.ID && parts[0] == "/start" {
			return messengerTarget{Provider: provider, Kind: "private", ChatID: strconv.FormatInt(msg.Chat.ID, 10), UserID: strconv.FormatInt(msg.From.ID, 10)}, parts[1], nil
		}
		if (msg.Chat.Type == "group" || msg.Chat.Type == "supergroup") && strings.EqualFold(parts[0], "/connect@"+username) && msg.Chat.ID < 0 {
			kind := "group"
			thread := ""
			if msg.ThreadID > 0 {
				kind = "topic"
				thread = strconv.FormatInt(msg.ThreadID, 10)
			}
			return messengerTarget{Provider: provider, Kind: kind, ChatID: strconv.FormatInt(msg.Chat.ID, 10), UserID: strconv.FormatInt(msg.From.ID, 10), ThreadID: thread}, parts[1], nil
		}
		return messengerTarget{}, "", nil
	}
	if provider == "max" {
		var update struct {
			Type      string `json:"update_type"`
			Timestamp int64  `json:"timestamp"`
			ChatID    int64  `json:"chat_id"`
			Payload   string `json:"payload"`
			User      *struct {
				ID  int64 `json:"user_id"`
				Bot bool  `json:"is_bot"`
			} `json:"user"`
		}
		if json.Unmarshal(body, &update) != nil || update.Type == "" {
			return messengerTarget{}, "", ErrMessengerInvalid
		}
		if update.Type != "bot_started" {
			return messengerTarget{}, "", nil
		}
		if update.Timestamp <= 0 || update.ChatID <= 0 || update.User == nil || update.User.ID <= 0 {
			return messengerTarget{}, "", ErrMessengerInvalid
		}
		if update.User.Bot {
			return messengerTarget{}, "", nil
		}
		return messengerTarget{Provider: provider, Kind: "private", ChatID: strconv.FormatInt(update.ChatID, 10), UserID: strconv.FormatInt(update.User.ID, 10)}, update.Payload, nil
	}
	return messengerTarget{}, "", ErrMessengerInvalid
}

func (m *Messenger) observeLink(ctx context.Context, target messengerTarget, token string) error {
	if len(token) != 43 || strings.ContainsAny(token, "/+= \r\n") {
		return nil
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, accountID, provider, kind, status string
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,account_id::text,provider,kind,status,expires_at FROM notifications.messenger_links WHERE token_hash=$1 FOR UPDATE`, hash[:]).Scan(&id, &accountID, &provider, &kind, &status, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if provider != target.Provider || kind != target.Kind || !expires.After(time.Now()) || status != "pending" {
		return nil
	}
	body, err := json.Marshal(target)
	if err != nil {
		return err
	}
	encrypted, err := m.seal(accountID, id, "observation", body)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE notifications.messenger_links SET status='observed',encrypted_observation=$2,next_attempt_at=now() WHERE id=$1 AND status='pending'`, id, encrypted)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	return tx.Commit(ctx)
}
