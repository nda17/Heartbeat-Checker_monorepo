package deployguard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

type Service struct {
	Image       string            `json:"image"`
	Environment map[string]string `json:"environment"`
}
type Compose map[string]Service

func ReadCompose(input io.Reader) (Compose, error) {
	b, err := deploydata.ReadBounded(input, 2<<20)
	if err != nil {
		return nil, deploydata.ErrInvalid
	}
	if _, err = deploydata.Object(b); err != nil {
		return nil, err
	}
	var value struct {
		Services Compose `json:"services"`
	}
	if json.Unmarshal(b, &value) != nil || value.Services == nil {
		return nil, deploydata.ErrInvalid
	}
	return value.Services, nil
}

func CheckCompose(input io.Reader) error {
	c, err := ReadCompose(input)
	if err != nil {
		return errors.New("Invalid Compose configuration")
	}
	api, worker := c["api"].Environment, c["worker"].Environment
	invalid := func(message string) error { return errors.New(message) }
	raw := api["DATABASE_URL"]
	u, err := url.Parse(raw)
	if err != nil {
		return invalid("DATABASE_URL must target this Compose PostgreSQL with matching credentials")
	}
	username, password := "", ""
	hasPassword := false
	postgresPassword, hasPostgresPassword := c["postgres"].Environment["POSTGRES_PASSWORD"]
	if _, authority, ok := strings.Cut(raw, "://"); ok {
		authority = strings.SplitN(authority, "/", 2)[0]
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			username, password, hasPassword = strings.Cut(authority[:at], ":")
		}
	}
	q, queryErr := url.ParseQuery(u.RawQuery)
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") || !strings.EqualFold(u.Hostname(), "postgres") || u.Port() != "5432" || username != "heartbeat" || !hasPassword || !hasPostgresPassword || password != postgresPassword || u.EscapedPath() != "/heartbeat" || queryErr != nil || len(q) != 1 || len(q["sslmode"]) != 1 || q.Get("sslmode") != "disable" {
		return invalid("DATABASE_URL must target this Compose PostgreSQL with matching credentials")
	}
	boolFlag := func(s string) bool { return s == "true" || s == "false" }
	if !boolFlag(api["EMAIL_NOTIFICATIONS_ENABLED"]) || api["EMAIL_NOTIFICATIONS_ENABLED"] != worker["EMAIL_NOTIFICATIONS_ENABLED"] {
		return invalid("API and worker email notification flags must match")
	}
	other := []string{"probe", "postgres", "caddy", "migrate"}
	contains := func(roles, keys []string) bool {
		for _, role := range roles {
			for _, key := range keys {
				if _, ok := c[role].Environment[key]; ok {
					return true
				}
			}
		}
		return false
	}
	if contains(other, []string{"EMAIL_NOTIFICATIONS_ENABLED"}) {
		return invalid("Email notifications must be scoped to API and worker only")
	}
	apiPush := defaultValue(api, "ANDROID_PUSH_ENABLED", "false")
	if !boolFlag(apiPush) || apiPush != defaultValue(worker, "ANDROID_PUSH_ENABLED", "false") {
		return invalid("API and worker Android push flags must match")
	}
	if api["RUSTORE_PUSH_PROJECT_ID"] != worker["RUSTORE_PUSH_PROJECT_ID"] {
		return invalid("API and worker RuStore project identifiers must match")
	}
	if contains([]string{"api"}, []string{"RUSTORE_PUSH_SERVICE_TOKEN"}) || contains(other, []string{"ANDROID_PUSH_ENABLED", "RUSTORE_PUSH_PROJECT_ID", "RUSTORE_PUSH_SERVICE_TOKEN"}) {
		return invalid("RuStore credentials must be scoped to the worker only")
	}
	if apiPush == "true" && (worker["RUSTORE_PUSH_PROJECT_ID"] == "" || !deploydata.Printable(worker["RUSTORE_PUSH_SERVICE_TOKEN"], 32, 8192, "")) {
		return invalid("Enabled Android push requires complete worker configuration")
	}
	if contains([]string{"api", "probe", "postgres", "caddy", "migrate"}, []string{"FCM_SERVICE_ACCOUNT_BASE64", "HUAWEI_CLIENT_ID", "HUAWEI_CLIENT_SECRET"}) {
		return invalid("Cloud push sending credentials must be worker-only")
	}
	for _, spec := range []struct {
		flag, project string
		credentials   []string
	}{{"FCM_PUSH_ENABLED", "FCM_PROJECT_ID", []string{"FCM_SERVICE_ACCOUNT_BASE64"}}, {"HUAWEI_PUSH_ENABLED", "HUAWEI_PROJECT_ID", []string{"HUAWEI_CLIENT_ID", "HUAWEI_CLIENT_SECRET"}}} {
		flag := defaultValue(api, spec.flag, "false")
		if !boolFlag(flag) || flag != defaultValue(worker, spec.flag, "false") || api[spec.project] != worker[spec.project] {
			return invalid("Cloud push public configuration must match API and worker")
		}
		if flag == "true" {
			if apiPush != "true" || worker[spec.project] == "" {
				return invalid("Enabled cloud push requires complete worker configuration")
			}
			for _, key := range spec.credentials {
				if worker[key] == "" {
					return invalid("Enabled cloud push requires complete worker configuration")
				}
			}
		}
	}
	if worker["SMTP_TLS_MODE"] != "implicit_tls" && worker["SMTP_TLS_MODE"] != "starttls" {
		return invalid("SMTP_TLS_MODE must use verified TLS")
	}
	mfa := api["AUTH_MFA_ENCRYPTION_KEY"]
	decoded, err := base64.StdEncoding.DecodeString(mfa)
	if err != nil || strings.ContainsAny(mfa, "\r\n") || len(decoded) != 32 {
		return invalid("Production admin MFA key is required")
	}
	if contains([]string{"worker", "probe"}, []string{"AUTH_MFA_ENCRYPTION_KEY"}) {
		return invalid("Admin MFA key must be scoped to API only")
	}
	if contains([]string{"worker", "probe"}, oauthKeys) {
		return invalid("OAuth configuration must be scoped to API only")
	}
	yk := []string{"BILLING_TEST_ACCOUNT_IDS", "YOOKASSA_MODE", "YOOKASSA_TEST_SHOP_ID", "YOOKASSA_TEST_SECRET_KEY", "YOOKASSA_PRODUCTION_SHOP_ID", "YOOKASSA_PRODUCTION_SECRET_KEY"}
	for _, key := range yk {
		if api[key] != worker[key] {
			return invalid("API and worker must share the same YooKassa policy")
		}
	}
	if contains(other, yk) {
		return invalid("YooKassa configuration must be scoped to API and worker only")
	}
	if api["BILLING_PROVIDER"] == "yookassa" {
		switch api["YOOKASSA_MODE"] {
		case "test":
			if api["BILLING_TEST_ACCOUNT_IDS"] != deploydata.OwnerID || !match(`^[0-9]+$`, api["YOOKASSA_TEST_SHOP_ID"]) || !strings.HasPrefix(api["YOOKASSA_TEST_SECRET_KEY"], "test_") {
				return invalid("YooKassa test mode requires the reviewed owner-only sandbox")
			}
		case "production":
			if !match(`^[0-9]+$`, api["YOOKASSA_PRODUCTION_SHOP_ID"]) || len(api["YOOKASSA_PRODUCTION_SECRET_KEY"]) < 16 || len(api["YOOKASSA_PRODUCTION_SECRET_KEY"]) > 512 || strings.HasPrefix(api["YOOKASSA_PRODUCTION_SECRET_KEY"], "test_") {
				return invalid("YooKassa production mode requires the selected production shop")
			}
		default:
			return invalid("YooKassa mode must be explicit")
		}
	}
	return nil
}

// WriteS3Env exports only five selected values; Compose doubles literal dollars.
func WriteS3Env(input io.Reader, output string) error {
	c, err := ReadCompose(input)
	if err != nil {
		return deploydata.ErrInvalid
	}
	env := c["backup"].Environment
	keys := []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_REGION", "BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY"}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		v := env[key]
		if v == "" || strings.ContainsAny(v, "\r\n\x00") || strings.Contains(strings.ReplaceAll(v, "$$", ""), "$") {
			return deploydata.ErrInvalid
		}
		b.WriteString(key + "=" + strings.ReplaceAll(v, "$$", "$") + "\n")
	}
	// The shell creates this tempfile before passing it; never follow a link or
	// change its permissions. Tests may use a fresh destination.
	if _, err := os.Lstat(output); os.IsNotExist(err) {
		return deploydata.WriteExclusive(output, []byte(b.String()))
	}
	if _, err := deploydata.ReadPrivate(output, 1<<20); err != nil {
		return deploydata.ErrInvalid
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return deploydata.ErrInvalid
	}
	_, err = f.WriteString(b.String())
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return deploydata.ErrInvalid
	}
	return nil
}

func ProbeSmoke(ctx context.Context, input io.Reader, client *http.Client) error {
	c, err := ReadCompose(input)
	if err != nil {
		return deploydata.ErrInvalid
	}
	secret := c["probe"].Environment["PROBE_SHARED_SECRET"]
	if secret == "" {
		return deploydata.ErrInvalid
	}
	if client == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		client = &http.Client{Transport: t}
	}
	h := *client
	h.Timeout = 20 * time.Second
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, target := range []string{"https://heartbeat-checker.ru/", "http://169.254.169.254/"} {
		body, _ := json.Marshal(map[string]any{"runId": "release-smoke", "attemptToken": "release-smoke", "configVersion": 1, "url": target, "method": "GET", "expectedStatus": 200, "bodyContains": ""})
		r, err := http.NewRequestWithContext(ctx, "POST", "http://172.30.80.2:8090/internal/v1/check", strings.NewReader(string(body)))
		if err != nil {
			return deploydata.ErrInvalid
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		response, err := h.Do(r)
		if err != nil {
			return deploydata.ErrInvalid
		}
		b, err := deploydata.ReadBounded(response.Body, 64<<10)
		response.Body.Close()
		if err != nil {
			return deploydata.ErrInvalid
		}
		m, err := deploydata.Object(b)
		if err != nil {
			return deploydata.ErrInvalid
		}
		if target == "https://heartbeat-checker.ru/" {
			if response.StatusCode != 200 || m["outcome"] != "UP" || m["httpStatus"] != json.Number("200") {
				return errors.New("Probe could not resolve and GET the own public site")
			}
		} else if response.StatusCode != 400 || m["error"] != "unsupported_target" {
			return errors.New("Probe did not reject metadata address")
		}
	}
	return nil
}
