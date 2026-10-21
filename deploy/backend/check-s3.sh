#!/usr/bin/env bash
# Run only by the exact-source backend workflow before configuration activation.
set -euo pipefail
[[ $# == 4 && $1 =~ ^[0-9a-f]{40}$ && $2 =~ ^[0-9a-f]{64}$ && $3 =~ ^[0-9a-f]{64}$ && $4 =~ ^[0-9]+-[0-9]+$ ]] || {
  echo 'Invalid S3 compatibility source/checksum arguments' >&2; exit 1;
}
[[ $(id -u) -ne 0 ]] || { echo 'S3 compatibility requires the deployment user' >&2; exit 1; }
source_sha=$1
ops_sha=$2
test_sha=$3
run_id=$4
cd /opt/heartbeat-checker/backend
exec 9>.deploy.lock
flock -n 9 || { echo 'Backend deployment/backup is in progress' >&2; exit 1; }
[[ ! -e pending && ! -L pending ]] || { echo 'Resolve pending backend deployment before S3 compatibility checks' >&2; exit 1; }
stage="$PWD/s3-compat/$source_sha-$run_id"
[[ -d s3-compat && ! -L s3-compat && -d $stage && ! -L $stage && $(stat -c %a "$stage") == 700 && $(stat -c %u "$stage") == "$(id -u)" ]] || {
  echo 'Invalid private S3 compatibility stage' >&2; exit 1;
}
workdir=
cleanup() {
  local status=$? cid file
  trap - EXIT
  if [[ -n $workdir ]]; then
    for file in "$workdir/source.cid" "$workdir/live.cid"; do
      if [[ -f $file && ! -L $file ]]; then
        cid=$(cat "$file")
        if [[ $cid =~ ^[0-9a-f]{64}$ ]]; then docker rm -f "$cid" >/dev/null 2>&1 || true; fi
      fi
    done
    rm -rf -- "$workdir"
  fi
  rm -rf -- "$stage"
  exit "$status"
}
trap cleanup EXIT
for file in hc-ops s3-compat.test; do
  [[ -f $stage/$file && ! -L $stage/$file && $(stat -c %u "$stage/$file") == "$(id -u)" ]] || {
    echo 'Missing regular S3 compatibility artifact' >&2; exit 1;
  }
done
[[ $(sha256sum "$stage/hc-ops" | awk '{print $1}') == "$ops_sha" && $(sha256sum "$stage/s3-compat.test" | awk '{print $1}') == "$test_sha" ]] || {
  echo 'S3 compatibility artifact transport checksum mismatch' >&2; exit 1;
}
umask 077
workdir=$(mktemp -d)
cp -p "$stage/hc-ops" "$stage/s3-compat.test" "$workdir/"
# Copies remain under a private parent; the non-root container can execute the
# separately mounted test file without accessing other host deployment files.
chmod 555 "$workdir/hc-ops" "$workdir/s3-compat.test"
[[ $(sha256sum "$workdir/hc-ops" | awk '{print $1}') == "$ops_sha" && $(sha256sum "$workdir/s3-compat.test" | awk '{print $1}') == "$test_sha" ]] || {
  echo 'S3 compatibility copied artifact checksum mismatch' >&2; exit 1;
}
ops="$workdir/hc-ops"
[[ $("$ops" version) == "$source_sha" ]] || { echo 'S3 configuration helper source mismatch' >&2; exit 1; }
for file in .env .backup.env .release.env compose.backup.yaml; do
  [[ -f $file && ! -L $file ]] || { echo 'Missing regular installed S3 runtime configuration' >&2; exit 1; }
done
for file in .env .backup.env .release.env; do
  [[ $(stat -c %a "$file") == 600 && $(stat -c %u "$file") == "$(id -u)" ]] || {
    echo 'Installed S3 runtime configuration must be private' >&2; exit 1;
  }
done
installed_source=$(awk -F= '$1 == "SOURCE_SHA" {print $2}' .release.env)
[[ $installed_source =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid installed S3 runtime source' >&2; exit 1; }
compose=(docker compose --env-file .env --env-file .backup.env --env-file .release.env -f compose.backup.yaml)
"${compose[@]}" config -q
image=$("${compose[@]}" config --format json | "$ops" compose-field backup.image)
composed_source=$("${compose[@]}" config --format json | "$ops" compose-field backup.source)
[[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/backup@sha256:[0-9a-f]{64}$ && $composed_source == "$installed_source" && $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image") == "$installed_source" ]] || {
  echo 'Installed S3 compatibility runtime provenance mismatch' >&2; exit 1;
}
runtime=(docker run --rm --pull never --user "$(id -u):$(id -g)" --read-only --cap-drop ALL --security-opt no-new-privileges:true
  --pids-limit 64 --memory 256m --cpus 1 --tmpfs '/tmp:rw,noexec,nosuid,size=32m,mode=1777'
  --mount "type=bind,src=$workdir/s3-compat.test,dst=/opt/s3-compat.test,readonly" --entrypoint /opt/s3-compat.test)
# This first invocation cannot access S3: no credential file and no network.
if ! timeout --kill-after=10s 30s "${runtime[@]}" --cidfile "$workdir/source.cid" --network none \
    --env HC_LIVE_S3_SOURCE_ONLY=1 "$image" > "$workdir/source.stdout" 2> "$workdir/source.stderr"; then
  echo 'S3 test artifact source check failed' >&2; exit 1;
fi
[[ $(cat "$workdir/source.stdout") == "$source_sha" && ! -s $workdir/source.stderr ]] || {
  echo 'S3 test artifact does not match the release source' >&2; exit 1;
}
"${compose[@]}" config --format json | "$ops" compose-s3-env "$workdir/s3.env"
if ! timeout --kill-after=10s 360s "${runtime[@]}" --cidfile "$workdir/live.cid" \
    --env-file "$workdir/s3.env" --env HC_LIVE_S3=1 "$image" \
    -test.run '^TestLiveS3Compatibility$' -test.timeout=300s -test.v > "$workdir/live.stdout" 2> "$workdir/live.stderr"; then
  echo 'S3 compatibility or exact-object cleanup failed; deployment blocked' >&2; exit 1;
fi
grep -Eq '^--- PASS: TestLiveS3Compatibility \([0-9.]+s\)$' "$workdir/live.stdout" || {
  echo 'S3 compatibility test did not complete; deployment blocked' >&2; exit 1;
}
# Never forward SDK diagnostics, secret environment data or presigned URLs.
printf '{"status":"PASS","sourceSha":"%s","testBinarySha256":"%s","helperSha256":"%s","existingRuntimeSourceSha":"%s","syntheticObjects":2,"multipartBytes":9437184,"cleanupVerified":true}\n' \
  "$source_sha" "$test_sha" "$ops_sha" "$installed_source"
