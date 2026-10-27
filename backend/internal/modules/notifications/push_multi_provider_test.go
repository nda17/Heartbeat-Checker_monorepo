package notifications

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	incidentcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
)

func TestPushCloudSendersUseTheirProviderSpecificDataRoutes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "project-123", "client_email": "worker@project-123.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, key)})),
		"token_uri":   "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	fcm, err := NewFCMPushSender("project-123", base64.StdEncoding.EncodeToString(credentials))
	if err != nil {
		t.Fatalf("create FCM sender from synthetic credentials: %v", err)
	}
	huawei, err := NewHuaweiPushSender("12345", "67890", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, provider, host, path, tokenField string
		sender                                 *cloudPushSender
	}{
		{name: "fcm", provider: "fcm", host: "fcm.googleapis.com", path: "/v1/projects/project-123/messages:send", tokenField: "fid", sender: fcm.(*cloudPushSender)},
		{name: "huawei", provider: "huawei", host: "push-api.cloud.huawei.com", path: "/v1/67890/messages:send", tokenField: "token", sender: huawei.(*cloudPushSender)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			tc.sender.client = &http.Client{Transport: pushRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Host == "oauth2.googleapis.com" || r.URL.Host == "oauth-login.cloud.huawei.com" {
					if tc.provider == "huawei" {
						formBody, err := io.ReadAll(r.Body)
						if err != nil {
							t.Fatal(err)
						}
						form, err := url.ParseQuery(string(formBody))
						if err != nil || form.Get("client_id") != "67890" || form.Get("client_id") == "12345" {
							t.Fatalf("Huawei OAuth client ID did not remain distinct from project ID: %v", err)
						}
					}
					return pushResponse(r, http.StatusOK, `{"access_token":"synthetic-access-token-123","expires_in":3600,"token_type":"Bearer"}`), nil
				}
				if r.URL.Host != tc.host || r.URL.Path != tc.path || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-access-token-123" {
					t.Fatalf("provider request = %s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				message, ok := body["message"].(map[string]any)
				if !ok || len(body) != 1 || message["notification"] != nil {
					t.Fatalf("provider payload must contain a data-only message: %#v", body)
				}
				if tc.provider == "fcm" {
					if message["fid"] != "destination-token" || message["token"] != nil {
						t.Errorf("FCM destination fields = %#v; require fid and prohibit token", message)
					}
					data, ok := message["data"].(map[string]any)
					if !ok || len(data) != 5 || data["v"] != "2" || data["deliveryId"] != "opaque-1" || data["installationId"] != "install-1" || data["bindingGeneration"] != "2" || data["expiresAt"] != "2030-01-02T03:04:05Z" {
						t.Errorf("FCM data = %#v", message["data"])
					}
				} else {
					tokens, ok := message["token"].([]any)
					if !ok || len(tokens) != 1 || tokens[0] != "destination-token" || message["fid"] != nil {
						t.Errorf("Huawei destination fields = %#v; require token list", message)
					}
					data, ok := message["data"].(string)
					var decoded map[string]string
					if !ok || json.Unmarshal([]byte(data), &decoded) != nil || len(decoded) != 5 || decoded["v"] != "2" || decoded["deliveryId"] != "opaque-1" || decoded["installationId"] != "install-1" || decoded["bindingGeneration"] != "2" || decoded["expiresAt"] != "2030-01-02T03:04:05Z" {
						t.Errorf("Huawei opaque data = %#v", message["data"])
					}
					android, ok := message["android"].(map[string]any)
					if !ok || android["urgency"] != "HIGH" || android["ttl"] != "60s" || android["collapse_key"] != float64(-1) {
						t.Errorf("Huawei Android delivery options = %#v; require high urgency, TTL, and no collapse", message["android"])
					}
				}
				if tc.provider == "fcm" {
					return pushResponse(r, http.StatusOK, `{"name":"projects/project-123/messages/opaque"}`), nil
				}
				return pushResponse(r, http.StatusOK, `{"code":"80000000"}`), nil
			})}
			if err := tc.sender.Send(context.Background(), "destination-token", map[string]string{
				"v": "2", "deliveryId": "opaque-1", "installationId": "install-1", "bindingGeneration": "2", "expiresAt": "2030-01-02T03:04:05Z",
			}, time.Minute); err != nil {
				t.Fatalf("synthetic %s send: %v", tc.provider, err)
			}
			if requests != 2 {
				t.Fatalf("synthetic %s request count = %d, want OAuth plus send", tc.provider, requests)
			}
		})
	}
}

func TestPushCloudInvalidTokenErrorsAreRedacted(t *testing.T) {
	for _, tc := range []struct{ name, provider, response string }{
		{name: "fcm", provider: "fcm", response: `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`},
		{name: "huawei", provider: "huawei", response: `{"code":"80300007","msg":"private provider detail"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &cloudPushSender{provider: tc.provider, projectID: "project-123", clientID: "67890", clientSecret: strings.Repeat("s", 32),
				client: &http.Client{Transport: pushRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
					if strings.Contains(r.URL.Host, "oauth") {
						return pushResponse(r, http.StatusOK, `{"access_token":"synthetic-access-token-123","expires_in":3600,"token_type":"Bearer"}`), nil
					}
					return pushResponse(r, http.StatusNotFound, tc.response), nil
				})},
			}
			if tc.provider == "fcm" {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
				sender.key = key
				sender.email = "worker@project-123.iam.gserviceaccount.com"
			}
			err := sender.Send(context.Background(), "synthetic-secret-destination", map[string]string{"deliveryId": "opaque"}, time.Minute)
			var providerErr pushProviderError
			if !strings.Contains(err.Error(), "unavailable") || !errorsAs(err, &providerErr) || !providerErr.invalidToken {
				t.Fatalf("provider invalid-token error = (%v, %+v), want redacted invalid-token classification", err, providerErr)
			}
			if strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "private provider detail") {
				t.Fatalf("provider error exposed sensitive content: %v", err)
			}
		})
	}
}

func TestHuaweiTransientProviderErrorRemainsRetryable(t *testing.T) {
	sender, err := NewHuaweiPushSender("11111", "22222", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	cloudSender := sender.(*cloudPushSender)
	cloudSender.client = &http.Client{Transport: pushRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "oauth-login.cloud.huawei.com" {
			return pushResponse(r, http.StatusOK, `{"access_token":"synthetic-access-token-123","expires_in":3600,"token_type":"Bearer"}`), nil
		}
		if r.URL.Path != "/v1/22222/messages:send" {
			t.Fatalf("Huawei send path = %q, want client ID path", r.URL.Path)
		}
		return pushResponse(r, http.StatusOK, `{"code":"81000001"}`), nil
	})}
	err = sender.Send(context.Background(), "synthetic-huawei-token", map[string]string{"deliveryId": "opaque"}, time.Minute)
	var providerErr pushProviderError
	if !errorsAs(err, &providerErr) || providerErr.code != "provider_unavailable" || providerErr.permanent {
		t.Fatalf("Huawei transient provider error = (%v, %+v), want retryable provider_unavailable", err, providerErr)
	}
}

func TestPushIncidentBodyKeepsLegacySingleLineBoundsAndMonitorZoneAcrossDST(t *testing.T) {
	providers := []string{
		strings.Repeat("Ж", 100),
		strings.Repeat("К", 100),
		strings.Repeat("Я", 100),
		"must-not-be-included",
	}
	firstThree := strings.Join(providers[:3], "; ")
	wantProviderLabel := string([]rune(firstThree)[:160])
	openedWinter := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	confirmedWinter := openedWinter.Add(5 * time.Minute)
	openedSummer := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	confirmedSummer := openedSummer.Add(2 * time.Minute)
	closedSummer := openedSummer.Add(3 * time.Minute)
	for _, tc := range []struct {
		name, kind, label, expectedTime, expectedDuration string
		opened, confirmed, closed                         time.Time
	}{
		{name: "DOWN winter", kind: "incident_opened", label: "Монитор недоступен", opened: openedWinter, confirmed: confirmedWinter, expectedTime: "15.01.2026 13:00 CET (UTC+01)", expectedDuration: "До подтверждения: 5 мин"},
		{name: "UP summer", kind: "incident_closed", label: "Монитор снова доступен", opened: openedSummer, confirmed: confirmedSummer, closed: closedSummer, expectedTime: "15.07.2026 14:00 CEST (UTC+02)", expectedDuration: "Длительность: 3 мин"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var closed *time.Time
			if tc.kind == "incident_closed" {
				closed = &tc.closed
			}
			body := pushIncidentBody(incidentcontracts.Notification{
				Kind:        tc.kind,
				Incident:    incidentcontracts.IncidentState{OpenedAt: tc.opened, ConfirmedAt: tc.confirmed, ClosedAt: closed},
				Observation: incidentcontracts.Observation{TimeZone: "Europe/Berlin", ProviderNames: providers},
			}, tc.label)
			if !strings.Contains(body, tc.expectedTime) || !strings.Contains(body, tc.expectedDuration) {
				t.Fatalf("push body omitted event-time zone/duration: %q", body)
			}
			if !strings.HasPrefix(body, tc.label+" · ") {
				t.Errorf("push body starts with unexpected incident label: %q", body)
			}
			if !strings.Contains(body, "Восстановлено: 15.07.2026 14:03 CEST (UTC+02)") && tc.kind == "incident_closed" {
				t.Errorf("UP body omitted summer recovery timestamp: %q", body)
			}
			if len(body) > 2048 {
				t.Errorf("push body has %d UTF-8 bytes, maximum is 2048", len(body))
			}
			if !utf8.ValidString(body) {
				t.Fatal("push body is not valid UTF-8")
			}
			for _, r := range body {
				if unicode.IsControl(r) {
					t.Errorf("push body contains control character U+%04X", r)
				}
			}
			providerAt := strings.Index(body, "Провайдер / CDN: ")
			if providerAt < 0 {
				t.Fatalf("push body has no provider label: %q", body)
			}
			providerLabel := strings.TrimPrefix(body[providerAt:], "Провайдер / CDN: ")
			if providerLabel != wantProviderLabel || utf8.RuneCountInString(providerLabel) != 160 {
				t.Errorf("bounded provider label has %d runes and value %q", utf8.RuneCountInString(providerLabel), providerLabel)
			}
			if strings.Contains(body, providers[3]) {
				t.Error("push body included a fourth provider name")
			}
		})
	}
}

func TestPushDeliveryRouteStaysFrozenAcrossTokenRotationAndRetry(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner, pair, installation := f.addDeliveryOwner(t, "frozen-route")
	f.push.cfg.Providers = []string{"rustore", "fcm", "huawei"}
	f.push.cfg.Senders = map[string]PushSender{
		"rustore": pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil }),
		"fcm":     pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil }),
		"huawei":  pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil }),
	}
	f.push.cfg.Sender = nil
	newToken := "fcm-destination-token"
	registration, err := f.push.UpdateRegistration(context.Background(), pair.Installation.Credential, PushRegistrationInput{
		RegistrationVersion: installation.RegistrationVersion, Provider: "fcm", Token: &newToken, Permission: "granted",
	})
	if err != nil || registration.Provider != "fcm" {
		t.Fatalf("register FCM route = (%+v, %v)", registration, err)
	}
	deliveryID := f.addDeliveryEvent(t, owner, installation.ID, "down", 1, "Frozen route monitor")
	attempt, err := f.push.ClaimPush(context.Background())
	if err != nil || attempt == nil || attempt.Provider != "fcm" || attempt.RegistrationToken != newToken {
		t.Fatalf("first claim = (%+v, %v), want FCM and current destination", attempt, err)
	}
	// A transient/ambiguous send result keeps the first destination immutable.
	if err := f.push.CompletePush(context.Background(), *attempt, pushProviderError{code: "request_failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.webhook.pool.Exec(context.Background(), `UPDATE notifications.push_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, deliveryID); err != nil {
		t.Fatal(err)
	}
	rotatedToken := "huawei-next-registration-token"
	registration, err = f.push.UpdateRegistration(context.Background(), pair.Installation.Credential, PushRegistrationInput{
		RegistrationVersion: registration.RegistrationVersion, Provider: "huawei", Token: &rotatedToken, Permission: "granted",
	})
	if err != nil {
		t.Fatalf("change provider during retry wait: %v", err)
	}
	listed, err := f.push.ListInstallations(context.Background(), owner.accountID)
	if err != nil {
		t.Fatalf("list account installations after provider route change: %v", err)
	}
	foundHuawei := false
	for _, item := range listed {
		if item.ID == installation.ID {
			foundHuawei = item.Provider == "huawei" && item.RegistrationVersion == registration.RegistrationVersion
		}
	}
	if !foundHuawei {
		t.Fatalf("account installation list omitted current Huawei provider/version: %+v", listed)
	}
	retry, err := f.push.ClaimPush(context.Background())
	if err != nil || retry == nil || retry.Provider != "fcm" || retry.RegistrationToken != newToken {
		t.Fatalf("retry claim = (%+v, %v), want frozen FCM destination despite live registration change", retry, err)
	}
	if err := f.push.CompletePush(context.Background(), *retry, nil); err != nil {
		t.Fatal(err)
	}
	var status, provider string
	var sealed []byte
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT status,delivery_provider,encrypted_destination FROM notifications.push_deliveries WHERE id=$1`, deliveryID).Scan(&status, &provider, &sealed); err != nil {
		t.Fatal(err)
	}
	if status != "accepted" || provider != "fcm" || len(sealed) == 0 {
		t.Fatalf("delivery after successful frozen-route retry = (%q,%q,%d bytes), want accepted FCM", status, provider, len(sealed))
	}
}

func TestPushDefaultRegistrationAndStaleInvalidTokenCAS(t *testing.T) {
	f := newPushIntegrationFixture(t)
	owner, pair, installation := f.addDeliveryOwner(t, "default-registration")
	// The legacy registration DTO omits provider; its established route remains Rustore.
	legacy, err := f.push.UpdateRegistration(context.Background(), pair.Installation.Credential, PushRegistrationInput{
		RegistrationVersion: installation.RegistrationVersion, Permission: "granted",
	})
	if err != nil || legacy.Provider != "rustore" {
		t.Fatalf("registration without provider = (%+v, %v), want legacy Rustore", legacy, err)
	}
	f.push.cfg.Providers = []string{"rustore", "fcm", "huawei"}
	f.push.cfg.Senders = map[string]PushSender{
		"rustore": pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil }),
		"fcm":     pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil }),
		"huawei":  pushSenderFunc(func(context.Context, string, map[string]string, time.Duration) error { return nil }),
	}
	oldHuaweiToken := "old-huawei-registration-token"
	huaweiRegistration, err := f.push.UpdateRegistration(context.Background(), pair.Installation.Credential, PushRegistrationInput{
		RegistrationVersion: legacy.RegistrationVersion, Provider: "huawei", Token: &oldHuaweiToken, Permission: "granted",
	})
	if err != nil || huaweiRegistration.Provider != "huawei" {
		t.Fatalf("switch installation route to Huawei = (%+v, %v)", huaweiRegistration, err)
	}
	deliveryID := f.addDeliveryEvent(t, owner, installation.ID, "down", 1, "Stale token monitor")
	attempt, err := f.push.ClaimPush(context.Background())
	if err != nil || attempt == nil || attempt.Provider != "huawei" || attempt.RegistrationToken != oldHuaweiToken {
		t.Fatalf("claim Huawei route = (%+v, %v)", attempt, err)
	}
	rotated := "new-huawei-registration-token"
	if _, err := f.push.UpdateRegistration(context.Background(), pair.Installation.Credential, PushRegistrationInput{
		RegistrationVersion: huaweiRegistration.RegistrationVersion, Provider: "huawei", Token: &rotated, Permission: "granted",
	}); err != nil {
		t.Fatalf("rotate Huawei registration before stale invalid-token response: %v", err)
	}
	if err := f.push.CompletePush(context.Background(), *attempt, pushProviderError{code: "invalid_token", permanent: true, invalidToken: true}); err != nil {
		t.Fatal(err)
	}
	state, err := f.push.Installation(context.Background(), pair.Installation.Credential)
	if err != nil || state.Status != "ready" || state.Provider != "huawei" || state.RegistrationVersion <= attempt.RegistrationVersion {
		t.Fatalf("stale invalid-token callback changed current registration = (%+v, %v)", state, err)
	}
	var valid bool
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT token_valid FROM notifications.push_installations WHERE id=$1`, installation.ID).Scan(&valid); err != nil || !valid {
		t.Fatalf("current token valid = (%t, %v), want true after stale callback", valid, err)
	}
	var status string
	if err := f.webhook.pool.QueryRow(context.Background(), `SELECT status FROM notifications.push_deliveries WHERE id=$1`, deliveryID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("invalid-token attempt delivery = (%q, %v), want failed without invalidating new token", status, err)
	}
}

func pushResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func mustPKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
