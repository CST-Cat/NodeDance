#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GO_BIN="${GO_BIN:-go}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nodedance-s17-runtime.XXXXXXXX")"
CORE_PID=""
REPORT_PATH="${NODEDANCE_S17_REPORT_PATH:-}"
if [[ -z "$REPORT_PATH" ]]; then
  REPORT_PATH="$WORK/report.txt"
else
  mkdir -p -- "$(dirname -- "$REPORT_PATH")"
fi

cleanup() {
  local status=$?
  if [[ -n "$CORE_PID" ]] && kill -0 "$CORE_PID" 2>/dev/null; then
    kill -TERM "$CORE_PID" 2>/dev/null || true
    wait "$CORE_PID" 2>/dev/null || true
  fi
  # Remove the private temporary Core data, including its one-time setup
  # credential. Workflow evidence is written outside this directory.
  rm -rf -- "$WORK"
  return "$status"
}
trap cleanup EXIT

fail() {
  echo "S17 native runtime smoke: $*" >&2
  if [[ -f "$WORK/core.log" ]]; then
    echo '--- Core process log (temporary isolated data directory) ---' >&2
    cat "$WORK/core.log" >&2
    echo '--- end Core process log ---' >&2
  fi
  exit 1
}

cd "$ROOT"

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH=amd64 ;;
  aarch64|arm64) HOST_ARCH=arm64 ;;
  *) fail "unsupported native runner architecture: $(uname -m)" ;;
esac
if [[ "$(uname -s)" != Linux ]]; then
  fail "expected Linux runner, got $(uname -s)"
fi
EXPECTED_ARCH="${NODEDANCE_S17_EXPECT_ARCH:-$HOST_ARCH}"
[[ "$EXPECTED_ARCH" == "$HOST_ARCH" ]] || fail "runner architecture mismatch: expected $EXPECTED_ARCH, got $HOST_ARCH"

mapfile -t GO_TARGET < <(env -u GOOS -u GOARCH -u GOARM -u GOAMD64 "$GO_BIN" env GOOS GOARCH)
GOOS="${GO_TARGET[0]:-}"
GOARCH="${GO_TARGET[1]:-}"
[[ "$GOOS" == linux && "$GOARCH" == "$HOST_ARCH" ]] || \
  fail "Go toolchain target is not native Linux/$HOST_ARCH: $GOOS/$GOARCH"

mkdir -m 700 "$WORK/home" "$WORK/config" "$WORK/xdg-data" "$WORK/core-data"
printf '{}\n' > "$WORK/core.json"
chmod 600 "$WORK/core.json"

build_env=(env -u GOOS -u GOARCH -u GOARM -u GOAMD64 CGO_ENABLED=0)
"${build_env[@]}" "$GO_BIN" build -trimpath -buildvcs=false \
  -ldflags '-X main.version=s17-runtime-smoke' \
  -o "$WORK/nodedance" ./cmd/nodedance
"${build_env[@]}" "$GO_BIN" build -trimpath -buildvcs=false \
  -ldflags '-X main.version=s17-runtime-smoke' \
  -o "$WORK/nodedance-agent" ./cmd/nodedance-agent

CORE_VERSION="$(env -i PATH="$PATH" HOME="$WORK/home" XDG_CONFIG_HOME="$WORK/config" \
  "$WORK/nodedance" version)"
AGENT_VERSION="$(env -i PATH="$PATH" HOME="$WORK/home" XDG_CONFIG_HOME="$WORK/config" \
  "$WORK/nodedance-agent" version)"
[[ "$CORE_VERSION" == 'NodeDance s17-runtime-smoke' ]] || fail "unexpected Core version output: $CORE_VERSION"
[[ "$AGENT_VERSION" == 'NodeDance Agent s17-runtime-smoke' ]] || fail "unexpected Agent version output: $AGENT_VERSION"

# Test the published default listener first. On a developer host where another
# process already owns 8180, use a fresh OS-assigned loopback port and record
# that the default-port portion was blocked. CI expects 8180 to be available.
LISTEN_PORT=8180
PORT_SELECTION="default 127.0.0.1:8180"
if ! python3 - "$LISTEN_PORT" <<'PY'
import socket, sys
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    sock.bind(("127.0.0.1", int(sys.argv[1])))
except OSError:
    raise SystemExit(1)
finally:
    sock.close()
PY
then
  LISTEN_PORT="$(python3 <<'PY'
import socket
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
)"
  PORT_SELECTION="alternate 127.0.0.1:$LISTEN_PORT because default 127.0.0.1:8180 is occupied"
fi
LISTEN="127.0.0.1:$LISTEN_PORT"

# Use an empty environment and dedicated config/home/data paths. This avoids
# reading or changing the caller's NodeDance configuration and secrets.
env -i PATH="$PATH" HOME="$WORK/home" XDG_CONFIG_HOME="$WORK/config" \
  XDG_DATA_HOME="$WORK/xdg-data" LANG=C.UTF-8 LC_ALL=C.UTF-8 \
  "$WORK/nodedance" serve --listen "$LISTEN" --data-dir "$WORK/core-data" \
  --config "$WORK/core.json" >"$WORK/core.log" 2>&1 &
CORE_PID=$!

HEALTH_BODY=""
for _ in $(seq 1 80); do
  if ! kill -0 "$CORE_PID" 2>/dev/null; then
    wait "$CORE_PID" || true
    CORE_PID=""
    fail 'Core exited before health became ready'
  fi
  if HEALTH_BODY="$(curl --fail --silent --show-error --max-time 1 "http://$LISTEN/api/v1/health" 2>/dev/null)"; then
    break
  fi
  sleep 0.25
done
[[ -n "$HEALTH_BODY" ]] || fail "Core health endpoint did not become ready on $LISTEN"
python3 - "$HEALTH_BODY" <<'PY' || fail 'Core health response was invalid'
import json, sys
data = json.loads(sys.argv[1])
if data.get("status") != "ok" or data.get("version") != "s17-runtime-smoke":
    raise SystemExit(f"unexpected health response: {data!r}")
PY

python3 - "$LISTEN_PORT" "$CORE_PID" <<'PY' || fail 'Core listener was not proven to be loopback-only'
import subprocess, sys
port, pid = sys.argv[1:]
result = subprocess.run(["ss", "-H", "-ltnp"], check=True, text=True, capture_output=True)
lines = [line for line in result.stdout.splitlines() if f"pid={pid}," in line]
if len(lines) != 1:
    raise SystemExit(f"expected exactly one Core listener, got {lines!r}")
fields = lines[0].split()
local = fields[3] if len(fields) >= 5 else ""
owner = fields[-1]
if local != f"127.0.0.1:{port}" or f"pid={pid}," not in owner:
    raise SystemExit(f"listener is not the expected Core loopback socket: {lines[0]!r}")
print(f"verified listener: {local}; owner PID {pid}; no wildcard/public bind")
PY

kill -TERM "$CORE_PID"
STOPPED_PID="$CORE_PID"
set +e
wait "$CORE_PID"
CORE_EXIT=$?
set -e
CORE_PID=""
[[ "$CORE_EXIT" == 0 ]] || fail "Core did not exit cleanly after SIGTERM (status $CORE_EXIT)"
if ss -H -ltnp | grep -F "pid=$STOPPED_PID," >/dev/null 2>&1; then
  fail "Core PID $STOPPED_PID still owns a listening socket after clean exit"
fi

{
  echo 'S17-03 native runtime smoke: PASS'
  echo "host_architecture=$HOST_ARCH"
  echo "go_target=$GOOS/$GOARCH"
  echo "core_version=$CORE_VERSION"
  echo "agent_version=$AGENT_VERSION"
  echo "listener_selection=$PORT_SELECTION"
  echo "health_response=$HEALTH_BODY"
  echo 'listener_binding=127.0.0.1 only; owning Core PID confirmed with ss'
  echo 'shutdown=SIGTERM; exit_status=0'
  echo 'agent_enrollment=not run; Docker Engine/container integration=not covered by this smoke'
} | tee "$REPORT_PATH"

if [[ -f "$WORK/core.log" ]]; then
  echo 'Core startup log:'
  sed -E 's#(/[^[:space:]]*/setup-credential)([^[:space:]]*)#\1[redacted]#g' "$WORK/core.log"
fi
