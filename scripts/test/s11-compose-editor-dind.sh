#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENGINE="${1:-}"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || { echo 'usage: s11-compose-editor-dind.sh ENGINE(28|29)' >&2; exit 2; }

DIND_ROOT="$ROOT/.artifacts/dind/v$ENGINE"
MARKER="$DIND_ROOT/owner.json"
FIXTURE_ROOT="$ROOT/.artifacts/fixtures/s11-engine${ENGINE}-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
GO_BIN="$ROOT/.tools/go1.26.8/bin/go"

if [[ ! -f "$MARKER" ]]; then
  echo "S11 NOT_READY: Engine $ENGINE owner marker is missing; refusing to use any Docker daemon" >&2
  exit 3
fi
if [[ ! -x "$GO_BIN" ]]; then
  echo "S11 NOT_READY: locked Go toolchain is missing: $GO_BIN" >&2
  exit 3
fi

python3 - "$MARKER" "$DIND_ROOT/socket/docker.sock" "$ENGINE" <<'PY'
import json,sys
marker,socket,engine=sys.argv[1:]
d=json.load(open(marker))
assert d.get("suite")=="nodedance-s00-dind", "DIND marker belongs to another suite"
assert d.get("socket")==socket, "DIND marker socket path differs"
assert d.get("server_version", "").startswith(engine+"."), "DIND marker engine version differs"
PY

# Resolve exactly the locked Compose plugin in this job-local Docker config;
# never depend on whichever plugin happens to be installed on the runner.
source "$ROOT/scripts/docker-test-config.sh"

mkdir -p "$ROOT/.artifacts/s11"
if [[ -e "$FIXTURE_ROOT" ]]; then
  echo "S11 NOT_READY: run-scoped fixture path already exists; preserving it: $FIXTURE_ROOT" >&2
  exit 3
fi

export NODEDANCE_S11_DIND_ROOT="$DIND_ROOT"
export NODEDANCE_S11_FIXTURE_ROOT="$FIXTURE_ROOT"
export NODEDANCE_S11_RUN_ID="${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}-engine${ENGINE}"
export GOTOOLCHAIN=local

"$GO_BIN" test -count=1 -run '^TestDINDComposeEditorRealEngine$' -v ./internal/agent/composeedit \
  2>&1 | tee "$ROOT/.artifacts/s11/engine-${ENGINE}.log"
