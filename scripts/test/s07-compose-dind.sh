#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENGINE="${1:-}"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || { echo 'usage: s07-compose-dind.sh 28|29' >&2; exit 2; }
DIND_ROOT="${NODEDANCE_S07_DIND_ROOT:-$ROOT/.artifacts/dind/v$ENGINE}"
DIND_ROOT="$(realpath -m "$DIND_ROOT")"
EXPECTED_ROOT="$(realpath -m "$ROOT/.artifacts/dind/v$ENGINE")"
[[ "$DIND_ROOT" == "$EXPECTED_ROOT" ]] || { echo "S07 DIND NOT_READY: expected this checkout's marked fixture at $EXPECTED_ROOT" >&2; exit 3; }

GO_BIN="$ROOT/.tools/go1.26.8/bin/go"
[[ -x "$GO_BIN" ]] || { echo 'S07 DIND NOT_READY: locked Go 1.26.8 binary is unavailable' >&2; exit 3; }
GO_VERSION="$(GOTOOLCHAIN=local "$GO_BIN" version)"
[[ "$GO_VERSION" == 'go version go1.26.8 '* ]] || { echo "S07 DIND NOT_READY: expected locked Go 1.26.8, found $GO_VERSION" >&2; exit 3; }

source "$ROOT/scripts/docker-test-config.sh"
ARTIFACT_DIR="$ROOT/.artifacts/s07"
mkdir -p "$ARTIFACT_DIR"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM"
ARTIFACT="$ARTIFACT_DIR/compose-dind-engine${ENGINE}-${RUN_ID}.log"

set -o pipefail
{
  echo "S07 DIND run=$RUN_ID engine=$ENGINE started=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  [[ -f "$DIND_ROOT/owner.json" ]] || { echo 'S07 DIND NOT_READY: owner marker is missing' >&2; exit 3; }
  NODEDANCE_S07_DIND_ROOT="$DIND_ROOT" GOTOOLCHAIN=local "$GO_BIN" test -race -mod=readonly -timeout=15m -count=1 -v \
    ./internal/agent/compose -run '^TestDINDComposeLifecycle$'
  echo "S07 DIND run=$RUN_ID engine=$ENGINE finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} 2>&1 | tee "$ARTIFACT"

echo "S07 DIND evidence: $ARTIFACT"
