#!/usr/bin/env bash
set -euo pipefail

cd /opt/heartbeat-checker/frontend

source_sha=${1:?source SHA is required}
web_image=${2:?web image digest is required}
api_origin=${3-}
registry_user=${4:?registry user is required}
web_mode=${5:-app}
public_origin=https://heartbeat-checker.ru

[[ $source_sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid source SHA' >&2; exit 1; }
[[ $web_image =~ ^ghcr\.io/[a-z0-9_-]+/[a-z0-9_.-]+/web@sha256:[0-9a-f]{64}$ ]] || {
  echo 'Expected an immutable GHCR web image' >&2
  exit 1
}
[[ $web_mode == app || $web_mode == landing ]] || { echo 'Invalid web mode' >&2; exit 1; }
if [[ $web_mode == app ]]; then
  [[ $api_origin =~ ^https://[a-zA-Z0-9.-]+(:443)?$ ]] || { echo 'Invalid HTTPS API origin' >&2; exit 1; }
  normalized_api_origin=$(printf '%s' "$api_origin" | tr '[:upper:]' '[:lower:]')
  normalized_api_origin=${normalized_api_origin%:443}
  normalized_api_origin=${normalized_api_origin%.}
  [[ $normalized_api_origin != *localhost* && $normalized_api_origin != *127.0.0.1* ]] || { echo 'API origin must be public' >&2; exit 1; }
  [[ $normalized_api_origin != "$public_origin" ]] || { echo 'API origin must differ from web origin' >&2; exit 1; }
else
  [[ -z $api_origin ]] || { echo 'Landing must not have an API origin' >&2; exit 1; }
fi
[[ $registry_user =~ ^[a-zA-Z0-9-]+$ ]] || { echo 'Invalid registry user' >&2; exit 1; }
for staged in compose.yaml.incoming Caddyfile.incoming; do
  [[ -s $staged ]] || { echo "Missing $staged" >&2; exit 1; }
done

workdir=$(mktemp -d)
export DOCKER_CONFIG="$workdir/docker-config"
mkdir -m 700 "$DOCKER_CONFIG"
trap 'rm -rf "$workdir"' EXIT

# The workflow sends its short-lived packages token on stdin; never persist it.
docker login ghcr.io -u "$registry_user" --password-stdin >/dev/null
docker pull "$web_image"
actual_sha=$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$web_image")
[[ $actual_sha == "$source_sha" ]] || { echo 'Image revision does not match source SHA' >&2; exit 1; }
actual_mode=$(docker image inspect --format '{{ index .Config.Labels "org.heartbeat-checker.web-mode" }}' "$web_image")
[[ $actual_mode == "$web_mode" ]] || { echo 'Image web mode does not match requested deployment mode' >&2; exit 1; }

if [[ $web_mode == app ]]; then
  api_status=$(curl -sS -o "$workdir/api-response" -w '%{http_code}' --max-time 10 "$api_origin/api/v1/me")
  [[ $api_status == 401 ]] || { echo "API preflight returned $api_status, expected 401" >&2; exit 1; }
  grep -Eq '"error"[[:space:]]*:[[:space:]]*"unauthorized"' "$workdir/api-response" || { echo 'API preflight response is not the Go auth contract' >&2; exit 1; }
fi

printf 'WEB_IMAGE=%s\n' "$web_image" > image.env.incoming
chmod 600 image.env.incoming
docker compose --env-file image.env.incoming -f compose.yaml.incoming config -q
docker compose --env-file image.env.incoming -f compose.yaml.incoming pull caddy

had_previous=false
if [[ -e .env || -e compose.yaml || -e Caddyfile ]]; then
  for file in .env compose.yaml Caddyfile; do
    [[ -f $file ]] || { echo "Incomplete existing deployment: $file" >&2; exit 1; }
    cp -p "$file" "$workdir/$file"
  done
  had_previous=true
fi

rollback() {
  trap - ERR INT TERM
  echo 'Frontend deployment failed; restoring previous deployment' >&2
  if $had_previous; then
    for file in .env compose.yaml Caddyfile; do cp -p "$workdir/$file" "$file"; done
    if ! docker compose up -d --pull never --force-recreate; then
      echo 'CRITICAL: previous frontend could not be restarted; inspect Compose immediately' >&2
    fi
  else
    if docker compose down; then
      rm -f .env compose.yaml Caddyfile
    else
      echo 'CRITICAL: failed to stop the incomplete first deployment' >&2
    fi
  fi
  exit 1
}

trap rollback ERR INT TERM
mv image.env.incoming .env
mv compose.yaml.incoming compose.yaml
mv Caddyfile.incoming Caddyfile
docker compose up -d --pull never --force-recreate --remove-orphans

resolved=(--resolve heartbeat-checker.ru:443:127.0.0.1)
landing_postflight() {
  curl -fsS --max-time 5 "${resolved[@]}" -o "$workdir/home-response" "$public_origin/" || return 1
  grep -Fq 'data-web-mode="landing"' "$workdir/home-response" || return 1
  grep -Fq 'Сервис готовится к запуску' "$workdir/home-response" || return 1
  if grep -Eiq 'href="/(register|login|app)(["/?#])' "$workdir/home-response"; then return 1; fi
  local status route
  for route in terms privacy cookies contacts; do
    grep -Fq "href=\"/$route\"" "$workdir/home-response" || return 1
    status=$(curl -sS --max-time 5 "${resolved[@]}" -o "$workdir/$route-response" -w '%{http_code}' "$public_origin/$route") || return 1
    [[ $status == 200 && -s "$workdir/$route-response" ]] || return 1
  done
  curl -fsS --max-time 5 "${resolved[@]}" -o /dev/null "$public_origin/theme-init.js" || return 1
  status=$(curl -sS --max-time 5 "${resolved[@]}" -o /dev/null -w '%{http_code}' "$public_origin/api/v1/me") || return 1
  [[ $status == 404 ]] || return 1
  for route in register login forgot-password reset-password verify-email app app/settings; do
    status=$(curl -sS --max-time 5 "${resolved[@]}" -D "$workdir/route-headers" -o /dev/null -w '%{http_code}' "$public_origin/$route") || return 1
    [[ $status == 307 ]] || return 1
    grep -Eiq '^location:[[:space:]]*(/|https://heartbeat-checker\.ru/)[[:space:]]*$' "$workdir/route-headers" || return 1
  done
}

app_postflight() {
  curl -fsS --max-time 5 "${resolved[@]}" -o /dev/null "$public_origin/" || return 1
  curl -fsS --max-time 5 "${resolved[@]}" -o /dev/null "$public_origin/theme-init.js" || return 1
  local status
  status=$(curl -sS --max-time 5 "${resolved[@]}" -o "$workdir/proxy-response" -w '%{http_code}' "$public_origin/api/v1/me") || return 1
  [[ $status == 401 ]] && grep -Eq '"error"[[:space:]]*:[[:space:]]*"unauthorized"' "$workdir/proxy-response"
}

healthy=false
for ((attempt = 1; attempt <= 30; attempt++)); do
  if [[ $web_mode == landing ]]; then
    if landing_postflight; then healthy=true; break; fi
  else
    if app_postflight; then healthy=true; break; fi
  fi
  sleep 3
done

if ! $healthy; then
  echo "Public HTTPS, asset, or $web_mode postflight failed" >&2
  rollback
fi

if $had_previous; then
  mkdir -p previous
  for file in .env compose.yaml Caddyfile; do cp -p "$workdir/$file" "previous/$file"; done
fi
trap - ERR INT TERM
echo "Frontend deployed: $source_sha $web_mode $web_image"
