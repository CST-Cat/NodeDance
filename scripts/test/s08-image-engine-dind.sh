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
AUTH_USER="nodedance-s08-user-$RUN_ID"
AUTH_PASSWORD="nodedance-s08-password-$RUN_ID"
TEST_PACKAGE="${NODEDANCE_S08_TEST_PACKAGE:-./internal/agent/images}"
case "$TEST_PACKAGE" in
  ./internal/agent/images)
    ARTIFACT_STEM="image-engine"
    DEFAULT_TEST_PATTERN='^TestDINDImageManagementRealEngine$'
    SLOW_PROXY_BIN="$ARTIFACT_DIR/s08-slow-proxy-${RUN_ID}"
    ENGINE_ARCH="$(docker --host "unix://$EXPECTED_SOCKET" version --format '{{.Server.Arch}}')"
    [[ "$ENGINE_ARCH" == amd64 || "$ENGINE_ARCH" == arm64 ]] || { echo "S08 DIND NOT_READY: unsupported Engine architecture: $ENGINE_ARCH" >&2; exit 3; }
    GOOS=linux GOARCH="$ENGINE_ARCH" CGO_ENABLED=0 GOTOOLCHAIN=local \
      "$GO_BIN" build -mod=readonly -trimpath -o "$SLOW_PROXY_BIN" ./scripts/test/s08-slow-proxy
    ;;
  ./internal/core/server)
    ARTIFACT_STEM="core-image-auth"
    DEFAULT_TEST_PATTERN='^TestS08CoreAgentAuthenticatedImagePullOnOwnedDIND$'
    SLOW_PROXY_BIN=""
    ;;
  *)
    echo "S08 DIND NOT_READY: unsupported test package: $TEST_PACKAGE" >&2
    exit 2
    ;;
esac
ARTIFACT="$ARTIFACT_DIR/${ARTIFACT_STEM}-engine${ENGINE}-${RUN_ID}.log"
TEST_PATTERN="${NODEDANCE_S08_TEST_PATTERN:-$DEFAULT_TEST_PATTERN}"
echo "S08 real image Engine run=$RUN_ID engine=$ENGINE started=$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$ARTIFACT"
set +e
NODEDANCE_S08_DIND_ROOT="$DIND_ROOT" NODEDANCE_S08_RUN_ID="$RUN_ID" \
  NODEDANCE_S08_REGISTRY_USER="$AUTH_USER" NODEDANCE_S08_REGISTRY_PASSWORD="$AUTH_PASSWORD" \
  NODEDANCE_S08_SLOW_PROXY_BIN="$SLOW_PROXY_BIN" GOTOOLCHAIN=local \
  "$GO_BIN" test -mod=readonly -count=1 -timeout=8m -v "$TEST_PACKAGE" -run "$TEST_PATTERN" \
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
if [[ -n "$SLOW_PROXY_BIN" ]]; then rm -f "$SLOW_PROXY_BIN"; fi
echo "S08 DIND evidence: $ARTIFACT"
