package stagepush

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pushFixture(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	workdir := filepath.Join(dir, "backend")
	if err := os.Mkdir(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("DATABASE_URL=test\nANDROID_PUSH_ENABLED=false\nKEEP_EXISTING=one\n")
	env := filepath.Join(workdir, ".env")
	if err := os.WriteFile(env, original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workdir)
	return workdir, original
}

func basicPushPayload(project, secret string) []byte {
	b, _ := json.Marshal(map[string]string{"projectId": project, "serviceToken": secret})
	return b
}

func TestStageWritesOnlyAllowlistedKeysAndPreservesLiteralSecret(t *testing.T) {
	workdir, original := pushFixture(t)
	secret := "$synthetic-token-0123456789-abcdefghijklmnop"
	if err := Stage(workdir, bytes.NewReader(basicPushPayload("project_123", secret))); err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(filepath.Join(workdir, ".env.next"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ANDROID_PUSH_ENABLED=true", "RUSTORE_PUSH_PROJECT_ID=project_123", "RUSTORE_PUSH_SERVICE_TOKEN='" + secret + "'", "DATABASE_URL=test", "KEEP_EXISTING=one"} {
		if !bytes.Contains(candidate, []byte(want)) {
			t.Fatalf("candidate missing %q: %s", want, candidate)
		}
	}
	if bytes.Contains(candidate, []byte("ANDROID_PUSH_ENABLED=false")) {
		t.Fatal("candidate retained disabled Android flag")
	}
	if got := mustStat(t, filepath.Join(workdir, ".env.next")).Mode().Perm(); got != 0600 {
		t.Fatalf("candidate mode = %o, want 600", got)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("source env mutated: %q, %v", got, err)
	}
}

func TestStageRejectsUnframedDuplicateOversizedAndUnsafeInput(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"unframed", append(basicPushPayload("project", strings.Repeat("x", 32)), []byte("\nextra")...)},
		{"duplicate key", []byte(`{"projectId":"one","projectId":"two","serviceToken":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`)},
		{"short secret", basicPushPayload("project", "short")},
		{"oversized secret", basicPushPayload("project", strings.Repeat("x", 8193))},
		{"quote injection", basicPushPayload("project", strings.Repeat("x", 31)+"'x")},
		{"unsafe project", basicPushPayload("bad/project", strings.Repeat("x", 32))},
		{"oversized document", bytes.Repeat([]byte(" "), 131073)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workdir, original := pushFixture(t)
			if err := Stage(workdir, bytes.NewReader(tc.payload)); err == nil {
				t.Fatal("unsafe stage input accepted")
			}
			if _, err := os.Lstat(filepath.Join(workdir, ".env.next")); !os.IsNotExist(err) {
				t.Fatalf("candidate created after rejection: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || !bytes.Equal(got, original) {
				t.Fatalf("source env changed after rejection: %v", err)
			}
		})
	}
}

func TestStageRefusesToOverwriteExistingCandidate(t *testing.T) {
	workdir, _ := pushFixture(t)
	path := filepath.Join(workdir, ".env.next")
	old := []byte("reviewed candidate\n")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Stage(workdir, bytes.NewReader(basicPushPayload("project", strings.Repeat("x", 32)))); err == nil {
		t.Fatal("existing candidate overwritten")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, old) {
		t.Fatalf("existing candidate changed: %q, %v", got, err)
	}
}

func TestStageAddsFCMAndPreservesProductionBillingBytes(t *testing.T) {
	workdir, original := pushFixture(t)
	billing := []byte("BILLING_PROVIDER=yookassa\nYOOKASSA_MODE=production\nYOOKASSA_PRODUCTION_SHOP_ID=654321\nYOOKASSA_PRODUCTION_SECRET_KEY='production_synthetic_secret_123'\n")
	original = append([]byte("DATABASE_URL=test\nANDROID_PUSH_ENABLED=false\n"), billing...)
	original = append(original, []byte("KEEP_EXISTING=one\n")...)
	if err := os.WriteFile(filepath.Join(workdir, ".env"), original, 0600); err != nil {
		t.Fatal(err)
	}
	account := map[string]string{"type": "service_account", "project_id": "synthetic-fcm-project", "client_email": "push@synthetic-fcm-project.iam.gserviceaccount.com", "private_key": "synthetic-private-key", "token_uri": "https://oauth2.googleapis.com/token"}
	accountJSON, _ := json.Marshal(account)
	encoded := base64.StdEncoding.EncodeToString(accountJSON)
	payload := map[string]any{"projectId": "synthetic-project", "serviceToken": "$synthetic-rustore-service-token-0123456789", "fcm": map[string]string{"projectId": "synthetic-fcm-project", "serviceAccountBase64": encoded}}
	b, _ := json.Marshal(payload)
	if err := Stage(workdir, bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(filepath.Join(workdir, ".env.next"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FCM_PUSH_ENABLED=true", "FCM_PROJECT_ID=synthetic-fcm-project", "FCM_SERVICE_ACCOUNT_BASE64=" + encoded, "RUSTORE_PUSH_SERVICE_TOKEN='$synthetic-rustore-service-token-0123456789'"} {
		if !bytes.Contains(candidate, []byte(want)) {
			t.Fatalf("FCM candidate missing %q", want)
		}
	}
	for _, line := range bytes.Split(billing, []byte("\n")) {
		if len(line) > 0 && !bytes.Contains(candidate, line) {
			t.Fatalf("billing setting changed or dropped: %q", line)
		}
	}
	if got, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("source billing config changed: %v", err)
	}
}

func TestStageRejectsCloudProjectMismatchAndUnknownNestedFields(t *testing.T) {
	account := map[string]string{"type": "service_account", "project_id": "synthetic-fcm-project", "client_email": "push@synthetic-fcm-project.iam.gserviceaccount.com", "private_key": "synthetic-private-key", "token_uri": "https://oauth2.googleapis.com/token"}
	raw, _ := json.Marshal(account)
	encoded := base64.StdEncoding.EncodeToString(raw)
	valid := map[string]any{"projectId": "synthetic-project", "serviceToken": "synthetic-rustore-service-token-0123456789", "fcm": map[string]any{"projectId": "synthetic-fcm-project", "serviceAccountBase64": encoded}, "huawei": map[string]any{"projectId": "12345", "clientId": "67890", "clientSecret": "synthetic_huawei_secret_0123456789"}}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"fcm project mismatch", func(m map[string]any) { m["fcm"].(map[string]any)["projectId"] = "different-fcm-project" }},
		{"unknown fcm field", func(m map[string]any) { m["fcm"].(map[string]any)["privateKey"] = "must-not-be-accepted" }},
		{"unknown Huawei field", func(m map[string]any) {
			m["huawei"] = map[string]any{"projectId": "12345", "clientId": "67890", "region": "unknown"}
		}},
		{"partial Huawei", func(m map[string]any) { m["huawei"] = map[string]any{"projectId": "12345", "clientId": "67890"} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workdir, _ := pushFixture(t)
			b, _ := json.Marshal(valid)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			tc.mutate(m)
			b, _ = json.Marshal(m)
			if err := Stage(workdir, bytes.NewReader(b)); err == nil {
				t.Fatal("invalid nested push config accepted")
			}
			if _, err := os.Lstat(filepath.Join(workdir, ".env.next")); !os.IsNotExist(err) {
				t.Fatalf("candidate created after rejected config: %v", err)
			}
		})
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	i, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return i
}
