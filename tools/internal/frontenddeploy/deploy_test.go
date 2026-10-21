package frontenddeploy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const frontendSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const frontendImage = "ghcr.io/example/heartbeat-checker/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const frontendAPI = "https://api.heartbeat-checker.ru"

// TestCommandMock is the executable used by the Bash driver's PATH-injected
// command mocks. It stays in this test binary so CI never needs Python.
func TestCommandMock(t *testing.T) {
	if os.Getenv("HC_FRONTEND_MOCK") != "1" {
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
	if err := frontendMock(args[0], args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestFrontendDeployBashScenarios(t *testing.T) {
	cases := []struct {
		name                 string
		mode, api, imageMode string
		env                  map[string]string
		old, wantSuccess     bool
		wantErr              string
		check                func(*testing.T, *frontendFixture)
	}{
		{"successful_first_deployment", "app", frontendAPI, "app", nil, false, true, "", func(t *testing.T, f *frontendFixture) {
			contains(t, f.output(), "Frontend deployed:")
			contains(t, file(t, filepath.Join(f.site, ".env")), frontendImage)
			equals(t, file(t, filepath.Join(f.site, "compose.yaml")), "new compose\n")
			equals(t, file(t, filepath.Join(f.site, "Caddyfile")), "new caddy\n")
			if exists(filepath.Join(f.site, "previous")) {
				t.Fatal("unexpected previous snapshot")
			}
			calls := f.curlCalls()
			n := 0
			for _, c := range calls {
				if last(c) == frontendAPI+"/api/v1/me" {
					n++
					if !has(c, "X-Heartbeat-Client-IP: 127.0.0.1") {
						t.Fatal("missing client IP header")
					}
				}
			}
			if n != 1 {
				t.Fatalf("preflight count=%d", n)
			}
		}},
		{"landing_deploys_without_api_preflight", "landing", "", "landing", nil, false, true, "", func(t *testing.T, f *frontendFixture) {
			urls := f.urls()
			for _, p := range []string{"/", "/theme-init.js", "/api/v1/me", "/terms", "/privacy", "/cookies", "/contacts", "/register", "/login", "/forgot-password", "/reset-password", "/verify-email", "/app", "/app/settings"} {
				if !has(urls, "https://heartbeat-checker.ru"+p) {
					t.Fatalf("missing postflight %s", p)
				}
			}
			if has(urls, frontendAPI+"/api/v1/me") {
				t.Fatal("landing ran API preflight")
			}
			f.assertModeInspect(t)
		}},
		{"frontend_deploys_without_api_origin_or_preflight", "frontend", "", "frontend", nil, false, true, "", func(t *testing.T, f *frontendFixture) {
			urls := f.urls()
			for _, p := range []string{"/", "/theme-init.js", "/api/v1/me", "/api/v1/auth/login", "/terms", "/privacy", "/cookies", "/contacts", "/login", "/register", "/forgot-password", "/reset-password", "/verify-email", "/app", "/app/settings"} {
				if !has(urls, "https://heartbeat-checker.ru"+p) {
					t.Fatalf("missing postflight %s", p)
				}
			}
			if has(urls, frontendAPI+"/api/v1/me") {
				t.Fatal("frontend ran API preflight")
			}
			for _, call := range f.curlCalls() {
				if strings.Contains(last(call), "/api/v1/") {
					if !has(call, "-D") || !has(call, "-X") {
						t.Fatalf("API smoke request lacks method/headers: %v", call)
					}
				}
			}
			headers := f.curlHeaderCalls()
			if len(headers) != 2 {
				t.Fatalf("frontend API response header checks=%d", len(headers))
			}
			for _, h := range headers {
				value := strings.ToLower(h.Headers)
				if !strings.Contains(value, "cache-control: no-store") || strings.Contains(value, "set-cookie:") {
					t.Fatalf("unsafe API smoke headers %q", h.Headers)
				}
			}
			f.assertModeInspect(t)
		}},
		{"frontend_rejects_nonempty_api_origin_before_registry_or_compose_actions", "frontend", frontendAPI, "frontend", nil, true, false, "frontend must not have an API origin", func(t *testing.T, f *frontendFixture) {
			if len(f.dockerCalls()) != 0 || len(f.curlCalls()) != 0 {
				t.Fatal("side effects before invalid frontend-origin rejection")
			}
			equals(t, file(t, filepath.Join(f.site, ".env")), "WEB_IMAGE=old-image\n")
		}},
		{"bad_api_auth_contract_prevents_switch", "app", frontendAPI, "app", map[string]string{"PREFLIGHT_BODY": `{"error":"forbidden"}`}, true, false, "API preflight response is not the Go auth contract", func(t *testing.T, f *frontendFixture) {
			if anyPrefix(f.dockerCalls(), "compose") {
				t.Fatal("compose switched after bad API auth contract")
			}
			equals(t, file(t, filepath.Join(f.site, ".env")), "WEB_IMAGE=old-image\n")
			if !exists(filepath.Join(f.site, "compose.yaml.incoming")) {
				t.Fatal("staged compose consumed")
			}
		}},
		{"image_revision_mismatch_prevents_switch", "app", frontendAPI, "app", map[string]string{"IMAGE_REVISION": strings.Repeat("c", 40)}, true, false, "Image revision does not match source SHA", func(t *testing.T, f *frontendFixture) {
			calls := f.dockerCalls()
			if len(calls) != 3 || !strings.HasPrefix(calls[0][0], "login") || !strings.HasPrefix(calls[1][0], "pull") || !strings.Contains(strings.Join(calls[2], " "), "org.opencontainers.image.revision") {
				t.Fatalf("unexpected registry calls: %#v", calls)
			}
			equals(t, file(t, filepath.Join(f.site, ".env")), "WEB_IMAGE=old-image\n")
		}},
		{"bad_mode_is_rejected_before_registry_or_compose_actions", "preview", "", "app", nil, true, false, "Invalid web mode", func(t *testing.T, f *frontendFixture) {
			if len(f.dockerCalls()) != 0 {
				t.Fatal("registry called for invalid mode")
			}
			equals(t, file(t, filepath.Join(f.site, ".env")), "WEB_IMAGE=old-image\n")
		}},
		{"image_mode_mismatch_prevents_switch_landing", "landing", "", "app", nil, true, false, "Image web mode does not match requested deployment mode", noSwitch},
		{"image_mode_mismatch_prevents_switch_frontend", "frontend", "", "app", nil, true, false, "Image web mode does not match requested deployment mode", noSwitch},
		{"failed_postflight_restores_previous_files_and_recreates_app", "app", frontendAPI, "app", map[string]string{"FAIL_POSTFLIGHT": "1"}, true, false, "restoring previous deployment", rollbackPrevious},
		{"failed_landing_postflight_restores_previous_files_and_recreates", "landing", "", "landing", map[string]string{"FAIL_POSTFLIGHT": "1"}, true, false, "restoring previous deployment", rollbackPrevious},
		{"failed_frontend_postflight_restores_previous_files_and_recreates", "frontend", "", "frontend", map[string]string{"FAIL_FRONTEND_POSTFLIGHT": "1"}, true, false, "restoring previous deployment", rollbackPrevious},
		{"landing_legal_page_failure_restores_previous_files", "landing", "", "landing", map[string]string{"FAIL_LEGAL_PAGE": "privacy"}, true, false, "restoring previous deployment", func(t *testing.T, f *frontendFixture) {
			rollbackPrevious(t, f)
			if !has(f.urls(), "https://heartbeat-checker.ru/privacy") {
				t.Fatal("privacy page was never checked")
			}
		}},
		{"failed_first_deployment_stops_containers_without_volume_deletion", "app", frontendAPI, "app", map[string]string{"FAIL_POSTFLIGHT": "1"}, false, false, "restoring previous deployment", func(t *testing.T, f *frontendFixture) {
			downs := callsContaining(f.dockerCalls(), "down")
			if len(downs) != 1 || has(downs[0], "-v") || has(downs[0], "--volumes") {
				t.Fatalf("unsafe first-deploy cleanup: %v", downs)
			}
			for _, n := range []string{".env", "compose.yaml", "Caddyfile"} {
				if exists(filepath.Join(f.site, n)) {
					t.Fatalf("left incomplete %s", n)
				}
			}
		}},
		{"failed_first_deployment_down_error_retains_config", "app", frontendAPI, "app", map[string]string{"FAIL_POSTFLIGHT": "1", "FAIL_DOWN": "1"}, false, false, "CRITICAL: failed to stop the incomplete first deployment", func(t *testing.T, f *frontendFixture) {
			contains(t, file(t, filepath.Join(f.site, ".env")), frontendImage)
			equals(t, file(t, filepath.Join(f.site, "compose.yaml")), "new compose\n")
		}},
		{"rollback_restart_error_has_critical_log_and_nonzero_exit", "app", frontendAPI, "app", map[string]string{"FAIL_POSTFLIGHT": "1", "FAIL_UP_NUMBER": "2"}, true, false, "CRITICAL: previous frontend could not be restarted", func(t *testing.T, f *frontendFixture) {
			if len(callsContaining(f.dockerCalls(), "up")) != 2 {
				t.Fatal("expected attempted rollback restart")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFrontendFixture(t)
			if tc.old {
				f.oldDeployment()
			}
			result := f.run(tc.mode, tc.api, tc.imageMode, tc.env)
			if tc.wantSuccess && result.err != nil {
				t.Fatalf("deploy failed: %v\n%s", result.err, result.stderr)
			}
			if !tc.wantSuccess {
				if result.err == nil {
					t.Fatal("deployment unexpectedly succeeded")
				}
				if !strings.Contains(result.stderr, tc.wantErr) {
					t.Fatalf("stderr missing %q: %s", tc.wantErr, result.stderr)
				}
			}
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

type frontendResult struct {
	err            error
	stdout, stderr string
}
type frontendFixture struct {
	t               *testing.T
	root, bin, site string
	env             map[string]string
	outputText      string
}

func newFrontendFixture(t *testing.T) *frontendFixture {
	t.Helper()
	root := t.TempDir()
	f := &frontendFixture{t: t, root: root, bin: filepath.Join(root, "bin"), site: filepath.Join(root, "site"), env: map[string]string{}}
	must(os.MkdirAll(f.bin, 0700))
	must(os.Mkdir(f.site, 0700))
	must(os.WriteFile(filepath.Join(f.site, "compose.yaml.incoming"), []byte("new compose\n"), 0600))
	must(os.WriteFile(filepath.Join(f.site, "Caddyfile.incoming"), []byte("new caddy\n"), 0600))
	src, err := os.ReadFile(filepath.Join(repoRootFrontend(t), "deploy/frontend/deploy.sh"))
	must(err)
	must(os.WriteFile(filepath.Join(root, "deploy.sh"), []byte(strings.Replace(string(src), "cd /opt/heartbeat-checker/frontend", `cd "$DEPLOY_DIR"`, 1)), 0700))
	for _, name := range []string{"docker", "curl"} {
		p := filepath.Join(f.bin, name)
		wrapper := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestCommandMock -- %s \"$@\"\n", os.Args[0], name)
		must(os.WriteFile(p, []byte(wrapper), 0700))
	}
	must(os.WriteFile(filepath.Join(f.bin, "sleep"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	// The mock reexecutes this test binary for each shell command. Suppress only
	// the race runtime's one-second child-process exit delay; race checks remain on.
	f.env = map[string]string{"HC_FRONTEND_MOCK": "1", "GORACE": "atexit_sleep_ms=0", "PATH": f.bin + ":" + os.Getenv("PATH"), "DEPLOY_DIR": f.site, "DOCKER_LOG": filepath.Join(root, "docker.log"), "CURL_LOG": filepath.Join(root, "curl.log"), "CURL_HEADERS_LOG": filepath.Join(root, "curl-headers.log"), "DOCKER_STATE": filepath.Join(root, "docker.state"), "IMAGE_REVISION": frontendSHA, "IMAGE_MODE": "app", "DEPLOY_MODE": "app"}
	return f
}
func (f *frontendFixture) oldDeployment() {
	for n, v := range map[string]string{".env": "WEB_IMAGE=old-image\n", "compose.yaml": "old compose\n", "Caddyfile": "old caddy\n"} {
		must(os.WriteFile(filepath.Join(f.site, n), []byte(v), 0600))
	}
}
func (f *frontendFixture) run(mode, api, imageMode string, extra map[string]string) frontendResult {
	env := []string{}
	over := map[string]string{}
	for k, v := range f.env {
		over[k] = v
	}
	over["DEPLOY_MODE"] = mode
	over["IMAGE_MODE"] = imageMode
	cmd := exec.Command("bash", filepath.Join(f.root, "deploy.sh"), frontendSHA, frontendImage, api, "ci-bot", mode)
	cmd.Dir = f.root
	for k, v := range extra {
		over[k] = v
	}
	for k, v := range over {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	f.outputText = string(out)
	return frontendResult{err: err, stdout: string(out), stderr: string(out)}
}
func (f *frontendFixture) dockerCalls() [][]string { return readCalls(f.env["DOCKER_LOG"]) }
func (f *frontendFixture) curlCalls() [][]string   { return readCalls(f.env["CURL_LOG"]) }

type curlHeaderRecord struct {
	URL     string `json:"url"`
	Method  string `json:"method"`
	Headers string `json:"headers"`
}

func (f *frontendFixture) curlHeaderCalls() []curlHeaderRecord {
	b, err := os.ReadFile(f.env["CURL_HEADERS_LOG"])
	if err != nil {
		return nil
	}
	out := []curlHeaderRecord{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var item curlHeaderRecord
		if json.Unmarshal([]byte(line), &item) == nil {
			out = append(out, item)
		}
	}
	return out
}
func (f *frontendFixture) output() string { return f.outputText }
func (f *frontendFixture) urls() []string {
	r := []string{}
	for _, c := range f.curlCalls() {
		r = append(r, last(c))
	}
	return r
}
func (f *frontendFixture) assertModeInspect(t *testing.T) {
	n := 0
	for _, c := range f.dockerCalls() {
		if len(c) > 3 && c[0] == "image" && c[1] == "inspect" && strings.Contains(c[3], "org.heartbeat-checker.web-mode") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("mode inspect count=%d", n)
	}
}

func frontendMock(command string, args []string) error {
	env := os.Getenv
	log := func(key string, v any) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(env(key), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = f.Write(append(b, '\n'))
		return e
	}
	switch command {
	case "sleep":
		return nil
	case "docker":
		if err := log("DOCKER_LOG", args); err != nil {
			return err
		}
		if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
			format := ""
			for i, a := range args {
				if a == "--format" && i+1 < len(args) {
					format = args[i+1]
				}
			}
			if strings.Contains(format, "org.opencontainers.image.revision") {
				fmt.Println(env("IMAGE_REVISION"))
			} else if strings.Contains(format, "org.heartbeat-checker.web-mode") {
				fmt.Println(env("IMAGE_MODE"))
			} else {
				return fmt.Errorf("unknown inspect format")
			}
			return nil
		}
		if len(args) > 0 && args[0] == "compose" && has(args, "up") {
			state := env("DOCKER_STATE")
			b, _ := os.ReadFile(state)
			n := 0
			fmt.Sscan(string(b), &n)
			n++
			_ = os.WriteFile(state, []byte(fmt.Sprint(n)), 0600)
			if env("FAIL_UP_NUMBER") == fmt.Sprint(n) {
				return fmt.Errorf("forced compose up failure")
			}
		}
		if len(args) > 0 && args[0] == "compose" && has(args, "down") && env("FAIL_DOWN") == "1" {
			return fmt.Errorf("forced compose down failure")
		}
		return nil
	case "curl":
		if err := log("CURL_LOG", args); err != nil {
			return err
		}
		option := func(flag string) string {
			for i, a := range args {
				if a == flag && i+1 < len(args) {
					return args[i+1]
				}
			}
			return ""
		}
		url := last(args)
		postflight := has(args, "--resolve")
		method := option("-X")
		if method == "" {
			method = "GET"
		}
		path := ""
		if postflight {
			path = strings.TrimPrefix(url, "https://heartbeat-checker.ru")
		}
		status, body := "401", env("PREFLIGHT_BODY")
		if body == "" {
			body = `{"error":"unauthorized"}`
		}
		headers := ""
		if postflight {
			if env("FAIL_POSTFLIGHT") == "1" || path == "/api/v1/me" && env("DEPLOY_MODE") == "frontend" && env("FAIL_FRONTEND_POSTFLIGHT") == "1" {
				return fmt.Errorf("forced curl failure")
			}
			switch {
			case path == "/":
				status = "200"
				if env("DEPLOY_MODE") == "frontend" {
					body = `<html data-web-mode="frontend"><a href="/login">Войти</a><a href="/register">Регистрация</a></html>`
				} else {
					body = `<html data-web-mode="landing"><footer><a href="/terms">Условия</a><a href="/privacy">Конфиденциальность</a><a href="/cookies">Cookies</a><a href="/contacts">Контакты</a></footer></html>`
				}
			case path == "/theme-init.js":
				status = "200"
				body = ""
			case path == "/api/v1/me":
				status = "401"
				if env("DEPLOY_MODE") == "frontend" {
					status = "503"
					body = `{"error":"temporarily_unavailable"}`
					headers = "HTTP/2 503\r\ncache-control: no-store\r\n"
				} else if env("DEPLOY_MODE") == "landing" {
					status = "404"
					body = `{"error":"not_found"}`
				} else {
					body = `{"error":"unauthorized"}`
				}
			case path == "/api/v1/auth/login":
				status = "404"
				body = `{"error":"not_found"}`
				if env("DEPLOY_MODE") == "frontend" {
					status = "503"
					body = `{"error":"temporarily_unavailable"}`
					headers = "HTTP/2 503\r\ncache-control: no-store\r\n"
				}
			case path == "/terms" || path == "/privacy" || path == "/cookies" || path == "/contacts":
				status = "200"
				body = "Legal page: " + path
				if env("FAIL_LEGAL_PAGE") == strings.TrimPrefix(path, "/") {
					status = "500"
					body = ""
				}
			case strings.HasPrefix(path, "/"):
				if env("DEPLOY_MODE") == "frontend" {
					status = "200"
					body = "Frontend page: " + path
				} else {
					status = "307"
					body = ""
					headers = "HTTP/2 307\r\nlocation: /\r\n"
				}
			default:
				status = "404"
				body = ""
			}
		}
		out := option("-o")
		if out != "" && out != "/dev/null" {
			if err := os.WriteFile(out, []byte(body), 0600); err != nil {
				return err
			}
		}
		hdr := option("-D")
		if hdr != "" {
			if err := os.WriteFile(hdr, []byte(headers), 0600); err != nil {
				return err
			}
			if strings.Contains(path, "/api/v1/") {
				_ = log("CURL_HEADERS_LOG", map[string]any{"url": url, "method": method, "headers": headers})
			}
		}
		if option("-w") != "" {
			fmt.Print(status)
		}
		return nil
	default:
		return fmt.Errorf("unexpected mock command %s", command)
	}
}

func repoRootFrontend(t *testing.T) string {
	_, f, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test path unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(f), "../../../"))
}
func readCalls(path string) [][]string {
	f, e := os.Open(path)
	if e != nil {
		return nil
	}
	defer f.Close()
	out := [][]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		var c []string
		_ = json.Unmarshal(s.Bytes(), &c)
		out = append(out, c)
	}
	return out
}
func noSwitch(t *testing.T, f *frontendFixture) {
	if anyPrefix(f.dockerCalls(), "compose") || len(f.curlCalls()) != 0 {
		t.Fatal("switch or postflight ran after image mode mismatch")
	}
	equals(t, file(t, filepath.Join(f.site, ".env")), "WEB_IMAGE=old-image\n")
}
func rollbackPrevious(t *testing.T, f *frontendFixture) {
	for n, v := range map[string]string{".env": "WEB_IMAGE=old-image\n", "compose.yaml": "old compose\n", "Caddyfile": "old caddy\n"} {
		equals(t, file(t, filepath.Join(f.site, n)), v)
	}
	ups := callsContaining(f.dockerCalls(), "up")
	if len(ups) != 2 || !has(ups[0], "--force-recreate") || !has(ups[1], "--force-recreate") {
		t.Fatalf("rollback did not force recreate both versions: %v", ups)
	}
}
func callsContaining(calls [][]string, s string) [][]string {
	r := [][]string{}
	for _, c := range calls {
		if has(c, s) {
			r = append(r, c)
		}
	}
	return r
}
func anyPrefix(calls [][]string, s string) bool {
	for _, c := range calls {
		if len(c) > 0 && c[0] == s {
			return true
		}
	}
	return false
}
func has(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func last(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}
func file(t *testing.T, p string) string {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func exists(p string) bool { _, e := os.Stat(p); return e == nil }
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func contains(t *testing.T, s, part string) {
	t.Helper()
	if !strings.Contains(s, part) {
		t.Fatalf("%q does not contain %q", s, part)
	}
}
func equals(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}
