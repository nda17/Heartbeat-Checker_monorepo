#!/usr/bin/env bash
set -euo pipefail

[[ $EUID -eq 0 ]] || { echo 'probe firewall must run as root' >&2; exit 1; }
# Docker may not have started yet (this script is also Docker's ExecStartPre).
iptables -N DOCKER-USER 2>/dev/null || true
iptables -S DOCKER-USER >/dev/null

probe_ip=172.30.80.2/32
forward_chain=HEARTBEAT_PROBE
input_chain=HEARTBEAT_PROBE_INPUT

iptables -N "$forward_chain" 2>/dev/null || true
iptables -N "$input_chain" 2>/dev/null || true

# Install a deny first. A restarted service must never briefly open probe egress.
iptables -C "$forward_chain" -j REJECT 2>/dev/null || iptables -A "$forward_chain" -j REJECT
iptables -C "$input_chain" -j REJECT 2>/dev/null || iptables -A "$input_chain" -j REJECT

# Insert after an existing copy, leaving the terminal reject at the end.
insert_before_reject() {
  local chain=$1
  shift
  iptables -C "$chain" "$@" 2>/dev/null && return 0
  local reject_line terminal
  terminal=$(iptables -S "$chain" | tail -n 1)
  [[ $terminal == "-A $chain -j REJECT"* ]] || {
    echo "terminal reject is not the final rule in $chain" >&2; exit 1;
  }
  reject_line=$(iptables -L "$chain" --line-numbers -n | tail -n 1 | awk '{print $1}')
  [[ $reject_line =~ ^[0-9]+$ ]] || { echo "missing terminal reject in $chain" >&2; exit 1; }
  iptables -I "$chain" "$reject_line" "$@"
}

insert_before_reject "$forward_chain" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
insert_before_reject "$input_chain" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN

# Docker's embedded resolver may forward DNS on behalf of the container. These
# explicit public resolvers are the only direct DNS destinations permitted.
for resolver in 1.1.1.1 9.9.9.9; do
  insert_before_reject "$forward_chain" -d "$resolver/32" -p udp --dport 53 -j RETURN
  insert_before_reject "$forward_chain" -d "$resolver/32" -p tcp --dport 53 -j RETURN
done

# DNS from the probe goes to Docker's embedded 127.0.0.11 resolver inside its
# namespace. All forwarded new traffic to non-public address space is denied.
for cidr in \
  0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 \
  172.16.0.0/12 192.0.0.0/24 192.0.2.0/24 192.168.0.0/16 \
  198.18.0.0/15 198.51.100.0/24 203.0.113.0/24 224.0.0.0/4 240.0.0.0/4; do
  insert_before_reject "$forward_chain" -d "$cidr" -j REJECT
done
insert_before_reject "$forward_chain" -p tcp -m multiport --dports 80,443 -j RETURN

iptables -C DOCKER-USER -s "$probe_ip" -j "$forward_chain" 2>/dev/null ||
  iptables -I DOCKER-USER 1 -s "$probe_ip" -j "$forward_chain"
iptables -C INPUT -s "$probe_ip" -j "$input_chain" 2>/dev/null ||
  iptables -I INPUT 1 -s "$probe_ip" -j "$input_chain"

iptables -C DOCKER-USER -s "$probe_ip" -j "$forward_chain"
iptables -C INPUT -s "$probe_ip" -j "$input_chain"
