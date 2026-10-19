import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parent))
import release


FINGERPRINT = ":".join(["AB"] * 32)
SOURCE_SHA = "a" * 40


def manifest(apk=b"synthetic-apk"):
    return {
        "schemaVersion": 1,
        "packageName": "ru.heartbeatchecker.app",
        "versionName": "1.2.3",
        "versionCode": 42,
        "sourceSha": SOURCE_SHA,
        "releasedAt": "2026-10-19T12:34:56Z",
        "sizeBytes": len(apk),
        "sha256": hashlib.sha256(apk).hexdigest(),
        "signingCertificateSha256": FINGERPRINT,
        "downloadUrl": release.PUBLIC_BASE + "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk",
    }


class ReleaseValidationTests(unittest.TestCase):
    def test_returns_versioned_immutable_key_and_checks_provenance(self):
        self.assertEqual(
            release.validate(manifest(), SOURCE_SHA, FINGERPRINT),
            "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk",
        )
        with self.assertRaisesRegex(AssertionError, "Source mismatch"):
            release.validate(manifest(), "b" * 40, FINGERPRINT)
        with self.assertRaisesRegex(AssertionError, "Signing certificate mismatch"):
            release.validate(manifest(), SOURCE_SHA, ":".join(["CD"] * 32))

    def test_rejects_extra_fields_noncanonical_immutable_urls_and_bad_digests(self):
        value = manifest()
        value["secret"] = "must not appear in public metadata"
        with self.assertRaisesRegex(AssertionError, "Invalid release fields"):
            release.validate(value)

        for mutate in (
            lambda item: item.update(downloadUrl=item["downloadUrl"].replace("/1.2.3/", "/latest/")),
            lambda item: item.update(downloadUrl=item["downloadUrl"] + "?token=secret"),
            lambda item: item.update(versionCode=True),
        ):
            invalid = manifest()
            mutate(invalid)
            with self.subTest(invalid=invalid):
                with self.assertRaises(AssertionError):
                    release.validate(invalid)

    def test_apk_must_be_regular_and_match_size_and_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            apk = Path(directory) / "app.apk"
            apk.write_bytes(b"synthetic-apk")
            value = manifest()
            manifest_path = Path(directory) / "release.json"
            manifest_path.write_text(json.dumps(value))
            result = self._run(manifest_path, apk)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), hashlib.sha256(manifest_path.read_bytes()).hexdigest())

            value["sizeBytes"] += 1
            manifest_path.write_text(json.dumps(value))
            self._assert_failed(manifest_path, apk, "APK size mismatch")

            value["sizeBytes"] -= 1
            value["sha256"] = "0" * 64
            manifest_path.write_text(json.dumps(value))
            self._assert_failed(manifest_path, apk, "APK checksum mismatch")

            link = Path(directory) / "link.apk"
            link.symlink_to(apk)
            manifest_path.write_text(json.dumps(manifest()))
            self._assert_failed(manifest_path, link, "Missing regular APK")

    @staticmethod
    def _run(manifest_path, apk):
        return subprocess.run(
            [sys.executable, str(Path(__file__).with_name("release.py")), str(manifest_path),
             "--source-sha", SOURCE_SHA, "--fingerprint", FINGERPRINT, "--apk", str(apk)],
            capture_output=True, text=True,
        )

    def _assert_failed(self, manifest_path, apk, expected):
        result = self._run(manifest_path, apk)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(expected, result.stderr)


if __name__ == "__main__":
    unittest.main()
