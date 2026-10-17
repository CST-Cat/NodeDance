#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

readonly RELEASE_BASE="https://github.com/CST-Cat/NodeDance/releases/download"
readonly LATEST_VERSION_URL="https://raw.githubusercontent.com/CST-Cat/NodeDance/main/agent-latest.txt"
readonly STATE_DIR="/var/lib/nodedance-agent"
readonly CONFIG_PATH="${STATE_DIR}/agent.json"
readonly BIN_DIR="/usr/local/bin"
readonly AGENT_PATH="${BIN_DIR}/nodedance-agent"

server_url=""
mode="native"
state_validator=""
release_tag=""
development=0
tmp_dir=""
staged_agent=""
installed_inode=""
state_inode=""
binary_installed=0
state_prepared=0
enrollment_started=0
bin_dir_created=0
token=""

usage() {
  cat <<'EOF'
NodeDance Agent installer

Usage:
  bash install-agent.sh --mode native --server https://core.example --token TOKEN

Options:
  --release-tag TAG     Published Agent-only tag (optional; default latest stable Agent)
  --server URL          Core HTTP or HTTPS origin (required)
  --mode MODE           native (systemd) or docker; default native
  --token TOKEN         Enrollment token included in the generated command (required)
  --dev                 Mark a local development enrollment; HTTP remains loopback-only
  --help                Show this help

The command completes enrollment without an additional token prompt. The Agent
receives the supplied enrollment token through stdin. State remains root-only.
EOF
}

fail() { printf 'NodeDance Agent installer: %s\n' "$*" >&2; exit 1; }

cleanup() {
  local status=$?
  if [[ -n "$staged_agent" && -e "$staged_agent" && ! -L "$staged_agent" ]]; then rm -f -- "$staged_agent" 2>/dev/null || true; fi
  if (( status != 0 )); then
    unset token
    local keep_install=0
    if (( enrollment_started )) && valid_agent_config; then
      keep_install=1
      printf '%s\n' 'Enrollment may have reached Core. The installer preserved the validated root-only config and binary for recovery.' >&2
      if [[ "$mode" == docker ]]; then
        printf '%s\n' 'Inspect the Docker Agent with: sudo docker logs nodedance-agent. Preserve /var/lib/nodedance-agent when recovering.' >&2
      else
        printf 'Inspect or recover with: sudo %s recover --config %s\n' "$AGENT_PATH" "$CONFIG_PATH" >&2
        printf 'Retry service installation with: sudo %s install-systemd --config %s --enable\n' "$AGENT_PATH" "$CONFIG_PATH" >&2
      fi
    elif (( enrollment_started )) && { [[ -e "$CONFIG_PATH" ]] || [[ -L "$CONFIG_PATH" ]]; }; then
      keep_install=1
      printf '%s\n' 'Agent state exists but its config did not pass validation. The installer preserved it and will not claim that recovery is available; inspect the state before retrying.' >&2
    elif (( enrollment_started )) && [[ -d "$STATE_DIR" && ! -L "$STATE_DIR" ]]; then
      local current_state_inode
      current_state_inode=$(stat -c '%d:%i' -- "$STATE_DIR" 2>/dev/null || true)
      if [[ -n "$state_inode" && "$current_state_inode" == "$state_inode" ]] && rmdir -- "$STATE_DIR" 2>/dev/null; then
        state_prepared=0
      else
        keep_install=1
        printf '%s\n' 'Agent state contains files but no validated config; the installer preserved state for inspection.' >&2
      fi
    fi
    if (( binary_installed && ! keep_install )) && [[ -n "$installed_inode" && -e "$AGENT_PATH" && ! -L "$AGENT_PATH" ]]; then
      local current_inode
      current_inode=$(stat -c '%d:%i' -- "$AGENT_PATH" 2>/dev/null || true)
      if [[ "$current_inode" == "$installed_inode" ]]; then rm -f -- "$AGENT_PATH" 2>/dev/null || true; fi
    fi
    if (( state_prepared && ! enrollment_started )) && [[ -d "$STATE_DIR" && ! -L "$STATE_DIR" ]]; then
      local current_state_inode
      current_state_inode=$(stat -c '%d:%i' -- "$STATE_DIR" 2>/dev/null || true)
      if [[ -n "$state_inode" && "$current_state_inode" == "$state_inode" ]]; then rmdir -- "$STATE_DIR" 2>/dev/null || true; fi
    fi
    if (( bin_dir_created )) && [[ -d "$BIN_DIR" && ! -L "$BIN_DIR" ]]; then
      local current_bin_inode
      current_bin_inode=$(stat -c '%d:%i' -- "$BIN_DIR" 2>/dev/null || true)
      if [[ -n "$bin_dir_inode" && "$current_bin_inode" == "$bin_dir_inode" ]]; then rmdir -- "$BIN_DIR" 2>/dev/null || true; fi
    fi
  fi
  if [[ -n "$tmp_dir" ]]; then rm -rf -- "$tmp_dir" 2>/dev/null || true; fi
  return "$status"
}

valid_agent_config() {
  [[ -d "$STATE_DIR" && ! -L "$STATE_DIR" && -f "$CONFIG_PATH" && ! -L "$CONFIG_PATH" ]] || return 1
  local state_metadata config_metadata
  state_metadata=$(stat -c '%u:%a' -- "$STATE_DIR" 2>/dev/null) || return 1
  config_metadata=$(stat -c '%u:%a:%h' -- "$CONFIG_PATH" 2>/dev/null) || return 1
  [[ "$state_metadata" == '0:700' && "$config_metadata" == '0:600:1' ]] || return 1
  "$state_validator" validate-systemd-state --config "$CONFIG_PATH" >/dev/null 2>&1
}
trap cleanup EXIT

while (($#)); do
  case "$1" in
    --server)
      (($# >= 2)) || fail '--server requires a value'
      server_url=$2
      shift 2
      ;;
    --release-tag)
      (($# >= 2)) || fail '--release-tag requires a value'
      release_tag=$2
      shift 2
      ;;
    --mode)
      (($# >= 2)) || fail '--mode requires a value'
      mode=$2
      shift 2
      ;;
    --token)
      (($# >= 2)) || fail '--token requires a value'
      token=$2
      shift 2
      ;;
    --dev)
      development=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *) fail "unknown option: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || fail 'run this installer with sudo or as root'
[[ "$(uname -s)" == Linux ]] || fail 'only Linux amd64 and arm64 are supported'
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported Linux architecture: $(uname -m)" ;;
esac

[[ -n "$server_url" ]] || fail 'the generated command must include --server'
[[ "$mode" == native || "$mode" == docker ]] || fail '--mode must be native or docker'
[[ "$token" =~ ^[a-f0-9]{64}$ ]] || fail '--token requires a valid enrollment token'
[[ -z "$release_tag" || "$release_tag" =~ ^agent-v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail '--release-tag must be a published Agent-only tag such as agent-v0.1.0'
if (( development )); then
  case "$server_url" in
    http://127.0.0.1|http://127.0.0.1:*|http://\[::1\]|http://\[::1\]:*) ;;
    *) fail '--dev allows only http://127.0.0.1 or http://[::1] loopback origins' ;;
  esac
else
  case "$server_url" in
    https://*|http://*) ;;
    *) fail 'Core URL must use HTTP or HTTPS' ;;
  esac
fi

for tool in awk chmod curl install ln mkdir mktemp rm rmdir sha256sum stat systemctl; do
  command -v "$tool" >/dev/null 2>&1 || fail "required command is missing: $tool"
done

[[ ! -e "$STATE_DIR" && ! -L "$STATE_DIR" ]] || fail "refusing to replace existing Agent state or credentials: $STATE_DIR"
if [[ "$mode" == native ]]; then
  [[ ! -e "$AGENT_PATH" && ! -L "$AGENT_PATH" ]] || fail "refusing to replace existing Agent binary: $AGENT_PATH"
  if existing_agent=$(command -v nodedance-agent 2>/dev/null); then fail "refusing to replace an existing Agent binary found at $existing_agent"; fi
  load_state=$(systemctl show --property=LoadState --value nodedance-agent.service 2>/dev/null) || fail 'cannot inspect systemd unit state'
  [[ "$load_state" == not-found ]] || fail "refusing to take over existing nodedance-agent.service (LoadState=$load_state)"
else
  command -v docker >/dev/null || fail 'Docker is required for Docker deployment'
  docker info >/dev/null || fail 'cannot access the rootful Docker daemon'
  if docker container inspect nodedance-agent >/dev/null 2>&1; then fail 'Agent container already exists; refusing to overwrite it'; fi
  if systemctl is-active --quiet nodedance-agent.service; then fail 'Stop the existing systemd Agent before Docker enrollment'; fi
fi

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/nodedance-agent-install.XXXXXXXX")
# Resolve at execution time from the static version file published by the
# release workflow. This keeps the installer on plain HTTPS downloads with no
# GitHub REST API dependency or per-IP rate quota; --release-tag pins explicitly.
if [[ -z "$release_tag" ]]; then
  release_tag=$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
    "$LATEST_VERSION_URL" 2>/dev/null | tr -d '[:space:]') \
    || fail 'could not resolve the latest stable Agent release; pass --release-tag agent-vMAJOR.MINOR.PATCH to pin one'
  [[ "$release_tag" =~ ^agent-v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
    || fail 'the Agent version file did not contain a valid stable tag; pass --release-tag agent-vMAJOR.MINOR.PATCH to pin one'
fi
printf 'Installing Agent %s (%s).\n' "$release_tag" "$mode"
asset="nodedance-agent-linux-${arch}"
manifest_url="${RELEASE_BASE}/${release_tag}/SHA256SUMS"
asset_url="${RELEASE_BASE}/${release_tag}/${asset}"

http_status=$(curl --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --output "$tmp_dir/SHA256SUMS" --write-out '%{http_code}' "$manifest_url" 2>/dev/null || true)
[[ "$http_status" == 200 ]] || fail "could not download the Agent release checksum manifest for ${release_tag} (HTTP ${http_status:-unknown}); check the Agent tag and release availability"

curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --output "$tmp_dir/$asset" "$asset_url" || fail "release asset is unavailable: $asset"
expected_sha=$(awk -v name="$asset" '$2 == name { print $1 }' "$tmp_dir/SHA256SUMS")
[[ "$expected_sha" =~ ^[[:xdigit:]]{64}$ ]] || fail "release checksum manifest does not contain one valid SHA256 entry for $asset"
printf '%s  %s\n' "$expected_sha" "$asset" | (cd "$tmp_dir" && sha256sum --check --status -) || fail "SHA256 verification failed for $asset"

chmod 0700 "$tmp_dir/$asset"
state_validator="$tmp_dir/$asset"

prepare_state() {
  local prepare_result
  prepare_result=$("$state_validator" prepare-systemd-state --require-new) || fail 'could not safely create a fresh root-only Agent state directory'
  if [[ "$prepare_result" =~ ^created:([0-9]+):([0-9]+)$ ]]; then
    state_inode="${BASH_REMATCH[1]}:${BASH_REMATCH[2]}"
    state_prepared=1
  else
    fail 'Agent state already exists or its newly created directory identity could not be verified'
  fi
}

if [[ "$mode" == docker ]]; then
  image="ghcr.io/cst-cat/nodedance-agent:$release_tag"
  docker pull "$image" || fail 'cannot pull the pinned Agent image'
  prepare_state
  docker_host_args=(--user=0:0 --privileged --pid=host --network=host --mount type=bind,src=/,dst=/host,readonly --mount "type=bind,src=$STATE_DIR,dst=$STATE_DIR")
  enroll_args=(enroll --server "$server_url" --token-stdin --config "$CONFIG_PATH")
  if (( development )); then enroll_args+=(--dev); fi
  enrollment_started=1
  printf '%s\n' "$token" | docker run --rm -i "${docker_host_args[@]}" "$image" "${enroll_args[@]}" || fail 'Docker Agent enrollment failed'
  unset token
  valid_agent_config || fail 'Docker enrollment did not create valid root-only credentials'
  docker run -d --name nodedance-agent --restart unless-stopped --stop-timeout 30 "${docker_host_args[@]}" "$image" || fail 'Agent enrolled; Docker startup failed'
  printf 'NodeDance Docker Agent is installed for Core %s.\n' "$server_url"
  exit 0
fi

bin_dir_inode=""
if [[ -e "$BIN_DIR" || -L "$BIN_DIR" ]]; then
  [[ -d "$BIN_DIR" ]] || fail "binary destination is not a directory: $BIN_DIR"
else
  mkdir -m 0755 -- "$BIN_DIR" || fail "cannot create binary directory: $BIN_DIR"
  bin_dir_created=1
  bin_dir_inode=$(stat -c '%d:%i' -- "$BIN_DIR")
  chmod 0755 -- "$BIN_DIR"
fi
staged_agent=$(mktemp "${BIN_DIR}/.nodedance-agent-installer.XXXXXXXX") || fail 'cannot create a private temporary binary in /usr/local/bin'
install -m 0755 "$tmp_dir/$asset" "$staged_agent"
[[ -f "$staged_agent" && ! -L "$staged_agent" ]] || fail 'temporary Agent binary changed during installation'
ln -- "$staged_agent" "$AGENT_PATH" || fail "Agent binary appeared during installation; refusing to overwrite $AGENT_PATH"
binary_installed=1
installed_inode=$(stat -c '%d:%i' -- "$AGENT_PATH")
rm -f -- "$staged_agent"
staged_agent=""

prepare_state

enrollment_started=1
if (( development )); then
  printf '%s\n' "$token" | "$AGENT_PATH" enroll --dev --server "$server_url" --config "$CONFIG_PATH" --token-stdin || fail 'Agent enrollment failed'
else
  printf '%s\n' "$token" | "$AGENT_PATH" enroll --server "$server_url" --config "$CONFIG_PATH" --token-stdin || fail 'Agent enrollment failed'
fi
unset token

"$AGENT_PATH" install-systemd --config "$CONFIG_PATH" --enable || fail 'systemd installation or Agent Core connection verification failed'
printf 'NodeDance Agent is installed and enabled for Core %s. The node name is the name attached to this enrollment token in Core.\n' "$server_url"
