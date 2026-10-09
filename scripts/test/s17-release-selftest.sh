#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nodedance-s17-release.XXXXXXXX")"
cleanup() { rm -rf -- "$WORK"; }
trap cleanup EXIT

cd "$ROOT"
GO_BIN="${GO_BIN:-go}"
"$GO_BIN" test ./cmd/nodedance-release -count=1
"$GO_BIN" vet ./cmd/nodedance-release
"$GO_BIN" build -o "$WORK/nodedance-release" ./cmd/nodedance-release
mkdir -m 700 "$WORK/keys" "$WORK/input"
"$WORK/nodedance-release" keygen --private-file "$WORK/keys/private.key" --public-file "$WORK/keys/public.key"
"$WORK/nodedance-release" keygen --private-file "$WORK/keys/wrong-private.key" --public-file "$WORK/keys/wrong-public.key"
case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH=amd64; OTHER_ARCH=arm64 ;;
  aarch64|arm64) HOST_ARCH=arm64; OTHER_ARCH=amd64 ;;
  *) echo "unsupported self-test host architecture: $(uname -m)" >&2; exit 2 ;;
esac
PUBLIC_KEY="$(tr -d '\n' < "$WORK/keys/public.key")"
for ARCH in "$HOST_ARCH" "$OTHER_ARCH"; do
  mkdir -m 700 "$WORK/input/$ARCH"
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -buildvcs=false \
    -ldflags '-X main.component=core' -o "$WORK/input/$ARCH/nodedance" ./scripts/test/s17-release-fixture.go
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -buildvcs=false \
    -ldflags '-X main.component=agent' -o "$WORK/input/$ARCH/nodedance-agent" ./scripts/test/s17-release-fixture.go
done

"$WORK/nodedance-release" bundle --core "$WORK/input/$HOST_ARCH/nodedance" --agent "$WORK/input/$HOST_ARCH/nodedance-agent" \
  --private-key-file "$WORK/keys/private.key" --version v1.2.3 --architecture "$HOST_ARCH" \
  --output "$WORK/valid.tar.gz"
"$WORK/nodedance-release" bundle --core "$WORK/input/$OTHER_ARCH/nodedance" --agent "$WORK/input/$OTHER_ARCH/nodedance-agent" \
  --private-key-file "$WORK/keys/private.key" --version v1.2.3 --architecture "$OTHER_ARCH" \
  --output "$WORK/wrong-arch.tar.gz"
python3 - "$WORK/valid.tar.gz" "$WORK/corrupt.tar.gz" <<'PY'
import io,sys,tarfile
from pathlib import Path
source,destination=map(Path,sys.argv[1:])
with tarfile.open(source,"r:gz") as old, tarfile.open(destination,"w:gz") as new:
    for member in old:
        data=old.extractfile(member).read()
        if member.name == "nodedance":
            data=bytes([data[0] ^ 1])+data[1:]
        info=tarfile.TarInfo(member.name)
        info.size=len(data)
        info.mode=member.mode
        info.type=tarfile.REGTYPE
        new.addfile(info,io.BytesIO(data))
PY

expect_failure() {
  if "$@"; then
    echo "expected command failure: $*" >&2
    exit 1
  fi
}
INSTALL="$ROOT/scripts/release/install.sh"
UNINSTALL="$ROOT/scripts/release/uninstall.sh"
expect_failure "$INSTALL" --bundle "$WORK/corrupt.tar.gz" --public-key-file "$WORK/keys/public.key" --prefix "$WORK/corrupt-install" --no-service
[[ ! -e "$WORK/corrupt-install" ]] || { echo 'corrupt bundle created an install prefix' >&2; exit 1; }
expect_failure "$INSTALL" --bundle "$WORK/valid.tar.gz" --public-key-file "$WORK/keys/wrong-public.key" --prefix "$WORK/wrong-key-install" --no-service
[[ ! -e "$WORK/wrong-key-install" ]] || { echo 'wrong key created an install prefix' >&2; exit 1; }
expect_failure "$INSTALL" --bundle "$WORK/wrong-arch.tar.gz" --public-key-file "$WORK/keys/public.key" --prefix "$WORK/wrong-arch-install" --no-service
[[ ! -e "$WORK/wrong-arch-install" ]] || { echo 'wrong architecture created an install prefix' >&2; exit 1; }

mkdir "$WORK/occupied-prefix"
echo 'keep me' > "$WORK/occupied-prefix/unknown"
expect_failure "$INSTALL" --bundle "$WORK/valid.tar.gz" --public-key-file "$WORK/keys/public.key" --prefix "$WORK/occupied-prefix" --no-service
[[ "$(cat "$WORK/occupied-prefix/unknown")" == 'keep me' ]] || { echo 'installer changed an unknown file' >&2; exit 1; }

PREFIX="$WORK/install"
"$INSTALL" --bundle "$WORK/valid.tar.gz" --public-key-file "$WORK/keys/public.key" --prefix "$PREFIX" --no-service
cmp "$WORK/input/$HOST_ARCH/nodedance" "$PREFIX/nodedance"
cmp "$WORK/input/$HOST_ARCH/nodedance-agent" "$PREFIX/nodedance-agent"
[[ "$(stat -c '%a' "$PREFIX/nodedance")" == 755 ]]
[[ "$(stat -c '%a' "$PREFIX/release-manifest.json")" == 644 ]]
expect_failure "$INSTALL" --bundle "$WORK/valid.tar.gz" --public-key-file "$WORK/keys/public.key" --prefix "$PREFIX" --no-service
cmp "$WORK/input/$HOST_ARCH/nodedance" "$PREFIX/nodedance"

mkdir "$PREFIX/data"
echo 'application state' > "$PREFIX/data/state.db"
expect_failure "$UNINSTALL" --public-key-file "$WORK/keys/wrong-public.key" --prefix "$PREFIX"
[[ -x "$PREFIX/nodedance" && -f "$PREFIX/data/state.db" ]] || { echo 'failed uninstall changed installation/data' >&2; exit 1; }
"$UNINSTALL" --public-key-file "$WORK/keys/public.key" --prefix "$PREFIX"
[[ ! -e "$PREFIX/nodedance" && ! -e "$PREFIX/nodedance-agent" && ! -e "$PREFIX/release-manifest.json" ]]
[[ "$(cat "$PREFIX/data/state.db")" == 'application state' ]] || { echo 'uninstall deleted business data' >&2; exit 1; }
expect_failure "$UNINSTALL" --public-key-file "$WORK/keys/public.key" --prefix "$PREFIX"
[[ "$(cat "$PREFIX/data/state.db")" == 'application state' ]] || { echo 'repeated uninstall deleted business data' >&2; exit 1; }

python3 - "$ROOT/scripts/release/operations.py" "$WORK/unit.service" <<'PY'
import importlib.util,sys
from pathlib import Path
module_path,output=map(Path,sys.argv[1:])
spec=importlib.util.spec_from_file_location("nodedance_release_ops",module_path)
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
unit=module.render_core_unit(Path("/opt/nodedance" )).decode()
expected=("User=nodedance", "Group=nodedance", "ExecStart=/opt/nodedance/nodedance serve --listen 127.0.0.1:8180 --data-dir /var/lib/nodedance", "ReadWritePaths=/var/lib/nodedance", "ProtectSystem=strict")
if any(line not in unit.splitlines() for line in expected):
    raise SystemExit("Core systemd unit is missing a required account, loopback, or data-directory setting")
if "0.0.0.0" in unit or "--dev" in unit:
    raise SystemExit("Core systemd unit exposed the development listener")
output.write_text(unit)
PY
if command -v systemd-analyze >/dev/null 2>&1 && getent passwd nodedance >/dev/null 2>&1; then
  systemd-analyze verify "$WORK/unit.service"
fi

python3 - "$ROOT/scripts/release/operations.py" "$WORK/rollback-root" <<'PY'
import importlib.util,sys
from pathlib import Path
module_path,root=map(Path,sys.argv[1:])
spec=importlib.util.spec_from_file_location("nodedance_release_rollback",module_path)
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
for fail_at in ("daemon-reload", "enable"):
    case=root/fail_at
    prefix=case/"opt"/"nodedance"
    systemd=case/"systemd"
    data=case/"var"/"lib"/"nodedance"
    prefix.mkdir(parents=True)
    systemd.mkdir(parents=True)
    data.mkdir(parents=True)
    marker=data/"state.db"
    marker.write_text("preserve",encoding="utf-8")
    entries={"manifest.json":b"signed manifest", "nodedance":b"core", "nodedance-agent":b"agent"}
    (prefix/"nodedance").write_bytes(entries["nodedance"])
    (prefix/"nodedance-agent").write_bytes(entries["nodedance-agent"])
    (prefix/"release-manifest.json").write_bytes(entries["manifest.json"])
    module.SYSTEMD_DIR=systemd
    failed=False
    calls=[]
    def fake_systemctl(*arguments):
        global failed
        calls.append(arguments)
        if fail_at == "daemon-reload" and arguments == ("daemon-reload",) and not failed:
            failed=True
            raise module.ReleaseError("injected daemon-reload failure")
        if fail_at == "enable" and arguments == ("enable", "--now", module.SERVICE_NAME):
            raise module.ReleaseError("injected service start failure")
    module.run_systemctl=fake_systemctl
    module.query_systemctl=lambda *arguments: 3 if arguments[0] == "is-active" else 1
    try:
        module.install_core_unit(prefix,entries)
    except module.ReleaseError as error:
        if "rolled back" not in str(error):
            raise
    else:
        raise SystemExit(f"injected {fail_at} failure did not fail the install")
    if prefix.exists() or (systemd/module.SERVICE_NAME).exists():
        raise SystemExit(f"injected {fail_at} failure left the newly installed unit or binaries")
    if ("stop", module.SERVICE_NAME) not in calls or ("disable", module.SERVICE_NAME) not in calls:
        raise SystemExit(f"injected {fail_at} failure did not attempt to deactivate the unit")
    if marker.read_text(encoding="utf-8") != "preserve":
        raise SystemExit(f"injected {fail_at} failure changed shared business data")
PY

echo 'S17 release bundle/install/uninstall self-test PASS'
