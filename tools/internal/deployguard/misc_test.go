package deployguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validOwnerPasswordHash = "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func ownerFixture() []byte {
	return []byte(`{"accountId":"51ec7d19-80c4-4fb6-8edb-79dc7abe3120","name":"Synthetic Owner","email":"khv1702@gmail.com","passwordHash":"` + validOwnerPasswordHash + `","quota":1000,"validUntil":"2027-10-18T00:00:00Z"}`)
}

func TestEnableEmailChangesOnlyOneFlagAndRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	old := []byte("DATABASE_URL=fixture\nEMAIL_NOTIFICATIONS_ENABLED=false\nKEEP_EXISTING=literal=$HOME\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), old, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	output := filepath.Join(dir, ".env.next")
	if err := EnableEmail(output); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("EMAIL_NOTIFICATIONS_ENABLED=true\n")) || !bytes.Contains(got, []byte("KEEP_EXISTING=literal=$HOME")) {
		t.Fatalf("email candidate changed unrelated bytes: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("EMAIL_NOTIFICATIONS_ENABLED=false\nEMAIL_NOTIFICATIONS_ENABLED=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := EnableEmail(filepath.Join(dir, "duplicate.next")); err == nil {
		t.Fatal("duplicate email flag accepted")
	}
}

func TestOwnerPayloadValidationAndSQLFramingStayPrivateAndExact(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "owner.json")
	if err := os.WriteFile(payload, ownerFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwner(payload); err != nil {
		t.Fatalf("synthetic valid owner payload rejected: %v", err)
	}
	if err := os.Chmod(payload, 0640); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwner(payload); err == nil {
		t.Fatal("owner payload with broad mode accepted")
	}
	if err := os.Chmod(payload, 0600); err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(dir, "owner.sql")
	if err := os.WriteFile(template, []byte("INSERT INTO owner_import(payload,sha256,source_sha) VALUES ('__OWNER_PAYLOAD_ROW__');\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := OwnerSQL(payload, template, strings.Repeat("a", 40), &rendered); err != nil {
		t.Fatal(err)
	}
	encoded := hex.EncodeToString(ownerFixture())
	hash := sha256.Sum256(ownerFixture())
	for _, want := range []string{encoded, hex.EncodeToString(hash[:]), strings.Repeat("a", 40)} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("owner SQL framing lost %q", want)
		}
	}
	if strings.Contains(rendered.String(), "Synthetic Owner") || strings.Contains(rendered.String(), "passwordHash") {
		t.Fatal("owner SQL did not encode payload as data")
	}
	if err := os.WriteFile(template, []byte("__OWNER_PAYLOAD_ROW__ __OWNER_PAYLOAD_ROW__"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := OwnerSQL(payload, template, strings.Repeat("a", 40), io.Discard); err == nil {
		t.Fatal("ambiguous owner SQL marker accepted")
	}
}

func TestCheckBackupEnvAllowsOnlyApprovedKeysAndRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".backup.env")
	valid := []byte("BACKUP_S3_ENDPOINT=https://s3.twcstorage.ru\nBACKUP_S3_BUCKET=heartbeat-data-1\nBACKUP_S3_REGION=ru-1\nBACKUP_AGE_RECIPIENT=age1synthetic\nTELEGRAM_INTERNAL_CHAT_ID=-1004317880847\n")
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckBackupEnv(path); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{append(append([]byte(nil), valid...), []byte("JWT_SECRET=must-not-leak\n")...), append(append([]byte(nil), valid...), []byte("BACKUP_S3_REGION=duplicate\n")...), []byte("bad key=value\n")} {
		if err := os.WriteFile(path, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if err := CheckBackupEnv(path); err == nil {
			t.Fatalf("invalid backup env accepted: %s", invalid)
		}
	}
}

func TestFrameWritesValidatedRegistryAndSelectedJSONWithoutPartialSecrets(t *testing.T) {
	lookup := map[string]string{"GH_TOKEN": "synthetic-token", "ACTIVATE_ANDROID_PUSH": "true", "RUSTORE_PUSH_PROJECT_ID": "synthetic-project", "RUSTORE_PUSH_SERVICE_TOKEN": "synthetic-rustore-service-token-0123456789", "FCM_PROJECT_ID": "synthetic-fcm-project", "FCM_SERVICE_ACCOUNT_BASE64": "eyJ0eXBlIjoic2VydmljZV9hY2NvdW50In0="}
	var out bytes.Buffer
	if err := Frame(&out, func(key string) string { return lookup[key] }); err != nil {
		t.Fatal(err)
	}
	first, payload, ok := bytes.Cut(out.Bytes(), []byte("\n"))
	if !ok || string(first) != "synthetic-token" {
		t.Fatalf("registry frame header malformed: %q", out.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["projectId"] != "synthetic-project" || parsed["serviceToken"] != lookup["RUSTORE_PUSH_SERVICE_TOKEN"] {
		t.Fatalf("push payload mismatch: %#v", parsed)
	}
	lookup["RUSTORE_PUSH_SERVICE_TOKEN"] = ""
	out.Reset()
	if err := Frame(&out, func(key string) string { return lookup[key] }); err == nil || out.Len() != 0 {
		t.Fatalf("failed frame leaked partial registry secret %q (%v)", out.String(), err)
	}
}

func TestFrameIncludesHuaweiOnlyForExplicitAndroidOptIn(t *testing.T) {
	lookup := map[string]string{
		"GH_TOKEN": "synthetic-token", "ACTIVATE_ANDROID_PUSH": "true", "ACTIVATE_HUAWEI_PUSH": "true",
		"RUSTORE_PUSH_PROJECT_ID": "rustore-project", "RUSTORE_PUSH_SERVICE_TOKEN": "synthetic-rustore-service-token-0123456789",
		"FCM_PROJECT_ID": "fcm-project", "FCM_SERVICE_ACCOUNT_BASE64": "eyJ0eXBlIjoic2VydmljZV9hY2NvdW50In0=",
		"HUAWEI_PROJECT_ID": "11111", "HUAWEI_CLIENT_ID": "22222", "HUAWEI_CLIENT_SECRET": "synthetic_huawei_secret_0123456789",
	}
	frame := func(t *testing.T) map[string]any {
		t.Helper()
		var out bytes.Buffer
		if err := Frame(&out, func(key string) string { return lookup[key] }); err != nil {
			t.Fatalf("Frame() error = %v", err)
		}
		_, payload, ok := bytes.Cut(out.Bytes(), []byte("\n"))
		if !ok {
			t.Fatalf("missing registry frame delimiter: %q", out.String())
		}
		var parsed map[string]any
		if err := json.Unmarshal(payload, &parsed); err != nil {
			t.Fatalf("decode framed push config: %v", err)
		}
		return parsed
	}

	parsed := frame(t)
	huawei, ok := parsed["huawei"].(map[string]any)
	if !ok || len(huawei) != 3 || huawei["projectId"] != "11111" || huawei["clientId"] != "22222" || huawei["clientSecret"] != lookup["HUAWEI_CLIENT_SECRET"] {
		t.Fatalf("Huawei config did not preserve its distinct project and OAuth client IDs: %#v", parsed["huawei"])
	}
	if huawei["projectId"] == huawei["clientId"] {
		t.Fatal("fixture must distinguish Huawei project ID from client ID")
	}

	for _, activate := range []string{"", "false"} {
		lookup["ACTIVATE_HUAWEI_PUSH"] = activate
		if got := frame(t); got["huawei"] != nil {
			t.Fatalf("Huawei config emitted with activation flag %q: %#v", activate, got["huawei"])
		}
	}
}

func TestFrameRejectsPartialOrMalformedHuaweiOptInWithoutPartialRegistry(t *testing.T) {
	base := map[string]string{
		"GH_TOKEN": "synthetic-token", "ACTIVATE_ANDROID_PUSH": "true", "ACTIVATE_HUAWEI_PUSH": "true",
		"RUSTORE_PUSH_PROJECT_ID": "rustore-project", "RUSTORE_PUSH_SERVICE_TOKEN": "synthetic-rustore-service-token-0123456789",
		"FCM_PROJECT_ID": "fcm-project", "FCM_SERVICE_ACCOUNT_BASE64": "eyJ0eXBlIjoic2VydmljZV9hY2NvdW50In0=",
		"HUAWEI_PROJECT_ID": "11111", "HUAWEI_CLIENT_ID": "22222", "HUAWEI_CLIENT_SECRET": "synthetic_huawei_secret_0123456789",
	}
	for _, tc := range []struct {
		name, key, value string
		android          bool
	}{
		{name: "missing project", key: "HUAWEI_PROJECT_ID", value: ""},
		{name: "missing client", key: "HUAWEI_CLIENT_ID", value: ""},
		{name: "missing secret", key: "HUAWEI_CLIENT_SECRET", value: ""},
		{name: "non numeric project", key: "HUAWEI_PROJECT_ID", value: "project/11111"},
		{name: "non numeric client", key: "HUAWEI_CLIENT_ID", value: "client-22222"},
		{name: "unsafe secret", key: "HUAWEI_CLIENT_SECRET", value: "synthetic_secret-with-space "},
		{name: "activation without Android", android: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := make(map[string]string, len(base))
			for key, value := range base {
				lookup[key] = value
			}
			if tc.key != "" {
				lookup[tc.key] = tc.value
			}
			if tc.android {
				lookup["ACTIVATE_ANDROID_PUSH"] = "false"
			}
			var out bytes.Buffer
			if err := Frame(&out, func(key string) string { return lookup[key] }); err == nil || out.Len() != 0 {
				t.Fatalf("invalid Huawei activation produced frame %q (err=%v)", out.String(), err)
			}
		})
	}
}

func TestJSONCheckRejectsDuplicateOwnerJSONAndBoundsInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner.json")
	if err := os.WriteFile(path, ownerFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"json-check", path}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
	for _, invalid := range [][]byte{[]byte(`{"owner":{"id":1,"id":2}}`), bytes.Repeat([]byte("x"), (1<<20)+1)} {
		if err := os.WriteFile(path, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), []string{"json-check", path}, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("duplicate or oversized JSON accepted")
		}
	}
}
