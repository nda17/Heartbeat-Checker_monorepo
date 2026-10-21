#!/usr/bin/env bash
set -euo pipefail

cd /opt/heartbeat-checker/backend

source_sha=${1:?source SHA is required}
api_image=${2:?API image digest is required}
worker_image=${3:?worker image digest is required}
probe_image=${4:?probe image digest is required}
migrate_image=${5:?migration image digest is required}
registry_user=${6:?registry user is required}
backup_image=${7:?backup image digest is required}
activate_telegram_relay=${8-false}
activate_messenger_notifications=${9-false}
provision_owner=${10-false}
activate_admin_mfa=${11-false}
activate_oauth=${12-false}
firewall_image=${13:?firewall image digest is required}
firewall_policy_sha=${14:?firewall policy hash is required}
activate_yookassa_test=${15-false}
activate_email_notifications=${16-false}
activate_android_push=${17-false}
activate_yookassa_production=${18-false}
activate_native_sdk_test=${19-false}

[[ ( $# -eq 14 || $# -eq 15 || $# -eq 16 || $# -eq 17 || $# -eq 18 || $# -eq 19 ) && ( $activate_native_sdk_test == true || $activate_native_sdk_test == false ) && ( $activate_yookassa_production == true || $activate_yookassa_production == false ) && ( $activate_android_push == true || $activate_android_push == false ) && ( $activate_email_notifications == true || $activate_email_notifications == false ) && ( $activate_yookassa_test == true || $activate_yookassa_test == false ) && $firewall_policy_sha =~ ^[0-9a-f]{64}$ && $source_sha =~ ^[0-9a-f]{40}$ && $registry_user =~ ^[a-zA-Z0-9-]+$ && ( $activate_telegram_relay == true || $activate_telegram_relay == false ) && ( $activate_messenger_notifications == true || $activate_messenger_notifications == false ) && ( $provision_owner == true || $provision_owner == false ) && ( $activate_admin_mfa == true || $activate_admin_mfa == false ) && ( $activate_oauth == true || $activate_oauth == false ) ]] || {
  echo 'Invalid release arguments' >&2; exit 1;
}
for spec in "api:$api_image" "worker:$worker_image" "probe:$probe_image" "migrate:$migrate_image" "backup:$backup_image" "firewall:$firewall_image"; do
  target=${spec%%:*}
  image=${spec#*:}
  [[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/$target@sha256:[0-9a-f]{64}$ ]] || {
    echo "Expected immutable GHCR $target image" >&2; exit 1;
  }
done
exec 9>.deploy.lock
flock -n 9 || { echo 'Another backend deployment is in progress' >&2; exit 1; }

[[ -f .env && ! -L .env && $(stat -c %a .env) == 600 ]] || {
  echo 'Provision a regular mode-600 .env first' >&2; exit 1;
}
[[ -s compose.yaml.incoming && -s Caddyfile.incoming && -s compose.backup.yaml.incoming && -s backup.sh.incoming ]] || {
  echo 'Missing staged deployment files' >&2; exit 1;
}
[[ ! -e pending ]] || { echo 'Resolve the previous pending deployment before retrying' >&2; exit 1; }

workdir=$(mktemp -d)
export DOCKER_CONFIG=$workdir/docker-config
mkdir -m 700 "$DOCKER_CONFIG"
umask 077
mkdir -p backups receipts

had_previous=false
if [[ -e .release.env || -e compose.yaml || -e Caddyfile ]]; then
  for file in .release.env compose.yaml Caddyfile; do
    [[ -f $file && ! -L $file ]] || { echo "Incomplete existing deployment: $file" >&2; exit 1; }
    cp -p "$file" "$workdir/$file"
  done
  had_previous=true
fi

release_installed=false
migration_started=false
scratch_created=false
recovery_dir=
activation_applied=false
activation_generated=false
activation_stage_created=false
owner_import_result=skipped
on_exit() {
  local status=$?
  local rollback_failed=false retained_dir
  trap - EXIT
  set +e
  if $scratch_created; then
    docker compose --env-file .env --env-file .release.env exec -T postgres \
      dropdb -U heartbeat heartbeat_restorecheck >/dev/null 2>&1 ||
      printf 'CRITICAL: remove heartbeat_restorecheck manually\n' >&2
  fi
  if ((status != 0)); then
    rm -f -- "receipts/$source_sha.json.incoming"
    rm -f -- receipts/owner-import.json.incoming
    if $migration_started; then
      printf 'Backend release failed after migration began; keep database, backup and pending journal for forward recovery\n' >&2
    else
      env_restored=true
      if $activation_applied; then
        if ! cp -p "$workdir/.env" .env.rollback.incoming || ! mv -f .env.rollback.incoming .env; then
          env_restored=false
          rollback_failed=true
          printf 'CRITICAL: previous backend .env could not be restored\n' >&2
        fi
      fi
      if $release_installed && $had_previous; then
        for file in .release.env compose.yaml Caddyfile; do cp -p "$workdir/$file" "$file"; done
        if $env_restored; then
          if ! docker compose --env-file .env --env-file .release.env up -d --pull never --remove-orphans; then
            printf 'CRITICAL: previous backend could not be restarted\n' >&2
          fi
        fi
      fi
    fi
    if $rollback_failed; then
      # Keep only the protected recovery inputs; the registry token is transient.
      rm -rf -- "$workdir/docker-config"
      rm -f -- "$workdir/.env.next"
      rm -f -- "$workdir/.owner-import.json"
      mkdir -p recovery
      retained_dir="recovery/${source_sha}-env-rollback-failed-$(date -u +%Y%m%dT%H%M%SZ)-$$"
      if mv -- "$workdir" "$retained_dir"; then workdir=$retained_dir; fi
      if [[ -e pending ]]; then
        printf 'env_rollback_failed=1\nenv_snapshot=%s/.env\n' "$workdir" >> pending
      else
        printf 'source_sha=%s\nphase=env_rollback_failed\nenv_snapshot=%s/.env\n' \
          "$source_sha" "$workdir" > pending.incoming && mv pending.incoming pending
      fi
      printf 'CRITICAL: retained previous backend configuration at %s; resolve pending before retrying\n' "$workdir" >&2
    else
      if ! $migration_started; then rm -f pending; fi
      if ! $migration_started && [[ -n $recovery_dir ]]; then rm -rf -- "$recovery_dir"; fi
    fi
  fi
  if $activation_stage_created; then rm -f .env.activate.incoming .env.rollback.incoming; fi
  if ! $rollback_failed; then rm -rf -- "$workdir"; fi
  exit "$status"
}
trap on_exit EXIT

# Only a runner-built, checksum-transported binary from this exact source may
# process deployment inputs. Never execute a mutable incoming path after copying.
[[ ${HC_OPS_SHA256:-} =~ ^[0-9a-f]{64}$ && -f hc-ops.incoming && ! -L hc-ops.incoming ]] || {
  echo 'Missing source-pinned deployment helper' >&2; exit 1;
}
[[ $(sha256sum hc-ops.incoming | awk '{print $1}') == "$HC_OPS_SHA256" ]] || {
  echo 'Deployment helper transport checksum mismatch' >&2; exit 1;
}
ops="$workdir/hc-ops"
cp -p hc-ops.incoming "$ops"
chmod 700 "$ops"
[[ $(sha256sum "$ops" | awk '{print $1}') == "$HC_OPS_SHA256" && $("$ops" version) == "$source_sha" ]] || {
  echo 'Deployment helper source/checksum mismatch' >&2; exit 1;
}
for helper_stage in hc-ops.installing hc-ops.sha256.installing; do
  [[ ! -e $helper_stage && ! -L $helper_stage ]] || { echo 'Resolve staged helper installation first' >&2; exit 1; }
done
had_previous_helper=false
if [[ -e hc-ops || -L hc-ops || -e hc-ops.sha256 || -L hc-ops.sha256 ]]; then
  [[ -f hc-ops && ! -L hc-ops && -f hc-ops.sha256 && ! -L hc-ops.sha256 && $(stat -c %a hc-ops.sha256) == 600 ]] || {
    echo 'Incomplete installed deployment helper' >&2; exit 1;
  }
  cp -p hc-ops "$workdir/hc-ops.previous"
  cp -p hc-ops.sha256 "$workdir/hc-ops.previous.sha256"
  previous_helper_sha=$(cat "$workdir/hc-ops.previous.sha256")
  previous_helper_source=$(awk -F= '$1 == "SOURCE_SHA" {print $2}' "$workdir/.release.env")
  [[ $previous_helper_sha =~ ^[0-9a-f]{64}$ && $previous_helper_source =~ ^[0-9a-f]{40}$ && $(sha256sum "$workdir/hc-ops.previous" | awk '{print $1}') == "$previous_helper_sha" ]] || {
    echo 'Installed deployment helper checksum mismatch' >&2; exit 1;
  }
  chmod 700 "$workdir/hc-ops.previous"
  [[ $("$workdir/hc-ops.previous" version) == "$previous_helper_source" ]] || {
    echo 'Installed deployment helper source mismatch' >&2; exit 1;
  }
  had_previous_helper=true
fi

# Framed stdin: one registry-token line, followed only by an optional bounded
# YooKassa JSON payload. The token is never exported or passed as an argument.
unset registry_token
if ! IFS= read -r -n 4097 registry_token || [[ -z $registry_token || ${#registry_token} -gt 4096 || ! $registry_token =~ ^[a-zA-Z0-9_.-]+$ ]]; then
  echo 'Invalid registry credential framing' >&2; exit 1
fi
if $activate_android_push; then
  if $activate_telegram_relay || $activate_messenger_notifications || $activate_admin_mfa || $activate_oauth || $provision_owner || $activate_email_notifications || $activate_yookassa_test || $activate_yookassa_production || $activate_native_sdk_test; then
    echo 'Android push activation must be a separate configuration operation' >&2; exit 1
  fi
  [[ ! -e .env.next && ! -L .env.next ]] || { echo 'Resolve the existing staged configuration before Android push activation' >&2; exit 1; }
  "$ops" stage-push "$workdir"
  activation_generated=true
elif $activate_yookassa_test || $activate_yookassa_production || $activate_native_sdk_test; then
  if $activate_telegram_relay || $activate_messenger_notifications || $activate_admin_mfa || $activate_oauth || $provision_owner || $activate_email_notifications || $activate_android_push || ( $activate_yookassa_test && $activate_yookassa_production ) || ( $activate_native_sdk_test && ( $activate_yookassa_test || $activate_yookassa_production ) ); then
    echo 'YooKassa activation must be a separate configuration operation' >&2; exit 1
  fi
  [[ ! -e .env.next && ! -L .env.next ]] || { echo 'Resolve the existing staged configuration before YooKassa activation' >&2; exit 1; }
  billing_mode='test'
  if $activate_yookassa_production; then billing_mode='production'; fi
  if $activate_native_sdk_test; then billing_mode='native-test'; fi
  "$ops" stage-yookassa "$workdir" "$billing_mode"
  activation_generated=true
else
  # Legacy deployment accepts only the original registry token, never a payload.
  if ! "$ops" deploy-json stdin-empty; then
    echo 'Unexpected deployment input' >&2; exit 1
  fi
fi

# This activation changes one non-secret flag under the deployment lock. SMTP,
# encryption keys and every other byte remain the currently active values.
if $activate_email_notifications; then
  if $activate_telegram_relay || $activate_messenger_notifications || $activate_admin_mfa || $activate_oauth || $provision_owner || $activate_yookassa_test || $activate_yookassa_production || $activate_native_sdk_test; then
    echo 'Email activation must be a separate configuration operation' >&2; exit 1
  fi
  [[ ! -e .env.next && ! -L .env.next ]] || { echo 'Resolve the existing staged configuration before email activation' >&2; exit 1; }
  "$ops" deploy-json email-enable "$workdir/.env.next"
  activation_generated=true
fi

if $provision_owner; then
  [[ -f .owner-import.next.json && ! -L .owner-import.next.json && $(stat -c %a .owner-import.next.json) == 600 && -f import-owner.sql.incoming && ! -L import-owner.sql.incoming ]] || {
    echo 'Owner import requires a regular mode-600 payload and tracked SQL' >&2; exit 1;
  }
  cp -p .owner-import.next.json "$workdir/.owner-import.json"
  cp -p import-owner.sql.incoming "$workdir/import-owner.sql"
  "$ops" deploy-json owner-validate "$workdir/.owner-import.json"
fi

if $activate_telegram_relay || $activate_messenger_notifications || $activate_admin_mfa || $activate_oauth || $activate_yookassa_test || $activate_yookassa_production || $activate_native_sdk_test || $activate_email_notifications || $activate_android_push; then
  if ! $activation_generated; then
    [[ -f .env.next && ! -L .env.next && $(stat -c %a .env.next) == 600 ]] || {
      if $activate_messenger_notifications; then
        echo 'Configuration activation requires a regular mode-600 .env.next' >&2
      else
        echo 'Relay activation requires a regular mode-600 .env.next' >&2
      fi
      exit 1
    }
    cp -p .env.next "$workdir/.env.next"
  fi
  [[ ! -e .env.activate.incoming && ! -L .env.activate.incoming && ! -e .env.rollback.incoming && ! -L .env.rollback.incoming ]] || {
    echo 'Resolve the previous configuration activation staging file' >&2; exit 1;
  }
  "$ops" deploy-json activation-check "$workdir/.env.next" "$activate_telegram_relay" "$activate_messenger_notifications" "$activate_admin_mfa" "$activate_oauth" "$activate_yookassa_test" "$activate_email_notifications" "$activate_android_push" "$activate_yookassa_production" "$activate_native_sdk_test"
  cp -p .env "$workdir/.env"
  activation_stage_created=true
  cp -p "$workdir/.env.next" .env.activate.incoming
  activation_applied=true
  mv -f .env.activate.incoming .env
fi

# Only the first stdin frame belongs to the registry client.
printf '%s\n' "$registry_token" | docker login ghcr.io -u "$registry_user" --password-stdin >/dev/null
unset registry_token

cat > .release.env.incoming <<EOF
API_IMAGE=$api_image
WORKER_IMAGE=$worker_image
PROBE_IMAGE=$probe_image
MIGRATE_IMAGE=$migrate_image
BACKUP_IMAGE=$backup_image
SOURCE_SHA=$source_sha
FIREWALL_IMAGE=$firewall_image
FIREWALL_POLICY_SHA256=$firewall_policy_sha
EOF
chmod 600 .release.env.incoming
HC_OPS_PATH="$ops" bash backup.sh.incoming preflight
compose_new=(docker compose --env-file .env --env-file .release.env.incoming -f compose.yaml.incoming)
"${compose_new[@]}" config -q
"${compose_new[@]}" config --format json | "$ops" compose-check
"${compose_new[@]}" pull postgres caddy api worker probe migrate
docker pull "$backup_image"
docker pull "$firewall_image"

for spec in "api:$api_image" "worker:$worker_image" "probe:$probe_image" "migrate:$migrate_image" "backup:$backup_image" "firewall:$firewall_image"; do
  image=${spec#*:}
  [[ $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image") == "$source_sha" ]] || {
    echo "Image revision mismatch: ${spec%%:*}" >&2; exit 1;
  }
done

# Install the reviewed network policy before any migration or new process can
# run. The helper has no Docker socket or general host filesystem access.
installed_policy_sha=$(docker run --rm --network host --read-only --cap-drop ALL \
  --cap-add NET_ADMIN --security-opt no-new-privileges:true --user 0:0 \
  --mount type=bind,source=/usr/local/sbin,target=/host-sbin \
  --tmpfs /run:rw,noexec,nosuid,size=1m "$firewall_image")
[[ $installed_policy_sha == "$firewall_policy_sha" ]] || { echo 'Firewall policy receipt mismatch' >&2; exit 1; }

mv .release.env.incoming .release.env
mv compose.yaml.incoming compose.yaml
mv Caddyfile.incoming Caddyfile
release_installed=true
compose=(docker compose --env-file .env --env-file .release.env)
"${compose[@]}" config -q
"${compose[@]}" up -d --pull never postgres
db_id=$("${compose[@]}" ps -q postgres)
[[ -n $db_id ]] || { echo 'PostgreSQL container missing' >&2; exit 1; }
for ((attempt = 1; attempt <= 30; attempt++)); do
  [[ $(docker inspect --format '{{.State.Health.Status}}' "$db_id") == healthy ]] && break
  sleep 2
done
[[ $(docker inspect --format '{{.State.Health.Status}}' "$db_id") == healthy ]] || {
  echo 'PostgreSQL did not become healthy' >&2; exit 1;
}
"${compose[@]}" exec -T postgres psql -U heartbeat -d heartbeat -Atqc 'SELECT 1' | grep -Fxq 1

# Stop writers before taking the recovery snapshot. On a pre-migration failure
# the EXIT trap restores the previous release and restarts its processes.
"${compose[@]}" stop -t 30 api worker probe
if $activate_yookassa_production || $activate_yookassa_test; then
  # Writers are stopped. A shop switch is safe only when no payment operation
  # or untrusted webhook hint can still need the previous shop's credentials.
  switch_blockers=$("${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U heartbeat -d heartbeat -Atqc \
    "SELECT (SELECT count(*) FROM billing.orders WHERE status='pending' OR (provider='yookassa' AND (reconciliation_status<>'complete' OR fulfillment_status='pending' OR reconcile_token IS NOT NULL OR reconcile_lease_until IS NOT NULL OR last_error_code='payment_create_uncertain'))) + (SELECT count(*) FROM billing.webhook_inbox WHERE processed_at IS NULL)")
  [[ $switch_blockers =~ ^[0-9]+$ && $switch_blockers == 0 ]] || {
    echo 'YooKassa shop switch blocked by unfinished payment evidence' >&2; exit 1;
  }
fi
backup="backups/${source_sha}-$(date -u +%Y%m%dT%H%M%SZ).dump"
"${compose[@]}" exec -T postgres pg_dump -U heartbeat -d heartbeat -Fc > "$backup.partial"
[[ -s $backup.partial ]] || { echo 'Database backup is empty' >&2; exit 1; }
mv "$backup.partial" "$backup"
backup_sha=$(sha256sum "$backup" | awk '{print $1}')
"${compose[@]}" exec -T postgres createdb -U heartbeat heartbeat_restorecheck
scratch_created=true
"${compose[@]}" exec -T postgres pg_restore -U heartbeat -d heartbeat_restorecheck \
  --exit-on-error --no-owner < "$backup"
"${compose[@]}" exec -T postgres psql -U heartbeat -d heartbeat_restorecheck -Atqc 'SELECT 1' | grep -Fxq 1
"${compose[@]}" exec -T postgres dropdb -U heartbeat heartbeat_restorecheck
scratch_created=false
mkdir -p recovery
candidate_recovery="recovery/${source_sha}-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 700 "$candidate_recovery"
recovery_dir=$candidate_recovery
# Keep both the previous helper and the exact next helper for forward recovery.
# Legacy releases legitimately have no previous helper.
if $had_previous_helper; then
  cp -p "$workdir/hc-ops.previous" "$recovery_dir/hc-ops"
  cp -p "$workdir/hc-ops.previous.sha256" "$recovery_dir/hc-ops.sha256"
fi
cp -p "$ops" "$recovery_dir/hc-ops.next"
printf '%s\n' "$HC_OPS_SHA256" > "$recovery_dir/hc-ops.next.sha256"
if $activation_applied; then
  cp -p "$workdir/.env" "$recovery_dir/.env"
else
  cp -p .env "$recovery_dir/.env"
fi
if $had_previous; then
  for file in .release.env compose.yaml Caddyfile; do cp -p "$workdir/$file" "$recovery_dir/$file"; done
else
  for file in .release.env compose.yaml Caddyfile; do cp -p "$file" "$recovery_dir/$file"; done
fi
printf 'source_sha=%s\nbackup=%s\nbackup_sha256=%s\nrecovery=%s\nphase=pre_migration\n' \
  "$source_sha" "$backup" "$backup_sha" "$recovery_dir" > pending

printf 'source_sha=%s\nbackup=%s\nbackup_sha256=%s\nrecovery=%s\nphase=migration_started\n' \
  "$source_sha" "$backup" "$backup_sha" "$recovery_dir" > pending
migration_started=true
"${compose[@]}" --profile migration run --rm --no-deps migrate
printf 'source_sha=%s\nbackup=%s\nbackup_sha256=%s\nrecovery=%s\nphase=migration_succeeded\n' \
  "$source_sha" "$backup" "$backup_sha" "$recovery_dir" > pending

if $provision_owner; then
  # The JSON remains in a private snapshot. Only hex data travels over stdin;
  # psql output and diagnostics stay in the protected workdir on failure.
  if ! "$ops" deploy-json owner-sql "$workdir/.owner-import.json" "$workdir/import-owner.sql" "$source_sha" \
      | "${compose[@]}" exec -T postgres psql -X -q -At -v ON_ERROR_STOP=1 -U heartbeat -d heartbeat \
          > "$workdir/owner-import.stdout" 2> "$workdir/owner-import.stderr"; then
    echo 'Owner import transaction failed; retain pending and protected payload' >&2; exit 1
  fi
  owner_import_result=$(tr -d '\r\n' < "$workdir/owner-import.stdout")
  [[ $owner_import_result =~ ^(created|noop)\|[0-9a-f]{40}$ ]] || {
    echo 'Owner import transaction returned an invalid result' >&2; exit 1;
  }
  owner_payload_sha=$(sha256sum "$workdir/.owner-import.json" | awk '{print $1}')
  jq -n --arg operation_id owner-production-2026-10-19-v1 \
    --arg account_id 51ec7d19-80c4-4fb6-8edb-79dc7abe3120 \
    --arg payload_sha256 "$owner_payload_sha" --arg source_sha "${owner_import_result#*|}" \
    '{operation_id:$operation_id,account_id:$account_id,payload_sha256:$payload_sha256,source_sha:$source_sha,completed_at:(now|todate)}' \
    > receipts/owner-import.json.incoming
  mv receipts/owner-import.json.incoming receipts/owner-import.json
fi

"${compose[@]}" up -d --pull never --force-recreate --remove-orphans api probe worker caddy
for service in api probe worker caddy; do
  container=$("${compose[@]}" ps -q "$service")
  [[ -n $container && $(docker inspect --format '{{.State.Running}}' "$container") == true ]] || {
    echo "$service is not running" >&2; exit 1;
  }
done

ready=false
for ((attempt = 1; attempt <= 30; attempt++)); do
  if curl -fsS --max-time 3 -o /dev/null http://172.30.64.3:8080/readyz; then ready=true; break; fi
  sleep 2
done
$ready || { echo 'API readiness failed' >&2; exit 1; }
curl -fsS --max-time 3 -o /dev/null http://172.30.80.2:8090/readyz || {
  echo 'Probe readiness failed' >&2; exit 1;
}
"${compose[@]}" config --format json | "$ops" deploy-json probe-smoke
sleep 5
worker_id=$("${compose[@]}" ps -q worker)
[[ -n $worker_id && $(docker inspect --format '{{.State.Running}}' "$worker_id") == true ]] || {
  echo 'Worker exited during startup' >&2; exit 1;
}

# The backend edge rejects this local host request. The frontend VPS makes the
# authorized 401 check via its own rollout preflight.
edge_ready=false
for ((attempt = 1; attempt <= 24; attempt++)); do
  if status=$(curl -sS --max-time 5 --resolve api.heartbeat-checker.ru:443:127.0.0.1 \
    -o /dev/null -w '%{http_code}' https://api.heartbeat-checker.ru/api/v1/me 2>/dev/null); then
    if [[ $status == 403 ]]; then edge_ready=true; break; fi
  fi
  sleep 5
done
$edge_ready || { echo 'Backend HTTPS edge did not return 403 within 120 seconds' >&2; exit 1; }

ledger=$("${compose[@]}" exec -T postgres psql -U heartbeat -d heartbeat -Atqc \
  "SELECT count(*) FROM public.schema_migrations")
[[ $ledger =~ ^[1-9][0-9]*$ ]] || { echo 'Migration ledger is empty' >&2; exit 1; }

jq -n --arg source_sha "$source_sha" --arg api "$api_image" \
  --arg worker "$worker_image" --arg probe "$probe_image" --arg migrate "$migrate_image" --arg backup_image "$backup_image" \
  --arg backup "$backup" --arg backup_sha256 "$backup_sha" --argjson migrations "$ledger" \
  --arg firewall_image "$firewall_image" --arg firewall_policy_sha256 "$firewall_policy_sha" \
  --argjson telegram_relay_activated "$activate_telegram_relay" \
  --argjson messenger_notifications_activated "$activate_messenger_notifications" \
  --argjson email_notifications_activated "$activate_email_notifications" \
  --argjson android_push_activated "$activate_android_push" \
  --argjson owner_import_requested "$provision_owner" --argjson admin_mfa_activated "$activate_admin_mfa" \
  --argjson oauth_activated "$activate_oauth" --argjson yookassa_test_activated "$activate_yookassa_test" \
  --argjson yookassa_production_activated "$activate_yookassa_production" \
  --argjson native_sdk_test_activated "$activate_native_sdk_test" \
  '{source_sha:$source_sha, images:{api:$api,worker:$worker,probe:$probe,migrate:$migrate,backup:$backup_image,firewall:$firewall_image},firewall_policy_sha256:$firewall_policy_sha256,backup:$backup,backup_sha256:$backup_sha256,migrations:$migrations,telegram_relay_activated:$telegram_relay_activated,messenger_notifications_activated:$messenger_notifications_activated,email_notifications_activated:$email_notifications_activated,android_push_activated:$android_push_activated,owner_import_requested:$owner_import_requested,admin_mfa_activated:$admin_mfa_activated,oauth_activated:$oauth_activated,yookassa_test_activated:$yookassa_test_activated,yookassa_production_activated:$yookassa_production_activated,native_sdk_test_activated:$native_sdk_test_activated,completed_at:(now|todate)}' \
  > "receipts/$source_sha.json.incoming"
# Install helpers only after migration/readiness, so every pre-migration rollback
# leaves the prior helper (or its legitimate absence) unchanged.
cp -p "$ops" hc-ops.installing
printf '%s\n' "$HC_OPS_SHA256" > hc-ops.sha256.installing
chmod 700 hc-ops.installing
chmod 600 hc-ops.sha256.installing
mv hc-ops.installing hc-ops
mv hc-ops.sha256.installing hc-ops.sha256
mv compose.backup.yaml.incoming compose.backup.yaml
mv backup.sh.incoming backup.sh
chmod 700 backup.sh
if $activation_applied; then
  if $activation_generated; then
    if [[ ! -f .env || -L .env || $(stat -c %a .env) != 600 ]] || ! cmp -s .env "$workdir/.env.next"; then
      echo 'Activated private candidate changed during deployment; retain pending for review' >&2; exit 1
    fi
  else
    if [[ ! -f .env.next || -L .env.next || $(stat -c %a .env.next) != 600 ]] || ! cmp -s .env.next "$workdir/.env.next"; then
      echo 'Staged activation env changed during deployment; retain pending for review' >&2; exit 1;
    fi
    rm -f .env.next
  fi
fi
if $provision_owner; then
  if [[ ! -f .owner-import.next.json || -L .owner-import.next.json || $(stat -c %a .owner-import.next.json) != 600 ]] \
      || ! cmp -s .owner-import.next.json "$workdir/.owner-import.json" \
      || ! cmp -s import-owner.sql.incoming "$workdir/import-owner.sql"; then
    echo 'Staged owner import input changed during deployment; retain pending for review' >&2; exit 1;
  fi
  rm -f .owner-import.next.json import-owner.sql.incoming
fi
mv "receipts/$source_sha.json.incoming" "receipts/$source_sha.json"
rm -f pending
printf 'Backend deployed: %s\n' "$source_sha"
