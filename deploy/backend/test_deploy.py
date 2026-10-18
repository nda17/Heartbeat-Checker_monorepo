#!/usr/bin/env python3
"""Failure and recovery tests for the backend deployment transaction."""

import json
import os
from pathlib import Path
import tempfile
import unittest
import subprocess


SOURCE_SHA = "a" * 40
IMAGES = [
    f"ghcr.io/nda17/heartbeat-checker_monorepo/{name}@sha256:{digest * 64}"
    for name, digest in (("api", "1"), ("worker", "2"), ("probe", "3"), ("migrate", "4"))
]
BACKUP_IMAGE = "ghcr.io/nda17/heartbeat-checker_monorepo/backup@sha256:" + "5" * 64
DOCKER_MOCK = r'''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ["DOCKER_LOG"], "a") as log:
    log.write(json.dumps(args) + "\n")
if args[:2] == ["login", "ghcr.io"]:
    sys.stdin.read(); raise SystemExit(0)
if args[:2] == ["image", "inspect"]:
    print(os.environ["SOURCE_SHA"]); raise SystemExit(0)
if "--profile" in args and "migration" in args:
    raise SystemExit(1 if os.environ.get("FAIL_MIGRATION") == "1" else 0)
if args[:2] == ["inspect", "--format"]:
    fmt = args[2]
    print("healthy" if "Health.Status" in fmt else "true"); raise SystemExit(0)
if args[:2] == ["inspect"]:
    print("healthy" if "Health.Status" in args[1] else "true"); raise SystemExit(0)
if args and args[0] == "compose":
    if "config" in args and "--format" in args:
        print(json.dumps({"services": {
            "api": {"environment": {"DATABASE_URL": "postgres://heartbeat:testpass@postgres:5432/heartbeat?sslmode=disable"}},
            "postgres": {"environment": {"POSTGRES_PASSWORD": "testpass"}},
            "worker": {"environment": {"SMTP_TLS_MODE": "implicit_tls"}},
        }})); raise SystemExit(0)
    if "ps" in args and "-q" in args:
        print("container-id"); raise SystemExit(0)
    if "exec" in args:
        if "pg_dump" in args:
            sys.stdout.write("isolated-test-database-dump\n"); raise SystemExit(0)
        if "SELECT 1" in args:
            print("1"); raise SystemExit(0)
        if "schema_migrations" in " ".join(args):
            print("6"); raise SystemExit(0)
raise SystemExit(0)
'''

CURL_MOCK = r'''#!/usr/bin/env python3
import sys
args = sys.argv[1:]
if "-w" in args:
    sys.stdout.write("403")
'''

IPTABLES_MOCK = r'''#!/usr/bin/env python3
import json, os, sys
state_path = os.environ["IPTABLES_STATE"]
try:
    state = json.load(open(state_path))
except FileNotFoundError:
    state = {"DOCKER-USER": [], "INPUT": []}
args = sys.argv[1:]
def save():
    json.dump(state, open(state_path, "w"))
def rules(chain):
    if chain not in state: raise SystemExit(1)
    return state[chain]
if args[0] == "-N":
    if args[1] in state: raise SystemExit(1)
    state[args[1]] = []; save(); raise SystemExit(0)
if args[0] == "-S":
    chain = args[1]
    if chain not in state: raise SystemExit(1)
    for rule in rules(chain): print("-A " + chain + " " + " ".join(rule))
    raise SystemExit(0)
if args[0] == "-L":
    chain = args[1]
    if chain not in state: raise SystemExit(1)
    print("Chain " + chain)
    for index, rule in enumerate(rules(chain), 1): print(str(index) + " " + " ".join(rule))
    raise SystemExit(0)
if args[0] == "-C":
    chain, rule = args[1], args[2:]
    raise SystemExit(0 if rule in rules(chain) else 1)
if args[0] == "-A":
    chain, rule = args[1], args[2:]
    rules(chain).append(rule); save(); raise SystemExit(0)
if args[0] == "-I":
    chain = args[1]
    if args[2].isdigit(): pos, rule = int(args[2]) - 1, args[3:]
    else: pos, rule = 0, args[2:]
    rules(chain).insert(pos, rule); save(); raise SystemExit(0)
raise SystemExit(2)
'''


class BackendDeployTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.backend = self.root / "backend"
        self.backend.mkdir()
        self.log = self.root / "docker.jsonl"
        self.script = self.root / "deploy.sh"
        source = Path(__file__).with_name("deploy.sh").read_text()
        self.script.write_text(source.replace("cd /opt/heartbeat-checker/backend", 'cd "$DEPLOY_DIR"', 1))
        self.write_mock("docker", DOCKER_MOCK)
        self.write_mock("curl", CURL_MOCK)
        self.write_mock("sleep", "#!/bin/sh\nexit 0\n")
        self.write_mock("stat", "#!/bin/sh\nif [ \"$1\" = \"-c\" ] && [ \"$2\" = \"%a\" ]; then echo 600; exit 0; fi\nexit 1\n")
        self.write_mock("flock", "#!/bin/sh\nexit 0\n")
        self.write_mock("sha256sum", "#!/bin/sh\nprintf '%064d  %s\\n' 0 \"$1\"\n")
        (self.backend / ".env").write_text("DATABASE_URL=test\n")
        (self.backend / ".env").chmod(0o600)
        (self.backend / "compose.yaml.incoming").write_text("compose candidate\n")
        (self.backend / "Caddyfile.incoming").write_text("Caddy candidate\n")
        (self.backend / "compose.backup.yaml.incoming").write_text("backup compose candidate\n")
        (self.backend / "backup.sh.incoming").write_text("#!/usr/bin/env bash\nif [[ \"$1\" == preflight && \"${FAIL_BACKUP_PREFLIGHT:-0}\" == 1 ]]; then echo preflight-failed >&2; exit 1; fi\nexit 0\n")
        (self.backend / ".release.env").write_text("API_IMAGE=old\n")
        (self.backend / "compose.yaml").write_text("old compose\n")
        (self.backend / "Caddyfile").write_text("old Caddy\n")

    def tearDown(self):
        self.temp.cleanup()

    def write_mock(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def run_deploy(self, **extra_env):
        env = os.environ.copy()
        env.update({
            "PATH": f"{self.bin}:{env['PATH']}",
            "DEPLOY_DIR": str(self.backend),
            "DOCKER_LOG": str(self.log),
            "SOURCE_SHA": SOURCE_SHA,
        })
        env.update(extra_env)
        return subprocess.run(
            ["bash", str(self.script), SOURCE_SHA, *IMAGES, "nda17", BACKUP_IMAGE],
            input="test-token\n", cwd=self.root, env=env, text=True, capture_output=True,
        )

    def docker_calls(self):
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_missing_provisioned_config_fails_before_any_change(self):
        (self.backend / ".env").unlink()
        before = {p.name: p.read_bytes() for p in self.backend.iterdir() if p.is_file()}
        result = self.run_deploy()
        after = {p.name: p.read_bytes() for p in self.backend.iterdir() if p.is_file()}
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("mode-600 .env", result.stderr)
        self.assertEqual(self.docker_calls(), [])
        self.assertEqual(after, before)

    def test_backup_preflight_failure_preserves_release_and_skips_migration(self):
        stable = {name: (self.backend / name).read_bytes() for name in (".release.env", "compose.yaml", "Caddyfile")}
        result = self.run_deploy(FAIL_BACKUP_PREFLIGHT="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("preflight-failed", result.stderr)
        self.assertFalse((self.backend / "pending").exists())
        self.assertFalse(list((self.backend / "backups").glob("*.dump")))
        self.assertFalse(any("--profile" in call and "migration" in call for call in self.docker_calls()))
        self.assertEqual({name: (self.backend / name).read_bytes() for name in stable}, stable)

    def test_migration_failure_preserves_backup_journal_database_and_new_release(self):
        result = self.run_deploy(FAIL_MIGRATION="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("keep database, backup and pending journal", result.stderr)
        pending = (self.backend / "pending").read_text()
        self.assertIn("phase=migration_started", pending)
        recovery_path = next(line.split("=", 1)[1] for line in pending.splitlines() if line.startswith("recovery="))
        recovery = self.backend / recovery_path
        self.assertEqual((recovery / ".release.env").read_text(), "API_IMAGE=old\n")
        self.assertEqual((recovery / ".env").read_text(), "DATABASE_URL=test\n")
        backups = list((self.backend / "backups").glob("*.dump"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_text(), "isolated-test-database-dump\n")
        self.assertIn(f"API_IMAGE={IMAGES[0]}", (self.backend / ".release.env").read_text())
        calls = self.docker_calls()
        self.assertTrue(any("pg_dump" in call for call in calls), "backup must precede migration")
        self.assertTrue(any("--profile" in call and "migration" in call for call in calls))
        flattened = [" ".join(call) for call in calls]
        stop_index = next(i for i, call in enumerate(flattened) if "stop" in call and "api" in call and "worker" in call and "probe" in call)
        dump_index = next(i for i, call in enumerate(flattened) if "pg_dump" in call)
        self.assertLess(stop_index, dump_index, "all database writers must stop before the backup is captured")
        restore_calls = [call for call in flattened if "pg_restore" in call]
        self.assertEqual(len(restore_calls), 1, "backup should be checked with one isolated restore")
        self.assertIn("heartbeat_restorecheck", restore_calls[0])
        self.assertNotIn("-d heartbeat ", restore_calls[0], "deployment must never restore over the live database")
        self.assertFalse(any("down" in call and "-v" in call for call in flattened), "deployment must never remove database volumes")
        self.assertFalse(any("up" in call and "old" in call for call in flattened), "post-migration failure must not restart old binaries")
        self.assertTrue(any("stop" in call and "api" in call and "worker" in call and "probe" in call for call in flattened), "writers must stop before backup and migration")
        self.assertFalse((self.backend / "receipts" / f"{SOURCE_SHA}.json").exists())


class ProbeFirewallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.state = self.root / "iptables.json"
        mock = self.bin / "iptables"
        mock.write_text(IPTABLES_MOCK)
        mock.chmod(0o755)
        source = Path(__file__).with_name("probe-firewall.sh").read_text()
        self.script = self.root / "probe-firewall.sh"
        self.script.write_text(source.replace("[[ $EUID -eq 0 ]]", "[[ 0 -eq 0 ]]", 1))

    def tearDown(self):
        self.temp.cleanup()

    def run_firewall(self):
        env = os.environ.copy()
        env.update({"PATH": f"{self.bin}:{env['PATH']}", "IPTABLES_STATE": str(self.state)})
        return subprocess.run(["bash", str(self.script)], env=env, text=True, capture_output=True)

    def load_rules(self):
        return json.loads(self.state.read_text())

    @staticmethod
    def target(rule):
        return rule[rule.index("-j") + 1]

    def test_rules_are_ordered_and_idempotent_for_probe_egress_and_host_input(self):
        first = self.run_firewall()
        self.assertEqual(first.returncode, 0, first.stderr)
        rules = self.load_rules()
        forward = rules["HEARTBEAT_PROBE"]
        self.assertEqual(self.target(forward[0]), "RETURN")
        self.assertIn("ESTABLISHED,RELATED", forward[0])
        self.assertEqual(self.target(forward[-1]), "REJECT")
        self.assertEqual(self.target(rules["HEARTBEAT_PROBE_INPUT"][0]), "RETURN")
        self.assertEqual(self.target(rules["HEARTBEAT_PROBE_INPUT"][-1]), "REJECT")
        dns_rules = [rule for rule in forward if "--dport" in rule]
        self.assertEqual(len(dns_rules), 4)
        self.assertTrue(all(self.target(rule) == "RETURN" for rule in dns_rules))
        self.assertTrue(all(forward.index(rule) < len(forward) - 1 for rule in dns_rules))
        allow_index = next(i for i, rule in enumerate(forward) if "--dports" in rule)
        self.assertEqual(self.target(forward[allow_index]), "RETURN")
        self.assertTrue(all(self.target(rule) == "REJECT" for rule in forward[1:allow_index] if "--dport" not in rule))
        self.assertIn(["-s", "172.30.80.2/32", "-j", "HEARTBEAT_PROBE"], rules["DOCKER-USER"])
        self.assertIn(["-s", "172.30.80.2/32", "-j", "HEARTBEAT_PROBE_INPUT"], rules["INPUT"])

        snapshot = json.dumps(rules, sort_keys=True)
        second = self.run_firewall()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(json.dumps(self.load_rules(), sort_keys=True), snapshot, "rerunning firewall setup must not duplicate or reorder rules")

    def test_rule_order_rejects_private_and_non_http_traffic_but_allows_established_and_public_web(self):
        result = self.run_firewall()
        self.assertEqual(result.returncode, 0, result.stderr)
        forward = self.load_rules()["HEARTBEAT_PROBE"]

        def verdict(destination, protocol, port, state="NEW"):
            for rule in forward:
                if "--ctstate" in rule:
                    accepted = rule[rule.index("--ctstate") + 1].split(",")
                    if state in accepted:
                        return self.target(rule)
                    continue
                if "-d" in rule:
                    cidr = rule[rule.index("-d") + 1]
                    import ipaddress
                    if ipaddress.ip_address(destination) not in ipaddress.ip_network(cidr):
                        continue
                if "-p" in rule and rule[rule.index("-p") + 1] != protocol:
                    continue
                if "--dports" in rule and str(port) not in rule[rule.index("--dports") + 1].split(","):
                    continue
                if "--dport" in rule and str(port) != rule[rule.index("--dport") + 1]:
                    continue
                if "--dports" in rule and protocol != "tcp":
                    continue
                return self.target(rule)
            return "REJECT"

        self.assertEqual(verdict("10.1.2.3", "tcp", 443), "REJECT")
        self.assertEqual(verdict("8.8.8.8", "tcp", 443), "RETURN")
        self.assertEqual(verdict("8.8.8.8", "tcp", 8080), "REJECT")
        self.assertEqual(verdict("8.8.8.8", "tcp", 443, "ESTABLISHED"), "RETURN")
        self.assertEqual(verdict("1.1.1.1", "udp", 53), "RETURN")
        self.assertEqual(verdict("8.8.8.8", "udp", 53), "REJECT")
        input_rules = self.load_rules()["HEARTBEAT_PROBE_INPUT"]
        self.assertEqual(self.target(input_rules[0]), "RETURN")
        self.assertIn("ESTABLISHED,RELATED", input_rules[0])
        self.assertEqual(self.target(input_rules[-1]), "REJECT")


if __name__ == "__main__":
    unittest.main()
