#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nodedance-check-tools-host-shell.XXXXXX")"
cleanup() {
  python3 -c 'import pathlib,shutil,sys; shutil.rmtree(pathlib.Path(sys.argv[1]), ignore_errors=True)' "$TEMP_DIR"
}
trap cleanup EXIT

CLEAN_ROOT="$TEMP_DIR/source"
python3 - "$ROOT" "$CLEAN_ROOT" <<'PY'
import pathlib
import shutil
import sys

source, destination = map(pathlib.Path, sys.argv[1:])
ignored = shutil.ignore_patterns(
    ".git", ".tools", ".build", ".artifacts", "node_modules", "dist",
    "__pycache__", "*.pyc", "coverage.out", "coverage.html",
)
shutil.copytree(source, destination, ignore=ignored)
if (destination / ".tools").exists():
    raise SystemExit("clean-source test unexpectedly copied the local tool cache")
if (destination / ".artifacts").exists():
    raise SystemExit("clean-source test unexpectedly copied local run artifacts")
PY

mkdir -p "$TEMP_DIR/host-bin"
cat > "$TEMP_DIR/host-bin/go" <<'SH'
#!/usr/bin/env bash
printf 'called\n' >> "$NODEDANCE_TEST_HOST_GO_SENTINEL"
printf 'host Go shim must not be executed\n' >&2
exit 97
SH
chmod 755 "$TEMP_DIR/host-bin/go"
export NODEDANCE_TEST_HOST_GO_SENTINEL="$TEMP_DIR/host-go-called"
HOST_PATH="$TEMP_DIR/host-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

run_case() {
  local name="$1"
  shift
  local log="$TEMP_DIR/$name.log"
  if ! env GOTOOLCHAIN=auto PATH="$HOST_PATH" "$@" >"$log" 2>&1; then
    cat "$log" >&2
    echo "clean-source host-shell verification failed: $name" >&2
    return 1
  fi
  if [[ -e "$NODEDANCE_TEST_HOST_GO_SENTINEL" ]]; then
    cat "$log" >&2
    echo "clean-source verification called the parent Go shim: $name" >&2
    return 1
  fi
  if ! grep -Fq 'Toolchain PASS:' "$log" || ! grep -Fq 'GOTOOLCHAIN=local' "$log"; then
    cat "$log" >&2
    echo "clean-source verification did not prove locked local Go: $name" >&2
    return 1
  fi
  if grep -Fq 'go: downloading go' "$log"; then
    cat "$log" >&2
    echo "clean-source verification attempted an implicit toolchain download: $name" >&2
    return 1
  fi
}

run_case make-entry make --no-print-directory -C "$CLEAN_ROOT" verify-tools
run_case standalone "$CLEAN_ROOT/scripts/check-tools.sh"
echo "Clean-source host-shell Go isolation PASS: GOTOOLCHAIN=auto parent did not execute host Go or trigger an implicit toolchain download"
