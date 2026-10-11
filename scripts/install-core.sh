#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

readonly RELEASE_BASE="https://github.com/CST-Cat/NodeDance/releases/download"
readonly RELEASES_API="https://api.github.com/repos/CST-Cat/NodeDance/releases/latest"

release_tag=""
install_dir="/usr/local/bin"
tmp_dir=""
staged_binary=""

usage() {
  cat <<'EOF'
NodeDance Core installer

Usage:
  bash install-core.sh [--release-tag core-v0.1.0] [--install-dir /usr/local/bin]

Options:
  --release-tag TAG   Published Core tag (default: latest stable Core release)
  --install-dir DIR   Install the Core executable into DIR (default: /usr/local/bin)
  --help              Show this help

By default, the installer resolves the latest stable Core release, then pins
the archive and checksum downloads to that exact tag. It installs only the
`nodedance` binary and does not change Core data or configure a service.
EOF
}

fail() { printf 'NodeDance Core installer: %s\n' "$*" >&2; exit 1; }

cleanup() {
  [[ -z "$tmp_dir" ]] || rm -rf -- "$tmp_dir" 2>/dev/null || true
  [[ -z "$staged_binary" || ! -e "$staged_binary" || -L "$staged_binary" ]] || rm -f -- "$staged_binary" 2>/dev/null || true
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

[[ -n "$install_dir" ]] || fail '--install-dir cannot be empty'

for tool in awk curl install mkdir mktemp mv rm sha256sum tar uname; do
  command -v "$tool" >/dev/null 2>&1 || fail "required command is missing: $tool"
done

if [[ -z "$release_tag" ]]; then
  latest_release=$(curl --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' --tlsv1.2 "$RELEASES_API") || fail 'could not resolve the latest stable Core release'
  release_tag=$(printf '%s\n' "$latest_release" | awk -F'"' '$2 == "tag_name" { print $4; exit }')
fi
[[ "$release_tag" =~ ^core-v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail 'latest release is not a valid stable Core tag; pass --release-tag core-vMAJOR.MINOR.PATCH to pin a release'

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
[[ -d "$install_dir" ]] || fail "install destination is not a directory: $install_dir"
target="${install_dir%/}/nodedance"
[[ ! -L "$target" ]] || fail "refusing to replace a symlink: $target"
[[ ! -d "$target" ]] || fail "refusing to replace a directory: $target"

staged_binary=$(mktemp "${install_dir%/}/.nodedance.install.XXXXXXXX") || fail "cannot create a temporary binary in $install_dir"
install -m 0755 -- "$tmp_dir/$binary" "$staged_binary" || fail 'cannot stage the Core binary'
mv -fT -- "$staged_binary" "$target" || fail "cannot install Core at $target"
staged_binary=""

printf 'Installed NodeDance Core %s (%s) at %s\n' "$release_tag" "$arch" "$target"
