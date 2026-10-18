#!/usr/bin/env python3
"""Isolated rollback and WinWidget cleanup tests for deploy.sh.

Run with Python and Docker available. The harness starts the existing backup
image with no network, copies this test and deploy.sh into it (no bind mounts),
then exercises the real script against disposable filesystem fixtures.
"""

import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import uuid


IMAGE = os.environ.get("HEARTBEAT_RELAY_TEST_IMAGE", "heartbeat-checker/backup:local")
SCRIPT = Path(__file__).with_name("deploy.sh")
TEST_FILE = Path(__file__)


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs)


def execute_scenario(scenario, script_source):
    deploy_dir = Path("/opt/heartbeat-checker/relay")
    fake_bin = Path("/tmp/relay-test-bin")
    fixture = Path("/tmp/relay-test-fixture")
    for path in (deploy_dir, fake_bin, fixture):
        shutil.rmtree(path, ignore_errors=True)
        path.mkdir(parents=True)

    aero_files = {
        "/etc/nginx/sites-available/telegram.aerocrm.space": b"fixture aeroCRM vhost\n",
        "/etc/nginx/snippets/aerocrm-telegram-webhooks.conf": b"fixture aeroCRM snippet\n",
        "/etc/nginx/.aerocrm-telegram-business-relay.json": b"{\"fixture\":true}\n",
    }
    for raw_path, content in aero_files.items():
        path = Path(raw_path)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
    (Path("/etc/nginx/sites-enabled")).mkdir(parents=True, exist_ok=True)
    (Path("/etc/nginx/sites-enabled/zz-telegram.aerocrm.space")).symlink_to(
        "/etc/nginx/sites-available/telegram.aerocrm.space"
    )

    old_files = {
        "/etc/nginx/sites-available/tg.winwidget.ru": b"old WinWidget vhost\n",
        "/etc/nginx/sites-available/tg.winwidget.ru.bak-20260804-api-v1": b"old WinWidget backup\n",
        "/etc/nginx/modules-enabled/99-winwidget-telegram-api-stream.conf": b"old WinWidget stream\n",
        "/etc/letsencrypt/renewal/tg.winwidget.ru.conf": b"old renewal config\n",
    }
    for raw_path, content in old_files.items():
        path = Path(raw_path)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
    old_link = Path("/etc/nginx/sites-enabled/tg.winwidget.ru")
    old_link.symlink_to("/etc/nginx/sites-available/tg.winwidget.ru")
    for path in (
        "/etc/letsencrypt/archive/tg.winwidget.ru",
        "/etc/letsencrypt/live/tg.winwidget.ru",
        "/var/log/nginx",
        "/etc/nginx/modules-enabled",
    ):
        Path(path).mkdir(parents=True, exist_ok=True)
    for filename in ("cert1.pem", "privkey1.pem"):
        (Path("/etc/letsencrypt/archive/tg.winwidget.ru") / filename).write_text("old cert\n")
    for filename in ("fullchain.pem", "privkey.pem"):
        (Path("/etc/letsencrypt/live/tg.winwidget.ru") / filename).write_text("old live cert\n")
    for filename in (
        "telegram-api-stream.access.log", "telegram-api-stream.error.log",
        "tg.winwidget.ru.access.log", "tg.winwidget.ru.error.log",
    ):
        (Path("/var/log/nginx") / filename).write_text("old log\n")

    deploy_dir.mkdir(parents=True, exist_ok=True)
    (deploy_dir / "nginx-http.conf.incoming").write_text("relay HTTP fixture\n")
    (deploy_dir / "nginx-https.conf.incoming").write_text("relay HTTPS fixture\n")
    (deploy_dir / "deploy.sh.incoming").write_text("staged deploy script fixture\n")
    (deploy_dir / "receipt.txt").write_text("previous receipt\n")
    (deploy_dir / "receipts").mkdir()

    script = Path(script_source).read_text()
    for expected, raw_path in (
        ("a58f76fbff9d5852c3b7c6157c46b05347b0136e08c3ab864c4ac1c510ffaa1e",
         "/etc/nginx/sites-available/telegram.aerocrm.space"),
        ("da5df41bb386f9b8627981e2eda3a44de00d6de2177f75ceb749a4dd1a080062",
         "/etc/nginx/snippets/aerocrm-telegram-webhooks.conf"),
        ("9066c4678e22b37b986507bfb3d242a23e00b18f525e4a60305f3ff9680e142b",
         "/etc/nginx/.aerocrm-telegram-business-relay.json"),
    ):
        replacement = hashlib.sha256(aero_files[raw_path]).hexdigest()
        old_entry = expected + " " + raw_path
        if old_entry not in script:
            raise AssertionError("expected aeroCRM checksum entry changed: " + raw_path)
        script = script.replace(old_entry, replacement + " " + raw_path, 1)
    copied_script = fixture / "deploy.sh"
    copied_script.write_text(script)

    wrappers = {
        "nginx": r'''#!/bin/bash
set -eu
case "${1:-}" in
  -t) [[ ${RELAY_TEST_FAIL_NGINX:-} != 1 ]] ;;
  -T)
    shopt -s nullglob
    for file in /etc/nginx/sites-enabled/* /etc/nginx/modules-enabled/*; do
      [[ -e $file || -L $file ]] || continue
      cat "$file"
    done
    ;;
  *) exit 2 ;;
esac
''',
        "systemctl": r'''#!/bin/bash
set -eu
[[ ${1:-} == is-active && ${2:-} == --quiet ]] && exit 0
[[ ${1:-} == reload && ${2:-} == nginx ]] && exit 0
exit 2
''',
        "getent": r'''#!/bin/bash
printf '185.184.122.62 STREAM messengers-proxy.heartbeat-checker.ru\n'
''',
        "timeout": r'''#!/bin/bash
set -eu
shift
exec "$@"
''',
        "openssl": r'''#!/bin/bash
set -eu
case "${1:-}" in
  s_client)
    server_name=
    while (($#)); do
      if [[ $1 == -servername ]]; then server_name=$2; shift 2; else shift; fi
    done
    if [[ $server_name == telegram.aerocrm.space ]]; then
      printf 'aero-current\n'
    elif [[ $server_name == messengers-proxy.heartbeat-checker.ru ]]; then
      count=0
      [[ ! -f /tmp/relay-test-fixture/relay-tls-probes ]] || count=$(cat /tmp/relay-test-fixture/relay-tls-probes)
      count=$((count + 1))
      printf '%s\n' "$count" > /tmp/relay-test-fixture/relay-tls-probes
      printf 'relay-current-%s\n' "$count"
    else
      exit 2
    fi
    ;;
  x509)
    if [[ ${2:-} == -in ]]; then
      printf 'sha256 Fingerprint=DD:EE:FF\n'
    else
      certificate=$(cat)
      case "$certificate" in
        aero-current) printf 'sha256 Fingerprint=AA:BB:CC\n' ;;
        relay-current-*)
          count=${certificate#relay-current-}
          if [[ ${RELAY_TEST_FAIL:-} == tlspermanent ]] || \
             [[ ${RELAY_TEST_FAIL:-} == late && $count -lt 4 ]]; then
            printf 'sha256 Fingerprint=11:22:33\n'
          else
            printf 'sha256 Fingerprint=DD:EE:FF\n'
          fi
          ;;
        *) exit 2 ;;
      esac
    fi
    ;;
  *) exit 2 ;;
esac
''',
        "curl": r'''#!/bin/bash
set -eu
output=
url=
while (($#)); do
  case "$1" in
    -o) output=$2; shift 2 ;;
    -w|-X|-H|--max-time|--resolve|--data|--noproxy) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
status=200
body=
case "$url" in
  https://telegram.aerocrm.space/) status=200 ;;
  https://messengers-proxy.heartbeat-checker.ru/)
    count=0
    [[ ! -f /tmp/relay-test-fixture/relay-root-curl-probes ]] || count=$(cat /tmp/relay-test-fixture/relay-root-curl-probes)
    count=$((count + 1))
    printf '%s\n' "$count" > /tmp/relay-test-fixture/relay-root-curl-probes
    if [[ ${RELAY_TEST_FAIL:-} == late && $count -eq 1 ]]; then exit 60; fi
    status=404
    ;;
  */telegram-api/*/getMe)
    printf 'called\n' > /tmp/relay-test-fixture/api-probe
    status=401; body='{"ok":false,"error_code":401}'
    if [[ ${RELAY_TEST_FAIL:-} == postflight ]]; then status=500; body='{"ok":false,"error_code":500}'; fi
    ;;
  */webhooks/support/telegram) status=401; body=unauthorized ;;
  */telegram-api/*/setWebhook) status=404 ;;
  *) echo "unexpected mocked curl URL: $url" >&2; exit 2 ;;
esac
if [[ -n $output && $output != /dev/null ]]; then printf '%s' "$body" > "$output"; fi
printf '%s' "$status"
''',
        "certbot": r'''#!/bin/bash
set -eu
case "${1:-}" in
  certonly)
    printf 'called\n' > /tmp/relay-test-fixture/certbot-certonly
    [[ ${RELAY_TEST_FAIL:-} != certonly ]] || exit 55
    mkdir -p /etc/letsencrypt/live/messengers-proxy.heartbeat-checker.ru
    printf 'fixture certificate\n' > /etc/letsencrypt/live/messengers-proxy.heartbeat-checker.ru/fullchain.pem
    printf 'fixture key\n' > /etc/letsencrypt/live/messengers-proxy.heartbeat-checker.ru/privkey.pem
    ;;
  delete)
    printf 'called\n' > /tmp/relay-test-fixture/certbot-delete
    if [[ ${RELAY_TEST_FAIL:-} == delete ]]; then
      rm -rf /etc/letsencrypt/archive/tg.winwidget.ru
      exit 56
    fi
    rm -rf /etc/letsencrypt/archive/tg.winwidget.ru /etc/letsencrypt/live/tg.winwidget.ru
    rm -f /etc/letsencrypt/renewal/tg.winwidget.ru.conf
    ;;
  *) exit 2 ;;
esac
''',
        "ufw": r'''#!/bin/bash
set -eu
if [[ ${1:-} == status ]]; then
  printf 'Status: active\n8443/tcp ALLOW Anywhere\n'
elif [[ ${1:-} == --force && ${2:-} == delete ]]; then
  printf 'deleted\n' > /tmp/relay-test-fixture/ufw-deleted
elif [[ ${1:-} == allow ]]; then
  printf 'restored\n' > /tmp/relay-test-fixture/ufw-restored
else
  exit 2
fi
''',
    }
    for wrapper_name, contents in wrappers.items():
        path = fake_bin / wrapper_name
        path.write_text(contents)
        path.chmod(0o755)

    env = dict(os.environ, PATH=str(fake_bin) + ":/usr/bin:/bin", RELAY_TEST_FAIL=scenario)
    result = subprocess.run(
        ["bash", str(copied_script), "a" * 40, "b" * 64, "ops@example.com"],
        text=True, capture_output=True, env=env,
    )
    if scenario in ("success", "late") and result.returncode != 0:
        raise AssertionError("deploy success scenario failed: " + result.stdout + result.stderr)
    if scenario not in ("success", "late") and result.returncode == 0:
        raise AssertionError("deploy failure scenario unexpectedly succeeded")
    phase_markers = {
        "certonly": "Relay deployment failed; restoring previous nginx configuration",
        "postflight": "Telegram relay did not reach upstream",
        "tlspermanent": "Relay TLS vhost did not serve the issued certificate within 30 seconds",
        "delete": "Relay is active, but old certificate cleanup may be partial",
        "late": "Relay deployed and WinWidget proxy removed",
        "success": "Relay deployed and WinWidget proxy removed",
    }
    output = result.stdout + result.stderr
    if phase_markers[scenario] not in output:
        raise AssertionError("scenario failed at the wrong phase; expected marker " + repr(phase_markers[scenario]) + ":\n" + output)

    def probe_count(filename):
        path = fixture / filename
        return int(path.read_text()) if path.exists() else 0

    api_probed = (fixture / "api-probe").exists()
    if scenario == "certonly":
        if not (fixture / "certbot-certonly").exists() or api_probed:
            raise AssertionError("certonly failure did not stop before relay postflight")
        if probe_count("relay-root-curl-probes") or probe_count("relay-tls-probes"):
            raise AssertionError("certonly failure unexpectedly entered TLS readiness")
    elif scenario == "postflight":
        if not api_probed or probe_count("relay-tls-probes") != 2:
            raise AssertionError("postflight failure did not follow successful TLS readiness")
    elif scenario == "tlspermanent":
        if api_probed or probe_count("relay-tls-probes") < 5:
            raise AssertionError("permanent TLS mismatch did not exhaust readiness before postflight")
    elif scenario == "delete":
        if not api_probed or not (fixture / "certbot-delete").exists():
            raise AssertionError("partial cleanup failure did not follow postflight and begin certificate deletion")
    elif scenario == "late":
        if not api_probed or probe_count("relay-root-curl-probes") < 6:
            raise AssertionError("late convergence did not retry initial curl TLS error and complete postflight")
        if probe_count("relay-tls-probes") < 5:
            raise AssertionError("late convergence did not observe mismatch then two matching certificate probes")
    elif scenario == "success":
        if not api_probed or not (fixture / "certbot-delete").exists():
            raise AssertionError("successful deploy did not reach relay postflight and WinWidget certificate deletion")

    managed = (
        "/etc/nginx/sites-available/tg.winwidget.ru",
        "/etc/nginx/sites-enabled/tg.winwidget.ru",
        "/etc/nginx/sites-available/tg.winwidget.ru.bak-20260804-api-v1",
        "/etc/nginx/modules-enabled/99-winwidget-telegram-api-stream.conf",
    )
    old_certs = (
        "/etc/letsencrypt/renewal/tg.winwidget.ru.conf",
        "/etc/letsencrypt/archive/tg.winwidget.ru",
        "/etc/letsencrypt/live/tg.winwidget.ru",
    )
    receipt = (deploy_dir / "receipt.txt").read_text()
    if scenario in ("success", "late"):
        if any(Path(path).exists() or Path(path).is_symlink() for path in managed + old_certs):
            raise AssertionError("successful deploy left a WinWidget config or certificate path")
        if not Path("/tmp/relay-test-fixture/ufw-deleted").exists():
            raise AssertionError("successful deploy did not remove the old 8443 firewall rule")
        if "status=complete\n" not in receipt:
            raise AssertionError("successful deploy receipt is not complete")
        for raw_path, content in aero_files.items():
            if Path(raw_path).read_bytes() != content:
                raise AssertionError("successful deploy changed aeroCRM file: " + raw_path)
        for filename in ("telegram-api-stream.access.log", "telegram-api-stream.error.log",
                     "tg.winwidget.ru.access.log", "tg.winwidget.ru.error.log"):
            if (Path("/var/log/nginx") / filename).exists():
                raise AssertionError("successful deploy left obsolete WinWidget log: " + filename)
        if scenario == "late":
            probes = int(Path("/tmp/relay-test-fixture/relay-tls-probes").read_text())
            if probes < 5:
                raise AssertionError("readiness check did not wait for two consecutive matching TLS probes")
    elif scenario == "delete":
        if any(Path(path).exists() or Path(path).is_symlink() for path in managed):
            raise AssertionError("post-commit certificate cleanup failure rolled back active relay config")
        if Path("/etc/letsencrypt/archive/tg.winwidget.ru").exists():
            raise AssertionError("cleanup failure fixture did not simulate a partial certificate deletion")
        for raw_path in (
            "/etc/letsencrypt/renewal/tg.winwidget.ru.conf",
            "/etc/letsencrypt/live/tg.winwidget.ru",
        ):
            if not Path(raw_path).exists():
                raise AssertionError("partial cleanup did not preserve expected remaining certificate path")
        if not Path("/tmp/relay-test-fixture/ufw-deleted").exists():
            raise AssertionError("post-commit cleanup failure unexpectedly restored the old firewall rule")
        if "status=cleanup_pending\n" not in receipt:
            raise AssertionError("post-commit cleanup failure lost its pending receipt")
        if not Path("/etc/nginx/sites-enabled/heartbeat-relay-https.conf").is_symlink():
            raise AssertionError("post-commit cleanup failure removed the active relay")
    else:
        for raw_path, content in old_files.items():
            if Path(raw_path).read_bytes() != content:
                raise AssertionError("rollback did not restore original file: " + raw_path)
        if not old_link.is_symlink() or old_link.readlink() != Path("/etc/nginx/sites-available/tg.winwidget.ru"):
            raise AssertionError("rollback did not restore the original WinWidget symlink")
        for raw_path in old_certs:
            if not Path(raw_path).exists():
                raise AssertionError("rollback did not preserve old certificate path: " + raw_path)
        if receipt != "previous receipt\n":
            raise AssertionError("rollback did not restore the previous receipt")
        if Path("/tmp/relay-test-fixture/ufw-deleted").exists():
            raise AssertionError("failure before cleanup removed the 8443 firewall rule")
        if scenario == "tlspermanent" and Path("/etc/nginx/sites-available/heartbeat-relay-https.conf").exists():
            raise AssertionError("permanent relay TLS mismatch did not roll back the candidate HTTPS vhost")


class RelayDeployDockerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not shutil.which("docker"):
            raise RuntimeError("Docker is required for relay deploy safety tests")
        try:
            run(["docker", "image", "inspect", IMAGE])
        except (subprocess.CalledProcessError, PermissionError) as exc:
            raise RuntimeError("local backup Docker image is unavailable or inaccessible: " + IMAGE) from exc

    def run_in_isolated_container(self, scenario):
        container = "hc-relay-deploy-test-" + uuid.uuid4().hex[:12]
        run(["docker", "create", "--network", "none", "--user", "0", "--name", container,
             "--entrypoint", "/bin/sleep", IMAGE, "120"])
        try:
            run(["docker", "cp", str(SCRIPT), container + ":/tmp/deploy-source.sh"])
            run(["docker", "cp", str(TEST_FILE), container + ":/tmp/test-deploy.py"])
            run(["docker", "start", container])
            result = subprocess.run(
                ["docker", "exec", container, "/usr/bin/python3", "/tmp/test-deploy.py", "--inner", scenario],
                text=True, capture_output=True,
            )
            if result.returncode:
                self.fail("isolated deploy scenario failed:\n" + result.stdout + result.stderr)
        finally:
            subprocess.run(["docker", "rm", "-f", container], capture_output=True, text=True)

    def test_failures_rollback_and_success_removes_only_winwidget_assets(self):
        for scenario in ("certonly", "postflight", "tlspermanent", "delete", "late", "success"):
            with self.subTest(scenario=scenario):
                self.run_in_isolated_container(scenario)


if __name__ == "__main__" and len(os.sys.argv) == 3 and os.sys.argv[1] == "--inner":
    try:
        execute_scenario(os.sys.argv[2], "/tmp/deploy-source.sh")
    except Exception as exc:
        raise SystemExit(str(exc))
elif __name__ == "__main__":
    unittest.main()
