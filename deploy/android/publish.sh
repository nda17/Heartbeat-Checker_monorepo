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
# Use the current Android source binary, never a publisher from the installed
# backup image. The CI transport binds these bytes before the first execution.
incoming_ops="$stage/hc-ops"
[[ ${HC_OPS_SHA256:-} =~ ^[0-9a-f]{64}$ && -f $incoming_ops && ! -L $incoming_ops ]] || { echo 'Missing Android publication helper'; exit 1; }
[[ $(sha256sum "$incoming_ops" | awk '{print $1}') == "$HC_OPS_SHA256" ]] || { echo 'Android helper checksum mismatch'; exit 1; }
publish_workdir=$(mktemp -d)
trap 'rm -rf -- "$publish_workdir"' EXIT
ops="$publish_workdir/hc-ops"
cp -p "$incoming_ops" "$ops"
# The parent is private; the separately pinned bind-mounted executable must also
# be executable by the existing non-root container UID. Never run as root.
chmod 555 "$ops"
[[ $(sha256sum "$ops" | awk '{print $1}') == "$HC_OPS_SHA256" ]] || { echo 'Android helper copy checksum mismatch'; exit 1; }
[[ $("$ops" version) == "$source_sha" ]] || { echo 'Android helper source mismatch'; exit 1; }
for file in .backup.env .env .release.env compose.backup.yaml; do
  [[ -f $file && ! -L $file ]]
done
[[ $(stat -c %a .backup.env) == 600 ]]
compose=(docker compose --env-file .env --env-file .backup.env --env-file .release.env -f compose.backup.yaml)
image=$("${compose[@]}" config --format json | "$ops" compose-field backup.image)
installed_sha=$("${compose[@]}" config --format json | "$ops" compose-field backup.source)
[[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/backup@sha256:[0-9a-f]{64}$ && $installed_sha =~ ^[0-9a-f]{40}$ ]]
[[ $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image") == "$installed_sha" ]]
# Export only S3 values to the existing isolated backup runtime. Never app keys.
env_file="$publish_workdir/s3.env"
(umask 077; : > "$env_file")
"${compose[@]}" config --format json | "$ops" compose-s3-env "$env_file"
docker run --rm --pull never --user "$(id -u):$(id -g)" --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --pids-limit 64 --memory 256m --cpus 1 --env-file "$env_file" \
  --mount "type=bind,src=$stage,dst=/release,readonly" \
  --mount "type=bind,src=$ops,dst=/opt/hc-ops,readonly" \
  --entrypoint /opt/hc-ops "$image" android-publish "$source_sha" "$fingerprint" "$recovery_sha" > "$stage/publication-receipt.json.incoming"
"$ops" deploy-json json-check "$stage/publication-receipt.json.incoming"
mv "$stage/publication-receipt.json.incoming" "$stage/publication-receipt.json"
cat "$stage/publication-receipt.json"
