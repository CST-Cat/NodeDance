#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENGINE="${1:-}"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || { echo 'usage: s12-container-rebuild-dind.sh 28|29' >&2; exit 2; }
DIND_ROOT="${NODEDANCE_S12_DIND_ROOT:-$ROOT/.artifacts/dind/v$ENGINE}"
DIND_ROOT="$(realpath -m "$DIND_ROOT")"
EXPECTED_ROOT="$(realpath -m "$ROOT/.artifacts/dind/v$ENGINE")"
[[ "$DIND_ROOT" == "$EXPECTED_ROOT" ]] || { echo "S12 DIND NOT_READY: expected this checkout's marked fixture at $EXPECTED_ROOT" >&2; exit 3; }
[[ -f "$DIND_ROOT/owner.json" ]] || { echo 'S12 DIND NOT_READY: owner marker is missing' >&2; exit 3; }
python3 - "$DIND_ROOT/owner.json" "$ENGINE" "$DIND_ROOT/socket/docker.sock" <<'PY'
import json,sys
owner=json.load(open(sys.argv[1]))
assert owner.get("suite")=="nodedance-s00-dind"
assert owner.get("socket")==sys.argv[3]
assert owner.get("host_daemon")
assert owner.get("server_version", "").startswith(sys.argv[2]+".")
PY

GO_BIN="$ROOT/.tools/go1.26.8/bin/go"
[[ -x "$GO_BIN" ]] || { echo 'S12 DIND NOT_READY: locked Go 1.26.8 binary is unavailable' >&2; exit 3; }
GO_VERSION="$(GOTOOLCHAIN=local "$GO_BIN" version)"
[[ "$GO_VERSION" == 'go version go1.26.8 '* ]] || { echo "S12 DIND NOT_READY: expected locked Go 1.26.8, found $GO_VERSION" >&2; exit 3; }

ARTIFACT_DIR="$ROOT/.artifacts/s12"
mkdir -p "$ARTIFACT_DIR"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM"
ARTIFACT="$ARTIFACT_DIR/rebuild-dind-engine${ENGINE}-${RUN_ID}.log"

set -o pipefail
{
  echo "S12 DIND run=$RUN_ID engine=$ENGINE started=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  NODEDANCE_S12_DIND_ROOT="$DIND_ROOT" GOTOOLCHAIN=local "$GO_BIN" test -race -mod=readonly -timeout=15m -count=1 -v \
    ./internal/agent/containerrebuild -run '^TestDINDRebuildLifecycle$'
  echo "S12 DIND run=$RUN_ID engine=$ENGINE finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} 2>&1 | tee "$ARTIFACT"

echo "S12 DIND evidence: $ARTIFACT"
