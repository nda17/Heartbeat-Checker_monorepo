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
        command_patch, encrypt_patch = self.patch_dump_and_encrypt()
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
