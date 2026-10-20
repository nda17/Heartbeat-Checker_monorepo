#!/usr/bin/env python3
"""Safe staging tests for the Android push deployment input helper."""

import json
import base64
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("stage-push.py")


class StagePushTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.workdir = Path(self.temp.name) / "backend"
        self.workdir.mkdir(mode=0o700)
        self.env = self.workdir / ".env"
        self.original = b"DATABASE_URL=test\nANDROID_PUSH_ENABLED=false\nKEEP_EXISTING=one\n"
        self.env.write_bytes(self.original)
        self.env.chmod(0o600)

    def tearDown(self):
        self.temp.cleanup()

    def run_stage(self, payload):
        return subprocess.run(
            [sys.executable, str(SCRIPT), str(self.workdir)],
            input=payload,
            cwd=self.workdir,
            capture_output=True,
            check=False,
        )

    def test_stages_only_allowlisted_keys_and_preserves_literal_secret(self):
        payload = json.dumps({"projectId": "project_123", "serviceToken": "$synthetic-token-0123456789-abcdefghijklmnop"}).encode("ascii")
        result = self.run_stage(payload)
        self.assertEqual(result.returncode, 0, result.stderr)
        candidate = self.workdir / ".env.next"
        self.assertEqual(candidate.stat().st_mode & 0o777, 0o600)
        data = candidate.read_bytes()
        self.assertIn(b"ANDROID_PUSH_ENABLED=true", data)
        self.assertIn(b"RUSTORE_PUSH_PROJECT_ID=project_123", data)
        self.assertIn(b"RUSTORE_PUSH_SERVICE_TOKEN='$synthetic-token-0123456789-abcdefghijklmnop'", data)
        self.assertIn(b"DATABASE_URL=test", data)
        self.assertIn(b"KEEP_EXISTING=one", data)
        self.assertNotIn(b"ANDROID_PUSH_ENABLED=false", data)
        self.assertEqual(self.env.read_bytes(), self.original)

    def test_rejects_unframed_duplicate_oversized_or_unsafe_input(self):
        valid = b'{"projectId":"project","serviceToken":"' + b"x" * 32 + b'"}'
        cases = (
            b"",
            valid + b"\nextra",
            b'{"projectId":"one","projectId":"two","serviceToken":"' + b"x" * 32 + b'"}',
            b'{"projectId":"project","serviceToken":"short"}',
            b'{"projectId":"project","serviceToken":"' + b"x" * 8193 + b'"}',
            b'{"projectId":"project","serviceToken":"' + b"x" * 31 + b"'" + b"x" + b'"}',
            b'{"projectId":"bad/project","serviceToken":"' + b"x" * 32 + b'"}',
            b" " * 16385,
        )
        for payload in cases:
            with self.subTest(length=len(payload)):
                result = self.run_stage(payload)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.workdir / ".env.next").exists())
                self.assertEqual(self.env.read_bytes(), self.original)

    def test_refuses_to_overwrite_existing_candidate(self):
        candidate = self.workdir / ".env.next"
        candidate.write_bytes(b"reviewed candidate\n")
        result = self.run_stage(json.dumps({"projectId": "project", "serviceToken": "x" * 32}).encode())
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(candidate.read_bytes(), b"reviewed candidate\n")
        self.assertEqual(self.env.read_bytes(), self.original)

    def test_stages_fcm_for_current_release_without_changing_yookassa_production_bytes(self):
        billing = (
            b"BILLING_PROVIDER=yookassa\n"
            b"YOOKASSA_MODE=production\n"
            b"YOOKASSA_PRODUCTION_SHOP_ID=654321\n"
            b"YOOKASSA_PRODUCTION_SECRET_KEY='production_synthetic_secret_123'\n"
        )
        self.original = b"DATABASE_URL=test\nANDROID_PUSH_ENABLED=false\n" + billing + b"KEEP_EXISTING=one\n"
        self.env.write_bytes(self.original)
        self.env.chmod(0o600)
        account = {
            "type": "service_account",
            "project_id": "synthetic-fcm-project",
            "client_email": "push@synthetic-fcm-project.iam.gserviceaccount.com",
            "private_key": "synthetic-private-key",
            "token_uri": "https://oauth2.googleapis.com/token",
        }
        encoded_account = base64.b64encode(json.dumps(account, separators=(",", ":")).encode()).decode()
        payload = {
            "projectId": "synthetic-project",
            "serviceToken": "$synthetic-rustore-service-token-0123456789",
            "fcm": {"projectId": "synthetic-fcm-project", "serviceAccountBase64": encoded_account},
        }

        result = self.run_stage(json.dumps(payload).encode("ascii"))

        self.assertEqual(result.returncode, 0, result.stderr)
        candidate = (self.workdir / ".env.next").read_bytes()
        for expected in (
            b"FCM_PUSH_ENABLED=true",
            b"FCM_PROJECT_ID=synthetic-fcm-project",
            b"FCM_SERVICE_ACCOUNT_BASE64=" + encoded_account.encode(),
            b"RUSTORE_PUSH_SERVICE_TOKEN='$synthetic-rustore-service-token-0123456789'",
        ):
            self.assertIn(expected, candidate)
        for line in billing.splitlines():
            self.assertIn(line, candidate, f"staging changed billing setting bytes {line!r}")
        self.assertEqual(self.env.read_bytes(), self.original)

    def test_rejects_cloud_project_mismatch_and_unknown_nested_fields(self):
        account = {
            "type": "service_account",
            "project_id": "synthetic-fcm-project",
            "client_email": "push@synthetic-fcm-project.iam.gserviceaccount.com",
            "private_key": "synthetic-private-key",
            "token_uri": "https://oauth2.googleapis.com/token",
        }
        encoded_account = base64.b64encode(json.dumps(account).encode()).decode()
        valid = {
            "projectId": "synthetic-project",
            "serviceToken": "synthetic-rustore-service-token-0123456789",
            "fcm": {"projectId": "synthetic-fcm-project", "serviceAccountBase64": encoded_account},
            "huawei": {"projectId": "12345", "clientId": "67890", "clientSecret": "synthetic_huawei_secret_0123456789"},
        }
        cases = []
        mismatch = json.loads(json.dumps(valid))
        mismatch["fcm"]["projectId"] = "different-fcm-project"
        cases.append(("fcm project mismatch", mismatch))
        unknown_fcm = json.loads(json.dumps(valid))
        unknown_fcm["fcm"]["privateKey"] = "must-not-be-accepted"
        cases.append(("unknown fcm field", unknown_fcm))
        unknown_huawei = json.loads(json.dumps(valid))
        unknown_huawei["huawei"] = {"projectId": "12345", "clientId": "67890", "region": "unknown"}
        cases.append(("unknown Huawei field", unknown_huawei))
        partial_huawei = json.loads(json.dumps(valid))
        partial_huawei["huawei"] = {"projectId": "12345", "clientId": "67890"}
        cases.append(("partial Huawei configuration", partial_huawei))
        for name, payload in cases:
            with self.subTest(name=name):
                result = self.run_stage(json.dumps(payload).encode("ascii"))
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.workdir / ".env.next").exists())
                self.assertEqual(self.env.read_bytes(), self.original)


if __name__ == "__main__":
    unittest.main()
