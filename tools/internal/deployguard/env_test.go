package deployguard

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeEnvFixture(t *testing.T, old, next string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(dir, ".env.next")
	if err := os.WriteFile(candidate, []byte(next), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return candidate
}
func envText(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key + "=" + values[key] + "\n")
	}
	return b.String()
}
func mergeEnv(base, changes map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range base {
		result[key] = value
	}
	for key, value := range changes {
		result[key] = value
	}
	return result
}

func TestValidateEnvScopesEmailAndAndroidPushAndPreservesRawOtherBytes(t *testing.T) {
	old := map[string]string{"DATABASE_URL": "postgresql://fixture", "EMAIL_NOTIFICATIONS_ENABLED": "false", "ANDROID_PUSH_ENABLED": "false", "KEEP_EXISTING": "literal=$HOME"}
	candidate := mergeEnv(old, map[string]string{"EMAIL_NOTIFICATIONS_ENABLED": "true"})
	path := writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{Email: true}); err != nil {
		t.Fatal(err)
	}
	candidate["KEEP_EXISTING"] = "changed"
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{Email: true}); err == nil {
		t.Fatal("email activation accepted unrelated config change")
	}
	old = map[string]string{"DATABASE_URL": "postgresql://fixture", "ANDROID_PUSH_ENABLED": "false", "RUSTORE_PUSH_PROJECT_ID": "", "RUSTORE_PUSH_SERVICE_TOKEN": "", "FCM_PUSH_ENABLED": "false", "HUAWEI_PUSH_ENABLED": "false"}
	changes := map[string]string{"ANDROID_PUSH_ENABLED": "true", "RUSTORE_PUSH_PROJECT_ID": "project_123", "RUSTORE_PUSH_SERVICE_TOKEN": "'$synthetic-token-0123456789-abcdefghijklmnop'"}
	candidate = mergeEnv(old, changes)
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{AndroidPush: true}); err != nil {
		t.Fatalf("valid Android push activation rejected: %v", err)
	}
	old["RUSTORE_PUSH_SERVICE_TOKEN"] = "'$existing-token-0123456789-abcdefghijklmnop'"
	candidate = mergeEnv(old, changes)
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{AndroidPush: true}); err == nil {
		t.Fatal("RuStore key rotation accepted")
	}
}

func TestValidateEnvGuardsMFAOAuthAndQuotedExistingTelegramToken(t *testing.T) {
	old := map[string]string{"DATABASE_URL": "postgresql://fixture"}
	mfa := base64.StdEncoding.EncodeToString(make([]byte, 32))
	candidate := mergeEnv(old, map[string]string{"AUTH_MFA_ENCRYPTION_KEY": mfa})
	path := writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{AdminMFA: true}); err != nil {
		t.Fatalf("valid one-time MFA key rejected: %v", err)
	}
	old["AUTH_MFA_ENCRYPTION_KEY"] = mfa
	candidate = mergeEnv(old, map[string]string{"AUTH_MFA_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))})
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{AdminMFA: true}); err == nil {
		t.Fatal("active MFA key rotation accepted")
	}
	old = map[string]string{"DATABASE_URL": "postgresql://fixture"}
	oauth := map[string]string{"YANDEX_CLIENT_ID": "synthetic-client-123456", "YANDEX_CLIENT_SECRET": "synthetic-yandex-secret-123", "YANDEX_REDIRECT_URI": "https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback", "YANDEX_SCOPES": "'login:email login:info'", "VK_CLIENT_ID": "123456", "VK_CALLBACK_URL": "https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback"}
	candidate = mergeEnv(old, oauth)
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{OAuth: true}); err != nil {
		t.Fatalf("valid OAuth candidate rejected: %v", err)
	}
	old = mergeEnv(old, map[string]string{"TELEGRAM_BOT_INFORMER_TOKEN": "'123456:unchanged-token'", "TELEGRAM_SUPPORT_WEBHOOK_SECRET": "support_secret_123456789012345678901234"})
	messenger := map[string]string{"MESSENGER_NOTIFICATIONS_ENABLED": "true", "TELEGRAM_INFORMER_WEBHOOK_SECRET": "telegram_informer_secret_1234567890", "MAX_INFORMER_WEBHOOK_SECRET": "max_informer_secret_123456789012", "MAX_BOT_INFORMER_TOKEN": "max-token-synthetic-0123456789", "TELEGRAM_INFORMER_BOT_USERNAME": "HeartbeatInformerBot", "MAX_INFORMER_BOT_URL": "https://max.ru/heartbeat_bot"}
	candidate = mergeEnv(old, messenger)
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{Messenger: true}); err != nil {
		t.Fatalf("valid informer candidate rejected: %v", err)
	}
	candidate["TELEGRAM_BOT_INFORMER_TOKEN"] = "123456:unchanged-token"
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{Messenger: true}); err == nil {
		t.Fatal("quote-normalized existing token mutation accepted")
	}
}

func TestValidateEnvRequiresReviewedYooKassaModesAndBlocksCredentialRotation(t *testing.T) {
	old := map[string]string{"DATABASE_URL": "postgresql://fixture", "YOOKASSA_TEST_SHOP_ID": "", "YOOKASSA_TEST_SECRET_KEY": ""}
	testMode := map[string]string{"BILLING_PROVIDER": "yookassa", "YOOKASSA_MODE": "test", "BILLING_TEST_ACCOUNT_IDS": "51ec7d19-80c4-4fb6-8edb-79dc7abe3120", "YOOKASSA_TEST_SHOP_ID": "123456", "YOOKASSA_TEST_SECRET_KEY": "'test_synthetic_secret_123'"}
	candidate := mergeEnv(old, testMode)
	path := writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{YooKassaTest: true}); err != nil {
		t.Fatalf("valid owner-only sandbox rejected: %v", err)
	}
	old = mergeEnv(old, testMode)
	testMode["YOOKASSA_TEST_SECRET_KEY"] = "'test_changed_secret_123'"
	candidate = mergeEnv(old, testMode)
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{YooKassaTest: true}); err == nil {
		t.Fatal("sandbox credential rotation accepted")
	}
	old = map[string]string{"DATABASE_URL": "postgresql://fixture", "YOOKASSA_PRODUCTION_SHOP_ID": "", "YOOKASSA_PRODUCTION_SECRET_KEY": ""}
	prod := map[string]string{"BILLING_PROVIDER": "yookassa", "YOOKASSA_MODE": "production", "YOOKASSA_PRODUCTION_SHOP_ID": "654321", "YOOKASSA_PRODUCTION_SECRET_KEY": "production_synthetic_secret_123"}
	candidate = mergeEnv(old, prod)
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{YooKassaProduction: true}); err != nil {
		t.Fatalf("valid production credentials rejected: %v", err)
	}
	prod["YOOKASSA_PRODUCTION_SECRET_KEY"] = "test_synthetic_secret_123"
	candidate = mergeEnv(old, prod)
	path = writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{YooKassaProduction: true}); err == nil {
		t.Fatal("test credential accepted for production")
	}
}

func TestValidateEnvActivatesNativeSDKTestWithoutChangingHostedProduction(t *testing.T) {
	old := map[string]string{
		"BILLING_PROVIDER": "yookassa", "YOOKASSA_MODE": "production",
		"YOOKASSA_PRODUCTION_SHOP_ID": "654321", "YOOKASSA_PRODUCTION_SECRET_KEY": "production_existing_secret",
		"YOOKASSA_NATIVE_SDK_ENABLED": "false", "YOOKASSA_TEST_SHOP_ID": "123456", "YOOKASSA_TEST_SECRET_KEY": "'test_synthetic_secret_123'",
	}
	candidate := mergeEnv(old, map[string]string{
		"BILLING_TEST_ACCOUNT_IDS": "51ec7d19-80c4-4fb6-8edb-79dc7abe3120",
		"YOOKASSA_TEST_SHOP_ID":    "123456", "YOOKASSA_TEST_SECRET_KEY": "'test_synthetic_secret_123'",
		"YOOKASSA_NATIVE_SDK_ENABLED": "true", "YOOKASSA_NATIVE_SDK_MODE": "test",
	})
	path := writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{NativeSDKTest: true}); err != nil {
		t.Fatalf("valid hosted-live/native-test activation rejected: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"hosted production mode drift", func(m map[string]string) { m["YOOKASSA_MODE"] = "test" }},
		{"wrong owner", func(m map[string]string) { m["BILLING_TEST_ACCOUNT_IDS"] = "61ec7d19-80c4-4fb6-8edb-79dc7abe3120" }},
		{"test credential rotation", func(m map[string]string) { m["YOOKASSA_TEST_SECRET_KEY"] = "'test_changed_secret_123'" }},
		{"SDK is not explicitly enabled in test mode", func(m map[string]string) { m["YOOKASSA_NATIVE_SDK_MODE"] = "production" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := mergeEnv(old, map[string]string{
				"BILLING_TEST_ACCOUNT_IDS": "51ec7d19-80c4-4fb6-8edb-79dc7abe3120",
				"YOOKASSA_TEST_SHOP_ID":    "123456", "YOOKASSA_TEST_SECRET_KEY": "'test_synthetic_secret_123'",
				"YOOKASSA_NATIVE_SDK_ENABLED": "true", "YOOKASSA_NATIVE_SDK_MODE": "test",
			})
			tc.mutate(mutated)
			path := writeEnvFixture(t, envText(old), envText(mutated))
			if err := ValidateEnv(path, Activation{NativeSDKTest: true}); err == nil {
				t.Fatal("unsafe native SDK activation accepted")
			}
		})
	}
}

func TestValidateEnvRequiresRelayTargetsAndRejectsSymlinkOrBadModeCandidate(t *testing.T) {
	old := map[string]string{"DATABASE_URL": "postgresql://fixture", "TELEGRAM_API_BASE_URL": "", "TELEGRAM_API_PROXY_IP": ""}
	candidate := mergeEnv(old, relayTargets)
	path := writeEnvFixture(t, envText(old), envText(candidate))
	if err := ValidateEnv(path, Activation{Relay: true}); err != nil {
		t.Fatalf("valid relay target rejected: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envText(old)), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "candidate-target")
	if err := os.WriteFile(target, []byte(envText(candidate)), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".env.next")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := ValidateEnv(link, Activation{Relay: true}); err == nil {
		t.Fatal("symlink candidate accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte(envText(candidate)), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(link, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnv(link, Activation{Relay: true}); err == nil {
		t.Fatal("candidate with broad permissions accepted")
	}
}
