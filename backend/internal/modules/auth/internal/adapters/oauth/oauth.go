package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

type Config struct{ Provider, ClientID, ClientSecret, RedirectURI, Scopes string }
type Provider struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) (*Provider, error) {
	if cfg.Provider != "vk" && cfg.Provider != "yandex" {
		return nil, errors.New("unsupported OAuth provider")
	}
	u, err := url.Parse(cfg.RedirectURI)
	if strings.TrimSpace(cfg.ClientID) == "" || err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) || u.Path != "/api/v1/auth/oauth/"+cfg.Provider+"/callback" {
		return nil, errors.New("invalid OAuth client or redirect configuration")
	}
	if cfg.Provider == "yandex" && cfg.ClientSecret == "" {
		return nil, errors.New("Yandex client secret is required")
	}
	if cfg.Provider == "vk" && cfg.ClientSecret != "" {
		return nil, errors.New("VK PKCE must not use a client secret")
	}
	if cfg.Scopes == "" {
		if cfg.Provider == "vk" {
			cfg.Scopes = "email"
		} else {
			cfg.Scopes = "login:email login:info"
		}
	}
	return &Provider{cfg: cfg, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *Provider) ID() string { return p.cfg.Provider }
func (p *Provider) AuthorizationURL(state, challenge string) string {
	base := "https://oauth.yandex.ru/authorize"
	if p.ID() == "vk" {
		base = "https://id.vk.ru/authorize"
	}
	q := url.Values{"response_type": {"code"}, "client_id": {p.cfg.ClientID}, "redirect_uri": {p.cfg.RedirectURI}, "state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "scope": {p.cfg.Scopes}}
	if p.ID() == "vk" {
		q.Set("code_challenge_method", "s256")
	}
	return base + "?" + q.Encode()
}
func (p *Provider) request(ctx context.Context, endpoint string, form url.Values, token string, out any) error {
	var body io.Reader
	method := http.MethodGet
	if form != nil {
		body = strings.NewReader(form.Encode())
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return errors.New("OAuth provider request failed")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "OAuth "+token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return errors.New("OAuth provider request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("OAuth provider rejected request")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("invalid OAuth provider response")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("invalid OAuth provider response")
	}
	return nil
}
func (p *Provider) Exchange(ctx context.Context, code, verifier, deviceID, state string) (service.OAuthIdentityProfile, error) {
	var empty service.OAuthIdentityProfile
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if code == "" || len(code) > 8192 || verifier == "" {
		return empty, errors.New("invalid OAuth code")
	}
	endpoint := "https://oauth.yandex.ru/token"
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {p.cfg.ClientID}, "redirect_uri": {p.cfg.RedirectURI}, "code_verifier": {verifier}}
	if p.ID() == "vk" {
		if deviceID == "" || len(deviceID) > 4096 {
			return empty, errors.New("missing VK device")
		}
		endpoint = "https://id.vk.ru/oauth2/auth"
		form.Set("device_id", deviceID)
		form.Set("state", state)
	} else {
		form.Set("client_secret", p.cfg.ClientSecret)
	}
	var token struct {
		AccessToken string          `json:"access_token"`
		State       string          `json:"state"`
		UserID      json.Number     `json:"user_id"`
		Error       json.RawMessage `json:"error"`
	}
	if err := p.request(ctx, endpoint, form, "", &token); err != nil {
		return empty, err
	}
	if token.AccessToken == "" || len(token.Error) > 0 && string(token.Error) != "null" {
		return empty, errors.New("OAuth token missing")
	}
	if p.ID() == "vk" {
		if token.State != state || token.UserID.String() == "" {
			return empty, errors.New("VK token identity mismatch")
		}
		var data struct {
			User struct {
				UserID    json.Number `json:"user_id"`
				Email     string      `json:"email"`
				FirstName string      `json:"first_name"`
				LastName  string      `json:"last_name"`
			} `json:"user"`
		}
		if err := p.request(ctx, "https://id.vk.ru/oauth2/user_info", url.Values{"access_token": {token.AccessToken}, "client_id": {p.cfg.ClientID}}, "", &data); err != nil {
			return empty, err
		}
		if data.User.UserID.String() != token.UserID.String() {
			return empty, errors.New("VK user identity mismatch")
		}
		if _, err := strconv.ParseUint(token.UserID.String(), 10, 64); err != nil {
			return empty, errors.New("invalid VK subject")
		}
		return service.OAuthIdentityProfile{Subject: token.UserID.String(), Email: data.User.Email, Name: strings.TrimSpace(data.User.FirstName + " " + data.User.LastName)}, nil
	}
	var data struct {
		ID           string `json:"id"`
		DefaultEmail string `json:"default_email"`
		DisplayName  string `json:"display_name"`
		RealName     string `json:"real_name"`
	}
	if err := p.request(ctx, "https://login.yandex.ru/info?format=json", nil, token.AccessToken, &data); err != nil {
		return empty, err
	}
	if data.ID == "" {
		return empty, errors.New("Yandex subject missing")
	}
	name := data.RealName
	if name == "" {
		name = data.DisplayName
	}
	return service.OAuthIdentityProfile{Subject: data.ID, Email: data.DefaultEmail, Name: name}, nil
}
