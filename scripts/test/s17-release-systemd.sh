#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GO_BIN="${GO_BIN:-go}"
DATA_DIR=/var/lib/nodedance

if [[ "${GITHUB_ACTIONS:-}" != true || -z "${GITHUB_RUN_ID:-}" ]]; then
  echo 'S17-10 SYSTEMD NOT_READY: real service installation is restricted to a job-owned GitHub Actions runner' >&2
  exit 3
fi
if [[ "$(ps -p 1 -o comm= | tr -d ' ')" != systemd ]] || ! command -v systemctl >/dev/null 2>&1 || ! systemctl show-environment >/dev/null 2>&1; then
  echo 'S17-10 SYSTEMD NOT_READY: this runner does not expose a controllable systemd manager' >&2
  exit 3
fi
command -v sudo >/dev/null 2>&1 || { echo 'S17-10 SYSTEMD NOT_READY: sudo is unavailable' >&2; exit 3; }
[[ -f "$ROOT/internal/core/webassets/dist/index.html" ]] || { echo 'build the embedded Web assets with make frontend first' >&2; exit 2; }

if sudo test -e /etc/systemd/system/nodedance.service || sudo test -L /etc/systemd/system/nodedance.service; then
  echo 'refusing to touch a pre-existing nodedance.service on this runner' >&2
  exit 2
fi
if sudo test -e "$DATA_DIR" || getent passwd nodedance >/dev/null 2>&1 || getent group nodedance >/dev/null 2>&1; then
  echo 'refusing to touch pre-existing nodedance account/data on this runner' >&2
  exit 2
fi
python3 - <<'PY'
import socket
sock=socket.socket()
try:
    sock.bind(("127.0.0.1",8180))
except OSError as error:
    raise SystemExit(f"S17-10 SYSTEMD NOT_READY: loopback port 8180 is unavailable: {error}")
finally:
    sock.close()
PY

WORK="$(mktemp -d "${RUNNER_TEMP:-/tmp}/nodedance-s17-systemd.XXXXXXXX")"
chmod 755 "$WORK"
SUFFIX="${GITHUB_RUN_ID:-local}-$$"
VERSION="v0.0.0-s17.${GITHUB_RUN_NUMBER:-1}"
PREFIX="/opt/nodedance-s17-${SUFFIX}"
MARKER="$DATA_DIR/s17-release-marker-${SUFFIX}"
DATA_OWNED=1
cleanup() {
  set +e
  if sudo test -f "$PREFIX/release-manifest.json"; then
    sudo "$ROOT/scripts/release/uninstall.sh" --public-key-file "$WORK/keys/public.key" --prefix "$PREFIX" >/dev/null 2>&1
  fi
  if [[ "$DATA_OWNED" == 1 ]] && ! systemctl is-active --quiet nodedance.service && ! sudo test -e /etc/systemd/system/nodedance.service; then
    sudo rm -rf -- "$DATA_DIR"
    if getent passwd nodedance >/dev/null 2>&1; then sudo userdel nodedance >/dev/null 2>&1; fi
    if getent group nodedance >/dev/null 2>&1 && ! getent passwd nodedance >/dev/null 2>&1; then sudo groupdel nodedance >/dev/null 2>&1; fi
  fi
  rm -rf -- "$WORK"
}
trap cleanup EXIT

mkdir -m 700 "$WORK/keys"
"$GO_BIN" build -trimpath -buildvcs=false -o "$WORK/nodedance-release" ./cmd/nodedance-release
"$WORK/nodedance-release" keygen --private-file "$WORK/keys/private.key" --public-file "$WORK/keys/public.key"
PUBLIC_KEY="$(tr -d '\n' < "$WORK/keys/public.key")"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported runner architecture: $ARCH" >&2; exit 2 ;;
esac
GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -buildvcs=false \
  -ldflags "-X main.version=$VERSION" -o "$WORK/nodedance" ./cmd/nodedance
GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -buildvcs=false \
  -ldflags "-X main.version=$VERSION -X github.com/CST-Cat/NodeDance/internal/agent/update.TrustedPublicKeyBase64=$PUBLIC_KEY" \
  -o "$WORK/nodedance-agent" ./cmd/nodedance-agent
"$WORK/nodedance-release" bundle --core "$WORK/nodedance" --agent "$WORK/nodedance-agent" \
  --private-key-file "$WORK/keys/private.key" --version "$VERSION" --architecture "$ARCH" \
  --output "$WORK/release.tar.gz"
python3 "$ROOT/scripts/release/operations.py" verify --bundle "$WORK/release.tar.gz" --public-key-file "$WORK/keys/public.key"

sudo "$ROOT/scripts/release/install.sh" --bundle "$WORK/release.tar.gz" \
  --public-key-file "$WORK/keys/public.key" --prefix "$PREFIX"
active=0
for _ in $(seq 1 45); do
  if systemctl is-active --quiet nodedance.service && curl --fail --silent --show-error --max-time 2 \
      http://127.0.0.1:8180/api/v1/health >/dev/null; then
    active=1
    break
  fi
  sleep 1
done
[[ "$active" == 1 ]] || { echo 'NodeDance Core did not become healthy on 127.0.0.1:8180' >&2; systemctl status nodedance.service --no-pager >&2 || true; exit 1; }
[[ "$(systemctl show --property=User --value nodedance.service)" == nodedance ]]
[[ "$(systemctl show --property=ExecStart --value nodedance.service)" == *"--listen 127.0.0.1:8180"* ]]
sudo -u nodedance sh -c 'printf %s "$1" > "$2"' sh "NodeDance S17 release business data ${SUFFIX}" "$MARKER"
[[ "$(sudo cat "$MARKER")" == "NodeDance S17 release business data ${SUFFIX}" ]]

sudo "$ROOT/scripts/release/uninstall.sh" --public-key-file "$WORK/keys/public.key" --prefix "$PREFIX"
[[ ! -e "$PREFIX" ]] || { echo 'uninstaller left the owned release prefix behind' >&2; exit 1; }
[[ ! -e /etc/systemd/system/nodedance.service ]] || { echo 'uninstaller left the Core unit behind' >&2; exit 1; }
if systemctl is-active --quiet nodedance.service; then
  echo 'Core service remained active after uninstall' >&2
  exit 1
fi
[[ "$(sudo cat "$MARKER")" == "NodeDance S17 release business data ${SUFFIX}" ]] || {
  echo 'uninstall removed Core business data' >&2
  exit 1
}
echo 'S17-10 real Core/systemd install-health-uninstall-data-preservation PASS'
