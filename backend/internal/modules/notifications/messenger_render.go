package notifications

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type messengerButton struct{ Text, URL string }

var moscow = time.FixedZone("МСК", 3*60*60)

func messengerTime(at time.Time) string {
	return at.In(moscow).Format("02.01.2006 15:04") + " МСК (UTC+03)"
}

func messengerMessage(s messengerSnapshot) (string, []messengerButton) {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = "Монитор"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	var lines []string
	switch s.Kind {
	case "down":
		lines = []string{fmt.Sprintf("🔴 Инцидент №%d · %s", s.Number, name), "Первый сбой: " + messengerTime(s.OpenedAt), "Недоступность подтверждена: " + messengerTime(s.ConfirmedAt), "До подтверждения: " + messengerDuration(s.ConfirmedAt.Sub(s.OpenedAt))}
	case "up":
		closed := s.CheckedAt
		if s.ClosedAt != nil {
			closed = *s.ClosedAt
		}
		lines = []string{fmt.Sprintf("🟢 Инцидент №%d завершён · %s", s.Number, name), "Первый сбой: " + messengerTime(s.OpenedAt), "Недоступность подтверждена: " + messengerTime(s.ConfirmedAt), "Восстановлено: " + messengerTime(closed), "Длительность: " + messengerDuration(closed.Sub(s.OpenedAt))}
	case "reminder":
		lines = []string{fmt.Sprintf("🔴 Инцидент №%d продолжается · %s", s.Number, name), "Первый сбой: " + messengerTime(s.OpenedAt), "Недоступность подтверждена: " + messengerTime(s.ConfirmedAt), "Проверка: " + messengerTime(s.CheckedAt), "Длительность: " + messengerDuration(s.CheckedAt.Sub(s.OpenedAt))}
	}
	if s.CheckType == "tcp" {
		lines = append(lines, "TCP: "+net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	} else if s.CheckType == "icmp" {
		lines = append(lines, "ICMP Ping: "+s.Host)
	} else if s.URL != "" {
		lines = append(lines, "Сайт: "+s.URL)
	}
	if cause := messengerCause(s.ErrorCode); cause != "" && s.Kind != "up" {
		lines = append(lines, "Причина: "+cause)
	}
	if s.HTTPStatus >= 100 && s.HTTPStatus <= 599 {
		lines = append(lines, fmt.Sprintf("HTTP: %d", s.HTTPStatus))
	}
	if s.LatencyMS >= 0 {
		label := "Длительность проверки"
		if s.CheckType == "" || s.CheckType == "http" {
			label = "Время HTTP-проверки"
		}
		lines = append(lines, fmt.Sprintf("%s: %d мс", label, s.LatencyMS))
		if s.RoundTripMS != nil {
			lines = append(lines, fmt.Sprintf("ICMP RTT: %d мс", *s.RoundTripMS))
		}
	}
	buttons := []messengerButton{{"Открыть инцидент", "https://heartbeat-checker.ru/app?monitor=" + url.QueryEscape(s.MonitorID) + "&incident=" + url.QueryEscape(s.IncidentID)}}
	if u, err := url.Parse(s.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil {
		buttons = append(buttons, messengerButton{"Открыть сайт", s.URL})
	}
	text := strings.Join(lines, "\n")
	if len([]rune(text)) > 4000 {
		text = string([]rune(text)[:4000])
	}
	return text, buttons
}

func messengerCause(code string) string {
	switch code {
	case "tcp_connection_refused":
		return "TCP-соединение отклонено"
	case "tcp_timeout":
		return "время подключения к TCP-порту истекло"
	case "tcp_failed":
		return "не удалось подключиться к TCP-порту"
	case "icmp_timeout":
		return "ответ ICMP Echo не получен; сервер или сеть могут блокировать Ping"
	case "icmp_failed":
		return "ICMP-проверка не выполнена"
	case "dns_failed":
		return "не удалось определить адрес сервера"
	case "unexpected_status":
		return "получен неожиданный HTTP-статус"
	case "body_mismatch":
		return "ожидаемый фрагмент ответа не найден"
	case "redirect_limit":
		return "слишком много перенаправлений"
	case "invalid_redirect":
		return "некорректное перенаправление"
	case "response_too_large":
		return "ответ превышает допустимый размер"
	case "request_failed":
		return "HTTP-запрос не выполнен"
	case "request_timeout":
		return "время HTTP-проверки истекло"
	case "tls_certificate_invalid":
		return "не удалось проверить TLS-сертификат"
	case "tls_handshake_failed":
		return "не удалось установить TLS-соединение"
	}
	return ""
}

func messengerDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	minutes := int64(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		return "менее минуты"
	}
	if minutes < 60 {
		return fmt.Sprintf("%d мин", minutes)
	}
	hours := minutes / 60
	if minutes%60 == 0 {
		return fmt.Sprintf("%d ч", hours)
	}
	return fmt.Sprintf("%d ч %d мин", hours, minutes%60)
}
