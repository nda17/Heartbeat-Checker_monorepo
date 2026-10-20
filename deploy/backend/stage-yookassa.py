#!/usr/bin/env python3
"""Build a selected YooKassa candidate inside the private deployment workdir."""

import json
import os
from pathlib import Path
import re
import stat
import sys
from urllib.request import Request, build_opener, ProxyHandler, HTTPRedirectHandler
from urllib.error import URLError
import base64


OWNER_ID = "51ec7d19-80c4-4fb6-8edb-79dc7abe3120"
BILLING_KEYS = {
    b"BILLING_PROVIDER", b"BILLING_TEST_ACCOUNT_IDS", b"YOOKASSA_MODE",
    b"YOOKASSA_TEST_SHOP_ID", b"YOOKASSA_TEST_SECRET_KEY",
}
PRODUCTION_KEYS = {
    b"BILLING_PROVIDER", b"YOOKASSA_MODE",
    b"YOOKASSA_PRODUCTION_SHOP_ID", b"YOOKASSA_PRODUCTION_SECRET_KEY",
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


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        return None


def verify_production_shop(shop, secret):
    request = Request("https://api.yookassa.ru/v3/me")
    credentials = base64.b64encode(f"{shop}:{secret}".encode("ascii")).decode("ascii")
    request.add_header("Authorization", "Basic " + credentials)
    opener = build_opener(ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=8) as response:
            if response.status != 200:
                reject()
            raw = response.read((64 << 10) + 1)
        if len(raw) > 64 << 10:
            reject()
        data = json.loads(raw)
        if not isinstance(data, dict) or str(data.get("account_id")) != shop or data.get("test") is not False or data.get("status") != "enabled":
            reject()
    except (OSError, URLError, UnicodeError, ValueError, TypeError):
        reject()


def stage(workdir, mode):
    if mode not in ("test", "production"):
        reject()
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
            or (mode == "test" and not secret.startswith("test_"))
            or (mode == "production" and secret.startswith("test_"))
            or any(not 33 <= ord(c) <= 126 for c in secret)
            or "'" in secret or "\\" in secret):
        reject()
    if mode == "production":
        verify_production_shop(shop, secret)
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
    selected = BILLING_KEYS if mode == "test" else PRODUCTION_KEYS
    for line in data.split(b"\n"):
        if not line.strip() or line.lstrip().startswith(b"#"):
            untouched.append(line)
            continue
        match = re.fullmatch(rb"([A-Za-z_][A-Za-z0-9_]*)=(.*)", line)
        if match is None or match[1] in seen:
            reject()
        key = match[1]
        seen.add(key)
        if key not in selected:
            untouched.append(line)
    changed = [b"BILLING_PROVIDER=yookassa", b"YOOKASSA_MODE=" + mode.encode("ascii")]
    if mode == "test":
        changed += [
            b"BILLING_TEST_ACCOUNT_IDS=" + OWNER_ID.encode("ascii"),
            b"YOOKASSA_TEST_SHOP_ID=" + shop.encode("ascii"),
            b"YOOKASSA_TEST_SECRET_KEY='" + secret.encode("ascii") + b"'",
        ]
    else:
        changed += [
            b"YOOKASSA_PRODUCTION_SHOP_ID=" + shop.encode("ascii"),
            b"YOOKASSA_PRODUCTION_SECRET_KEY='" + secret.encode("ascii") + b"'",
        ]
    candidate = directory / ".env.next"
    descriptor = os.open(candidate, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as output:
        output.write(b"\n".join(changed + untouched))


if __name__ == "__main__":
    try:
        if len(sys.argv) not in (2, 3):
            reject()
        stage(sys.argv[1], sys.argv[2] if len(sys.argv) == 3 else "test")
    except (OSError, UnicodeError, ValueError, TypeError, KeyError, RecursionError):
        reject()
