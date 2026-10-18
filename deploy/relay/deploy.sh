#!/usr/bin/env bash
set -euo pipefail

source_sha=${1:?source SHA is required}
backup_sha256=${2:?off-server config archive SHA-256 is required}
cert_email=${3:?certificate email is required}
[[ $# -eq 3 && $EUID -eq 0 && $source_sha =~ ^[0-9a-f]{40}$ && $backup_sha256 =~ ^[0-9a-f]{64}$ ]] || {
  echo 'Invalid release arguments or user' >&2; exit 1;
}
[[ $cert_email =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]] || {
  echo 'Invalid certificate email' >&2; exit 1;
}

domain=messengers-proxy.heartbeat-checker.ru
deploy_dir=/opt/heartbeat-checker/relay
http_site=/etc/nginx/sites-available/heartbeat-relay-http.conf
https_site=/etc/nginx/sites-available/heartbeat-relay-https.conf
http_link=/etc/nginx/sites-enabled/heartbeat-relay-http.conf
https_link=/etc/nginx/sites-enabled/heartbeat-relay-https.conf
acme_root=/var/lib/heartbeat-checker/relay/acme
old_site=/etc/nginx/sites-available/tg.winwidget.ru
old_link=/etc/nginx/sites-enabled/tg.winwidget.ru
old_backup=/etc/nginx/sites-available/tg.winwidget.ru.bak-20260804-api-v1
old_stream=/etc/nginx/modules-enabled/99-winwidget-telegram-api-stream.conf
old_logs=(
  /var/log/nginx/telegram-api-stream.access.log
  /var/log/nginx/telegram-api-stream.error.log
  /var/log/nginx/tg.winwidget.ru.access.log
  /var/log/nginx/tg.winwidget.ru.error.log
)

verify_aero() {
  local expected file
  while read -r expected file; do
    [[ -f $file && ! -L $file && $(sha256sum "$file" | cut -d' ' -f1) == "$expected" ]] || {
      echo "aeroCRM file changed: $file" >&2; return 1;
    }
  done <<'HASHES'
a58f76fbff9d5852c3b7c6157c46b05347b0136e08c3ab864c4ac1c510ffaa1e /etc/nginx/sites-available/telegram.aerocrm.space
da5df41bb386f9b8627981e2eda3a44de00d6de2177f75ceb749a4dd1a080062 /etc/nginx/snippets/aerocrm-telegram-webhooks.conf
9066c4678e22b37b986507bfb3d242a23e00b18f525e4a60305f3ff9680e142b /etc/nginx/.aerocrm-telegram-business-relay.json
HASHES
  [[ $(readlink -f /etc/nginx/sites-enabled/zz-telegram.aerocrm.space) == /etc/nginx/sites-available/telegram.aerocrm.space ]] || {
    echo 'aeroCRM site link changed' >&2; return 1;
  }
}

[[ -d $deploy_dir && ! -L $deploy_dir ]] || { echo 'Missing relay deploy directory' >&2; exit 1; }
for file in nginx-http.conf nginx-https.conf deploy.sh; do
  [[ -s $deploy_dir/$file.incoming && ! -L $deploy_dir/$file.incoming ]] || {
    echo "Missing staged $file" >&2; exit 1;
  }
done
for command in nginx certbot curl python3 openssl ufw flock timeout; do
  command -v "$command" >/dev/null || { echo "Missing $command" >&2; exit 1; }
done
verify_aero
systemctl is-active --quiet nginx
nginx -t
[[ $(getent ahostsv4 "$domain" | awk '{print $1}' | sort -u) == 185.184.122.62 ]] || {
  echo 'Relay DNS does not resolve only to the approved VPS' >&2; exit 1;
}
aero_status=$(curl -sS --max-time 10 --resolve telegram.aerocrm.space:443:127.0.0.1 \
  -o /dev/null -w '%{http_code}' https://telegram.aerocrm.space/)
[[ $aero_status =~ ^[1-5][0-9]{2}$ ]] || { echo 'aeroCRM TLS preflight failed' >&2; exit 1; }
aero_cert_fingerprint=$(timeout 10s openssl s_client -connect 127.0.0.1:443 \
  -servername telegram.aerocrm.space </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256)
[[ $aero_cert_fingerprint == sha256\ Fingerprint=* ]] || { echo 'aeroCRM certificate preflight failed' >&2; exit 1; }

exec 9>/run/heartbeat-relay-deploy.lock
flock -n 9 || { echo 'Another relay deployment is in progress' >&2; exit 1; }
umask 077
workdir=$(mktemp -d /run/heartbeat-relay.XXXXXXXX)
trap 'rm -rf "$workdir"' EXIT
managed=("$http_site" "$https_site" "$http_link" "$https_link" "$old_site" "$old_link" "$old_backup" "$old_stream")
for path in "${managed[@]}"; do
  if [[ -e $path || -L $path ]]; then
    mkdir -p "$workdir/$(dirname "$path")"
    cp -a "$path" "$workdir/$path"
  fi
done
old_count=0
for path in "$old_site" "$old_link" "$old_backup" "$old_stream"; do
  if [[ -e $path || -L $path ]]; then ((old_count += 1)); fi
done
[[ $old_count -eq 0 || $old_count -eq 4 ]] || { echo 'Incomplete WinWidget nginx inventory' >&2; exit 1; }
old_cert_count=0
for path in /etc/letsencrypt/renewal/tg.winwidget.ru.conf /etc/letsencrypt/archive/tg.winwidget.ru /etc/letsencrypt/live/tg.winwidget.ru; do
  if [[ -e $path || -L $path ]]; then ((old_cert_count += 1)); fi
done
[[ $old_cert_count -eq 0 || $old_cert_count -eq 3 ]] || { echo 'Incomplete WinWidget certificate inventory' >&2; exit 1; }
ufw_had_8443=false
if ufw status | grep -Fq '8443/tcp'; then ufw_had_8443=true; fi
ufw_removed_8443=false
committed=false
irreversible_cleanup_started=false
receipt=$deploy_dir/receipt.txt
cp -a "$receipt" "$workdir/receipt.txt" 2>/dev/null || true

on_exit() {
  local status=$?
  trap - EXIT
  set +e
  if ((status != 0)) && $irreversible_cleanup_started; then
    echo 'Relay is active, but old certificate cleanup may be partial; inspect receipt and certbot state' >&2
  elif ((status != 0)) && ! $committed; then
    echo 'Relay deployment failed; restoring previous nginx configuration' >&2
    for path in "${managed[@]}"; do
      rm -f "$path"
      if [[ -e $workdir/$path || -L $workdir/$path ]]; then cp -a "$workdir/$path" "$path"; fi
    done
    if $ufw_removed_8443 && $ufw_had_8443; then ufw allow 8443/tcp >/dev/null || true; fi
    if [[ -f $workdir/receipt.txt ]]; then
      cp -a "$workdir/receipt.txt" "$receipt"
    else
      rm -f "$receipt"
    fi
    if nginx -t >/dev/null 2>&1; then
      systemctl reload nginx || echo 'CRITICAL: nginx reload failed during rollback' >&2
    else
      echo 'CRITICAL: previous nginx configuration did not pass validation' >&2
    fi
    verify_aero || true
  fi
  rm -f "$deploy_dir"/*.incoming
  rm -rf "$workdir"
  exit "$status"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

install -d -m 755 "$acme_root/.well-known/acme-challenge"
install -m 644 "$deploy_dir/nginx-http.conf.incoming" "$http_site"
ln -sfn "$http_site" "$http_link"
nginx -t
systemctl reload nginx

certbot certonly --webroot -w "$acme_root" -d "$domain" --cert-name "$domain" \
  --non-interactive --agree-tos --no-eff-email --email "$cert_email" \
  --keep-until-expiring --deploy-hook 'nginx -t && systemctl reload nginx'
[[ -s /etc/letsencrypt/live/$domain/fullchain.pem && -s /etc/letsencrypt/live/$domain/privkey.pem ]] || {
  echo 'Relay certificate is unavailable' >&2; exit 1;
}

install -m 644 "$deploy_dir/nginx-https.conf.incoming" "$https_site"
ln -sfn "$https_site" "$https_link"
nginx -t
systemctl reload nginx

postflight() {
  local status
  local -a resolved=(--resolve "$domain:443:127.0.0.1")
  status=$(curl -sS --max-time 15 "${resolved[@]}" -o "$workdir/telegram-response" \
    -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data '{}' \
    "https://$domain/telegram-api/bot000000:invalid/getMe")
  [[ $status == 401 || $status == 404 ]] || { echo 'Telegram relay did not reach upstream' >&2; return 1; }
  python3 - "$workdir/telegram-response" "$status" <<'PY' || {
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    data = json.load(response)
if data.get("ok") is not False or data.get("error_code") != int(sys.argv[2]):
    raise SystemExit(1)
PY
    echo 'Telegram relay response is not the upstream API contract' >&2; return 1;
  }
  status=$(curl -sS --max-time 15 "${resolved[@]}" -o "$workdir/webhook-response" \
    -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -H 'X-Telegram-Bot-Api-Secret-Token: invalid' --data '{}' \
    "https://$domain/webhooks/support/telegram")
  if [[ $status != 401 ]] || ! grep -Fxq unauthorized "$workdir/webhook-response"; then
    echo 'Webhook relay did not reach Go authorization' >&2; return 1;
  fi
  status=$(curl -sS --max-time 10 "${resolved[@]}" -o /dev/null -w '%{http_code}' \
    "https://$domain/telegram-api/bot000000:invalid/setWebhook")
  [[ $status == 404 ]] || { echo 'Disallowed Telegram method was exposed' >&2; return 1; }
  verify_aero
  systemctl is-active --quiet nginx
  local current_aero_status
  current_aero_status=$(curl -sS --max-time 10 --resolve telegram.aerocrm.space:443:127.0.0.1 \
    -o /dev/null -w '%{http_code}' https://telegram.aerocrm.space/)
  [[ $current_aero_status == "$aero_status" ]] || {
    echo 'aeroCRM response changed during relay deployment' >&2; return 1;
  }
  local current_aero_cert_fingerprint
  current_aero_cert_fingerprint=$(timeout 10s openssl s_client -connect 127.0.0.1:443 \
    -servername telegram.aerocrm.space </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256)
  [[ $current_aero_cert_fingerprint == "$aero_cert_fingerprint" ]] || {
    echo 'aeroCRM certificate changed during relay deployment' >&2; return 1;
  }
}
postflight

if ((old_count == 4)); then
  rm -f "$old_link" "$old_site" "$old_backup" "$old_stream"
  nginx -t
  systemctl reload nginx
fi
if $ufw_had_8443; then
  ufw --force delete allow 8443/tcp >/dev/null
  ufw_removed_8443=true
fi
nginx -T > "$workdir/nginx-config" 2>&1
if grep -Eq 'tg\.winwidget\.ru|telegram-api-stream|listen[[:space:]]+8443' "$workdir/nginx-config"; then
  echo 'WinWidget listener remains in active nginx configuration' >&2; exit 1
fi
postflight

for log in "${old_logs[@]}"; do rm -f "$log"; done
shopt -s nullglob
for log in /var/log/nginx/tg.winwidget.ru.* /var/log/nginx/telegram-api-stream.*; do
  [[ -f $log && ! -L $log ]] || { echo "Unexpected WinWidget log path: $log" >&2; exit 1; }
  rm -f "$log"
done
shopt -u nullglob
mkdir -p "$deploy_dir/receipts"
{
  printf 'status=cleanup_pending\n'
  printf 'source_sha=%s\n' "$source_sha"
  printf 'off_server_config_archive_sha256=%s\n' "$backup_sha256"
  printf 'deployed_at=%s\n' "$(date -u +%FT%TZ)"
  printf 'certificate_sha256=%s\n' "$(sha256sum "/etc/letsencrypt/live/$domain/fullchain.pem" | cut -d' ' -f1)"
  printf 'aerocrm_vhost_sha256=%s\n' 'a58f76fbff9d5852c3b7c6157c46b05347b0136e08c3ab864c4ac1c510ffaa1e'
  printf 'aerocrm_certificate=%s\n' "$aero_cert_fingerprint"
} > "$deploy_dir/receipts/$source_sha.incoming"
mv "$deploy_dir/receipts/$source_sha.incoming" "$receipt"

if ((old_cert_count == 3)); then
  irreversible_cleanup_started=true
  certbot delete --cert-name tg.winwidget.ru --non-interactive
fi
for path in /etc/letsencrypt/renewal/tg.winwidget.ru.conf /etc/letsencrypt/archive/tg.winwidget.ru /etc/letsencrypt/live/tg.winwidget.ru; do
  [[ ! -e $path && ! -L $path ]] || { echo "Old certificate path remains: $path" >&2; exit 1; }
done
nginx -t
postflight
sed -i 's/^status=cleanup_pending$/status=complete/' "$receipt"
committed=true
echo "Relay deployed and WinWidget proxy removed: $source_sha"
