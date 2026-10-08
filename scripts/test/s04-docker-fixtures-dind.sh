#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENGINE="${1:-28}"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || { echo 'usage: s04-docker-fixtures-dind.sh 28|29' >&2; exit 2; }

GO_BIN="$ROOT/.tools/go1.26.8/bin/go"
[[ -x "$GO_BIN" ]] || { echo 'S04 DIND NOT_READY: locked Go 1.26.8 binary is unavailable under .tools' >&2; exit 3; }
GO_VERSION="$(GOTOOLCHAIN=local "$GO_BIN" version)"
[[ "$GO_VERSION" == 'go version go1.26.8 '* ]] || { echo "S04 DIND NOT_READY: expected locked Go 1.26.8, found $GO_VERSION" >&2; exit 3; }

ARTIFACT_DIR="$ROOT/.artifacts/s04"
mkdir -p "$ARTIFACT_DIR"
RUN_ID="nd-s04-$(date -u +%Y%m%dt%H%M%Sz)-$RANDOM"
ENDPOINT="unix://$ROOT/.artifacts/dind/v$ENGINE/socket/docker.sock"
ARTIFACT="$ARTIFACT_DIR/fixtures-engine${ENGINE}-${RUN_ID}.log"
RUN_ROOT="$ROOT/.artifacts/fixtures/$RUN_ID"

finish() {
  result=$?
  if [[ -f "$RUN_ROOT/.nodedance-fixture-owner" ]]; then
    NODEDANCE_TEST_DOCKER_HOST="$ENDPOINT" "$ROOT/scripts/test/fixtures.sh" clean "$RUN_ID" || result=1
  fi
  echo "S04 DIND fixture run=$RUN_ID engine=$ENGINE cleanup_status=$result finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  exit "$result"
}
trap finish EXIT

set -o pipefail
{
  echo "S04 DIND fixture run=$RUN_ID engine=$ENGINE started=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  "$ROOT/scripts/test/dind.sh" start "$ENGINE"
  NODEDANCE_TEST_DOCKER_HOST="$ENDPOINT" "$ROOT/scripts/test/fixtures.sh" create "$RUN_ID"
  NODEDANCE_S04_DIND_HOST="$ENDPOINT" NODEDANCE_S04_FIXTURE_RUN_ID="$RUN_ID" GOTOOLCHAIN=local \
    "$GO_BIN" test -race -mod=readonly -timeout=75s -count=1 -v \
      ./internal/agent/docker -run '^TestDINDInventoryMatchesOwnedFixtures$'
} 2>&1 | tee "$ARTIFACT"

echo "S04 DIND fixture evidence: $ARTIFACT"
