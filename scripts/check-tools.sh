#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCK="$ROOT/toolchain.lock.json"
EXPECTED_GO="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["go"]["version"])' "$LOCK")"
EXPECTED_NODE="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["node"]["version"])' "$LOCK")"
EXPECTED_PNPM="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pnpm"]["version"])' "$LOCK")"
EXPECTED_COMPOSE="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["compose"]["version"])' "$LOCK")"
GO_BIN="$ROOT/.tools/go$EXPECTED_GO/bin/go"
NODE_BIN="$ROOT/.tools/node-v$EXPECTED_NODE/bin/node"
PNPM_BIN="$ROOT/.tools/pnpm/node_modules/.bin/pnpm"
[[ -x "$GO_BIN" ]] || { echo "locked Go binary is missing: $GO_BIN (run make bootstrap)" >&2; exit 1; }
[[ -x "$NODE_BIN" ]] || { echo "locked Node.js binary is missing: $NODE_BIN (run make bootstrap)" >&2; exit 1; }
export PATH="$(dirname "$GO_BIN"):$(dirname "$NODE_BIN"):$ROOT/.tools/pnpm/node_modules/.bin:$PATH"
export GOTOOLCHAIN=local
[[ "$(command -v go)" == "$GO_BIN" ]] || { echo "locked Go is not first on PATH: $(command -v go)" >&2; exit 1; }
[[ "$(command -v node)" == "$NODE_BIN" ]] || { echo "locked Node.js is not first on PATH: $(command -v node)" >&2; exit 1; }
PNPM_EXPECTED_SHA="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["pnpm"]["native_packages"]["linux/"+sys.argv[2]]["binary_sha256"])' "$LOCK" "$(go env GOARCH)")"
python3 - "$PNPM_BIN" <<'PY' || { echo 'locked pnpm must be installed as a native ELF before checks run' >&2; exit 1; }
import pathlib,sys
path=pathlib.Path(sys.argv[1])
try:
    with path.open('rb') as stream: magic=stream.read(4)
except OSError as error:
    raise SystemExit(str(error))
if magic!=b'\x7fELF': raise SystemExit(f"pnpm executable is not native ELF: {magic!r}")
PY
actual_pnpm_sha="$(sha256sum "$PNPM_BIN" | awk '{print $1}')"
[[ "$actual_pnpm_sha" == "$PNPM_EXPECTED_SHA" ]] || { echo "pnpm native binary SHA-256 mismatch: expected $PNPM_EXPECTED_SHA, got $actual_pnpm_sha" >&2; exit 1; }
actual_go="$(go version | awk '{print $3}' | sed 's/^go//')"
actual_node="$(node --version | sed 's/^v//')"
actual_pnpm="$("$PNPM_BIN" --version)"
isolated_pnpm="$(env -i HOME="$ROOT/.tools/pnpm/empty-home" PATH=/nonexistent "$PNPM_BIN" --version)"
[[ "$actual_go" == "$EXPECTED_GO" ]] || { echo "Go mismatch: expected $EXPECTED_GO, got $actual_go" >&2; exit 1; }
[[ "$actual_node" == "$EXPECTED_NODE" ]] || { echo "Node mismatch: expected $EXPECTED_NODE, got $actual_node" >&2; exit 1; }
[[ "$actual_pnpm" == "$EXPECTED_PNPM" ]] || { echo "pnpm mismatch: expected $EXPECTED_PNPM, got $actual_pnpm" >&2; exit 1; }
[[ "$isolated_pnpm" == "$EXPECTED_PNPM" ]] || { echo "pnpm native direct-exec mismatch: expected $EXPECTED_PNPM, got $isolated_pnpm" >&2; exit 1; }
[[ "$(go env GOTOOLCHAIN)" == local ]] || { echo 'GOTOOLCHAIN must be local; implicit toolchain upgrades are prohibited' >&2; exit 1; }
GOTOOLCHAIN=local go mod verify
COMPOSE_BIN="$ROOT/.tools/docker/cli-plugins/docker-compose"
COMPOSE_SHA="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["compose"]["archives"][sys.argv[2]]["sha256"])' "$LOCK" "linux/$(go env GOARCH)")"
[[ -x "$COMPOSE_BIN" ]] || { echo "locked Docker Compose plugin is missing: $COMPOSE_BIN (run make bootstrap)" >&2; exit 1; }
echo "$COMPOSE_SHA  $COMPOSE_BIN" | sha256sum --check --status || { echo 'locked Docker Compose plugin SHA-256 mismatch' >&2; exit 1; }
actual_compose="$("$COMPOSE_BIN" version --short | sed 's/^v//')"
[[ "$actual_compose" == "$EXPECTED_COMPOSE" ]] || { echo "Compose mismatch: expected $EXPECTED_COMPOSE, got $actual_compose" >&2; exit 1; }
# Test Docker CLI plugin resolution using a repository-local Docker config. This
# does not contact a daemon and never reads or edits the user's ~/.docker.
source "$ROOT/scripts/docker-test-config.sh"
actual_cli_compose="$(docker --config "$DOCKER_CONFIG" compose version --short 2>/dev/null | sed 's/^v//')"
[[ "$actual_cli_compose" == "$EXPECTED_COMPOSE" ]] || { echo "Docker CLI did not resolve the locked Compose plugin: expected $EXPECTED_COMPOSE, got ${actual_cli_compose:-missing}" >&2; exit 1; }
python3 - "$ROOT" <<'PY'
import json, pathlib, re, sys
root = pathlib.Path(sys.argv[1])
lock = json.loads((root / "toolchain.lock.json").read_text())
go_mod = (root / "go.mod").read_text()
for module, version in lock["go_modules"].items():
    if not re.search(r"^\s*" + re.escape(module) + r"\s+" + re.escape(version) + r"\s*$", go_mod, re.M):
        raise SystemExit(f"go.mod missing pinned module {module}@{version}")
package = json.loads((root / "web/package.json").read_text())
pnpm_package = json.loads((root / "tooling/pnpm/package.json").read_text())
if pnpm_package.get("dependencies", {}).get("pnpm") != lock["pnpm"]["version"]:
    raise SystemExit("tooling/pnpm/package.json does not pin the locked pnpm version")
pnpm_lock = json.loads((root / lock["pnpm"]["package_lock"]).read_text())
locked_pnpm = pnpm_lock["packages"]["node_modules/pnpm"]
if locked_pnpm.get("version") != lock["pnpm"]["version"] or locked_pnpm.get("integrity") != lock["pnpm"]["integrity"]:
    raise SystemExit("pnpm package-lock version/integrity mismatch")
for arch, package_name in (("linux/amd64", "@pnpm/exe.linux-x64"), ("linux/arm64", "@pnpm/exe.linux-arm64")):
    native = pnpm_lock["packages"].get("node_modules/" + package_name, {})
    expected_native = lock["pnpm"]["native_packages"][arch]
    if native.get("version") != expected_native["version"] or native.get("integrity") != expected_native["integrity"] or native.get("resolved") != expected_native["tarball"]:
        raise SystemExit(f"pnpm native package lock mismatch for {arch}")
for section in ("dependencies", "devDependencies"):
    for name, version in package.get(section, {}).items():
        if version.startswith(("^", "~", ">", "*")):
            raise SystemExit(f"floating frontend dependency: {name} {version}")
        if lock["frontend"].get(name) != version:
            raise SystemExit(f"toolchain lock mismatch for {name}: {version}")
if package["packageManager"] != f"pnpm@{lock['pnpm']['version']}":
    raise SystemExit("packageManager does not match toolchain.lock.json")
compose_file = (root / "docker-compose.version").read_text().strip()
if compose_file != lock["compose"]["version"]:
    raise SystemExit(f"docker-compose.version ({compose_file}) does not match toolchain lock ({lock['compose']['version']})")
PY
echo "Toolchain PASS: Go $actual_go, Node $actual_node, pnpm $actual_pnpm (native SHA-256 verified; direct execution without Node PATH), Docker Compose $actual_compose (SHA-256 verified and CLI-resolved); GOTOOLCHAIN=local"
