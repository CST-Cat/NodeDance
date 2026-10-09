#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK="$ROOT/test-images.lock.json"
ARTIFACT_ROOT="$ROOT/.artifacts/s06/dind/v29"
SUITE="nodedance-s06-dind"
HOST_DOCKER="${NODEDANCE_HOST_DOCKER_HOST:-unix:///var/run/docker.sock}"

usage() { echo "usage: $0 start RUN_ID | $0 clean RUN_ID [--purge-data]" >&2; exit 2; }
[[ $# -ge 2 && $# -le 3 ]] || usage
ACTION="$1"
RUN_ID="$2"
[[ "$ACTION" == start && $# == 2 || "$ACTION" == clean ]] || usage
[[ "$ACTION" != clean || $# == 2 || "${3:-}" == --purge-data ]] || usage
[[ "$RUN_ID" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]{0,35}$ ]] || usage
[[ "$HOST_DOCKER" == unix:///var/run/docker.sock ]] || {
  echo "S06 DIND refuses a host Docker endpoint other than the runner's local Unix socket" >&2
  exit 3
}

IMAGE="$(python3 - "$LOCK" <<'PY'
import json,sys
print(json.load(open(sys.argv[1]))["images"]["engine29"])
PY
)"
VERSION=29.7.2
RUN_ROOT="$ARTIFACT_ROOT/$RUN_ID"
SOCKET_DIR="$RUN_ROOT/socket"
DATA_DIR="$RUN_ROOT/data"
SOCKET="$SOCKET_DIR/docker.sock"
MARKER="$RUN_ROOT/owner.json"
NAME="nodedance-s06-dind-v29-$RUN_ID"
NETWORK="nodedance-s06-dind-net-v29-$RUN_ID"

host_docker() { docker --host "$HOST_DOCKER" "$@"; }
test_docker() { docker --host "unix://$SOCKET" "$@"; }
fail() { echo "S06 DIND $ACTION NOT_READY: $*" >&2; exit 3; }

case "$ACTION" in
  start)
    host_docker info >/dev/null 2>&1 || fail "runner-local Docker daemon is unavailable"
    [[ ! -L "$ROOT/.artifacts" && ! -L "$ROOT/.artifacts/s06" && ! -L "$ROOT/.artifacts/s06/dind" && ! -L "$ARTIFACT_ROOT" ]] || fail "artifact parent contains a symlink"
    mkdir -p "$ARTIFACT_ROOT"
    [[ ! -L "$ARTIFACT_ROOT" ]] || fail "Engine artifact root is a symlink"
    [[ ! -e "$RUN_ROOT" ]] || fail "run root already exists; refusing to reuse another Engine or its data: $RUN_ROOT"
    host_docker container inspect "$NAME" >/dev/null 2>&1 && fail "exact run container name already exists"
    host_docker network inspect "$NETWORK" >/dev/null 2>&1 && fail "exact run network name already exists"

    mkdir "$RUN_ROOT"
    chmod 700 "$RUN_ROOT"
    mkdir "$SOCKET_DIR" "$DATA_DIR"
    chmod 700 "$SOCKET_DIR" "$DATA_DIR"
    python3 - "$MARKER" "$NAME" "$NETWORK" "$IMAGE" "$SOCKET" "$HOST_DOCKER" "$RUN_ID" <<'PY'
import json,os,sys,tempfile
path,name,network,image,socket,host,run_id=sys.argv[1:]
data={"schema":1,"suite":"nodedance-s06-dind","container_name":name,"network_name":network,
      "image":image,"socket":socket,"host_daemon":host,"server_version":"starting","run_id":run_id}
fd,tmp=tempfile.mkstemp(prefix="owner.",dir=os.path.dirname(path),text=True)
with os.fdopen(fd,"w") as stream: json.dump(data,stream,indent=2)
os.chmod(tmp,0o600)
os.replace(tmp,path)
PY
    host_docker pull "$IMAGE" >/dev/null || fail "could not obtain the locked Engine 29 image"
    host_docker network create --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" --label "io.nodedance.run=$RUN_ID" "$NETWORK" >/dev/null || fail "could not create the exact run network"
    host_docker run --detach --privileged --name "$NAME" \
      --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" --label "io.nodedance.run=$RUN_ID" \
      --network "$NETWORK" \
      --mount "type=bind,source=$SOCKET_DIR,destination=/run/nodedance-socket" \
      --mount "type=bind,source=$DATA_DIR,destination=/var/lib/docker" \
      "$IMAGE" --host=unix:///run/nodedance-socket/docker.sock \
      --data-root=/var/lib/docker --exec-root=/run/nodedance-socket/exec \
      --pidfile=/run/nodedance-socket/docker.pid --log-driver=json-file --storage-driver=overlay2 >/dev/null || fail "runner refused the owner-marked nested Engine"

    for _ in $(seq 1 120); do
      host_docker exec "$NAME" sh -c 'test -S /run/nodedance-socket/docker.sock && chmod 0666 /run/nodedance-socket/docker.sock' >/dev/null 2>&1 || true
      if test_docker info >/dev/null 2>&1; then
        SERVER_VERSION="$(test_docker version --format '{{.Server.Version}}')"
        [[ "$SERVER_VERSION" == "$VERSION" ]] || fail "wanted Engine $VERSION, got $SERVER_VERSION"
        python3 - "$MARKER" "$SERVER_VERSION" <<'PY'
import json,os,sys,tempfile
path,version=sys.argv[1:]
data=json.load(open(path)); data["server_version"]=version
fd,tmp=tempfile.mkstemp(prefix="owner.",dir=os.path.dirname(path),text=True)
with os.fdopen(fd,"w") as stream: json.dump(data,stream,indent=2)
os.chmod(tmp,0o600)
os.replace(tmp,path)
PY
        echo "S06 DIND READY: Engine $SERVER_VERSION run=$RUN_ID socket=$SOCKET"
        exit 0
      fi
      sleep 1
    done
    host_docker logs "$NAME" >&2 || true
    fail "Engine 29 did not become ready within 120 seconds"
    ;;
  clean)
    [[ -f "$MARKER" && ! -L "$MARKER" ]] || fail "owner marker missing or unsafe; refusing cleanup"
    python3 - "$MARKER" "$NAME" "$NETWORK" "$IMAGE" "$SOCKET" "$HOST_DOCKER" "$RUN_ID" <<'PY' || fail "owner marker does not match this exact suite/run"
import json,sys
path,name,network,image,socket,host,run_id=sys.argv[1:]
data=json.load(open(path))
assert data.get("suite")=="nodedance-s06-dind" and data.get("container_name")==name
assert data.get("network_name")==network and data.get("image")==image
assert data.get("socket")==socket and data.get("host_daemon")==host and data.get("run_id")==run_id
assert data.get("server_version") in {"starting","29.7.2"}
PY
    if host_docker container inspect "$NAME" >/dev/null 2>&1; then
      host_docker container inspect "$NAME" --format '{{ index .Config.Labels "io.nodedance.test" }}|{{ index .Config.Labels "io.nodedance.suite" }}|{{ index .Config.Labels "io.nodedance.run" }}|{{.Config.Image}}' \
        | grep -Fxq "true|$SUITE|$RUN_ID|$IMAGE" || fail "Engine container ownership label/image mismatch"
      host_docker rm --force "$NAME" >/dev/null
    fi
    if host_docker network inspect "$NETWORK" >/dev/null 2>&1; then
      host_docker network inspect "$NETWORK" --format '{{ index .Labels "io.nodedance.test" }}|{{ index .Labels "io.nodedance.suite" }}|{{ index .Labels "io.nodedance.run" }}' \
        | grep -Fxq "true|$SUITE|$RUN_ID" || fail "Engine network ownership label mismatch"
      host_docker network rm "$NETWORK" >/dev/null
    fi
    [[ ! -L "$RUN_ROOT" && ! -L "$SOCKET_DIR" && ! -L "$DATA_DIR" ]] || fail "exact run cleanup path contains a symlink"
    [[ "$(realpath -m "$RUN_ROOT")" == "$ARTIFACT_ROOT/$RUN_ID" &&
       "$(realpath -m "$SOCKET_DIR")" == "$RUN_ROOT/socket" &&
       "$(realpath -m "$DATA_DIR")" == "$RUN_ROOT/data" ]] || fail "unsafe exact-run data cleanup path"
    if [[ "${3:-}" == --purge-data && -d "$DATA_DIR" ]]; then
      if host_docker image inspect "$IMAGE" >/dev/null 2>&1; then
        host_docker run --rm --privileged --network none \
          --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE-cleaner" --label "io.nodedance.run=$RUN_ID" \
          --mount "type=bind,source=$DATA_DIR,destination=/nodedance-data" \
          --mount "type=bind,source=$SOCKET_DIR,destination=/nodedance-socket" \
          --entrypoint /bin/sh "$IMAGE" -c \
          'find /nodedance-data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; find /nodedance-socket -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +' >/dev/null
        rm -rf -- "$DATA_DIR"
      elif [[ -z "$(find "$DATA_DIR" -mindepth 1 -print -quit)" ]]; then
        rmdir "$DATA_DIR"
      else
        fail "locked cleaner image is unavailable while exact run data remains; refusing unsafe host cleanup"
      fi
    fi
    rm -rf -- "$SOCKET_DIR"
    rm -f -- "$MARKER"
    if [[ "${3:-}" == --purge-data ]]; then
      rmdir "$RUN_ROOT"
      ! host_docker container inspect "$NAME" >/dev/null 2>&1 || fail "exact run Engine container remains after cleanup"
      ! host_docker network inspect "$NETWORK" >/dev/null 2>&1 || fail "exact run Engine network remains after cleanup"
      echo "S06 DIND CLEANUP PASS: removed only Engine $NAME, network $NETWORK, and run=$RUN_ID data"
    else
      echo "S06 DIND CLEANUP PASS: removed only Engine $NAME and network $NETWORK; run=$RUN_ID data retained"
    fi
    ;;
  *) usage ;;
esac
