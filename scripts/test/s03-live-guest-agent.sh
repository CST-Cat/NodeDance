#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
tool_root="${NODEDANCE_TOOL_ROOT:-$repo_root/.tools}"
if [[ ! -x "$tool_root/go1.26.8/bin/go" && -x "$repo_root/../NodeDance/.tools/go1.26.8/bin/go" ]]; then
	tool_root="$repo_root/../NodeDance/.tools"
fi
go_bin="$tool_root/go1.26.8/bin/go"
[[ -x "$go_bin" ]] || { printf 'Pinned Go 1.26.8 is unavailable: %s\n' "$go_bin" >&2; exit 2; }
[[ "$($go_bin version)" == *"go1.26.8"* ]] || { printf 'Go version is not locked to 1.26.8\n' >&2; exit 2; }

run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
work_dir="$repo_root/.artifacts/work-s03/guest-agent/$run_id"
mkdir -p "$work_dir/bin" "$work_dir/core"
chmod 700 "$work_dir" "$work_dir/bin" "$work_dir/core"
core_log="$work_dir/core.log"
guest_log="$work_dir/guest.log"
core_pid=""
core_binary="$work_dir/bin/s03-guest-core-harness"
core_start_time=""
core_parent_pid="$$"
source "$repo_root/scripts/test/s03-core-process-guard.sh"

core_process_is_owned() {
	[[ -n "$core_pid" && -n "$core_start_time" ]] || return 1
	local state
	state="$(s03_core_process_state "$core_pid" "$core_binary" "$core_parent_pid" "$core_start_time" 2>/dev/null)" || return 1
	[[ "$state" == "RUNNING $core_start_time" ]]
}

cleanup() {
	local exit_status=$?
	trap - EXIT
	if [[ -n "$core_pid" ]] && ! s03_terminate_core_child \
		"$core_pid" "$core_binary" "$core_parent_pid" "$core_start_time" "$core_log"; then
		exit_status=1
	fi
	exit "$exit_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$repo_root"
GOTOOLCHAIN=local "$go_bin" build -mod=readonly -trimpath -o "$core_binary" ./scripts/test/s03-guest-core-harness
NODEDANCE_S03_GUEST_CORE_WORK="$work_dir/core" \
	"$core_binary" >"$core_log" 2>&1 &
core_pid=$!

for _ in $(seq 1 50); do
	core_state="$(s03_core_process_state "$core_pid" "$core_binary" "$core_parent_pid" 2>/dev/null)" || core_state="UNKNOWN"
	case "$core_state" in
		RUNNING\ *)
			core_start_time="${core_state#RUNNING }"
			break
			;;
		STARTING\ *|UNKNOWN) sleep 0.1 ;;
		GONE|ZOMBIE\ *|OTHER\ *) break ;;
		*) printf 'Invalid Core process state while starting: %s\n' "$core_state" >&2; break ;;
	esac
done
if [[ -z "$core_start_time" ]]; then
	cat "$core_log" >&2
	printf 'Could not capture the Core harness PID/start-time/command identity. Evidence: %s\n' "$core_log" >&2
	exit 1
fi

manifest_path=""
for _ in $(seq 1 200); do
	ready_json="$(sed -n 's/^READY //p' "$core_log" | head -n 1)"
	if [[ -n "$ready_json" ]]; then
		manifest_path="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["manifest"])' "$ready_json")"
		break
	fi
	if ! core_process_is_owned; then
		cat "$core_log" >&2
		printf 'Real TLS Core harness exited before guest enrollment setup. Evidence: %s\n' "$core_log" >&2
		exit 1
	fi
	sleep 0.1
done
if [[ -z "$manifest_path" ]]; then
	cat "$core_log" >&2
	printf 'Real TLS Core harness did not become ready within 20s. Evidence: %s\n' "$core_log" >&2
	exit 2
fi
printf 'Real TLS Core harness ready; enrollment manifest is private under %s\n' "$work_dir/core"

set +e
python3 scripts/test/s03-guest.py --rounds 1 --agent-core-manifest "$manifest_path" 2>&1 | tee "$guest_log"
guest_exit=${PIPESTATUS[0]}
set -e
printf 'Real guest Agent/Core evidence: %s (exit=%d)\n' "$guest_log" "$guest_exit"
exit "$guest_exit"
