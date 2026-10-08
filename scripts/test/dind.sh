#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK="$ROOT/test-images.lock.json"
ARTIFACT_ROOT="$ROOT/.artifacts/dind"
SUITE="nodedance-s00-dind"

usage() { echo "usage: $0 start|stop|status|clean ENGINE(28|29) [--purge-data]" >&2; exit 2; }
[[ $# -ge 2 ]] || usage
ACTION="$1"
ENGINE="$2"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || usage
IMAGE="$(python3 - "$LOCK" "$ENGINE" <<'PY'
import json,sys
print(json.load(open(sys.argv[1]))["images"]["engine"+sys.argv[2]])
PY
)"
VERSION=28.5.2
[[ "$ENGINE" == 29 ]] && VERSION=29.7.2
NAME="nodedance-s00-dind-v$ENGINE"
NETWORK="nodedance-s00-dind-net-v$ENGINE"
DIR="$ARTIFACT_ROOT/v$ENGINE"
SOCKET_DIR="$DIR/socket"
DATA_DIR="$DIR/data"
SOCKET="$SOCKET_DIR/docker.sock"
MARKER="$DIR/owner.json"
HOST_DOCKER="${NODEDANCE_HOST_DOCKER_HOST:-unix:///var/run/docker.sock}"

host_docker() { docker --host "$HOST_DOCKER" "$@"; }
test_docker() { docker --host "unix://$SOCKET" "$@"; }
fail() { echo "DIND $ACTION $ENGINE NOT_READY: $*" >&2; exit 3; }

case "$ACTION" in
  start)
    [[ "$HOST_DOCKER" == unix://* ]] || fail "isolated dind requires a local Unix socket host daemon"
    host_docker info >/dev/null 2>&1 || fail "the configured host Docker daemon is unavailable"
    mkdir -p "$ROOT/.artifacts/fixtures"
    mkdir -p "$SOCKET_DIR" "$DATA_DIR"
    chmod 700 "$DIR" "$SOCKET_DIR"
    if [[ -O "$DATA_DIR" ]]; then chmod 700 "$DATA_DIR"; fi
    if [[ -e "$MARKER" ]]; then
      python3 - "$MARKER" "$NAME" "$IMAGE" "$HOST_DOCKER" <<'PY' || fail "owner marker does not match the requested test daemon/host"
import json,sys
d=json.load(open(sys.argv[1]))
assert d.get("container_name")==sys.argv[2] and d.get("image")==sys.argv[3] and d.get("host_daemon")==sys.argv[4]
PY
    fi
    if ! host_docker network inspect "$NETWORK" >/dev/null 2>&1; then
      host_docker network create --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" "$NETWORK" >/dev/null || fail "could not create isolated host network"
    else
      host_docker network inspect "$NETWORK" --format '{{ index .Labels "io.nodedance.suite" }}' | grep -Fxq "$SUITE" || fail "network name exists without our ownership label"
    fi
    if host_docker container inspect "$NAME" >/dev/null 2>&1; then
      host_docker container inspect "$NAME" --format '{{ index .Config.Labels "io.nodedance.suite" }}' | grep -Fxq "$SUITE" || fail "container name exists without our ownership label"
    else
      host_docker run --detach --privileged --name "$NAME" \
        --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" \
        --network "$NETWORK" \
        --mount "type=bind,source=$SOCKET_DIR,destination=/run/nodedance-socket" \
        --mount "type=bind,source=$ROOT/.artifacts/fixtures,destination=$ROOT/.artifacts/fixtures" \
        --mount "type=bind,source=$DATA_DIR,destination=/var/lib/docker" \
        "$IMAGE" \
        --host=unix:///run/nodedance-socket/docker.sock \
        --data-root=/var/lib/docker \
        --exec-root=/run/nodedance-socket/exec \
        --pidfile=/run/nodedance-socket/docker.pid \
        --log-driver=json-file \
        --storage-driver=overlay2 >/dev/null || fail "host daemon refused the dedicated privileged test container"
      python3 - "$MARKER" "$NAME" "$IMAGE" "$SOCKET" "$HOST_DOCKER" <<'PY'
import json,sys
p,n,image,socket,host=sys.argv[1:]
json.dump({"schema":1,"suite":"nodedance-s00-dind","container_name":n,"image":image,"socket":socket,"host_daemon":host,"server_version":"starting"},open(p,"w"),indent=2)
PY
      chmod 600 "$MARKER"
    fi
    if [[ "$(host_docker container inspect "$NAME" --format '{{.State.Running}}')" != true ]]; then
      host_docker start "$NAME" >/dev/null || fail "could not restart the owned test daemon container"
    fi
    for _ in $(seq 1 60); do
      # The engine runs in its own user namespace and assigns its socket group
      # inside that namespace. The host client reaches it through a 0700
      # repository-owned directory, so make only this dedicated socket
      # accessible to the repository owner.
      host_docker exec "$NAME" sh -c 'test -S /run/nodedance-socket/docker.sock && chmod 0666 /run/nodedance-socket/docker.sock' >/dev/null 2>&1 || true
      if test_docker info >/dev/null 2>&1; then
        SERVER_VERSION="$(test_docker version --format '{{.Server.Version}}')"
        [[ "$SERVER_VERSION" == "$VERSION"* ]] || fail "wanted Engine $VERSION, got $SERVER_VERSION"
        python3 - "$MARKER" "$NAME" "$IMAGE" "$SOCKET" "$SERVER_VERSION" "$HOST_DOCKER" <<'PY'
import json,sys
p,n,image,socket,version,host=sys.argv[1:]
json.dump({"schema":1,"suite":"nodedance-s00-dind","container_name":n,"image":image,"socket":socket,"host_daemon":host,"server_version":version},open(p,"w"),indent=2)
PY
        chmod 600 "$MARKER"
        echo "DIND READY: Engine $SERVER_VERSION via unix://$SOCKET"
        exit 0
      fi
      sleep 1
    done
    host_docker logs "$NAME" >&2 || true
    fail "daemon did not become ready within 60 seconds"
    ;;
  stop)
    [[ -f "$MARKER" ]] || fail "owner marker is missing; refusing to stop any container"
    python3 - "$MARKER" "$HOST_DOCKER" <<'PY' || fail "host daemon endpoint differs from owner marker"
import json,sys
assert json.load(open(sys.argv[1])).get("host_daemon")==sys.argv[2]
PY
    host_docker container inspect "$NAME" --format '{{ index .Config.Labels "io.nodedance.suite" }}' 2>/dev/null | grep -Fxq "$SUITE" || fail "container ownership label mismatch"
    host_docker stop --time 20 "$NAME" >/dev/null
    echo "DIND STOPPED: $NAME (data preserved under $DATA_DIR)"
    ;;
  status)
    if [[ ! -f "$MARKER" ]]; then echo "DIND NOT_READY: Engine $ENGINE has not been prepared"; exit 3; fi
    python3 - "$MARKER" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); print(f"{d['server_version']} {d['socket']}")
PY
    if test_docker info >/dev/null 2>&1; then echo RUNNING; else echo STOPPED; exit 3; fi
    ;;
  clean)
    [[ -f "$MARKER" ]] || fail "owner marker missing; refusing cleanup"
    python3 - "$MARKER" "$HOST_DOCKER" <<'PY' || fail "host daemon endpoint differs from owner marker"
import json,sys
assert json.load(open(sys.argv[1])).get("host_daemon")==sys.argv[2]
PY
    host_docker container inspect "$NAME" --format '{{ index .Config.Labels "io.nodedance.suite" }}' 2>/dev/null | grep -Fxq "$SUITE" || fail "container ownership label mismatch"
    host_docker rm --force "$NAME" >/dev/null
    if host_docker network inspect "$NETWORK" --format '{{ index .Labels "io.nodedance.suite" }}' 2>/dev/null | grep -Fxq "$SUITE"; then
      host_docker network rm "$NETWORK" >/dev/null
    fi
    if [[ "${3:-}" == "--purge-data" ]]; then
      case "$(realpath -m "$DIR")/" in "$ARTIFACT_ROOT"/v"$ENGINE"/*|"$ARTIFACT_ROOT"/v"$ENGINE"/) ;; *) fail "unsafe data path" ;; esac
      # The nested daemon writes root-owned files. Remove contents through a
      # one-shot, locked image container, with only these two exact test
      # directories mounted and networking disabled.
      host_docker run --rm --privileged --network none \
        --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE-cleaner" \
        --mount "type=bind,source=$DATA_DIR,destination=/nodedance-data" \
        --mount "type=bind,source=$SOCKET_DIR,destination=/nodedance-socket" \
        --entrypoint /bin/sh "$IMAGE" -c \
        'find /nodedance-data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; find /nodedance-socket -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +' >/dev/null
      rm -rf -- "$DIR"
    else
      rm -f -- "$SOCKET" "$MARKER"
      echo "DIND container/network removed; test data retained at $DATA_DIR (pass --purge-data to delete this exact directory)"
    fi
    ;;
  *) usage ;;
esac
