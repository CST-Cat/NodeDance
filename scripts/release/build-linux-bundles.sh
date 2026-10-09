#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
if [[ $# -ne 4 ]]; then
  echo "usage: $0 VERSION PRIVATE_KEY_FILE PUBLIC_KEY_FILE OUTPUT_DIR" >&2
  exit 2
fi
VERSION="$1"
PRIVATE_KEY_FILE="$2"
PUBLIC_KEY_FILE="$3"
OUTPUT_DIR="$4"
GO_BIN="${GO_BIN:-go}"
[[ "$VERSION" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] || { echo 'invalid release version' >&2; exit 2; }
[[ -f "$PRIVATE_KEY_FILE" && ! -L "$PRIVATE_KEY_FILE" ]] || { echo 'private key must be a regular file' >&2; exit 2; }
[[ "$(stat -c '%a' "$PRIVATE_KEY_FILE")" == 600 ]] || { echo 'private signing key must have mode 0600' >&2; exit 2; }
[[ -f "$PUBLIC_KEY_FILE" && ! -L "$PUBLIC_KEY_FILE" ]] || { echo 'public key must be a regular file' >&2; exit 2; }
PUBLIC_KEY="$(python3 - "$PUBLIC_KEY_FILE" <<'PY'
import base64,sys
raw=open(sys.argv[1],"rb").read().strip()
key=base64.b64decode(raw,validate=True)
if len(key) != 32:
    raise SystemExit("public key must be a base64 Ed25519 public key")
print(base64.b64encode(key).decode("ascii"))
PY
)"
make -C "$ROOT" frontend
mkdir -p -- "$OUTPUT_DIR"
RELEASE_DIR="$OUTPUT_DIR/$VERSION"
mkdir -- "$RELEASE_DIR" || { echo "release output already exists: $RELEASE_DIR" >&2; exit 1; }
WORK="$(mktemp -d "$OUTPUT_DIR/.nodedance-build.XXXXXXXX")"
cleanup() {
  rm -rf -- "$WORK"
  rm -f -- "$RELEASE_DIR/nodedance-$VERSION-linux-amd64.tar.gz" "$RELEASE_DIR/nodedance-$VERSION-linux-arm64.tar.gz"
  rmdir -- "$RELEASE_DIR" 2>/dev/null || true
}
trap cleanup EXIT

"$GO_BIN" build -trimpath -buildvcs=false -o "$WORK/nodedance-release" ./cmd/nodedance-release
for ARCH in amd64 arm64; do
  mkdir -m 700 "$WORK/$ARCH"
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -buildvcs=false \
    -ldflags "-X main.version=$VERSION" -o "$WORK/$ARCH/nodedance" ./cmd/nodedance
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -buildvcs=false \
    -ldflags "-X main.version=$VERSION -X github.com/CST-Cat/NodeDance/internal/agent/update.TrustedPublicKeyBase64=$PUBLIC_KEY" \
    -o "$WORK/$ARCH/nodedance-agent" ./cmd/nodedance-agent
  "$WORK/nodedance-release" bundle \
    --core "$WORK/$ARCH/nodedance" --agent "$WORK/$ARCH/nodedance-agent" \
    --private-key-file "$PRIVATE_KEY_FILE" --version "$VERSION" --architecture "$ARCH" \
    --output "$WORK/nodedance-$VERSION-linux-$ARCH.tar.gz"
  python3 "$ROOT/scripts/release/operations.py" verify \
    --bundle "$WORK/nodedance-$VERSION-linux-$ARCH.tar.gz" --public-key-file "$PUBLIC_KEY_FILE"
  mv -- "$WORK/nodedance-$VERSION-linux-$ARCH.tar.gz" "$RELEASE_DIR/"
done
echo "Created verified linux/amd64 and linux/arm64 release bundles in $RELEASE_DIR"
trap - EXIT
rm -rf -- "$WORK"
