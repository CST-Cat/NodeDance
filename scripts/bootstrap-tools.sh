#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOL_ROOT="$ROOT/.tools"
LOCK="$ROOT/toolchain.lock.json"
GO_VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["go"]["version"])' "$LOCK")"
NODE_VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["node"]["version"])' "$LOCK")"
PNPM_VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pnpm"]["version"])' "$LOCK")"
COMPOSE_VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["compose"]["version"])' "$LOCK")"

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) GO_ARCH=amd64; NODE_ARCH=x64 ;;
  Linux/aarch64|Linux/arm64) GO_ARCH=arm64; NODE_ARCH=arm64 ;;
  *) echo "unsupported bootstrap host: $(uname -s)/$(uname -m)" >&2; exit 2 ;;
esac

mkdir -p "$TOOL_ROOT/cache"
GO_DIR="$TOOL_ROOT/go$GO_VERSION"
NODE_DIR="$TOOL_ROOT/node-v$NODE_VERSION"
COMPOSE_DIR="$TOOL_ROOT/docker/cli-plugins"

if [[ ! -x "$GO_DIR/bin/go" ]]; then
  GO_FILE="go$GO_VERSION.linux-$GO_ARCH.tar.gz"
  GO_SHA="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["go"]["archives"][sys.argv[2]]["sha256"])' "$LOCK" "linux/$GO_ARCH")"
  GO_ARCHIVE="$TOOL_ROOT/cache/$GO_FILE"
  curl --fail --location --retry 3 --output "$GO_ARCHIVE.tmp" "https://go.dev/dl/$GO_FILE"
  echo "$GO_SHA  $GO_ARCHIVE.tmp" | sha256sum --check --status || { rm -f "$GO_ARCHIVE.tmp"; echo "Go archive checksum mismatch" >&2; exit 1; }
  mv "$GO_ARCHIVE.tmp" "$GO_ARCHIVE"
  rm -rf "$TOOL_ROOT/unpack-go"
  mkdir -p "$TOOL_ROOT/unpack-go"
  tar -xzf "$GO_ARCHIVE" -C "$TOOL_ROOT/unpack-go"
  mv "$TOOL_ROOT/unpack-go/go" "$GO_DIR"
  rmdir "$TOOL_ROOT/unpack-go"
fi

if [[ ! -x "$NODE_DIR/bin/node" ]]; then
  NODE_FILE="node-v$NODE_VERSION-linux-$NODE_ARCH.tar.xz"
  NODE_SHA="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["node"]["archives"][sys.argv[2]]["sha256"])' "$LOCK" "linux/$GO_ARCH")"
  NODE_ARCHIVE="$TOOL_ROOT/cache/$NODE_FILE"
  curl --fail --location --retry 3 --output "$NODE_ARCHIVE.tmp" "https://nodejs.org/dist/v$NODE_VERSION/$NODE_FILE"
  echo "$NODE_SHA  $NODE_ARCHIVE.tmp" | sha256sum --check --status || { rm -f "$NODE_ARCHIVE.tmp"; echo "Node archive checksum mismatch" >&2; exit 1; }
  mv "$NODE_ARCHIVE.tmp" "$NODE_ARCHIVE"
  rm -rf "$TOOL_ROOT/unpack-node"
  mkdir -p "$TOOL_ROOT/unpack-node"
  tar -xJf "$NODE_ARCHIVE" -C "$TOOL_ROOT/unpack-node"
  mv "$TOOL_ROOT/unpack-node/node-v$NODE_VERSION-linux-$NODE_ARCH" "$NODE_DIR"
  rmdir "$TOOL_ROOT/unpack-node"
fi

export PATH="$GO_DIR/bin:$NODE_DIR/bin:$TOOL_ROOT/pnpm/node_modules/.bin:$PATH"
export GOTOOLCHAIN=local
if [[ "$(node --version)" != "v$NODE_VERSION" ]]; then
  echo "local Node version mismatch: expected v$NODE_VERSION, got $(node --version)" >&2; exit 1
fi
if [[ "$(go version | awk '{print $3}')" != "go$GO_VERSION" ]]; then
  echo "local Go version mismatch: expected go$GO_VERSION, got $(go version)" >&2; exit 1
fi
PNPM_BIN="$TOOL_ROOT/pnpm/node_modules/.bin/pnpm"
PNPM_ARCH_SHA="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["pnpm"]["native_packages"][sys.argv[2]]["binary_sha256"])' "$LOCK" "linux/$GO_ARCH")"
PNPM_LOCK_SHA="$(cat "$ROOT/tooling/pnpm/package.json" "$ROOT/tooling/pnpm/package-lock.json" | sha256sum | awk '{print $1}')"
PNPM_INSTALL_MARKER="$TOOL_ROOT/pnpm/.nodedance-installed-lock-sha256"
pnpm_is_elf() {
  python3 - "$PNPM_BIN" <<'PY'
import pathlib,sys
path=pathlib.Path(sys.argv[1])
try:
    with path.open('rb') as stream:
        magic=stream.read(4)
except OSError as error:
    raise SystemExit(str(error))
if magic != b'\x7fELF':
    raise SystemExit(f"pnpm executable is not a Linux ELF native binary: {magic!r}")
PY
}

mkdir -p "$TOOL_ROOT/pnpm"
cp "$ROOT/tooling/pnpm/package.json" "$TOOL_ROOT/pnpm/package.json"
cp "$ROOT/tooling/pnpm/package-lock.json" "$TOOL_ROOT/pnpm/package-lock.json"
pnpm_needs_install=0
if [[ ! -f "$PNPM_INSTALL_MARKER" ]] || [[ "$(cat "$PNPM_INSTALL_MARKER")" != "$PNPM_LOCK_SHA" ]] || ! pnpm_is_elf; then
  pnpm_needs_install=1
else
  actual_pnpm_sha="$(sha256sum "$PNPM_BIN" | awk '{print $1}')"
  [[ "$actual_pnpm_sha" == "$PNPM_ARCH_SHA" ]] || pnpm_needs_install=1
  if ((pnpm_needs_install == 0)); then
    actual_pnpm_version="$(env -i HOME="$TOOL_ROOT/pnpm/empty-home" PATH=/nonexistent "$PNPM_BIN" --version 2>/dev/null || true)"
    [[ "$actual_pnpm_version" == "$PNPM_VERSION" ]] || pnpm_needs_install=1
  fi
fi
if ((pnpm_needs_install)); then
  rm -rf -- "$TOOL_ROOT/pnpm/node_modules"
  npm ci --prefix "$TOOL_ROOT/pnpm" --ignore-scripts=false --no-audit --no-fund
  printf '%s\n' "$PNPM_LOCK_SHA" > "$PNPM_INSTALL_MARKER.tmp"
  mv "$PNPM_INSTALL_MARKER.tmp" "$PNPM_INSTALL_MARKER"
fi
pnpm_is_elf || { echo 'locked pnpm install did not produce a native executable; refusing a hidden Node/network fallback' >&2; exit 1; }
actual_pnpm_sha="$(sha256sum "$PNPM_BIN" | awk '{print $1}')"
[[ "$actual_pnpm_sha" == "$PNPM_ARCH_SHA" ]] || { echo "native pnpm SHA-256 mismatch: expected $PNPM_ARCH_SHA, got $actual_pnpm_sha" >&2; exit 1; }
PNPM_ISOLATED_VERSION="$(env -i HOME="$TOOL_ROOT/pnpm/empty-home" PATH=/nonexistent "$PNPM_BIN" --version)"
[[ "$PNPM_ISOLATED_VERSION" == "$PNPM_VERSION" ]] || { echo "native pnpm direct-exec mismatch: expected $PNPM_VERSION, got $PNPM_ISOLATED_VERSION" >&2; exit 1; }

case "$GO_ARCH" in
  amd64) COMPOSE_ARCH=x86_64 ;;
  arm64) COMPOSE_ARCH=aarch64 ;;
esac
COMPOSE_FILE="docker-compose-linux-$COMPOSE_ARCH"
COMPOSE_SHA="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["compose"]["archives"][sys.argv[2]]["sha256"])' "$LOCK" "linux/$GO_ARCH")"
COMPOSE_CHECKSUMS_SHA="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["compose"]["checksums_asset_sha256"])' "$LOCK")"
COMPOSE_BIN="$COMPOSE_DIR/docker-compose"
COMPOSE_CHECKSUMS="$TOOL_ROOT/cache/docker-compose-v$COMPOSE_VERSION-checksums.txt"
mkdir -p "$COMPOSE_DIR"
if [[ ! -f "$COMPOSE_CHECKSUMS" ]] || ! echo "$COMPOSE_CHECKSUMS_SHA  $COMPOSE_CHECKSUMS" | sha256sum --check --status; then
  curl --fail --location --retry 3 --output "$COMPOSE_CHECKSUMS.tmp" "https://github.com/docker/compose/releases/download/v$COMPOSE_VERSION/checksums.txt"
  echo "$COMPOSE_CHECKSUMS_SHA  $COMPOSE_CHECKSUMS.tmp" | sha256sum --check --status || { rm -f "$COMPOSE_CHECKSUMS.tmp"; echo "Docker Compose checksums.txt SHA-256 mismatch" >&2; exit 1; }
  mv "$COMPOSE_CHECKSUMS.tmp" "$COMPOSE_CHECKSUMS"
fi
RELEASE_COMPOSE_SHA="$(awk -v file="$COMPOSE_FILE" '$2=="*" file || $2==file {print $1}' "$COMPOSE_CHECKSUMS")"
[[ "$RELEASE_COMPOSE_SHA" == "$COMPOSE_SHA" ]] || { echo "Docker Compose release checksums asset disagrees with toolchain lock for $COMPOSE_FILE" >&2; exit 1; }
if [[ ! -x "$COMPOSE_BIN" ]] || ! echo "$COMPOSE_SHA  $COMPOSE_BIN" | sha256sum --check --status; then
  COMPOSE_TMP="$COMPOSE_BIN.tmp"
  curl --fail --location --retry 3 --output "$COMPOSE_TMP" "https://github.com/docker/compose/releases/download/v$COMPOSE_VERSION/$COMPOSE_FILE"
  echo "$COMPOSE_SHA  $COMPOSE_TMP" | sha256sum --check --status || { rm -f "$COMPOSE_TMP"; echo "Docker Compose archive checksum mismatch" >&2; exit 1; }
  chmod 755 "$COMPOSE_TMP"
  mv "$COMPOSE_TMP" "$COMPOSE_BIN"
fi
COMPOSE_PLUGIN_VERSION="$("$COMPOSE_BIN" version --short | sed 's/^v//')"
[[ "$COMPOSE_PLUGIN_VERSION" == "$COMPOSE_VERSION" ]] || { echo "local Docker Compose plugin mismatch: expected $COMPOSE_VERSION, got $COMPOSE_PLUGIN_VERSION" >&2; exit 1; }

echo "NodeDance local tools ready: go $(go version | awk '{print $3}'), node $(node --version), native-pnpm $PNPM_ISOLATED_VERSION, docker-compose $COMPOSE_PLUGIN_VERSION"
