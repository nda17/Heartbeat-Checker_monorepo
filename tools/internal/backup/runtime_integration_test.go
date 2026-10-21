package backup

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const backupInnerEnv = "HC_BACKUP_INNER_RUNTIME_TEST"

// TestRealAgePostgresBackupRestoreIntegration is opt-in because it builds and
// executes the package test binary inside the pinned backup runtime image. The
// runtime container has no network and gets only a bounded temporary filesystem.
func TestRealAgePostgresBackupRestoreIntegration(t *testing.T) {
	if os.Getenv(backupInnerEnv) == "1" {
		runRealAgePostgresRoundTrip(t)
		return
	}
	if os.Getenv("HEARTBEAT_BACKUP_INTEGRATION") != "1" {
		t.Skip("set HEARTBEAT_BACKUP_INTEGRATION=1 to run the isolated age/PostgreSQL runtime proof")
	}
	image := os.Getenv("HEARTBEAT_BACKUP_TEST_IMAGE")
	if image == "" {
		t.Fatal("HEARTBEAT_BACKUP_TEST_IMAGE must name the newly built backup runtime image")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("docker CLI required for runtime integration: ", err)
	}
	arch, err := dockerOutput(t, "image", "inspect", "--format", "{{.Architecture}}", image)
	if err != nil {
		t.Fatal(err)
	}
	goarch := strings.TrimSpace(arch)
	if goarch == "aarch64" {
		goarch = "arm64"
	}
	if goarch != "amd64" && goarch != "arm64" {
		t.Fatalf("unsupported backup image architecture %q", goarch)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "backup-runtime.test")
	cmd := exec.Command("go", "test", "-c", "-o", binary, ".")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile backup runtime test binary: %v\n%s", err, output)
	}
	container := fmt.Sprintf("hc-backup-runtime-test-%d", os.Getpid())
	if _, err = dockerOutput(t, "create", "--network", "none", "--tmpfs", "/tmp:rw,nosuid,size=512m", "--name", container, "--entrypoint", "/bin/sleep", image, "300"); err != nil {
		t.Fatal(err)
	}
	// The image's default user is postgres; create's lack of --user preserves it.
	defer func() { _ = exec.Command("docker", "rm", "-f", container).Run() }()
	if _, err = dockerOutput(t, "start", container); err != nil {
		t.Fatal(err)
	}
	if _, err = dockerOutput(t, "cp", binary, container+":/backup-runtime.test"); err != nil {
		t.Fatal(err)
	}
	args := []string{"exec", "-e", backupInnerEnv + "=1", container, "/backup-runtime.test", "-test.run=^TestRealAgePostgresBackupRestoreIntegration$"}
	if output, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("real age/PostgreSQL roundtrip failed: %v\n%s", err, output)
	}
}

func dockerOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %v: %w: %s", args, err, out)
	}
	return string(out), nil
}

func runRealAgePostgresRoundTrip(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"age", "age-keygen", "initdb", "pg_ctl", "createdb", "pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("backup image is missing required runtime tool %s: %v", tool, err)
		}
	}
	ctx := context.Background()
	scratch, err := os.MkdirTemp("/tmp", "hc-backup-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(scratch)
	if err = os.Chmod(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	path := func(name string) string { return filepath.Join(scratch, name) }
	command := func(args []string, env []string, output *os.File) []byte {
		t.Helper()
		out, e := Command(ctx, args, env, output)
		if e != nil {
			t.Fatalf("%v: %v", args, e)
		}
		return out
	}
	identity := path("recipient.agekey")
	command([]string{"age-keygen", "-o", identity}, nil, nil)
	identityBytes, err := os.ReadFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	recipient := ""
	for _, line := range strings.Split(string(identityBytes), "\n") {
		if strings.HasPrefix(line, "# public key: ") {
			recipient = strings.TrimSpace(strings.TrimPrefix(line, "# public key: "))
		}
	}
	if !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("invalid generated age recipient %q", recipient)
	}
	getenv := func(key string) string {
		if key == "BACKUP_AGE_RECIPIENT" {
			return recipient
		}
		if key == "PATH" {
			return os.Getenv("PATH")
		}
		return ""
	}
	runner := New(nil)
	runner.Getenv = getenv
	runner.Command = Command
	pgEnv := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + scratch, "LANG=C.UTF-8"}
	createSource := func(name string, withMFA bool) string {
		t.Helper()
		cluster, socket := path(name+"-cluster"), path(name+"-socket")
		if err := os.Mkdir(socket, 0700); err != nil {
			t.Fatal(err)
		}
		command([]string{"initdb", "-D", cluster, "--auth-local=trust", "--auth-host=reject", "--no-locale", "-U", "heartbeat"}, pgEnv, nil)
		command([]string{"pg_ctl", "-D", cluster, "-l", path(name + ".log"), "-o", "-c listen_addresses='' -c unix_socket_directories=" + socket, "-w", "start"}, pgEnv, nil)
		t.Cleanup(func() {
			_, _ = Command(context.Background(), []string{"pg_ctl", "-D", cluster, "-m", "immediate", "-w", "stop"}, pgEnv, nil)
		})
		conn := []string{"-h", socket, "-U", "heartbeat"}
		command(append([]string{"createdb"}, append(conn, name)...), pgEnv, nil)
		sql := "CREATE TABLE public.schema_migrations(version text PRIMARY KEY); INSERT INTO public.schema_migrations VALUES ('2026102101');"
		if withMFA {
			sql += " CREATE SCHEMA auth; CREATE TABLE auth.mfa_factors(secret bytea); CREATE TABLE auth.mfa_enrollments(secret bytea); INSERT INTO auth.mfa_factors VALUES (decode('0102','hex'));"
		}
		command(append([]string{"psql"}, append(append(conn, "-d", name, "-v", "ON_ERROR_STOP=1", "-c"), sql)...), pgEnv, nil)
		dump := path(name + ".dump")
		f, err := os.OpenFile(dump, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		dumpArgs := append([]string{"pg_dump"}, conn...)
		dumpArgs = append(dumpArgs, "-d", name, "--format=custom", "--no-owner", "--no-privileges")
		command(dumpArgs, pgEnv, f)
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		return dump
	}
	modernKeys := map[string]string{}
	names := append([]string(nil), RecoveryKeys...)
	for _, name := range names {
		modernKeys[name] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	legacyKeys := map[string]string{}
	for _, name := range LegacyRecoveryKeys {
		legacyKeys[name] = modernKeys[name]
	}
	modernDump := createSource("heartbeat", true)
	roundTrip := func(dump string, keys map[string]string, names []string, expectErr string) {
		t.Helper()
		hash, e := Digest(dump)
		if e != nil {
			t.Fatal(e)
		}
		metadata := State{"format_version": 1, "database": "heartbeat", "created_at": "2026-10-21T00:00:00Z", "source_sha": strings.Repeat("a", 40), "dump_sha256": hash, "recovery_key_names": names, "postgres_version": "PostgreSQL 18"}
		restoreScratch, err := os.MkdirTemp(scratch, "restore-")
		if err != nil {
			t.Fatal(err)
		}
		ephemeralIdentity := filepath.Join(restoreScratch, "ephemeral.agekey")
		cipher := filepath.Join(scratch, fmt.Sprintf("roundtrip-%d-%s.tar.age", len(names), filepath.Base(dump)))
		if e = runner.EncryptBundle(ctx, dump, cipher, metadata, keys, ephemeralIdentity); e != nil {
			t.Fatal("EncryptBundle:", e)
		}
		migrations, e := runner.RestoreDownloaded(ctx, cipher, ephemeralIdentity, restoreScratch, metadata, keys)
		if expectErr != "" {
			if e == nil || !strings.Contains(e.Error(), expectErr) {
				t.Fatalf("expected restore error %q, got migrations=%v err=%v", expectErr, migrations, e)
			}
			return
		}
		if e != nil {
			t.Fatal("RestoreDownloaded:", e)
		}
		if len(migrations) != 1 {
			t.Fatalf("restored migration ledger=%#v", migrations)
		}
	}
	roundTrip(modernDump, modernKeys, names, "")
	legacyDump := createSource("legacydb", false)
	roundTrip(legacyDump, legacyKeys, LegacyRecoveryKeys, "")
	roundTrip(modernDump, legacyKeys, LegacyRecoveryKeys, "missing_mfa_recovery_key")
	corrupt := path("corrupt.tar.age")
	if err = os.WriteFile(corrupt, []byte("not an age archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RestoreDownloaded(ctx, corrupt, identity, path("corrupt-restore"), State{"dump_sha256": "x", "recovery_key_names": names}, modernKeys); err == nil {
		t.Fatal("corrupt encrypted archive restored")
	}
}
