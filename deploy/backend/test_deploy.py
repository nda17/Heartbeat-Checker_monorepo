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
BASE_ENV = (
    "DATABASE_URL=test\n"
    "TELEGRAM_API_BASE_URL=https://api.telegram.org\n"
    "KEEP_EXISTING=one\n"
    "TELEGRAM_API_PROXY_IP=\n"
)
NEXT_ENV = (
    "DATABASE_URL=test\n"
    "TELEGRAM_API_PROXY_IP=185.184.122.62\n"
    "KEEP_EXISTING=one\n"
    "TELEGRAM_API_BASE_URL=https://messengers-proxy.heartbeat-checker.ru/telegram-api\n"
)
INFORMER_BASE_ENV = BASE_ENV + (
    "MESSENGER_NOTIFICATIONS_ENABLED=false\n"
    "TELEGRAM_SUPPORT_WEBHOOK_SECRET=support_telegram_secret_0123456789\n"
    "MAX_SUPPORT_WEBHOOK_SECRET=support_max_secret_0123456789012\n"
    "TELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token\n"
    "MAX_BOT_INFORMER_TOKEN=old_max_token_1234567890\n"
    "TELEGRAM_INFORMER_BOT_USERNAME=heartbeat_oldbot\n"
    "MAX_INFORMER_BOT_URL=https://max.ru/heartbeat_oldbot\n"
)
DOCKER_MOCK = r'''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ["DOCKER_LOG"], "a") as log:
    log.write(json.dumps(args) + "\n")
if args[:2] == ["login", "ghcr.io"]:
    sys.stdin.read()
    if os.environ.get("MUTATE_NEXT_DURING_DEPLOY") == "1":
        with open(os.path.join(os.environ["DEPLOY_DIR"], ".env.next"), "a") as candidate:
            candidate.write("# changed during deployment\n")
    raise SystemExit(0)
if args[:2] == ["image", "inspect"]:
    print(os.environ["SOURCE_SHA"]); raise SystemExit(0)
if args and args[0] == "compose" and "config" in args and os.environ.get("FAIL_COMPOSE_PREFLIGHT") == "1":
    raise SystemExit(42)
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
            "probe": {"environment": {"PROBE_SHARED_SECRET": "test-probe-secret"}},
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

STAT_MOCK = r'''#!/usr/bin/env python3
import os, stat, sys
if sys.argv[1:3] == ["-c", "%a"] and len(sys.argv) == 4:
    print(format(stat.S_IMODE(os.stat(sys.argv[3]).st_mode), "o"))
    raise SystemExit(0)
raise SystemExit(1)
'''

PYTHON_MOCK = r'''#!/bin/sh
case "$*" in
  *"Probe public GET and metadata rejection passed"*)
    cat >/dev/null
    printf '%s\n' "Probe public GET and metadata rejection passed"
    exit 0
    ;;
esac
exec "$REAL_PYTHON" "$@"
'''

CP_MOCK = r'''#!/bin/sh
for last do :; done
if [ "${FAIL_ENV_ROLLBACK_COPY:-0}" = 1 ] && [ "$last" = .env.rollback.incoming ]; then exit 1; fi
exec "$REAL_CP" "$@"
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
        self.write_mock("stat", STAT_MOCK)
        self.write_mock("python3", PYTHON_MOCK)
        self.write_mock("cp", CP_MOCK)
        self.write_mock("flock", "#!/bin/sh\nexit 0\n")
        self.write_mock("sha256sum", "#!/bin/sh\nprintf '%064d  %s\\n' 0 \"$1\"\n")
        (self.backend / ".env").write_text(BASE_ENV)
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

    def run_deploy(self, activate_next_config=None, activate_messenger_notifications=None, **extra_env):
        env = os.environ.copy()
        env.update({
            "PATH": f"{self.bin}:{env['PATH']}",
            "DEPLOY_DIR": str(self.backend),
            "DOCKER_LOG": str(self.log),
            "SOURCE_SHA": SOURCE_SHA,
            "REAL_PYTHON": os.sys.executable,
            "REAL_CP": "/bin/cp",
        })
        env.update(extra_env)
        args = ["bash", str(self.script), SOURCE_SHA, *IMAGES, "nda17", BACKUP_IMAGE]
        if activate_next_config is not None:
            args.append("true" if activate_next_config else "false")
        if activate_messenger_notifications is not None:
            if activate_next_config is None:
                args.append("false")
            args.append("true" if activate_messenger_notifications else "false")
        return subprocess.run(
            args,
            input="test-token\n", cwd=self.root, env=env, text=True, capture_output=True,
        )

    def write_next_config(self, contents, mode=0o600):
        path = self.backend / ".env.next"
        path.write_text(contents)
        path.chmod(mode)
        return path

    def informer_candidate(self, relay=False, **overrides):
        values = {
            "MESSENGER_NOTIFICATIONS_ENABLED": "true",
            "TELEGRAM_INFORMER_WEBHOOK_SECRET": "informer_telegram_secret_0123456789",
            "MAX_INFORMER_WEBHOOK_SECRET": "informer_max_secret_012345678901",
            "TELEGRAM_BOT_INFORMER_TOKEN": "123456:existing_informer_token",
            "MAX_BOT_INFORMER_TOKEN": "max_token_1234567890_valid",
            "TELEGRAM_INFORMER_BOT_USERNAME": "heartbeat_notifier_bot",
            "MAX_INFORMER_BOT_URL": "https://max.ru/heartbeat_notifier_bot",
        }
        if relay:
            values.update({
                "TELEGRAM_API_BASE_URL": "https://messengers-proxy.heartbeat-checker.ru/telegram-api",
                "TELEGRAM_API_PROXY_IP": "185.184.122.62",
            })
        values.update(overrides)
        lines = (self.backend / ".env").read_text().splitlines()
        present = set()
        for index, line in enumerate(lines):
            key, separator, _ = line.partition("=")
            if separator and key in values:
                lines[index] = key + "=" + values[key]
                present.add(key)
        lines.extend(key + "=" + value for key, value in values.items() if key not in present)
        return "\n".join(lines) + "\n"

    @staticmethod
    def set_env_values(contents, **overrides):
        lines = contents.splitlines()
        for index, line in enumerate(lines):
            key, separator, _ = line.partition("=")
            if separator and key in overrides:
                lines[index] = key + "=" + overrides[key]
        return "\n".join(lines) + "\n"

    def configure_informer_base(self):
        path = self.backend / ".env"
        path.write_text(INFORMER_BASE_ENV)
        path.chmod(0o600)

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
        self.assertEqual((recovery / ".env").read_text(), BASE_ENV)
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

    def assert_invalid_candidate_rejected(self, expected_error):
        self.assert_invalid_activation_rejected(expected_error, relay=True)

    def assert_invalid_activation_rejected(self, expected_error, relay=False, messenger=False):
        original_env = (self.backend / ".env").read_bytes()
        result = self.run_deploy(activate_next_config=relay, activate_messenger_notifications=messenger)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(expected_error, result.stderr)
        self.assertEqual((self.backend / ".env").read_bytes(), original_env)
        self.assertTrue((self.backend / ".env.next").exists() or (self.backend / ".env.next").is_symlink())
        self.assertEqual(self.docker_calls(), [], "invalid activation must fail before Docker or release changes")
        self.assertFalse((self.backend / "pending").exists())

    def test_next_config_is_untouched_without_explicit_opt_in(self):
        candidate = self.write_next_config(NEXT_ENV)
        before_env = (self.backend / ".env").read_bytes()
        before_candidate = candidate.read_bytes()
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.backend / ".env").read_bytes(), before_env)
        self.assertEqual(candidate.read_bytes(), before_candidate)

    def test_both_activation_flags_false_ignore_candidate(self):
        candidate = self.write_next_config(NEXT_ENV)
        before_env = (self.backend / ".env").read_bytes()
        before_candidate = candidate.read_bytes()
        result = self.run_deploy(activate_next_config=False, activate_messenger_notifications=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.backend / ".env").read_bytes(), before_env)
        self.assertEqual(candidate.read_bytes(), before_candidate)
        receipt = json.loads((self.backend / "receipts" / f"{SOURCE_SHA}.json").read_text())
        self.assertFalse(receipt["telegram_relay_activated"])
        self.assertFalse(receipt["messenger_notifications_activated"])

    def assert_activation_mode(self, candidate_contents, relay, messenger):
        candidate = self.write_next_config(candidate_contents)
        result = self.run_deploy(activate_next_config=relay, activate_messenger_notifications=messenger)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.backend / ".env").read_text(), candidate_contents)
        self.assertFalse(candidate.exists())
        receipt = json.loads((self.backend / "receipts" / f"{SOURCE_SHA}.json").read_text())
        self.assertEqual(receipt["telegram_relay_activated"], relay)
        self.assertEqual(receipt["messenger_notifications_activated"], messenger)

    def test_activation_mode_relay_only(self):
        self.assert_activation_mode(NEXT_ENV, relay=True, messenger=False)

    def test_activation_mode_messenger_only(self):
        self.configure_informer_base()
        self.assert_activation_mode(self.informer_candidate(), relay=False, messenger=True)

    def test_activation_mode_combined(self):
        self.configure_informer_base()
        self.assert_activation_mode(self.informer_candidate(relay=True), relay=True, messenger=True)

    def test_each_flag_rejects_changes_outside_its_selected_key_set(self):
        self.configure_informer_base()
        relay_candidate = self.set_env_values(INFORMER_BASE_ENV, TELEGRAM_API_BASE_URL="https://messengers-proxy.heartbeat-checker.ru/telegram-api", TELEGRAM_API_PROXY_IP="185.184.122.62")
        self.write_next_config(relay_candidate)
        self.assert_invalid_activation_rejected("Configuration activation rejected: changes outside selected keys", messenger=True)

        self.configure_informer_base()
        messenger_candidate = self.informer_candidate()
        messenger_candidate = messenger_candidate.replace("KEEP_EXISTING=one\n", "KEEP_EXISTING=mutated\n")
        self.write_next_config(messenger_candidate)
        self.assert_invalid_activation_rejected("Configuration activation rejected: changes outside selected keys", messenger=True)

    def test_informer_activation_requires_complete_keys_unchanged_telegram_token_and_distinct_secrets(self):
        self.configure_informer_base()
        cases = (
            (self.informer_candidate().replace("MAX_INFORMER_WEBHOOK_SECRET=informer_max_secret_012345678901\n", ""), "invalid Informer webhook secret"),
            (self.informer_candidate(TELEGRAM_BOT_INFORMER_TOKEN="123456:rotated_token"), "existing Telegram Informer token must remain unchanged"),
            (self.informer_candidate(TELEGRAM_INFORMER_WEBHOOK_SECRET="support_telegram_secret_0123456789"), "Informer webhook secret matches Support"),
            (self.informer_candidate(MAX_INFORMER_WEBHOOK_SECRET="support_max_secret_0123456789012"), "Informer webhook secret matches Support"),
            (self.informer_candidate(MESSENGER_NOTIFICATIONS_ENABLED="false"), "messenger flag must be true"),
        )
        for candidate, reason in cases:
            with self.subTest(reason=reason):
                self.write_next_config(candidate)
                self.assert_invalid_activation_rejected("Configuration activation rejected: " + reason, messenger=True)

    def test_informer_candidate_rejects_control_bytes_and_unapproved_config_edits(self):
        self.configure_informer_base()
        self.write_next_config(self.informer_candidate() + "# invalid\x01\n")
        self.assert_invalid_activation_rejected("Configuration activation rejected: invalid env content", messenger=True)

        self.configure_informer_base()
        self.write_next_config(self.informer_candidate() + "UNRELATED_SECRET=must-not-activate\n")
        self.assert_invalid_activation_rejected("Configuration activation rejected: changes outside selected keys", messenger=True)

    def test_informer_candidate_must_be_regular_mode_600(self):
        self.configure_informer_base()
        target = self.backend / ".env.next.target"
        target.write_text(self.informer_candidate())
        target.chmod(0o600)
        (self.backend / ".env.next").symlink_to(target.name)
        self.assert_invalid_activation_rejected("Configuration activation requires a regular mode-600 .env.next", messenger=True)

        self.configure_informer_base()
        self.write_next_config(self.informer_candidate(), mode=0o640)
        self.assert_invalid_activation_rejected("Configuration activation requires a regular mode-600 .env.next", messenger=True)

    def test_quoted_existing_telegram_token_is_accepted_only_when_raw_value_is_unchanged(self):
        self.configure_informer_base()
        baseline = (self.backend / ".env").read_text().replace(
            "TELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token\n",
            'TELEGRAM_BOT_INFORMER_TOKEN="123456:existing_informer_token"\n',
        )
        (self.backend / ".env").write_text(baseline)
        (self.backend / ".env").chmod(0o600)
        candidate_contents = self.informer_candidate(
            TELEGRAM_BOT_INFORMER_TOKEN='"123456:existing_informer_token"',
        )
        self.assert_activation_mode(candidate_contents, relay=False, messenger=True)

    def test_quote_normalized_telegram_token_change_is_rejected(self):
        self.configure_informer_base()
        baseline = (self.backend / ".env").read_text().replace(
            "TELEGRAM_BOT_INFORMER_TOKEN=123456:existing_informer_token\n",
            'TELEGRAM_BOT_INFORMER_TOKEN="123456:existing_informer_token"\n',
        )
        (self.backend / ".env").write_text(baseline)
        (self.backend / ".env").chmod(0o600)
        self.write_next_config(self.informer_candidate())
        self.assert_invalid_activation_rejected(
            "Configuration activation rejected: existing Telegram Informer token must remain unchanged",
            messenger=True,
        )

    def test_quoted_support_secret_cannot_be_reused_for_informer(self):
        self.configure_informer_base()
        support_secret = "informer_telegram_secret_0123456789"
        baseline = (self.backend / ".env").read_text().replace(
            "TELEGRAM_SUPPORT_WEBHOOK_SECRET=support_telegram_secret_0123456789\n",
            f'TELEGRAM_SUPPORT_WEBHOOK_SECRET="{support_secret}"\n',
        )
        (self.backend / ".env").write_text(baseline)
        (self.backend / ".env").chmod(0o600)
        self.write_next_config(self.informer_candidate(TELEGRAM_INFORMER_WEBHOOK_SECRET=support_secret))
        self.assert_invalid_activation_rejected(
            "Configuration activation rejected: Informer webhook secret matches Support",
            messenger=True,
        )

    def test_mutated_candidate_after_activation_keeps_recovery_journal_without_receipt(self):
        self.configure_informer_base()
        activated_contents = self.informer_candidate()
        candidate = self.write_next_config(activated_contents)
        result = self.run_deploy(activate_next_config=False, activate_messenger_notifications=True, MUTATE_NEXT_DURING_DEPLOY="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Staged activation env changed during deployment", result.stderr)
        self.assertTrue(candidate.exists())
        self.assertIn("# changed during deployment", candidate.read_text())
        self.assertIn("phase=migration_succeeded", (self.backend / "pending").read_text())
        self.assertEqual((self.backend / ".env").read_text(), activated_contents)
        self.assertFalse((self.backend / "receipts" / f"{SOURCE_SHA}.json").exists())

    def test_valid_two_key_candidate_is_activated_and_consumed_only_on_success(self):
        candidate = self.write_next_config(NEXT_ENV)
        result = self.run_deploy(activate_next_config=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.backend / ".env").read_text(), NEXT_ENV)
        self.assertFalse(candidate.exists(), ".env.next should be consumed after the release is complete")

    def test_unapproved_next_config_key_fails_before_activation(self):
        self.write_next_config(NEXT_ENV + "UNRELATED_SECRET=must-not-activate\n")
        self.assert_invalid_candidate_rejected("Relay activation rejected:")

    def test_unexpected_endpoint_and_missing_target_key_fail_before_activation(self):
        variants = (
            NEXT_ENV.replace("/telegram-api", "/other-path"),
            NEXT_ENV.replace("TELEGRAM_API_PROXY_IP=185.184.122.62\n", ""),
        )
        for contents in variants:
            with self.subTest(contents=contents):
                self.write_next_config(contents)
                self.assert_invalid_candidate_rejected("Relay activation rejected: unexpected Telegram endpoint")

    def test_duplicate_next_config_key_fails_before_activation(self):
        self.write_next_config(NEXT_ENV + "TELEGRAM_API_BASE_URL=https://api.telegram.org\n")
        self.assert_invalid_candidate_rejected("Relay activation rejected: duplicate env key")

    def test_malformed_next_config_line_fails_before_activation(self):
        self.write_next_config(NEXT_ENV + "this is not a key-value row\n")
        self.assert_invalid_candidate_rejected("Relay activation rejected: invalid env line")

    def test_symlink_next_config_fails_before_activation(self):
        target = self.backend / ".env.next.target"
        target.write_text(NEXT_ENV)
        target.chmod(0o600)
        (self.backend / ".env.next").symlink_to(target.name)
        self.assert_invalid_candidate_rejected("Relay activation requires a regular mode-600 .env.next")

    def test_non_600_next_config_fails_before_activation(self):
        self.write_next_config(NEXT_ENV, mode=0o644)
        self.assert_invalid_candidate_rejected("Relay activation requires a regular mode-600 .env.next")

    def test_pre_migration_failure_restores_old_env_and_keeps_candidate(self):
        candidate = self.write_next_config(NEXT_ENV)
        result = self.run_deploy(activate_next_config=True, FAIL_COMPOSE_PREFLIGHT="1")
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertTrue(any(call and call[0] == "compose" and "config" in call for call in self.docker_calls()),
                        "the injected failure must occur during Compose preflight")
        self.assertEqual((self.backend / ".env").read_text(), BASE_ENV)
        self.assertEqual(candidate.read_text(), NEXT_ENV)
        self.assertFalse((self.backend / "pending").exists())
        self.assertEqual((self.backend / ".release.env").read_text(), "API_IMAGE=old\n")

    def test_env_rollback_failure_retains_recovery_snapshot_without_registry_credentials(self):
        self.write_next_config(NEXT_ENV)
        result = self.run_deploy(activate_next_config=True, FAIL_COMPOSE_PREFLIGHT="1",
                                 FAIL_ENV_ROLLBACK_COPY="1")
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertIn("previous backend .env could not be restored", result.stderr)
        self.assertIn("phase=env_rollback_failed", (self.backend / "pending").read_text())
        pending = (self.backend / "pending").read_text()
        snapshot_line = next(line for line in pending.splitlines() if line.startswith("env_snapshot="))
        snapshot = self.backend / snapshot_line.split("=", 1)[1]
        self.assertEqual(snapshot.read_text(), BASE_ENV)
        self.assertEqual(snapshot.stat().st_mode & 0o777, 0o600)
        self.assertIn("TELEGRAM_API_PROXY_IP=185.184.122.62\n", (self.backend / ".env").read_text())
        self.assertFalse((snapshot.parent / "docker-config").exists(), "registry credentials must be removed before retaining recovery data")
        self.assertFalse(any("up" in call for call in self.docker_calls()), "failed env rollback must not restart services with the activated env")

    def test_post_migration_failure_keeps_activated_env_and_pending_journal(self):
        candidate = self.write_next_config(NEXT_ENV)
        result = self.run_deploy(activate_next_config=True, FAIL_MIGRATION="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("phase=migration_started", (self.backend / "pending").read_text())
        self.assertIn("TELEGRAM_API_BASE_URL=https://messengers-proxy.heartbeat-checker.ru/telegram-api\n",
                      (self.backend / ".env").read_text())
        self.assertIn("TELEGRAM_API_PROXY_IP=185.184.122.62\n", (self.backend / ".env").read_text())
        self.assertTrue(candidate.exists(), ".env.next must remain available until full deployment succeeds")


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
