#!/usr/bin/env python3
"""Validate public Android metadata; no storage credentials or network access."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

FIELDS = {"schemaVersion", "packageName", "versionName", "versionCode", "sourceSha", "releasedAt", "sizeBytes", "sha256", "signingCertificateSha256", "downloadUrl"}
PUBLIC_BASE = "https://s3.twcstorage.ru/heartbeat-data-1/"


def validate(value, source_sha=None, fingerprint=None):
    assert isinstance(value, dict) and set(value) == FIELDS, "Invalid release fields"
    assert value["schemaVersion"] == 1 and value["packageName"] == "ru.heartbeatchecker.app", "Invalid application"
    assert re.fullmatch(r"[1-9][0-9]*\.[0-9]+\.[0-9]+", value["versionName"]), "Invalid version"
    assert type(value["versionCode"]) is int and value["versionCode"] > 0, "Invalid version code"
    assert re.fullmatch(r"[a-f0-9]{40}", value["sourceSha"]), "Invalid source SHA"
    assert re.fullmatch(r"[a-f0-9]{64}", value["sha256"]), "Invalid APK digest"
    assert re.fullmatch(r"(?:[A-F0-9]{2}:){31}[A-F0-9]{2}", value["signingCertificateSha256"]), "Invalid signing fingerprint"
    assert re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value["releasedAt"]), "Invalid release date"
    assert type(value["sizeBytes"]) is int and 0 < value["sizeBytes"] < 100 * 1024 * 1024, "Invalid APK size"
    key = f'heartbeat-checker/android/{value["versionName"]}/Heartbeat-Checker.apk'
    assert value["downloadUrl"] == PUBLIC_BASE + key, "Invalid immutable public URL"
    assert source_sha is None or value["sourceSha"] == source_sha, "Source mismatch"
    assert fingerprint is None or value["signingCertificateSha256"] == fingerprint, "Signing certificate mismatch"
    return key


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--source-sha")
    parser.add_argument("--frontend-sha")
    parser.add_argument("--fingerprint", required=True)
    parser.add_argument("--apk", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    value = json.loads(args.manifest.read_text())
    validate(value, args.source_sha, args.fingerprint)
    if args.frontend_sha:
        assert re.fullmatch(r"[a-f0-9]{40}", args.frontend_sha), "Invalid frontend SHA"
        subprocess.run(["git", "merge-base", "--is-ancestor", value["sourceSha"], args.frontend_sha], check=True)
    if args.apk:
        assert args.apk.is_file() and not args.apk.is_symlink(), "Missing regular APK"
        assert args.apk.stat().st_size == value["sizeBytes"], "APK size mismatch"
        assert hashlib.sha256(args.apk.read_bytes()).hexdigest() == value["sha256"], "APK checksum mismatch"
    if args.output:
        args.output.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    print(hashlib.sha256(args.manifest.read_bytes()).hexdigest())


if __name__ == "__main__":
    main()
