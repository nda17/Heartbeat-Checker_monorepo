#!/usr/bin/env python3
"""Linux-kernel lifecycle tests for the immutable firewall installer image."""

import os
import hashlib
from pathlib import Path
import shlex
import subprocess
import unittest
import uuid


IMAGE = os.environ.get("FIREWALL_TEST_IMAGE")


@unittest.skipUnless(IMAGE, "set FIREWALL_TEST_IMAGE to run Docker-backed firewall tests")
class FirewallImageTests(unittest.TestCase):
    def setUp(self):
        self.name = "heartbeat-firewall-test-" + uuid.uuid4().hex[:12]
        self.volume = self.name + "-sbin"
        self.container = None
        self.run_docker("volume", "create", self.volume)
        self.container = self.run_docker(
            "run", "-d", "--name", self.name, "--network", "none",
            "--cap-drop", "ALL", "--cap-add", "NET_ADMIN", "--read-only",
            "--tmpfs", "/run:rw,noexec,nosuid,size=1m",
            "--mount", f"type=volume,source={self.volume},target=/host-sbin",
            "--entrypoint", "/bin/sleep", IMAGE, "300",
        ).stdout.strip()
        self.run_in_container(
            "/bin/bash", "-ec",
            "printf '#!/bin/sh\\nexit 0\\n' > /host-sbin/heartbeat-probe-firewall; "
            "chmod 0755 /host-sbin/heartbeat-probe-firewall",
        )

    def tearDown(self):
        if self.container:
            subprocess.run(["docker", "rm", "-f", self.name], capture_output=True, check=False)
        subprocess.run(["docker", "volume", "rm", "-f", self.volume], capture_output=True, check=False)

    def run_docker(self, *args, check=True, **kwargs):
        result = subprocess.run(["docker", *args], text=True, capture_output=True, check=False, **kwargs)
        if check and result.returncode:
            self.fail(f"docker {' '.join(args)} failed ({result.returncode}): {result.stderr}")
        return result

    def run_in_container(self, *args, check=True, env=None):
        command = ["docker", "exec"]
        for key, value in (env or {}).items():
            command.extend(["-e", f"{key}={value}"])
        command.extend([self.name, *args])
        result = subprocess.run(command, text=True, capture_output=True, check=False)
        if check and result.returncode:
            self.fail(f"container command {' '.join(args)} failed ({result.returncode}): {result.stderr}")
        return result

    def seed_legacy_hooks(self, *, forward_hook=True, probe_hook=True):
        script = [
            "iptables -N DOCKER-USER 2>/dev/null || true",
            "iptables -N HEARTBEAT_PROBE 2>/dev/null || true",
            "iptables -N HEARTBEAT_PROBE_INPUT 2>/dev/null || true",
            "iptables -C INPUT -s 172.30.80.2/32 -j HEARTBEAT_PROBE_INPUT 2>/dev/null || "
            "iptables -I INPUT 1 -s 172.30.80.2/32 -j HEARTBEAT_PROBE_INPUT",
        ]
        if forward_hook:
            script.append("iptables -C FORWARD -j DOCKER-USER 2>/dev/null || iptables -I FORWARD 1 -j DOCKER-USER")
        if probe_hook:
            script.append("iptables -C DOCKER-USER -s 172.30.80.2/32 -j HEARTBEAT_PROBE 2>/dev/null || "
                          "iptables -I DOCKER-USER 1 -s 172.30.80.2/32 -j HEARTBEAT_PROBE")
        self.run_in_container("/bin/bash", "-ec", "; ".join(script))

    def install(self, *, check=True, env=None):
        return self.run_in_container("/bin/bash", "/opt/firewall/install.sh", check=check, env=env)

    def contents(self):
        return self.run_in_container("/bin/cat", "/host-sbin/heartbeat-probe-firewall").stdout

    def test_missing_docker_forward_hook_fails_without_replacing_bootstrap(self):
        self.seed_legacy_hooks(forward_hook=False)
        original = self.contents()
        result = self.install(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.contents(), original)
        self.assertNotIn("HEARTBEAT_PROBE_V2", self.run_in_container("iptables", "-S", "DOCKER-USER").stdout)

    def test_upgrade_replaces_legacy_hook_idempotently_and_denial_preserves_old_file(self):
        self.seed_legacy_hooks()
        original = self.contents()

        # Inject a single failure while constructing the replacement policy. The old
        # persisted bootstrap and legacy forwarding hook must remain available.
        wrapper = (
            "#!/bin/bash\n"
            "if [[ $1 == -I && $2 == HEARTBEAT_PROBE_V2 ]]; then "
            "echo injected-iptables-failure >&2; exit 77; fi\n"
            "exec /usr/sbin/iptables \"$@\"\n"
        )
        self.run_in_container(
            "/bin/bash", "-ec",
            "printf %s " + shlex.quote(wrapper) + " > /host-sbin/iptables; chmod 0755 /host-sbin/iptables",
        )
        failed = self.install(check=False, env={"PATH": "/host-sbin:/usr/sbin:/usr/bin:/sbin:/bin"})
        self.assertNotEqual(failed.returncode, 0)
        self.assertIn("injected-iptables-failure", failed.stderr)
        self.assertEqual(self.contents(), original)
        rules_after_failure = self.run_in_container("iptables", "-S", "DOCKER-USER").stdout.splitlines()
        self.assertTrue(any(line.endswith("-j HEARTBEAT_PROBE") for line in rules_after_failure))
        self.assertFalse(any(line.endswith("-j HEARTBEAT_PROBE_V2") for line in rules_after_failure))
        self.run_in_container("/bin/rm", "-f", "/host-sbin/iptables")

        installed = self.install()
        policy_sha = installed.stdout.strip()
        self.assertRegex(policy_sha, r"^[0-9a-f]{64}$")
        policy = (Path(__file__).parent.parent / "probe-firewall.sh").read_bytes()
        self.assertEqual(policy_sha, hashlib.sha256(policy).hexdigest())
        rules = self.run_in_container("iptables", "-S", "DOCKER-USER").stdout.splitlines()
        self.assertEqual(sum("-s 172.30.80.2/32 -j HEARTBEAT_PROBE_V2" in line for line in rules), 1)
        self.assertFalse(any(line.endswith("-j HEARTBEAT_PROBE") for line in rules))
        forward = self.run_in_container("iptables", "-S", "HEARTBEAT_PROBE_V2").stdout.splitlines()
        self.assertTrue(forward[-1].endswith("-j REJECT"))

        snapshot = rules
        repeated = self.install()
        self.assertEqual(repeated.stdout.strip(), policy_sha)
        self.assertEqual(self.run_in_container("iptables", "-S", "DOCKER-USER").stdout.splitlines(), snapshot)
        installed_contents = self.contents()

        denied = self.run_docker(
            "run", "--rm", "--network", f"container:{self.name}", "--cap-drop", "ALL",
            "--read-only", "--tmpfs", "/run:rw,noexec,nosuid,size=1m",
            "--mount", f"type=volume,source={self.volume},target=/host-sbin",
            "--entrypoint", "/bin/bash", IMAGE, "/opt/firewall/install.sh", check=False,
        )
        self.assertNotEqual(denied.returncode, 0, "installer without NET_ADMIN unexpectedly succeeded")
        self.assertEqual(self.contents(), installed_contents)
        self.assertEqual(self.run_in_container("iptables", "-S", "DOCKER-USER").stdout.splitlines(), snapshot)


if __name__ == "__main__":
    unittest.main()
