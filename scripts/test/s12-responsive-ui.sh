#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NODE_BIN="$ROOT/.tools/node-v22.23.3/bin"
PNPM_BIN="$ROOT/.tools/pnpm/node_modules/.bin"
export PATH="$NODE_BIN:$PNPM_BIN:$PATH"
NODE="$NODE_BIN/node"
[[ -x "$NODE" && "$($NODE --version)" == 'v22.23.3' ]] || { echo 'S12 UI NOT_READY: locked Node 22.23.3 is unavailable' >&2; exit 3; }

PORT=4189
ORIGIN="http://127.0.0.1:$PORT"
ARTIFACT_DIR="$ROOT/.artifacts/s12"
mkdir -p "$ARTIFACT_DIR"
VITE_LOG="$ARTIFACT_DIR/responsive-ui-vite.log"
VITE_PID=""
cleanup() {
  local result=$?
  trap - EXIT
  if [[ -n "$VITE_PID" ]] && kill -0 "$VITE_PID" 2>/dev/null; then
    kill "$VITE_PID" 2>/dev/null || true
    wait "$VITE_PID" 2>/dev/null || true
  fi
  exit "$result"
}
trap cleanup EXIT

pnpm --dir "$ROOT/web" exec vite --host 127.0.0.1 --port "$PORT" --strictPort >"$VITE_LOG" 2>&1 &
VITE_PID=$!
ready=0
for _ in $(seq 1 100); do
  if curl --fail --silent "$ORIGIN/tests/fixtures/s03-nodes-dashboard.html" -o /dev/null; then
    ready=1
    break
  fi
  if ! kill -0 "$VITE_PID" 2>/dev/null; then
    cat "$VITE_LOG" >&2
    echo 'S12 UI FAIL: owned Vite process exited before readiness' >&2
    exit 1
  fi
  sleep 0.1
done
if [[ "$ready" != 1 ]]; then
  cat "$VITE_LOG" >&2
  echo "S12 UI FAIL: owned Vite server did not become ready at $ORIGIN" >&2
  exit 1
fi

PLAYWRIGHT_BASE_URL="$ORIGIN" pnpm --dir "$ROOT/web" exec playwright test \
  --config playwright.config.ts --project=chromium tests/s12-container-rebuild.spec.ts
