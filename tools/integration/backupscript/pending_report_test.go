package backupscript

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	nextSource = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oldSource  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	backupSHA  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	backupImg  = "ghcr.io/nda17/heartbeat-checker_monorepo/backup@sha256:" + backupSHA
)

func TestPendingReportRequiresTheValidatedNextHelperAndRunStaysBlocked(t *testing.T) {
	t.Run("report selects retained next helper", func(t *testing.T) {
		f := newHarness(t)
		result := f.run("report")
		if result.err != nil {
			helper, _ := os.ReadFile(f.helperLog)
			docker, _ := os.ReadFile(f.dockerLog)
			t.Fatalf("report failed (%v): %s; helper=%q docker=%q", result.err, result.output, helper, docker)
		}
		log, _ := os.ReadFile(f.helperLog)
		if strings.Contains(string(log), "hc-ops ") || !strings.Contains(string(log), "hc-ops.next version") || !strings.Contains(string(log), "hc-ops.next deploy-json backup-env-check") {
			t.Fatalf("pending report did not select the next helper: %q", log)
		}
		docker, _ := os.ReadFile(f.dockerLog)
		if !strings.Contains(string(docker), "backup report") {
			t.Fatalf("report mode did not reach the immutable backup runtime: %q", docker)
		}
	})

	for _, tc := range []struct {
		name           string
		mutate         func(*harness)
		wantDiagnostic string
	}{
		{name: "checksum mismatch", mutate: func(f *harness) {
			f.write("recovery/"+f.recoveryName+"/hc-ops.next.sha256", strings.Repeat("d", 64)+"\n", 0o600)
		}, wantDiagnostic: "Backup helper checksum/source mismatch"},
		{name: "wrong helper source", mutate: func(f *harness) {
			f.write("recovery/"+f.recoveryName+"/hc-ops.next", helperScript(oldSource, "hc-ops.next"), 0o700)
			f.refreshNextChecksum()
		}, wantDiagnostic: "Backup helper checksum/source mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHarness(t)
			tc.mutate(f)
			result := f.run("report")
			if result.err == nil || !strings.Contains(result.output, tc.wantDiagnostic) {
				t.Fatalf("invalid pending helper was not rejected: %+v", result)
			}
			docker, _ := os.ReadFile(f.dockerLog)
			if len(docker) != 0 {
				t.Fatalf("Docker ran before helper validation: %q", docker)
			}
		})
	}

	t.Run("run remains blocked while deployment is pending", func(t *testing.T) {
		f := newHarness(t)
		result := f.run("run")
		if result.err == nil || !strings.Contains(result.output, "Resolve pending backend deployment before backup") {
			t.Fatalf("run did not fail closed on pending deployment: %+v", result)
		}
		helper, _ := os.ReadFile(f.helperLog)
		docker, _ := os.ReadFile(f.dockerLog)
		if len(helper) != 0 || len(docker) != 0 {
			t.Fatalf("blocked run invoked helper or Docker: helper=%q docker=%q", helper, docker)
		}
	})
}

type harness struct {
	t            *testing.T
	root         string
	bin          string
	recoveryName string
	helperLog    string
	dockerLog    string
	script       string
}

type runResult struct {
	output string
	err    error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	f := &harness{t: t, root: root, bin: filepath.Join(root, "bin"), recoveryName: nextSource + "-20261021T120000Z", helperLog: filepath.Join(root, "helper.log"), dockerLog: filepath.Join(root, "docker.log")}
	if err := os.MkdirAll(f.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate integration test source")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	script, err := os.ReadFile(filepath.Join(repo, "deploy/backend/backup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const fixedCWD = "cd /opt/heartbeat-checker/backend"
	if strings.Count(string(script), fixedCWD) != 1 {
		t.Fatal("backup script working directory anchor changed")
	}
	f.script = filepath.Join(root, "backup.sh")
	patched := strings.Replace(string(script), fixedCWD, `cd "$BACKUP_DIR"`, 1)
	if err := os.WriteFile(f.script, []byte(patched), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{".env", ".backup.env", ".release.env", "compose.backup.yaml"} {
		f.write(name, "fixture\n", 0o600)
	}
	f.write(".release.env", "SOURCE_SHA="+nextSource+"\n", 0o600)
	if err := os.MkdirAll(filepath.Join(root, "recovery", f.recoveryName), 0o700); err != nil {
		t.Fatal(err)
	}
	f.write("pending", "source_sha="+nextSource+"\nphase=migration_started\nrecovery=recovery/"+f.recoveryName+"\n", 0o600)
	f.write("hc-ops", helperScript(oldSource, "hc-ops"), 0o700)
	f.refreshInstalledChecksum()
	f.write("recovery/"+f.recoveryName+"/hc-ops.next", helperScript(nextSource, "hc-ops.next"), 0o700)
	f.refreshNextChecksum()

	f.writeCommand("flock", "#!/bin/sh\nexit 0\n")
	f.writeCommand("stat", `#!/bin/sh
if [ "$1" = "-c" ] && [ "$2" = "%a" ]; then
  case "$(uname -s)" in Darwin) mode=$(/usr/bin/stat -f '%Lp' "$3") ;; *) mode=$(/usr/bin/stat -c '%a' "$3") ;; esac || exit 1
  printf '%s\n' "$mode"
elif [ "$1" = "-c" ] && [ "$2" = "%u" ]; then
  case "$(uname -s)" in Darwin) uid=$(/usr/bin/stat -f '%u' "$3") ;; *) uid=$(/usr/bin/stat -c '%u' "$3") ;; esac || exit 1
  printf '%s\n' "$uid"
else exit 1; fi
`)
	f.writeCommand("sha256sum", `#!/bin/sh
for path do :; done
case "$(uname -s)" in Darwin) /usr/bin/shasum -a 256 "$path" ;; *) /usr/bin/sha256sum "$path" ;; esac
`)
	f.writeCommand("docker", `#!/bin/sh
printf '%s\n' "$*" >> "$DOCKER_LOG"
if [ "$1" = "image" ] && [ "$2" = "inspect" ]; then printf '%s\n' "$HC_TEST_SOURCE"; exit 0; fi
if [ "$1" = "compose" ]; then
  action=; previous=
  for arg do
    case "$arg" in
      config) action=config ;;
      -q) action=quiet ;;
      json) [ "$previous" = "--format" ] && action=json ;;
      run) action=run ;;
    esac
    previous=$arg
  done
  case "$action" in
    json) printf '{"services":{"backup":{"image":"%s","environment":{"BACKUP_SOURCE_SHA":"%s"}}}}\n' "$HC_TEST_BACKUP_IMAGE" "$HC_TEST_SOURCE"; exit 0 ;;
    quiet) exit 0 ;;
    run) exit 0 ;;
    *) exit 4 ;;
  esac
fi
exit 5
`)
	return f
}

func (f *harness) write(name, contents string, mode os.FileMode) {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *harness) writeCommand(name, contents string) {
	f.t.Helper()
	path := filepath.Join(f.bin, name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		f.t.Fatal(err)
	}
}

func (f *harness) refreshInstalledChecksum() {
	f.refreshChecksum("hc-ops", "hc-ops.sha256")
}

func (f *harness) refreshNextChecksum() {
	f.refreshChecksum("recovery/"+f.recoveryName+"/hc-ops.next", "recovery/"+f.recoveryName+"/hc-ops.next.sha256")
}

func (f *harness) refreshChecksum(binary, checksum string) {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, binary))
	if err != nil {
		f.t.Fatal(err)
	}
	digest := sha256.Sum256(b)
	f.write(checksum, hex.EncodeToString(digest[:])+"\n", 0o600)
}

func (f *harness) run(mode string) runResult {
	f.t.Helper()
	cmd := exec.Command("/bin/bash", f.script, mode)
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(), "PATH="+f.bin+":"+os.Getenv("PATH"), "BACKUP_DIR="+f.root, "DOCKER_LOG="+f.dockerLog, "HELPER_LOG="+f.helperLog, "HC_TEST_SOURCE="+nextSource, "HC_TEST_BACKUP_IMAGE="+backupImg)
	out, err := cmd.CombinedOutput()
	return runResult{output: string(out), err: err}
}

func helperScript(source, name string) string {
	return fmt.Sprintf(`#!/bin/sh
printf '%%s %%s\n' '%s' "$*" >> "$HELPER_LOG"
case "$1" in
  version) printf '%%s\n' '%s' ;;
  deploy-json) [ "$2" = backup-env-check ] ;;
  compose-field)
    case "$2" in
      backup.image) printf '%%s\n' "$HC_TEST_BACKUP_IMAGE" ;;
      backup.source) printf '%%s\n' "$HC_TEST_SOURCE" ;;
      *) exit 2 ;;
    esac ;;
  *) exit 2 ;;
esac
`, name, source)
}
