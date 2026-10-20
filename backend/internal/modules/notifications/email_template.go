package notifications

import (
	"bytes"
	"fmt"
	"html/template"
	"net"
	"strconv"
	"strings"
	"unicode"
)

const incidentEmailFooter = "Вы получаете письмо, потому что включили email-уведомления для этого монитора. Отключить их можно в настройках уведомлений монитора в личном кабинете."

type incidentEmailRow struct{ Label, Value string }

type incidentEmailView struct {
	Title, Intro, Name, IncidentURL, SiteURL string
	Number                                   int64
	Recovered                                bool
	Rows                                     []incidentEmailRow
}

func incidentEmailSubjectName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "Монитор"
	}
	if runes := []rune(name); len(runes) > 100 {
		name = string(runes[:100])
	}
	return name
}

func incidentEmailData(s messengerSnapshot, buttons []messengerButton) incidentEmailView {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = "Монитор"
	}
	if runes := []rune(name); len(runes) > 100 {
		name = string(runes[:100])
	}
	v := incidentEmailView{
		Title: "Сбой подтверждён", Intro: "Проверки подтвердили недоступность монитора. Откройте инцидент, чтобы посмотреть подробности.",
		Name: name, Number: s.Number,
		Rows: []incidentEmailRow{
			{"Первый сбой", messengerTime(s.OpenedAt, s.TimeZone)},
			{"Недоступность подтверждена", messengerTime(s.ConfirmedAt, s.TimeZone)},
		},
	}
	switch s.Kind {
	case "up":
		v.Title = "Сервис восстановился"
		v.Intro = "Монитор снова доступен. Инцидент завершён."
		v.Recovered = true
		closed := s.CheckedAt
		if s.ClosedAt != nil {
			closed = *s.ClosedAt
		}
		v.Rows = append(v.Rows, incidentEmailRow{"Восстановлено", messengerTime(closed, s.TimeZone)}, incidentEmailRow{"Длительность", messengerDuration(closed.Sub(s.OpenedAt))})
	case "reminder":
		v.Title = "Сбой продолжается"
		v.Intro = "Монитор по-прежнему недоступен. Это напоминание об открытом инциденте."
		v.Rows = append(v.Rows, incidentEmailRow{"Проверка", messengerTime(s.CheckedAt, s.TimeZone)}, incidentEmailRow{"Длительность", messengerDuration(s.CheckedAt.Sub(s.OpenedAt))})
	default:
		v.Rows = append(v.Rows, incidentEmailRow{"До подтверждения", messengerDuration(s.ConfirmedAt.Sub(s.OpenedAt))})
	}
	switch s.CheckType {
	case "tcp":
		v.Rows = append(v.Rows, incidentEmailRow{"Тип проверки", "TCP"}, incidentEmailRow{"TCP", net.JoinHostPort(s.Host, strconv.Itoa(s.Port))})
	case "icmp":
		v.Rows = append(v.Rows, incidentEmailRow{"Тип проверки", "ICMP Ping"}, incidentEmailRow{"ICMP Ping", s.Host})
	default:
		v.Rows = append(v.Rows, incidentEmailRow{"Тип проверки", "HTTP"})
		if s.URL != "" {
			v.Rows = append(v.Rows, incidentEmailRow{"Сайт", s.URL})
		}
	}
	provider := "Неизвестно"
	if len(s.ProviderNames) > 0 {
		provider = strings.Join(s.ProviderNames, "; ")
	}
	v.Rows = append(v.Rows, incidentEmailRow{"Провайдер / CDN", provider})
	if cause := messengerCause(s.ErrorCode); cause != "" && s.Kind != "up" {
		v.Rows = append(v.Rows, incidentEmailRow{"Причина", cause})
	}
	if s.HTTPStatus >= 100 && s.HTTPStatus <= 599 {
		v.Rows = append(v.Rows, incidentEmailRow{"HTTP", strconv.Itoa(s.HTTPStatus)})
	}
	if s.LatencyMS >= 0 {
		label := "Длительность проверки"
		if s.CheckType == "" || s.CheckType == "http" {
			label = "Время HTTP-проверки"
		}
		v.Rows = append(v.Rows, incidentEmailRow{label, fmt.Sprintf("%d мс", s.LatencyMS)})
		if s.RoundTripMS != nil {
			v.Rows = append(v.Rows, incidentEmailRow{"ICMP RTT", fmt.Sprintf("%d мс", *s.RoundTripMS)})
		}
	}
	// Both links come from the existing snapshot URL builder; site links accept
	// only absolute HTTP(S) URLs without user credentials.
	if len(buttons) > 0 {
		v.IncidentURL = buttons[0].URL
	}
	if len(buttons) > 1 {
		v.SiteURL = buttons[1].URL
	}
	return v
}

var incidentEmailTemplate = template.Must(template.New("incident-email").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} · {{.Name}}</title></head>
<body style="margin:0;padding:24px 12px;background:#f2f7f7;font-family:Arial,sans-serif;color:#173b3d">
<div style="display:none;max-height:0;overflow:hidden;opacity:0">{{.Title}} · {{.Name}} · Инцидент №{{.Number}}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="width:100%;max-width:600px;margin:auto;border-collapse:collapse">
<tr><td style="padding:24px;background:#ffffff;border-radius:20px">
<img src="https://heartbeat-checker.ru/brand/avatar512.png" width="56" height="56" alt="Heartbeat Checker" style="display:block;border:0;border-radius:12px">
<p style="margin:16px 0 28px;color:#087f86;font-size:18px;font-weight:bold">Heartbeat Checker</p>
<table role="presentation" cellpadding="0" cellspacing="0"><tr><td style="border-radius:8px;padding:8px 12px;font-size:13px;font-weight:bold;{{if .Recovered}}background:#e9f7ee;color:#187447{{else}}background:#fff0ef;color:#b3312b{{end}}">{{if .Recovered}}Доступен{{else}}Недоступен{{end}} · Инцидент №{{.Number}}</td></tr></table>
<h1 style="margin:20px 0 12px;font-size:26px;line-height:1.3;{{if .Recovered}}color:#187447{{else}}color:#b3312b{{end}}">{{.Title}}</h1>
<h2 style="margin:0 0 12px;font-size:20px;line-height:1.4;word-break:break-word;overflow-wrap:anywhere">{{.Name}}</h2>
<p style="margin:0 0 24px;font-size:15px;line-height:1.6;color:#567174">{{.Intro}}</p>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="width:100%;border-collapse:collapse;font-size:14px;line-height:1.6">
{{range .Rows}}<tr><td valign="top" style="width:42%;padding:12px 12px 12px 0;border-bottom:1px solid #e5eeee;color:#567174">{{.Label}}</td><td valign="top" style="padding:12px 0;border-bottom:1px solid #e5eeee;color:#173b3d;word-break:break-word;overflow-wrap:anywhere">{{.Value}}</td></tr>{{end}}
</table>
<table role="presentation" cellpadding="0" cellspacing="0" style="margin-top:28px"><tr><td style="background:#087f86;border-radius:10px;padding:16px 24px"><a href="{{.IncidentURL}}" style="display:inline-block;color:#ffffff;text-decoration:none;font-size:15px;font-weight:bold">Открыть инцидент</a></td></tr></table>
{{if .SiteURL}}<p style="margin:20px 0 0;font-size:14px;line-height:1.6"><a href="{{.SiteURL}}" style="color:#087f86;text-decoration:underline">Открыть сайт</a></p>{{end}}
<p style="margin:24px 0 8px;font-size:13px;line-height:1.6;color:#567174">Если кнопка не открывается, скопируйте адрес в браузер:</p>
<p style="margin:0;font-size:13px;line-height:1.6;word-break:break-all"><a href="{{.IncidentURL}}" style="color:#087f86">{{.IncidentURL}}</a></p>
</td></tr><tr><td style="padding:24px 12px;text-align:center;font-size:13px;line-height:1.6;color:#567174">` + incidentEmailFooter + `<p style="margin:16px 0 0">Команда Heartbeat Checker · <a href="https://heartbeat-checker.ru" style="color:#087f86;text-decoration:none">heartbeat-checker.ru</a></p></td></tr>
</table></body></html>`))

func renderIncidentEmail(view incidentEmailView) (string, error) {
	var markup bytes.Buffer
	if err := incidentEmailTemplate.Execute(&markup, view); err != nil {
		return "", err
	}
	return markup.String(), nil
}
