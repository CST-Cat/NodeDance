#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/scripts/docker-test-config.sh"
LOCK="$ROOT/test-images.lock.json"
ACTION="${1:-}"
RUN_ID="${2:-}"
[[ "$ACTION" =~ ^(create|fault|reset|clean|status)$ ]] || { echo 'usage: fixtures.sh create|fault|reset|clean|status RUN_ID [FAULT]' >&2; exit 2; }
[[ "$RUN_ID" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$ ]] || { echo 'RUN_ID must be a unique 1-32 character alphanumeric/hyphen ID' >&2; exit 2; }
TEST_ROOT="$ROOT/.artifacts/fixtures/$RUN_ID"
HOST="${NODEDANCE_TEST_DOCKER_HOST:-}"
MARKER=""

not_ready() { echo "FIXTURE $ACTION NOT_READY: $*" >&2; exit 3; }
[[ "$HOST" == unix://* ]] || not_ready 'NODEDANCE_TEST_DOCKER_HOST must point at the dedicated dind Unix socket'
SOCKET="${HOST#unix://}"
case "$(realpath -m "$SOCKET")" in "$ROOT"/.artifacts/dind/v*/socket/docker.sock) ;; *) not_ready 'socket is outside this repository .artifacts/dind tree' ;; esac
[[ -S "$SOCKET" ]] || not_ready "dedicated test socket does not exist: $SOCKET"
ENGINE_DIR="$(dirname "$(dirname "$SOCKET")")"
MARKER="$ENGINE_DIR/owner.json"
[[ -f "$MARKER" ]] || not_ready 'dedicated daemon owner marker is missing'
python3 - "$MARKER" "$HOST" <<'PY' || not_ready 'daemon marker does not match this socket'
import json,sys
d=json.load(open(sys.argv[1]))
assert d.get("suite")=="nodedance-s00-dind" and d.get("socket")==sys.argv[2].removeprefix("unix://")
PY
export DOCKER_HOST="$HOST"
docker info >/dev/null 2>&1 || not_ready 'dedicated Docker Engine is not responding'
EXPECTED_COMPOSE="$(cat "$ROOT/docker-compose.version")"
ACTUAL_COMPOSE="$(docker compose version --short 2>/dev/null | sed 's/^v//')"
[[ "$ACTUAL_COMPOSE" == "$EXPECTED_COMPOSE" ]] || not_ready "Compose plugin must be $EXPECTED_COMPOSE; found ${ACTUAL_COMPOSE:-missing}"
PLUGIN_DETAILS="$(docker info --format '{{json .ClientInfo.Plugins}}' 2>/dev/null)" || not_ready 'Docker client plugin details are unavailable'
python3 - "$PLUGIN_DETAILS" "$ROOT/.tools/docker/cli-plugins/docker-compose" "v$EXPECTED_COMPOSE" <<'PY' || not_ready 'Docker CLI did not select the checksum-locked local Compose plugin'
import json,os,sys
plugins=json.loads(sys.argv[1])
expected=os.path.realpath(sys.argv[2])
matches=[p for p in plugins if p.get("Name")=="compose"]
assert len(matches)==1, matches
plugin=matches[0]
assert os.path.realpath(plugin["Path"])==expected, (plugin.get("Path"),expected)
assert plugin["Version"]==sys.argv[3], plugin.get("Version")
PY
ACTUAL_ENGINE="$(docker version --format '{{.Server.Version}}')"
PLUGIN_PATH="$(python3 - "$PLUGIN_DETAILS" <<'PY'
import json,sys
print(next(p["Path"] for p in json.loads(sys.argv[1]) if p.get("Name")=="compose"))
PY
)"

if [[ -e "$TEST_ROOT" ]]; then
  [[ -f "$TEST_ROOT/.nodedance-fixture-owner" ]] || not_ready "fixture root exists without ownership marker: $TEST_ROOT"
  [[ "$(cat "$TEST_ROOT/.nodedance-fixture-owner")" == "$RUN_ID" ]] || not_ready 'fixture root owner ID mismatch'
fi

project="nodedance-demo-$RUN_ID"
label="io.nodedance.suite=$RUN_ID"
docker_filter="label=$label"
docker_run_id="nodedance-$RUN_ID"

project_files() {
  docker compose --env-file "$TEST_ROOT/.env" --project-directory "$TEST_ROOT" \
    --project-name "$project" -f "$TEST_ROOT/compose.yaml" -f "$TEST_ROOT/compose.override.yaml" "$@"
}

require_fixture() {
  [[ -f "$TEST_ROOT/.nodedance-fixture-owner" ]] || not_ready "fixture run $RUN_ID does not exist"
}

wait_health() {
  local name="$1" expected="$2"
  for _ in $(seq 1 30); do
    status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$name" 2>/dev/null || true)"
    [[ "$status" == "$expected" ]] && return 0
    sleep 1
  done
  return 1
}

case "$ACTION" in
  create)
    if [[ ! -d "$TEST_ROOT" ]]; then
      mkdir -p "$TEST_ROOT/bind-data"
      printf '%s\n' "$RUN_ID" > "$TEST_ROOT/.nodedance-fixture-owner"
      chmod 700 "$TEST_ROOT" "$TEST_ROOT/bind-data"
    fi
    cp "$ROOT/tests/fixtures/compose.yaml" "$TEST_ROOT/compose.yaml"
    cp "$ROOT/tests/fixtures/compose.override.yaml" "$TEST_ROOT/compose.override.yaml"
    images="$(python3 - "$LOCK" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))["images"]
print(d["nginx"]); print(d["busybox"]); print(d["redis"])
PY
)"
    mapfile -t image_list <<< "$images"
    export NODEDANCE_TEST_RUN_ID="$RUN_ID"
    export ND_NGINX_IMAGE="${image_list[0]}" ND_BUSYBOX_IMAGE="${image_list[1]}" ND_REDIS_IMAGE="${image_list[2]}"
    cat > "$TEST_ROOT/.env" <<EOF
NODEDANCE_TEST_RUN_ID=$RUN_ID
ND_NGINX_IMAGE=${image_list[0]}
ND_BUSYBOX_IMAGE=${image_list[1]}
ND_REDIS_IMAGE=${image_list[2]}
ND_FIXTURE_VALUE=$RUN_ID
EOF
    chmod 600 "$TEST_ROOT/.env"
    printf 'NodeDance fixture %s\n' "$RUN_ID" > "$TEST_ROOT/bind-data/index.html"
    docker run --detach --name "nd-web-$RUN_ID" --label io.nodedance.test=true --label "$label" \
      --publish 127.0.0.1:18080:80/tcp "${image_list[0]}" >/dev/null
    docker run --detach --name "nd-udp-$RUN_ID" --label io.nodedance.test=true --label "$label" \
      --publish 127.0.0.1:18053:53/udp "${image_list[1]}" sh -c 'while true; do sleep 3600; done' >/dev/null
    docker run --detach --name "nd-expose-$RUN_ID" --label io.nodedance.test=true --label "$label" \
      --expose 2375/tcp "${image_list[1]}" sh -c 'while true; do sleep 3600; done' >/dev/null
    docker run --detach --name "nd-host-$RUN_ID" --label io.nodedance.test=true --label "$label" \
      --network host "${image_list[1]}" sh -c 'while true; do sleep 3600; done' >/dev/null
    docker run --detach --name "nd-stopped-$RUN_ID" --label io.nodedance.test=true --label "$label" \
      "${image_list[1]}" sh -c 'while true; do sleep 3600; done' >/dev/null
    docker stop "nd-stopped-$RUN_ID" >/dev/null
    docker run --detach --name "nd-health-$RUN_ID" --label io.nodedance.test=true --label "$label" \
      --health-cmd 'test ! -f /tmp/nodedance-unhealthy' --health-interval 2s --health-timeout 1s --health-retries 2 \
      "${image_list[1]}" sh -c 'while true; do sleep 3600; done' >/dev/null
    docker run --detach --name "nd-log-$RUN_ID" --label io.nodedance.test=true --label "$label" \
      "${image_list[1]}" sh -c 'i=0; while true; do printf "stdout fixture UTF-8 你好 NodeDance %s\\n" "$i"; printf "stderr fixture UTF-8 你好 NodeDance %s\\n" "$i" >&2; i=$((i+1)); sleep 1; done' >/dev/null
    project_files --profile demo-worker up --detach --wait
    if ! wait_health "nd-health-$RUN_ID" healthy; then echo 'health fixture did not become healthy' >&2; exit 1; fi
    project_files exec --no-TTY data redis-cli SET nodedance_fixture "$RUN_ID" >/dev/null
    project_files exec --no-TTY data sh -c 'printf "%s\n" "$NODEDANCE_TEST_RUN_ID" > /data/nodedance-marker'
    mounted_page="$(project_files exec --no-TTY web cat /usr/share/nginx/html/index.html)"
    [[ "$mounted_page" == "NodeDance fixture $RUN_ID" ]] || { echo 'Compose bind mount did not expose the exact fixture root content' >&2; exit 1; }
    volume_marker="$(project_files exec --no-TTY data cat /data/nodedance-marker)"
    redis_marker="$(project_files exec --no-TTY data redis-cli GET nodedance_fixture)"
    [[ "$volume_marker" == "$RUN_ID" && "$redis_marker" == "$RUN_ID" ]] || { echo 'named-volume/Redis marker did not persist fixture run ID' >&2; exit 1; }
    echo "FIXTURES READY: run=$RUN_ID engine=$ACTUAL_ENGINE compose=$ACTUAL_COMPOSE compose-plugin=$PLUGIN_PATH standalone=7 compose-services=3 volumes=1 bind-marker=verified named-volume-marker=verified redis-marker=verified labels=$label"
    ;;
  fault)
    require_fixture
    fault="${3:-}"
    case "$fault" in
      health-unhealthy)
        docker exec "nd-health-$RUN_ID" touch /tmp/nodedance-unhealthy
        wait_health "nd-health-$RUN_ID" unhealthy || { echo 'health fixture failed to reach unhealthy state' >&2; exit 1; }
        echo "FAULT ACTIVE: health-unhealthy run=$RUN_ID"
        ;;
      daemon-outage)
        engine="$(python3 - "$MARKER" <<'PY'
import json,sys
v=json.load(open(sys.argv[1]))["server_version"]
print("28" if v.startswith("28.") else "29" if v.startswith("29.") else "unsupported")
PY
)"
        [[ "$engine" != unsupported ]] || not_ready 'only the locked Engine 28/29 dind test daemons can be stopped by this fixture command'
        "$ROOT/scripts/test/dind.sh" stop "$engine"
        "$ROOT/scripts/test/dind.sh" start "$engine"
        export DOCKER_HOST="$HOST"
        echo "FAULT RECOVERED: dedicated daemon restarted; fixture data root preserved for $RUN_ID"
        ;;
      *) echo 'supported faults: health-unhealthy, daemon-outage' >&2; exit 2 ;;
    esac
    ;;
  reset)
    require_fixture
    fault="${3:-}"
    case "$fault" in
      health)
        docker exec "nd-health-$RUN_ID" rm -f /tmp/nodedance-unhealthy
        wait_health "nd-health-$RUN_ID" healthy || { echo 'health fixture failed to recover' >&2; exit 1; }
        echo "FAULT CLEARED: health run=$RUN_ID"
        ;;
      *) echo 'supported reset: health' >&2; exit 2 ;;
    esac
    ;;
  status)
    require_fixture
    docker ps -a --filter "$label" --format '{{.Names}} {{.Status}}'
    project_files ps
    ;;
  clean)
    require_fixture
    # Compose down is scoped to this exact generated project. It does not
    # remove images or affect any resource without the generated project name.
    project_files --profile demo-worker down --volumes --remove-orphans
    mapfile -t containers < <(docker ps -aq --filter "$docker_filter")
    if ((${#containers[@]})); then docker rm --force "${containers[@]}" >/dev/null; fi
    mapfile -t volumes < <(docker volume ls -q --filter "$docker_filter")
    if ((${#volumes[@]})); then docker volume rm "${volumes[@]}" >/dev/null; fi
    mapfile -t networks < <(docker network ls -q --filter "$docker_filter")
    if ((${#networks[@]})); then docker network rm "${networks[@]}" >/dev/null; fi
    remaining="$(docker ps -aq --filter "$docker_filter")"
    [[ -z "$remaining" ]] || { echo "cleanup left suite containers: $remaining" >&2; exit 1; }
    rm -rf -- "$TEST_ROOT"
    echo "FIXTURES CLEAN: only io.nodedance.suite=$RUN_ID resources removed; images retained"
    ;;
esac
