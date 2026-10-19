#!/usr/bin/env bash
set -euo pipefail
[[ $# == 4 && $1 =~ ^[0-9a-f]{40}$ && $2 =~ ^([0-9A-F]{2}:){31}[0-9A-F]{2}$ && $3 =~ ^[0-9]+$ && $4 =~ ^[0-9a-f]{64}$ ]] || { echo 'Expected source SHA, public fingerprint, run ID and encrypted recovery checksum'; exit 1; }
[[ $(id -u) -ne 0 ]] || { echo 'Android publication requires the non-root deployment user'; exit 1; }
source_sha=$1
fingerprint=$2
run_id=$3
recovery_sha=$4
cd /opt/heartbeat-checker/backend
exec 9>.deploy.lock
flock -w 1800 9 || { echo 'Backend deployment lock timeout'; exit 1; }
[[ ! -e pending ]] || { echo 'Resolve pending backend deployment first'; exit 1; }
stage="$PWD/android-releases/$source_sha-$run_id"
[[ -d $stage && ! -L $stage ]]
for file in .backup.env .env .release.env compose.backup.yaml; do
  [[ -f $file && ! -L $file ]]
done
[[ $(stat -c %a .backup.env) == 600 ]]
compose=(docker compose --env-file .env --env-file .backup.env --env-file .release.env -f compose.backup.yaml)
image=$("${compose[@]}" config --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["backup"]["image"])')
installed_sha=$("${compose[@]}" config --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["backup"]["environment"]["BACKUP_SOURCE_SHA"])')
[[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/backup@sha256:[0-9a-f]{64}$ && $installed_sha =~ ^[0-9a-f]{40}$ ]]
[[ $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image") == "$installed_sha" ]]
# Export only S3 values to the existing isolated backup runtime. Never app keys.
env_file=$(mktemp)
chmod 600 "$env_file"
trap 'rm -f "$env_file"' EXIT
"${compose[@]}" config --format json | python3 -c '
from pathlib import Path
import json, sys
allowed = {"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_REGION", "BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY"}
environment = json.load(sys.stdin)["services"]["backup"]["environment"]
values = {key: environment.get(key) for key in allowed}
assert all(isinstance(value, str) and value and not any(char in value for char in "\r\n\0")
           for value in values.values()), "Missing or invalid S3 configuration"
# Compose config serializes each literal dollar twice, including in JSON.
assert all("$" not in value.replace("$$", "") for value in values.values()), "Invalid Compose dollar escaping"
values = {key: value.replace("$$", "$") for key, value in values.items()}
Path(sys.argv[1]).write_text("".join(key + "=" + values[key] + "\n" for key in sorted(values)), encoding="utf-8")
' "$env_file"
docker run --rm --pull never --user "$(id -u):$(id -g)" --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --pids-limit 64 --memory 256m --cpus 1 --env-file "$env_file" \
  --mount "type=bind,src=$stage,dst=/release,readonly" \
  --entrypoint /opt/backup-venv/bin/python "$image" /release/publish.py "$source_sha" "$fingerprint" "$recovery_sha" > "$stage/publication-receipt.json.incoming"
python3 -m json.tool "$stage/publication-receipt.json.incoming" > /dev/null
mv "$stage/publication-receipt.json.incoming" "$stage/publication-receipt.json"
cat "$stage/publication-receipt.json"
