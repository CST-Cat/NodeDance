#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK="$ROOT/test-images.lock.json"
ARTIFACT_ROOT="$ROOT/.artifacts/s08/dind"
SUITE="nodedance-s08-dind"

usage() { echo "usage: $0 start ENGINE(28|29) RUN_ID | $0 clean ENGINE(28|29) RUN_ID [--purge-data]" >&2; exit 2; }
[[ $# -ge 3 && $# -le 4 ]] || usage
ACTION="$1"
ENGINE="$2"
RUN_ID="$3"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || usage
[[ "$ACTION" == start && $# == 3 || "$ACTION" == clean ]] || usage
[[ "$ACTION" != clean || $# == 3 || "${4:-}" == --purge-data ]] || usage
[[ "$RUN_ID" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]{0,35}$ ]] || usage

IMAGE="$(python3 - "$LOCK" "$ENGINE" <<'PY'
import json,sys
print(json.load(open(sys.argv[1]))["images"]["engine"+sys.argv[2]])
PY
)"
VERSION=28.5.2
[[ "$ENGINE" == 29 ]] && VERSION=29.7.2
RUN_ROOT="$ARTIFACT_ROOT/v$ENGINE/$RUN_ID"
SOCKET_DIR="$RUN_ROOT/socket"
DATA_DIR="$RUN_ROOT/data"
SOCKET="$SOCKET_DIR/docker.sock"
MARKER="$RUN_ROOT/owner.json"
NAME="nodedance-s08-dind-v$ENGINE-$RUN_ID"
NETWORK="nodedance-s08-dind-net-v$ENGINE-$RUN_ID"
HOST_DOCKER="${NODEDANCE_HOST_DOCKER_HOST:-unix:///var/run/docker.sock}"

host_docker() { docker --host "$HOST_DOCKER" "$@"; }
test_docker() { docker --host "unix://$SOCKET" "$@"; }
fail() { echo "S08 DIND $ACTION $ENGINE/$RUN_ID NOT_READY: $*" >&2; exit 3; }

case "$ACTION" in
  start)
    [[ "$HOST_DOCKER" == unix://* ]] || fail "isolated DIND requires a local Unix-socket host daemon"
    host_docker info >/dev/null 2>&1 || fail "the configured host Docker daemon is unavailable"
    [[ ! -e "$RUN_ROOT" ]] || fail "run root already exists; refusing to reuse an Engine or its data: $RUN_ROOT"
    host_docker container inspect "$NAME" >/dev/null 2>&1 && fail "container name already exists; refusing reuse"
    host_docker network inspect "$NETWORK" >/dev/null 2>&1 && fail "network name already exists; refusing reuse"

    mkdir -p "$RUN_ROOT"
    chmod 700 "$RUN_ROOT"
    mkdir "$SOCKET_DIR" "$DATA_DIR"
    chmod 700 "$SOCKET_DIR" "$DATA_DIR"
    host_docker pull "$IMAGE" >/dev/null || fail "could not obtain the locked DIND image"
    host_docker network create --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" "$NETWORK" >/dev/null || fail "could not create the dedicated test network"
    python3 - "$MARKER" "$NAME" "$IMAGE" "$SOCKET" "$HOST_DOCKER" "$RUN_ID" <<'PY'
import json,os,sys,tempfile
path,name,image,socket,host,run_id=sys.argv[1:]
data={"schema":1,"suite":"nodedance-s08-dind","container_name":name,"image":image,
      "socket":socket,"host_daemon":host,"server_version":"starting","run_id":run_id}
fd,tmp=tempfile.mkstemp(prefix="owner.",dir=os.path.dirname(path),text=True)
with os.fdopen(fd,"w") as stream: json.dump(data,stream,indent=2)
os.chmod(tmp,0o600)
os.replace(tmp,path)
PY
    host_docker run --detach --privileged --name "$NAME" \
      --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" --label "io.nodedance.run=$RUN_ID" \
      --network "$NETWORK" \
      --mount "type=bind,source=$SOCKET_DIR,destination=/run/nodedance-socket" \
      --mount "type=bind,source=$DATA_DIR,destination=/var/lib/docker" \
      "$IMAGE" \
      --host=unix:///run/nodedance-socket/docker.sock \
      --data-root=/var/lib/docker \
      --exec-root=/run/nodedance-socket/exec \
      --pidfile=/run/nodedance-socket/docker.pid \
      --log-driver=json-file --storage-driver=overlay2 >/dev/null || fail "host daemon refused the dedicated privileged test Engine"

    for _ in $(seq 1 120); do
      # The socket is inside a mode-0700 run directory, so other local users
      # cannot reach it even though this exact test socket is group/world open.
      host_docker exec "$NAME" sh -c 'test -S /run/nodedance-socket/docker.sock && chmod 0666 /run/nodedance-socket/docker.sock' >/dev/null 2>&1 || true
      if test_docker info >/dev/null 2>&1; then
        SERVER_VERSION="$(test_docker version --format '{{.Server.Version}}')"
        [[ "$SERVER_VERSION" == "$VERSION"* ]] || fail "wanted Engine $VERSION, got $SERVER_VERSION"
        python3 - "$MARKER" "$SERVER_VERSION" <<'PY'
import json,os,sys,tempfile
path,version=sys.argv[1:]
data=json.load(open(path)); data["server_version"]=version
fd,tmp=tempfile.mkstemp(prefix="owner.",dir=os.path.dirname(path),text=True)
with os.fdopen(fd,"w") as stream: json.dump(data,stream,indent=2)
os.chmod(tmp,0o600)
os.replace(tmp,path)
PY
        echo "S08 DIND READY: Engine $SERVER_VERSION via unix://$SOCKET"
        exit 0
      fi
      sleep 1
    done
    host_docker logs "$NAME" >&2 || true
    fail "daemon did not become ready within 120 seconds"
    ;;
  clean)
    [[ -f "$MARKER" ]] || fail "owner marker is missing; refusing cleanup"
    python3 - "$MARKER" "$NAME" "$IMAGE" "$SOCKET" "$HOST_DOCKER" "$RUN_ID" <<'PY' || fail "owner marker does not match the exact requested run"
import json,sys
path,name,image,socket,host,run_id=sys.argv[1:]
data=json.load(open(path))
assert data.get("suite")=="nodedance-s08-dind" and data.get("container_name")==name
assert data.get("image")==image and data.get("socket")==socket
assert data.get("host_daemon")==host and data.get("run_id")==run_id
PY
    if host_docker container inspect "$NAME" >/dev/null 2>&1; then
      host_docker container inspect "$NAME" --format '{{ index .Config.Labels "io.nodedance.suite" }}|{{ index .Config.Labels "io.nodedance.run" }}|{{.Config.Image}}' \
        | grep -Fxq "$SUITE|$RUN_ID|$IMAGE" || fail "container ownership label or image mismatch"
      host_docker rm --force "$NAME" >/dev/null
    fi
    if host_docker network inspect "$NETWORK" >/dev/null 2>&1; then
      host_docker network inspect "$NETWORK" --format '{{ index .Labels "io.nodedance.suite" }}' \
        | grep -Fxq "$SUITE" || fail "network ownership label mismatch"
      host_docker network rm "$NETWORK" >/dev/null
    fi
    case "$(realpath -m "$SOCKET_DIR")/" in "$ARTIFACT_ROOT"/v"$ENGINE"/"$RUN_ID"/*|"$ARTIFACT_ROOT"/v"$ENGINE"/"$RUN_ID"/) ;; *) fail "unsafe socket cleanup path" ;; esac
    if [[ "${4:-}" == --purge-data ]]; then
      case "$(realpath -m "$DATA_DIR")/" in "$ARTIFACT_ROOT"/v"$ENGINE"/"$RUN_ID"/*|"$ARTIFACT_ROOT"/v"$ENGINE"/"$RUN_ID"/) ;; *) fail "unsafe data cleanup path" ;; esac
      host_docker run --rm --privileged --network none \
        --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE-cleaner" \
        --mount "type=bind,source=$DATA_DIR,destination=/nodedance-data" \
        --mount "type=bind,source=$SOCKET_DIR,destination=/nodedance-socket" \
        --entrypoint /bin/sh "$IMAGE" -c \
        'find /nodedance-data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; find /nodedance-socket -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +' >/dev/null
      rm -rf -- "$DATA_DIR"
    fi
    rm -rf -- "$SOCKET_DIR"
    rm -f -- "$MARKER"
    if [[ "${4:-}" == --purge-data ]]; then
      rmdir "$RUN_ROOT"
      echo "S08 DIND cleaned: only $NAME, $NETWORK, and their exact run data"
    else
      echo "S08 DIND cleaned: only $NAME and $NETWORK; Engine data retained at $DATA_DIR"
    fi
    ;;
  *) usage ;;
esac
