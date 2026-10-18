#!/usr/bin/env python3
"""Daily encrypted recovery bundles with verified S3 round trips and durable reports."""

import argparse
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import http.client
import ipaddress
import io
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo

import boto3
from botocore.config import Config


PREFIX = "heartbeat-checker/production/daily/"
RECOVERY_KEYS = ("WEBHOOK_ENCRYPTION_KEY", "AUTH_MAIL_ENCRYPTION_KEY", "SUPPORT_ENCRYPTION_KEY")
STATE_DIR = Path(os.environ.get("BACKUP_STATE_DIR", "/state"))
UTC = timezone.utc


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise BackupError("http_redirect_rejected")


class PinnedHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self, host, *, pinned_ip, **kwargs):
        super().__init__(host, **kwargs)
        self.pinned_ip = pinned_ip

    def connect(self):
        self.sock = socket.create_connection((self.pinned_ip, self.port), self.timeout, self.source_address)
        self.sock = self._context.wrap_socket(self.sock, server_hostname=self.host)


class PinnedHTTPSHandler(urllib.request.HTTPSHandler):
    def __init__(self, pinned_ip):
        super().__init__()
        self.pinned_ip = pinned_ip

    def https_open(self, request):
        def connection(host, **kwargs):
            return PinnedHTTPSConnection(host, pinned_ip=self.pinned_ip, **kwargs)
        return self.do_open(connection, request, context=self._context)


class BackupError(Exception):
    """Public, secret-free failure code suitable for logs and internal reporting."""


def utcnow():
    return datetime.now(UTC)


def timestamp():
    return utcnow().isoformat()


def telegram_transport():
    base_url = os.environ.get("TELEGRAM_API_BASE_URL", "").strip() or "https://api.telegram.org"
    if base_url not in ("https://api.telegram.org", "https://messengers-proxy.heartbeat-checker.ru/telegram-api"):
        raise BackupError("invalid_telegram_api_base_url")
    pinned_ip = os.environ.get("TELEGRAM_API_PROXY_IP", "").strip()
    handlers = [urllib.request.ProxyHandler({}), NoRedirect()]
    if pinned_ip:
        try:
            address = ipaddress.ip_address(pinned_ip)
        except ValueError as exc:
            raise BackupError("invalid_telegram_api_proxy_ip") from exc
        if not address.is_global or address.is_multicast or address.is_reserved:
            raise BackupError("invalid_telegram_api_proxy_ip")
        handlers.append(PinnedHTTPSHandler(pinned_ip))
    return base_url, urllib.request.build_opener(*handlers)


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def save(path, value):
    partial = path.with_suffix(".partial")
    with partial.open("w") as target:
        json.dump(value, target, ensure_ascii=False, sort_keys=True)
        target.write("\n")
        target.flush()
        os.fsync(target.fileno())
    partial.replace(path)
    directory = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def command(args, *, env=None, output=None, timeout=1200):
    try:
        return subprocess.run(args, check=True, env=env, stdout=output or subprocess.PIPE,
                              stderr=subprocess.PIPE, timeout=timeout).stdout
    except (subprocess.SubprocessError, OSError) as exc:
        # CLI stderr can contain database credentials and customer data.
        raise BackupError("command_failed:" + Path(args[0]).name) from exc


def database_env(url):
    parsed = urllib.parse.urlparse(url)
    if (parsed.scheme not in ("postgres", "postgresql") or parsed.hostname != "postgres"
            or parsed.port != 5432 or parsed.username != "heartbeat"
            or parsed.path != "/heartbeat"
            or urllib.parse.parse_qs(parsed.query) != {"sslmode": ["disable"]}):
        raise BackupError("invalid_database_target")
    return {"PATH": os.environ["PATH"], "HOME": "/tmp", "LANG": "C.UTF-8",
            "PGHOST": "postgres", "PGPORT": "5432", "PGUSER": "heartbeat",
            "PGDATABASE": "heartbeat", "PGPASSWORD": urllib.parse.unquote(parsed.password or ""),
            "PGCONNECT_TIMEOUT": "10"}


def recovery_keys():
    result = {}
    for key in RECOVERY_KEYS:
        value = os.environ.get(key, "")
        try:
            if len(base64.b64decode(value, validate=True)) != 32:
                raise ValueError()
        except ValueError as exc:
            raise BackupError("invalid_recovery_key:" + key) from exc
        result[key] = value
    return result


def s3_client():
    if (os.environ.get("BACKUP_S3_ENDPOINT") != "https://s3.twcstorage.ru"
            or os.environ.get("BACKUP_S3_BUCKET") != "heartbeat-data-1"
            or os.environ.get("BACKUP_S3_REGION") != "ru-1"):
        raise BackupError("invalid_s3_target")
    for key in ("BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY"):
        if not os.environ.get(key):
            raise BackupError("missing_s3_credentials")
    return boto3.client("s3", endpoint_url=os.environ["BACKUP_S3_ENDPOINT"],
                        region_name=os.environ["BACKUP_S3_REGION"],
                        aws_access_key_id=os.environ["BACKUP_S3_ACCESS_KEY_ID"],
                        aws_secret_access_key=os.environ["BACKUP_S3_SECRET_ACCESS_KEY"],
                        config=Config(signature_version="s3v4", connect_timeout=10, read_timeout=60,
                                      retries={"max_attempts": 3, "mode": "standard"},
                                      s3={"addressing_style": "path"}))


def add_bytes(archive, name, data):
    info = tarfile.TarInfo(name)
    info.size = len(data)
    info.mode = 0o600
    archive.addfile(info, io.BytesIO(data))


def encrypt_bundle(dump, destination, metadata, keys, identity):
    recipient = os.environ.get("BACKUP_AGE_RECIPIENT", "")
    if not re.fullmatch(r"age1[0-9a-z]{58}", recipient):
        raise BackupError("invalid_age_recipient")
    command(["age-keygen", "-o", str(identity)])
    ephemeral_recipient = command(["age-keygen", "-y", str(identity)]).decode().strip()
    process = subprocess.Popen(["age", "-r", recipient, "-r", ephemeral_recipient,
                                "-o", str(destination)], stdin=subprocess.PIPE,
                               stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        with tarfile.open(fileobj=process.stdin, mode="w|") as archive:
            archive.add(dump, arcname="database.dump", recursive=False)
            add_bytes(archive, "recovery-keys.json", json.dumps(keys).encode())
            add_bytes(archive, "recovery.json", json.dumps(metadata, sort_keys=True).encode())
        process.stdin.close()
        if process.wait(timeout=1200) != 0:
            raise BackupError("encryption_failed")
    except BaseException:
        process.kill()
        process.wait()
        raise
    finally:
        process.stderr.close()


def download(client, key, destination):
    with destination.open("wb") as target:
        client.download_fileobj(os.environ["BACKUP_S3_BUCKET"], key, target)


def restore_downloaded(ciphertext, identity, scratch, metadata, keys):
    restored_tar = scratch / "downloaded.tar"
    command(["age", "-d", "-i", str(identity), "-o", str(restored_tar), str(ciphertext)])
    dump = scratch / "downloaded.dump"
    with tarfile.open(restored_tar) as archive:
        if {member.name for member in archive.getmembers()} != {"database.dump", "recovery-keys.json", "recovery.json"}:
            raise BackupError("unexpected_recovery_bundle")
        if json.load(archive.extractfile("recovery.json")) != metadata:
            raise BackupError("recovery_metadata_mismatch")
        if json.load(archive.extractfile("recovery-keys.json")) != keys:
            raise BackupError("recovery_keys_mismatch")
        with dump.open("wb") as target:
            shutil.copyfileobj(archive.extractfile("database.dump"), target)
    restored_tar.unlink()
    if digest(dump) != metadata["dump_sha256"]:
        raise BackupError("dump_checksum_mismatch")
    cluster, socket = scratch / "postgres", scratch / "socket"
    socket.mkdir()
    local_env = {"PATH": os.environ["PATH"], "HOME": str(scratch), "LANG": "C.UTF-8"}
    command(["initdb", "-D", str(cluster), "--auth-local=trust", "--auth-host=reject",
             "--no-locale", "-U", "heartbeat"], env=local_env)
    started = False
    try:
        command(["pg_ctl", "-D", str(cluster), "-l", str(scratch / "postgres.log"),
                 "-o", "-c listen_addresses='' -c unix_socket_directories=" + str(socket),
                 "-w", "start"], env=local_env)
        started = True
        connection = ["-h", str(socket), "-U", "heartbeat"]
        command(["createdb", *connection, "restorecheck"], env=local_env)
        command(["pg_restore", *connection, "-d", "restorecheck", "--exit-on-error",
                 "--no-owner", "--no-privileges", str(dump)], env=local_env)
        ledger = command(["psql", *connection, "-d", "restorecheck", "-At", "-v", "ON_ERROR_STOP=1",
                          "-c", "SELECT COALESCE(json_agg(row_to_json(m) ORDER BY m.version), '[]') "
                          "FROM public.schema_migrations m"], env=local_env)
        migrations = json.loads(ledger)
        if not migrations:
            raise BackupError("restored_migration_ledger_empty")
        return migrations
    finally:
        if started:
            command(["pg_ctl", "-D", str(cluster), "-m", "immediate", "-w", "stop"], env=local_env)


def prune(client):
    """Delete only exact, application-owned dated keys after 14 complete days."""
    cutoff = (utcnow().astimezone(ZoneInfo("Europe/Moscow")).date() - timedelta(days=14)).isoformat()
    pattern = re.compile(re.escape(PREFIX) + r"(\d{4}-\d{2}-\d{2})/(bundle\.tar\.age|manifest\.json)$")
    for page in client.get_paginator("list_objects_v2").paginate(Bucket=os.environ["BACKUP_S3_BUCKET"], Prefix=PREFIX):
        for item in page.get("Contents", []):
            match = pattern.fullmatch(item["Key"])
            if match and match.group(1) < cutoff:
                client.delete_object(Bucket=os.environ["BACKUP_S3_BUCKET"], Key=item["Key"])


def run_backup(state, path, client):
    if state.get("status") == "success":
        return
    state.update(status="running", phase="dump", started_at=timestamp(), report_sent=False)
    save(path, state)
    encrypted = STATE_DIR / (state["date"] + ".tar.age")
    partial = encrypted.with_suffix(".partial")
    try:
        with tempfile.TemporaryDirectory(prefix="heartbeat-backup-", dir="/tmp") as temporary:
            scratch = Path(temporary)
            keys = recovery_keys()
            dump = scratch / "database.dump"
            with dump.open("wb") as target:
                command(["pg_dump", "--format=custom", "--no-owner", "--no-privileges"],
                        env=database_env(os.environ["DATABASE_URL"]), output=target)
            if not dump.stat().st_size:
                raise BackupError("empty_dump")
            metadata = {"format_version": 1, "database": "heartbeat", "created_at": state["started_at"],
                        "source_sha": os.environ["BACKUP_SOURCE_SHA"], "dump_sha256": digest(dump),
                        "recovery_key_names": list(RECOVERY_KEYS),
                        "postgres_version": command(["pg_dump", "--version"]).decode().strip()}
            state["phase"] = "encrypt"
            save(path, state)
            identity = scratch / "ephemeral.agekey"
            encrypt_bundle(dump, partial, metadata, keys, identity)
            partial.replace(encrypted)
            dump.unlink()
            checksum = digest(encrypted)
            object_key = PREFIX + state["date"] + "/bundle.tar.age"
            state.update(phase="upload", object_key=object_key, ciphertext_sha256=checksum,
                         bytes=encrypted.stat().st_size)
            save(path, state)
            # Do not set ACL or bucket policy. This bucket is provisioned privately.
            client.delete_object(Bucket=os.environ["BACKUP_S3_BUCKET"],
                                 Key=PREFIX + state["date"] + "/manifest.json")
            client.upload_file(str(encrypted), os.environ["BACKUP_S3_BUCKET"], object_key,
                               ExtraArgs={"Metadata": {"sha256": checksum}, "ContentType": "application/octet-stream"})
            state["phase"] = "download_and_restore"
            save(path, state)
            downloaded = scratch / "downloaded.age"
            download(client, object_key, downloaded)
            if digest(downloaded) != checksum:
                raise BackupError("s3_checksum_mismatch")
            migrations = restore_downloaded(downloaded, identity, scratch, metadata, keys)
            manifest = {**metadata, "object_key": object_key, "ciphertext_sha256": checksum,
                        "bytes": state["bytes"], "restore_checked_at": timestamp(), "migrations": migrations}
            manifest_body = json.dumps(manifest, sort_keys=True).encode()
            manifest_key = PREFIX + state["date"] + "/manifest.json"
            # Publish the completion marker last, only after restoring downloaded bytes.
            client.put_object(Bucket=os.environ["BACKUP_S3_BUCKET"], Key=manifest_key,
                              Body=manifest_body, ContentType="application/json")
            downloaded_manifest = scratch / "manifest.json"
            download(client, manifest_key, downloaded_manifest)
            if downloaded_manifest.read_bytes() != manifest_body:
                raise BackupError("s3_manifest_mismatch")
            state.update(status="success", phase="complete", completed_at=timestamp(),
                         manifest_key=manifest_key, migrations=len(migrations), report_sent=False)
            state.pop("error", None)
            save(path, state)
            encrypted.unlink()
        try:
            prune(client)
        except Exception:
            state["retention_warning"] = True
            state["report_sent"] = False
            save(path, state)
    except Exception as exc:
        code = str(exc) if isinstance(exc, BackupError) else type(exc).__name__
        state.update(status="failed", error=code, failed_at=timestamp(), report_sent=False)
        save(path, state)
        partial.unlink(missing_ok=True)
        raise BackupError(code) from exc


def report(state, path, client):
    if state.get("report_sent"):
        return True
    if state.get("status") == "success":
        link = client.generate_presigned_url("get_object", Params={"Bucket": os.environ["BACKUP_S3_BUCKET"],
                                              "Key": state["object_key"]}, ExpiresIn=3600)
        text = ("Резервная копия Heartbeat Checker за " + state["date"] + " готова.\n"
                "База: heartbeat (все таблицы и ledger).\n"
                "S3: загружена, SHA256 проверен, скачанный архив восстановлен в отдельном PostgreSQL.\n"
                "Размер: " + str(state["bytes"]) + " байт.\nSHA256: " + state["ciphertext_sha256"]
                + "\nЗашифрованный архив (ссылка на 1 час):\n" + link)
        if state.get("retention_warning"):
            text += "\nВнимание: очистка копий старше 14 дней не выполнена."
    else:
        text = ("ОШИБКА резервного копирования Heartbeat Checker за " + state["date"]
                + ".\nЭтап: " + state.get("phase", "unknown") + ". Код: "
                + state.get("error", "interrupted") + ".\nУспешная резервная копия не подтверждена.")
    if os.environ.get("TELEGRAM_INTERNAL_CHAT_ID") != "-1004317880847" or os.environ.get("TELEGRAM_BACKUPS_TOPIC_ID") != "4":
        raise BackupError("invalid_telegram_target")
    payload = json.dumps({"chat_id": os.environ["TELEGRAM_INTERNAL_CHAT_ID"], "message_thread_id": 4,
                          "text": text, "link_preview_options": {"is_disabled": True}}).encode()
    token = os.environ.get("TELEGRAM_BOT_INFORMER_TOKEN", "")
    if not token:
        raise BackupError("missing_telegram_token")
    base_url, opener = telegram_transport()
    request = urllib.request.Request(base_url + "/bot" + token + "/sendMessage", data=payload,
                                     headers={"Content-Type": "application/json"}, method="POST")
    try:
        with opener.open(request, timeout=20) as response:
            result = json.loads(response.read(1024 * 1024))
        if result.get("ok") is not True:
            raise BackupError("telegram_rejected")
    except Exception as exc:
        state.update(report_error="telegram_delivery_failed", report_last_attempt=timestamp())
        save(path, state)
        # Do not log exception: urllib embeds bot tokens and presigned URLs.
        return False
    state.update(report_sent=True, report_sent_at=timestamp())
    state.pop("report_error", None)
    save(path, state)
    return True


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("run", "report"))
    args = parser.parse_args()
    os.umask(0o077)
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    client = s3_client()
    failed = False
    if args.mode == "run":
        day = utcnow().astimezone(ZoneInfo("Europe/Moscow")).date().isoformat()
        path = STATE_DIR / (day + ".json")
        state = json.loads(path.read_text()) if path.exists() else {"date": day}
        try:
            run_backup(state, path, client)
        except BackupError:
            failed = True
    for path in sorted(STATE_DIR.glob("????-??-??.json")):
        state = json.loads(path.read_text())
        # A killed/OOM container leaves a durable intent; reporting does not run another dump.
        if state.get("status") == "running":
            state.update(status="failed", error="interrupted", failed_at=timestamp(), report_sent=False)
            save(path, state)
        if not report(state, path, client):
            failed = True
        if state.get("report_sent") and state["date"] < (utcnow().date() - timedelta(days=30)).isoformat():
            path.unlink()
            (STATE_DIR / (state["date"] + ".tar.age")).unlink(missing_ok=True)
    print("Backup operation completed" if not failed else "Backup/report requires retry")
    return 1 if failed else 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as failure:
        print("Backup operation failed: " + (str(failure) if isinstance(failure, BackupError) else type(failure).__name__), file=sys.stderr)
        sys.exit(1)
