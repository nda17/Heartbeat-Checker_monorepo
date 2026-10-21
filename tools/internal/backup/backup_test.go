package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/s3store"
)

type testStore struct {
	objects      map[string][]byte
	uploads      []string
	deleted      []string
	corruptGet   bool
	presignCalls []time.Duration
	presignErr   error
}

func newTestStore() *testStore { return &testStore{objects: map[string][]byte{}} }
func (s *testStore) Put(_ context.Context, key string, body io.Reader, _ int64, _ s3store.PutOptions) error {
	b, err := io.ReadAll(body)
	if err == nil {
		s.objects[key] = b
	}
	return err
}
func (s *testStore) Upload(_ context.Context, key, path string, _ s3store.PutOptions) error {
	b, err := os.ReadFile(path)
	if err == nil {
		s.objects[key] = b
		s.uploads = append(s.uploads, key)
	}
	return err
}
func (s *testStore) Get(_ context.Context, key string, target io.Writer) error {
	b, ok := s.objects[key]
	if !ok {
		return s3store.ErrNotFound
	}
	if s.corruptGet && strings.HasSuffix(key, "bundle.tar.age") {
		b = append([]byte(nil), b...)
		b = append(b, '!')
	}
	_, err := target.Write(b)
	return err
}
func (s *testStore) Head(_ context.Context, key string) (s3store.Object, error) {
	b, ok := s.objects[key]
	if !ok {
		return s3store.Object{}, s3store.ErrNotFound
	}
	return s3store.Object{Size: int64(len(b))}, nil
}
func (s *testStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	delete(s.objects, key)
	return nil
}
func (s *testStore) List(_ context.Context, prefix string, _ int32) ([]string, error) {
	var keys []string
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
func (*testStore) PrivateACL(context.Context, string) (bool, error) { return true, nil }
func (*testStore) PublicRead(context.Context, string) error         { return nil }
func (s *testStore) Presign(_ context.Context, _ string, lifetime time.Duration) (string, error) {
	s.presignCalls = append(s.presignCalls, lifetime)
	return "https://example.test/signed", s.presignErr
}

func testKey() string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)) }
func backupEnvironment(withMFA bool) map[string]string {
	values := map[string]string{"DATABASE_URL": "postgresql://heartbeat:synthetic-password@postgres:5432/heartbeat?sslmode=disable", "PATH": "/usr/bin"}
	for _, key := range LegacyRecoveryKeys {
		values[key] = testKey()
	}
	if withMFA {
		values[MFARecoveryKey] = testKey()
	}
	return values
}
func testRunner(t *testing.T, store *testStore, withMFA bool) *Runner {
	t.Helper()
	values := backupEnvironment(withMFA)
	dir := t.TempDir()
	r := New(store)
	r.StateDir = dir
	r.Now = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	r.Getenv = func(key string) string { return values[key] }
	r.Command = func(_ context.Context, args, env []string, out io.Writer) ([]byte, error) {
		if len(args) > 0 && args[0] == "psql" {
			switch {
			case strings.Contains(args[len(args)-1], "to_regclass"):
				return []byte("f|f"), nil
			case strings.Contains(args[len(args)-1], "SELECT EXISTS"):
				return []byte("f"), nil
			}
		}
		if len(args) > 0 && args[0] == "pg_dump" && len(args) > 1 && args[1] == "--version" {
			return []byte("pg_dump (PostgreSQL) 16.1\n"), nil
		}
		if len(args) > 0 && args[0] == "pg_dump" {
			if out == nil {
				return nil, errors.New("pg_dump fixture missing output")
			}
			_, err := io.WriteString(out, "synthetic database dump")
			return nil, err
		}
		return nil, errors.New("unexpected mocked command")
	}
	r.Encrypt = func(_ context.Context, dump, dest string, metadata State, keys map[string]string, identity string) error {
		return os.WriteFile(dest, []byte("age-encrypted synthetic fixture"), 0600)
	}
	r.Restore = func(_ context.Context, ciphertext, identity, scratch string, metadata State, keys map[string]string) ([]any, error) {
		return []any{map[string]any{"version": "20261020120000", "name": "fixture"}}, nil
	}
	return r
}

func TestRecoveryKeysValidatesLegacyAndOptionalNewKey(t *testing.T) {
	values := backupEnvironment(true)
	r := New(newTestStore())
	r.Getenv = func(key string) string { return values[key] }
	keys, names, err := r.RecoveryKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != len(RecoveryKeys) || len(names) != len(RecoveryKeys) {
		t.Fatalf("expected all old and new recovery keys, keys=%v names=%v", keys, names)
	}
	delete(values, MFARecoveryKey)
	r.Getenv = func(key string) string { return values[key] }
	keys, names, err = r.RecoveryKeys()
	if err != nil {
		t.Fatalf("legacy key set should remain readable: %v", err)
	}
	if len(keys) != len(LegacyRecoveryKeys) || len(names) != len(LegacyRecoveryKeys) {
		t.Fatalf("unexpected legacy key set: %v", names)
	}
	values[MFARecoveryKey] = base64.StdEncoding.EncodeToString([]byte("short"))
	if _, _, err = r.RecoveryKeys(); err == nil || err.Error() != "invalid_recovery_key:"+MFARecoveryKey {
		t.Fatalf("invalid new key accepted: %v", err)
	}
}

func TestDatabaseEnvPinsTargetAndReturnsOnlyDatabaseCredentials(t *testing.T) {
	r := New(newTestStore())
	r.Getenv = func(string) string { return "/fixture/bin" }
	env, err := r.DatabaseEnv("postgresql://heartbeat:literal%24secret@postgres:5432/heartbeat?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/fixture/bin", "HOME=/tmp", "LANG=C.UTF-8", "PGHOST=postgres", "PGPORT=5432", "PGUSER=heartbeat", "PGDATABASE=heartbeat", "PGPASSWORD=literal$secret", "PGCONNECT_TIMEOUT=10"}
	if !bytes.Equal([]byte(strings.Join(env, "\n")), []byte(strings.Join(want, "\n"))) {
		t.Fatalf("unexpected database environment: %#v", env)
	}
	for _, raw := range []string{"postgresql://heartbeat:x@remote:5432/heartbeat?sslmode=disable", "postgresql://root:x@postgres:5432/heartbeat?sslmode=disable", "postgresql://heartbeat:x@postgres:5432/heartbeat?sslmode=require"} {
		if _, err := r.DatabaseEnv(raw); err == nil {
			t.Fatalf("unsafe database target accepted: %s", raw)
		}
	}
}

func TestEncryptedMFADataFailsClosedForPartialSchema(t *testing.T) {
	r := New(newTestStore())
	r.Command = func(context.Context, []string, []string, io.Writer) ([]byte, error) { return []byte("t|f"), nil }
	if _, err := r.EncryptedMFAData(context.Background(), nil, nil); err == nil || err.Error() != "incomplete_mfa_schema" {
		t.Fatalf("partial MFA schema did not fail closed: %v", err)
	}
}

func TestRunBackupPublishesManifestOnlyAfterChecksumAndRestoreAndKeepsSecretsPrivate(t *testing.T) {
	store := newTestStore()
	r := testRunner(t, store, true)
	path := filepath.Join(r.StateDir, "2026-10-20.json")
	state := State{"date": "2026-10-20"}
	if err := r.RunBackup(context.Background(), state, path); err != nil {
		t.Fatal(err)
	}
	manifestKey := Prefix + "2026-10-20/manifest.json"
	manifestBytes, ok := store.objects[manifestKey]
	if !ok {
		t.Fatal("restore-verified manifest was not published")
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["ciphertext_sha256"] == "" || manifest["restore_checked_at"] == "" {
		t.Fatalf("manifest lacks restore/checksum evidence: %#v", manifest)
	}
	if manifest["recovery_key_names"] == nil {
		t.Fatalf("manifest lacks recovery-key metadata: %#v", manifest)
	}
	for _, secret := range backupEnvironment(true) {
		if secret == "" || strings.Contains(string(manifestBytes), secret) {
			t.Fatalf("manifest contains configured secret %q", secret)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testKey()) || strings.Contains(string(raw), "synthetic-password") {
		t.Fatal("durable backup state contains credentials")
	}
	var saved State
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["status"] != "success" || saved["phase"] != "complete" {
		t.Fatalf("backup state not complete after verified restore: %#v", saved)
	}
	for _, key := range store.uploads {
		if !strings.HasSuffix(key, "bundle.tar.age") {
			t.Fatalf("unexpected uploaded object: %s", key)
		}
	}
}

func TestRunBackupLegacyRecoveryKeysAreAllowedOnlyWithoutEncryptedMFAData(t *testing.T) {
	store := newTestStore()
	r := testRunner(t, store, false)
	commands := 0
	r.Command = func(_ context.Context, args, env []string, out io.Writer) ([]byte, error) {
		commands++
		if len(args) > 0 && args[0] == "psql" {
			if strings.Contains(args[len(args)-1], "to_regclass") {
				return []byte("t|t"), nil
			}
			return []byte("t"), nil
		}
		return nil, errors.New("unexpected command")
	}
	state := State{"date": "2026-10-20"}
	path := filepath.Join(r.StateDir, "2026-10-20.json")
	err := r.RunBackup(context.Background(), state, path)
	if err == nil || err.Error() != "missing_mfa_recovery_key" {
		t.Fatalf("backup with encrypted MFA rows and legacy keys should fail closed: %v", err)
	}
	if commands != 2 {
		t.Fatalf("MFA schema/data checks did not stop before pg_dump: %d commands", commands)
	}
	if len(store.uploads) != 0 {
		t.Fatalf("unsafe legacy recovery backup uploaded objects: %v", store.uploads)
	}
}

func recoveryTarFixture(t *testing.T, metadata State, keys map[string]string) []byte {
	t.Helper()
	var body bytes.Buffer
	archive := tar.NewWriter(&body)
	dump := []byte("synthetic restored database dump")
	entries := []struct {
		name string
		data []byte
	}{
		{"database.dump", dump},
	}
	keyBytes, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, struct {
		name string
		data []byte
	}{"recovery-keys.json", keyBytes}, struct {
		name string
		data []byte
	}{"recovery.json", metadataBytes})
	for _, entry := range entries {
		if err := archive.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func runRestoreFixture(t *testing.T, includeMFA, encryptedRows bool) error {
	t.Helper()
	keys := map[string]string{}
	names := []string{}
	for _, name := range LegacyRecoveryKeys {
		keys[name] = testKey()
		names = append(names, name)
	}
	if includeMFA {
		keys[MFARecoveryKey] = testKey()
		names = append(names, MFARecoveryKey)
	}
	dump := []byte("synthetic restored database dump")
	metadata := State{"format_version": 1, "database": "heartbeat", "dump_sha256": digestBytes(dump), "recovery_key_names": names, "postgres_version": "16.1"}
	tarBytes := recoveryTarFixture(t, metadata, keys)
	scratch := t.TempDir()
	ciphertext := filepath.Join(scratch, "bundle.tar.age")
	if err := os.WriteFile(ciphertext, []byte("synthetic ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(newTestStore())
	r.Getenv = func(key string) string {
		if key == "PATH" {
			return "/fixture/bin"
		}
		return ""
	}
	var commands [][]string
	r.Command = func(_ context.Context, args, env []string, _ io.Writer) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if args[0] == "age" {
			for i, arg := range args {
				if arg == "-o" {
					if err := os.WriteFile(args[i+1], tarBytes, 0600); err != nil {
						return nil, err
					}
					return nil, nil
				}
			}
			return nil, errors.New("missing age output path")
		}
		if args[0] == "psql" {
			query := args[len(args)-1]
			switch {
			case strings.Contains(query, "schema_migrations"):
				return []byte(`[{"version":"20261020120000","name":"fixture"}]`), nil
			case strings.Contains(query, "to_regclass"):
				return []byte("t|t"), nil
			case strings.Contains(query, "SELECT EXISTS"):
				if encryptedRows {
					return []byte("t"), nil
				}
				return []byte("f"), nil
			}
		}
		return nil, nil
	}
	_, err := r.RestoreDownloaded(context.Background(), ciphertext, filepath.Join(scratch, "identity"), scratch, metadata, keys)
	if err != nil {
		return err
	}
	var actual []string
	for _, command := range commands {
		actual = append(actual, command[0])
	}
	for _, name := range []string{"age", "initdb", "pg_ctl", "createdb", "pg_restore", "psql"} {
		found := false
		for _, got := range actual {
			if got == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("restore skipped isolated %s command: %v", name, actual)
		}
	}
	if actual[0] != "age" || actual[len(actual)-1] != "pg_ctl" {
		t.Fatalf("restore decrypt/stop ordering is wrong: %v", actual)
	}
	return nil
}

func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func TestRestoreChecksEncryptedBundleAndAcceptsLegacyOrNewRecoveryKeys(t *testing.T) {
	if err := runRestoreFixture(t, false, false); err != nil {
		t.Fatalf("legacy recovery keys should restore when MFA rows are absent: %v", err)
	}
	if err := runRestoreFixture(t, true, false); err != nil {
		t.Fatalf("new recovery key set should restore: %v", err)
	}
}

func TestRestoreRejectsLegacyKeysWhenRestoredDatabaseHasEncryptedMFAData(t *testing.T) {
	err := runRestoreFixture(t, false, true)
	if err == nil || err.Error() != "missing_mfa_recovery_key" {
		t.Fatalf("restore accepted encrypted MFA rows without the new key: %v", err)
	}
}

func TestRunBackupChecksumAndRestoreFailuresNeverPublishSuccessManifest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		corrupt    bool
		restoreErr bool
	}{{"checksum", true, false}, {"restore", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore()
			r := testRunner(t, store, true)
			store.corruptGet = tc.corrupt
			if tc.restoreErr {
				r.Restore = func(context.Context, string, string, string, State, map[string]string) ([]any, error) {
					return nil, errors.New("restore fixture failed")
				}
			}
			state := State{"date": "2026-10-20"}
			path := filepath.Join(r.StateDir, "2026-10-20.json")
			if err := r.RunBackup(context.Background(), state, path); err == nil {
				t.Fatal("unsafe backup unexpectedly succeeded")
			}
			if _, ok := store.objects[Prefix+"2026-10-20/manifest.json"]; ok {
				t.Fatal("failed backup published a success manifest")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved State
			if err = json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["status"] != "failed" || saved["report_sent"] != false {
				t.Fatalf("failure state was not retained: %#v", saved)
			}
		})
	}
}

type telegramFixture struct {
	status   int
	body     string
	requests []*http.Request
}

func (f *telegramFixture) Do(request *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, request)
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	body := f.body
	if body == "" {
		body = `{"ok":true,"result":{}}`
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func reportRunner(t *testing.T, store *testStore, telegram HTTPClient) *Runner {
	t.Helper()
	r := New(store)
	r.Now = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	values := map[string]string{"TELEGRAM_INTERNAL_CHAT_ID": "-1004317880847", "TELEGRAM_BACKUPS_TOPIC_ID": "4", "TELEGRAM_BOT_INFORMER_TOKEN": "synthetic-token"}
	r.Getenv = func(key string) string { return values[key] }
	r.Telegram = telegram
	return r
}

func TestTelegramTransportAllowlistPinsOnlyPublicAddressesAndRejectsRedirects(t *testing.T) {
	for _, base := range []string{"https://api.telegram.org", "https://messengers-proxy.heartbeat-checker.ru/telegram-api"} {
		values := map[string]string{"TELEGRAM_API_BASE_URL": base, "TELEGRAM_API_PROXY_IP": "8.8.8.8"}
		got, client, err := TelegramTransport(func(key string) string { return values[key] })
		if err != nil || got != base || client == nil {
			t.Fatalf("allowed Telegram target rejected: %s, %v", base, err)
		}
		if _, ok := client.Transport.(*http.Transport); !ok {
			t.Fatal("Telegram client does not use explicit transport")
		}
		if err := client.CheckRedirect(&http.Request{}, nil); err == nil || err.Error() != "http_redirect_rejected" {
			t.Fatalf("redirect was not rejected: %v", err)
		}
	}
	for _, bad := range []struct{ base, pin string }{{"https://attacker.example", ""}, {"https://api.telegram.org", "127.0.0.1"}, {"https://api.telegram.org", "169.254.1.2"}, {"https://api.telegram.org", "not-an-ip"}} {
		values := map[string]string{"TELEGRAM_API_BASE_URL": bad.base, "TELEGRAM_API_PROXY_IP": bad.pin}
		if _, _, err := TelegramTransport(func(key string) string { return values[key] }); err == nil {
			t.Fatalf("unsafe Telegram target accepted: %+v", bad)
		}
	}
}

func TestReportRetriesWithoutDumpAndDoesNotFollowRedirect(t *testing.T) {
	store := newTestStore()
	telegram := &telegramFixture{status: http.StatusTemporaryRedirect, body: `{"ok":false}`}
	r := reportRunner(t, store, telegram)
	path := filepath.Join(t.TempDir(), "2026-10-20.json")
	state := State{"date": "2026-10-20", "status": "failed", "phase": "download_and_restore", "error": "s3_checksum_mismatch", "report_sent": false}
	sent, err := r.Report(context.Background(), state, path)
	if err != nil {
		t.Fatal(err)
	}
	if sent {
		t.Fatal("redirect response marked report delivered")
	}
	if len(telegram.requests) != 1 {
		t.Fatalf("unexpected Telegram attempts: %d", len(telegram.requests))
	}
	if !strings.Contains(telegram.requests[0].URL.Path, "/botsynthetic-token/sendMessage") {
		t.Fatalf("wrong Telegram recipient path: %s", telegram.requests[0].URL.Path)
	}
	body, err := io.ReadAll(telegram.requests[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "database.dump") || strings.Contains(string(body), "synthetic-password") {
		t.Fatalf("failure report contains backup content: %s", body)
	}
	var saved State
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["report_error"] != "telegram_delivery_failed" || saved["report_sent"] != false {
		t.Fatalf("failed report attempt was not retained: %#v", saved)
	}
}

func TestSuccessReportUsesReadableSizeAndSevenDayLinkOnce(t *testing.T) {
	store := newTestStore()
	telegram := &telegramFixture{}
	r := reportRunner(t, store, telegram)
	path := filepath.Join(t.TempDir(), "2026-10-20.json")
	state := State{"date": "2026-10-20", "status": "success", "bytes": int64(1536), "object_key": Prefix + "2026-10-20/bundle.tar.age", "ciphertext_sha256": "fixture-hash", "report_sent": false}
	sent, err := r.Report(context.Background(), state, path)
	if err != nil || !sent {
		t.Fatalf("success report failed: %v", err)
	}
	if len(store.presignCalls) != 1 || store.presignCalls[0] != 7*24*time.Hour {
		t.Fatalf("signed-link lifetime = %v", store.presignCalls)
	}
	if len(telegram.requests) != 1 {
		t.Fatalf("expected one Telegram delivery, got %d", len(telegram.requests))
	}
	var payload map[string]any
	if err = json.NewDecoder(telegram.requests[0].Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	message := payload["text"].(string)
	if !strings.Contains(message, "1,5 КБ") || !strings.Contains(message, "7 дней") || !strings.Contains(message, "https://example.test/signed") {
		t.Fatalf("success report text is incomplete: %s", message)
	}
	if again, err := r.Report(context.Background(), state, path); err != nil || !again {
		t.Fatalf("sent report was not idempotent: %v", err)
	}
	if len(telegram.requests) != 1 {
		t.Fatalf("report sent twice: %d", len(telegram.requests))
	}
}

func TestReportFormatsSizeAndCapsLinkAtRetentionBoundary(t *testing.T) {
	if FormatSize(1536) != "1,5 КБ" || FormatSize(1536*1024) != "1,5 МБ" {
		t.Fatalf("unexpected readable sizes: %s, %s", FormatSize(1536), FormatSize(1536*1024))
	}
	store := newTestStore()
	telegram := &telegramFixture{}
	r := reportRunner(t, store, telegram)
	r.Now = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	path := filepath.Join(t.TempDir(), "2026-10-10.json")
	state := State{"date": "2026-10-10", "status": "success", "bytes": int64(1), "object_key": "old-object", "ciphertext_sha256": "fixture-hash"}
	if sent, err := r.Report(context.Background(), state, path); err != nil || !sent {
		t.Fatalf("retention-limited report failed: %v", err)
	}
	if len(store.presignCalls) != 1 || store.presignCalls[0] <= 0 || store.presignCalls[0] >= 7*24*time.Hour {
		t.Fatalf("link was not capped to remaining retention: %v", store.presignCalls)
	}
}

func TestExpiredBackupReportDoesNotPresignOrClaimAvailability(t *testing.T) {
	store := newTestStore()
	telegram := &telegramFixture{}
	r := reportRunner(t, store, telegram)
	r.Now = func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }
	path := filepath.Join(t.TempDir(), "2026-10-01.json")
	state := State{"date": "2026-10-01", "status": "success", "bytes": int64(1), "object_key": "old-object", "ciphertext_sha256": "fixture-hash"}
	if sent, err := r.Report(context.Background(), state, path); err != nil || !sent {
		t.Fatalf("expired report failed: %v", err)
	}
	if len(store.presignCalls) != 0 {
		t.Fatalf("expired backup was signed: %v", store.presignCalls)
	}
	if len(telegram.requests) != 1 {
		t.Fatalf("report not sent: %d", len(telegram.requests))
	}
	var payload map[string]any
	if err := json.NewDecoder(telegram.requests[0].Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	message := payload["text"].(string)
	if !strings.Contains(message, "не формируется") || !strings.Contains(message, "доступность не подтверждена") {
		t.Fatalf("report claims expired archive availability: %s", message)
	}
}

func TestFormatSizeUsesKilobytesBelowOneMegabyte(t *testing.T) {
	if got := FormatSize(512); got != "0,5 КБ" {
		t.Fatalf("size below one megabyte formatted as %q", got)
	}
}

func TestPruneDeletesOnlyOldExactObjectsUnderOwnedPrefix(t *testing.T) {
	store := newTestStore()
	for _, key := range []string{
		Prefix + "2026-10-05/bundle.tar.age", Prefix + "2026-10-05/manifest.json", Prefix + "2026-10-05/extra.tar.age",
		Prefix + "2026-10-08/bundle.tar.age", Prefix + "2026-10-08/manifest.json", Prefix + "2026-13-40/bundle.tar.age",
		"other/2026-10-01/bundle.tar.age",
	} {
		store.objects[key] = []byte("fixture")
	}
	r := reportRunner(t, store, &telegramFixture{})
	if err := r.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	deleted := map[string]bool{}
	for _, key := range store.deleted {
		deleted[key] = true
	}
	for _, key := range []string{Prefix + "2026-10-05/bundle.tar.age", Prefix + "2026-10-05/manifest.json"} {
		if !deleted[key] {
			t.Errorf("old owned object not pruned: %s", key)
		}
	}
	for _, key := range []string{Prefix + "2026-10-05/extra.tar.age", Prefix + "2026-10-08/bundle.tar.age", Prefix + "2026-10-08/manifest.json", Prefix + "2026-13-40/bundle.tar.age", "other/2026-10-01/bundle.tar.age"} {
		if deleted[key] {
			t.Errorf("non-owned/non-exact/recent object pruned: %s", key)
		}
	}
}
