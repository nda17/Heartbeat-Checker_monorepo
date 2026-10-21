package deployguard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var s3EnvironmentKeys = []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_REGION", "BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY"}

func composeS3Config(environment any) []byte {
	b, _ := json.Marshal(map[string]any{"services": map[string]any{"backup": map[string]any{"environment": environment}, "api": map[string]any{"environment": map[string]any{"JWT_SECRET": "never-export"}}}})
	return b
}

func TestWriteS3EnvPreservesResolvedValuesAndExportsOnlySortedS3Keys(t *testing.T) {
	values := map[string]string{
		"BACKUP_S3_ENDPOINT":          `https://example.test/path?$$mode=a#fragment=1 "quoted"`,
		"BACKUP_S3_BUCKET":            "bucket=with=equals # comment-like text with $$, $$$$, and $${NAME}",
		"BACKUP_S3_REGION":            "'region-starts-with-a-quote",
		"BACKUP_S3_ACCESS_KEY_ID":     `access$$HOME'and"both"`,
		"BACKUP_S3_SECRET_ACCESS_KEY": `secret="double" and 'single' # $$VALUE=a; $$$$; $$$$$$$; $${NAME}`,
		"DATABASE_URL":                "must-not-be-exported", "JWT_SECRET": "must-not-be-exported-either",
	}
	env := map[string]string{}
	for k, v := range values {
		if k != "DATABASE_URL" && k != "JWT_SECRET" {
			env[k] = v
		}
	}
	// Preserve Compose's resolved representation: each literal dollar is doubled once.
	resolved := map[string]string{}
	for k, v := range env {
		resolved[k] = strings.ReplaceAll(v, "$", "$$")
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "resolved.env")
	if err := WriteS3Env(bytes.NewReader(composeS3Config(resolved)), output); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for key := range resolved {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var want strings.Builder
	for _, key := range keys {
		want.WriteString(key + "=" + env[key] + "\n")
	}
	if string(got) != want.String() {
		t.Fatalf("resolved values changed\nwant: %q\ngot:  %q", want.String(), got)
	}
	if bytes.Contains(got, []byte("JWT_SECRET")) || bytes.Contains(got, []byte("DATABASE_URL")) {
		t.Fatalf("non-S3 secrets were exported: %s", got)
	}
	if mode := mustFileMode(t, output); mode != 0600 {
		t.Fatalf("temporary env mode = %o, want 600", mode)
	}
}

func TestWriteS3EnvFailsClosedForMissingEmptyTypedAndMultilineValues(t *testing.T) {
	valid := map[string]string{}
	for _, key := range s3EnvironmentKeys {
		valid[key] = "synthetic-value"
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing", func(m map[string]any) { delete(m, s3EnvironmentKeys[1]) }},
		{"empty", func(m map[string]any) { m[s3EnvironmentKeys[1]] = "" }},
		{"nonstring", func(m map[string]any) { m[s3EnvironmentKeys[1]] = 42 }},
		{"carriage return", func(m map[string]any) { m[s3EnvironmentKeys[1]] = "value\rhidden" }},
		{"line feed", func(m map[string]any) { m[s3EnvironmentKeys[1]] = "value\nhidden" }},
		{"nul", func(m map[string]any) { m[s3EnvironmentKeys[1]] = "value\x00hidden" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{}
			for k, v := range valid {
				input[k] = v
			}
			tc.mutate(input)
			output := filepath.Join(t.TempDir(), "resolved.env")
			if err := WriteS3Env(bytes.NewReader(composeS3Config(input)), output); err == nil {
				t.Fatal("invalid Compose env accepted")
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatalf("invalid input left env file: %v", err)
			}
		})
	}
}

func TestWriteS3EnvRejectsUnpairedDollarButCollapsesEscapedDollarOnce(t *testing.T) {
	valid := map[string]string{}
	for _, key := range s3EnvironmentKeys {
		valid[key] = "synthetic-value"
	}
	valid[s3EnvironmentKeys[1]] = "literal $HOME"
	output := filepath.Join(t.TempDir(), "resolved.env")
	if err := WriteS3Env(bytes.NewReader(composeS3Config(valid)), output); err == nil {
		t.Fatal("unpaired Compose dollar accepted")
	}
	valid[s3EnvironmentKeys[1]] = "literal $$HOME"
	output = filepath.Join(t.TempDir(), "escaped.env")
	if err := WriteS3Env(bytes.NewReader(composeS3Config(valid)), output); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("BACKUP_S3_BUCKET=literal $HOME\n")) {
		t.Fatalf("escaped literal dollar not preserved: %s", got)
	}
}

func TestWriteS3EnvRejectsSymlinkDestination(t *testing.T) {
	values := map[string]string{}
	for _, key := range s3EnvironmentKeys {
		values[key] = "synthetic-value"
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("do not follow\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteS3Env(bytes.NewReader(composeS3Config(values)), link); err == nil {
		t.Fatal("symlink output was accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || !reflect.DeepEqual(got, []byte("do not follow\n")) {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
}

func mustFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func validComposeFixture() map[string]any {
	api := map[string]string{"DATABASE_URL": "postgresql://heartbeat:testpass@postgres:5432/heartbeat?sslmode=disable", "EMAIL_NOTIFICATIONS_ENABLED": "false", "ANDROID_PUSH_ENABLED": "false", "RUSTORE_PUSH_PROJECT_ID": "", "AUTH_MFA_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32))}
	worker := map[string]string{"EMAIL_NOTIFICATIONS_ENABLED": "false", "ANDROID_PUSH_ENABLED": "false", "RUSTORE_PUSH_PROJECT_ID": "", "SMTP_TLS_MODE": "implicit_tls"}
	return map[string]any{"services": map[string]any{"api": map[string]any{"environment": api}, "worker": map[string]any{"environment": worker}, "postgres": map[string]any{"environment": map[string]string{"POSTGRES_PASSWORD": "testpass"}}, "probe": map[string]any{"environment": map[string]string{"PROBE_SHARED_SECRET": "synthetic-probe-secret"}}, "caddy": map[string]any{"environment": map[string]string{}}, "migrate": map[string]any{"environment": map[string]string{}}, "backup": map[string]any{"environment": map[string]string{}}}}
}

func composeBytes(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckComposeRequiresMatchedScopedDatabaseAndNotificationConfiguration(t *testing.T) {
	valid := validComposeFixture()
	if err := CheckCompose(bytes.NewReader(composeBytes(t, valid))); err != nil {
		t.Fatalf("valid scoped Compose rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"database host", func(m map[string]any) {
			m["services"].(map[string]any)["api"].(map[string]any)["environment"].(map[string]string)["DATABASE_URL"] = "postgresql://heartbeat:testpass@attacker:5432/heartbeat?sslmode=disable"
		}},
		{"database password", func(m map[string]any) {
			m["services"].(map[string]any)["postgres"].(map[string]any)["environment"].(map[string]string)["POSTGRES_PASSWORD"] = "changed"
		}},
		{"email flag mismatch", func(m map[string]any) {
			m["services"].(map[string]any)["worker"].(map[string]any)["environment"].(map[string]string)["EMAIL_NOTIFICATIONS_ENABLED"] = "true"
		}},
		{"probe scoped secret", func(m map[string]any) {
			m["services"].(map[string]any)["probe"].(map[string]any)["environment"].(map[string]string)["EMAIL_NOTIFICATIONS_ENABLED"] = "false"
		}},
		{"unverified SMTP", func(m map[string]any) {
			m["services"].(map[string]any)["worker"].(map[string]any)["environment"].(map[string]string)["SMTP_TLS_MODE"] = "none"
		}},
		{"MFA key missing", func(m map[string]any) {
			delete(m["services"].(map[string]any)["api"].(map[string]any)["environment"].(map[string]string), "AUTH_MFA_ENCRYPTION_KEY")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copy := validComposeFixture()
			tc.mutate(copy)
			if err := CheckCompose(bytes.NewReader(composeBytes(t, copy))); err == nil {
				t.Fatal("invalid Compose configuration accepted")
			}
		})
	}
}

type probeRoundTripper struct {
	requests     []*http.Request
	targets      []string
	failMetadata bool
}

func (f *probeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, request.Clone(request.Context()))
	body, _ := io.ReadAll(request.Body)
	_ = request.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	target, _ := payload["url"].(string)
	f.targets = append(f.targets, target)
	status := http.StatusOK
	response := `{"outcome":"UP","httpStatus":200}`
	if strings.Contains(target, "169.254") {
		status = http.StatusBadRequest
		response = `{"error":"unsupported_target"}`
	}
	if f.failMetadata && strings.Contains(target, "169.254") {
		status = http.StatusOK
		response = `{"outcome":"UP","httpStatus":200}`
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
}

func TestProbeSmokeChecksOwnSiteAndRejectsMetadataAddressWithoutRedirects(t *testing.T) {
	raw := composeBytes(t, validComposeFixture())
	transport := &probeRoundTripper{}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	if err := ProbeSmoke(context.Background(), bytes.NewReader(raw), client); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 2 || transport.requests[0].Header.Get("Authorization") != "Bearer synthetic-probe-secret" {
		t.Fatalf("probe smoke did not use the service credential and both targets: %#v", transport.requests)
	}
	if transport.targets[0] != "https://heartbeat-checker.ru/" || transport.targets[1] != "http://169.254.169.254/" {
		t.Fatalf("probe did not exercise public and metadata targets: %v", transport.targets)
	}
	transport = &probeRoundTripper{failMetadata: true}
	client = &http.Client{Transport: transport}
	if err := ProbeSmoke(context.Background(), bytes.NewReader(raw), client); err == nil {
		t.Fatal("probe metadata-address acceptance was not rejected")
	}
}
