#!/usr/bin/env python3
"""Build a RuStore push candidate inside the current deployment workdir."""

import json
import os
from pathlib import Path
import re
import stat
import sys


PUSH_KEYS = {b"ANDROID_PUSH_ENABLED", b"RUSTORE_PUSH_PROJECT_ID", b"RUSTORE_PUSH_SERVICE_TOKEN"}


def reject():
    raise SystemExit("Android push CI candidate validation failed")


def unique_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate field")
        result[key] = value
    return result


def stage(workdir):
    raw = sys.stdin.buffer.read(16385)
    if not raw or len(raw) > 16384:
        reject()
    payload = json.loads(raw.decode("ascii"), object_pairs_hook=unique_fields)
    if not isinstance(payload, dict) or set(payload) != {"projectId", "serviceToken"}:
        reject()
    project, secret = payload["projectId"], payload["serviceToken"]
    if not isinstance(project, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", project):
        reject()
    # Single-quoted dotenv values are literal, including dollar signs. Reject
    # characters that need escape interpretation instead of changing key bytes.
    if (not isinstance(secret, str) or not 32 <= len(secret) <= 8192
            or any(not 33 <= ord(c) <= 126 for c in secret)
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
        if key not in PUSH_KEYS:
            untouched.append(line)
    changed = [
        b"ANDROID_PUSH_ENABLED=true",
        b"RUSTORE_PUSH_PROJECT_ID=" + project.encode("ascii"),
        b"RUSTORE_PUSH_SERVICE_TOKEN='" + secret.encode("ascii") + b"'",
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
