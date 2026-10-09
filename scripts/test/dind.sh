#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
LOCK="$ROOT/test-images.lock.json"
ARTIFACT_ROOT="$ROOT/.artifacts/dind"
SUITE="nodedance-s00-dind"
OWNER_HELPER="$ROOT/scripts/test/dind-owner.py"

usage() { echo "usage: $0 start|stop|status|clean ENGINE(28|29) [--purge-data]" >&2; exit 2; }
[[ $# -ge 2 && $# -le 3 ]] || usage
ACTION="$1"
ENGINE="$2"
[[ "$ENGINE" == 28 || "$ENGINE" == 29 ]] || usage
if [[ $# -eq 3 && ( "$ACTION" != clean || "$3" != --purge-data ) ]]; then usage; fi
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
FIXTURE_DIR="$ROOT/.artifacts/fixtures"
MARKER="$DIR/owner.json"
HOST_DOCKER="${NODEDANCE_HOST_DOCKER_HOST:-unix:///var/run/docker.sock}"

host_docker() { docker --host "$HOST_DOCKER" "$@"; }
test_docker() { docker --host "unix://$SOCKET" "$@"; }
fail() { echo "DIND $ACTION $ENGINE NOT_READY: $*" >&2; exit 3; }
helper_args=()

read_snapshot() {
  local kind="$1" target="$2" output rc lower
  if [[ "$kind" == container ]]; then
    if output="$(host_docker container inspect "$target" 2>&1)"; then printf '%s' "$output"; return 0; else rc=$?; fi
  else
    if output="$(host_docker network inspect "$target" 2>&1)"; then printf '%s' "$output"; return 0; else rc=$?; fi
  fi
  lower="${output,,}"
  if [[ "$lower" == *"no such object"* || "$lower" == *"not found"* || "$lower" == *"no such network"* ]]; then
    return 4
  fi
  echo "$output" >&2
  return "$rc"
}

snapshot_optional() {
  local kind="$1" target="$2" output rc
  if output="$(read_snapshot "$kind" "$target")"; then printf '%s' "$output"; return 0; else rc=$?; fi
  [[ "$rc" == 4 ]] && return 0
  return "$rc"
}

owner_validate() {
  local mode="$1" container_json="$2" network_json="$3"
  ND_DIND_CONTAINER_JSON="$container_json" ND_DIND_NETWORK_JSON="$network_json" \
    python3 "$OWNER_HELPER" validate "$mode" "${helper_args[@]}"
}

owner_validate_ids() {
  local mode="$1" container_json="$2" network_json="$3" result
  result="$(owner_validate "$mode" "$container_json" "$network_json")" || return
  [[ "$result" == *'|'* ]] || return 1
  CONTAINER_ID="${result%%|*}"
  NETWORK_ID="${result#*|}"
}

owner_write() {
  local container_id="$1" network_id="$2" version="$3"
  python3 "$OWNER_HELPER" write "${helper_args[@]}" "$container_id" "$network_id" "$version"
}

assert_local_paths_safe() {
  local path
  for path in "$ROOT/.artifacts" "$ARTIFACT_ROOT" "$DIR" "$SOCKET_DIR" "$DATA_DIR" "$FIXTURE_DIR"; do
    [[ ! -L "$path" ]] || fail "refusing symlink in owned fixture path: $path"
  done
  [[ "$(realpath -m "$DIR")" == "$ARTIFACT_ROOT/v$ENGINE" ]] || fail "fixture directory resolves outside this checkout"
  [[ "$(realpath -m "$SOCKET_DIR")" == "$DIR/socket" ]] || fail "socket directory resolves outside this checkout"
  [[ "$(realpath -m "$DATA_DIR")" == "$DIR/data" ]] || fail "data directory resolves outside this checkout"
  [[ "$(realpath -m "$FIXTURE_DIR")" == "$ROOT/.artifacts/fixtures" ]] || fail "fixture bind path resolves outside this checkout"
}

case "$ACTION" in
  start)
    [[ "$HOST_DOCKER" == unix://* ]] || fail "isolated dind requires a local Unix socket host daemon"
    HOST_ID="$(host_docker info --format '{{.ID}}' 2>/dev/null)" || fail "the configured host Docker daemon is unavailable"
    [[ -n "$HOST_ID" ]] || fail "the configured host Docker daemon returned no stable ID"
    helper_args=("$MARKER" "$SUITE" "$NAME" "$NETWORK" "$IMAGE" "$HOST_DOCKER" "$HOST_ID" "$SOCKET" "$SOCKET_DIR" "$DATA_DIR" "$FIXTURE_DIR")

    # These are read-only lookups. Do not create/chmod paths, create a network,
    # start a container, or exec into it until the marker and both fixed names
    # have been checked against the live daemon.
    CONTAINER_JSON="$(snapshot_optional container "$NAME")" || fail "could not determine whether the fixed container name exists"
    NETWORK_JSON="$(snapshot_optional network "$NETWORK")" || fail "could not determine whether the fixed network name exists"
    owner_validate_ids start "$CONTAINER_JSON" "$NETWORK_JSON" || fail "fixed DIND resource is not proven to belong to this checkout; refusing all mutation"

    if [[ ! -e "$MARKER" && -e "$DIR" ]]; then
      fail "fixture directory already exists without an owner marker; refusing to alter retained data"
    fi
    assert_local_paths_safe
    mkdir -p "$ARTIFACT_ROOT" "$DIR" "$SOCKET_DIR" "$DATA_DIR" "$FIXTURE_DIR"
    chmod 700 "$DIR" "$SOCKET_DIR"
    if [[ -O "$DATA_DIR" ]]; then chmod 700 "$DATA_DIR"; fi
    if [[ ! -f "$MARKER" ]]; then
      owner_write "" "" starting || fail "could not create owner marker"
    fi

    if [[ -z "$NETWORK_ID" ]]; then
      CREATED_NETWORK_ID="$(host_docker network create --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" "$NETWORK")" || fail "could not create isolated host network"
      [[ -n "$CREATED_NETWORK_ID" ]] || fail "host daemon returned no ID for the newly created network"
      NETWORK_JSON="$(read_snapshot network "$CREATED_NETWORK_ID")" || fail "new host network could not be inspected"
      ND_DIND_NETWORK_JSON="$NETWORK_JSON" python3 "$OWNER_HELPER" attest-network "${helper_args[@]}" "$CREATED_NETWORK_ID" || fail "new network did not match the exact owner marker"
      NETWORK_ID="$CREATED_NETWORK_ID"
    fi

    if [[ -z "$CONTAINER_ID" ]]; then
      CREATED_CONTAINER_ID="$(host_docker run --detach --privileged --name "$NAME" \
        --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE" \
        --network "$NETWORK_ID" \
        --mount "type=bind,source=$SOCKET_DIR,destination=/run/nodedance-socket" \
        --mount "type=bind,source=$FIXTURE_DIR,destination=$FIXTURE_DIR" \
        --mount "type=bind,source=$DATA_DIR,destination=/var/lib/docker" \
        "$IMAGE" \
        --host=unix:///run/nodedance-socket/docker.sock \
        --data-root=/var/lib/docker \
        --exec-root=/run/nodedance-socket/exec \
        --pidfile=/run/nodedance-socket/docker.pid \
        --log-driver=json-file \
        --storage-driver=overlay2)" || fail "host daemon refused the dedicated privileged test container"
      [[ -n "$CREATED_CONTAINER_ID" ]] || fail "host daemon returned no ID for the newly created container"
      CONTAINER_JSON="$(read_snapshot container "$CREATED_CONTAINER_ID")" || fail "new host container could not be inspected"
      NETWORK_JSON="$(read_snapshot network "$NETWORK_ID")" || fail "owned host network could not be inspected"
      ND_DIND_CONTAINER_JSON="$CONTAINER_JSON" ND_DIND_NETWORK_JSON="$NETWORK_JSON" \
        python3 "$OWNER_HELPER" attest-container "${helper_args[@]}" "$CREATED_CONTAINER_ID" || fail "new container did not match the exact owner marker"
      CONTAINER_ID="$CREATED_CONTAINER_ID"
    fi

    # Re-read by immutable IDs immediately before start/exec. Subsequent
    # mutations use IDs, so replacing a fixed name cannot redirect them.
    CONTAINER_JSON="$(read_snapshot container "$CONTAINER_ID")" || fail "owned container disappeared before startup"
    NETWORK_JSON="$(read_snapshot network "$NETWORK_ID")" || fail "owned network disappeared before startup"
    owner_validate_ids start "$CONTAINER_JSON" "$NETWORK_JSON" || fail "live DIND resources no longer match the owner marker"
    if [[ "$(host_docker container inspect "$CONTAINER_ID" --format '{{.State.Running}}')" != true ]]; then
      host_docker start "$CONTAINER_ID" >/dev/null || fail "could not restart the owned test daemon container"
    fi

    CONTAINER_JSON="$(read_snapshot container "$CONTAINER_ID")" || fail "owned container disappeared after startup"
    NETWORK_JSON="$(read_snapshot network "$NETWORK_ID")" || fail "owned network disappeared after startup"
    owner_validate_ids start "$CONTAINER_JSON" "$NETWORK_JSON" || fail "live DIND resources no longer match the owner marker"
    for _ in $(seq 1 60); do
      # This exec is allowed only after the live object ID, labels, image,
      # mount sources, network ID, host daemon ID and local paths all matched.
      host_docker exec "$CONTAINER_ID" sh -c 'test -S /run/nodedance-socket/docker.sock && chmod 0666 /run/nodedance-socket/docker.sock' >/dev/null 2>&1 || true
      if test_docker info >/dev/null 2>&1; then
        SERVER_VERSION="$(test_docker version --format '{{.Server.Version}}')"
        [[ "$SERVER_VERSION" == "$VERSION"* ]] || fail "wanted Engine $VERSION, got $SERVER_VERSION"
        owner_write "$CONTAINER_ID" "$NETWORK_ID" "$SERVER_VERSION" || fail "could not finalize the owner marker"
        echo "DIND READY: Engine $SERVER_VERSION via unix://$SOCKET"
        exit 0
      fi
      sleep 1
    done
    host_docker logs "$CONTAINER_ID" >&2 || true
    fail "daemon did not become ready within 60 seconds"
    ;;
  stop|clean)
    [[ -f "$MARKER" && ! -L "$MARKER" ]] || fail "owner marker is missing or unsafe; refusing to touch any container"
    [[ "$HOST_DOCKER" == unix://* ]] || fail "isolated dind requires a local Unix socket host daemon"
    HOST_ID="$(host_docker info --format '{{.ID}}' 2>/dev/null)" || fail "the configured host Docker daemon is unavailable"
    [[ -n "$HOST_ID" ]] || fail "the configured host Docker daemon returned no stable ID"
    helper_args=("$MARKER" "$SUITE" "$NAME" "$NETWORK" "$IMAGE" "$HOST_DOCKER" "$HOST_ID" "$SOCKET" "$SOCKET_DIR" "$DATA_DIR" "$FIXTURE_DIR")
    CONTAINER_JSON="$(snapshot_optional container "$NAME")" || fail "could not inspect the fixed container name"
    NETWORK_JSON="$(snapshot_optional network "$NETWORK")" || fail "could not inspect the fixed network name"
    owner_validate_ids "$ACTION" "$CONTAINER_JSON" "$NETWORK_JSON" || fail "resource identity, labels, host daemon or mount source does not match the owner marker"
    assert_local_paths_safe

    if [[ "$ACTION" == stop ]]; then
      CONTAINER_JSON="$(read_snapshot container "$CONTAINER_ID")" || fail "owned container disappeared before stop"
      NETWORK_JSON="$(read_snapshot network "$NETWORK_ID")" || fail "owned network disappeared before stop"
      owner_validate_ids stop "$CONTAINER_JSON" "$NETWORK_JSON" || fail "live DIND resources changed before stop"
      host_docker stop --time 20 "$CONTAINER_ID" >/dev/null || fail "could not stop the owned test daemon container"
      echo "DIND STOPPED: $NAME (data preserved under $DATA_DIR)"
      exit 0
    fi

    if [[ -n "$CONTAINER_JSON" ]]; then
      host_docker rm --force "$CONTAINER_ID" >/dev/null || fail "could not remove the owned test daemon container"
    fi
    if [[ -n "$NETWORK_ID" ]]; then
      if NETWORK_JSON="$(snapshot_optional network "$NETWORK_ID")"; then
        if [[ -n "$NETWORK_JSON" ]]; then
        ND_DIND_CONTAINER_JSON="" ND_DIND_NETWORK_JSON="$NETWORK_JSON" \
          python3 "$OWNER_HELPER" validate clean "${helper_args[@]}" >/dev/null || fail "network identity changed before removal"
        host_docker network rm "$NETWORK_ID" >/dev/null || fail "could not remove the owned test network"
        fi
      else
        fail "could not inspect the owned network before removal"
      fi
    fi

    if [[ "${3:-}" == --purge-data ]]; then
      # Path containment and symlink checks ran before deleting either Engine
      # object. The cleaner receives only the two exact owned data directories.
      host_docker run --rm --privileged --network none \
        --label io.nodedance.test=true --label "io.nodedance.suite=$SUITE-cleaner" \
        --mount "type=bind,source=$DATA_DIR,destination=/nodedance-data" \
        --mount "type=bind,source=$SOCKET_DIR,destination=/nodedance-socket" \
        --entrypoint /bin/sh "$IMAGE" -c \
        'find /nodedance-data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; find /nodedance-socket -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +' >/dev/null || fail "owned data directory cleanup failed"
      rm -rf -- "$DIR"
    else
      rm -f -- "$SOCKET" "$MARKER"
      echo "DIND container/network removed; test data retained at $DATA_DIR (pass --purge-data to delete this exact directory)"
    fi
    ;;
  status)
    [[ -f "$MARKER" && ! -L "$MARKER" ]] || { echo "DIND NOT_READY: Engine $ENGINE has not been prepared"; exit 3; }
    python3 - "$MARKER" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); print(f"{d['server_version']} {d['socket']}")
PY
    if test_docker info >/dev/null 2>&1; then echo RUNNING; else echo STOPPED; exit 3; fi
    ;;
  *) usage ;;
esac
