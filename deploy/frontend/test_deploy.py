#!/usr/bin/env python3
"""Regression tests for deploy.sh using temporary files and command mocks."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SOURCE_SHA = "a" * 40
IMAGE = "ghcr.io/example/heartbeat-checker/web@sha256:" + "b" * 64
API_ORIGIN = "https://api.heartbeat-checker.ru"


DOCKER_MOCK = r'''#!/usr/bin/env python3
import json, os, sys

args = sys.argv[1:]
with open(os.environ["DOCKER_LOG"], "a") as f:
    f.write(json.dumps(args) + "\n")
if args[:2] == ["image", "inspect"]:
    print(os.environ["IMAGE_REVISION"])
    raise SystemExit(0)
if args and args[0] == "compose" and "up" in args:
    state = os.environ["DOCKER_STATE"]
    try:
        count = int(open(state).read()) + 1
    except FileNotFoundError:
        count = 1
    open(state, "w").write(str(count))
    if os.environ.get("FAIL_UP_NUMBER") == str(count):
        raise SystemExit(1)
if args and args[0] == "compose" and "down" in args:
    if os.environ.get("FAIL_DOWN") == "1":
        raise SystemExit(1)
'''


CURL_MOCK = r'''#!/usr/bin/env python3
import os, sys

args = sys.argv[1:]
def option_value(flag):
    try:
        return args[args.index(flag) + 1]
    except (ValueError, IndexError):
        return None

out = option_value("-o")
url = args[-1]
is_postflight = "--resolve" in args
if is_postflight:
    if os.environ.get("FAIL_POSTFLIGHT") == "1":
        raise SystemExit(22)
    status, body = "401", '{"error":"unauthorized"}'
else:
    status = os.environ.get("PREFLIGHT_STATUS", "401")
    body = os.environ.get("PREFLIGHT_BODY", '{"error":"unauthorized"}')
if out and out != "/dev/null":
    open(out, "w").write(body)
if option_value("-w") is not None:
    sys.stdout.write(status)
'''


class DeployTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name, script in (("docker", DOCKER_MOCK), ("curl", CURL_MOCK)):
            path = self.bin / name
            path.write_text(script)
            path.chmod(0o755)
        sleep = self.bin / "sleep"
        sleep.write_text("#!/bin/sh\nexit 0\n")
        sleep.chmod(0o755)

        source = Path(__file__).with_name("deploy.sh").read_text()
        source = source.replace(
            "cd /opt/heartbeat-checker/frontend",
            'cd "$DEPLOY_DIR"',
            1,
        )
        self.script = self.root / "deploy.sh"
        self.script.write_text(source)
        self.deploy_dir = self.root / "site"
        self.deploy_dir.mkdir()
        (self.deploy_dir / "compose.yaml.incoming").write_text("new compose\n")
        (self.deploy_dir / "Caddyfile.incoming").write_text("new caddy\n")
        self.log = self.root / "docker.log"
        self.state = self.root / "docker.state"

    def tearDown(self):
        self.temp.cleanup()

    def old_deployment(self):
        for name, value in ((".env", "WEB_IMAGE=old-image\n"),
                            ("compose.yaml", "old compose\n"),
                            ("Caddyfile", "old caddy\n")):
            (self.deploy_dir / name).write_text(value)

    def run_deploy(self, **extra_env):
        env = os.environ.copy()
        env.update({
            "PATH": f"{self.bin}:{env['PATH']}",
            "DEPLOY_DIR": str(self.deploy_dir),
            "DOCKER_LOG": str(self.log),
            "DOCKER_STATE": str(self.state),
            "IMAGE_REVISION": SOURCE_SHA,
        })
        env.update({key: str(value) for key, value in extra_env.items()})
        return subprocess.run(
            ["bash", str(self.script), SOURCE_SHA, IMAGE, API_ORIGIN, "ci-bot"],
            cwd=self.root,
            env=env,
            text=True,
            capture_output=True,
        )

    def docker_calls(self):
        if not self.log.exists():
            return []
        import json
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_successful_first_deployment(self):
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Frontend deployed:", result.stdout)
        self.assertIn(IMAGE, (self.deploy_dir / ".env").read_text())
        self.assertEqual((self.deploy_dir / "compose.yaml").read_text(), "new compose\n")
        self.assertEqual((self.deploy_dir / "Caddyfile").read_text(), "new caddy\n")
        self.assertFalse((self.deploy_dir / "previous").exists())

    def test_bad_api_auth_contract_prevents_switch(self):
        self.old_deployment()
        result = self.run_deploy(PREFLIGHT_BODY='{"error":"forbidden"}')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("API preflight response is not the Go auth contract", result.stderr)
        self.assertFalse(any(call[:1] == ["compose"] for call in self.docker_calls()))
        self.assertEqual((self.deploy_dir / ".env").read_text(), "WEB_IMAGE=old-image\n")
        self.assertTrue((self.deploy_dir / "compose.yaml.incoming").exists())

    def test_image_revision_mismatch_prevents_switch(self):
        self.old_deployment()
        result = self.run_deploy(IMAGE_REVISION="c" * 40)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Image revision does not match source SHA", result.stderr)
        self.assertEqual(self.docker_calls(), [
            ["login", "ghcr.io", "-u", "ci-bot", "--password-stdin"],
            ["pull", IMAGE],
            ["image", "inspect", "--format", '{{ index .Config.Labels "org.opencontainers.image.revision" }}', IMAGE],
        ])
        self.assertEqual((self.deploy_dir / ".env").read_text(), "WEB_IMAGE=old-image\n")

    def test_failed_postflight_restores_previous_files_and_recreates(self):
        self.old_deployment()
        result = self.run_deploy(FAIL_POSTFLIGHT="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("restoring previous deployment", result.stderr)
        for name, expected in ((".env", "WEB_IMAGE=old-image\n"),
                               ("compose.yaml", "old compose\n"),
                               ("Caddyfile", "old caddy\n")):
            self.assertEqual((self.deploy_dir / name).read_text(), expected)
        ups = [call for call in self.docker_calls() if "up" in call]
        self.assertEqual(len(ups), 2)
        self.assertIn("--force-recreate", ups[0])
        self.assertIn("--force-recreate", ups[1])

    def test_failed_first_deployment_stops_containers_without_volume_deletion(self):
        result = self.run_deploy(FAIL_POSTFLIGHT="1")
        self.assertNotEqual(result.returncode, 0)
        calls = self.docker_calls()
        downs = [call for call in calls if "down" in call]
        self.assertEqual(len(downs), 1)
        self.assertNotIn("-v", downs[0])
        self.assertNotIn("--volumes", downs[0])
        self.assertFalse((self.deploy_dir / ".env").exists())
        self.assertFalse((self.deploy_dir / "compose.yaml").exists())
        self.assertFalse((self.deploy_dir / "Caddyfile").exists())

    def test_failed_first_deployment_down_error_retains_config(self):
        result = self.run_deploy(FAIL_POSTFLIGHT="1", FAIL_DOWN="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("CRITICAL: failed to stop the incomplete first deployment", result.stderr)
        self.assertIn(IMAGE, (self.deploy_dir / ".env").read_text())
        self.assertEqual((self.deploy_dir / "compose.yaml").read_text(), "new compose\n")
        self.assertEqual((self.deploy_dir / "Caddyfile").read_text(), "new caddy\n")

    def test_rollback_restart_error_has_critical_log_and_nonzero_exit(self):
        self.old_deployment()
        result = self.run_deploy(FAIL_POSTFLIGHT="1", FAIL_UP_NUMBER="2")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(
            "CRITICAL: previous frontend could not be restarted; inspect Compose immediately",
            result.stderr,
        )


if __name__ == "__main__":
    unittest.main()
