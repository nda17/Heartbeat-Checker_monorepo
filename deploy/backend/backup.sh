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
  [[ $mode == report || ( ! -e pending && ! -L pending ) ]] || { echo 'Resolve pending backend deployment before backup' >&2; exit 1; }
fi
for file in .env .backup.env "$release_env" "$backup_compose"; do
  [[ -f $file && ! -L $file ]] || { echo "Missing regular backup configuration: $file" >&2; exit 1; }
done
for file in .env .backup.env; do
  [[ $(stat -c %a "$file") == 600 ]] || { echo "$file must be mode 600" >&2; exit 1; }
done
# Public release metadata is parsed without sourcing any secret environment.
source_sha=$(awk -F= '$1 == "SOURCE_SHA" {print $2}' "$release_env")
[[ $source_sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid installed backup source SHA' >&2; exit 1; }
if [[ $mode == preflight ]]; then
  ops=${HC_OPS_PATH:-$PWD/hc-ops.incoming}
  helper_sha=${HC_OPS_SHA256:-}
else
  ops="$PWD/hc-ops"
  helper_checksum="$PWD/hc-ops.sha256"
  # A failed Go-to-Go deployment may already have installed the next release
  # metadata while preserving the previous host helper. Reporting may use only
  # the exact next helper retained in that deployment's private recovery bundle.
  if [[ $mode == report && ( -e pending || -L pending ) ]]; then
    [[ -f pending && ! -L pending && $(stat -c %a pending) == 600 && $(stat -c %u pending) == "$(id -u)" ]] || {
      echo 'Invalid pending backup report metadata' >&2; exit 1;
    }
    pending_source=$(awk -F= '$1 == "source_sha" {print $2}' pending)
    pending_phase=$(awk -F= '$1 == "phase" {print $2}' pending)
    case "$pending_phase" in
      pre_migration|migration_started|migration_succeeded)
        recovery_dir=$(awk -F= '$1 == "recovery" {print $2}' pending)
        [[ $pending_source == "$source_sha" && $recovery_dir =~ ^recovery/${source_sha}-[0-9]{8}T[0-9]{6}Z$ && -d recovery && ! -L recovery && -d $recovery_dir && ! -L $recovery_dir && $(stat -c %a "$recovery_dir") == 700 && $(stat -c %u "$recovery_dir") == "$(id -u)" ]] || {
          echo 'Pending recovery helper does not match the active source' >&2; exit 1;
        }
        ops="$PWD/$recovery_dir/hc-ops.next"
        helper_checksum="$PWD/$recovery_dir/hc-ops.next.sha256"
        ;;
      env_rollback_failed)
        # No next-source migration bundle exists for this failure phase. Only
        # the installed helper matching the restored release may be used.
        [[ $pending_source =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid pending source' >&2; exit 1; }
        ;;
      *) echo 'Unrecognized pending backup report phase' >&2; exit 1 ;;
    esac
  fi
  [[ -f $helper_checksum && ! -L $helper_checksum && $(stat -c %a "$helper_checksum") == 600 && $(stat -c %u "$helper_checksum") == "$(id -u)" ]] || {
    echo 'Missing installed helper checksum' >&2; exit 1;
  }
  helper_sha=$(cat "$helper_checksum")
fi
[[ $helper_sha =~ ^[0-9a-f]{64}$ && -f $ops && ! -L $ops && -x $ops && $(stat -c %u "$ops") == "$(id -u)" ]] || {
  echo 'Missing pinned backup configuration helper' >&2; exit 1;
}
[[ $(sha256sum "$ops" | awk '{print $1}') == "$helper_sha" && $("$ops" version) == "$source_sha" ]] || {
  echo 'Backup helper checksum/source mismatch' >&2; exit 1;
}
"$ops" deploy-json backup-env-check .backup.env
compose=(docker compose --env-file .env --env-file .backup.env --env-file "$release_env" -f "$backup_compose")
"${compose[@]}" config -q
if [[ $mode == preflight ]]; then
  exit 0
fi
image=$("${compose[@]}" config --format json | "$ops" compose-field backup.image)
[[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/backup@sha256:[0-9a-f]{64}$ ]] || { echo 'Immutable backup image required' >&2; exit 1; }
composed_source=$("${compose[@]}" config --format json | "$ops" compose-field backup.source)
[[ $composed_source == "$source_sha" ]] || { echo 'Backup Compose source mismatch' >&2; exit 1; }
[[ $source_sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid installed backup source SHA' >&2; exit 1; }
[[ $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image") == "$source_sha" ]] || {
  echo 'Installed backup image revision mismatch' >&2; exit 1;
}
# No pull/build here: CI installs an exact, green release and checks its OCI revision.
"${compose[@]}" run --rm --no-deps --pull never backup "$mode"
