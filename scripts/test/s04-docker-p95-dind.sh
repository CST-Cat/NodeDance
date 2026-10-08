#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENGINE="${1:-}"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || { echo 'usage: s04-docker-p95-dind.sh 28|29' >&2; exit 2; }

GO_BIN="$ROOT/.tools/go1.26.8/bin/go"
[[ -x "$GO_BIN" ]] || { echo 'S04 P95 NOT_READY: locked Go 1.26.8 binary is unavailable under .tools' >&2; exit 3; }
GO_VERSION="$(GOTOOLCHAIN=local "$GO_BIN" version)"
[[ "$GO_VERSION" == 'go version go1.26.8 '* ]] || { echo "S04 P95 NOT_READY: expected locked Go 1.26.8, found $GO_VERSION" >&2; exit 3; }

SOCKET="$ROOT/.artifacts/dind/v$ENGINE/socket/docker.sock"
MARKER="$ROOT/.artifacts/dind/v$ENGINE/owner.json"
ENDPOINT="unix://$SOCKET"
[[ -S "$SOCKET" ]] || { echo "S04 P95 NOT_READY: owned Engine $ENGINE socket is unavailable: $SOCKET" >&2; exit 3; }
[[ -f "$MARKER" ]] || { echo "S04 P95 NOT_READY: owned Engine $ENGINE marker is unavailable: $MARKER" >&2; exit 3; }
python3 - "$MARKER" "$SOCKET" "$ENGINE" <<'PY'
import json, os, sys
marker = json.load(open(sys.argv[1], encoding="utf-8"))
expected_socket = os.path.realpath(sys.argv[2])
assert marker.get("suite") == "nodedance-s00-dind", marker
assert os.path.realpath(marker.get("socket", "")) == expected_socket, marker
assert marker.get("server_version", "").startswith(sys.argv[3] + "."), marker
PY

# This runner deliberately does not start, stop, restart, or reconfigure DIND.
# It only runs the opt-in test against the exact already-running owned socket.
SERVER_VERSION="$(docker --host "$ENDPOINT" version --format '{{.Server.Version}}' 2>/dev/null)" || {
  echo "S04 P95 NOT_READY: owned Engine $ENGINE is not responding; start it through the repository harness first" >&2
  exit 3
}
[[ "$SERVER_VERSION" == "$ENGINE."* ]] || { echo "S04 P95 NOT_READY: expected Engine $ENGINE, found $SERVER_VERSION" >&2; exit 3; }

ARTIFACT_DIR="$ROOT/.artifacts/s04"
mkdir -p "$ARTIFACT_DIR"
RUN_ID="nd-s04-p95-$(date -u +%y%m%d%H%M%S)-$RANDOM"
ARTIFACT="$ARTIFACT_DIR/p95-engine${ENGINE}-${RUN_ID}.log"

finish() {
  result=$?
  remaining="$(docker --host "$ENDPOINT" ps -aq --filter "label=io.nodedance.suite=$RUN_ID" 2>&1)" || {
    printf 'cleanup verification failed: could not query only run label %s: %s\n' "$RUN_ID" "$remaining" | tee -a "$ARTIFACT" >&2
    result=1
  }
  if [[ -n "$remaining" ]]; then
    printf 'cleanup verification failed: exact run label %s still owns container IDs: %s\n' "$RUN_ID" "$remaining" | tee -a "$ARTIFACT" >&2
    result=1
  else
    printf 'cleanup verification passed: exact run label %s has no containers\n' "$RUN_ID" | tee -a "$ARTIFACT" >&2
  fi
  printf 'S04 module P95 runner run=%s engine=%s exit_status=%s finished=%s\n' "$RUN_ID" "$ENGINE" "$result" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "$ARTIFACT" >&2
  printf 'S04 module P95 evidence retained at %s\n' "$ARTIFACT" >&2
  exit "$result"
}
trap finish EXIT

set -o pipefail
{
  echo "S04 module P95 run=$RUN_ID engine=$ENGINE server=$SERVER_VERSION started=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "scope=isolated-agent-module observer; full_Core_Agent_WebSocket_S04-SUP-01=false"
  echo "dind_policy=already-running-owned-socket-only; no daemon lifecycle commands"
  NODEDANCE_S04_DIND_HOST="$ENDPOINT" \
    NODEDANCE_S04_DIND_ENGINE="$ENGINE" \
    NODEDANCE_S04_P95_RUN_ID="$RUN_ID" \
    GOTOOLCHAIN=local \
    "$GO_BIN" test -race -mod=readonly -timeout=8m -count=1 -v \
      ./internal/agent/docker -run '^TestDIND100ExternalChangesConvergenceP95$'
} 2>&1 | tee "$ARTIFACT"
