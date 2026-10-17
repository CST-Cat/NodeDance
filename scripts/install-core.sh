#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

readonly RELEASE_BASE="https://github.com/CST-Cat/NodeDance/releases/download"
readonly LATEST_VERSION_URL="https://raw.githubusercontent.com/CST-Cat/NodeDance/main/core-latest.txt"

release_tag=""
install_dir="/usr/local/bin"
data_dir="/var/lib/nodedance"
unit_dir="/etc/systemd/system"
tmp_dir=""
staged_binary=""
staged_unit=""

usage() {
  cat <<'EOF'
NodeDance Core installer

Usage:
  bash install-core.sh [--release-tag core-v0.1.0] [--install-dir /usr/local/bin]

Options:
  --release-tag TAG   Published Core tag (default: latest stable Core release)
  --install-dir DIR   Install the Core executable into DIR (default: /usr/local/bin)
  --data-dir DIR      Core data directory (default: /var/lib/nodedance)
  --unit-dir DIR      systemd unit directory (default: /etc/systemd/system)
  --help              Show this help

By default, the installer resolves the latest stable Core release, then pins
the archive and checksum downloads to that exact tag. It installs the `nodedance`
binary and starts the root-owned nodedance.service. Existing Core data is preserved.
EOF
}

fail() { printf 'NodeDance Core installer: %s\n' "$*" >&2; exit 1; }

cleanup() {
  [[ -z "$tmp_dir" ]] || rm -rf -- "$tmp_dir" 2>/dev/null || true
  [[ -z "$staged_binary" || ! -e "$staged_binary" || -L "$staged_binary" ]] || rm -f -- "$staged_binary" 2>/dev/null || true
  [[ -z "$staged_unit" || ! -e "$staged_unit" || -L "$staged_unit" ]] || rm -f -- "$staged_unit" 2>/dev/null || true
}
trap cleanup EXIT

while (($#)); do
  case "$1" in
    --release-tag)
      (($# >= 2)) || fail '--release-tag requires a value'
      release_tag=$2
      shift 2
      ;;
    --install-dir)
      (($# >= 2)) || fail '--install-dir requires a value'
      install_dir=$2
      shift 2
      ;;
    --data-dir)
      (($# >= 2)) || fail '--data-dir requires a value'
      data_dir=$2
      shift 2
      ;;
    --unit-dir)
      (($# >= 2)) || fail '--unit-dir requires a value'
      unit_dir=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *) fail "unknown option: $1" ;;
  esac
done

[[ "$(uname -s)" == Linux ]] || fail 'only Linux amd64 and arm64 are supported'
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported Linux architecture: $(uname -m)" ;;
esac

for value in "$install_dir" "$data_dir" "$unit_dir"; do
  [[ "$value" =~ ^/[A-Za-z0-9_./-]+$ ]] || fail 'installation paths must be absolute and contain only letters, digits, slash, dot, underscore or hyphen'
  [[ "$value" != *'/../'* && "$value" != */.. ]] || fail 'installation paths cannot contain parent-directory segments'
done

unit_path="${unit_dir%/}/nodedance.service"
target="${install_dir%/}/nodedance"
for tool in awk chmod curl grep install ln mkdir mktemp mv rm sha256sum systemctl tar uname; do
  command -v "$tool" >/dev/null 2>&1 || fail "required command is missing: $tool"
done

if ((EUID != 0)) && { [[ "$install_dir" == /usr/local/bin || "$data_dir" == /var/lib/nodedance || "$unit_dir" == /etc/systemd/system ]]; }; then
  fail 'run the Core installer as root (for example, pipe it to sudo bash)'
fi

if [[ -L "$unit_dir" || ( -e "$unit_dir" && ! -d "$unit_dir" ) ]]; then
  fail "refusing unsafe systemd unit directory: $unit_dir"
fi
if [[ -L "$unit_path" ]]; then
  fail "refusing to replace a symlink: $unit_path"
fi
if [[ -e "$unit_path" ]]; then
  [[ -f "$unit_path" ]] || fail "refusing to replace a non-file unit: $unit_path"
  grep -Fxq '# NodeDanceCoreUnit=1' "$unit_path" || fail "refusing to replace a unit not owned by NodeDance: $unit_path"
fi
if [[ -L "$data_dir" || ( -e "$data_dir" && ! -d "$data_dir" ) ]]; then
  fail "refusing unsafe Core data path: $data_dir"
fi

if [[ -z "$release_tag" ]]; then
  # Plain HTTPS version file published by the release workflow; no GitHub REST
  # API and no per-IP rate quota in the install path.
  release_tag=$(curl --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' --tlsv1.2 "$LATEST_VERSION_URL" 2>/dev/null | tr -d '[:space:]') \
    || fail 'could not resolve the latest stable Core release; pass --release-tag core-vMAJOR.MINOR.PATCH to pin one'
fi
[[ "$release_tag" =~ ^core-v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail 'resolved release is not a valid stable Core tag; pass --release-tag core-vMAJOR.MINOR.PATCH to pin a release'

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/nodedance-core-install.XXXXXXXX")
archive="nodedance-linux-${arch}.tar.gz"
binary="nodedance-linux-${arch}"
release_url="${RELEASE_BASE}/${release_tag}"

curl --fail --silent --show-error --location \
  --proto '=https' --proto-redir '=https' --tlsv1.2 \
  --output "$tmp_dir/SHA256SUMS" "${release_url}/SHA256SUMS" || fail "could not download SHA256SUMS for ${release_tag}; check that the release is published"
curl --fail --silent --show-error --location \
  --proto '=https' --proto-redir '=https' --tlsv1.2 \
  --output "$tmp_dir/$archive" "${release_url}/${archive}" || fail "release archive is unavailable: $archive"

expected_sha=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp_dir/SHA256SUMS")
[[ "$expected_sha" =~ ^[[:xdigit:]]{64}$ ]] || fail "SHA256SUMS does not contain exactly one valid entry for $archive"
printf '%s  %s\n' "$expected_sha" "$archive" | (cd "$tmp_dir" && sha256sum --check --status -) || fail "SHA256 verification failed for $archive"

members=$(tar -tzf "$tmp_dir/$archive") || fail "cannot list release archive: $archive"
[[ "$members" == "$binary" ]] || fail "release archive must contain only $binary"
tar -xzf "$tmp_dir/$archive" -C "$tmp_dir" -- "$binary" || fail "cannot extract $binary from the release archive"
[[ -f "$tmp_dir/$binary" && ! -L "$tmp_dir/$binary" ]] || fail 'extracted Core binary is not a regular file'

mkdir -p -- "$install_dir" || fail "cannot create install directory: $install_dir"
[[ -d "$install_dir" && ! -L "$install_dir" ]] || fail "install destination is not a real directory: $install_dir"
[[ ! -L "$target" ]] || fail "refusing to replace a symlink: $target"
[[ ! -d "$target" ]] || fail "refusing to replace a directory: $target"

staged_binary=$(mktemp "${install_dir%/}/.nodedance.install.XXXXXXXX") || fail "cannot create a temporary binary in $install_dir"
install -m 0755 -- "$tmp_dir/$binary" "$staged_binary" || fail 'cannot stage the Core binary'
installed_version=$("$staged_binary" --version) || fail 'staged Core binary cannot execute on this machine'
[[ "$installed_version" == "NodeDance $release_tag" ]] || fail 'staged Core binary does not match the requested release version'
mv -fT -- "$staged_binary" "$target" || fail "cannot install Core at $target"
staged_binary=""

mkdir -p -- "$unit_dir" || fail "cannot create systemd unit directory: $unit_dir"
if ((EUID == 0)); then
  install -d -o root -g root -m 0700 -- "$data_dir" || fail "cannot prepare Core data directory: $data_dir"
else
  install -d -m 0700 -- "$data_dir" || fail "cannot prepare Core data directory: $data_dir"
fi

staged_unit=$(mktemp "${unit_dir%/}/.nodedance.service.XXXXXXXX") || fail "cannot stage systemd unit in $unit_dir"
cat >"$staged_unit" <<EOF
# NodeDanceCoreUnit=1
[Unit]
Description=NodeDance Core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
EnvironmentFile=-/etc/default/nodedance
Environment=HOME=$data_dir
Environment=NODEDANCE_LISTEN=0.0.0.0:8180
Environment=NODEDANCE_DATA_DIR=$data_dir
ExecStart=$target serve
Restart=on-failure
RestartSec=3
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "$staged_unit" || fail 'cannot set systemd unit permissions'
if [[ -e "$unit_path" ]]; then
  [[ -f "$unit_path" && ! -L "$unit_path" ]] || fail "refusing unsafe systemd unit path: $unit_path"
  grep -Fxq '# NodeDanceCoreUnit=1' "$unit_path" || fail "refusing to replace a unit not owned by NodeDance: $unit_path"
  mv -fT -- "$staged_unit" "$unit_path" || fail "cannot install systemd unit: $unit_path"
  staged_unit=""
else
  if ! ln -- "$staged_unit" "$unit_path"; then
    fail "systemd unit appeared during installation; refusing to replace it: $unit_path"
  fi
  rm -f -- "$staged_unit"
  staged_unit=""
fi

systemctl daemon-reload || fail 'systemd could not reload unit files'
systemctl enable nodedance.service || fail 'could not enable nodedance.service'
if systemctl is-active --quiet nodedance.service; then
  systemctl restart nodedance.service || fail 'could not restart nodedance.service'
else
  systemctl start nodedance.service || fail 'could not start nodedance.service'
fi

printf 'Installed NodeDance Core %s (%s) at %s\n' "$release_tag" "$arch" "$target"
printf 'Enabled and started nodedance.service (0.0.0.0:8180, data: %s)\n' "$data_dir"
