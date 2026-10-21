package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func publicAddress(address netip.Addr) bool {
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "fc00::/7", "fe80::/10"} {
		if netip.MustParsePrefix(cidr).Contains(address) {
			return false
		}
	}
	return true
}

// TelegramTransport pins only the dial destination; TLS still validates the URL host.
func TelegramTransport(getenv func(string) string) (string, *http.Client, error) {
	base := strings.TrimSpace(getenv("TELEGRAM_API_BASE_URL"))
	if base == "" {
		base = "https://api.telegram.org"
	}
	if base != "https://api.telegram.org" && base != "https://messengers-proxy.heartbeat-checker.ru/telegram-api" {
		return "", nil, failure("invalid_telegram_api_base_url")
	}
	pin := strings.TrimSpace(getenv("TELEGRAM_API_PROXY_IP"))
	if pin != "" {
		address, err := netip.ParseAddr(pin)
		if err != nil || !publicAddress(address.Unmap()) {
			return "", nil, failure("invalid_telegram_api_proxy_ip")
		}
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 20 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if pin != "" {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			address = net.JoinHostPort(pin, port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	return base, &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return failure("http_redirect_rejected") }}, nil
}
func FormatSize(size int64) string {
	unit, amount := "КБ", float64(size)/1024
	if size >= 1024*1024 {
		unit = "МБ"
		amount = float64(size) / (1024 * 1024)
	}
	return strings.ReplaceAll(fmt.Sprintf("%.1f", amount), ".", ",") + " " + unit
}
func (r *Runner) Report(ctx context.Context, state State, path string) (bool, error) {
	if truth(state["report_sent"]) {
		return true, nil
	}
	text := ""
	if state["status"] == "success" {
		now := r.Now()
		retainedUntil, err := time.ParseInLocation("2006-01-02", stringify(state["date"]), moscow())
		if err != nil {
			return false, err
		}
		retainedUntil = retainedUntil.AddDate(0, 0, 14)
		lifetime := min(int64(604800), int64(retainedUntil.Sub(now)/time.Second))
		text = "Резервная копия Heartbeat Checker за " + stringify(state["date"]) + " была создана.\nБаза: heartbeat (все таблицы и ledger).\nS3: загружена, SHA256 проверен, скачанный архив восстановлен в отдельном PostgreSQL.\nРазмер: " + FormatSize(number(state["bytes"])) + ".\nSHA256: " + stringify(state["ciphertext_sha256"])
		retentionDate := retainedUntil.Format("02.01.2006 15:04 МСК")
		if lifetime > 0 {
			link, err := r.Store.Presign(ctx, stringify(state["object_key"]), time.Duration(lifetime)*time.Second)
			if err != nil {
				return false, err
			}
			expires := now.Add(time.Duration(lifetime) * time.Second).In(moscow()).Format("02.01.2006 15:04 МСК")
			duration := "оставшийся срок хранения"
			if lifetime == 604800 {
				duration = "7 дней"
			}
			text += "\nСсылка действует " + duration + ", до " + expires + ".\nСрок хранения копии — 14 дней; граница выдачи ссылки: " + retentionDate + ".\nЗашифрованный архив:\n" + link
		} else {
			text += "\nСрок хранения 14 дней истёк " + retentionDate + ". Ссылка на архив не формируется; его доступность не подтверждена."
		}
		if truth(state["retention_warning"]) {
			text += "\nВнимание: очистка копий старше 14 дней не выполнена."
		}
	} else {
		phase := stringify(state["phase"])
		if phase == "" {
			phase = "unknown"
		}
		code := stringify(state["error"])
		if code == "" {
			code = "interrupted"
		}
		text = "ОШИБКА резервного копирования Heartbeat Checker за " + stringify(state["date"]) + ".\nЭтап: " + phase + ". Код: " + code + ".\nУспешная резервная копия не подтверждена."
	}
	if r.Getenv("TELEGRAM_INTERNAL_CHAT_ID") != "-1004317880847" || r.Getenv("TELEGRAM_BACKUPS_TOPIC_ID") != "4" {
		return false, failure("invalid_telegram_target")
	}
	token := r.Getenv("TELEGRAM_BOT_INFORMER_TOKEN")
	if token == "" {
		return false, failure("missing_telegram_token")
	}
	base, client, err := TelegramTransport(r.Getenv)
	if err != nil {
		return false, err
	}
	var sender HTTPClient = client
	if r.Telegram != nil {
		sender = r.Telegram
	}
	payload, err := marshal(map[string]any{"chat_id": r.Getenv("TELEGRAM_INTERNAL_CHAT_ID"), "message_thread_id": 4, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}})
	if err != nil {
		return false, err
	}
	delivered := func() bool {
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, base+"/bot"+token+"/sendMessage", bytes.NewReader(payload))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := sender.Do(req)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return false
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		if err != nil {
			return false
		}
		var result map[string]any
		if json.Unmarshal(body, &result) != nil {
			return false
		}
		return result["ok"] == true
	}()
	if !delivered {
		state["report_error"] = "telegram_delivery_failed"
		state["report_last_attempt"] = r.timestamp()
		return false, Save(path, state)
	}
	state["report_sent"] = true
	state["report_sent_at"] = r.timestamp()
	delete(state, "report_error")
	return true, Save(path, state)
}
