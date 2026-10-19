package service

import (
	"bytes"
	"errors"
	htmltemplate "html/template"
	"net/url"
	texttemplate "text/template"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
)

type AuthMailData struct {
	Name, ActionURL, Provider string
	ExpiresAt                 time.Time
}
type authMailView struct{ Name, ActionURL, Provider, Expiry, Title, Intro, Button, Note, Logo string }

const authMailText = `Heartbeat Checker
{{.Title}}

Здравствуйте{{if .Name}}, {{.Name}}{{end}}!

{{.Intro}}
{{.ActionURL}}

{{.Note}}
Ссылка действительна до {{.Expiry}} (UTC).
Если вы не запрашивали это письмо, просто проигнорируйте его.

Команда Heartbeat Checker
https://heartbeat-checker.ru
`
const authMailHTML = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title></head><body style="margin:0;padding:24px 12px;background:#f2f7f7;font-family:Arial,sans-serif;color:#173b3d"><table role="presentation" style="width:100%;max-width:600px;margin:auto;border-collapse:collapse"><tr><td style="padding:24px;background:#ffffff;border-radius:20px"><img src="{{.Logo}}" width="56" height="56" alt="Heartbeat Checker" style="display:block;border-radius:12px"><p style="color:#087f86;font-size:18px;font-weight:bold">Heartbeat Checker</p><h1 style="font-size:26px;line-height:1.3">{{.Title}}</h1><p style="line-height:1.6">Здравствуйте{{if .Name}}, {{.Name}}{{end}}!</p><p style="line-height:1.6">{{.Intro}}</p><table role="presentation"><tr><td style="background:#087f86;border-radius:10px;padding:16px 24px"><a href="{{.ActionURL}}" style="color:#ffffff;text-decoration:none;font-weight:bold">{{.Button}}</a></td></tr></table><p style="line-height:1.6">{{.Note}}</p><p style="font-size:14px;line-height:1.6;color:#567174">Ссылка действительна до {{.Expiry}} (UTC). Если кнопка не открывается, скопируйте адрес в браузер:</p><p style="word-break:break-all;font-size:13px"><a href="{{.ActionURL}}" style="color:#087f86">{{.ActionURL}}</a></p><p style="font-size:14px;line-height:1.6;color:#567174">Если вы не запрашивали это письмо, просто проигнорируйте его.</p></td></tr><tr><td style="padding:24px;text-align:center;font-size:13px;color:#567174">Команда Heartbeat Checker · heartbeat-checker.ru</td></tr></table></body></html>`

func RenderAuthMail(kind string, data AuthMailData) (string, contracts.MailContent, error) {
	u, err := url.Parse(data.ActionURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || data.ExpiresAt.IsZero() {
		return "", contracts.MailContent{}, errors.New("invalid auth mail data")
	}
	v := authMailView{Name: data.Name, ActionURL: data.ActionURL, Provider: data.Provider, Expiry: data.ExpiresAt.UTC().Format("02.01.2006 15:04"), Logo: u.Scheme + "://" + u.Host + "/brand/avatar512.png"}
	switch kind {
	case "registration":
		v.Title = "Подтвердите вашу почту"
		v.Intro = "Остался один шаг до регистрации в Heartbeat Checker: подтвердите адрес электронной почты."
		v.Button = "Подтвердить почту"
		v.Note = "На странице подтверждения введите пароль, который указали при регистрации."
	case "oauth_registration":
		v.Title = "Подтвердите вашу почту"
		v.Intro = "Вы начали регистрацию в Heartbeat Checker через " + data.Provider + ". Подтвердите адрес электронной почты, чтобы создать аккаунт."
		v.Button = "Завершить регистрацию"
		v.Note = "Откройте ссылку в том же браузере, где начали регистрацию."
	case "email_verification":
		v.Title = "Подтвердите email аккаунта"
		v.Intro = "Подтвердите адрес электронной почты для уведомлений о сбоях и восстановлении ваших мониторов."
		v.Button = "Подтвердить email"
		v.Note = "Войдите в свой аккаунт и подтвердите адрес на странице настроек. Пароль и текущие сеансы не изменятся. Уведомления включаются отдельно для каждого монитора."
	case "password_reset":
		v.Title = "Установите новый пароль"
		v.Intro = "Вы запросили установку нового пароля для аккаунта Heartbeat Checker."
		v.Button = "Установить пароль"
		v.Note = "После установки нового пароля все текущие сеансы будут завершены."
	default:
		return "", contracts.MailContent{}, errors.New("unknown auth mail kind")
	}
	var text, html bytes.Buffer
	t, err := texttemplate.New("auth").Parse(authMailText)
	if err != nil {
		return "", contracts.MailContent{}, err
	}
	if err = t.Execute(&text, v); err != nil {
		return "", contracts.MailContent{}, err
	}
	h, err := htmltemplate.New("auth").Parse(authMailHTML)
	if err != nil {
		return "", contracts.MailContent{}, err
	}
	if err = h.Execute(&html, v); err != nil {
		return "", contracts.MailContent{}, err
	}
	return v.Title + " — Heartbeat Checker", contracts.MailContent{Text: text.String(), HTML: html.String()}, nil
}
