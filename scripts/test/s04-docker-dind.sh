#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENGINE="${1:-28}"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || { echo 'usage: s04-docker-dind.sh 28|29' >&2; exit 2; }

GO_BIN="$ROOT/.tools/go1.26.8/bin/go"
[[ -x "$GO_BIN" ]] || { echo 'S04 DIND NOT_READY: locked Go 1.26.8 binary is unavailable under .tools' >&2; exit 3; }
GO_VERSION="$(GOTOOLCHAIN=local "$GO_BIN" version)"
[[ "$GO_VERSION" == 'go version go1.26.8 '* ]] || { echo "S04 DIND NOT_READY: expected locked Go 1.26.8, found $GO_VERSION" >&2; exit 3; }

ARTIFACT_DIR="$ROOT/.artifacts/s04"
mkdir -p "$ARTIFACT_DIR"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM"
ARTIFACT="$ARTIFACT_DIR/dind-engine${ENGINE}-${RUN_ID}.log"

set -o pipefail
{
  echo "S04 DIND run=$RUN_ID engine=$ENGINE started=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  "$ROOT/scripts/test/dind.sh" start "$ENGINE"
  NODEDANCE_S04_DIND_HOST="unix://$ROOT/.artifacts/dind/v$ENGINE/socket/docker.sock" \
    GOTOOLCHAIN=local "$GO_BIN" test -race -mod=readonly -timeout=90s -count=1 -v \
      ./internal/agent/docker -run '^TestDINDEventLifetimeAndReconnectSnapshot$'
  echo "S04 DIND run=$RUN_ID engine=$ENGINE finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} 2>&1 | tee "$ARTIFACT"

echo "S04 DIND evidence: $ARTIFACT"
