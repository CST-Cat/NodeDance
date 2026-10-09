#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENGINE="${1:-}"
RUN_ID="${2:-}"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || { echo 'usage: s08-image-engine-dind.sh ENGINE(28|29) RUN_ID' >&2; exit 2; }
[[ "$RUN_ID" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]{0,35}$ ]] || { echo 'S08 DIND NOT_READY: invalid run ID' >&2; exit 2; }

DIND_ROOT="$ROOT/.artifacts/s08/dind/v$ENGINE/$RUN_ID"
MARKER="$DIND_ROOT/owner.json"
EXPECTED_SOCKET="$DIND_ROOT/socket/docker.sock"
GO_BIN="$ROOT/.tools/go1.26.8/bin/go"
if [[ ! -f "$MARKER" ]]; then
  echo "S08 DIND NOT_READY: this run's Engine owner marker is missing: $MARKER" >&2
  exit 3
fi
python3 - "$MARKER" "$ENGINE" "$RUN_ID" "$EXPECTED_SOCKET" <<'PY'
import json,sys
path,engine,run_id,socket=sys.argv[1:]
data=json.load(open(path))
assert data.get("suite")=="nodedance-s08-dind", "owner marker belongs to another suite"
assert data.get("run_id")==run_id, "owner marker run ID differs"
assert data.get("socket")==socket, "owner marker socket differs"
assert data.get("server_version", "").startswith(engine+"."), "owner marker Engine version differs"
assert data.get("host_daemon"), "owner marker has no host daemon identity"
PY
[[ -x "$GO_BIN" ]] || { echo "S08 DIND NOT_READY: locked Go toolchain is missing: $GO_BIN" >&2; exit 3; }
GO_VERSION="$(GOTOOLCHAIN=local "$GO_BIN" version)"
[[ "$GO_VERSION" == 'go version go1.26.8 '* ]] || { echo "S08 DIND NOT_READY: expected locked Go 1.26.8, found $GO_VERSION" >&2; exit 3; }

ARTIFACT_DIR="$ROOT/.artifacts/s08"
mkdir -p "$ARTIFACT_DIR"
ARTIFACT="$ARTIFACT_DIR/image-engine${ENGINE}-${RUN_ID}.log"
AUTH_USER="nodedance-s08-user-$RUN_ID"
AUTH_PASSWORD="nodedance-s08-password-$RUN_ID"
echo "S08 real image Engine run=$RUN_ID engine=$ENGINE started=$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$ARTIFACT"
set +e
NODEDANCE_S08_DIND_ROOT="$DIND_ROOT" NODEDANCE_S08_RUN_ID="$RUN_ID" \
  NODEDANCE_S08_REGISTRY_USER="$AUTH_USER" NODEDANCE_S08_REGISTRY_PASSWORD="$AUTH_PASSWORD" GOTOOLCHAIN=local \
  "$GO_BIN" test -mod=readonly -count=1 -timeout=8m -v ./internal/agent/images -run '^TestDINDImageManagementRealEngine$' \
  >> "$ARTIFACT" 2>&1
TEST_STATUS=$?
set -e
echo "S08 real image Engine run=$RUN_ID engine=$ENGINE finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$ARTIFACT"
if grep -Fq -- "$AUTH_USER" "$ARTIFACT" || grep -Fq -- "$AUTH_PASSWORD" "$ARTIFACT" || grep -Fq -- "$AUTH_PASSWORD-wrong" "$ARTIFACT"; then
  echo "S08 credential canary appeared in test output; details withheld; inspect the protected run artifact: $ARTIFACT" >&2
  exit 1
fi
cat "$ARTIFACT"
[[ "$TEST_STATUS" == 0 ]] || exit "$TEST_STATUS"
echo "S08 DIND evidence: $ARTIFACT"
