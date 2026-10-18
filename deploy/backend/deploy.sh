#!/usr/bin/env bash
set -euo pipefail

cd /opt/heartbeat-checker/backend

source_sha=${1:?source SHA is required}
api_image=${2:?API image digest is required}
worker_image=${3:?worker image digest is required}
probe_image=${4:?probe image digest is required}
migrate_image=${5:?migration image digest is required}
registry_user=${6:?registry user is required}

[[ $# -eq 6 && $source_sha =~ ^[0-9a-f]{40}$ && $registry_user =~ ^[a-zA-Z0-9-]+$ ]] || {
  echo 'Invalid release arguments' >&2; exit 1;
}
for spec in "api:$api_image" "worker:$worker_image" "probe:$probe_image" "migrate:$migrate_image"; do
  target=${spec%%:*}
  image=${spec#*:}
  [[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/$target@sha256:[0-9a-f]{64}$ ]] || {
    echo "Expected immutable GHCR $target image" >&2; exit 1;
  }
done
[[ -f .env && ! -L .env && $(stat -c %a .env) == 600 ]] || {
  echo 'Provision a regular mode-600 .env first' >&2; exit 1;
}
[[ -s compose.yaml.incoming && -s Caddyfile.incoming ]] || {
  echo 'Missing staged deployment files' >&2; exit 1;
}
[[ ! -e pending ]] || { echo 'Resolve the previous pending deployment before retrying' >&2; exit 1; }

exec 9>.deploy.lock
flock -n 9 || { echo 'Another backend deployment is in progress' >&2; exit 1; }

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
on_exit() {
  local status=$?
  trap - EXIT
  if $scratch_created; then
    docker compose --env-file .env --env-file .release.env exec -T postgres \
      dropdb -U heartbeat heartbeat_restorecheck >/dev/null 2>&1 ||
      printf 'CRITICAL: remove heartbeat_restorecheck manually\n' >&2
  fi
  if ((status != 0)); then
    if $migration_started; then
      printf 'Backend release failed after migration began; keep database, backup and pending journal for forward recovery\n' >&2
    elif $release_installed && $had_previous; then
      for file in .release.env compose.yaml Caddyfile; do cp -p "$workdir/$file" "$file"; done
      if ! docker compose --env-file .env --env-file .release.env up -d --pull never --remove-orphans; then
        printf 'CRITICAL: previous backend could not be restarted\n' >&2
      fi
    fi
    if ! $migration_started; then rm -f pending; fi
    if ! $migration_started && [[ -n $recovery_dir ]]; then rm -rf -- "$recovery_dir"; fi
  fi
  rm -rf -- "$workdir"
  exit "$status"
}
trap on_exit EXIT

# The workflow passes its short-lived package token on stdin.
docker login ghcr.io -u "$registry_user" --password-stdin >/dev/null

cat > .release.env.incoming <<EOF
API_IMAGE=$api_image
WORKER_IMAGE=$worker_image
PROBE_IMAGE=$probe_image
MIGRATE_IMAGE=$migrate_image
EOF
chmod 600 .release.env.incoming
compose_new=(docker compose --env-file .env --env-file .release.env.incoming -f compose.yaml.incoming)
"${compose_new[@]}" config -q
"${compose_new[@]}" config --format json | python3 -c '
import json, sys
from urllib.parse import urlparse, parse_qs
c = json.load(sys.stdin)["services"]
u = urlparse(c["api"]["environment"]["DATABASE_URL"])
p = c["postgres"]["environment"]["POSTGRES_PASSWORD"]
if (u.scheme not in ("postgres", "postgresql") or u.hostname != "postgres" or
    u.port != 5432 or u.username != "heartbeat" or u.password != p or
    u.path != "/heartbeat" or parse_qs(u.query) != {"sslmode": ["disable"]}):
    raise SystemExit("DATABASE_URL must target this Compose PostgreSQL with matching credentials")
if c["worker"]["environment"]["SMTP_TLS_MODE"] not in ("implicit_tls", "starttls"):
    raise SystemExit("SMTP_TLS_MODE must use verified TLS")
'
"${compose_new[@]}" pull postgres caddy api worker probe migrate

for spec in "api:$api_image" "worker:$worker_image" "probe:$probe_image" "migrate:$migrate_image"; do
  image=${spec#*:}
  [[ $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image") == "$source_sha" ]] || {
    echo "Image revision mismatch: ${spec%%:*}" >&2; exit 1;
  }
done

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
cp -p .env "$recovery_dir/.env"
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
"${compose[@]}" config --format json | python3 -c '
import json, sys, urllib.error, urllib.request
c = json.load(sys.stdin)["services"]
secret = c["probe"]["environment"]["PROBE_SHARED_SECRET"]
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
endpoint = "http://172.30.80.2:8090/internal/v1/check"
def check(url):
    body = json.dumps({"runId":"release-smoke", "attemptToken":"release-smoke", "configVersion":1,
                       "url":url, "method":"GET", "expectedStatus":200, "bodyContains":""}).encode()
    req = urllib.request.Request(endpoint, data=body, method="POST",
                                 headers={"Authorization":"Bearer " + secret, "Content-Type":"application/json"})
    try:
        with opener.open(req, timeout=20) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        return response.code, json.load(response)
status, result = check("https://heartbeat-checker.ru/")
if status != 200 or result.get("outcome") != "UP" or result.get("httpStatus") != 200:
    raise SystemExit("Probe could not resolve and GET the own public site")
status, result = check("http://169.254.169.254/")
if status != 400 or result.get("error") != "unsupported_target":
    raise SystemExit("Probe did not reject metadata address")
print("Probe public GET and metadata rejection passed")
'
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
  --arg worker "$worker_image" --arg probe "$probe_image" --arg migrate "$migrate_image" \
  --arg backup "$backup" --arg backup_sha256 "$backup_sha" --argjson migrations "$ledger" \
  '{source_sha:$source_sha, images:{api:$api,worker:$worker,probe:$probe,migrate:$migrate},backup:$backup,backup_sha256:$backup_sha256,migrations:$migrations,completed_at:(now|todate)}' \
  > "receipts/$source_sha.json"
rm -f pending
printf 'Backend deployed: %s\n' "$source_sha"
