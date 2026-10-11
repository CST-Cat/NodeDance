#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ ${EUID} == 0 ]] || { echo 'NodeDance update supervisor requires root.' >&2; exit 1; }
[[ ${1:-} == --release-tag && ${2:-} =~ ^(core|agent)-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo 'Usage: install-updater.sh --release-tag COMPONENT-vX.Y.Z [deployment registration flags]' >&2; exit 1; }
tag=$2; shift 2
case $(uname -m) in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo 'Unsupported Linux architecture' >&2;exit 1 ;; esac
[[ $(uname -s) == Linux ]] || exit 1
workspace=$(mktemp -d); trap 'rm -rf -- "$workspace"' EXIT
base="https://github.com/CST-Cat/NodeDance/releases/download/$tag"
asset="nodedance-updater-linux-$arch"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 "$base/SHA256SUMS" -o "$workspace/SHA256SUMS"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 "$base/$asset" -o "$workspace/$asset"
checksum=$(awk -v asset="$asset" '$2 == asset {print $1}' "$workspace/SHA256SUMS")
[[ "$checksum" =~ ^[0-9a-f]{64}$ ]] || { echo 'Missing or ambiguous updater checksum' >&2; exit 1; }
printf '%s  %s\n' "$checksum" "$workspace/$asset" | sha256sum --check --status
chmod 0700 "$workspace/$asset"
"$workspace/$asset" install "$@"
