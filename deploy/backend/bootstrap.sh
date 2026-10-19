#!/usr/bin/env bash
set -euo pipefail

# Run as root on a supported Ubuntu amd64 host, passing one SSH public key on stdin:
#   sudo bash bootstrap.sh < /path/to/deploy-key.pub

die() {
  printf 'bootstrap: %s\n' "$*" >&2
  exit 1
}

[[ $# -eq 0 ]] || die 'usage: sudo bash bootstrap.sh < deploy-key.pub'
[[ $EUID -eq 0 ]] || die 'run as root'
[[ ! -t 0 ]] || die 'provide one SSH public key on stdin'

IFS= read -r public_key || [[ -n ${public_key:-} ]] || die 'empty SSH public key'
[[ -n $public_key && $public_key != *$'\r'* ]] || die 'invalid SSH public key'
if IFS= read -r extra_line || [[ -n ${extra_line:-} ]]; then
  die 'provide exactly one SSH public key line'
fi

read -r key_type key_blob _ <<< "$public_key"
case $key_type in
  ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh.com|sk-ecdsa-sha2-nistp256@openssh.com) ;;
  *) die 'unsupported SSH public key type or key options' ;;
esac
[[ ${key_blob:-} =~ ^[A-Za-z0-9+/]+={0,2}$ ]] || die 'invalid SSH public key encoding'

# shellcheck source=/dev/null
source /etc/os-release
[[ $ID == ubuntu ]] || die 'Ubuntu is required'
case $VERSION_ID in
  22.04|24.04|26.04) ;;
  *) die "unsupported Ubuntu version: $VERSION_ID" ;;
esac
[[ $(dpkg --print-architecture) == amd64 ]] || die 'amd64 is required'
codename=${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}
case $VERSION_ID:$codename in
  22.04:jammy|24.04:noble|26.04:resolute) ;;
  *) die 'Ubuntu version and codename do not match' ;;
esac

umask 077
temp_dir=$(mktemp -d)
trap 'rm -rf -- "$temp_dir"' EXIT
export GNUPGHOME=$temp_dir/gnupg
install -d -m 0700 "$GNUPGHOME"
printf '%s\n' "$public_key" > "$temp_dir/public-key"

export DEBIAN_FRONTEND=noninteractive
apt_options=(-y -o Dpkg::Use-Pty=0)

missing_packages=()
for package in ca-certificates curl gnupg openssh-client iptables jq python3; do
  if [[ $(dpkg-query -W -f='${Status}' "$package" 2>/dev/null || true) != 'install ok installed' ]]; then
    missing_packages+=("$package")
  fi
done
if ((${#missing_packages[@]})); then
  apt-get update
  apt-get "${apt_options[@]}" install --no-install-recommends "${missing_packages[@]}"
fi
ssh-keygen -lf "$temp_dir/public-key" >/dev/null || die 'SSH public key failed validation'

# Replacing distribution/container runtimes can disrupt an existing host.
for package in docker.io docker-compose docker-compose-v2 docker-doc docker-buildx podman-docker containerd runc; do
  [[ $(dpkg-query -W -f='${Status}' "$package" 2>/dev/null || true) != 'install ok installed' ]] ||
    die "conflicting package installed: $package"
done

keyring=/etc/apt/keyrings/docker.asc
source_file=/etc/apt/sources.list.d/docker.sources
[[ ! -e /etc/apt/sources.list.d/docker.list ]] || die 'existing docker.list must be reviewed first'
install -d -m 0755 /etc/apt/keyrings
if [[ ! -e $keyring ]]; then
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o "$temp_dir/docker.asc"
  fingerprint=$(gpg --show-keys --with-colons "$temp_dir/docker.asc" | awk -F: '$1 == "pub" { primary = 1; next } primary && $1 == "fpr" { print $10; primary = 0 }')
  [[ $fingerprint == 9DC858229FC7DD38854AE2D88D81803C0EBFCD88 ]] || die 'unexpected Docker apt key fingerprint'
  install -m 0644 "$temp_dir/docker.asc" "$keyring"
else
  [[ -f $keyring && ! -L $keyring ]] || die 'Docker apt key is not a regular file'
  fingerprint=$(gpg --show-keys --with-colons "$keyring" | awk -F: '$1 == "pub" { primary = 1; next } primary && $1 == "fpr" { print $10; primary = 0 }')
  [[ $fingerprint == 9DC858229FC7DD38854AE2D88D81803C0EBFCD88 ]] || die 'unexpected existing Docker apt key fingerprint'
fi

cat > "$temp_dir/docker.sources" <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $codename
Components: stable
Architectures: amd64
Signed-By: $keyring
EOF
if [[ -e $source_file ]]; then
  [[ -f $source_file && ! -L $source_file ]] || die 'Docker apt source is not a regular file'
  cmp -s "$temp_dir/docker.sources" "$source_file" || die 'existing Docker apt source differs'
else
  install -m 0644 "$temp_dir/docker.sources" "$source_file"
fi

missing_packages=()
for package in docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin; do
  if [[ $(dpkg-query -W -f='${Status}' "$package" 2>/dev/null || true) != 'install ok installed' ]]; then
    missing_packages+=("$package")
  fi
done
if ((${#missing_packages[@]})); then
  apt-get update
  apt-get "${apt_options[@]}" install --no-install-recommends "${missing_packages[@]}"
fi
systemctl enable --now docker
docker info >/dev/null
docker compose version >/dev/null

deploy_user=heartbeat-deploy
deploy_home=/home/heartbeat-deploy
if getent passwd "$deploy_user" >/dev/null; then
  IFS=: read -r _ _ _ _ _ existing_home existing_shell < <(getent passwd "$deploy_user")
  [[ $existing_home == "$deploy_home" && $existing_shell == /bin/bash ]] ||
    die 'existing deploy account has an unexpected home or shell'
else
  [[ ! -e $deploy_home ]] || die 'deploy home exists without its account'
  useradd --create-home --user-group --home-dir "$deploy_home" --shell /bin/bash "$deploy_user"
fi
[[ -d $deploy_home && ! -L $deploy_home ]] || die 'deploy home is not a directory'
[[ $(stat -c %U "$deploy_home") == "$deploy_user" ]] || die 'deploy home has an unexpected owner'
chmod 0700 "$deploy_home"
usermod -aG docker "$deploy_user"

ssh_dir=$deploy_home/.ssh
[[ ! -L $ssh_dir ]] || die 'deploy SSH directory is a symbolic link'
install -d -m 0700 -o "$deploy_user" -g "$deploy_user" "$ssh_dir"
authorized_keys=$ssh_dir/authorized_keys
printf 'no-agent-forwarding,no-port-forwarding,no-X11-forwarding,no-pty,no-user-rc %s\n' "$public_key" > "$temp_dir/authorized_keys"
if [[ -e $authorized_keys ]]; then
  [[ -f $authorized_keys && ! -L $authorized_keys ]] || die 'authorized_keys is not a regular file'
  cmp -s "$temp_dir/authorized_keys" "$authorized_keys" ||
    die 'existing authorized_keys differs; review key rotation manually'
  chown "$deploy_user:$deploy_user" "$authorized_keys"
  chmod 0600 "$authorized_keys"
else
  install -m 0600 -o "$deploy_user" -g "$deploy_user" "$temp_dir/authorized_keys" "$authorized_keys"
fi

[[ ! -L /opt/heartbeat-checker && ! -L /opt/heartbeat-checker/backend ]] || die 'deploy path contains a symbolic link'
install -d -m 0755 /opt/heartbeat-checker
if [[ -e /opt/heartbeat-checker/backend ]]; then
  [[ -d /opt/heartbeat-checker/backend ]] || die 'backend deploy path is not a directory'
  [[ $(stat -c %U /opt/heartbeat-checker/backend) == "$deploy_user" ]] ||
    die 'backend deploy path has an unexpected owner'
fi
install -d -m 0700 -o "$deploy_user" -g "$deploy_user" /opt/heartbeat-checker/backend

firewall_source=$(dirname -- "$(realpath -- "${BASH_SOURCE[0]}")")/probe-firewall.sh
[[ -f $firewall_source && ! -L $firewall_source ]] || die 'probe-firewall.sh must be next to bootstrap.sh'
install -m 0755 "$firewall_source" /usr/local/sbin/heartbeat-probe-firewall
install -d -m 0755 /etc/systemd/system/docker.service.d
cat > "$temp_dir/heartbeat-probe-firewall.conf" <<'EOF'
[Service]
ExecStartPre=/usr/local/sbin/heartbeat-probe-firewall
ExecStartPost=/usr/local/sbin/heartbeat-probe-firewall
EOF
install -m 0644 "$temp_dir/heartbeat-probe-firewall.conf" /etc/systemd/system/docker.service.d/heartbeat-probe-firewall.conf
systemctl daemon-reload
/usr/local/sbin/heartbeat-probe-firewall
iptables -C DOCKER-USER -s 172.30.80.2/32 -j HEARTBEAT_PROBE_V2 || die 'probe forward firewall hook is absent'
iptables -C INPUT -s 172.30.80.2/32 -j HEARTBEAT_PROBE_INPUT || die 'probe host firewall hook is absent'

printf 'bootstrap: Docker, probe firewall and %s are ready; backend deploy path is /opt/heartbeat-checker/backend\n' "$deploy_user"
