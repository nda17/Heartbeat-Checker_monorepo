#!/usr/bin/env bash
set -euo pipefail
cd /opt/heartbeat-checker/backend
mode=${1:-run}
[[ $# -le 1 && ( $mode == run || $mode == report || $mode == preflight ) ]] || {
  echo 'Expected run, report or preflight' >&2; exit 1;
}
release_env=.release.env
backup_compose=compose.backup.yaml
if [[ $mode == preflight ]]; then
  release_env=.release.env.incoming
  backup_compose=compose.backup.yaml.incoming
else
  exec 9>.deploy.lock
  flock -w 1800 9 || { echo 'Backend deployment/backup lock timeout' >&2; exit 1; }
  [[ $mode == report || ! -e pending ]] || { echo 'Resolve pending backend deployment before backup' >&2; exit 1; }
fi
for file in .env .backup.env "$release_env" "$backup_compose"; do
  [[ -f $file && ! -L $file ]] || { echo "Missing regular backup configuration: $file" >&2; exit 1; }
done
for file in .env .backup.env; do
  [[ $(stat -c %a "$file") == 600 ]] || { echo "$file must be mode 600" >&2; exit 1; }
done
python3 - <<'PY'
from pathlib import Path
import re

allowed = {
    "BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_REGION",
    "BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY",
    "BACKUP_AGE_RECIPIENT", "TELEGRAM_BOT_INFORMER_TOKEN",
    "TELEGRAM_INTERNAL_CHAT_ID", "TELEGRAM_BACKUPS_TOPIC_ID",
}
for line in Path(".backup.env").read_text().splitlines():
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    key = line.split("=", 1)[0].strip()
    if not re.fullmatch(r"[A-Z][A-Z0-9_]*", key) or key not in allowed:
        raise SystemExit(".backup.env contains an unsupported key: " + key)
PY
compose=(docker compose --env-file .env --env-file .backup.env --env-file "$release_env" -f "$backup_compose")
"${compose[@]}" config -q
if [[ $mode == preflight ]]; then
  exit 0
fi
image=$("${compose[@]}" config --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["backup"]["image"])')
[[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/backup@sha256:[0-9a-f]{64}$ ]] || { echo 'Immutable backup image required' >&2; exit 1; }
source_sha=$("${compose[@]}" config --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["backup"]["environment"]["BACKUP_SOURCE_SHA"])')
[[ $source_sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid installed backup source SHA' >&2; exit 1; }
[[ $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image") == "$source_sha" ]] || {
  echo 'Installed backup image revision mismatch' >&2; exit 1;
}
# No pull/build here: CI installs an exact, green release and checks its OCI revision.
"${compose[@]}" run --rm --no-deps --pull never backup "$mode"
