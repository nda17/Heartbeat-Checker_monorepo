#!/usr/bin/env bash
set -euo pipefail

[[ $# == 4 && $1 =~ ^[0-9a-f]{40}$ && $2 =~ ^([0-9A-F]{2}:){31}[0-9A-F]{2}$ && $3 =~ ^[1-9][0-9]{0,17}$ && $4 =~ ^[0-9a-f]{64}$ ]] || {
  echo 'Expected source SHA, dev signing fingerprint, run ID and operations checksum'; exit 1;
}
[[ $(id -u) -ne 0 ]] || { echo 'Dev APK publication requires the non-root deployment user'; exit 1; }
source_sha=$1
fingerprint=$2
run_id=$3
ops_sha=$4
cd /opt/heartbeat-checker/backend
exec 9>.deploy.lock
flock -w 1800 9 || { echo 'Backend deployment lock timeout'; exit 1; }
incoming="$PWD/android-dev-private/incoming"
stage="$incoming/$source_sha-$run_id"
private_root="$PWD/android-dev-private/payload"
[[ -d $incoming && ! -L $incoming && -d $stage && ! -L $stage && -d $private_root && ! -L $private_root ]] || { echo 'Dev APK private storage is unavailable'; exit 1; }
for name in Heartbeat-Checker-dev.apk release.json hc-ops; do
  [[ -f $stage/$name && ! -L $stage/$name ]] || { echo 'Incomplete dev candidate stage'; exit 1; }
done
[[ $(sha256sum "$stage/hc-ops" | awk '{print $1}') == "$ops_sha" ]] || { echo 'Dev operations binary checksum mismatch'; exit 1; }
chmod 700 "$stage/hc-ops"
[[ "$("$stage/hc-ops" version)" == "$source_sha" ]] || { echo 'Dev operations binary source mismatch'; exit 1; }
receipt=$("$stage/hc-ops" android-dev-publish "$stage/release.json" "$stage/Heartbeat-Checker-dev.apk" "$private_root" "$source_sha" "$fingerprint")
rm -rf -- "$stage"
printf '%s\n' "$receipt"
