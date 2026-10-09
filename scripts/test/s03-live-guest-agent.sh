#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
tool_root="${NODEDANCE_TOOL_ROOT:-$repo_root/.tools}"
if [[ ! -x "$tool_root/go1.26.8/bin/go" && -x "$repo_root/../NodeDance/.tools/go1.26.8/bin/go" ]]; then
	tool_root="$repo_root/../NodeDance/.tools"
fi
go_bin="$tool_root/go1.26.8/bin/go"
[[ -x "$go_bin" ]] || { printf 'Pinned Go 1.26.8 is unavailable: %s\n' "$go_bin" >&2; exit 2; }
[[ "$("$go_bin" version)" == *"go1.26.8"* ]] || { printf 'Go version is not locked to 1.26.8\n' >&2; exit 2; }

run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
work_dir="$repo_root/.artifacts/work-s03/guest-agent/$run_id"
mkdir -p "$work_dir/bin" "$work_dir/core" "$work_dir/browser"
chmod 700 "$work_dir" "$work_dir/bin" "$work_dir/core" "$work_dir/browser"
core_log="$work_dir/core.log"
guest_log="$work_dir/guest.log"
browser_log="$work_dir/browser.log"
phase_file="$work_dir/browser/guest-phase.json"
browser_evidence="$work_dir/browser/engine"
browser_ready="$work_dir/browser/ready"
core_pid=""
core_binary="$work_dir/bin/s03-guest-core-harness"
core_start_time=""
web_pid=""
web_start_time=""
guest_pid=""
guest_start_time=""
guest_exit=2
web_exit=2
stage_exit=2
core_parent_pid="$$"
bash_bin="$(readlink -f "$(command -v bash)")"
python_bin="$(readlink -f "$(command -v python3)")"
source "$repo_root/scripts/test/s03-core-process-guard.sh"

cleanup() {
	local exit_status=$?
	trap - EXIT
	if [[ -n "$guest_pid" ]] && ! s03_terminate_core_child \
		"$guest_pid" "$python_bin" "$core_parent_pid" "$guest_start_time" "$guest_log"; then
		exit_status=1
	fi
	if [[ -n "$web_pid" ]] && ! s03_terminate_core_child \
		"$web_pid" "$bash_bin" "$core_parent_pid" "$web_start_time" "$browser_log"; then
		exit_status=1
	fi
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
	NODEDANCE_S03_PUBLIC_ORIGIN="http://127.0.0.1:4187" \
	"$core_binary" >"$core_log" 2>&1 &
core_pid=$!

for _ in $(seq 1 50); do
	core_state="$(s03_core_process_state "$core_pid" "$core_binary" "$core_parent_pid" 2>/dev/null)" || core_state="UNKNOWN"
	case "$core_state" in
		RUNNING\ *) core_start_time="${core_state#RUNNING }"; break ;;
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

ready_json=""
for _ in $(seq 1 200); do
	ready_json="$(sed -n 's/^READY //p' "$core_log" | head -n 1)"
	if [[ -n "$ready_json" ]]; then break; fi
	core_state="$(s03_core_process_state "$core_pid" "$core_binary" "$core_parent_pid" "$core_start_time")"
	if [[ "$core_state" != "RUNNING $core_start_time" ]]; then
		cat "$core_log" >&2
		printf 'Real TLS Core harness exited before enrollment setup (%s). Evidence: %s\n' "$core_state" "$core_log" >&2
		exit 1
	fi
	sleep 0.1
done
if [[ -z "$ready_json" ]]; then
	cat "$core_log" >&2
	printf 'Real TLS Core harness did not become ready within 20s. Evidence: %s\n' "$core_log" >&2
	exit 2
fi

manifest_path="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["manifest"])' "$ready_json")"
core_url="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["coreUrl"])' "$ready_json")"
guest_url="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["guestUrl"])' "$ready_json")"
public_origin="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["publicOrigin"])' "$ready_json")"
core_ca="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["caCertificate"])' "$ready_json")"
node_id="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["nodeId"])' "$ready_json")"
[[ "$core_url" =~ ^https://127\.0\.0\.1:[0-9]+$ ]] || { printf 'Core listener URL is not host loopback: %s\n' "$core_url" >&2; exit 1; }
[[ "$guest_url" =~ ^https://10\.0\.2\.2:[0-9]+$ ]] || { printf 'Agent URL is not the isolated QEMU host gateway: %s\n' "$guest_url" >&2; exit 1; }
[[ "$public_origin" == "http://127.0.0.1:4187" ]] || { printf 'Core browser PublicOrigin is not the exact loopback Vite origin: %s\n' "$public_origin" >&2; exit 1; }
[[ "$core_ca" == "$work_dir/core/core-ca.pem" && -f "$core_ca" && ! -L "$core_ca" ]] || { printf 'Core CA path is outside this private harness: %s\n' "$core_ca" >&2; exit 1; }
python3 - "$work_dir/harness.json" "$core_url" "$guest_url" "$public_origin" "$node_id" <<'PY'
import json
import pathlib
import sys

target = pathlib.Path(sys.argv[1])
target.write_text(json.dumps({
    "coreUrl": sys.argv[2],
    "agentServerUrl": sys.argv[3],
    "browserPublicOrigin": sys.argv[4],
    "nodeId": sys.argv[5],
}, indent=2) + "\n", encoding="utf-8")
target.chmod(0o600)
PY
python3 - "$phase_file" <<'PY'
import json
import pathlib
import sys

target = pathlib.Path(sys.argv[1])
target.write_text(json.dumps({"phase": "before_agent", "events": [], "eventValues": {}, "guestFinished": False}) + "\n", encoding="utf-8")
target.chmod(0o600)
PY
printf 'Real TLS Core ready: Core=%s AgentServerURL=%s BrowserOrigin=%s node=%s\n' "$core_url" "$guest_url" "$public_origin" "$node_id"

NODEDANCE_S03_CORE_TARGET="$core_url" NODEDANCE_S03_CORE_CA="$core_ca" \
	NODEDANCE_S03_PHASE_FILE="$phase_file" NODEDANCE_S03_WEB_ORIGIN="$public_origin" \
	NODEDANCE_S03_BROWSER_EVIDENCE="$browser_evidence" NODEDANCE_S03_BROWSER_READY_PREFIX="$browser_ready" \
	"$bash_bin" "$repo_root/scripts/test/s03-live-web.sh" >"$browser_log" 2>&1 &
web_pid=$!
web_state="$(s03_wait_for_core_identity "$web_pid" "$bash_bin" "$core_parent_pid" 50)" || {
	cat "$browser_log" >&2 || true
	printf 'Cannot capture the owned Vite/Playwright runner identity: %s\n' "$web_state" >&2
	exit 1
}
web_start_time="${web_state#RUNNING }"

all_browsers_ready=0
for _ in $(seq 1 900); do
	if [[ -f "${browser_ready}-chromium.json" && -f "${browser_ready}-webkit.json" && -f "${browser_ready}-firefox.json" ]]; then
		all_browsers_ready=1
		break
	fi
	web_state="$(s03_core_process_state "$web_pid" "$bash_bin" "$core_parent_pid" "$web_start_time")"
	case "$web_state" in
		"RUNNING $web_start_time"|UNKNOWN) sleep 0.2 ;;
		*) cat "$browser_log" >&2 || true; printf 'Browser runner exited before all projects authenticated (%s).\n' "$web_state" >&2; exit 1 ;;
	esac
done
if (( all_browsers_ready == 0 )); then
	cat "$browser_log" >&2 || true
	printf 'All three authenticated browser projects did not become ready within 180s.\n' >&2
	exit 1
fi

printf 'All three real browser projects are authenticated; starting the isolated real Linux guest.\n'
NODEDANCE_S03_PHASE_FILE="$phase_file" \
	"$python_bin" "$repo_root/scripts/test/s03-guest.py" --rounds 1 --agent-core-manifest "$manifest_path" >"$guest_log" 2>&1 &
guest_pid=$!
guest_state="$(s03_wait_for_core_identity "$guest_pid" "$python_bin" "$core_parent_pid" 50)" || {
	cat "$guest_log" >&2 || true
	printf 'Cannot capture the owned QEMU guest runner identity: %s\n' "$guest_state" >&2
	exit 1
}
guest_start_time="${guest_state#RUNNING }"

guest_done=0
web_done=0
for _ in $(seq 1 1200); do
	if (( guest_done == 0 )); then
		guest_state="$(s03_core_process_state "$guest_pid" "$python_bin" "$core_parent_pid" "$guest_start_time")"
		case "$guest_state" in
			"RUNNING $guest_start_time"|UNKNOWN) ;;
			GONE|"ZOMBIE $guest_start_time")
				if wait "$guest_pid"; then guest_exit=0; else guest_exit=$?; fi
				guest_pid=""
				guest_done=1
				python3 - "$phase_file" "$guest_exit" <<'PY'
import json
import os
import pathlib
import sys

target = pathlib.Path(sys.argv[1])
try:
    payload = json.loads(target.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    payload = {"phase": "unknown", "events": [], "eventValues": {}}
payload["guestRunnerFinished"] = True
payload["guestRunnerExitCode"] = int(sys.argv[2])
temporary = target.with_name(target.name + f".{os.getpid()}.tmp")
temporary.write_text(json.dumps(payload, ensure_ascii=False) + "\n", encoding="utf-8")
temporary.chmod(0o600)
os.replace(temporary, target)
PY
				cat "$guest_log"
				printf 'Real guest Agent/Core evidence: %s (exit=%d)\n' "$guest_log" "$guest_exit"
				;;
			*) printf 'Guest runner process identity changed (%s); refusing signal or wait.\n' "$guest_state" >&2; guest_exit=1; guest_done=1; guest_pid="" ;;
		esac
	fi
	if (( web_done == 0 )); then
		web_state="$(s03_core_process_state "$web_pid" "$bash_bin" "$core_parent_pid" "$web_start_time")"
		case "$web_state" in
			"RUNNING $web_start_time"|UNKNOWN) ;;
			GONE|"ZOMBIE $web_start_time")
				if wait "$web_pid"; then web_exit=0; else web_exit=$?; fi
				web_pid=""
				web_done=1
				cat "$browser_log"
				printf 'Real dashboard browser evidence: %s (exit=%d)\n' "$browser_log" "$web_exit"
				;;
			*) printf 'Browser runner process identity changed (%s); refusing signal or wait.\n' "$web_state" >&2; web_exit=1; web_done=1; web_pid="" ;;
		esac
	fi
	if (( guest_done && web_done )); then break; fi
	sleep 0.5
done
if (( guest_done == 0 || web_done == 0 )); then
	printf 'Guest/browser integration exceeded its 600s bound (guest_done=%d browser_done=%d). Evidence: %s\n' \
		"$guest_done" "$web_done" "$work_dir" >&2
	exit 1
fi

guest_summary="$(sed -n 's/^S03 guest evidence: //p' "$guest_log" | tail -n 1)"
browser_summary="$work_dir/browser/engine-summary.json"
printf 'Real guest summary: %s\n' "$guest_summary"
printf 'Real browser summary: %s\n' "$browser_summary"
printf 'S03 real guest/browser harness evidence: %s\n' "$work_dir"

if (( guest_exit == 1 || web_exit == 1 )); then stage_exit=1
elif (( guest_exit == 2 || web_exit == 2 )); then stage_exit=2
else stage_exit=0
fi
exit "$stage_exit"
