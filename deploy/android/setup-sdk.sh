#!/usr/bin/env bash
set -euo pipefail

[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]]
: "${RUNNER_TEMP:?Expected GitHub runner temporary directory}"
: "${GITHUB_ENV:?Expected GitHub environment file}"
: "${GITHUB_PATH:?Expected GitHub path file}"

# Java-based tools avoid the unpinned Android CLI bootstrap in tools 23.
# Google publishes this archive/checksum in repository2-3.xml (cmdline-tools;20.0).
tools_version=20.0
tools_build=14742923
tools_sha1=48833c34b761c10cb20bcd16582129395d121b27
tools_sha256=04453066b540409d975c676d781da1477479dde3761310f1a7eb92a1dfb15af7
sdk_root="$RUNNER_TEMP/heartbeat-android-sdk"
download_dir=$(mktemp -d)
trap 'rm -rf "$download_dir"' EXIT

curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  --retry 3 --retry-all-errors --connect-timeout 20 --max-time 180 \
  --output "$download_dir/tools.zip" \
  "https://dl.google.com/android/repository/commandlinetools-linux-${tools_build}_latest.zip"
printf '%s  %s\n' "$tools_sha1" "$download_dir/tools.zip" | sha1sum --check --status
printf '%s  %s\n' "$tools_sha256" "$download_dir/tools.zip" | sha256sum --check --status
unzip -q "$download_dir/tools.zip" -d "$download_dir/extracted"
mkdir -p "$sdk_root/cmdline-tools"
[[ ! -e "$sdk_root/cmdline-tools/$tools_version" ]]
mv "$download_dir/extracted/cmdline-tools" "$sdk_root/cmdline-tools/$tools_version"

export ANDROID_HOME="$sdk_root"
export ANDROID_SDK_ROOT="$sdk_root"
export ANDROID_USER_HOME="$RUNNER_TEMP/heartbeat-android-user"
export ANDROID_AVD_HOME="$ANDROID_USER_HOME/avd"
export PATH="$sdk_root/cmdline-tools/$tools_version/bin:$sdk_root/platform-tools:$sdk_root/emulator:$PATH"
mkdir -p "$ANDROID_AVD_HOME"
printf 'ANDROID_HOME=%s\nANDROID_SDK_ROOT=%s\nANDROID_USER_HOME=%s\nANDROID_AVD_HOME=%s\n' \
  "$ANDROID_HOME" "$ANDROID_SDK_ROOT" "$ANDROID_USER_HOME" "$ANDROID_AVD_HOME" >> "$GITHUB_ENV"
printf '%s\n' "$sdk_root/cmdline-tools/$tools_version/bin" "$sdk_root/platform-tools" \
  "$sdk_root/emulator" >> "$GITHUB_PATH"

sdkmanager --sdk_root="$sdk_root" --licenses < <(yes) > /dev/null
for attempt in 1 2 3; do
  if sdkmanager --sdk_root="$sdk_root" 'platforms;android-37.0' 'build-tools;36.0.0' \
    'platform-tools' 'ndk;27.1.12297006' 'cmake;3.30.5' "$@"; then
    break
  fi
  if (( attempt == 3 )); then
    echo 'Android SDK component installation failed after 3 attempts' >&2
    exit 1
  fi
  sleep "$(( attempt * 2 ))"
done
