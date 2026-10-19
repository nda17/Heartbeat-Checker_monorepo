#!/usr/bin/env python3
"""Build an owner-only sandbox candidate inside the current deployment workdir."""

import json
import os
from pathlib import Path
import re
import stat
import sys


OWNER_ID = "51ec7d19-80c4-4fb6-8edb-79dc7abe3120"
BILLING_KEYS = {
    b"BILLING_PROVIDER", b"BILLING_TEST_ACCOUNT_IDS", b"YOOKASSA_MODE",
    b"YOOKASSA_TEST_SHOP_ID", b"YOOKASSA_TEST_SECRET_KEY",
}


def reject():
    raise SystemExit("YooKassa CI candidate validation failed")


def unique_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate field")
        result[key] = value
    return result


def stage(workdir):
    raw = sys.stdin.buffer.read(2049)
    if not raw or len(raw) > 2048:
        reject()
    payload = json.loads(raw.decode("ascii"), object_pairs_hook=unique_fields)
    if not isinstance(payload, dict) or set(payload) != {"shopId", "secretKey"}:
        reject()
    shop, secret = payload["shopId"], payload["secretKey"]
    if not isinstance(shop, str) or not re.fullmatch(r"[0-9]{1,32}", shop):
        reject()
    # Single-quoted dotenv values are literal, including dollar signs. Reject
    # characters that need escape interpretation instead of changing key bytes.
    if (not isinstance(secret, str) or not 16 <= len(secret) <= 512
            or not secret.startswith("test_") or any(not 33 <= ord(c) <= 126 for c in secret)
            or "'" in secret or "\\" in secret):
        reject()
    directory = Path(workdir)
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o700 or info.st_uid != os.geteuid():
        reject()
    descriptor = os.open(".env", os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, "rb") as active:
        info = os.fstat(active.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_uid != os.geteuid():
            reject()
        data = active.read((256 << 10) + 1)
    if len(data) > 256 << 10 or any((c < 32 and c != 10) or c == 127 for c in data):
        reject()
    untouched, seen = [], set()
    for line in data.split(b"\n"):
        if not line.strip() or line.lstrip().startswith(b"#"):
            untouched.append(line)
            continue
        match = re.fullmatch(rb"([A-Za-z_][A-Za-z0-9_]*)=(.*)", line)
        if match is None or match[1] in seen:
            reject()
        key = match[1]
        seen.add(key)
        if key not in BILLING_KEYS:
            untouched.append(line)
    changed = [
        b"BILLING_PROVIDER=yookassa",
        b"BILLING_TEST_ACCOUNT_IDS=" + OWNER_ID.encode("ascii"),
        b"YOOKASSA_MODE=test",
        b"YOOKASSA_TEST_SHOP_ID=" + shop.encode("ascii"),
        b"YOOKASSA_TEST_SECRET_KEY='" + secret.encode("ascii") + b"'",
    ]
    candidate = directory / ".env.next"
    descriptor = os.open(candidate, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as output:
        output.write(b"\n".join(changed + untouched))


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            reject()
        stage(sys.argv[1])
    except (OSError, UnicodeError, ValueError, TypeError, KeyError, RecursionError):
        reject()
