package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

const liveTurnstileTestSecret = "1x0000000000000000000000000000000AA"
const liveTurnstileDummyToken = "XXXX.DUMMY.TOKEN.XXXX"

type mailpitAddress struct {
	Address string
}

type mailpitSummary struct {
	Messages []struct {
		ID      string
		Subject string
		To      []mailpitAddress
	} `json:"messages"`
}

func TestLiveAuthProviderSmoke(t *testing.T) {
	if os.Getenv("HEARTBEAT_LIVE_AUTH") != "true" {
		t.Skip("set HEARTBEAT_LIVE_AUTH=true to run live Turnstile and Mailpit smoke")
	}
	captcha, err := NewTurnstile(liveTurnstileTestSecret, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := captcha.Verify(t.Context(), liveTurnstileDummyToken, "192.0.2.10", "register"); err != nil {
		t.Fatalf("official Turnstile testing verification failed: %v", err)
	}
	runAuthMailSmoke(t, captcha, liveTurnstileDummyToken)
}

func TestLocalSMTPAuthSmoke(t *testing.T) {
	if os.Getenv("HEARTBEAT_LIVE_AUTH") != "true" {
		t.Skip("set HEARTBEAT_LIVE_AUTH=true to run local SMTP and Mailpit auth smoke")
	}
	runAuthMailSmoke(t, integrationCaptcha{}, integrationToken)
}

func runAuthMailSmoke(t *testing.T, captcha CaptchaVerifier, captchaToken string) {
	t.Helper()
	f := newAuthFixture(t)
	apiURL := os.Getenv("HEARTBEAT_MAILPIT_API")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:58025"
	}
	if err := requireLocalHTTPOrigin(apiURL); err != nil {
		t.Fatal(err)
	}
	smtpAddr := os.Getenv("HEARTBEAT_SMTP_ADDR")
	if smtpAddr == "" {
		smtpAddr = "127.0.0.1:51025"
	}
	mailer, err := NewLocalMailer(smtpAddr, "noreply@heartbeat.local")
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(f.pool, accounts.New(f.pool), captcha, mailer, Config{
		Origin:          integrationOrigin,
		ChallengeTTL:    10 * time.Minute,
		ResetTTL:        10 * time.Minute,
		RateLimitWindow: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.module = module
	f.h = module.Handler()

	uniqueID, err := integrationRandomID()
	if err != nil {
		t.Fatal(err)
	}
	email := "hc-live-" + strings.ReplaceAll(uniqueID, "-", "") + "@example.test"
	var ownedIDs []string
	t.Cleanup(func() {
		if len(ownedIDs) == 0 {
			return
		}
		body, err := json.Marshal(struct {
			IDs []string `json:"IDs"`
		}{IDs: ownedIDs})
		if err != nil {
			t.Errorf("encode own Mailpit message IDs for cleanup: %v", err)
			return
		}
		req, err := http.NewRequest(http.MethodDelete, strings.TrimRight(apiURL, "/")+"/api/v1/messages", strings.NewReader(string(body)))
		if err != nil {
			t.Errorf("prepare cleanup for own Mailpit messages: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if err != nil {
			t.Errorf("delete own Mailpit test messages: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("delete own Mailpit test messages: status=%d", resp.StatusCode)
		}
	})

	registered := f.request(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "name": "Live Smoke User", "password": integrationPass, "turnstileToken": captchaToken,
	}, nil, "", integrationOrigin, "")
	if registered.Code != http.StatusAccepted {
		t.Fatalf("live registration returned %d (%s)", registered.Code, strings.TrimSpace(registered.Body.String()))
	}
	verifyToken, verifyMessageID := mailpitToken(t, apiURL, email, "Confirm your Heartbeat Checker email", &ownedIDs)
	verified := f.request(t, http.MethodPost, "/api/v1/auth/verify", map[string]string{"token": verifyToken, "password": integrationPass}, nil, "", integrationOrigin, "")
	if verified.Code != http.StatusNoContent {
		t.Fatalf("live email verification returned %d", verified.Code)
	}
	cookie, _, login := f.loginWithCaptcha(t, email, integrationPass, captchaToken)
	if login.Code != http.StatusOK || cookie == nil {
		t.Fatalf("live login returned %d", login.Code)
	}
	resetRequest := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", map[string]string{
		"email": email, "turnstileToken": captchaToken,
	}, nil, "", integrationOrigin, "")
	if resetRequest.Code != http.StatusAccepted {
		t.Fatalf("live password reset request returned %d", resetRequest.Code)
	}
	resetToken, resetMessageID := mailpitToken(t, apiURL, email, "Reset your Heartbeat Checker password", &ownedIDs)
	reset := f.request(t, http.MethodPost, "/api/v1/auth/password/reset/confirm", map[string]string{"token": resetToken, "password": "live reset secure password"}, nil, "", integrationOrigin, "")
	if reset.Code != http.StatusNoContent {
		t.Fatalf("live password reset confirmation returned %d", reset.Code)
	}
	if stale := f.request(t, http.MethodGet, "/api/v1/me", nil, cookie, "", "", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("live reset left login session valid: GET /me returned %d", stale.Code)
	}
	if _, _, relogin := f.loginWithCaptcha(t, email, "live reset secure password", captchaToken); relogin.Code != http.StatusOK {
		t.Fatalf("live login after reset returned %d", relogin.Code)
	}
	if verifyMessageID == "" || resetMessageID == "" {
		t.Fatal("Mailpit returned an empty message ID")
	}
}

func integrationRandomID() (string, error) {
	return security.ID()
}

func requireLocalHTTPOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("Mailpit API must be an exact local HTTP origin")
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("Mailpit API must use a loopback address")
	}
	return nil
}

func mailpitToken(t *testing.T, apiURL, email, subject string, ownedIDs *[]string) (string, string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiURL, "/")+"/api/v1/messages?start=0&limit=100", nil)
		if err != nil {
			t.Fatal("could not prepare Mailpit query")
		}
		resp, err := client.Do(req)
		if err == nil {
			var summary mailpitSummary
			decodeErr := json.NewDecoder(resp.Body).Decode(&summary)
			_ = resp.Body.Close()
			if decodeErr == nil && resp.StatusCode == http.StatusOK {
				for _, item := range summary.Messages {
					recipientMatch := false
					for _, recipient := range item.To {
						if strings.EqualFold(recipient.Address, email) {
							recipientMatch = true
						}
					}
					if !recipientMatch || item.Subject != subject {
						continue
					}
					*ownedIDs = append(*ownedIDs, item.ID)
					rawReq, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiURL, "/")+"/api/v1/message/"+url.PathEscape(item.ID)+"/raw", nil)
					if err != nil {
						t.Fatal("could not prepare Mailpit message query")
					}
					rawResp, err := client.Do(rawReq)
					if err != nil {
						break
					}
					raw, readErr := io.ReadAll(io.LimitReader(rawResp.Body, 64<<10))
					_ = rawResp.Body.Close()
					if readErr != nil || rawResp.StatusCode != http.StatusOK {
						break
					}
					message, err := mail.ReadMessage(strings.NewReader(string(raw)))
					if err != nil {
						break
					}
					decoded, err := io.ReadAll(quotedprintable.NewReader(message.Body))
					if err != nil {
						break
					}
					for _, field := range strings.Fields(string(decoded)) {
						if !strings.Contains(field, "#token=") {
							continue
						}
						u, err := url.Parse(field)
						if err == nil {
							fragment, err := url.ParseQuery(u.Fragment)
							if err == nil && fragment.Get("token") != "" {
								return fragment.Get("token"), item.ID
							}
						}
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Mailpit did not receive the expected auth email with a token")
	return "", ""
}
