package backenddeploy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	sourceSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policySHA = "7777777777777777777777777777777777777777777777777777777777777777"
	images    = "ghcr.io/nda17/heartbeat-checker_monorepo"
)

var (
	apiImage       = image("api", "1")
	workerImage    = image("worker", "2")
	probeImage     = image("probe", "3")
	migrateImage   = image("migrate", "4")
	backupImage    = image("backup", "5")
	firewallImage  = image("firewall", "6")
	artifactOnce   sync.Once
	probeServer    *httptest.Server
	yookassaServer *httptest.Server
	yookassaCert   []byte
	helperBytes    []byte
	helperSHA      string
	artifactErr    error
)

func image(name, digest string) string {
	return images + "/" + name + "@sha256:" + strings.Repeat(digest, 64)
}

// TestHelperProcess is the executable behind PATH-injected command shims.
// It only handles the explicitly selected command and never invokes Python.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("HC_MOCK_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	os.Exit(mockCommand(args[0], args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type fixture struct {
	t            *testing.T
	root         string
	backend      string
	bin          string
	log          string
	script       string
	stdin        string
	helperSHA    string
	probe        *httptest.Server
	yookassaRoot string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("", "hc-backend-deploy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &fixture{t: t, root: root, backend: filepath.Join(root, "backend"), bin: filepath.Join(root, "bin"), log: filepath.Join(root, "docker.jsonl"), stdin: filepath.Join(root, "docker-login.stdin")}
	for _, dir := range []string{f.backend, f.bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	scriptBytes, err := os.ReadFile(filepath.Join(repo, "deploy/backend/deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Keep the checked-in release script intact while redirecting its fixed VPS cwd.
	scriptBytes = bytes.Replace(scriptBytes, []byte("cd /opt/heartbeat-checker/backend"), []byte(`cd "$DEPLOY_DIR"`), 1)
	f.script = filepath.Join(root, "deploy.sh")
	if err := os.WriteFile(f.script, scriptBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	f.installMocks()
	f.write(".env", baseEnv, 0o600)
	f.write("compose.yaml.incoming", "compose candidate\n", 0o600)
	f.write("Caddyfile.incoming", "Caddy candidate\n", 0o600)
	f.write("compose.backup.yaml.incoming", "backup compose candidate\n", 0o600)
	f.write("backup.sh.incoming", "#!/usr/bin/env bash\nif [[ \"$1\" == preflight && \"${FAIL_BACKUP_PREFLIGHT:-0}\" == 1 ]]; then echo preflight-failed >&2; exit 1; fi\nexit 0\n", 0o700)
	f.write(".release.env", "API_IMAGE=old\n", 0o600)
	f.write("compose.yaml", "old compose\n", 0o600)
	f.write("Caddyfile", "old Caddy\n", 0o600)
	initArtifacts(t, repo)
	f.probe = probeServer
	f.yookassaRoot = filepath.Join(root, "yookassa-root.pem")
	if err := os.WriteFile(f.yookassaRoot, yookassaCert, 0o600); err != nil {
		t.Fatal(err)
	}
	f.installHCops(repo)
	return f
}

func initArtifacts(t *testing.T, repo string) {
	t.Helper()
	artifactOnce.Do(func() {
		probeServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-probe-secret" {
				http.Error(w, "unauthorized test request", http.StatusUnauthorized)
				return
			}
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch payload.URL {
			case "https://heartbeat-checker.ru/":
				_, _ = io.WriteString(w, `{"outcome":"UP","httpStatus":200}`)
			case "http://169.254.169.254/":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"unsupported_target"}`)
			default:
				http.Error(w, "unexpected probe target", http.StatusBadRequest)
			}
		}))
		yookassaServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			shop, secret, ok := r.BasicAuth()
			if r.Method != http.MethodGet || !ok || shop != "654321" || secret != "production_synthetic_secret_123" {
				http.Error(w, "invalid synthetic credentials", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"account_id":"654321","test":false,"status":"enabled"}`)
		}))
		yookassaCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: yookassaServer.Certificate().Raw})
		root, err := os.MkdirTemp("", "hc-backend-artifact-")
		if err != nil {
			artifactErr = err
			return
		}
		defer os.RemoveAll(root)
		deployguardPath := filepath.Join(repo, "tools/internal/deployguard/compose.go")
		original, err := os.ReadFile(deployguardPath)
		if err != nil {
			artifactErr = err
			return
		}
		const endpoint = "http://172.30.80.2:8090"
		if got := bytes.Count(original, []byte(endpoint)); got != 1 {
			artifactErr = fmt.Errorf("expected one fixed probe endpoint literal, found %d", got)
			return
		}
		patched := bytes.Replace(original, []byte(endpoint), []byte(probeServer.URL), 1)
		if bytes.Equal(original, patched) {
			artifactErr = fmt.Errorf("probe smoke endpoint anchor not found")
			return
		}
		overlaySource := filepath.Join(root, "deployguard-compose.go")
		if err := os.WriteFile(overlaySource, patched, 0o600); err != nil {
			artifactErr = err
			return
		}
		yookassaPath := filepath.Join(repo, "tools/internal/stageyookassa/stage.go")
		yookassaSource, err := os.ReadFile(yookassaPath)
		if err != nil {
			artifactErr = err
			return
		}
		const yookassaOrigin = "https://api.yookassa.ru"
		if got := bytes.Count(yookassaSource, []byte(yookassaOrigin)); got != 1 {
			artifactErr = fmt.Errorf("expected one YooKassa API origin literal, found %d", got)
			return
		}
		yookassaHost := strings.TrimPrefix(yookassaServer.URL, "https://")
		yookassaPatched := bytes.Replace(yookassaSource, []byte(yookassaOrigin), []byte("https://"+yookassaHost), 1)
		if bytes.Equal(yookassaSource, yookassaPatched) {
			artifactErr = fmt.Errorf("YooKassa API origin anchor not found")
			return
		}
		yookassaOverlaySource := filepath.Join(root, "stageyookassa.go")
		if err := os.WriteFile(yookassaOverlaySource, yookassaPatched, 0o600); err != nil {
			artifactErr = err
			return
		}
		overlay := filepath.Join(root, "go-overlay.json")
		overlayBytes, _ := json.Marshal(map[string]map[string]string{"Replace": {deployguardPath: overlaySource, yookassaPath: yookassaOverlaySource}})
		if err := os.WriteFile(overlay, overlayBytes, 0o600); err != nil {
			artifactErr = err
			return
		}
		bin := filepath.Join(root, "hc-ops")
		cmd := exec.Command("go", "build", "-overlay", overlay, "-ldflags", "-X main.sourceSHA="+sourceSHA, "-o", bin, "./cmd/hc-ops")
		cmd.Dir = filepath.Join(repo, "tools")
		if out, err := cmd.CombinedOutput(); err != nil {
			artifactErr = fmt.Errorf("build hc-ops: %w\n%s", err, out)
			return
		}
		helperBytes, err = os.ReadFile(bin)
		if err != nil {
			artifactErr = err
			return
		}
		digest := sha256.Sum256(helperBytes)
		helperSHA = hex.EncodeToString(digest[:])
	})
	if artifactErr != nil {
		t.Fatal(artifactErr)
	}
}

const baseEnv = "DATABASE_URL=test\nTELEGRAM_API_BASE_URL=https://api.telegram.org\nKEEP_EXISTING=one\nTELEGRAM_API_PROXY_IP=\n"
const nextEnv = "DATABASE_URL=test\nTELEGRAM_API_PROXY_IP=185.184.122.62\nKEEP_EXISTING=one\nTELEGRAM_API_BASE_URL=https://messengers-proxy.heartbeat-checker.ru/telegram-api\n"

func (f *fixture) installHCops(repo string) {
	f.t.Helper()
	if err := os.WriteFile(f.path("hc-ops.incoming"), helperBytes, 0o700); err != nil {
		f.t.Fatal(err)
	}
	f.helperSHA = helperSHA
}

func (f *fixture) installMocks() {
	f.t.Helper()
	self, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	shim := filepath.Join(f.root, "mock-command.sh")
	body := "#!/bin/sh\nHC_MOCK_PROCESS=1 exec " + shellQuote(self) + " -test.run=^TestHelperProcess$ -- \"$(basename \"$0\")\" \"$@\"\n"
	if err := os.WriteFile(shim, []byte(body), 0o700); err != nil {
		f.t.Fatal(err)
	}
	for _, name := range []string{"docker", "curl", "sleep", "stat", "flock", "cp", "iptables"} {
		if err := os.Symlink(shim, filepath.Join(f.bin, name)); err != nil {
			f.t.Fatal(err)
		}
	}
}

func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
func (f *fixture) write(name, contents string, mode os.FileMode) {
	f.t.Helper()
	path := filepath.Join(f.backend, name)
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) path(name string) string { return filepath.Join(f.backend, name) }
func (f *fixture) read(name string) string {
	f.t.Helper()
	b, err := os.ReadFile(f.path(name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}
func (f *fixture) run(stdin string, extra map[string]string, flags ...bool) *exec.Cmd {
	f.t.Helper()
	base := []string{sourceSHA, apiImage, workerImage, probeImage, migrateImage, "nda17", backupImage}
	for i := 0; i < 5; i++ {
		value := false
		if i < len(flags) {
			value = flags[i]
		}
		base = append(base, fmt.Sprint(value))
	}
	base = append(base, firewallImage, policySHA)
	if len(flags) > 5 {
		// Later flags keep the legacy 15..18 positional contract.
		base = append(base, fmt.Sprint(flags[5]))
		if len(flags) > 6 {
			base = append(base, fmt.Sprint(flags[6]))
		} else if len(flags) > 7 {
			base = append(base, "false")
		}
		if len(flags) > 7 {
			base = append(base, fmt.Sprint(flags[7]))
		} else if len(flags) > 8 {
			base = append(base, "false")
		}
		if len(flags) > 8 {
			for len(base) < 18 {
				base = append(base, "false")
			}
			base[17] = fmt.Sprint(flags[8])
		}
	}
	cmd := exec.Command("/bin/bash", append([]string{f.script}, base...)...)
	cmd.Dir = f.root
	env := append([]string(nil), os.Environ()...)
	env = append(env, "PATH="+f.bin+":"+os.Getenv("PATH"), "DEPLOY_DIR="+f.backend, "DOCKER_LOG="+f.log, "SOURCE_SHA="+sourceSHA, "FIREWALL_POLICY_SHA="+policySHA, "FIREWALL_POLICY_OUTPUT="+policySHA, "DOCKER_LOGIN_STDIN="+f.stdin, "HC_OPS_SHA256="+f.helperSHA, "SSL_CERT_FILE="+f.yookassaRoot, "GORACE="+raceOptionsWithNoExitDelay())
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	// Bash read -n can leave the terminating LF queued, as the CI framing does.
	cmd.Stdin = strings.NewReader(stdin)
	return cmd
}

func raceOptionsWithNoExitDelay() string {
	options := strings.Fields(os.Getenv("GORACE"))
	merged := make([]string, 0, len(options)+1)
	for _, option := range options {
		if !strings.HasPrefix(option, "atexit_sleep_ms=") {
			merged = append(merged, option)
		}
	}
	return strings.Join(append(merged, "atexit_sleep_ms=0"), " ")
}

type result struct {
	code           int
	stdout, stderr string
}

func (f *fixture) runResult(stdin string, extra map[string]string, flags ...bool) result {
	cmd := f.run(stdin, extra, flags...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			f.t.Fatalf("run deploy: %v", err)
		}
	}
	return result{code, out.String(), stderr.String()}
}

func (f *fixture) dockerCalls() [][]string {
	f.t.Helper()
	b, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var calls [][]string
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		var args []string
		if err := json.Unmarshal(scanner.Bytes(), &args); err != nil {
			f.t.Fatal(err)
		}
		calls = append(calls, args)
	}
	if err := scanner.Err(); err != nil {
		f.t.Fatal(err)
	}
	return calls
}

func mockCommand(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch name {
	case "sleep":
		return 0
	case "flock":
		if os.Getenv("FAIL_FLOCK") == "1" {
			return 1
		}
		return 0
	case "stat":
		if len(args) == 3 && args[0] == "-c" && args[1] == "%a" {
			info, err := os.Stat(args[2])
			if err != nil {
				return 1
			}
			fmt.Fprintf(stdout, "%o\n", info.Mode().Perm())
			return 0
		}
		return 1
	case "cp":
		if os.Getenv("FAIL_ENV_ROLLBACK_COPY") == "1" && len(args) > 0 && args[len(args)-1] == ".env.rollback.incoming" {
			return 1
		}
		return copyCommand(args)
	case "curl":
		for _, arg := range args {
			if arg == "-w" {
				fmt.Fprint(stdout, envOr("CURL_STATUS", "403"))
				break
			}
		}
		return 0
	case "docker":
		return mockDocker(args, stdin, stdout, stderr)
	case "iptables":
		return mockIPTables(args, stdout)
	}
	return 127
}

func envOr(k, fallback string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return fallback
}

func copyCommand(args []string) int {
	var paths []string
	for _, arg := range args {
		if arg != "-p" && arg != "-f" && arg != "--" {
			paths = append(paths, arg)
		}
	}
	if len(paths) < 2 {
		return 1
	}
	dest := paths[len(paths)-1]
	for _, src := range paths[:len(paths)-1] {
		info, err := os.Stat(src)
		if err != nil {
			return 1
		}
		if info.IsDir() {
			return 1
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return 1
		}
		mode := info.Mode().Perm()
		if err := os.WriteFile(dest, b, mode); err != nil {
			return 1
		}
		_ = os.Chmod(dest, mode)
	}
	return 0
}

func mockDocker(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	logPath := os.Getenv("DOCKER_LOG")
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 1
		}
		_ = json.NewEncoder(f).Encode(args)
		_ = f.Close()
	}
	if len(args) >= 2 && args[0] == "login" && args[1] == "ghcr.io" {
		b, _ := io.ReadAll(stdin)
		if path := os.Getenv("DOCKER_LOGIN_STDIN"); path != "" {
			_ = os.WriteFile(path, b, 0o600)
		}
		if os.Getenv("MUTATE_NEXT_DURING_DEPLOY") == "1" {
			path := filepath.Join(os.Getenv("DEPLOY_DIR"), ".env.next")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err == nil {
				_, _ = f.WriteString("# changed during deployment\n")
				_ = f.Close()
			}
		}
		return 0
	}
	if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
		fmt.Fprintln(stdout, sourceSHA)
		return 0
	}
	if len(args) > 0 && args[0] == "run" {
		for _, arg := range args {
			if strings.HasPrefix(arg, images+"/firewall@sha256:") {
				if os.Getenv("FAIL_FIREWALL_INSTALL") == "1" {
					return 43
				}
				fmt.Fprintln(stdout, envOr("FIREWALL_POLICY_OUTPUT", policySHA))
				return 0
			}
		}
	}
	if len(args) > 0 && args[0] == "compose" && strings.Contains(strings.Join(args, " "), "config") && os.Getenv("FAIL_COMPOSE_PREFLIGHT") == "1" {
		return 42
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--profile migration") {
		if os.Getenv("FAIL_MIGRATION") == "1" {
			return 1
		}
		return 0
	}
	if len(args) > 1 && args[0] == "inspect" {
		if strings.Contains(joined, "Health.Status") {
			fmt.Fprintln(stdout, "healthy")
		} else {
			fmt.Fprintln(stdout, "true")
		}
		return 0
	}
	if len(args) > 0 && args[0] == "compose" {
		if strings.Contains(joined, "config") && strings.Contains(joined, "--format") {
			api := map[string]string{"DATABASE_URL": "postgres://heartbeat:testpass@postgres:5432/heartbeat?sslmode=disable", "AUTH_MFA_ENCRYPTION_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "EMAIL_NOTIFICATIONS_ENABLED": "false", "ANDROID_PUSH_ENABLED": "false", "RUSTORE_PUSH_PROJECT_ID": ""}
			worker := map[string]string{"SMTP_TLS_MODE": "implicit_tls", "EMAIL_NOTIFICATIONS_ENABLED": "false", "ANDROID_PUSH_ENABLED": "false", "RUSTORE_PUSH_PROJECT_ID": ""}
			if os.Getenv("COMPOSE_EMAIL_KEYS") == "1" {
				api["EMAIL_NOTIFICATIONS_ENABLED"] = "true"
				worker["EMAIL_NOTIFICATIONS_ENABLED"] = "true"
			}
			if os.Getenv("COMPOSE_PUSH_KEYS") == "1" {
				api["ANDROID_PUSH_ENABLED"] = "true"
				api["RUSTORE_PUSH_PROJECT_ID"] = "synthetic-project"
				worker["ANDROID_PUSH_ENABLED"] = "true"
				worker["RUSTORE_PUSH_PROJECT_ID"] = "synthetic-project"
				worker["RUSTORE_PUSH_SERVICE_TOKEN"] = "synthetic-push-service-token-0123456789-abcdefgh"
			}
			if os.Getenv("COMPOSE_FCM_PUSH_KEYS") == "1" {
				api["FCM_PUSH_ENABLED"] = "true"
				api["FCM_PROJECT_ID"] = "synthetic-fcm-project"
				worker["FCM_PUSH_ENABLED"] = "true"
				worker["FCM_PROJECT_ID"] = "synthetic-fcm-project"
				worker["FCM_SERVICE_ACCOUNT_BASE64"] = "synthetic-fcm-service-account"
			}
			if os.Getenv("COMPOSE_OAUTH_KEYS") == "1" {
				api["YANDEX_CLIENT_ID"] = "synthetic-yandex-client"
				api["YANDEX_CLIENT_SECRET"] = "synthetic-yandex-secret-0123456789"
				api["YANDEX_REDIRECT_URI"] = "https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback"
				api["YANDEX_SCOPES"] = "login:email login:info"
				api["VK_CLIENT_ID"] = "12345678"
				api["VK_CALLBACK_URL"] = "https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback"
			}
			if os.Getenv("COMPOSE_YOOKASSA_KEYS") == "1" {
				for k, v := range map[string]string{"BILLING_PROVIDER": "yookassa", "BILLING_TEST_ACCOUNT_IDS": "51ec7d19-80c4-4fb6-8edb-79dc7abe3120", "YOOKASSA_MODE": "test", "YOOKASSA_TEST_SHOP_ID": "123456", "YOOKASSA_TEST_SECRET_KEY": "test_synthetic_secret_123"} {
					api[k] = v
					if k != "BILLING_PROVIDER" {
						worker[k] = v
					}
				}
			}
			if os.Getenv("COMPOSE_YOOKASSA_PRODUCTION_KEYS") == "1" {
				for k, v := range map[string]string{"BILLING_PROVIDER": "yookassa", "YOOKASSA_MODE": "production", "YOOKASSA_PRODUCTION_SHOP_ID": "654321", "YOOKASSA_PRODUCTION_SECRET_KEY": "production_synthetic_secret_123"} {
					api[k] = v
					if k != "BILLING_PROVIDER" {
						worker[k] = v
					}
				}
			}
			payload := map[string]any{"services": map[string]any{"api": map[string]any{"environment": api}, "postgres": map[string]any{"environment": map[string]string{"POSTGRES_PASSWORD": "testpass"}}, "worker": map[string]any{"environment": worker}, "probe": map[string]any{"environment": map[string]string{"PROBE_SHARED_SECRET": "test-probe-secret"}}, "caddy": map[string]any{"environment": map[string]string{}}, "migrate": map[string]any{"environment": map[string]string{}}, "backup": map[string]any{"environment": map[string]string{}}}}
			encoded, _ := json.Marshal(payload)
			if path := os.Getenv("COMPOSE_JSON_CAPTURE"); path != "" {
				_ = os.WriteFile(path, encoded, 0o600)
			}
			_, _ = stdout.Write(append(encoded, '\n'))
			return 0
		}
		if strings.Contains(joined, "ps -q") {
			fmt.Fprintln(stdout, "container-id")
			return 0
		}
		if strings.Contains(joined, "exec") {
			if strings.Contains(joined, "billing.orders") && strings.Contains(joined, "webhook_inbox") {
				fmt.Fprintln(stdout, envOr("YOOKASSA_SWITCH_BLOCKERS", "0"))
				return 0
			}
			if strings.Contains(joined, "pg_dump") {
				fmt.Fprintln(stdout, "isolated-test-database-dump")
				return 0
			}
			if strings.Contains(joined, "schema_migrations") {
				fmt.Fprintln(stdout, "6")
				return 0
			}
			if strings.Contains(joined, "SELECT 1") {
				fmt.Fprintln(stdout, "1")
				return 0
			}
			if strings.Contains(joined, "psql") && os.Getenv("OWNER_IMPORT_RESULT") != "" {
				payload, _ := io.ReadAll(stdin)
				if path := os.Getenv("OWNER_IMPORT_STDIN"); path != "" {
					_ = os.WriteFile(path, payload, 0o600)
				}
				fmt.Fprintln(stdout, os.Getenv("OWNER_IMPORT_RESULT"))
				return 0
			}
		}
	}
	return 0
}

func mockIPTables(args []string, stdout io.Writer) int {
	path := os.Getenv("IPTABLES_STATE")
	state := map[string][][]string{"DOCKER-USER": {}, "INPUT": {}, "FORWARD": {{"-j", "DOCKER-USER"}}}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &state) != nil {
			return 2
		}
	} else if !os.IsNotExist(err) {
		return 2
	}
	save := func() bool {
		b, err := json.Marshal(state)
		if err != nil {
			return false
		}
		return os.WriteFile(path, b, 0o600) == nil
	}
	if len(args) < 2 {
		return 2
	}
	chain := args[1]
	switch args[0] {
	case "-N":
		if _, exists := state[chain]; exists {
			return 1
		}
		state[chain] = [][]string{}
		if !save() {
			return 2
		}
		return 0
	case "-S":
		rules, ok := state[chain]
		if !ok {
			return 1
		}
		for _, rule := range rules {
			fmt.Fprintln(stdout, "-A "+chain+" "+strings.Join(rule, " "))
		}
		return 0
	case "-L":
		rules, ok := state[chain]
		if !ok {
			return 1
		}
		fmt.Fprintln(stdout, "Chain "+chain)
		for i, rule := range rules {
			fmt.Fprintf(stdout, "%d %s\n", i+1, strings.Join(rule, " "))
		}
		return 0
	case "-C":
		rules, ok := state[chain]
		if !ok {
			return 1
		}
		want := args[2:]
		for _, rule := range rules {
			if sameStrings(rule, want) {
				return 0
			}
		}
		return 1
	case "-A":
		if _, ok := state[chain]; !ok {
			return 1
		}
		state[chain] = append(state[chain], args[2:])
		if !save() {
			return 2
		}
		return 0
	case "-I":
		rules, ok := state[chain]
		if !ok {
			return 1
		}
		pos, ruleStart := 0, 2
		if len(args) > 2 {
			if n, err := strconv.Atoi(args[2]); err == nil {
				pos, ruleStart = n-1, 3
			}
		}
		if pos < 0 {
			pos = 0
		}
		if pos > len(rules) {
			pos = len(rules)
		}
		rule := append([]string(nil), args[ruleStart:]...)
		rules = append(rules, nil)
		copy(rules[pos+1:], rules[pos:])
		rules[pos] = rule
		state[chain] = rules
		if !save() {
			return 2
		}
		return 0
	}
	return 2
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBackendDeploySuccessCreatesReceiptAndRecoveryEvidence(t *testing.T) {
	f := newFixture(t)
	r := f.runResult("test-token\n", nil)
	if r.code != 0 {
		t.Fatalf("deploy failed (%d): %s\n%s\nDocker calls: %#v", r.code, r.stdout, r.stderr, f.dockerCalls())
	}
	if !strings.Contains(r.stdout, "Backend deployed: "+sourceSHA) {
		t.Fatalf("missing completion line: %q", r.stdout)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["source_sha"] != sourceSHA {
		t.Fatalf("receipt source_sha = %v", receipt["source_sha"])
	}
	loginInput, err := os.ReadFile(f.stdin)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(loginInput); got != "test-token\n" {
		t.Fatalf("docker login received %q", got)
	}
	if _, err := os.Stat(f.path("pending")); !os.IsNotExist(err) {
		t.Fatalf("pending journal remains after success: %v", err)
	}
	if _, err := os.Stat(f.path("hc-ops")); err != nil {
		t.Fatalf("installed helper missing: %v", err)
	}
	if _, err := os.Stat(f.path("recovery")); err != nil {
		t.Fatalf("recovery inputs missing: %v", err)
	}
}

func TestInvalidHelperChecksumStopsBeforeDockerAndDoesNotLeakToken(t *testing.T) {
	f := newFixture(t)
	r := f.runResult("test-token\n", map[string]string{"HC_OPS_SHA256": strings.Repeat("0", 64)})
	if r.code == 0 || !strings.Contains(r.stderr, "transport checksum mismatch") {
		t.Fatalf("unexpected result: %+v", r)
	}
	if calls := f.dockerCalls(); len(calls) != 0 {
		t.Fatalf("docker called before helper verification: %v", calls)
	}
	if strings.Contains(r.stdout+r.stderr, "test-token") {
		t.Fatal("registry token appeared in output")
	}
}

func TestHelperVersionMustMatchReleaseSourceEvenWithValidTransportChecksum(t *testing.T) {
	f := newFixture(t)
	cmd := f.run("test-token\n", nil)
	cmd.Args[2] = strings.Repeat("b", 40)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil || !strings.Contains(stderr.String(), "Deployment helper source/checksum mismatch") {
		t.Fatalf("helper built for another source accepted: %v %s", err, stderr.String())
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called before source pin validation: %v", f.dockerCalls())
	}
}

func TestDeploymentLockFailureStopsBeforeDocker(t *testing.T) {
	f := newFixture(t)
	candidate := `{"shopId":"123456","secretKey":"test_synthetic_secret_123"}`
	r := f.runResult("test-token\n"+candidate+"\n", map[string]string{"FAIL_FLOCK": "1"}, false, false, false, false, false, true, false)
	if r.code == 0 || !strings.Contains(r.stderr, "Another backend deployment is in progress") {
		t.Fatalf("deployment lock failure ignored: %+v", r)
	}
	if got := f.read(".env"); got != baseEnv {
		t.Fatalf("lock rejection changed active env: %q", got)
	}
	if _, err := os.Stat(f.path(".env.next")); !os.IsNotExist(err) {
		t.Fatalf("lock rejection staged a YooKassa candidate: %v", err)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called while deployment lock held: %v", f.dockerCalls())
	}
}

func TestPreviousHelperMustMatchPreviousReleaseAndIsRetainedForRecovery(t *testing.T) {
	f := newFixture(t)
	f.write(".release.env", "SOURCE_SHA="+sourceSHA+"\nAPI_IMAGE=old\n", 0o600)
	if err := os.WriteFile(f.path("hc-ops"), helperBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path("hc-ops.sha256"), []byte(helperSHA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.runResult("test-token\n", nil)
	if r.code != 0 {
		t.Fatalf("deploy with a valid previous helper failed: %s", r.stderr)
	}
	recoveryEntries, err := os.ReadDir(f.path("recovery"))
	if err != nil || len(recoveryEntries) == 0 {
		t.Fatalf("recovery folder missing: %v", err)
	}
	recovery := filepath.Join(f.path("recovery"), recoveryEntries[0].Name())
	previous, err := os.ReadFile(filepath.Join(recovery, "hc-ops"))
	if err != nil || !bytes.Equal(previous, helperBytes) {
		t.Fatalf("previous helper not retained intact: %v", err)
	}
	previousSHA, err := os.ReadFile(filepath.Join(recovery, "hc-ops.sha256"))
	if err != nil || strings.TrimSpace(string(previousSHA)) != helperSHA {
		t.Fatalf("previous helper checksum not retained: %q %v", previousSHA, err)
	}
	if mode := mustMode(t, filepath.Join(recovery, "hc-ops")); mode != 0o700 {
		t.Fatalf("retained helper mode=%o", mode)
	}
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestPreMigrationComposeFailureRestoresActivatedEnv(t *testing.T) {
	f := newFixture(t)
	f.write(".env.next", nextEnv, 0o600)
	r := f.runResult("test-token\n", map[string]string{"FAIL_COMPOSE_PREFLIGHT": "1"}, true)
	if r.code != 42 {
		t.Fatalf("exit code = %d, want 42: %s", r.code, r.stderr)
	}
	if got := f.read(".env"); got != baseEnv {
		t.Fatalf("active env was not rolled back:\n%s", got)
	}
	if got := f.read(".env.next"); got != nextEnv {
		t.Fatalf("staged env changed:\n%s", got)
	}
	if got := f.read(".release.env"); got != "API_IMAGE=old\n" {
		t.Fatalf("release was not restored: %q", got)
	}
	if _, err := os.Stat(f.path("pending")); !os.IsNotExist(err) {
		t.Fatalf("unexpected pending journal: %v", err)
	}
}

func TestMigrationFailureKeepsForwardRecoveryJournal(t *testing.T) {
	f := newFixture(t)
	r := f.runResult("test-token\n", map[string]string{"FAIL_MIGRATION": "1"})
	if r.code == 0 {
		t.Fatal("migration failure returned success")
	}
	pending := f.read("pending")
	if !strings.Contains(pending, "phase=migration_started") {
		t.Fatalf("missing forward recovery phase: %s", pending)
	}
	if !strings.Contains(pending, "backup_sha256=") || !strings.Contains(pending, "recovery=") {
		t.Fatalf("recovery metadata incomplete: %s", pending)
	}
	if _, err := os.Stat(f.path("backups")); err != nil {
		t.Fatalf("backup directory missing: %v", err)
	}
	if strings.Contains(r.stdout+r.stderr, "test-token") {
		t.Fatal("registry token leaked on failure")
	}
}

func TestMissingEnvIsRejectedBeforeAnyReleaseMutation(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.path(".env")); err != nil {
		t.Fatal(err)
	}
	r := f.runResult("test-token\n", nil)
	if r.code == 0 || !strings.Contains(r.stderr, "mode-600 .env") {
		t.Fatalf("unexpected result: %+v", r)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatal("docker was called without provisioned env")
	}
	if got := f.read(".release.env"); got != "API_IMAGE=old\n" {
		t.Fatalf("release changed: %q", got)
	}
}

func TestSecretsAreNotLoggedAndCandidateIgnoredWithoutOptIn(t *testing.T) {
	f := newFixture(t)
	secret := "synthetic-secret-value-123456"
	f.write(".env.next", baseEnv+"UNRELATED_SECRET="+secret+"\n", 0o600)
	r := f.runResult("registry-token-123\n", nil)
	if r.code != 0 {
		t.Fatalf("deploy failed: %s", r.stderr)
	}
	log, _ := os.ReadFile(f.log)
	if strings.Contains(string(log)+r.stdout+r.stderr, secret) || strings.Contains(string(log)+r.stdout+r.stderr, "registry-token-123") {
		t.Fatal("secret was emitted")
	}
	if got := f.read(".env.next"); !strings.Contains(got, secret) {
		t.Fatal("candidate was consumed without explicit activation")
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["telegram_relay_activated"] != false || receipt["messenger_notifications_activated"] != false {
		t.Fatalf("explicitly false activation flags not recorded in receipt: %#v", receipt)
	}
}

func TestCandidateActivationPreservesLiteralDollarAndConsumesOnlyAfterSuccess(t *testing.T) {
	f := newFixture(t)
	f.write(".env", strings.Replace(baseEnv, "KEEP_EXISTING=one", "KEEP_EXISTING=$$HOME", 1), 0o600)
	candidate := strings.Replace(nextEnv, "KEEP_EXISTING=one", "KEEP_EXISTING=$$HOME", 1)
	f.write(".env.next", candidate, 0o600)
	r := f.runResult("test-token\n", nil, true)
	if r.code != 0 {
		t.Fatalf("activation failed (%d): %s", r.code, r.stderr)
	}
	if got := f.read(".env"); got != candidate {
		t.Fatalf("activated bytes differ\nwant: %q\ngot:  %q", candidate, got)
	}
	if _, err := os.Stat(f.path(".env.next")); !os.IsNotExist(err) {
		t.Fatalf("candidate remains after success: %v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["telegram_relay_activated"] != true {
		t.Fatalf("relay activation missing from receipt: %#v", receipt)
	}
}

func TestActivationRejectsUnknownDuplicateMalformedAndSymlinkCandidates(t *testing.T) {
	cases := []struct {
		name, contents, reason string
		symlink                bool
		mode                   os.FileMode
	}{
		{"unknown key", nextEnv + "UNRELATED_SECRET=not-allowed\n", "Relay activation rejected: changes outside selected keys", false, 0o600},
		{"duplicate key", nextEnv + "TELEGRAM_API_BASE_URL=https://api.telegram.org\n", "duplicate", false, 0o600},
		{"malformed line", nextEnv + "not a key-value row\n", "invalid env", false, 0o600},
		{"wrong endpoint", strings.Replace(nextEnv, "/telegram-api", "/other", 1), "unexpected Telegram endpoint", false, 0o600},
		{"missing relay target", strings.Replace(nextEnv, "TELEGRAM_API_PROXY_IP=185.184.122.62\n", "", 1), "unexpected Telegram endpoint", false, 0o600},
		{"wrong mode", nextEnv, "regular mode-600", false, 0o644},
		{"symlink", nextEnv, "regular mode-600", true, 0o600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.symlink {
				f.write(".env.next.target", tc.contents, 0o600)
				if err := os.Symlink(".env.next.target", f.path(".env.next")); err != nil {
					t.Fatal(err)
				}
			} else {
				f.write(".env.next", tc.contents, tc.mode)
			}
			before, err := os.ReadFile(f.path(".env"))
			if err != nil {
				t.Fatal(err)
			}
			r := f.runResult("test-token\n", nil, true)
			if r.code == 0 || !strings.Contains(r.stderr, tc.reason) {
				t.Fatalf("candidate accepted or wrong error: %+v", r)
			}
			if got, err := os.ReadFile(f.path(".env")); err != nil || !bytes.Equal(got, before) {
				t.Fatalf("active env changed: %q, %v", got, err)
			}
			if len(f.dockerCalls()) != 0 {
				t.Fatalf("Docker called before candidate validation: %v", f.dockerCalls())
			}
			if _, err := os.Stat(f.path("pending")); !os.IsNotExist(err) {
				t.Fatalf("unexpected pending journal: %v", err)
			}
		})
	}
}

func TestActivationRollbackCopyFailureRetainsOnlyProtectedRecoverySnapshot(t *testing.T) {
	f := newFixture(t)
	f.write(".env.next", nextEnv, 0o600)
	r := f.runResult("registry-token-secret\n", map[string]string{"FAIL_COMPOSE_PREFLIGHT": "1", "FAIL_ENV_ROLLBACK_COPY": "1"}, true)
	if r.code != 42 || !strings.Contains(r.stderr, "previous backend .env could not be restored") {
		t.Fatalf("unexpected rollback failure result: %+v", r)
	}
	pending := f.read("pending")
	if !strings.Contains(pending, "phase=env_rollback_failed") {
		t.Fatalf("rollback failure did not retain journal: %s", pending)
	}
	var snapshot string
	for _, line := range strings.Split(pending, "\n") {
		if strings.HasPrefix(line, "env_snapshot=") {
			snapshot = strings.TrimPrefix(line, "env_snapshot=")
		}
	}
	if snapshot == "" {
		t.Fatalf("recovery env snapshot path missing: %s", pending)
	}
	b, err := os.ReadFile(f.path(snapshot))
	if err != nil || string(b) != baseEnv {
		t.Fatalf("prior env snapshot mismatch: %q, %v", b, err)
	}
	if strings.Contains(r.stdout+r.stderr, "registry-token-secret") {
		t.Fatal("credential leaked while retaining rollback evidence")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.path(snapshot)), "docker-config")); !os.IsNotExist(err) {
		t.Fatalf("transient registry config retained: %v", err)
	}
	if calls := f.dockerCalls(); hasCall(calls, "up") {
		t.Fatalf("services restarted after failed env rollback: %v", calls)
	}
}

func TestBackupPreflightFailureKeepsReleaseAndSkipsMigration(t *testing.T) {
	f := newFixture(t)
	r := f.runResult("test-token\n", map[string]string{"FAIL_BACKUP_PREFLIGHT": "1"})
	if r.code == 0 || !strings.Contains(r.stderr, "preflight-failed") {
		t.Fatalf("preflight failure not propagated: %+v", r)
	}
	if got := f.read(".release.env"); got != "API_IMAGE=old\n" {
		t.Fatalf("release env changed: %q", got)
	}
	if hasCall(f.dockerCalls(), "migration") {
		t.Fatalf("migration ran before backup preflight: %v", f.dockerCalls())
	}
	if _, err := os.Stat(f.path("pending")); !os.IsNotExist(err) {
		t.Fatalf("pending journal created before preflight: %v", err)
	}
}

func TestFirewallPolicyMustInstallBeforeMigration(t *testing.T) {
	f := newFixture(t)
	r := f.runResult("test-token\n", nil)
	if r.code != 0 {
		t.Fatalf("deploy failed: %s", r.stderr)
	}
	calls := f.dockerCalls()
	firewall, migration := indexCall(calls, "run"), indexCall(calls, "migration")
	if firewall < 0 || migration < 0 || firewall >= migration {
		t.Fatalf("firewall install must precede migration: firewall=%d migration=%d calls=%v", firewall, migration, calls)
	}
}

func TestPreviousPendingJournalFailsClosedBeforeDocker(t *testing.T) {
	f := newFixture(t)
	f.write("pending", "source_sha=old\nphase=migration_started\n", 0o600)
	r := f.runResult("test-token\n", nil)
	if r.code == 0 || !strings.Contains(r.stderr, "Resolve the previous pending deployment") {
		t.Fatalf("pending journal did not block retry: %+v", r)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called with unresolved journal: %v", f.dockerCalls())
	}
}

func TestPreflightRejectsBadEnvironmentAndStagingFilesystemTypes(t *testing.T) {
	t.Run("unsafe env mode", func(t *testing.T) {
		f := newFixture(t)
		_ = os.Chmod(f.path(".env"), 0o644)
		r := f.runResult("test-token\n", nil)
		if r.code == 0 || !strings.Contains(r.stderr, "mode-600 .env") {
			t.Fatalf("unsafe env accepted: %+v", r)
		}
		if len(f.dockerCalls()) != 0 {
			t.Fatal("Docker called")
		}
	})
	t.Run("env symlink", func(t *testing.T) {
		f := newFixture(t)
		if err := os.Rename(f.path(".env"), f.path(".env.real")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(".env.real", f.path(".env")); err != nil {
			t.Fatal(err)
		}
		r := f.runResult("test-token\n", nil)
		if r.code == 0 || !strings.Contains(r.stderr, "mode-600 .env") {
			t.Fatalf("symlink env accepted: %+v", r)
		}
		if len(f.dockerCalls()) != 0 {
			t.Fatal("Docker called")
		}
	})
	t.Run("missing staged compose", func(t *testing.T) {
		f := newFixture(t)
		_ = os.Remove(f.path("compose.yaml.incoming"))
		r := f.runResult("test-token\n", nil)
		if r.code == 0 || !strings.Contains(r.stderr, "Missing staged deployment files") {
			t.Fatalf("missing stage accepted: %+v", r)
		}
		if len(f.dockerCalls()) != 0 {
			t.Fatal("Docker called")
		}
	})
}

func TestInvalidReleaseArgumentsAndImagesFailBeforeDocker(t *testing.T) {
	f := newFixture(t)
	cmd := f.run("test-token\n", nil)
	cmd.Args[2] = "not-a-source-sha"
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("invalid source SHA accepted")
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called for invalid arguments: %v", f.dockerCalls())
	}
	f2 := newFixture(t)
	cmd = f2.run("test-token\n", nil)
	cmd.Args[3] = "mutable:latest"
	stderr.Reset()
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil || !strings.Contains(stderr.String(), "Expected immutable GHCR api image") {
		t.Fatalf("mutable image accepted: %v %s", err, stderr.String())
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("Docker called for mutable image: %v", f2.dockerCalls())
	}
}

func TestEmailActivationChangesOnlyFlagAndKeepsSMTPSecretPrivate(t *testing.T) {
	f := newFixture(t)
	original := baseEnv + "EMAIL_NOTIFICATIONS_ENABLED=false\nSMTP_ADDR=smtp.example.test:465\nSMTP_PASSWORD=synthetic-smtp-password\nSMTP_TLS_MODE=implicit_tls\nAUTH_MAIL_ENCRYPTION_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
	f.write(".env", original, 0o600)
	flags := []bool{false, false, false, false, false, false, true}
	r := f.runResult("test-token\n", map[string]string{"COMPOSE_EMAIL_KEYS": "1"}, flags...)
	if r.code != 0 {
		t.Fatalf("email activation failed: %s", r.stderr)
	}
	want := strings.Replace(original, "EMAIL_NOTIFICATIONS_ENABLED=false", "EMAIL_NOTIFICATIONS_ENABLED=true", 1)
	if got := f.read(".env"); got != want {
		t.Fatalf("email activation changed unrelated bytes\nwant: %q\ngot: %q", want, got)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["email_notifications_activated"] != true {
		t.Fatalf("email activation not receipted: %#v", receipt)
	}
	log, _ := os.ReadFile(f.log)
	if strings.Contains(r.stdout+r.stderr+string(log), "synthetic-smtp-password") {
		t.Fatal("SMTP password leaked")
	}
}

func TestEmailActivationRejectsMixedFlagsAndPreexistingCandidateBeforeDocker(t *testing.T) {
	f := newFixture(t)
	flags := []bool{false, true, false, false, false, false, true}
	r := f.runResult("test-token\n", nil, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "separate configuration operation") {
		t.Fatalf("mixed activation accepted: %+v", r)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("docker called for mixed config operation: %v", f.dockerCalls())
	}
	f2 := newFixture(t)
	f2.write(".env.next", baseEnv+"SMTP_PASSWORD=attacker-value\n", 0o600)
	r = f2.runResult("test-token\n", nil, false, false, false, false, false, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "Resolve the existing staged configuration") {
		t.Fatalf("existing candidate accepted: %+v", r)
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("docker called for preexisting candidate: %v", f2.dockerCalls())
	}
}

func TestAndroidPushActivationIsSeparateWorkerScopedAndReceipted(t *testing.T) {
	f := newFixture(t)
	secret := "synthetic-service-token-0123456789-abcdefgh"
	payload, _ := json.Marshal(map[string]string{"projectId": "synthetic-project", "serviceToken": secret})
	flags := []bool{false, false, false, false, false, false, false, true}
	r := f.runResult("test-token\n"+string(payload)+"\n", map[string]string{"COMPOSE_PUSH_KEYS": "1"}, flags...)
	if r.code != 0 {
		t.Fatalf("push activation failed: %s", r.stderr)
	}
	activated := f.read(".env")
	for _, want := range []string{"ANDROID_PUSH_ENABLED=true", "RUSTORE_PUSH_PROJECT_ID=synthetic-project", "RUSTORE_PUSH_SERVICE_TOKEN='" + secret + "'"} {
		if !strings.Contains(activated, want) {
			t.Fatalf("activated config missing %q: %s", want, activated)
		}
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["android_push_activated"] != true {
		t.Fatalf("push receipt missing: %#v", receipt)
	}
	log, _ := os.ReadFile(f.log)
	if strings.Contains(string(log)+r.stdout+r.stderr, secret) {
		t.Fatal("push service token leaked")
	}
}

func TestAndroidPushActivationRejectsMalformedFrameAndCredentialRotation(t *testing.T) {
	f := newFixture(t)
	flags := []bool{false, false, false, false, false, false, false, true}
	r := f.runResult("test-token\n{}\n", nil, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "Android push CI candidate validation failed") {
		t.Fatalf("malformed push payload accepted: %+v", r)
	}
	if got := f.read(".env"); got != baseEnv {
		t.Fatalf("active env changed on invalid payload: %q", got)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called before push payload validation: %v", f.dockerCalls())
	}
	for _, payload := range []string{"", "{}", `{"projectId":"bad/project","serviceToken":"synthetic-service-token-0123456789-abcdefgh"}`} {
		framed := "test-token\n"
		if payload != "" {
			framed += payload + "\n"
		}
		fBad := newFixture(t)
		r = fBad.runResult(framed, nil, flags...)
		if r.code == 0 || fBad.read(".env") != baseEnv {
			t.Fatalf("unsafe push frame was accepted or changed active env (%q): %+v", payload, r)
		}
		if _, err := os.Stat(fBad.path("receipts/" + sourceSHA + ".json")); !os.IsNotExist(err) {
			t.Fatalf("receipt created for invalid push frame %q: %v", payload, err)
		}
	}
	fMixed := newFixture(t)
	r = fMixed.runResult("test-token\n", nil, false, false, false, false, false, false, true, true)
	if r.code == 0 || !strings.Contains(r.stderr, "Android push activation must be a separate configuration operation") {
		t.Fatalf("mixed email/push activation accepted: %+v", r)
	}
	if len(fMixed.dockerCalls()) != 0 {
		t.Fatalf("Docker called for mixed email/push operation: %v", fMixed.dockerCalls())
	}
	f2 := newFixture(t)
	f2.write(".env", baseEnv+"ANDROID_PUSH_ENABLED=true\nRUSTORE_PUSH_PROJECT_ID=old-project\nRUSTORE_PUSH_SERVICE_TOKEN='old-token-value-0123456789-abcdefgh'\n", 0o600)
	payload, _ := json.Marshal(map[string]string{"projectId": "new-project", "serviceToken": "synthetic-service-token-0123456789-abcdefgh"})
	r = f2.runResult("test-token\n"+string(payload)+"\n", nil, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "cannot rotate existing configuration") {
		t.Fatalf("push secret rotation accepted: %+v", r)
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("Docker called before detecting push rotation: %v", f2.dockerCalls())
	}
	f3 := newFixture(t)
	original := baseEnv + "ANDROID_PUSH_ENABLED=false\nRUSTORE_PUSH_PROJECT_ID=previous-project\nRUSTORE_PUSH_SERVICE_TOKEN='previous-service-token-0123456789-abcdefgh'\n"
	f3.write(".env", original, 0o600)
	payload, _ = json.Marshal(map[string]string{"projectId": "new-project", "serviceToken": "synthetic-service-token-0123456789-abcdefgh"})
	r = f3.runResult("test-token\n"+string(payload)+"\n", nil, flags...)
	if r.code == 0 || f3.read(".env") != original || len(f3.dockerCalls()) != 0 {
		t.Fatalf("push rotation did not fail before release mutation: %+v", r)
	}
	f4 := newFixture(t)
	r = f4.runResult("test-token\n"+string(payload)+"\n", map[string]string{"COMPOSE_PUSH_KEYS": "1", "FAIL_COMPOSE_PREFLIGHT": "1"}, flags...)
	if r.code == 0 || f4.read(".env") != baseEnv {
		t.Fatalf("preflight failure did not restore push activation env: %+v", r)
	}
	if _, err := os.Stat(f4.path(".env.next")); !os.IsNotExist(err) {
		t.Fatalf("push candidate remained after preflight rollback: %v", err)
	}
	if _, err := os.Stat(f4.path("receipts/" + sourceSHA + ".json")); !os.IsNotExist(err) {
		t.Fatalf("receipt created before successful push release: %v", err)
	}
}

func TestAndroidFCMActivationPreservesProductionBillingAndKeepsCredentialsWorkerOnly(t *testing.T) {
	f := newFixture(t)
	serviceToken := "synthetic-service-token-0123456789-abcdefgh"
	fcmSecret := "synthetic-fcm-private-key-v2"
	credentials, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "synthetic-fcm-project", "client_email": "worker@synthetic-fcm-project.iam.gserviceaccount.com", "private_key": fcmSecret, "token_uri": "https://oauth2.googleapis.com/token"})
	encodedCredentials := base64.StdEncoding.EncodeToString(credentials)
	billing := "BILLING_PROVIDER=yookassa\nYOOKASSA_MODE=production\nYOOKASSA_PRODUCTION_SHOP_ID=654321\nYOOKASSA_PRODUCTION_SECRET_KEY='production_synthetic_secret_123'\n"
	rustore := "ANDROID_PUSH_ENABLED=true\nRUSTORE_PUSH_PROJECT_ID=synthetic-project\nRUSTORE_PUSH_SERVICE_TOKEN='" + serviceToken + "'\n"
	original := baseEnv + rustore + billing
	f.write(".env", original, 0o600)
	payload, _ := json.Marshal(map[string]any{"projectId": "synthetic-project", "serviceToken": serviceToken, "fcm": map[string]string{"projectId": "synthetic-fcm-project", "serviceAccountBase64": encodedCredentials}})
	composePath := filepath.Join(f.root, "resolved-compose.json")
	flags := []bool{false, false, false, false, false, false, false, true}
	r := f.runResult("test-token\n"+string(payload)+"\n", map[string]string{"COMPOSE_PUSH_KEYS": "1", "COMPOSE_FCM_PUSH_KEYS": "1", "COMPOSE_YOOKASSA_PRODUCTION_KEYS": "1", "COMPOSE_JSON_CAPTURE": composePath}, flags...)
	if r.code != 0 {
		t.Fatalf("cloud push activation failed: %s", r.stderr)
	}
	active := f.read(".env")
	for _, line := range strings.Split(strings.TrimSuffix(billing, "\n"), "\n") {
		if !strings.Contains(active, line+"\n") {
			t.Errorf("billing bytes not preserved: %q", line)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(rustore, "\n"), "\n") {
		if !strings.Contains(active, line+"\n") {
			t.Errorf("RuStore bytes not preserved: %q", line)
		}
	}
	if !strings.Contains(active, "FCM_SERVICE_ACCOUNT_BASE64="+encodedCredentials) {
		t.Fatal("FCM credentials absent from activated env")
	}
	composeBytes, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(composeBytes, &compose); err != nil {
		t.Fatal(err)
	}
	if _, exists := compose.Services["api"].Environment["FCM_SERVICE_ACCOUNT_BASE64"]; exists {
		t.Fatal("FCM service credential was included in API environment")
	}
	if compose.Services["worker"].Environment["FCM_SERVICE_ACCOUNT_BASE64"] == "" {
		t.Fatal("FCM service credential missing from worker environment")
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["android_push_activated"] != true {
		t.Fatalf("cloud push receipt missing: %#v", receipt)
	}
	log, _ := os.ReadFile(f.log)
	if strings.Contains(r.stdout+r.stderr+string(log), fcmSecret) || strings.Contains(r.stdout+r.stderr+string(log), serviceToken) {
		t.Fatal("push secret leaked")
	}
	f2 := newFixture(t)
	old := baseEnv + rustore + "FCM_PUSH_ENABLED=true\nFCM_PROJECT_ID=synthetic-fcm-project\nFCM_SERVICE_ACCOUNT_BASE64=old-fcm-service-account\n"
	f2.write(".env", old, 0o600)
	rotated, _ := json.Marshal(map[string]any{"projectId": "synthetic-project", "serviceToken": serviceToken, "fcm": map[string]string{"projectId": "synthetic-fcm-project", "serviceAccountBase64": encodedCredentials}})
	r = f2.runResult("test-token\n"+string(rotated)+"\n", map[string]string{"COMPOSE_PUSH_KEYS": "1", "COMPOSE_FCM_PUSH_KEYS": "1"}, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "Android push activation cannot rotate existing configuration") || f2.read(".env") != old {
		t.Fatalf("existing FCM credentials rotated: %+v", r)
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("Docker called before rejecting FCM credential rotation: %v", f2.dockerCalls())
	}
}

func TestOwnerImportIsOptInAndValidatedBeforeDocker(t *testing.T) {
	f := newFixture(t)
	f.write(".owner-import.next.json", `{"accountId":"wrong","name":"Synthetic Owner","email":"khv1702@gmail.com","passwordHash":"bad","quota":1000,"validUntil":"2027-10-18T00:00:00Z"}`+"\n", 0o600)
	f.write("import-owner.sql.incoming", "-- synthetic\n__OWNER_PAYLOAD_ROW__\n", 0o600)
	r := f.runResult("test-token\n", nil, false, false, false)
	if r.code != 0 {
		t.Fatalf("staged owner payload should be ignored by default: %s", r.stderr)
	}
	if _, err := os.Stat(f.path("receipts/owner-import.json")); !os.IsNotExist(err) {
		t.Fatalf("owner import occurred without opt-in: %v", err)
	}
	f2 := newFixture(t)
	f2.write(".owner-import.next.json", `{"accountId":"wrong","name":"Synthetic Owner","email":"khv1702@gmail.com","passwordHash":"bad","quota":1000,"validUntil":"2027-10-18T00:00:00Z"}`+"\n", 0o600)
	f2.write("import-owner.sql.incoming", "-- synthetic\n__OWNER_PAYLOAD_ROW__\n", 0o600)
	r = f2.runResult("test-token\n", nil, false, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "Owner import payload validation failed") {
		t.Fatalf("invalid owner payload accepted: %+v", r)
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("Docker called before owner validation: %v", f2.dockerCalls())
	}
}

func TestOwnerImportSuccessKeepsPayloadPrivateAndWaitsForReadiness(t *testing.T) {
	f := newFixture(t)
	payload := `{"accountId":"51ec7d19-80c4-4fb6-8edb-79dc7abe3120","name":"Synthetic Production Owner","email":"khv1702@gmail.com","passwordHash":"$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","quota":1000,"validUntil":"2027-10-18T00:00:00Z"}`
	secret := "Synthetic Production Owner"
	f.write(".owner-import.next.json", payload+"\n", 0o600)
	f.write("import-owner.sql.incoming", "-- synthetic insert\n__OWNER_PAYLOAD_ROW__\n", 0o600)
	resultSHA := strings.Repeat("b", 40)
	r := f.runResult("test-token\n", map[string]string{"OWNER_IMPORT_RESULT": "created|" + resultSHA}, false, false, true)
	if r.code != 0 {
		t.Fatalf("owner import failed: %s", r.stderr)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/owner-import.json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["source_sha"] != resultSHA {
		t.Fatalf("owner import receipt source mismatch: %#v", receipt)
	}
	if _, err := os.Stat(f.path(".owner-import.next.json")); !os.IsNotExist(err) {
		t.Fatalf("payload not consumed after ready deployment: %v", err)
	}
	if _, err := os.Stat(f.path("import-owner.sql.incoming")); !os.IsNotExist(err) {
		t.Fatalf("SQL staging not consumed after ready deployment: %v", err)
	}
	loginInput, _ := os.ReadFile(f.stdin)
	log, _ := os.ReadFile(f.log)
	if strings.Contains(r.stdout+r.stderr+string(loginInput)+string(log), secret) {
		t.Fatal("owner payload leaked to public output or command log")
	}
	if !hasCall(f.dockerCalls(), "migration") {
		t.Fatal("owner import ran without migration")
	}
}

func TestOwnerPayloadMustBeMode600AndMigrationFailureRetainsPendingWithoutReceipt(t *testing.T) {
	f := newFixture(t)
	f.write(".owner-import.next.json", `{"bad":true}`+"\n", 0o640)
	f.write("import-owner.sql.incoming", "__OWNER_PAYLOAD_ROW__\n", 0o600)
	r := f.runResult("test-token\n", nil, false, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "mode-600 payload") {
		t.Fatalf("unsafe owner payload accepted: %+v", r)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called for unsafe owner payload: %v", f.dockerCalls())
	}
	f2 := newFixture(t)
	payload := `{"accountId":"51ec7d19-80c4-4fb6-8edb-79dc7abe3120","name":"Synthetic Production Owner","email":"khv1702@gmail.com","passwordHash":"$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","quota":1000,"validUntil":"2027-10-18T00:00:00Z"}`
	f2.write(".owner-import.next.json", payload+"\n", 0o600)
	f2.write("import-owner.sql.incoming", "__OWNER_PAYLOAD_ROW__\n", 0o600)
	r = f2.runResult("test-token\n", map[string]string{"FAIL_MIGRATION": "1"}, false, false, true)
	if r.code == 0 || !strings.Contains(f2.read("pending"), "phase=migration_started") {
		t.Fatalf("migration failure did not retain forward recovery: %+v", r)
	}
	if _, err := os.Stat(f2.path("receipts/owner-import.json")); !os.IsNotExist(err) {
		t.Fatalf("owner import receipt created despite migration failure: %v", err)
	}
}

func TestYooKassaTestStageIsPrivateAllowlistedAndReceipted(t *testing.T) {
	f := newFixture(t)
	secret := "test_synthetic_secret_123"
	payload := `{"shopId":"123456","secretKey":"` + secret + `"}`
	r := f.runResult("test-token\n"+payload+"\n", map[string]string{"COMPOSE_YOOKASSA_KEYS": "1"}, false, false, false, false, false, true)
	if r.code != 0 {
		t.Fatalf("YooKassa sandbox activation failed: %s", r.stderr)
	}
	active := f.read(".env")
	for _, want := range []string{"YOOKASSA_MODE=test", "YOOKASSA_TEST_SHOP_ID=123456", "YOOKASSA_TEST_SECRET_KEY='" + secret + "'"} {
		if !strings.Contains(active, want) {
			t.Fatalf("staged sandbox config missing %q: %s", want, active)
		}
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["yookassa_test_activated"] != true {
		t.Fatalf("sandbox flag absent from receipt: %#v", receipt)
	}
	log, _ := os.ReadFile(f.log)
	if strings.Contains(r.stdout+r.stderr+string(log), secret) {
		t.Fatal("YooKassa secret leaked")
	}
}

func TestYooKassaCandidateIsIgnoredWithoutOptIn(t *testing.T) {
	f := newFixture(t)
	candidate := baseEnv + "YOOKASSA_MODE=test\nYOOKASSA_TEST_SHOP_ID=123456\nYOOKASSA_TEST_SECRET_KEY='test_synthetic_secret_123'\n"
	f.write(".env.next", candidate, 0o600)
	r := f.runResult("test-token\n", nil)
	if r.code != 0 || f.read(".env") != baseEnv || f.read(".env.next") != candidate {
		t.Fatalf("inactive YooKassa candidate was activated or changed: %+v", r)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["yookassa_test_activated"] != false {
		t.Fatalf("inactive YooKassa stage marked active: %#v", receipt)
	}
}

func TestYooKassaPreMigrationFailureRollsBackPrivateCandidate(t *testing.T) {
	f := newFixture(t)
	candidate := `{"shopId":"123456","secretKey":"test_synthetic_secret_123"}`
	r := f.runResult("test-token\n"+candidate+"\n", map[string]string{
		"COMPOSE_YOOKASSA_KEYS":  "1",
		"FAIL_COMPOSE_PREFLIGHT": "1",
	}, false, false, false, false, false, true, false)
	if r.code != 42 {
		t.Fatalf("billing activation should stop at Compose preflight, got %d: %s", r.code, r.stderr)
	}
	if got := f.read(".env"); got != baseEnv {
		t.Fatalf("failed billing activation did not restore active env: %q", got)
	}
	if _, err := os.Stat(f.path(".env.next")); !os.IsNotExist(err) {
		t.Fatalf("failed billing candidate remains available after rollback: %v", err)
	}
	if _, err := os.Stat(f.path("pending")); !os.IsNotExist(err) {
		t.Fatalf("pre-migration rollback left a pending journal: %v", err)
	}
	if _, err := os.Stat(f.path("receipts/" + sourceSHA + ".json")); !os.IsNotExist(err) {
		t.Fatalf("failed billing activation wrote a receipt: %v", err)
	}
}

func TestYooKassaMalformedOrLiveKeyAndMixedActivationFailBeforeDocker(t *testing.T) {
	flags := []bool{false, false, false, false, false, true}
	for _, payload := range []string{
		`{"shopId":"123456","secretKey":"live_secret_value_123456789"}`,
		`{"shopId":"123456","secretKey":"test_short"}`,
		"not-json",
		`{"shopId":"123456","shopId":"654321","secretKey":"test_synthetic_secret_123"}`,
		`{"shopId":"123456","secretKey":"test_synthetic_secret_123","extra":"x"}`,
		`{"shopId":"123456","secretKey":"test_bad\nsecret_123"}`,
		`{"shopId":"` + strings.Repeat("1", 2050) + `","secretKey":"test_synthetic_secret_123"}`,
		strings.Repeat("x", 4096),
	} {
		f := newFixture(t)
		r := f.runResult("test-token\n"+payload+"\n", nil, flags...)
		if r.code == 0 || !strings.Contains(r.stderr, "YooKassa CI candidate validation failed") {
			t.Fatalf("invalid billing credential accepted: %+v", r)
		}
		if f.read(".env") != baseEnv {
			t.Fatalf("invalid billing candidate changed active env: %q", f.read(".env"))
		}
		if len(f.dockerCalls()) != 0 {
			t.Fatalf("Docker called before billing credential validation: %v", f.dockerCalls())
		}
		if _, err := os.Stat(f.path(".env.next")); !os.IsNotExist(err) {
			t.Fatalf("invalid billing candidate was retained: %v", err)
		}
	}
	f := newFixture(t)
	mixed := []bool{false, true, false, false, false, true}
	r := f.runResult("test-token\n{\"shopId\":\"123456\",\"secretKey\":\"test_synthetic_secret_123\"}\n", nil, mixed...)
	if r.code == 0 || !strings.Contains(r.stderr, "separate configuration operation") {
		t.Fatalf("combined billing activation accepted: %+v", r)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called for mixed billing activation: %v", f.dockerCalls())
	}
}

func TestYooKassaProductionActivationUsesSyntheticVerifiedShopAndStoppedWriterGate(t *testing.T) {
	flags := []bool{false, false, false, false, false, false, false, false, true}
	payload := `{"shopId":"654321","secretKey":"production_synthetic_secret_123"}`
	active := baseEnv + "BILLING_PROVIDER=yookassa\nBILLING_TEST_ACCOUNT_IDS=51ec7d19-80c4-4fb6-8edb-79dc7abe3120\nYOOKASSA_MODE=test\nYOOKASSA_TEST_SHOP_ID=123456\nYOOKASSA_TEST_SECRET_KEY=test_synthetic_secret_123\n"
	f := newFixture(t)
	f.write(".env", active, 0o600)
	r := f.runResult("test-token\n"+payload+"\n", map[string]string{"COMPOSE_YOOKASSA_PRODUCTION_KEYS": "1", "YOOKASSA_SWITCH_BLOCKERS": "1"}, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "YooKassa shop switch blocked") {
		t.Fatalf("unfinished writer gate accepted: %+v", r)
	}
	if got := f.read(".env"); got != active {
		t.Fatalf("blocked production switch did not restore prior env: %q", got)
	}
	if hasCall(f.dockerCalls(), "migration") {
		t.Fatalf("migration started before payment evidence gate: %v", f.dockerCalls())
	}
	if _, err := os.Stat(f.path("receipts/" + sourceSHA + ".json")); !os.IsNotExist(err) {
		t.Fatalf("receipt created for blocked switch: %v", err)
	}
	f2 := newFixture(t)
	f2.write(".env", active, 0o600)
	r = f2.runResult("test-token\n"+payload+"\n", map[string]string{"COMPOSE_YOOKASSA_PRODUCTION_KEYS": "1", "YOOKASSA_SWITCH_BLOCKERS": "0"}, flags...)
	if r.code != 0 {
		t.Fatalf("verified shop with no blockers failed: %s", r.stderr)
	}
	activated := f2.read(".env")
	for _, want := range []string{"YOOKASSA_MODE=production", "YOOKASSA_PRODUCTION_SHOP_ID=654321", "YOOKASSA_PRODUCTION_SECRET_KEY='production_synthetic_secret_123'", "YOOKASSA_TEST_SHOP_ID=123456", "YOOKASSA_TEST_SECRET_KEY=test_synthetic_secret_123"} {
		if !strings.Contains(activated, want) {
			t.Fatalf("production activation missing %q: %s", want, activated)
		}
	}
	calls := f2.dockerCalls()
	callIndex := func(required ...string) int {
		for i, args := range calls {
			joined := strings.Join(args, " ")
			matches := true
			for _, token := range required {
				if !strings.Contains(joined, token) {
					matches = false
					break
				}
			}
			if matches {
				return i
			}
		}
		return -1
	}
	stop := callIndex("stop", "api", "worker", "probe")
	gate := callIndex("billing.orders", "webhook_inbox")
	migration := callIndex("--profile", "migration")
	if stop < 0 || gate < 0 || migration < 0 || stop >= gate || gate >= migration {
		t.Fatalf("expected writers to stop, payment gate to pass, then migration to run; got calls: %#v", calls)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f2.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["yookassa_production_activated"] != true {
		t.Fatalf("production activation not receipted: %#v", receipt)
	}
	log, _ := os.ReadFile(f2.log)
	if strings.Contains(r.stdout+r.stderr+string(log), "production_synthetic_secret_123") {
		t.Fatal("production secret leaked")
	}
}

func TestYooKassaProductionVerificationRejectsWrongShopResponseWithoutDocker(t *testing.T) {
	flags := []bool{false, false, false, false, false, false, false, false, true}
	f := newFixture(t)
	r := f.runResult("test-token\n{\"shopId\":\"123456\",\"secretKey\":\"wrong_synthetic_secret_123456\"}\n", nil, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "YooKassa CI candidate validation failed") {
		t.Fatalf("wrong shop response accepted: %+v", r)
	}
	if len(f.dockerCalls()) != 0 {
		t.Fatalf("Docker called before provider verification: %v", f.dockerCalls())
	}
}

func TestOAuthActivationReceiptAndCandidateMutationRetainPendingJournal(t *testing.T) {
	f := newFixture(t)
	candidate := baseEnv + "YANDEX_CLIENT_ID=synthetic-yandex-client-1234\nYANDEX_CLIENT_SECRET=synthetic-yandex-secret-0123456789\nYANDEX_REDIRECT_URI=https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback\nYANDEX_SCOPES=login:email login:info\nVK_CLIENT_ID=12345678\nVK_CALLBACK_URL=https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback\n"
	f.write(".env.next", candidate, 0o600)
	r := f.runResult("test-token\n", map[string]string{"COMPOSE_OAUTH_KEYS": "1"}, false, false, false, false, true)
	if r.code != 0 {
		t.Fatalf("OAuth activation failed: %s", r.stderr)
	}
	if got := f.read(".env"); got != candidate {
		t.Fatalf("OAuth candidate bytes changed: %q", got)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["oauth_activated"] != true {
		t.Fatalf("OAuth activation not receipted: %#v", receipt)
	}
	f2 := newFixture(t)
	f2.write(".env.next", candidate, 0o600)
	r = f2.runResult("test-token\n", map[string]string{"COMPOSE_OAUTH_KEYS": "1", "MUTATE_NEXT_DURING_DEPLOY": "1"}, false, false, false, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "Staged activation env changed during deployment") {
		t.Fatalf("mutated candidate was accepted: %+v", r)
	}
	if !strings.Contains(f2.read("pending"), "phase=migration_succeeded") {
		t.Fatalf("post-migration journal not retained: %s", f2.read("pending"))
	}
	if _, err := os.Stat(f2.path("receipts/" + sourceSHA + ".json")); !os.IsNotExist(err) {
		t.Fatalf("receipt written for mutated candidate: %v", err)
	}
}

func TestOAuthCandidateIgnoreProviderContractAndCredentialRotation(t *testing.T) {
	valid := baseEnv + "YANDEX_CLIENT_ID=synthetic-yandex-client-1234\nYANDEX_CLIENT_SECRET=synthetic-yandex-secret-0123456789\nYANDEX_REDIRECT_URI=https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback\nYANDEX_SCOPES=login:email login:info\nVK_CLIENT_ID=12345678\nVK_CALLBACK_URL=https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback\n"
	f := newFixture(t)
	f.write(".env.next", valid, 0o600)
	r := f.runResult("test-token\n", nil)
	if r.code != 0 || f.read(".env") != baseEnv || f.read(".env.next") != valid {
		t.Fatalf("inactive OAuth candidate changed: %+v", r)
	}
	flags := []bool{false, false, false, false, true}
	cases := []struct{ contents, reason string }{
		{valid + "UNAPPROVED_OAUTH_SECRET=synthetic\n", "changes outside selected keys"},
		{strings.Replace(valid, "https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback", "https://attacker.example/callback", 1), "invalid Yandex callback URL"},
		{strings.Replace(valid, "YANDEX_SCOPES=login:email login:info", "YANDEX_SCOPES=login:email", 1), "invalid Yandex scopes"},
		{strings.Replace(valid, "synthetic-yandex-secret-0123456789", "short", 1), "invalid Yandex client secret"},
		{strings.Replace(valid, "https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback", "https://heartbeat-checker.ru/oauth/callback", 1), "invalid VK callback URL"},
	}
	for _, tc := range cases {
		f2 := newFixture(t)
		f2.write(".env.next", tc.contents, 0o600)
		r = f2.runResult("test-token\n", nil, flags...)
		if r.code == 0 || !strings.Contains(r.stderr, tc.reason) || len(f2.dockerCalls()) != 0 || f2.read(".env") != baseEnv {
			t.Fatalf("invalid OAuth candidate (%s) reached Docker or changed active config: %+v calls=%v", tc.reason, r, f2.dockerCalls())
		}
	}
	f3 := newFixture(t)
	old := strings.Replace(valid, "YANDEX_CLIENT_ID=synthetic-yandex-client-1234", `YANDEX_CLIENT_ID="synthetic-yandex-client-1234"`, 1)
	f3.write(".env", old, 0o600)
	f3.write(".env.next", strings.Replace(valid, "YANDEX_CLIENT_ID=synthetic-yandex-client-1234", "YANDEX_CLIENT_ID=changed-yandex-client-id", 1), 0o600)
	r = f3.runResult("test-token\n", nil, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "existing OAuth credential cannot be rotated") {
		t.Fatalf("OAuth credential rotation accepted: %+v", r)
	}
	if len(f3.dockerCalls()) != 0 || f3.read(".env") != old {
		t.Fatalf("OAuth raw credential rotation mutated state or reached Docker: %v", f3.dockerCalls())
	}
	f4 := newFixture(t)
	candidate := f4.path(".env.next")
	if err := os.WriteFile(candidate, []byte(valid), 0o640); err != nil {
		t.Fatal(err)
	}
	r = f4.runResult("test-token\n", nil, flags...)
	if r.code == 0 || !strings.Contains(r.stderr, "regular mode-600 .env.next") || len(f4.dockerCalls()) != 0 {
		t.Fatalf("non-600 OAuth candidate accepted: %+v", r)
	}
}

func TestPostMigrationFailureKeepsActivatedEnvAndForwardJournal(t *testing.T) {
	f := newFixture(t)
	f.write(".env.next", nextEnv, 0o600)
	r := f.runResult("test-token\n", map[string]string{"FAIL_MIGRATION": "1"}, true)
	if r.code == 0 {
		t.Fatal("injected migration failure returned success")
	}
	if got := f.read(".env"); got != nextEnv {
		t.Fatalf("activated config was rolled back after migration start: %q", got)
	}
	if !strings.Contains(f.read("pending"), "phase=migration_started") {
		t.Fatalf("forward recovery journal missing: %s", f.read("pending"))
	}
	if got := f.read(".env.next"); got != nextEnv {
		t.Fatalf("candidate removed before release completion: %q", got)
	}
}

func TestAdminMFAActivationIsExplicitScopedAndCannotRotateActiveKey(t *testing.T) {
	candidate := baseEnv + "AUTH_MFA_ENCRYPTION_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
	f := newFixture(t)
	f.write(".env.next", candidate, 0o600)
	r := f.runResult("test-token\n", nil, false, false, false, true)
	if r.code != 0 {
		t.Fatalf("MFA activation failed: %s", r.stderr)
	}
	if got := f.read(".env"); got != candidate {
		t.Fatalf("MFA activation changed unselected bytes: %q", got)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["admin_mfa_activated"] != true {
		t.Fatalf("MFA receipt flag missing: %#v", receipt)
	}
	if _, err := os.Stat(f.path(".env.next")); !os.IsNotExist(err) {
		t.Fatalf("MFA candidate not consumed after readiness: %v", err)
	}
	f2 := newFixture(t)
	active := baseEnv + "AUTH_MFA_ENCRYPTION_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
	f2.write(".env", active, 0o600)
	f2.write(".env.next", baseEnv+"AUTH_MFA_ENCRYPTION_KEY=BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=\n", 0o600)
	r = f2.runResult("test-token\n", nil, false, false, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "existing admin MFA key cannot be rotated") {
		t.Fatalf("active MFA key rotated: %+v", r)
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("Docker called for key rotation: %v", f2.dockerCalls())
	}
	f3 := newFixture(t)
	f3.write(".env.next", baseEnv+"AUTH_MFA_ENCRYPTION_KEY=not-base64\n", 0o600)
	r = f3.runResult("test-token\n", nil, false, false, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "invalid admin MFA key") {
		t.Fatalf("malformed MFA key accepted: %+v", r)
	}
	if len(f3.dockerCalls()) != 0 {
		t.Fatalf("Docker called before MFA validation: %v", f3.dockerCalls())
	}
}

func TestMessengerActivationPreservesQuotedRawValuesAndRejectsSupportSecretReuse(t *testing.T) {
	active := baseEnv + "MESSENGER_NOTIFICATIONS_ENABLED=false\nTELEGRAM_SUPPORT_WEBHOOK_SECRET=support_telegram_secret_0123456789\nMAX_SUPPORT_WEBHOOK_SECRET=support_max_secret_0123456789012\nTELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token\nMAX_BOT_INFORMER_TOKEN=old_max_token_1234567890\nTELEGRAM_INFORMER_BOT_USERNAME=heartbeat_oldbot\nMAX_INFORMER_BOT_URL=https://max.ru/heartbeat_oldbot\n"
	candidate := strings.Replace(active, "MESSENGER_NOTIFICATIONS_ENABLED=false", "MESSENGER_NOTIFICATIONS_ENABLED=true", 1)
	candidate = strings.Replace(candidate, "MAX_BOT_INFORMER_TOKEN=old_max_token_1234567890", "MAX_BOT_INFORMER_TOKEN=max_token_1234567890_valid", 1)
	candidate = strings.Replace(candidate, "TELEGRAM_INFORMER_BOT_USERNAME=heartbeat_oldbot", "TELEGRAM_INFORMER_BOT_USERNAME=heartbeat_notifier_bot", 1)
	candidate = strings.Replace(candidate, "MAX_INFORMER_BOT_URL=https://max.ru/heartbeat_oldbot", "MAX_INFORMER_BOT_URL=https://max.ru/heartbeat_notifier_bot", 1)
	candidate += "TELEGRAM_INFORMER_WEBHOOK_SECRET=informer_telegram_secret_0123456789\nMAX_INFORMER_WEBHOOK_SECRET=informer_max_secret_012345678901\n"
	f := newFixture(t)
	f.write(".env", active, 0o600)
	f.write(".env.next", candidate, 0o600)
	r := f.runResult("test-token\n", nil, false, true)
	if r.code != 0 {
		t.Fatalf("messenger activation failed: %s", r.stderr)
	}
	if got := f.read(".env"); got != candidate {
		t.Fatalf("messenger candidate bytes changed: %q", got)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["messenger_notifications_activated"] != true {
		t.Fatalf("messenger receipt missing: %#v", receipt)
	}
	f2 := newFixture(t)
	f2.write(".env", active, 0o600)
	bad := strings.Replace(candidate, "TELEGRAM_INFORMER_WEBHOOK_SECRET=informer_telegram_secret_0123456789", "TELEGRAM_INFORMER_WEBHOOK_SECRET=support_telegram_secret_0123456789", 1)
	f2.write(".env.next", bad, 0o600)
	r = f2.runResult("test-token\n", nil, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "Informer webhook secret matches Support") {
		t.Fatalf("reused support secret accepted: %+v", r)
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("Docker called for invalid messenger candidate: %v", f2.dockerCalls())
	}
	quotedActive := strings.Replace(active, "TELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token", `TELEGRAM_BOT_INFORMER_TOKEN="123456:existing_informer_token"`, 1)
	quotedCandidate := strings.Replace(candidate, "TELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token", `TELEGRAM_BOT_INFORMER_TOKEN="123456:existing_informer_token"`, 1)
	f3 := newFixture(t)
	f3.write(".env", quotedActive, 0o600)
	f3.write(".env.next", quotedCandidate, 0o600)
	r = f3.runResult("test-token\n", nil, false, true)
	if r.code != 0 {
		t.Fatalf("unchanged quoted Telegram token rejected: %s", r.stderr)
	}
	f4 := newFixture(t)
	f4.write(".env", quotedActive, 0o600)
	f4.write(".env.next", candidate, 0o600)
	r = f4.runResult("test-token\n", nil, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "existing Telegram Informer token must remain unchanged") {
		t.Fatalf("quote-normalized token change accepted: %+v", r)
	}
	quotedSupport := strings.Replace(active, "TELEGRAM_SUPPORT_WEBHOOK_SECRET=support_telegram_secret_0123456789", `TELEGRAM_SUPPORT_WEBHOOK_SECRET="informer_telegram_secret_0123456789"`, 1)
	quotedReuse := strings.Replace(candidate, "TELEGRAM_INFORMER_WEBHOOK_SECRET=informer_telegram_secret_0123456789", "TELEGRAM_INFORMER_WEBHOOK_SECRET=informer_telegram_secret_0123456789", 1)
	quotedReuse = strings.Replace(quotedReuse, "TELEGRAM_SUPPORT_WEBHOOK_SECRET=support_telegram_secret_0123456789", `TELEGRAM_SUPPORT_WEBHOOK_SECRET="informer_telegram_secret_0123456789"`, 1)
	f5 := newFixture(t)
	f5.write(".env", quotedSupport, 0o600)
	f5.write(".env.next", quotedReuse, 0o600)
	r = f5.runResult("test-token\n", nil, false, true)
	if r.code == 0 || !strings.Contains(r.stderr, "Informer webhook secret matches Support") {
		t.Fatalf("quoted support-secret reuse accepted: %+v", r)
	}
}

func TestCombinedMessengerAndOAuthActivationSetsBothReceiptFlags(t *testing.T) {
	active := baseEnv + "MESSENGER_NOTIFICATIONS_ENABLED=false\nTELEGRAM_SUPPORT_WEBHOOK_SECRET=support_telegram_secret_0123456789\nMAX_SUPPORT_WEBHOOK_SECRET=support_max_secret_0123456789012\nTELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token\nMAX_BOT_INFORMER_TOKEN=old_max_token_0123456789\nTELEGRAM_INFORMER_BOT_USERNAME=heartbeat_oldbot\nMAX_INFORMER_BOT_URL=https://max.ru/heartbeat_oldbot\n"
	candidate := strings.Replace(active, "MESSENGER_NOTIFICATIONS_ENABLED=false", "MESSENGER_NOTIFICATIONS_ENABLED=true", 1)
	candidate = strings.Replace(candidate, "MAX_BOT_INFORMER_TOKEN=old_max_token_0123456789", "MAX_BOT_INFORMER_TOKEN=max_token_0123456789_valid", 1)
	candidate = strings.Replace(candidate, "TELEGRAM_INFORMER_BOT_USERNAME=heartbeat_oldbot", "TELEGRAM_INFORMER_BOT_USERNAME=heartbeat_notifier_bot", 1)
	candidate = strings.Replace(candidate, "MAX_INFORMER_BOT_URL=https://max.ru/heartbeat_oldbot", "MAX_INFORMER_BOT_URL=https://max.ru/heartbeat_notifier_bot", 1)
	candidate += "TELEGRAM_INFORMER_WEBHOOK_SECRET=informer_telegram_secret_0123456789\nMAX_INFORMER_WEBHOOK_SECRET=informer_max_secret_012345678901\nYANDEX_CLIENT_ID=synthetic-yandex-client-1234\nYANDEX_CLIENT_SECRET=synthetic-yandex-secret-0123456789\nYANDEX_REDIRECT_URI=https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback\nYANDEX_SCOPES=login:email login:info\nVK_CLIENT_ID=12345678\nVK_CALLBACK_URL=https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback\n"
	f := newFixture(t)
	f.write(".env", active, 0o600)
	f.write(".env.next", candidate, 0o600)
	r := f.runResult("test-token\n", map[string]string{"COMPOSE_OAUTH_KEYS": "1"}, false, true, false, false, true)
	if r.code != 0 {
		t.Fatalf("combined activation failed: %s", r.stderr)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["messenger_notifications_activated"] != true || receipt["oauth_activated"] != true {
		t.Fatalf("combined receipt flags missing: %#v", receipt)
	}
}

func TestCombinedRelayAndMessengerActivationAndControlByteRejection(t *testing.T) {
	active := baseEnv + "MESSENGER_NOTIFICATIONS_ENABLED=false\nTELEGRAM_SUPPORT_WEBHOOK_SECRET=support_telegram_secret_0123456789\nMAX_SUPPORT_WEBHOOK_SECRET=support_max_secret_0123456789012\nTELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token\nMAX_BOT_INFORMER_TOKEN=old_max_token_0123456789\nTELEGRAM_INFORMER_BOT_USERNAME=heartbeat_oldbot\nMAX_INFORMER_BOT_URL=https://max.ru/heartbeat_oldbot\n"
	candidate := strings.Replace(active, "MESSENGER_NOTIFICATIONS_ENABLED=false", "MESSENGER_NOTIFICATIONS_ENABLED=true", 1)
	candidate = strings.Replace(candidate, "TELEGRAM_API_BASE_URL=https://api.telegram.org", "TELEGRAM_API_BASE_URL=https://messengers-proxy.heartbeat-checker.ru/telegram-api", 1)
	candidate = strings.Replace(candidate, "TELEGRAM_API_PROXY_IP=\n", "TELEGRAM_API_PROXY_IP=185.184.122.62\n", 1)
	candidate = strings.Replace(candidate, "MAX_BOT_INFORMER_TOKEN=old_max_token_0123456789", "MAX_BOT_INFORMER_TOKEN=max_token_0123456789_valid", 1)
	candidate = strings.Replace(candidate, "TELEGRAM_INFORMER_BOT_USERNAME=heartbeat_oldbot", "TELEGRAM_INFORMER_BOT_USERNAME=heartbeat_notifier_bot", 1)
	candidate = strings.Replace(candidate, "MAX_INFORMER_BOT_URL=https://max.ru/heartbeat_oldbot", "MAX_INFORMER_BOT_URL=https://max.ru/heartbeat_notifier_bot", 1)
	candidate += "TELEGRAM_INFORMER_WEBHOOK_SECRET=informer_telegram_secret_0123456789\nMAX_INFORMER_WEBHOOK_SECRET=informer_max_secret_012345678901\n"
	f := newFixture(t)
	f.write(".env", active, 0o600)
	f.write(".env.next", candidate, 0o600)
	r := f.runResult("test-token\n", nil, true, true)
	if r.code != 0 {
		t.Fatalf("combined relay/messenger activation failed: %s", r.stderr)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.read("receipts/"+sourceSHA+".json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["telegram_relay_activated"] != true || receipt["messenger_notifications_activated"] != true {
		t.Fatalf("combined activation flags missing: %#v", receipt)
	}
	f2 := newFixture(t)
	f2.write(".env", active, 0o600)
	f2.write(".env.next", candidate+"# invalid\x01byte\n", 0o600)
	r = f2.runResult("test-token\n", nil, true, true)
	if r.code == 0 || !strings.Contains(r.stderr, "invalid env content") {
		t.Fatalf("control byte accepted: %+v", r)
	}
	if len(f2.dockerCalls()) != 0 {
		t.Fatalf("Docker called for control byte candidate: %v", f2.dockerCalls())
	}
}

func TestProductionFirewallMismatchFailsBeforeMigration(t *testing.T) {
	f := newFixture(t)
	r := f.runResult("test-token\n", map[string]string{"FIREWALL_POLICY_OUTPUT": strings.Repeat("8", 64)})
	if r.code == 0 || !strings.Contains(r.stderr, "Firewall policy receipt mismatch") {
		t.Fatalf("mismatched firewall policy accepted: %+v", r)
	}
	calls := f.dockerCalls()
	if indexCall(calls, "run") < 0 || hasCall(calls, "migration") {
		t.Fatalf("migration ran despite firewall mismatch: %v", calls)
	}
	f2 := newFixture(t)
	r = f2.runResult("test-token\n", map[string]string{"FAIL_FIREWALL_INSTALL": "1"})
	if r.code == 0 {
		t.Fatalf("firewall installer failure was ignored: %+v", r)
	}
	if hasCall(f2.dockerCalls(), "migration") {
		t.Fatalf("migration ran after firewall install failure: %v", f2.dockerCalls())
	}
}

func TestProbeFirewallShellRulesAreOrderedAndIdempotent(t *testing.T) {
	f := newFixture(t)
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	source, err := os.ReadFile(filepath.Join(repo, "deploy/backend/probe-firewall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.Replace(source, []byte("[[ $EUID -eq 0 ]]"), []byte("[[ 0 -eq 0 ]]"), 1)
	script := filepath.Join(f.root, "probe-firewall.sh")
	if err := os.WriteFile(script, source, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(f.root, "iptables.json")
	run := func() error {
		cmd := exec.Command("/bin/bash", script)
		cmd.Env = append(os.Environ(), "PATH="+f.bin+":"+os.Getenv("PATH"), "IPTABLES_STATE="+statePath)
		return cmd.Run()
	}
	if err := run(); err != nil {
		t.Fatalf("first firewall run: %v", err)
	}
	first, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var rules map[string][][]string
	if err := json.Unmarshal(first, &rules); err != nil {
		t.Fatal(err)
	}
	forward, input := rules["HEARTBEAT_PROBE_V2"], rules["HEARTBEAT_PROBE_INPUT"]
	if len(forward) < 5 || !hasRuleTarget(forward[0], "RETURN") || !hasRuleTarget(forward[len(forward)-1], "REJECT") {
		t.Fatalf("forward rule order invalid: %#v", forward)
	}
	if len(input) < 2 || !hasRuleTarget(input[0], "RETURN") || !hasRuleTarget(input[len(input)-1], "REJECT") {
		t.Fatalf("host input rule order invalid: %#v", input)
	}
	if err := run(); err != nil {
		t.Fatalf("second firewall run: %v", err)
	}
	second, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("firewall rerun changed rules\nfirst=%s\nsecond=%s", first, second)
	}
}

func TestProbeFirewallShellRejectsPrivateTargetsAndUnapprovedProtocols(t *testing.T) {
	f := newFixture(t)
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	source, err := os.ReadFile(filepath.Join(repo, "deploy/backend/probe-firewall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.Replace(source, []byte("[[ $EUID -eq 0 ]]"), []byte("[[ 0 -eq 0 ]]"), 1)
	script := filepath.Join(f.root, "probe-firewall.sh")
	if err := os.WriteFile(script, source, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(f.root, "iptables.json")
	cmd := exec.Command("/bin/bash", script)
	cmd.Env = append(os.Environ(), "PATH="+f.bin+":"+os.Getenv("PATH"), "IPTABLES_STATE="+statePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("firewall script failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var rules map[string][][]string
	if err := json.Unmarshal(b, &rules); err != nil {
		t.Fatal(err)
	}
	forward := rules["HEARTBEAT_PROBE_V2"]
	cases := []struct {
		ip, proto         string
		port              int
		state, icmp, want string
	}{
		{"10.1.2.3", "tcp", 443, "NEW", "", "REJECT"},
		{"8.8.8.8", "tcp", 443, "NEW", "", "RETURN"},
		{"8.8.8.8", "tcp", 8080, "NEW", "", "RETURN"},
		{"8.8.8.8", "tcp", 443, "ESTABLISHED", "", "RETURN"},
		{"1.1.1.1", "udp", 53, "NEW", "", "RETURN"},
		{"8.8.8.8", "udp", 53, "NEW", "", "REJECT"},
		{"8.8.8.8", "icmp", 0, "NEW", "echo-request", "RETURN"},
		{"8.8.8.8", "icmp", 0, "NEW", "destination-unreachable", "REJECT"},
	}
	for _, tc := range cases {
		if got := firewallVerdict(forward, tc.ip, tc.proto, tc.port, tc.state, tc.icmp); got != tc.want {
			t.Errorf("%s %s/%d state=%s icmp=%s: got %s, want %s", tc.ip, tc.proto, tc.port, tc.state, tc.icmp, got, tc.want)
		}
	}
	input := rules["HEARTBEAT_PROBE_INPUT"]
	if got := firewallVerdict(input, "192.0.2.2", "tcp", 80, "ESTABLISHED", ""); got != "RETURN" {
		t.Errorf("established host input verdict=%s, want RETURN", got)
	}
}

func firewallVerdict(rules [][]string, destination, protocol string, port int, state, icmpType string) string {
	dst, err := netip.ParseAddr(destination)
	if err != nil {
		return "REJECT"
	}
	for _, rule := range rules {
		match := true
		for i := 0; i < len(rule); i++ {
			switch rule[i] {
			case "--ctstate":
				i++
				allowed := false
				for _, value := range strings.Split(rule[i], ",") {
					if value == state {
						allowed = true
					}
				}
				if !allowed {
					match = false
				}
			case "-d":
				i++
				prefix, err := netip.ParsePrefix(rule[i])
				if err != nil || !prefix.Contains(dst) {
					match = false
				}
			case "-p":
				i++
				if rule[i] != protocol {
					match = false
				}
			case "--icmp-type":
				i++
				if rule[i] != icmpType {
					match = false
				}
			case "--dports":
				i++
				found := false
				for _, value := range strings.Split(rule[i], ",") {
					if value == strconv.Itoa(port) {
						found = true
					}
				}
				if !found || protocol != "tcp" {
					match = false
				}
			case "--dport":
				i++
				parts := strings.SplitN(rule[i], ":", 2)
				if len(parts) == 2 {
					low, e1 := strconv.Atoi(parts[0])
					high, e2 := strconv.Atoi(parts[1])
					if e1 != nil || e2 != nil || port < low || port > high {
						match = false
					}
				} else if rule[i] != strconv.Itoa(port) {
					match = false
				}
			}
		}
		if match {
			for i := 0; i+1 < len(rule); i++ {
				if rule[i] == "-j" {
					return rule[i+1]
				}
			}
		}
	}
	return "REJECT"
}

func hasRuleTarget(rule []string, target string) bool {
	for i := 0; i+1 < len(rule); i++ {
		if rule[i] == "-j" && rule[i+1] == target {
			return true
		}
	}
	return false
}

func hasCall(calls [][]string, token string) bool { return indexCall(calls, token) >= 0 }
func indexCall(calls [][]string, token string) int {
	for i, args := range calls {
		if strings.Contains(strings.Join(args, " "), token) {
			return i
		}
	}
	return -1
}
