package firewall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These tests exercise the Linux iptables kernel through the immutable installer
// image. Set FIREWALL_TEST_IMAGE to the image built by CI or the local fixture.
func TestMissingDockerForwardHookFailsWithoutReplacingBootstrap(t *testing.T) {
	h := newFirewallHarness(t)
	h.seedLegacyHooks(false, true)
	original := h.contents()
	_, result := h.inContainerResult(nil, "/bin/bash", "/opt/firewall/install.sh")
	if result == nil {
		t.Fatal("installer succeeded without Docker's FORWARD hook")
	}
	if got := h.contents(); got != original {
		t.Fatalf("failed install replaced bootstrap: %q", got)
	}
	if strings.Contains(h.inContainer(true, nil, "iptables", "-S", "DOCKER-USER"), "HEARTBEAT_PROBE_V2") {
		t.Fatal("failed install left a replacement chain")
	}
}

func TestUpgradeReplacesLegacyHookIdempotentlyAndDenialPreservesOldFile(t *testing.T) {
	h := newFirewallHarness(t)
	h.seedLegacyHooks(true, true)
	original := h.contents()
	wrapper := "#!/bin/bash\nif [[ $1 == -I && $2 == HEARTBEAT_PROBE_V2 ]]; then echo injected-iptables-failure >&2; exit 77; fi\nexec /usr/sbin/iptables \"$@\"\n"
	h.inContainer(true, nil, "/bin/bash", "-ec", "printf %s "+shellQuote(wrapper)+" > /host-sbin/iptables; chmod 0755 /host-sbin/iptables")
	failedOut, failed := h.inContainerResult(map[string]string{"PATH": "/host-sbin:/usr/sbin:/usr/bin:/sbin:/bin"}, "/bin/bash", "/opt/firewall/install.sh")
	if failed == nil || !strings.Contains(failedOut, "injected-iptables-failure") {
		t.Fatalf("expected injected policy failure, got %v", failed)
	}
	if got := h.contents(); got != original {
		t.Fatalf("failed upgrade changed bootstrap: %q", got)
	}
	rules := strings.Split(h.inContainer(true, nil, "iptables", "-S", "DOCKER-USER"), "\n")
	if !hasSuffixLine(rules, "-j HEARTBEAT_PROBE") || hasSuffixLine(rules, "-j HEARTBEAT_PROBE_V2") {
		t.Fatalf("failed upgrade damaged legacy hooks: %v", rules)
	}
	h.inContainer(true, nil, "/bin/rm", "-f", "/host-sbin/iptables")
	installed := h.inContainer(true, nil, "/bin/bash", "/opt/firewall/install.sh")
	policySHA := strings.TrimSpace(installed)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(policySHA) {
		t.Fatalf("invalid policy receipt: %q", installed)
	}
	root := repoRoot(t)
	policy, err := os.ReadFile(filepath.Join(root, "deploy/backend/probe-firewall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(policy)
	if policySHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("policy digest mismatch: got %s", policySHA)
	}
	rules = strings.Split(h.inContainer(true, nil, "iptables", "-S", "DOCKER-USER"), "\n")
	if countContaining(rules, "-s 172.30.80.2/32 -j HEARTBEAT_PROBE_V2") != 1 || hasSuffixLine(rules, "-j HEARTBEAT_PROBE") {
		t.Fatalf("upgrade did not replace exactly one legacy hook: %v", rules)
	}
	forward := strings.Split(h.inContainer(true, nil, "iptables", "-S", "HEARTBEAT_PROBE_V2"), "\n")
	terminal := strings.Fields(forward[len(forward)-2])
	if len(terminal) < 4 || terminal[0] != "-A" || terminal[1] != "HEARTBEAT_PROBE_V2" {
		t.Fatalf("missing terminal firewall rule: %v", forward)
	}
	jump := indexOf(terminal, "-j")
	if jump < 0 || strings.Join(terminal[:jump], " ") != "-A HEARTBEAT_PROBE_V2" || terminal[jump+1] != "REJECT" {
		t.Fatalf("terminal reject is not unconditional: %v", terminal)
	}
	if len(terminal[jump+2:]) > 0 && strings.Join(terminal[jump+2:], " ") != "--reject-with icmp-port-unreachable" {
		t.Fatalf("unexpected reject mode: %v", terminal)
	}
	snapshot := append([]string(nil), rules...)
	repeated := strings.TrimSpace(h.inContainer(true, nil, "/bin/bash", "/opt/firewall/install.sh"))
	if repeated != policySHA || !equalStrings(strings.Split(h.inContainer(true, nil, "iptables", "-S", "DOCKER-USER"), "\n"), snapshot) {
		t.Fatal("repeated installation was not idempotent")
	}
	installedContents := h.contents()
	_, denied := h.dockerResult("run", "--rm", "--network", "container:"+h.name, "--cap-drop", "ALL", "--read-only", "--tmpfs", "/run:rw,noexec,nosuid,size=1m", "--mount", "type=volume,source="+h.volume+",target=/host-sbin", "--entrypoint", "/bin/bash", h.image, "/opt/firewall/install.sh")
	if denied == nil || h.contents() != installedContents || !equalStrings(strings.Split(h.inContainer(true, nil, "iptables", "-S", "DOCKER-USER"), "\n"), snapshot) {
		t.Fatal("install without NET_ADMIN changed the active policy or persisted bootstrap")
	}
}

type firewallHarness struct {
	t                   *testing.T
	image, name, volume string
}

func newFirewallHarness(t *testing.T) *firewallHarness {
	t.Helper()
	image := os.Getenv("FIREWALL_TEST_IMAGE")
	if image == "" {
		t.Skip("set FIREWALL_TEST_IMAGE for Docker NET_ADMIN firewall integration")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI unavailable")
	}
	h := &firewallHarness{t: t, image: image, name: fmt.Sprintf("heartbeat-firewall-test-%d", time.Now().UnixNano()), volume: fmt.Sprintf("heartbeat-firewall-test-%d-sbin", time.Now().UnixNano())}
	h.docker(true, "volume", "create", h.volume)
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", h.name).Run()
		_ = exec.Command("docker", "volume", "rm", "-f", h.volume).Run()
	})
	h.docker(true, "run", "-d", "--name", h.name, "--network", "none", "--cap-drop", "ALL", "--cap-add", "NET_ADMIN", "--read-only", "--tmpfs", "/run:rw,noexec,nosuid,size=1m", "--mount", "type=volume,source="+h.volume+",target=/host-sbin", "--entrypoint", "/bin/sleep", image, "300")
	h.inContainer(true, nil, "/bin/bash", "-ec", "printf '#!/bin/sh\\nexit 0\\n' > /host-sbin/heartbeat-probe-firewall; chmod 0755 /host-sbin/heartbeat-probe-firewall")
	return h
}

func (h *firewallHarness) docker(check bool, args ...string) string {
	out, err := h.command("docker", args...)
	if err != nil && check {
		h.t.Fatalf("docker %v: %v: %s", args, err, out)
	}
	return out
}
func (h *firewallHarness) dockerResult(args ...string) (string, error) {
	return h.command("docker", args...)
}
func (h *firewallHarness) inContainer(check bool, env map[string]string, args ...string) string {
	out, err := h.inContainerResult(env, args...)
	if err != nil && check {
		h.t.Fatalf("docker exec %v: %v: %s", args, err, out)
	}
	return out
}
func (h *firewallHarness) inContainerResult(env map[string]string, args ...string) (string, error) {
	a := []string{"exec"}
	for k, v := range env {
		a = append(a, "-e", k+"="+v)
	}
	a = append(a, h.name)
	a = append(a, args...)
	out, err := h.command("docker", a...)
	return out, err
}
func (h *firewallHarness) contents() string {
	return h.inContainer(true, nil, "/bin/cat", "/host-sbin/heartbeat-probe-firewall")
}
func (h *firewallHarness) seedLegacyHooks(forward, probe bool) {
	script := []string{"iptables -N DOCKER-USER 2>/dev/null || true", "iptables -N HEARTBEAT_PROBE 2>/dev/null || true", "iptables -N HEARTBEAT_PROBE_INPUT 2>/dev/null || true", "iptables -C INPUT -s 172.30.80.2/32 -j HEARTBEAT_PROBE_INPUT 2>/dev/null || iptables -I INPUT 1 -s 172.30.80.2/32 -j HEARTBEAT_PROBE_INPUT"}
	if forward {
		script = append(script, "iptables -C FORWARD -j DOCKER-USER 2>/dev/null || iptables -I FORWARD 1 -j DOCKER-USER")
	}
	if probe {
		script = append(script, "iptables -C DOCKER-USER -s 172.30.80.2/32 -j HEARTBEAT_PROBE 2>/dev/null || iptables -I DOCKER-USER 1 -s 172.30.80.2/32 -j HEARTBEAT_PROBE")
	}
	h.inContainer(true, nil, "/bin/bash", "-ec", strings.Join(script, "; "))
}
func (h *firewallHarness) command(name string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
func repoRoot(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../"))
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func hasSuffixLine(lines []string, suffix string) bool {
	for _, line := range lines {
		if strings.HasSuffix(line, suffix) {
			return true
		}
	}
	return false
}
func countContaining(lines []string, s string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, s) {
			n++
		}
	}
	return n
}
func indexOf(values []string, want string) int {
	for i, v := range values {
		if v == want {
			return i
		}
	}
	return -1
}
func equalStrings(a, b []string) bool {
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
