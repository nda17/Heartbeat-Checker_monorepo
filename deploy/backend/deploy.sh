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

[[ $# -eq 14 && $firewall_policy_sha =~ ^[0-9a-f]{64}$ && $source_sha =~ ^[0-9a-f]{40}$ && $registry_user =~ ^[a-zA-Z0-9-]+$ && ( $activate_telegram_relay == true || $activate_telegram_relay == false ) && ( $activate_messenger_notifications == true || $activate_messenger_notifications == false ) && ( $provision_owner == true || $provision_owner == false ) && ( $activate_admin_mfa == true || $activate_admin_mfa == false ) && ( $activate_oauth == true || $activate_oauth == false ) ]] || {
  echo 'Invalid release arguments' >&2; exit 1;
}
for spec in "api:$api_image" "worker:$worker_image" "probe:$probe_image" "migrate:$migrate_image" "backup:$backup_image" "firewall:$firewall_image"; do
  target=${spec%%:*}
  image=${spec#*:}
  [[ $image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/$target@sha256:[0-9a-f]{64}$ ]] || {
    echo "Expected immutable GHCR $target image" >&2; exit 1;
  }
done
[[ -f .env && ! -L .env && $(stat -c %a .env) == 600 ]] || {
  echo 'Provision a regular mode-600 .env first' >&2; exit 1;
}
[[ -s compose.yaml.incoming && -s Caddyfile.incoming && -s compose.backup.yaml.incoming && -s backup.sh.incoming ]] || {
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
activation_applied=false
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

if $provision_owner; then
  [[ -f .owner-import.next.json && ! -L .owner-import.next.json && $(stat -c %a .owner-import.next.json) == 600 && -f import-owner.sql.incoming && ! -L import-owner.sql.incoming ]] || {
    echo 'Owner import requires a regular mode-600 payload and tracked SQL' >&2; exit 1;
  }
  cp -p .owner-import.next.json "$workdir/.owner-import.json"
  cp -p import-owner.sql.incoming "$workdir/import-owner.sql"
  python3 - "$workdir/.owner-import.json" <<'PY'
import json
from pathlib import Path
import re
import sys

def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate field")
        result[key] = value
    return result

try:
    raw = Path(sys.argv[1]).read_bytes()
    if not 1 <= len(raw) <= 4096:
        raise ValueError("payload size")
    data = json.loads(raw.decode("utf-8"), object_pairs_hook=unique)
    if set(data) != {"accountId", "name", "email", "passwordHash", "quota", "validUntil"}:
        raise ValueError("payload fields")
    if (data["accountId"] != "51ec7d19-80c4-4fb6-8edb-79dc7abe3120"
            or data["email"] != "khv1702@gmail.com"
            or type(data["quota"]) is not int or data["quota"] != 1000
            or data["validUntil"] != "2027-10-18T00:00:00Z"):
        raise ValueError("payload identity")
    name = data["name"]
    if not isinstance(name, str) or not 2 <= len(name) <= 100 or name != name.strip() or any(ord(c) < 32 or ord(c) == 127 for c in name):
        raise ValueError("payload name")
    if not isinstance(data["passwordHash"], str) or not re.fullmatch(r"\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}", data["passwordHash"]):
        raise ValueError("payload hash")
except (OSError, UnicodeError, ValueError, TypeError):
    raise SystemExit("Owner import payload validation failed")
PY
fi

if $activate_telegram_relay || $activate_messenger_notifications || $activate_admin_mfa || $activate_oauth; then
  [[ -f .env.next && ! -L .env.next && $(stat -c %a .env.next) == 600 ]] || {
    if $activate_messenger_notifications; then
      echo 'Configuration activation requires a regular mode-600 .env.next' >&2
    else
      echo 'Relay activation requires a regular mode-600 .env.next' >&2
    fi
    exit 1
  }
  [[ ! -e .env.activate.incoming && ! -L .env.activate.incoming && ! -e .env.rollback.incoming && ! -L .env.rollback.incoming ]] || {
    echo 'Resolve the previous configuration activation staging file' >&2; exit 1;
  }
  cp -p .env.next "$workdir/.env.next"
  python3 - "$workdir/.env.next" "$activate_telegram_relay" "$activate_messenger_notifications" "$activate_admin_mfa" "$activate_oauth" <<'PY'
import base64
import binascii
from pathlib import Path
import re
import sys
from urllib.parse import urlsplit

relay_targets = {
    b"TELEGRAM_API_BASE_URL": b"https://messengers-proxy.heartbeat-checker.ru/telegram-api",
    b"TELEGRAM_API_PROXY_IP": b"185.184.122.62",
}
messenger_keys = {
    b"MESSENGER_NOTIFICATIONS_ENABLED",
    b"TELEGRAM_INFORMER_WEBHOOK_SECRET",
    b"MAX_INFORMER_WEBHOOK_SECRET",
    b"TELEGRAM_BOT_INFORMER_TOKEN",
    b"MAX_BOT_INFORMER_TOKEN",
    b"TELEGRAM_INFORMER_BOT_USERNAME",
    b"MAX_INFORMER_BOT_URL",
}
relay = sys.argv[2] == "true"
messenger = sys.argv[3] == "true"
admin_mfa = sys.argv[4] == "true"
mfa_key = b"AUTH_MFA_ENCRYPTION_KEY"
oauth_keys = {
    b"YANDEX_CLIENT_ID", b"YANDEX_CLIENT_SECRET", b"YANDEX_REDIRECT_URI",
    b"YANDEX_SCOPES", b"VK_CLIENT_ID", b"VK_CALLBACK_URL",
}
oauth = sys.argv[5] == "true"
allowed = (set(relay_targets) if relay else set()) | (messenger_keys if messenger else set()) | ({mfa_key} if admin_mfa else set()) | (oauth_keys if oauth else set())
prefix = "Relay activation rejected" if relay and not messenger else "Configuration activation rejected"

def reject(message):
    raise SystemExit(f"{prefix}: {message}")

def unquote_literal(value):
    # Accept one dotenv-style outer quote pair without interpreting escapes or variables.
    if len(value) >= 2 and value[:1] in (b"'", b'"') and value[-1:] == value[:1]:
        return value[1:-1]
    return value

def parse(path):
    data = Path(path).read_bytes()
    if any((c < 32 and c != 10) or c == 127 for c in data):
        reject("invalid env content")
    ordinary = []
    values = {}
    for line in data.split(b"\n"):
        if not line.strip(b" \t") or line.lstrip(b" \t").startswith(b"#"):
            ordinary.append(line)
            continue
        match = re.fullmatch(rb"([A-Za-z_][A-Za-z0-9_]*)=(.*)", line)
        if match is None:
            reject("invalid env line")
        key, value = match.groups()
        if key in values:
            reject("duplicate env key")
        values[key] = value
        if key not in allowed:
            ordinary.append(line)
    return ordinary, values

old_lines, old_values = parse(".env")
new_lines, new_values = parse(sys.argv[1])
if old_lines != new_lines:
    reject("changes outside selected keys")
if oauth:
    for key in oauth_keys:
        previous = old_values.get(key, b"")
        if unquote_literal(previous) and new_values.get(key) != previous:
            reject("existing OAuth credential cannot be rotated")
    if not re.fullmatch(rb"[A-Za-z0-9_-]{16,128}", new_values.get(b"YANDEX_CLIENT_ID", b"")):
        reject("invalid Yandex client ID")
    secret = new_values.get(b"YANDEX_CLIENT_SECRET", b"")
    if not 16 <= len(secret) <= 512 or any(c < 33 or c > 126 or c in b"'\"$`\\#" for c in secret):
        reject("invalid Yandex client secret")
    if new_values.get(b"YANDEX_REDIRECT_URI") != b"https://heartbeat-checker.ru/api/v1/auth/oauth/yandex/callback":
        reject("invalid Yandex callback URL")
    if unquote_literal(new_values.get(b"YANDEX_SCOPES", b"")) != b"login:email login:info":
        reject("invalid Yandex scopes")
    if not re.fullmatch(rb"[0-9]{4,32}", new_values.get(b"VK_CLIENT_ID", b"")):
        reject("invalid VK client ID")
    if new_values.get(b"VK_CALLBACK_URL") != b"https://heartbeat-checker.ru/api/v1/auth/oauth/vk/callback":
        reject("invalid VK callback URL")
if relay and any(new_values.get(key) != value for key, value in relay_targets.items()):
    reject("unexpected Telegram endpoint")
if admin_mfa:
    if unquote_literal(old_values.get(mfa_key, b"")):
        reject("existing admin MFA key cannot be rotated")
    value = new_values.get(mfa_key, b"")
    try:
        if len(base64.b64decode(value, validate=True)) != 32 or base64.b64encode(base64.b64decode(value, validate=True)) != value:
            reject("invalid admin MFA key")
    except (ValueError, binascii.Error):
        reject("invalid admin MFA key")
if messenger:
    if new_values.get(b"MESSENGER_NOTIFICATIONS_ENABLED") != b"true":
        reject("messenger flag must be true")
    secrets = (b"TELEGRAM_INFORMER_WEBHOOK_SECRET", b"MAX_INFORMER_WEBHOOK_SECRET")
    for key in secrets:
        value = new_values.get(key, b"")
        if not re.fullmatch(rb"[A-Za-z0-9_-]{32,256}", value):
            reject("invalid Informer webhook secret")
        support_key = key.replace(b"INFORMER", b"SUPPORT")
        if value == unquote_literal(old_values.get(support_key, b"")):
            reject("Informer webhook secret matches Support")
    telegram_token = old_values.get(b"TELEGRAM_BOT_INFORMER_TOKEN", b"")
    if not re.fullmatch(rb"[0-9]+:[A-Za-z0-9_-]+", unquote_literal(telegram_token)) or new_values.get(b"TELEGRAM_BOT_INFORMER_TOKEN") != telegram_token:
        reject("existing Telegram Informer token must remain unchanged")
    max_token = new_values.get(b"MAX_BOT_INFORMER_TOKEN", b"")
    if not 16 <= len(max_token) <= 512 or any(c < 33 or c > 126 or c in b"'\"$`\\#" for c in max_token):
        reject("invalid MAX Informer token")
    username = new_values.get(b"TELEGRAM_INFORMER_BOT_USERNAME", b"")
    if not re.fullmatch(rb"[A-Za-z][A-Za-z0-9_]{4,31}", username) or not username.lower().endswith(b"bot"):
        reject("invalid Telegram Informer bot username")
    try:
        url = urlsplit(new_values.get(b"MAX_INFORMER_BOT_URL", b"").decode("ascii"))
    except (UnicodeDecodeError, ValueError):
        reject("invalid MAX Informer bot URL")
    if (url.scheme != "https" or url.netloc != "max.ru" or not re.fullmatch(r"/[A-Za-z0-9_-]+", url.path)
            or url.query or url.fragment or url.username or url.password):
        reject("invalid MAX Informer bot URL")
PY
  cp -p .env "$workdir/.env"
  activation_stage_created=true
  cp -p "$workdir/.env.next" .env.activate.incoming
  activation_applied=true
  mv -f .env.activate.incoming .env
fi

# The workflow passes its short-lived package token on stdin.
docker login ghcr.io -u "$registry_user" --password-stdin >/dev/null

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
bash backup.sh.incoming preflight
compose_new=(docker compose --env-file .env --env-file .release.env.incoming -f compose.yaml.incoming)
"${compose_new[@]}" config -q
"${compose_new[@]}" config --format json | python3 -c '
import base64, binascii, json, sys
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
try:
    mfa_key = c["api"]["environment"]["AUTH_MFA_ENCRYPTION_KEY"]
    if len(base64.b64decode(mfa_key, validate=True)) != 32:
        raise ValueError()
except (KeyError, ValueError, binascii.Error):
    raise SystemExit("Production admin MFA key is required")
if any("AUTH_MFA_ENCRYPTION_KEY" in c[name]["environment"] for name in ("worker", "probe")):
    raise SystemExit("Admin MFA key must be scoped to API only")
oauth_keys = ("YANDEX_CLIENT_ID", "YANDEX_CLIENT_SECRET", "YANDEX_REDIRECT_URI", "YANDEX_SCOPES", "VK_CLIENT_ID", "VK_CALLBACK_URL")
if any(key in c[name]["environment"] for key in oauth_keys for name in ("worker", "probe")):
    raise SystemExit("OAuth configuration must be scoped to API only")
'
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
  if ! python3 - "$workdir/.owner-import.json" "$workdir/import-owner.sql" "$source_sha" <<'PY' \
      | "${compose[@]}" exec -T postgres psql -X -q -At -v ON_ERROR_STOP=1 -U heartbeat -d heartbeat \
          > "$workdir/owner-import.stdout" 2> "$workdir/owner-import.stderr"; then
import hashlib
from pathlib import Path
import sys

payload = Path(sys.argv[1]).read_bytes()
template = Path(sys.argv[2]).read_bytes()
marker = b"__OWNER_PAYLOAD_ROW__"
if template.count(marker) != 1:
    raise SystemExit("Invalid tracked owner import SQL")
row = payload.hex().encode() + b"\t" + hashlib.sha256(payload).hexdigest().encode() + b"\t" + sys.argv[3].encode()
sys.stdout.buffer.write(template.replace(marker, row))
PY
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
  --arg worker "$worker_image" --arg probe "$probe_image" --arg migrate "$migrate_image" --arg backup_image "$backup_image" \
  --arg backup "$backup" --arg backup_sha256 "$backup_sha" --argjson migrations "$ledger" \
  --arg firewall_image "$firewall_image" --arg firewall_policy_sha256 "$firewall_policy_sha" \
  --argjson telegram_relay_activated "$activate_telegram_relay" \
  --argjson messenger_notifications_activated "$activate_messenger_notifications" \
  --argjson owner_import_requested "$provision_owner" --argjson admin_mfa_activated "$activate_admin_mfa" \
  --argjson oauth_activated "$activate_oauth" \
  '{source_sha:$source_sha, images:{api:$api,worker:$worker,probe:$probe,migrate:$migrate,backup:$backup_image,firewall:$firewall_image},firewall_policy_sha256:$firewall_policy_sha256,backup:$backup,backup_sha256:$backup_sha256,migrations:$migrations,telegram_relay_activated:$telegram_relay_activated,messenger_notifications_activated:$messenger_notifications_activated,owner_import_requested:$owner_import_requested,admin_mfa_activated:$admin_mfa_activated,oauth_activated:$oauth_activated,completed_at:(now|todate)}' \
  > "receipts/$source_sha.json.incoming"
mv compose.backup.yaml.incoming compose.backup.yaml
mv backup.sh.incoming backup.sh
chmod 700 backup.sh
if $activation_applied; then
  if [[ ! -f .env.next || -L .env.next || $(stat -c %a .env.next) != 600 ]] || ! cmp -s .env.next "$workdir/.env.next"; then
    echo 'Staged activation env changed during deployment; retain pending for review' >&2; exit 1;
  fi
  rm -f .env.next
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
