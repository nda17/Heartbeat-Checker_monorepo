package support

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

type providerError struct {
	code       string
	permanent  bool
	retryAfter time.Duration
}

func (e *providerError) Error() string { return e.code }
func unavailable() error               { return &providerError{code: "provider_unavailable"} }

func providerRetry(header string, body []byte) time.Duration {
	var delay time.Duration
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds > 0 && seconds <= 86400 {
		delay = time.Duration(seconds) * time.Second
	} else if until, err := http.ParseTime(header); err == nil {
		delay = time.Until(until)
	}
	var p struct {
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
		RetryAfter int64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &p) == nil {
		seconds := p.Parameters.RetryAfter
		if p.RetryAfter > seconds {
			seconds = p.RetryAfter
		}
		if seconds > 0 && seconds <= 86400 && time.Duration(seconds)*time.Second > delay {
			delay = time.Duration(seconds) * time.Second
		}
	}
	if delay < 0 {
		delay = 0
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}

func (m *Module) request(ctx context.Context, method, endpoint, token, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return unavailable()
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return unavailable()
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return unavailable()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retry := providerRetry(resp.Header.Get("Retry-After"), data)
		var detail struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &detail)
		if detail.Code == "attachment.not.ready" {
			return &providerError{code: "attachment_not_ready", retryAfter: 5 * time.Second}
		}
		return &providerError{code: fmt.Sprintf("provider_http_%d", resp.StatusCode), permanent: resp.StatusCode != 408 && resp.StatusCode != 429 && resp.StatusCode < 500, retryAfter: retry}
	}
	if err = json.Unmarshal(data, out); err != nil {
		return unavailable()
	}
	return nil
}

func (m *Module) telegram(ctx context.Context, method string, params any, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return unavailable()
	}
	var response struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Code       int             `json:"error_code"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if err = m.request(ctx, http.MethodPost, "https://api.telegram.org/bot"+m.cfg.TelegramToken+"/"+method, "", "application/json", bytes.NewReader(b), &response); err != nil {
		return err
	}
	if !response.OK {
		return &providerError{code: fmt.Sprintf("telegram_%d", response.Code), permanent: response.Code != 429 && response.Code < 500, retryAfter: providerRetry(strconv.FormatInt(response.Parameters.RetryAfter, 10), nil)}
	}
	if err = json.Unmarshal(response.Result, out); err != nil {
		return unavailable()
	}
	return nil
}

func (m *Module) tgText(ctx context.Context, chat, thread int64, text string) (string, error) {
	if len([]rune(text)) > 4096 {
		return "", &providerError{code: "unsupported_text", permanent: true}
	}
	if err := m.tgRate(ctx, chat); err != nil {
		return "", err
	}
	p := map[string]any{"chat_id": chat, "text": text}
	if thread > 0 {
		p["message_thread_id"] = thread
	}
	var out struct {
		ID int64 `json:"message_id"`
	}
	err := m.telegram(ctx, "sendMessage", p, &out)
	m.cooldown(ctx, "telegram", strconv.FormatInt(chat, 10), err)
	if err == nil && out.ID <= 0 {
		return "", unavailable()
	}
	return strconv.FormatInt(out.ID, 10), err
}

func (m *Module) tgCopy(ctx context.Context, to, thread, from int64, id string) (string, error) {
	messageID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || messageID <= 0 {
		return "", errRejected
	}
	if err = m.tgRate(ctx, to); err != nil {
		return "", err
	}
	p := map[string]any{"chat_id": to, "from_chat_id": from, "message_id": messageID}
	if thread > 0 {
		p["message_thread_id"] = thread
	}
	var out struct {
		ID int64 `json:"message_id"`
	}
	err = m.telegram(ctx, "copyMessage", p, &out)
	m.cooldown(ctx, "telegram", strconv.FormatInt(to, 10), err)
	if err == nil && out.ID <= 0 {
		return "", unavailable()
	}
	return strconv.FormatInt(out.ID, 10), err
}
func (m *Module) tgRate(ctx context.Context, chat int64) error {
	interval := time.Second
	if chat < 0 {
		interval = 3 * time.Second
	}
	return m.reserveTarget(ctx, "telegram", strconv.FormatInt(chat, 10), interval)
}

// maxSend always creates a fresh native MAX message. It never copies an incoming
// link or recipient and never calls Telegram or downloads/uploads an attachment.
func (m *Module) maxSend(ctx context.Context, target int64, private bool, text string, attachments []attachment) (string, error) {
	if m.cfg.MAXSupportChatID == 0 {
		return "", errMAXUnconfigured
	}
	if len([]rune(text)) > 4000 {
		return "", &providerError{code: "unsupported_text", permanent: true}
	}
	address := "chat_id=" + strconv.FormatInt(target, 10)
	if private {
		address = "user_id=" + strconv.FormatInt(target, 10)
	}
	if err := m.reserveTarget(ctx, "max", address, 500*time.Millisecond); err != nil {
		return "", err
	}
	params := map[string]any{"text": text}
	if len(attachments) > 0 {
		params["attachments"] = attachments
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "", unavailable()
	}
	var out struct {
		Message struct {
			Body struct {
				ID string `json:"mid"`
			} `json:"body"`
		} `json:"message"`
	}
	err = m.request(ctx, http.MethodPost, "https://platform-api2.max.ru/messages?"+address, m.cfg.MAXToken, "application/json", bytes.NewReader(b), &out)
	m.cooldown(ctx, "max", address, err)
	if err == nil && (out.Message.Body.ID == "" || len(out.Message.Body.ID) > 256) {
		return "", unavailable()
	}
	return out.Message.Body.ID, err
}

// Native support connects only to official API hosts. All resolved addresses must
// be public and the normal TLS certificate/hostname validation remains enabled.
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || (host != "api.telegram.org" && host != "platform-api2.max.ru") {
		return nil, errors.New("invalid support destination")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("support DNS unavailable")
	}
	for _, ip := range ips {
		if !safeIP(ip) {
			return nil, errors.New("unsafe support DNS")
		}
	}
	d := net.Dialer{}
	var conn net.Conn
	for _, ip := range ips {
		conn, err = d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("support connection unavailable")
}

var forbiddenSupportPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
}

func safeIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.Zone() != "" || (ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip)) {
		return false
	}
	for _, p := range forbiddenSupportPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

type providerTransport struct{ standard, max *http.Transport }

func (t providerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.max != nil && r.URL.Hostname() == "platform-api2.max.ru" {
		return t.max.RoundTrip(r)
	}
	return t.standard.RoundTrip(r)
}

func (m *Module) cooldown(ctx context.Context, provider, address string, cause error) {
	var p *providerError
	if !errors.As(cause, &p) || p.retryAfter <= 0 {
		return
	}
	_, _ = m.pool.Exec(ctx, `UPDATE support.target_rates SET next_send_at=GREATEST(next_send_at,now()+$3::interval) WHERE provider=$1 AND target_key=$2`, provider, m.digest([]byte(address)), p.retryAfter.String())
}
