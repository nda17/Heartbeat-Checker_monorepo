#!/usr/bin/env python3
"""Run only inside the installed backup image on the protected backend VPS."""
import hashlib
import json
import os
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from release import PUBLIC_BASE, validate


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("public_redirect_rejected")


def main():
    directory = Path("/release")
    value = json.loads((directory / "release.json").read_text())
    key = validate(value, sys.argv[1], sys.argv[2])
    apk = (directory / "Heartbeat-Checker.apk").read_bytes()
    assert len(apk) == value["sizeBytes"] and hashlib.sha256(apk).hexdigest() == value["sha256"], "APK checksum mismatch"
    assert os.environ["BACKUP_S3_ENDPOINT"] == "https://s3.twcstorage.ru"
    assert os.environ["BACKUP_S3_BUCKET"] == "heartbeat-data-1"
    assert os.environ["BACKUP_S3_REGION"] == "ru-1"
    client = boto3.client("s3", endpoint_url=os.environ["BACKUP_S3_ENDPOINT"], region_name="ru-1",
                          aws_access_key_id=os.environ["BACKUP_S3_ACCESS_KEY_ID"],
                          aws_secret_access_key=os.environ["BACKUP_S3_SECRET_ACCESS_KEY"],
                          config=Config(signature_version="s3v4", connect_timeout=10, read_timeout=60,
                                        retries={"max_attempts": 3, "mode": "standard"},
                                        s3={"addressing_style": "path"}))
    public = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def denied(url):
        try:
            with public.open(urllib.request.Request(url, headers={"Range": "bytes=0-0"}), timeout=20):
                raise RuntimeError("private_storage_became_public")
        except urllib.error.HTTPError as error:
            assert error.code == 403, "Unexpected private storage response"

    def private_checks():
        acl = client.get_bucket_acl(Bucket="heartbeat-data-1")
        assert all(grant.get("Grantee", {}).get("Type") != "Group" for grant in acl["Grants"]), "Bucket ACL must remain private"
        denied(PUBLIC_BASE + "?list-type=2&max-keys=1")
        denied(PUBLIC_BASE + "?list-type=2&prefix=heartbeat-checker%2Fproduction%2Fdaily%2F&max-keys=1")

    private_checks()
    backup = client.list_objects_v2(Bucket="heartbeat-data-1", Prefix="heartbeat-checker/production/daily/", MaxKeys=1).get("Contents", [])
    assert backup, "No private backup object available for isolation check"
    backup_url = PUBLIC_BASE + urllib.parse.quote(backup[0]["Key"], safe="/")
    denied(backup_url)
    # Recoverability is a release prerequisite. Only the owner-encrypted archive
    # reaches the backup runner; the raw keystore and passwords never do.
    recovery = (directory / "recovery.tar.age").read_bytes()
    recovery_sha = sys.argv[3]
    assert 0 < len(recovery) < 1024 * 1024 and recovery.startswith(b"age-encryption.org/v1\n"), "Invalid encrypted signing archive"
    assert hashlib.sha256(recovery).hexdigest() == recovery_sha, "Encrypted signing archive checksum mismatch"
    recovery_key = "heartbeat-checker/android/signing/" + value["signingCertificateSha256"].replace(":", "").lower() + "/recovery.tar.age"
    recovery_metadata = {"sha256": recovery_sha, "signing-certificate-sha256": value["signingCertificateSha256"]}
    try:
        saved_recovery = client.head_object(Bucket="heartbeat-data-1", Key=recovery_key)
    except ClientError as error:
        if error.response["Error"]["Code"] not in ("404", "NoSuchKey", "NotFound"):
            raise
        saved_recovery = None
    if saved_recovery:
        assert saved_recovery["ContentLength"] == len(recovery) and saved_recovery["Metadata"] == recovery_metadata, "Immutable signing archive differs"
    else:
        client.put_object(Bucket="heartbeat-data-1", Key=recovery_key, Body=recovery, ACL="private", IfNoneMatch="*",
                          Metadata=recovery_metadata, ContentType="application/octet-stream")
    stored_recovery = client.get_object(Bucket="heartbeat-data-1", Key=recovery_key)["Body"].read(1024 * 1024 + 1)
    assert hashlib.sha256(stored_recovery).hexdigest() == recovery_sha, "Signing archive round-trip checksum mismatch"
    recovery_acl = client.get_object_acl(Bucket="heartbeat-data-1", Key=recovery_key)
    assert all(grant.get("Grantee", {}).get("Type") != "Group" for grant in recovery_acl["Grants"]), "Signing archive ACL must remain private"
    recovery_url = PUBLIC_BASE + recovery_key
    denied(recovery_url)
    metadata = {"source-sha": value["sourceSha"], "sha256": value["sha256"],
                "signing-certificate-sha256": value["signingCertificateSha256"], "version-code": str(value["versionCode"])}
    try:
        existing = client.head_object(Bucket="heartbeat-data-1", Key=key)
    except ClientError as error:
        if error.response["Error"]["Code"] not in ("404", "NoSuchKey", "NotFound"):
            raise
        existing = None
    if existing:
        assert existing["ContentLength"] == len(apk) and existing["Metadata"] == metadata, "Immutable version already exists with different provenance"
        assert hashlib.sha256(client.get_object(Bucket="heartbeat-data-1", Key=key)["Body"].read()).hexdigest() == value["sha256"], "Existing APK checksum mismatch"
        # Repeating publication of identical bytes is safe after an interrupted public check.
        client.put_object_acl(Bucket="heartbeat-data-1", Key=key, ACL="public-read")
    else:
        client.put_object(Bucket="heartbeat-data-1", Key=key, Body=apk, ACL="public-read", IfNoneMatch="*",
                          Metadata=metadata, ContentType="application/vnd.android.package-archive",
                          ContentDisposition='attachment; filename="Heartbeat-Checker.apk"',
                          CacheControl="public, max-age=31536000, immutable")
    with public.open(value["downloadUrl"], timeout=60) as response:
        assert response.status == 200 and response.headers.get("Content-Type", "").split(";")[0] == "application/vnd.android.package-archive", "Invalid public APK response"
        received = response.read(100 * 1024 * 1024 + 1)
        assert len(received) == len(apk) and hashlib.sha256(received).hexdigest() == value["sha256"], "Public APK checksum mismatch"
    private_checks()
    denied(backup_url)
    denied(recovery_url)
    print(json.dumps({"status": "published", "release": value, "manifestSha256": hashlib.sha256((directory / "release.json").read_bytes()).hexdigest(), "anonymousListing": "403", "anonymousBackupPrefixListing": "403", "anonymousBackupObject": "403", "anonymousSigningRecovery": "403", "signingRecoverySha256": recovery_sha}, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # SDK error text may include credentials or signed URLs. Keep logs secret-free.
        print("Android publication failed; immutable object and private-storage checks require review.", file=sys.stderr)
        sys.exit(1)
