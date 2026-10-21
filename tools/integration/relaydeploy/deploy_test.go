package relaydeploy

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

const relaySHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const relayInner = "HC_RELAY_INNER_SCENARIO"

func TestRelayInnerScenario(t *testing.T) {
	scenario := os.Getenv(relayInner)
	if scenario == "" {
		return
	}
	runRelayScenario(t, scenario)
}

func TestRelayDeployRollbackCleanupAndSuccessInIsolatedDocker(t *testing.T) {
	if os.Getenv("HEARTBEAT_RELAY_INTEGRATION") != "1" {
		t.Skip("set HEARTBEAT_RELAY_INTEGRATION=1 to run isolated relay deployment scenarios")
	}
	image := os.Getenv("HEARTBEAT_RELAY_TEST_IMAGE")
	if image == "" {
		image = "heartbeat-checker/backup:local"
	}
	arch, err := runOutput("docker", "image", "inspect", "--format", "{{.Architecture}}", image)
	if err != nil {
		t.Fatal(err)
	}
	goarch := strings.TrimSpace(arch)
	if goarch == "aarch64" {
		goarch = "arm64"
	}
	if goarch != "arm64" && goarch != "amd64" {
		t.Fatalf("unsupported relay test image architecture %q", goarch)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "relay.test")
	cmd := exec.Command("go", "test", "-c", "-o", binary, ".")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile relay test binary: %v\n%s", err, out)
	}
	helper := filepath.Join(root, "hc-ops")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-X main.sourceSHA="+relaySHA, "-o", helper, "../../cmd/hc-ops")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pinned helper: %v\n%s", err, out)
	}
	for _, scenario := range []string{"certonly", "postflight", "tlspermanent", "delete", "late", "success"} {
		if selected := os.Getenv("HEARTBEAT_RELAY_SCENARIO"); selected != "" && selected != scenario {
			continue
		}
		t.Run(scenario, func(t *testing.T) {
			container := fmt.Sprintf("hc-relay-test-%d-%s", os.Getpid(), scenario)
			defer func() { _ = exec.Command("docker", "rm", "-f", container).Run() }()
			if _, err := runOutput("docker", "create", "--network", "none", "--user", "0", "--name", container, "--entrypoint", "/bin/sleep", image, "120"); err != nil {
				t.Fatal(err)
			}
			if _, err := runOutput("docker", "start", container); err != nil {
				t.Fatal(err)
			}
			repo := repositoryRoot(t)
			script := filepath.Join(repo, "deploy/relay/deploy.sh")
			for _, pair := range [][2]string{{script, container + ":/tmp/deploy-source.sh"}, {binary, container + ":/tmp/relay.test"}, {helper, container + ":/tmp/hc-ops"}} {
				if _, err := runOutput("docker", "cp", pair[0], pair[1]); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"exec", "-e", relayInner + "=" + scenario, "-e", "HC_RELAY_HELPER=/tmp/hc-ops", container, "/tmp/relay.test", "-test.run=^TestRelayInnerScenario$"}
			if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
				t.Fatalf("relay %s scenario failed: %v\n%s", scenario, err, out)
			}
		})
	}
}

func runRelayScenario(t *testing.T, scenario string) {
	t.Helper()
	const deploy = "/opt/heartbeat-checker/relay"
	fakeBin := "/tmp/relay-test-bin"
	fixture := "/tmp/relay-test-fixture"
	for _, p := range []string{deploy, fakeBin, fixture} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	aero := map[string][]byte{"/etc/nginx/sites-available/telegram.aerocrm.space": []byte("fixture aeroCRM vhost\n"), "/etc/nginx/snippets/aerocrm-telegram-webhooks.conf": []byte("fixture aeroCRM snippet\n"), "/etc/nginx/.aerocrm-telegram-business-relay.json": []byte("{\"fixture\":true}\n")}
	for path, b := range aero {
		writeFixture(t, path, b)
	}
	mustMkdir(t, "/etc/nginx/sites-enabled")
	if err := os.Symlink("/etc/nginx/sites-available/telegram.aerocrm.space", "/etc/nginx/sites-enabled/zz-telegram.aerocrm.space"); err != nil {
		t.Fatal(err)
	}
	old := map[string][]byte{"/etc/nginx/sites-available/tg.winwidget.ru": []byte("old WinWidget vhost\n"), "/etc/nginx/sites-available/tg.winwidget.ru.bak-20260804-api-v1": []byte("old WinWidget backup\n"), "/etc/nginx/modules-enabled/99-winwidget-telegram-api-stream.conf": []byte("old WinWidget stream\n"), "/etc/letsencrypt/renewal/tg.winwidget.ru.conf": []byte("old renewal config\n")}
	for p, b := range old {
		writeFixture(t, p, b)
	}
	if err := os.Symlink("/etc/nginx/sites-available/tg.winwidget.ru", "/etc/nginx/sites-enabled/tg.winwidget.ru"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc/letsencrypt/archive/tg.winwidget.ru", "/etc/letsencrypt/live/tg.winwidget.ru", "/var/log/nginx", "/etc/nginx/modules-enabled"} {
		mustMkdir(t, p)
	}
	for _, n := range []string{"cert1.pem", "privkey1.pem"} {
		mustWrite(t, "/etc/letsencrypt/archive/tg.winwidget.ru/"+n, []byte("old cert\n"))
	}
	for _, n := range []string{"fullchain.pem", "privkey.pem"} {
		mustWrite(t, "/etc/letsencrypt/live/tg.winwidget.ru/"+n, []byte("old live cert\n"))
	}
	for _, n := range []string{"telegram-api-stream.access.log", "telegram-api-stream.error.log", "tg.winwidget.ru.access.log", "tg.winwidget.ru.error.log"} {
		mustWrite(t, "/var/log/nginx/"+n, []byte("old log\n"))
	}
	for _, p := range []string{deploy + "/nginx-http.conf.incoming", deploy + "/nginx-https.conf.incoming", deploy + "/deploy.sh.incoming"} {
		mustWrite(t, p, []byte("fixture staged file\n"))
	}
	mustWrite(t, deploy+"/receipt.txt", []byte("previous receipt\n"))
	mustMkdir(t, deploy+"/receipts")
	data, err := os.ReadFile("/tmp/deploy-source.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, entry := range []struct{ hash, path string }{{"a58f76fbff9d5852c3b7c6157c46b05347b0136e08c3ab864c4ac1c510ffaa1e", "/etc/nginx/sites-available/telegram.aerocrm.space"}, {"da5df41bb386f9b8627981e2eda3a44de00d6de2177f75ceb749a4dd1a080062", "/etc/nginx/snippets/aerocrm-telegram-webhooks.conf"}, {"9066c4678e22b37b986507bfb3d242a23e00b18f525e4a60305f3ff9680e142b", "/etc/nginx/.aerocrm-telegram-business-relay.json"}} {
		sum := sha256.Sum256(aero[entry.path])
		oldEntry := entry.hash + " " + entry.path
		if !strings.Contains(script, oldEntry) {
			t.Fatalf("expected aeroCRM checksum entry changed: %s", entry.path)
		}
		script = strings.Replace(script, oldEntry, hex.EncodeToString(sum[:])+" "+entry.path, 1)
	}
	mustWrite(t, fixture+"/deploy.sh", []byte(script))
	writeMocks(t, fakeBin, fixture, scenario)
	helper := os.Getenv("HC_RELAY_HELPER")
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(helperBytes)
	mustWrite(t, deploy+"/hc-ops.incoming", helperBytes)
	if err = os.Chmod(deploy+"/hc-ops.incoming", 0700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "RELAY_TEST_FAIL="+scenario, "HC_OPS_SHA256="+hex.EncodeToString(sum[:]))
	cmd := exec.Command("bash", fixture+"/deploy.sh", relaySHA, strings.Repeat("b", 64), "ops@example.com")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if (scenario == "success" || scenario == "late") && err != nil {
		t.Fatalf("successful deploy scenario failed: %v\n%s", err, out)
	}
	if scenario != "success" && scenario != "late" && err == nil {
		t.Fatalf("failure scenario unexpectedly succeeded: %s", out)
	}
	markers := map[string]string{"certonly": "Relay deployment failed; restoring previous nginx configuration", "postflight": "Telegram relay did not reach upstream", "tlspermanent": "Relay TLS vhost did not serve the issued certificate within 30 seconds", "delete": "Relay is active, but old certificate cleanup may be partial", "late": "Relay deployed and WinWidget proxy removed", "success": "Relay deployed and WinWidget proxy removed"}
	if !strings.Contains(string(out), markers[scenario]) {
		t.Fatalf("failed at wrong phase; expected %q; exit=%v, output:\n%s", markers[scenario], err, out)
	}
	probe := func(name string) int {
		b, e := os.ReadFile(fixture + "/" + name)
		if e != nil {
			return 0
		}
		var n int
		_, _ = fmt.Sscan(string(b), &n)
		return n
	}
	api := exists(fixture + "/api-probe")
	receipt, _ := os.ReadFile(deploy + "/receipt.txt")
	switch scenario {
	case "certonly":
		if !exists(fixture+"/certbot-certonly") || api || probe("relay-root-curl-probes") != 0 || probe("relay-tls-probes") != 0 {
			t.Fatal("certonly failure entered TLS readiness or relay postflight")
		}
	case "postflight":
		if !api || probe("relay-tls-probes") != 2 {
			t.Fatal("postflight failure did not follow successful TLS readiness")
		}
	case "tlspermanent":
		if api || probe("relay-tls-probes") < 5 {
			t.Fatal("TLS mismatch did not exhaust readiness before postflight")
		}
	case "delete":
		if !api || !exists(fixture+"/certbot-delete") {
			t.Fatal("partial cleanup did not follow relay postflight")
		}
	case "late":
		if !api || probe("relay-root-curl-probes") < 6 || probe("relay-tls-probes") < 5 {
			t.Fatal("late convergence did not retry curl and require two matching TLS probes")
		}
	case "success":
		if !api || !exists(fixture+"/certbot-delete") {
			t.Fatal("successful deployment skipped relay postflight/certificate cleanup")
		}
	}
	managed := []string{"/etc/nginx/sites-available/tg.winwidget.ru", "/etc/nginx/sites-enabled/tg.winwidget.ru", "/etc/nginx/sites-available/tg.winwidget.ru.bak-20260804-api-v1", "/etc/nginx/modules-enabled/99-winwidget-telegram-api-stream.conf", "/etc/letsencrypt/renewal/tg.winwidget.ru.conf", "/etc/letsencrypt/archive/tg.winwidget.ru", "/etc/letsencrypt/live/tg.winwidget.ru"}
	if scenario == "success" || scenario == "late" {
		for _, p := range managed {
			if exists(p) {
				t.Fatalf("successful deploy left WinWidget path %s", p)
			}
		}
		if !exists(fixture+"/ufw-deleted") || !strings.Contains(string(receipt), "status=complete\n") {
			t.Fatal("successful cleanup or receipt incomplete")
		}
		for p, b := range aero {
			if got, e := os.ReadFile(p); e != nil || string(got) != string(b) {
				t.Fatalf("aeroCRM file changed: %s", p)
			}
		}
		for _, n := range []string{"telegram-api-stream.access.log", "telegram-api-stream.error.log", "tg.winwidget.ru.access.log", "tg.winwidget.ru.error.log"} {
			if exists("/var/log/nginx/" + n) {
				t.Fatalf("obsolete log remains: %s", n)
			}
		}
	} else if scenario == "delete" {
		for _, p := range managed[:4] {
			if exists(p) {
				t.Fatalf("postcommit cleanup failure rolled back active relay or retained old config: %s", p)
			}
		}
		if exists(managed[5]) || !exists(managed[4]) || !exists(managed[6]) || !exists(fixture+"/ufw-deleted") || !strings.Contains(string(receipt), "status=cleanup_pending\n") || !isLink("/etc/nginx/sites-enabled/heartbeat-relay-https.conf") {
			t.Fatal("partial cleanup evidence does not match expected committed state")
		}
	} else {
		for p, b := range old {
			if got, e := os.ReadFile(p); e != nil || string(got) != string(b) {
				t.Fatalf("rollback failed to restore %s", p)
			}
		}
		link, e := os.Readlink("/etc/nginx/sites-enabled/tg.winwidget.ru")
		if e != nil || link != "/etc/nginx/sites-available/tg.winwidget.ru" {
			t.Fatal("rollback failed to restore WinWidget symlink")
		}
		if string(receipt) != "previous receipt\n" || exists(fixture+"/ufw-deleted") {
			t.Fatal("rollback changed receipt or removed firewall rule")
		}
		if scenario == "tlspermanent" && exists("/etc/nginx/sites-available/heartbeat-relay-https.conf") {
			t.Fatal("permanent TLS mismatch left candidate HTTPS vhost")
		}
	}
}

func writeMocks(t *testing.T, bin, fixture, scenario string) {
	t.Helper()
	scripts := map[string]string{
		"nginx": `#!/bin/bash
set -eu
case "${1:-}" in
  -t) [[ ${RELAY_TEST_FAIL:-} != 1 ]] ;;
  -T) shopt -s nullglob; for file in /etc/nginx/sites-enabled/* /etc/nginx/modules-enabled/*; do [[ -e $file || -L $file ]] || continue; cat "$file"; done ;;
  *) exit 2 ;;
esac
`,
		"systemctl": `#!/bin/bash
set -eu
[[ ${1:-} == is-active && ${2:-} == --quiet ]] && exit 0
[[ ${1:-} == reload && ${2:-} == nginx ]] && exit 0
exit 2
`,
		"getent":  "#!/bin/bash\nprintf '185.184.122.62 STREAM messengers-proxy.heartbeat-checker.ru\\n'\n",
		"timeout": "#!/bin/bash\nset -eu\nshift\nexec \"$@\"\n",
		"openssl": `#!/bin/bash
set -eu
case "${1:-}" in
  s_client) server_name=; while (($#)); do if [[ $1 == -servername ]]; then server_name=$2; shift 2; else shift; fi; done
    if [[ $server_name == telegram.aerocrm.space ]]; then printf 'aero-current\n'; elif [[ $server_name == messengers-proxy.heartbeat-checker.ru ]]; then count=0; [[ ! -f /tmp/relay-test-fixture/relay-tls-probes ]] || count=$(cat /tmp/relay-test-fixture/relay-tls-probes); count=$((count+1)); printf '%s\n' "$count" > /tmp/relay-test-fixture/relay-tls-probes; printf 'relay-current-%s\n' "$count"; else exit 2; fi ;;
  x509) if [[ ${2:-} == -in ]]; then printf 'sha256 Fingerprint=DD:EE:FF\n'; else certificate=$(cat); case "$certificate" in aero-current) printf 'sha256 Fingerprint=AA:BB:CC\n';; relay-current-*) count=${certificate#relay-current-}; if [[ ${RELAY_TEST_FAIL:-} == tlspermanent ]] || [[ ${RELAY_TEST_FAIL:-} == late && $count -lt 4 ]]; then printf 'sha256 Fingerprint=11:22:33\n'; else printf 'sha256 Fingerprint=DD:EE:FF\n'; fi;; *) exit 2;; esac; fi;;
  *) exit 2;;
esac
`,
		"curl": `#!/bin/bash
set -eu
output=;url=;while (($#));do case "$1" in -o) output=$2;shift 2;; -w|-X|-H|--max-time|--resolve|--data|--noproxy) shift 2;; -*) shift;; *) url=$1;shift;; esac;done
status=200;body=
case "$url" in
  https://telegram.aerocrm.space/) status=200;;
  https://messengers-proxy.heartbeat-checker.ru/) count=0;[[ ! -f /tmp/relay-test-fixture/relay-root-curl-probes ]]||count=$(cat /tmp/relay-test-fixture/relay-root-curl-probes);count=$((count+1));printf '%s\n' "$count" >/tmp/relay-test-fixture/relay-root-curl-probes;if [[ ${RELAY_TEST_FAIL:-} == late && $count -eq 1 ]];then exit 60;fi;status=404;;
  */telegram-api/*/getMe) printf 'called\n'>/tmp/relay-test-fixture/api-probe;status=401;body='{"ok":false,"error_code":401}';if [[ ${RELAY_TEST_FAIL:-} == postflight ]];then status=500;body='{"ok":false,"error_code":500}';fi;;
  */webhooks/support/telegram) status=401;body=unauthorized;; */telegram-api/*/setWebhook) status=404;; *) echo "unexpected mocked curl URL: $url" >&2;exit 2;; esac
if [[ -n $output && $output != /dev/null ]];then printf '%s' "$body">"$output";fi;printf '%s' "$status"
`,
		"certbot": `#!/bin/bash
set -eu
case "${1:-}" in certonly) printf 'called\n'>/tmp/relay-test-fixture/certbot-certonly;[[ ${RELAY_TEST_FAIL:-} != certonly ]]||exit 55;mkdir -p /etc/letsencrypt/live/messengers-proxy.heartbeat-checker.ru;printf 'fixture certificate\n'>/etc/letsencrypt/live/messengers-proxy.heartbeat-checker.ru/fullchain.pem;printf 'fixture key\n'>/etc/letsencrypt/live/messengers-proxy.heartbeat-checker.ru/privkey.pem;; delete) printf 'called\n'>/tmp/relay-test-fixture/certbot-delete;if [[ ${RELAY_TEST_FAIL:-} == delete ]];then rm -rf /etc/letsencrypt/archive/tg.winwidget.ru;exit 56;fi;rm -rf /etc/letsencrypt/archive/tg.winwidget.ru /etc/letsencrypt/live/tg.winwidget.ru;rm -f /etc/letsencrypt/renewal/tg.winwidget.ru.conf;; *) exit 2;;esac
`,
		"ufw": `#!/bin/bash
set -eu
if [[ ${1:-} == status ]];then printf 'Status: active\n8443/tcp ALLOW Anywhere\n';elif [[ ${1:-} == --force && ${2:-} == delete ]];then printf 'deleted\n'>/tmp/relay-test-fixture/ufw-deleted;elif [[ ${1:-} == allow ]];then printf 'restored\n'>/tmp/relay-test-fixture/ufw-restored;else exit 2;fi
`,
		"sleep": "#!/bin/bash\nexit 0\n",
	}
	for name, body := range scripts {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	_ = fixture
	_ = scenario
}

func runOutput(args ...string) (string, error) {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %w: %s", args, err, out)
	}
	return string(out), nil
}
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../"))
}
func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, path string, data []byte) { t.Helper(); writeFixture(t, path, data) }
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func isLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}
