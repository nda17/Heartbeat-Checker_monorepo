package support

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

var errRejected = errors.New("support event rejected")
var errMissingRoute = errors.New("support reply route pending")
var errMAXUnconfigured = errors.New("MAX support group not configured")

const unsupportedNotice = "Сообщение не отправлено: этот тип вложения или длина текста пока не поддерживается. Отправьте текст до 4000 символов либо изображение, аудио, видео или документ отдельным сообщением."

type route struct {
	Provider       string
	ChatID, UserID int64
	MessageID      string
}

func (m *Module) authorized(ctx context.Context, provider string, operatorID int64) error {
	if provider == "max" {
		return m.maxAuthorized(ctx, operatorID)
	}
	var self struct {
		ID  int64 `json:"id"`
		Bot bool  `json:"is_bot"`
	}
	if err := m.telegram(ctx, "getMe", map[string]any{}, &self); err != nil {
		return err
	}
	if self.ID <= 0 || !self.Bot {
		return errRejected
	}
	var bot struct {
		Status string `json:"status"`
	}
	if err := m.telegram(ctx, "getChatMember", map[string]any{"chat_id": m.cfg.InternalChatID, "user_id": self.ID}, &bot); err != nil {
		return err
	}
	if bot.Status != "administrator" && bot.Status != "creator" {
		return errRejected
	}
	if operatorID == 0 {
		return nil
	}
	var operator struct {
		Status string `json:"status"`
		User   struct {
			ID  int64 `json:"id"`
			Bot bool  `json:"is_bot"`
		} `json:"user"`
	}
	if err := m.telegram(ctx, "getChatMember", map[string]any{"chat_id": m.cfg.InternalChatID, "user_id": operatorID}, &operator); err != nil {
		return err
	}
	if operator.User.ID != operatorID || operator.User.Bot || (operator.Status != "administrator" && operator.Status != "creator") {
		return errRejected
	}
	return nil
}

type maxMember struct {
	ID    int64 `json:"user_id"`
	Bot   bool  `json:"is_bot"`
	Admin bool  `json:"is_admin"`
	Owner bool  `json:"is_owner"`
}

func (m *Module) maxAuthorized(ctx context.Context, operatorID int64) error {
	if m.cfg.MAXSupportChatID == 0 {
		return errMAXUnconfigured
	}
	base := "https://platform-api2.max.ru/chats/" + strconv.FormatInt(m.cfg.MAXSupportChatID, 10)
	var self maxMember
	if err := m.request(ctx, http.MethodGet, base+"/members/me", m.cfg.MAXToken, "", nil, &self); err != nil {
		return err
	}
	if self.ID <= 0 || !self.Bot || (!self.Admin && !self.Owner) {
		return errRejected
	}
	if operatorID == 0 {
		return nil
	}
	var marker *int64
	seen := map[int64]bool{}
	for page := 0; page < 100; page++ {
		endpoint := base + "/members/admins"
		if marker != nil {
			endpoint += "?marker=" + strconv.FormatInt(*marker, 10)
		}
		var admins struct {
			Members []maxMember `json:"members"`
			Marker  *int64      `json:"marker"`
		}
		if err := m.request(ctx, http.MethodGet, endpoint, m.cfg.MAXToken, "", nil, &admins); err != nil {
			return err
		}
		for _, member := range admins.Members {
			if member.ID == operatorID {
				if !member.Bot && (member.Admin || member.Owner) {
					return nil
				}
				return errRejected
			}
		}
		if admins.Marker == nil {
			return errRejected
		}
		if seen[*admins.Marker] {
			return errRejected
		}
		seen[*admins.Marker] = true
		marker = admins.Marker
	}
	return errRejected
}

func (m *Module) ownsLease(ctx context.Context, j job) error {
	var valid bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM support.inbox WHERE id=$1 AND status='sending' AND lease_token=$2 AND lease_until>now() AND created_at>now()-interval '24 hours')`, j.ID, j.Lease).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errRejected
	}
	return nil
}
func (m *Module) operatorChat(provider string) int64 {
	if provider == "max" {
		return m.cfg.MAXSupportChatID
	}
	return m.cfg.InternalChatID
}

func (m *Module) process(ctx context.Context, j job, e event) error {
	if e.Provider != j.Provider || (e.Provider != "telegram" && e.Provider != "max") || e.Bot || e.Anonymous || e.SenderID <= 0 {
		return errRejected
	}
	if e.Provider == "max" && m.cfg.MAXSupportChatID == 0 {
		return errMAXUnconfigured
	}
	if !e.Private {
		if e.ChatID != m.operatorChat(e.Provider) || e.ReplyID == "" || (e.Provider == "telegram" && e.ThreadID != m.cfg.SupportTopicID) {
			return errRejected
		}
		return m.reply(ctx, j, e)
	}
	if e.Provider == "telegram" && (e.ChatID <= 0 || e.ChatID != e.SenderID) {
		return errRejected
	}
	if err := m.authorized(ctx, e.Provider, 0); err != nil {
		return err
	}
	if err := m.ownsLease(ctx, j); err != nil {
		return err
	}
	var operatorMessageID string
	var err error
	if e.Provider == "telegram" {
		operatorMessageID, err = m.tgCopy(ctx, m.cfg.InternalChatID, m.cfg.SupportTopicID, e.ChatID, e.MessageID)
	} else {
		if e.Unsupported {
			return m.clientNotice(ctx, e)
		}
		operatorMessageID, err = m.maxSend(ctx, m.cfg.MAXSupportChatID, false, e.Text, e.Attachments)
	}
	if err != nil {
		if unsupportedProvider(err) {
			return m.clientNotice(ctx, e)
		}
		return err
	}
	return m.saveRoute(ctx, j, operatorMessageID, route{Provider: e.Provider, ChatID: e.ChatID, UserID: e.SenderID, MessageID: e.MessageID})
}

func unsupportedProvider(err error) bool {
	var p *providerError
	return errors.As(err, &p) && p.permanent && (p.code == "provider_http_400" || p.code == "telegram_400" || p.code == "unsupported_text")
}
func (m *Module) clientNotice(ctx context.Context, e event) error {
	if e.Provider == "max" {
		_, err := m.maxSend(ctx, e.SenderID, true, unsupportedNotice, nil)
		return err
	}
	_, err := m.tgText(ctx, e.ChatID, 0, unsupportedNotice)
	return err
}

func (m *Module) saveRoute(ctx context.Context, j job, operatorMessageID string, r route) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE support.inbox SET status='sent',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error=NULL WHERE id=$1 AND status='sending' AND lease_token=$2 AND lease_until>now()`, j.ID, j.Lease)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errRejected
	}
	if _, err = tx.Exec(ctx, `INSERT INTO support.message_routes(provider,operator_chat_id,operator_message_id,client_chat_id,client_user_id,client_message_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(provider,operator_chat_id,operator_message_id) DO NOTHING`, r.Provider, m.operatorChat(r.Provider), operatorMessageID, r.ChatID, r.UserID, r.MessageID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Module) reply(ctx context.Context, j job, e event) error {
	var r route
	err := m.pool.QueryRow(ctx, `SELECT provider,client_chat_id,client_user_id,client_message_id FROM support.message_routes WHERE provider=$1 AND operator_chat_id=$2 AND operator_message_id=$3 AND created_at>now()-interval '90 days'`, e.Provider, m.operatorChat(e.Provider), e.ReplyID).Scan(&r.Provider, &r.ChatID, &r.UserID, &r.MessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errMissingRoute
	}
	if err != nil {
		return err
	}
	// Authorization is checked just before the actual native provider delivery.
	if err = m.authorized(ctx, e.Provider, e.SenderID); err != nil {
		return err
	}
	if err = m.ownsLease(ctx, j); err != nil {
		return err
	}
	if r.Provider == "telegram" {
		_, err = m.tgCopy(ctx, r.ChatID, 0, m.cfg.InternalChatID, e.MessageID)
	} else if r.Provider == "max" {
		if e.Unsupported {
			return m.operatorNotice(ctx, e)
		}
		_, err = m.maxSend(ctx, r.UserID, true, e.Text, e.Attachments)
	} else {
		return fmt.Errorf("invalid support route")
	}
	if err != nil {
		if unsupportedProvider(err) {
			return m.operatorNotice(ctx, e)
		}
		return err
	}
	return m.saveRoute(ctx, j, e.MessageID, r)
}

func (m *Module) operatorNotice(ctx context.Context, e event) error {
	if e.Provider == "max" {
		_, err := m.maxSend(ctx, m.cfg.MAXSupportChatID, false, unsupportedNotice, nil)
		return err
	}
	_, err := m.tgText(ctx, m.cfg.InternalChatID, m.cfg.SupportTopicID, unsupportedNotice)
	return err
}
