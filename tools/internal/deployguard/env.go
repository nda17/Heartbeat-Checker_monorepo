// Package deployguard ports deployment data checks without owning rollout order.
package deployguard

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

type Activation struct{ Relay, Messenger, AdminMFA, OAuth, YooKassaTest, Email, AndroidPush, YooKassaProduction bool }

var pushKeys = []string{"ANDROID_PUSH_ENABLED", "RUSTORE_PUSH_PROJECT_ID", "RUSTORE_PUSH_SERVICE_TOKEN", "FCM_PUSH_ENABLED", "FCM_PROJECT_ID", "FCM_SERVICE_ACCOUNT_BASE64", "HUAWEI_PUSH_ENABLED", "HUAWEI_PROJECT_ID", "HUAWEI_CLIENT_ID", "HUAWEI_CLIENT_SECRET"}
var messengerKeys = []string{"MESSENGER_NOTIFICATIONS_ENABLED", "TELEGRAM_INFORMER_WEBHOOK_SECRET", "MAX_INFORMER_WEBHOOK_SECRET", "TELEGRAM_BOT_INFORMER_TOKEN", "MAX_BOT_INFORMER_TOKEN", "TELEGRAM_INFORMER_BOT_USERNAME", "MAX_INFORMER_BOT_URL"}
var oauthKeys = []string{"YANDEX_CLIENT_ID", "YANDEX_CLIENT_SECRET", "YANDEX_REDIRECT_URI", "YANDEX_SCOPES", "VK_CLIENT_ID", "VK_CALLBACK_URL"}
var testKeys = []string{"BILLING_PROVIDER", "BILLING_TEST_ACCOUNT_IDS", "YOOKASSA_MODE", "YOOKASSA_TEST_SHOP_ID", "YOOKASSA_TEST_SECRET_KEY"}
var productionKeys = []string{"BILLING_PROVIDER", "YOOKASSA_MODE", "YOOKASSA_PRODUCTION_SHOP_ID", "YOOKASSA_PRODUCTION_SECRET_KEY"}
var relayTargets = map[string]string{"TELEGRAM_API_BASE_URL": "https://messengers-proxy.heartbeat-checker.ru/telegram-api", "TELEGRAM_API_PROXY_IP": "185.184.122.62"}

func match(p, s string) bool { return regexp.MustCompile(p).MatchString(s) }
func defaultValue(m map[string]string, k, fallback string) string {
	v, ok := m[k]
	if !ok {
		return fallback
	}
	return v
}

func ValidateEnv(candidate string, a Activation) error {
	prefix := "Configuration activation rejected"
	if a.Relay && !a.Messenger {
		prefix = "Relay activation rejected"
	}
	reject := func(message string) error { return errors.New(prefix + ": " + message) }
	allowed := map[string]bool{}
	add := func(active bool, keys []string) {
		if active {
			for _, key := range keys {
				allowed[key] = true
			}
		}
	}
	add(a.Relay, []string{"TELEGRAM_API_BASE_URL", "TELEGRAM_API_PROXY_IP"})
	add(a.Messenger, messengerKeys)
	add(a.AdminMFA, []string{"AUTH_MFA_ENCRYPTION_KEY"})
	add(a.OAuth, oauthKeys)
	add(a.YooKassaTest, testKeys)
	add(a.YooKassaProduction, productionKeys)
	add(a.Email, []string{"EMAIL_NOTIFICATIONS_ENABLED"})
	add(a.AndroidPush, pushKeys)
	old, err := deploydata.ReadPrivate(".env", 256<<10)
	if err != nil {
		return reject("invalid active env")
	}
	next, err := deploydata.ReadPrivate(candidate, 256<<10)
	if err != nil {
		return reject("invalid candidate env")
	}
	oldLines, ov, err := deploydata.Env(old, allowed)
	if err != nil {
		return reject("invalid env content or duplicate key")
	}
	newLines, nv, err := deploydata.Env(next, allowed)
	if err != nil {
		return reject("invalid env content or duplicate key")
	}
	if len(oldLines) != len(newLines) {
		return reject("changes outside selected keys")
	}
	for i := range oldLines {
		if !bytes.Equal(oldLines[i], newLines[i]) {
			return reject("changes outside selected keys")
		}
	}
	u := deploydata.Unquote
	if a.AndroidPush {
		if u(nv["ANDROID_PUSH_ENABLED"]) != "true" {
			return reject("Android push flag must be true")
		}
		if !match(`^[A-Za-z0-9_-]{1,128}$`, u(nv["RUSTORE_PUSH_PROJECT_ID"])) {
			return reject("invalid RuStore push project")
		}
		if !deploydata.Printable(u(nv["RUSTORE_PUSH_SERVICE_TOKEN"]), 32, 8192, "") {
			return reject("invalid RuStore push service token")
		}
		for _, key := range []string{"FCM_PUSH_ENABLED", "HUAWEI_PUSH_ENABLED"} {
			v := u(defaultValue(nv, key, "false"))
			if v != "true" && v != "false" {
				return reject("invalid cloud push flag")
			}
			if u(defaultValue(ov, key, "false")) == "true" && v != "true" {
				return reject("Android push activation cannot disable existing providers")
			}
		}
		for _, key := range pushKeys {
			if key == "ANDROID_PUSH_ENABLED" || key == "FCM_PUSH_ENABLED" || key == "HUAWEI_PUSH_ENABLED" {
				continue
			}
			if old := u(ov[key]); old != "" && old != u(nv[key]) {
				return reject("Android push activation cannot rotate existing configuration")
			}
		}
	}
	if a.Email && nv["EMAIL_NOTIFICATIONS_ENABLED"] != "true" {
		return reject("email notification flag must be true")
	}
	if a.YooKassaTest {
		for key, value := range map[string]string{"BILLING_PROVIDER": "yookassa", "YOOKASSA_MODE": "test", "BILLING_TEST_ACCOUNT_IDS": deploydata.OwnerID} {
			if u(nv[key]) != value {
				return reject("YooKassa activation must use the reviewed test mode and owner account")
			}
		}
		if !match(`^[0-9]{1,32}$`, u(nv["YOOKASSA_TEST_SHOP_ID"])) || !match(`^test_[^\s]{11,507}$`, u(nv["YOOKASSA_TEST_SECRET_KEY"])) {
			return reject("complete YooKassa test credentials are required")
		}
		for _, key := range []string{"YOOKASSA_TEST_SHOP_ID", "YOOKASSA_TEST_SECRET_KEY"} {
			if old := u(ov[key]); old != "" && old != u(nv[key]) {
				return reject("YooKassa activation cannot rotate existing credentials")
			}
		}
	}
	if a.YooKassaProduction {
		if u(nv["BILLING_PROVIDER"]) != "yookassa" || u(nv["YOOKASSA_MODE"]) != "production" {
			return reject("YooKassa production mode is required")
		}
		secret := u(nv["YOOKASSA_PRODUCTION_SECRET_KEY"])
		if !match(`^[0-9]{1,32}$`, u(nv["YOOKASSA_PRODUCTION_SHOP_ID"])) || !deploydata.Printable(secret, 16, 512, "'\\") || strings.HasPrefix(secret, "test_") {
			return reject("complete YooKassa production credentials are required")
		}
		for _, key := range []string{"YOOKASSA_PRODUCTION_SHOP_ID", "YOOKASSA_PRODUCTION_SECRET_KEY"} {
			if old := u(ov[key]); old != "" && old != u(nv[key]) {
				return reject("YooKassa production activation cannot rotate existing credentials")
			}
		}
	}
	if a.OAuth {
		for _, key := range oauthKeys {
			if u(ov[key]) != "" && nv[key] != ov[key] {
				return reject("existing OAuth credential cannot be rotated")
			}
		}
		if !match(`^[A-Za-z0-9_-]{16,128}$`, nv["YANDEX_CLIENT_ID"]) {
			return reject("invalid Yandex client ID")
		}
		if !deploydata.Printable(nv["YANDEX_CLIENT_SECRET"], 16, 512, "'\"$`\\#") {
			return reject("invalid Yandex client secret")
		}
		if nv["YANDEX_REDIRECT_URI"] != "https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback" {
			return reject("invalid Yandex callback URL")
		}
		if u(nv["YANDEX_SCOPES"]) != "login:email login:info" {
			return reject("invalid Yandex scopes")
		}
		if !match(`^[0-9]{4,32}$`, nv["VK_CLIENT_ID"]) {
			return reject("invalid VK client ID")
		}
		if nv["VK_CALLBACK_URL"] != "https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback" {
			return reject("invalid VK callback URL")
		}
	}
	if a.Relay {
		for key, value := range relayTargets {
			if nv[key] != value {
				return reject("unexpected Telegram endpoint")
			}
		}
	}
	if a.AdminMFA {
		if u(ov["AUTH_MFA_ENCRYPTION_KEY"]) != "" {
			return reject("existing admin MFA key cannot be rotated")
		}
		v := nv["AUTH_MFA_ENCRYPTION_KEY"]
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(decoded) != 32 || base64.StdEncoding.EncodeToString(decoded) != v {
			return reject("invalid admin MFA key")
		}
	}
	if a.Messenger {
		if nv["MESSENGER_NOTIFICATIONS_ENABLED"] != "true" {
			return reject("messenger flag must be true")
		}
		for _, key := range []string{"TELEGRAM_INFORMER_WEBHOOK_SECRET", "MAX_INFORMER_WEBHOOK_SECRET"} {
			v := nv[key]
			if !match(`^[A-Za-z0-9_-]{32,256}$`, v) {
				return reject("invalid Informer webhook secret")
			}
			if v == u(ov[strings.ReplaceAll(key, "INFORMER", "SUPPORT")]) {
				return reject("Informer webhook secret matches Support")
			}
		}
		token := ov["TELEGRAM_BOT_INFORMER_TOKEN"]
		if !match(`^[0-9]+:[A-Za-z0-9_-]+$`, u(token)) || nv["TELEGRAM_BOT_INFORMER_TOKEN"] != token {
			return reject("existing Telegram Informer token must remain unchanged")
		}
		if !deploydata.Printable(nv["MAX_BOT_INFORMER_TOKEN"], 16, 512, "'\"$`\\#") {
			return reject("invalid MAX Informer token")
		}
		username := nv["TELEGRAM_INFORMER_BOT_USERNAME"]
		if !match(`^[A-Za-z][A-Za-z0-9_]{4,31}$`, username) || !strings.HasSuffix(strings.ToLower(username), "bot") {
			return reject("invalid Telegram Informer bot username")
		}
		v := nv["MAX_INFORMER_BOT_URL"]
		parsed, err := url.Parse(v)
		if err != nil || !deploydata.ASCII([]byte(v)) || parsed.Scheme != "https" || parsed.Host != "max.ru" || !match(`^/[A-Za-z0-9_-]+$`, parsed.Path) || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.User != nil {
			return reject("invalid MAX Informer bot URL")
		}
	}
	return nil
}
