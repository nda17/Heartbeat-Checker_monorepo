package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type messengerProviderError struct {
	permanent  bool
	retryAfter time.Duration
	statusCode int
	rebind     bool
}

func (e messengerProviderError) Error() string { return "messenger provider unavailable" }

var messengerForbiddenIP = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
}

func messengerPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" || ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range messengerForbiddenIP {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
func messengerDial(ctx context.Context, network, address, telegramHost, pinnedIP string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || host != telegramHost && host != "platform-api2.max.ru" {
		return nil, errors.New("invalid messenger destination")
	}
	var ips []netip.Addr
	if host == telegramHost && pinnedIP != "" {
		ip, err := netip.ParseAddr(pinnedIP)
		if err != nil || !messengerPublicIP(ip) {
			return nil, errors.New("invalid messenger pinned IP")
		}
		ips = []netip.Addr{ip}
	} else {
		ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("messenger DNS unavailable")
		}
		for _, ip := range ips {
			if !messengerPublicIP(ip) {
				return nil, errors.New("unsafe messenger DNS")
			}
		}
	}
	d := net.Dialer{}
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("messenger network unavailable")
}

type messengerTransport struct{ standard, max *http.Transport }

func (t messengerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "platform-api2.max.ru" && t.max != nil {
		return t.max.RoundTrip(r)
	}
	return t.standard.RoundTrip(r)
}
func messengerClient(cfg MessengerConfig) (*http.Client, error) {
	base := cfg.TelegramAPIBaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	if base != "https://api.telegram.org" && base != "https://messengers-proxy.heartbeat-checker.ru/telegram-api" {
		return nil, errors.New("invalid Telegram API base URL")
	}
	telegramHost := "api.telegram.org"
	if strings.Contains(base, "messengers-proxy") {
		telegramHost = "messengers-proxy.heartbeat-checker.ru"
	}
	standard := http.DefaultTransport.(*http.Transport).Clone()
	standard.Proxy = nil
	standard.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return messengerDial(ctx, network, address, telegramHost, cfg.TelegramAPIIP)
	}
	var maxTransport *http.Transport
	if cfg.MAXRootCAFile != "" {
		pem, err := os.ReadFile(cfg.MAXRootCAFile)
		if err != nil {
			return nil, errors.New("read MAX root CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid MAX root CA")
		}
		maxTransport = standard.Clone()
		maxTransport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: messengerTransport{standard, maxTransport}}, nil
}

func (m *Messenger) providerRequest(ctx context.Context, method, endpoint, token string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return messengerProviderError{permanent: true}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return messengerProviderError{permanent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return messengerProviderError{}
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(response) > 1<<20 {
		return messengerProviderError{}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retry := time.Duration(0)
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 86400 {
			retry = time.Duration(seconds) * time.Second
		}
		var telegramFailure struct {
			Description string `json:"description"`
			Parameters  struct {
				RetryAfter      int   `json:"retry_after"`
				MigrateToChatID int64 `json:"migrate_to_chat_id"`
			} `json:"parameters"`
			RetryAfter int `json:"retry_after"`
		}
		if json.Unmarshal(response, &telegramFailure) == nil {
			seconds := telegramFailure.Parameters.RetryAfter
			if telegramFailure.RetryAfter > seconds {
				seconds = telegramFailure.RetryAfter
			}
			if seconds > 0 && seconds <= 86400 && time.Duration(seconds)*time.Second > retry {
				retry = time.Duration(seconds) * time.Second
			}
		}
		return messengerProviderError{permanent: resp.StatusCode != 408 && resp.StatusCode != 429 && resp.StatusCode < 500, retryAfter: retry, statusCode: resp.StatusCode, rebind: telegramFailure.Parameters.MigrateToChatID != 0 || messengerNeedsRebind(telegramFailure.Description)}
	}
	if err = json.Unmarshal(response, out); err != nil {
		return messengerProviderError{}
	}
	return nil
}

func (m *Messenger) telegram(ctx context.Context, method string, body any, out any) error {
	base := m.cfg.TelegramAPIBaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	var response struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter      int   `json:"retry_after"`
			MigrateToChatID int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	if err := m.providerRequest(ctx, http.MethodPost, base+"/bot"+m.cfg.TelegramToken+"/"+method, "", body, &response); err != nil {
		return err
	}
	if !response.OK {
		return messengerProviderError{permanent: response.Code != 429 && response.Code < 500, retryAfter: time.Duration(response.Parameters.RetryAfter) * time.Second, statusCode: response.Code, rebind: response.Parameters.MigrateToChatID != 0 || messengerNeedsRebind(response.Description)}
	}
	if err := json.Unmarshal(response.Result, out); err != nil {
		return messengerProviderError{}
	}
	return nil
}

func messengerNeedsRebind(description string) bool {
	value := strings.ToLower(description)
	for _, reason := range []string{"message thread not found", "topic was closed", "topic is closed", "topic not found", "topic_closed", "topic_deleted", "chat was migrated", "migrated to a supergroup", "chat not found", "bot was kicked", "bot is not a member", "not enough rights to send"} {
		if strings.Contains(value, reason) {
			return true
		}
	}
	return false
}

func (m *Messenger) verifyTarget(ctx context.Context, t messengerTarget) (string, error) {
	if t.Provider == "max" {
		return "MAX · личный чат", nil
	}
	var bot struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		IsBot    bool   `json:"is_bot"`
	}
	if err := m.telegram(ctx, "getMe", map[string]any{}, &bot); err != nil {
		return "", err
	}
	if !bot.IsBot || bot.ID <= 0 || !strings.EqualFold(bot.Username, m.cfg.TelegramBotUsername) {
		return "", messengerProviderError{permanent: true}
	}
	if t.Kind == "private" {
		return "Telegram · личный чат", nil
	}
	chatID, err := strconv.ParseInt(t.ChatID, 10, 64)
	if err != nil || chatID >= 0 {
		return "", messengerProviderError{permanent: true}
	}
	userID, err := strconv.ParseInt(t.UserID, 10, 64)
	if err != nil || userID <= 0 {
		return "", messengerProviderError{permanent: true}
	}
	var chat struct {
		ID      int64  `json:"id"`
		Type    string `json:"type"`
		Title   string `json:"title"`
		IsForum bool   `json:"is_forum"`
	}
	if err = m.telegram(ctx, "getChat", map[string]any{"chat_id": chatID}, &chat); err != nil {
		return "", err
	}
	if chat.ID != chatID || chat.Type != "supergroup" && chat.Type != "group" || t.Kind == "topic" && (!chat.IsForum || t.ThreadID == "1") || t.Kind == "group" && chat.IsForum {
		return "", messengerProviderError{permanent: true}
	}
	for _, id := range []int64{bot.ID, userID} {
		var member struct {
			Status string `json:"status"`
			User   struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		if err = m.telegram(ctx, "getChatMember", map[string]any{"chat_id": chatID, "user_id": id}, &member); err != nil {
			return "", err
		}
		if member.User.ID != id || member.Status != "administrator" && member.Status != "creator" {
			return "", messengerProviderError{permanent: true}
		}
	}
	label := strings.TrimSpace(chat.Title)
	if label == "" {
		label = "Telegram · группа"
	}
	if len([]rune(label)) > 100 {
		label = string([]rune(label)[:100])
	}
	if t.Kind == "topic" {
		label += " · тема " + t.ThreadID
	}
	return label, nil
}

func (m *Messenger) sendTarget(ctx context.Context, t messengerTarget, text string, buttons []messengerButton) error {
	if len([]rune(text)) > 4000 {
		return messengerProviderError{permanent: true}
	}
	if t.Provider == "telegram" {
		chat, err := strconv.ParseInt(t.ChatID, 10, 64)
		if err != nil {
			return messengerProviderError{permanent: true}
		}
		body := map[string]any{"chat_id": chat, "text": text}
		if t.Kind == "topic" {
			thread, err := strconv.ParseInt(t.ThreadID, 10, 64)
			if err != nil {
				return messengerProviderError{permanent: true}
			}
			body["message_thread_id"] = thread
		}
		if len(buttons) > 0 {
			rows := []any{}
			for _, b := range buttons {
				rows = append(rows, []any{map[string]string{"text": b.Text, "url": b.URL}})
			}
			body["reply_markup"] = map[string]any{"inline_keyboard": rows}
		}
		var out struct {
			ID int64 `json:"message_id"`
		}
		if err = m.telegram(ctx, "sendMessage", body, &out); err != nil {
			return err
		}
		if out.ID <= 0 {
			return messengerProviderError{}
		}
		return nil
	}
	if t.Provider == "max" && t.Kind == "private" {
		params := map[string]any{"text": text}
		if len(buttons) > 0 {
			rows := []any{}
			for _, b := range buttons {
				rows = append(rows, []any{map[string]any{"type": "link", "text": b.Text, "url": b.URL}})
			}
			params["attachments"] = []any{map[string]any{"type": "inline_keyboard", "payload": map[string]any{"buttons": rows}}}
		}
		var out struct {
			Message struct {
				Body struct {
					ID string `json:"mid"`
				} `json:"body"`
			} `json:"message"`
		}
		if err := m.providerRequest(ctx, http.MethodPost, "https://platform-api2.max.ru/messages?user_id="+t.UserID, m.cfg.MAXToken, params, &out); err != nil {
			return err
		}
		if out.Message.Body.ID == "" {
			return messengerProviderError{}
		}
		return nil
	}
	return fmt.Errorf("invalid messenger target")
}
