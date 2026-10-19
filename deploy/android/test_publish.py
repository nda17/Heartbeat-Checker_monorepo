import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch
import urllib.error

sys.path.insert(0, str(Path(__file__).parent))
import release


APK = b"synthetic signed apk"
RECOVERY = b"age-encryption.org/v1\nsynthetic encrypted signing archive"
RECOVERY_SHA = hashlib.sha256(RECOVERY).hexdigest()
FINGERPRINT = ":".join(["AB"] * 32)
SOURCE_SHA = "a" * 40
METADATA = {
    "source-sha": SOURCE_SHA,
    "sha256": hashlib.sha256(APK).hexdigest(),
    "signing-certificate-sha256": FINGERPRINT,
    "version-code": "42",
}


def load_publisher():
    fake_boto3 = types.ModuleType("boto3")
    fake_boto3.client = None
    fake_config = types.ModuleType("botocore.config")
    fake_config.Config = lambda **kwargs: kwargs
    fake_exceptions = types.ModuleType("botocore.exceptions")

    class ClientError(Exception):
        def __init__(self, response):
            super().__init__("synthetic client error")
            self.response = response

    fake_exceptions.ClientError = ClientError
    fake_botocore = types.ModuleType("botocore")
    fake_botocore.config = fake_config
    fake_botocore.exceptions = fake_exceptions
    with patch.dict(sys.modules, {
        "boto3": fake_boto3,
        "botocore": fake_botocore,
        "botocore.config": fake_config,
        "botocore.exceptions": fake_exceptions,
    }):
        spec = importlib.util.spec_from_file_location("android_publish_under_test", Path(__file__).with_name("publish.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    return module, fake_boto3, ClientError


class FakeBody(io.BytesIO):
    pass


class FakeS3:
    def __init__(self, *, existing=None, public_acl=False, recovery_acl_public=False):
        self.existing = existing
        self.existing_recovery = None
        self.public_acl = public_acl
        self.recovery_acl_public = recovery_acl_public
        self.calls = []

    def get_bucket_acl(self, **kwargs):
        self.calls.append(("get_bucket_acl", kwargs))
        grants = [{"Grantee": {"Type": "Group"}}] if self.public_acl else [{"Grantee": {"Type": "CanonicalUser"}}]
        return {"Grants": grants}

    def list_objects_v2(self, **kwargs):
        self.calls.append(("list_objects_v2", kwargs))
        return {"Contents": [{"Key": "heartbeat-checker/production/daily/backup.tar"}]}

    def head_object(self, **kwargs):
        self.calls.append(("head_object", kwargs))
        if "/android/signing/" in kwargs["Key"]:
            if self.existing_recovery is None:
                raise self.client_error({"Error": {"Code": "404"}})
            return self.existing_recovery
        if self.existing is None:
            raise self.client_error({"Error": {"Code": "404"}})
        return self.existing

    def get_object(self, **kwargs):
        self.calls.append(("get_object", kwargs))
        content = RECOVERY if "/android/signing/" in kwargs["Key"] else (self.existing_apk if hasattr(self, "existing_apk") else APK)
        return {"Body": FakeBody(content)}

    def get_object_acl(self, **kwargs):
        self.calls.append(("get_object_acl", kwargs))
        grants = [{"Grantee": {"Type": "Group"}}] if self.recovery_acl_public else [{"Grantee": {"Type": "CanonicalUser"}}]
        return {"Grants": grants}

    def put_object(self, **kwargs):
        self.calls.append(("put_object", kwargs))

    def put_object_acl(self, **kwargs):
        self.calls.append(("put_object_acl", kwargs))


class FakeResponse:
    status = 200
    headers = {"Content-Type": "application/vnd.android.package-archive"}

    def __init__(self, body=APK):
        self.body = body

    def read(self, size=-1):
        return self.body[:size]

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False


class FakeOpener:
    def __init__(self, *, private_is_public=False, public_body=APK):
        self.private_is_public = private_is_public
        self.public_body = public_body
        self.urls = []

    def open(self, request, timeout=None):
        url = request if isinstance(request, str) else request.full_url
        self.urls.append(url)
        if "?list-type=2" in url or "/production/daily/" in url or "/android/signing/" in url:
            if self.private_is_public:
                return FakeResponse()
            raise urllib.error.HTTPError(url, 403, "forbidden", {}, None)
        return FakeResponse(self.public_body)


class PublisherTests(unittest.TestCase):
    def setUp(self):
        self.publisher, self.fake_boto3, self.client_error = load_publisher()
        self.temp = tempfile.TemporaryDirectory()
        self.directory = Path(self.temp.name)
        self.value = {
            "schemaVersion": 1,
            "packageName": "ru.heartbeatchecker.app",
            "versionName": "1.2.3",
            "versionCode": 42,
            "sourceSha": SOURCE_SHA,
            "releasedAt": "2026-10-19T12:34:56Z",
            "sizeBytes": len(APK),
            "sha256": hashlib.sha256(APK).hexdigest(),
            "signingCertificateSha256": FINGERPRINT,
            "downloadUrl": release.PUBLIC_BASE + "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk",
        }
        (self.directory / "release.json").write_text(json.dumps(self.value))
        (self.directory / "Heartbeat-Checker.apk").write_bytes(APK)
        (self.directory / "recovery.tar.age").write_bytes(RECOVERY)
        self.client = FakeS3()
        self.client.client_error = self.client_error
        self.opener = FakeOpener()

    def tearDown(self):
        self.temp.cleanup()

    def _run(self):
        environment = {
            "BACKUP_S3_ENDPOINT": "https://s3.twcstorage.ru",
            "BACKUP_S3_BUCKET": "heartbeat-data-1",
            "BACKUP_S3_REGION": "ru-1",
            "BACKUP_S3_ACCESS_KEY_ID": "synthetic-access-key",
            "BACKUP_S3_SECRET_ACCESS_KEY": "synthetic-secret",
        }
        self.client.calls.clear()
        self.opener.urls.clear()
        path_factory = lambda value: self.directory if value == "/release" else Path(value)
        self.fake_boto3.client = lambda *args, **kwargs: self.client
        with patch.object(self.publisher, "Path", path_factory), \
             patch.object(self.publisher.urllib.request, "build_opener", return_value=self.opener), \
             patch.dict(self.publisher.os.environ, environment, clear=True), \
             patch.object(sys, "argv", ["publish.py", SOURCE_SHA, FINGERPRINT, RECOVERY_SHA]):
            self.publisher.main()

    def test_new_object_is_immutable_publication_and_private_storage_stays_denied(self):
        self._run()
        puts = [call for call in self.client.calls if call[0] == "put_object"]
        self.assertEqual(len(puts), 2)
        recovery_put, apk_put = puts
        self.assertIn("/android/signing/", recovery_put[1]["Key"])
        self.assertEqual(recovery_put[1]["IfNoneMatch"], "*")
        self.assertEqual(recovery_put[1]["ACL"], "private")
        self.assertEqual(recovery_put[1]["Body"], RECOVERY)
        self.assertNotIn("/android/signing/", apk_put[1]["Key"])
        self.assertEqual(apk_put[1]["IfNoneMatch"], "*")
        self.assertEqual(apk_put[1]["Metadata"], METADATA)
        self.assertEqual(apk_put[1]["ACL"], "public-read")
        self.assertGreaterEqual(sum("?list-type=2" in url for url in self.opener.urls), 2)
        self.assertTrue(any("/production/daily/" in url for url in self.opener.urls))
        self.assertGreaterEqual(sum("/android/signing/" in url for url in self.opener.urls), 2)

    def test_public_bucket_or_private_backup_exposure_fails_before_upload(self):
        self.client.public_acl = True
        with self.assertRaisesRegex(AssertionError, "Bucket ACL must remain private"):
            self._run()
        self.assertFalse(any(name == "put_object" for name, _ in self.client.calls))

        self.client.public_acl = False
        self.client.recovery_acl_public = True
        with self.assertRaisesRegex(AssertionError, "Signing archive ACL must remain private"):
            self._run()
        apk_puts = [kwargs for name, kwargs in self.client.calls if name == "put_object" and "/android/signing/" not in kwargs["Key"]]
        self.assertFalse(apk_puts)

        self.client.public_acl = False
        self.opener.private_is_public = True
        with self.assertRaisesRegex(RuntimeError, "private_storage_became_public"):
            self._run()
        self.assertFalse(any(name == "put_object" for name, _ in self.client.calls))

    def test_existing_version_with_different_provenance_is_never_overwritten(self):
        self.client.existing = {"ContentLength": len(APK), "Metadata": {**METADATA, "source-sha": "b" * 40}}
        with self.assertRaisesRegex(AssertionError, "Immutable version already exists"):
            self._run()
        apk_writes = [kwargs for name, kwargs in self.client.calls if name == "put_object" and "/android/signing/" not in kwargs["Key"]]
        self.assertFalse(apk_writes)
        self.assertFalse(any(name == "put_object_acl" for name, _ in self.client.calls))

    def test_existing_version_must_match_downloaded_bytes_before_acl_update(self):
        self.client.existing = {"ContentLength": len(APK), "Metadata": METADATA}
        self.client.existing_apk = b"different bytes"
        with self.assertRaisesRegex(AssertionError, "Existing APK checksum mismatch"):
            self._run()
        self.assertFalse(any(name == "put_object_acl" for name, _ in self.client.calls))

    def test_existing_recovery_archive_with_different_provenance_blocks_apk_publication(self):
        self.client.existing_recovery = {
            "ContentLength": len(RECOVERY),
            "Metadata": {"sha256": "0" * 64, "signing-certificate-sha256": FINGERPRINT},
        }
        with self.assertRaisesRegex(AssertionError, "Immutable signing archive differs"):
            self._run()
        self.assertFalse(any(
            name == "put_object" and "/android/signing/" not in kwargs["Key"]
            for name, kwargs in self.client.calls
        ))

    def test_public_download_must_match_built_artifact_before_reporting_success(self):
        self.opener.public_body = b"wrong public bytes"
        with self.assertRaisesRegex(AssertionError, "Public APK checksum mismatch"):
            self._run()


class PublishEnvironmentTests(unittest.TestCase):
    SCRIPT = Path(__file__).with_name("publish.sh")
    ALLOWED = {
        "BACKUP_S3_ENDPOINT",
        "BACKUP_S3_BUCKET",
        "BACKUP_S3_REGION",
        "BACKUP_S3_ACCESS_KEY_ID",
        "BACKUP_S3_SECRET_ACCESS_KEY",
    }

    @classmethod
    def setUpClass(cls):
        script = cls.SCRIPT.read_text(encoding="utf-8")
        marker = 'python3 -c \'\n'
        start = script.index(marker) + len(marker)
        end = script.index("\n' \"$env_file\"", start)
        cls.parser = script[start:end]

    def _run_parser(self, environment, *, compose_dollars=True):
        if compose_dollars:
            environment = {
                key: value.replace("$", "$$") if isinstance(value, str) else value
                for key, value in environment.items()
            }
        config = {"services": {"backup": {"environment": environment}}}
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "resolved.env"
            result = subprocess.run(
                [sys.executable, "-c", self.parser, str(output)],
                input=json.dumps(config),
                text=True,
                capture_output=True,
                check=False,
            )
            return result, output.read_text(encoding="utf-8") if output.exists() else None

    def test_resolved_values_are_preserved_and_only_s3_keys_are_exported(self):
        environment = {
            "BACKUP_S3_ENDPOINT": 'https://example.test/path?$mode=a#fragment=1 "quoted"',
            "BACKUP_S3_BUCKET": "bucket=with=equals # comment-like text",
            "BACKUP_S3_REGION": "'region-starts-with-a-quote",
            "BACKUP_S3_ACCESS_KEY_ID": 'access$HOME\'and"both"',
            "BACKUP_S3_SECRET_ACCESS_KEY": 'secret="double" and \'single\' # $VALUE=a; $$; $$$; ${NAME}',
            "DATABASE_URL": "must-not-be-exported",
            "JWT_SECRET": "must-not-be-exported-either",
        }
        environment["BACKUP_S3_BUCKET"] += " with $, $$, $$$, and ${NAME}"

        result, output = self._run_parser(environment)

        self.assertEqual(result.returncode, 0, result.stderr)
        expected = "".join(f"{key}={environment[key]}\n" for key in sorted(self.ALLOWED))
        self.assertEqual(output, expected)
        self.assertEqual({line.split("=", 1)[0] for line in output.splitlines()}, self.ALLOWED)
        self.assertNotIn("DATABASE_URL", output)
        self.assertNotIn("JWT_SECRET", output)

    def test_missing_empty_nonstring_and_multiline_values_fail_closed(self):
        valid = {key: "synthetic-value" for key in self.ALLOWED}
        invalid_environments = {
            "missing": {key: value for key, value in valid.items() if key != "BACKUP_S3_BUCKET"},
            "empty": {**valid, "BACKUP_S3_BUCKET": ""},
            "non-string": {**valid, "BACKUP_S3_BUCKET": 42},
            "carriage-return": {**valid, "BACKUP_S3_BUCKET": "value\rhidden"},
            "line-feed": {**valid, "BACKUP_S3_BUCKET": "value\nhidden"},
            "nul": {**valid, "BACKUP_S3_BUCKET": "value\0hidden"},
        }

        for label, environment in invalid_environments.items():
            with self.subTest(label=label):
                result, output = self._run_parser(environment)
                self.assertNotEqual(result.returncode, 0)
                self.assertIsNone(output)

    def test_unpaired_dollar_in_compose_output_is_rejected(self):
        environment = {key: "synthetic-value" for key in self.ALLOWED}
        environment["BACKUP_S3_BUCKET"] = "literal ${UNESCAPED}"

        result, output = self._run_parser(environment, compose_dollars=False)

        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(output)


if __name__ == "__main__":
    unittest.main()
