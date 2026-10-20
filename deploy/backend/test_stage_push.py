#!/usr/bin/env python3
"""Safe staging tests for the Android push deployment input helper."""

import json
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


if __name__ == "__main__":
    unittest.main()
