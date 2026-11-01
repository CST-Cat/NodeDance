#!/bin/sh
# Build the trimmed static rsync binaries embedded by rsyncbundle from the
# pinned upstream source tarball, for linux/amd64 and linux/arm64.
# Requires: gcc, aarch64-linux-gnu-gcc, libc6-dev-arm64-cross (for arm64).
set -eu

VERSION=3.4.1
TARBALL=rsync-$VERSION.tar.gz
SHA256=2924bcb3a1ed8b551fc101f740b9f0fe0a202b115027647cf69850d65fd88c52
URL=https://github.com/RsyncProject/rsync/releases/download/v$VERSION/$TARBALL
TRIM="--disable-openssl --disable-xxhash --disable-zstd --disable-lz4 --disable-acl-support --disable-xattr-support --disable-iconv --with-included-zlib --with-included-popt"
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

curl -sL -o "$WORK/$TARBALL" "$URL"
echo "$SHA256  $WORK/$TARBALL" | sha256sum -c -

build() {
  name=$1; cc=$2; host=$3; strip=$4
  mkdir "$WORK/$name"
  tar xzf "$WORK/$TARBALL" -C "$WORK/$name" --strip-components=1
  cd "$WORK/$name"
  # shellcheck disable=SC2086
  CC=$cc ./configure --host="$host" $TRIM LDFLAGS="-static" CFLAGS="-Os" >/dev/null
  make -j4 >/dev/null
  $strip rsync
  mkdir -p "$WORK/out"
  tar czf "$WORK/out/rsync-$name.tar.gz" rsync
  cp COPYING "$WORK/out/COPYING"
}

build linux-amd64 gcc x86_64-linux-gnu strip
build linux-arm64 aarch64-linux-gnu-gcc aarch64-linux-gnu aarch64-linux-gnu-strip

ASSETS="$ROOT/internal/transfer/rsyncbundle/assets"
cp "$WORK/out/rsync-linux-amd64.tar.gz" "$ASSETS/rsync-linux-amd64.tar.gz"
cp "$WORK/out/rsync-linux-arm64.tar.gz" "$ASSETS/rsync-linux-arm64.tar.gz"
cp "$WORK/out/COPYING" "$ROOT/internal/transfer/rsyncbundle/licenses/COPYING"
sha256sum "$ASSETS"/rsync-linux-*.tar.gz
echo "Update manifest.json and the archiveSHA256 constants with these digests, then run scripts/vendor-rsync.py."
