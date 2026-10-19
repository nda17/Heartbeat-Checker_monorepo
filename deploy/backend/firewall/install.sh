#!/usr/bin/env bash
set -euo pipefail

# This immutable helper accepts no payload or command arguments. Its only host
# filesystem access is the sbin directory needed to atomically publish this file.
[[ $# -eq 0 && $EUID -eq 0 && -d /host-sbin ]] || { echo 'Invalid firewall installer invocation' >&2; exit 1; }
destination=/host-sbin/heartbeat-probe-firewall
[[ -f $destination && ! -L $destination ]] || { echo 'Existing root-owned firewall bootstrap is required' >&2; exit 1; }
[[ $(stat -c %u "$destination") == 0 && $(stat -c %a "$destination") == 755 ]] || { echo 'Unexpected installed firewall ownership or mode' >&2; exit 1; }
# Docker is already running during deployment. Refuse a different iptables
# backend that could create an unused DOCKER-USER chain and issue a false receipt.
iptables -C FORWARD -j DOCKER-USER
if ! iptables -C DOCKER-USER -s 172.30.80.2/32 -j HEARTBEAT_PROBE_V2 2>/dev/null; then
  iptables -C DOCKER-USER -s 172.30.80.2/32 -j HEARTBEAT_PROBE
fi
iptables -C INPUT -s 172.30.80.2/32 -j HEARTBEAT_PROBE_INPUT
policy=/opt/firewall/probe-firewall.sh
policy_sha=$(sha256sum "$policy" | awk '{print $1}')
staged=$(mktemp /host-sbin/.heartbeat-probe-firewall.XXXXXX)
trap 'rm -f -- "$staged"' EXIT
install -m 0755 "$policy" "$staged"
# A failed or interrupted apply retains the previous persistent script. The new
# chain is built deny-first and replaces the old hook only when complete.
bash "$policy"
iptables -C FORWARD -j DOCKER-USER
iptables -C DOCKER-USER -s 172.30.80.2/32 -j HEARTBEAT_PROBE_V2
iptables -C INPUT -s 172.30.80.2/32 -j HEARTBEAT_PROBE_INPUT
[[ $(iptables -S HEARTBEAT_PROBE_V2 | tail -n 1) == '-A HEARTBEAT_PROBE_V2 -j REJECT'* ]]
[[ $(iptables -S HEARTBEAT_PROBE_INPUT | tail -n 1) == '-A HEARTBEAT_PROBE_INPUT -j REJECT'* ]]
mv -f -- "$staged" "$destination"
[[ $(sha256sum "$destination" | awk '{print $1}') == "$policy_sha" ]]
printf '%s\n' "$policy_sha"
