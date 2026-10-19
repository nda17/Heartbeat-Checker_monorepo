#!/usr/bin/env python3
"""Regression tests for encrypted backup state, S3 checks, and reporting."""

import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import types
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from unittest.mock import patch
import urllib.request


MODULE_PATH = Path(__file__).with_name("backup.py")


class FakeConfig:
    def __init__(self, **kwargs):
        self.kwargs = kwargs


class FakeS3:
    def __init__(self):
        self.objects = {}
        self.deleted = []
        self.uploads = []
        self.puts = []
        self.pages = []
        self.presigned = []
        self.url = "https://s3.twcstorage.ru/signed"

    def upload_file(self, path, bucket, key, ExtraArgs):
        self.uploads.append((bucket, key, ExtraArgs))
        self.objects[key] = Path(path).read_bytes()

    def download_fileobj(self, bucket, key, target):
        target.write(self.objects[key])

    def put_object(self, **kwargs):
        self.puts.append(kwargs)
        self.objects[kwargs["Key"]] = kwargs["Body"]

    def delete_object(self, **kwargs):
        self.deleted.append(kwargs["Key"])
        self.objects.pop(kwargs["Key"], None)

    def get_paginator(self, name):
        self.asserted_paginator = name
        return self

    def paginate(self, **kwargs):
        return self.pages

    def generate_presigned_url(self, *args, **kwargs):
        self.presigned.append((args, kwargs))
        return self.url


def load_backup():
    boto3 = types.ModuleType("boto3")
    boto3.client = lambda *args, **kwargs: (args, kwargs)
    botocore = types.ModuleType("botocore")
    config = types.ModuleType("botocore.config")
    config.Config = FakeConfig
    botocore.config = config
    with patch.dict(sys.modules, {"boto3": boto3, "botocore": botocore, "botocore.config": config}):
        spec = importlib.util.spec_from_file_location("heartbeat_backup_under_test", MODULE_PATH)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    return module


backup = load_backup()


class BackupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.state_dir = Path(self.temp.name)
        backup.STATE_DIR = self.state_dir
        self.s3 = FakeS3()
        self.env = {
            "PATH": "/usr/bin",
            "BACKUP_S3_ENDPOINT": "https://s3.twcstorage.ru",
            "BACKUP_S3_BUCKET": "heartbeat-data-1",
            "BACKUP_S3_REGION": "ru-1",
            "BACKUP_S3_ACCESS_KEY_ID": "test-access",
            "BACKUP_S3_SECRET_ACCESS_KEY": "test-secret",
            "BACKUP_AGE_RECIPIENT": "age1" + "a" * 58,
            "BACKUP_SOURCE_SHA": "a" * 40,
            "DATABASE_URL": "postgres://heartbeat:testpass@postgres:5432/heartbeat?sslmode=disable",
            "WEBHOOK_ENCRYPTION_KEY": "d2ViaG9vay1rZXktc2VjcmV0",
            "AUTH_MAIL_ENCRYPTION_KEY": "YXV0aC1tYWlsLWtleS1zZWNyZXQ=",
            "SUPPORT_ENCRYPTION_KEY": "c3VwcG9ydC1rZXktc2VjcmV0",
            "TELEGRAM_INTERNAL_CHAT_ID": "-1004317880847",
            "TELEGRAM_BACKUPS_TOPIC_ID": "4",
            "TELEGRAM_BOT_INFORMER_TOKEN": "test-token",
        }
        self.env.update({key: __import__("base64").b64encode(bytes([n]) * 32).decode()
                         for n, key in enumerate(backup.RECOVERY_KEYS, 1)})
        self.env["BACKUP_STATE_DIR"] = str(self.state_dir)
        self.patcher = patch.dict(os.environ, self.env, clear=False)
        self.patcher.start()

    def tearDown(self):
        self.patcher.stop()
        self.temp.cleanup()

    def state_path(self, date="2026-10-19"):
        return self.state_dir / (date + ".json")

    def base_state(self):
        return {"date": "2026-10-19"}

    def patch_dump_and_encrypt(self, keys_seen=None):
        def command(args, *, env=None, output=None, **kwargs):
            if Path(args[0]).name == "pg_dump" and "--version" not in args:
                output.write(b"isolated test database dump")
                return b""
            if Path(args[0]).name == "pg_dump":
                return b"pg_dump (PostgreSQL) 18.6"
            raise AssertionError("unexpected external command: " + Path(args[0]).name)

        def encrypt(dump, destination, metadata, keys, identity):
            if keys_seen is not None:
                keys_seen.update(keys)
            destination.write_bytes(b"encrypted-bundle")
            identity.write_text("ephemeral identity")

        return patch.object(backup, "command", side_effect=command), patch.object(backup, "encrypt_bundle", side_effect=encrypt)

    def run_success(self, restore=None, download=None):
        state = self.base_state()
        _, encrypt_patch = self.patch_dump_and_encrypt()
        with command_patch, encrypt_patch, \
             patch.object(backup, "download", side_effect=download or self.s3.download_fileobj), \
             patch.object(backup, "restore_downloaded", side_effect=restore or (lambda *args: [{"version": 8}])):
            backup.run_backup(state, self.state_path(), self.s3)
        return state

    def test_s3_client_requires_fixed_https_endpoint_and_signing(self):
        observed = {}
        client = lambda *args, **kwargs: observed.update(args=args, kwargs=kwargs) or "client"
        with patch.object(backup.boto3, "client", side_effect=client), patch.dict(os.environ, self.env):
            self.assertEqual(backup.s3_client(), "client")
        self.assertEqual(observed["kwargs"]["endpoint_url"], "https://s3.twcstorage.ru")
        self.assertEqual(observed["kwargs"]["config"].kwargs["signature_version"], "s3v4")
        self.assertEqual(observed["kwargs"]["config"].kwargs["s3"]["addressing_style"], "path")
        bad = dict(self.env, BACKUP_S3_ENDPOINT="http://s3.twcstorage.ru")
        with patch.dict(os.environ, bad):
            with self.assertRaisesRegex(backup.BackupError, "invalid_s3_target"):
                backup.s3_client()

    def test_redirects_are_rejected_for_external_report_requests(self):
        handler = backup.NoRedirect()
        request = types.SimpleNamespace(full_url="https://api.telegram.org/sendMessage")
        with self.assertRaisesRegex(backup.BackupError, "http_redirect_rejected"):
            handler.redirect_request(request, None, 302, "Found", {}, "https://attacker.example/")

    def test_telegram_transport_allowlists_base_and_public_pin(self):
        for base in ("https://api.telegram.org", "https://messengers-proxy.heartbeat-checker.ru/telegram-api"):
            with self.subTest(base=base), patch.dict(os.environ, {
                "TELEGRAM_API_BASE_URL": base,
                "TELEGRAM_API_PROXY_IP": "8.8.8.8",
            }, clear=False):
                captured = {}

                def capture_handlers(*handlers):
                    captured["handlers"] = handlers
                    return types.SimpleNamespace(handlers=handlers)

                with patch.object(backup.urllib.request, "build_opener", side_effect=capture_handlers):
                    actual_base, opener = backup.telegram_transport()
                self.assertEqual(actual_base, base)
                self.assertTrue(any(isinstance(handler, urllib.request.ProxyHandler) and handler.proxies == {}
                                    for handler in captured["handlers"]))
                self.assertTrue(any(isinstance(handler, backup.NoRedirect) for handler in opener.handlers))

        for env in (
            {"TELEGRAM_API_BASE_URL": "http://api.telegram.org", "TELEGRAM_API_PROXY_IP": "8.8.8.8"},
            {"TELEGRAM_API_BASE_URL": "https://127.0.0.1/telegram-api", "TELEGRAM_API_PROXY_IP": "8.8.8.8"},
            {"TELEGRAM_API_BASE_URL": "https://api.telegram.org", "TELEGRAM_API_PROXY_IP": "127.0.0.1"},
        ):
            with self.subTest(env=env), patch.dict(os.environ, env, clear=False):
                with self.assertRaises(backup.BackupError):
                    backup.telegram_transport()

    def test_pinned_https_uses_hostname_for_sni_and_certificate_validation_without_dns(self):
        if not shutil.which("openssl"):
            self.skipTest("openssl is required to create an isolated TLS certificate")
        with tempfile.TemporaryDirectory() as directory:
            cert = Path(directory) / "server.pem"
            key = Path(directory) / "server-key.pem"
            subprocess.run([
                "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                "-keyout", str(key), "-out", str(cert), "-subj", "/CN=api.telegram.org",
                "-addext", "subjectAltName=DNS:api.telegram.org",
            ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

            class Handler(BaseHTTPRequestHandler):
                def do_POST(self):
                    self.send_response(200)
                    self.send_header("Content-Length", "2")
                    self.end_headers()
                    self.wfile.write(b"ok")

                def log_message(self, *_):
                    pass

            server = HTTPServer(("127.0.0.1", 0), Handler)
            port = server.server_address[1]
            tls_server = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls_server.load_cert_chain(certfile=str(cert), keyfile=str(key))
            observed_sni = []
            tls_server.set_servername_callback(lambda _socket, name, _context: observed_sni.append(name))
            server.socket = tls_server.wrap_socket(server.socket, server_side=True)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)
            self.addCleanup(thread.join, 2)

            context = ssl.create_default_context(cafile=str(cert))
            class LocalPinnedConnection(backup.PinnedHTTPSConnection):
                def __init__(self, host, *, pinned_ip, **kwargs):
                    super().__init__(host, pinned_ip=pinned_ip, port=port, **kwargs)

            handler = backup.PinnedHTTPSHandler("127.0.0.1")
            handler._context = context
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), backup.NoRedirect(), handler)
            request = urllib.request.Request(
                "https://api.telegram.org/botfixture/sendMessage", data=b"{}", method="POST",
            )
            original_getaddrinfo = socket.getaddrinfo

            def reject_hostname_lookup(host, *args, **kwargs):
                if host == "api.telegram.org":
                    raise AssertionError("pinned connection attempted hostname DNS lookup")
                return original_getaddrinfo(host, *args, **kwargs)

            with patch.object(socket, "getaddrinfo", side_effect=reject_hostname_lookup):
                with patch.object(backup, "PinnedHTTPSConnection", LocalPinnedConnection):
                    with opener.open(request, timeout=3) as response:
                        self.assertEqual(response.status, 200)
                        self.assertEqual(response.read(), b"ok")
            self.assertEqual(observed_sni, ["api.telegram.org"])

    def test_download_checksum_failure_never_publishes_manifest_or_success(self):
        state = self.base_state()
        command_patch, encrypt_patch = self.patch_dump_and_encrypt()
        def corrupt_download(client, key, destination):
            destination.write_bytes(b"corrupted ciphertext")
        with command_patch, encrypt_patch, patch.object(backup, "download", side_effect=corrupt_download):
            with self.assertRaisesRegex(backup.BackupError, "s3_checksum_mismatch"):
                backup.run_backup(state, self.state_path(), self.s3)
        self.assertEqual(state["status"], "failed")
        self.assertFalse(any(item["Key"].endswith("manifest.json") for item in self.s3.puts))
        self.assertNotEqual(state.get("status"), "success")

    def test_restore_failure_never_publishes_manifest_or_success(self):
        def reject_restore(*_):
            raise backup.BackupError("restore_rejected")
        state = self.base_state()
        command_patch, encrypt_patch = self.patch_dump_and_encrypt()
        with command_patch, encrypt_patch, patch.object(backup, "restore_downloaded", side_effect=reject_restore):
            with self.assertRaisesRegex(backup.BackupError, "restore_rejected"):
                backup.run_backup(state, self.state_path(), self.s3)
        self.assertEqual(state["status"], "failed")
        self.assertFalse(any(item["Key"].endswith("manifest.json") for item in self.s3.puts))

    def test_success_keeps_only_ciphertext_and_never_persists_recovery_secrets(self):
        keys_seen = {}
        state = self.base_state()
        command_patch, encrypt_patch = self.patch_dump_and_encrypt(keys_seen)
        with command_patch, encrypt_patch, \
             patch.object(backup, "restore_downloaded", return_value=[{"version": 8}]):
            backup.run_backup(state, self.state_path(), self.s3)
        self.assertEqual(state["status"], "success")
        self.assertEqual(len(self.s3.uploads), 1)
        self.assertEqual(self.s3.objects[state["object_key"]], b"encrypted-bundle")
        self.assertEqual(set(keys_seen), set(backup.RECOVERY_KEYS))
        persisted = b"".join(path.read_bytes() for path in self.state_dir.iterdir() if path.is_file())
        for secret in keys_seen.values():
            self.assertNotIn(secret.encode(), persisted)
        self.assertEqual([path.name for path in self.state_dir.iterdir()], ["2026-10-19.json"])

    def test_legacy_recovery_bundle_is_accepted_only_without_encrypted_mfa_rows(self):
        responses = iter((b"t|t", b"f"))
        with patch.object(backup, "command", side_effect=lambda *_args, **_kwargs: next(responses)):
            self.assertFalse(backup.encrypted_mfa_data(["-d", "restorecheck"], {"PATH": "/usr/bin"}))

        responses = iter((b"t|t", b"t"))
        with patch.object(backup, "command", side_effect=lambda *_args, **_kwargs: next(responses)):
            self.assertTrue(backup.encrypted_mfa_data(["-d", "restorecheck"], {"PATH": "/usr/bin"}))

        with patch.dict(os.environ, {backup.MFA_RECOVERY_KEY: ""}, clear=False):
            calls = []

            def encrypted_mfa_probe(args, *, env=None, output=None, **_kwargs):
                calls.append(args)
                if "pg_dump" in args[0]:
                    raise AssertionError("must reject before dumping when MFA rows lack a recovery key")
                return b"t|t" if "to_regclass" in args[-1] else b"t"

            state = self.base_state()
            with patch.object(backup, "command", side_effect=encrypted_mfa_probe):
                with self.assertRaisesRegex(backup.BackupError, "missing_mfa_recovery_key"):
                    backup.run_backup(state, self.state_path(), self.s3)
            self.assertEqual(state["status"], "failed")
            self.assertEqual(sum("to_regclass" in args[-1] for args in calls), 1)
            self.assertEqual(len(calls), 2)

        responses = iter((b"t|t", b"t", b"pg_dump (PostgreSQL) 18.6"))

        def command_with_mfa_bundle(args, *, output=None, **_kwargs):
            if Path(args[0]).name == "pg_dump" and "--version" not in args:
                output.write(b"synthetic dump")
                return b""
            return next(responses)

        command_patch, encrypt_patch = self.patch_dump_and_encrypt()
        state = self.base_state()
        with patch.object(backup, "command", side_effect=command_with_mfa_bundle), encrypt_patch, \
             patch.object(backup, "restore_downloaded", return_value=[{"version": 12}]):
            backup.run_backup(state, self.state_path(), self.s3)
        self.assertEqual(state["status"], "success")
        manifest = json.loads(self.s3.objects[state["manifest_key"]])
        self.assertEqual(set(manifest["recovery_key_names"]), set(backup.RECOVERY_KEYS))

    def test_mfa_schema_detection_fails_closed_if_only_one_table_exists(self):
        with patch.object(backup, "command", return_value=b"t|f"):
            with self.assertRaisesRegex(backup.BackupError, "incomplete_mfa_schema"):
                backup.encrypted_mfa_data(["-d", "restorecheck"], {"PATH": "/usr/bin"})

    def test_report_retries_without_dump_and_does_not_follow_redirect(self):
        state = {"date": "2026-10-19", "status": "failed", "phase": "dump", "error": "command_failed:pg_dump"}
        path = self.state_path()
        backup.save(path, state)
        class FailingOpener:
            def open(self, request, timeout):
                self_request.append(request)
                raise TimeoutError("transient")
        self_request = []
        with patch.object(backup.urllib.request, "build_opener", return_value=FailingOpener()), \
             patch.object(backup, "command", side_effect=AssertionError("report retry must not dump")):
            self.assertFalse(backup.report(state, path, self.s3))
            self.assertFalse(backup.report(state, path, self.s3))
        self.assertEqual(len(self_request), 2)
        self.assertFalse(state.get("report_sent", False))
        self.assertEqual(state["report_error"], "telegram_delivery_failed")
        self.assertEqual(self_request[0].full_url, "https://api.telegram.org/bottest-token/sendMessage")
        self.assertNotIn("test-token", path.read_text())

    def test_success_report_uses_readable_size_seven_day_link_and_sends_once(self):
        state = {
            "date": "2026-10-19",
            "status": "success",
            "object_key": backup.PREFIX + "2026-10-19/bundle.tar.age",
            "bytes": 1_572_864,
            "ciphertext_sha256": "a" * 64,
            "retention_warning": True,
        }
        path = self.state_path()
        requests = []

        class SuccessfulOpener:
            def open(self, request, timeout):
                requests.append((request, timeout))
                return io.BytesIO(b'{"ok": true}')

        with patch.object(backup, "ZoneInfo", return_value=backup.timezone(backup.timedelta(hours=3))), \
             patch.object(backup.urllib.request, "build_opener", return_value=SuccessfulOpener()):
            self.assertTrue(backup.report(state, path, self.s3))
            self.assertTrue(state["report_sent"])
            self.assertTrue(backup.report(state, path, self.s3))

        self.assertEqual(len(requests), 1, "completed report must not be sent again")
        self.assertEqual(len(self.s3.presigned), 1)
        self.assertEqual(self.s3.presigned[0][1]["ExpiresIn"], 604800)
        request, timeout = requests[0]
        self.assertEqual(timeout, 20)
        message = json.loads(request.data)["text"]
        self.assertIn("Размер: 1,5 МБ.", message)
        self.assertIn("a" * 64, message)
        self.assertIn("Ссылка действует 7 дней", message)
        self.assertIn("Срок хранения копии — 14 дней", message)
        self.assertIn("очистка копий старше 14 дней не выполнена", message)

    def test_report_size_uses_kilobytes_below_one_megabyte(self):
        self.assertEqual(backup.format_size(0), "0,0 КБ")
        self.assertEqual(backup.format_size(1024), "1,0 КБ")
        self.assertEqual(backup.format_size(1024 * 1024 - 1), "1024,0 КБ")

    def test_delayed_success_report_caps_link_lifetime_at_retention_boundary(self):
        state = {
            "date": "2026-10-19",
            "status": "success",
            "object_key": backup.PREFIX + "2026-10-19/bundle.tar.age",
            "bytes": 1024,
            "ciphertext_sha256": "b" * 64,
        }
        path = self.state_path()
        requests = []

        class SuccessfulOpener:
            def open(self, request, timeout):
                requests.append(request)
                return io.BytesIO(b'{"ok": true}')

        # Nov 1 21:00 MSK leaves only three hours before the archive's
        # conservative 14-day expiry (Nov 2 00:00 MSK).
        now = backup.datetime(2026, 11, 1, 18, tzinfo=backup.UTC)
        with patch.object(backup, "utcnow", return_value=now), \
             patch.object(backup, "ZoneInfo", return_value=backup.timezone(backup.timedelta(hours=3))), \
             patch.object(backup.urllib.request, "build_opener", return_value=SuccessfulOpener()):
            self.assertTrue(backup.report(state, path, self.s3))

        self.assertEqual(self.s3.presigned[0][1]["ExpiresIn"], 3 * 60 * 60)
        message = json.loads(requests[0].data)["text"]
        self.assertIn("Ссылка действует оставшийся срок хранения", message)
        self.assertIn("до 02.11.2026 00:00 МСК", message)

    def test_report_after_retention_expiry_does_not_presign_or_claim_availability(self):
        state = {
            "date": "2026-10-19",
            "status": "success",
            "object_key": backup.PREFIX + "2026-10-19/bundle.tar.age",
            "bytes": 1024,
            "ciphertext_sha256": "c" * 64,
        }
        requests = []

        class SuccessfulOpener:
            def open(self, request, timeout):
                requests.append(request)
                return io.BytesIO(b'{"ok": true}')

        now = backup.datetime(2026, 11, 2, 1, tzinfo=backup.timezone(backup.timedelta(hours=3)))
        with patch.object(backup, "utcnow", return_value=now), \
             patch.object(backup, "ZoneInfo", return_value=backup.timezone(backup.timedelta(hours=3))), \
             patch.object(backup.urllib.request, "build_opener", return_value=SuccessfulOpener()):
            self.assertTrue(backup.report(state, self.state_path(), self.s3))

        self.assertEqual(self.s3.presigned, [])
        message = json.loads(requests[0].data)["text"]
        self.assertIn("Срок хранения 14 дней истёк", message)
        self.assertIn("Ссылка на архив не формируется", message)
        self.assertIn("доступность не подтверждена", message)

    def test_retention_deletes_only_old_exact_keys_under_owned_prefix(self):
        today = backup.datetime(2026, 10, 19, tzinfo=backup.UTC)
        self.s3.pages = [{"Contents": [
            {"Key": backup.PREFIX + "2026-10-04/bundle.tar.age"},
            {"Key": backup.PREFIX + "2026-10-04/manifest.json"},
            {"Key": backup.PREFIX + "2026-10-05/bundle.tar.age"},
            {"Key": backup.PREFIX + "2026-10-04/other.dump"},
            {"Key": "other-service/2026-10-01/bundle.tar.age"},
        ]}]
        with patch.object(backup, "utcnow", return_value=today), patch.object(backup, "ZoneInfo", return_value=backup.UTC):
            backup.prune(self.s3)
        self.assertCountEqual(self.s3.deleted, [
            backup.PREFIX + "2026-10-04/bundle.tar.age",
            backup.PREFIX + "2026-10-04/manifest.json",
        ])


if __name__ == "__main__":
    unittest.main()
