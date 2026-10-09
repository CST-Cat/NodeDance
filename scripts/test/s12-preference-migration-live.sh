#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ -n "${NODEDANCE_GO_BIN:-}" ]]; then
  GO_BIN="$NODEDANCE_GO_BIN"
elif [[ -x "$ROOT/.tools/go1.26.8/bin/go" ]]; then
  GO_BIN="$ROOT/.tools/go1.26.8/bin/go"
else
  GO_BIN="$(command -v go)"
fi
GO_VERSION="$(GOTOOLCHAIN=local "$GO_BIN" version)"
[[ "$GO_VERSION" == 'go version go1.26.8 '* ]] || {
  echo "S12 preference migration candidate requires Go 1.26.8, found: $GO_VERSION" >&2
  exit 3
}

ARTIFACT_DIR="$ROOT/.artifacts/s12"
mkdir -p "$ARTIFACT_DIR"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM"
ARTIFACT="$ARTIFACT_DIR/preference-migration-live-${RUN_ID}.log"

set -o pipefail
{
  echo "S12 preference migration run=$RUN_ID started=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "The test pins DOCKER_HOST to a nonexistent socket under testing.T.TempDir and does not use Docker/DIND."
  GOTOOLCHAIN=local "$GO_BIN" test -mod=readonly -timeout=2m -count=1 -v \
    ./internal/core/server -run '^TestS12RebuildPreferenceMigrationHTTPSAgentWSSEndToEnd$'
  echo "S12 preference migration run=$RUN_ID finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} 2>&1 | tee "$ARTIFACT"

echo "S12 preference migration candidate evidence: $ARTIFACT"
