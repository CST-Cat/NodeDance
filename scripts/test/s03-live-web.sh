#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
tool_root="${NODEDANCE_TOOL_ROOT:-$repo_root/.tools}"
if [[ ! -x "$tool_root/node-v22.23.3/bin/node" && -x "$repo_root/../NodeDance/.tools/node-v22.23.3/bin/node" ]]; then
	tool_root="$repo_root/../NodeDance/.tools"
fi
node_bin="$tool_root/node-v22.23.3/bin/node"
[[ -x "$node_bin" ]] || { printf 'Pinned Node 22.23.3 is unavailable: %s\n' "$node_bin" >&2; exit 2; }
[[ "$("$node_bin" --version)" == "v22.23.3" ]] || { printf 'Node version is not locked to 22.23.3\n' >&2; exit 2; }

core_target="${NODEDANCE_S03_CORE_TARGET:-}"
core_ca="${NODEDANCE_S03_CORE_CA:-}"
phase_file="${NODEDANCE_S03_PHASE_FILE:-}"
web_origin="${NODEDANCE_S03_WEB_ORIGIN:-http://127.0.0.1:4187}"
evidence_prefix="${NODEDANCE_S03_BROWSER_EVIDENCE:-}"
ready_prefix="${NODEDANCE_S03_BROWSER_READY_PREFIX:-}"
[[ "$core_target" == https://127.0.0.1:* ]] || { printf 'S03 browser requires the actual loopback Core URL, got %s\n' "$core_target" >&2; exit 2; }
[[ -f "$core_ca" && ! -L "$core_ca" ]] || { printf 'S03 browser requires the private real Core CA file: %s\n' "$core_ca" >&2; exit 2; }
[[ -n "$phase_file" && -n "$evidence_prefix" && -n "$ready_prefix" ]] || { printf 'S03 browser phase/evidence/readiness paths are required\n' >&2; exit 2; }
[[ "$web_origin" == "http://127.0.0.1:4187" ]] || { printf 'S03 browser origin must be exact host loopback http://127.0.0.1:4187\n' >&2; exit 2; }

work_dir="$(dirname -- "$evidence_prefix")"
mkdir -p "$work_dir"
chmod 700 "$work_dir"
vite_log="$work_dir/vite.log"
browser_log="$work_dir/playwright.log"
vite_pid=""
vite_start=""
browser_pid=""
browser_start=""
script_exit=0
source "$repo_root/scripts/test/s03-core-process-guard.sh"

cleanup() {
	local status=$?
	trap - EXIT
	if [[ -n "$browser_pid" ]] && ! s03_terminate_core_child "$browser_pid" "$node_bin" "$$" "$browser_start" "$browser_log"; then
		status=1
	fi
	if [[ -n "$vite_pid" ]] && ! s03_terminate_core_child "$vite_pid" "$node_bin" "$$" "$vite_start" "$vite_log"; then
		status=1
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$repo_root/web"
NODEDANCE_S03_CORE_TARGET="$core_target" NODEDANCE_S03_CORE_CA="$core_ca" \
	"$node_bin" "$repo_root/web/node_modules/vite/bin/vite.js" \
	--host 127.0.0.1 --port 4187 --strictPort >"$vite_log" 2>&1 &
vite_pid=$!
vite_state="$(s03_wait_for_core_identity "$vite_pid" "$node_bin" "$$" 50)" || {
	cat "$vite_log" >&2
	printf 'Cannot capture the owned Vite loopback server identity: %s\n' "$vite_state" >&2
	exit 1
}
vite_start="${vite_state#RUNNING }"

vite_ready=0
for _ in $(seq 1 100); do
	if curl --fail --silent --show-error "$web_origin/" -o /dev/null 2>/dev/null; then
		vite_ready=1
		break
	fi
	vite_state="$(s03_core_process_state "$vite_pid" "$node_bin" "$$" "$vite_start")"
	case "$vite_state" in
		"RUNNING $vite_start") sleep 0.1 ;;
		*) cat "$vite_log" >&2; printf 'Owned Vite loopback server stopped before readiness (%s).\n' "$vite_state" >&2; exit 1 ;;
	esac
done
if (( vite_ready == 0 )); then
	cat "$vite_log" >&2
	printf 'Vite did not become ready on %s within 10s.\n' "$web_origin" >&2
	exit 1
fi

cd "$repo_root/web"
PLAYWRIGHT_BASE_URL="$web_origin" NODEDANCE_S03_PHASE_FILE="$phase_file" \
	NODEDANCE_S03_BROWSER_EVIDENCE="$evidence_prefix" NODEDANCE_S03_BROWSER_READY_PREFIX="$ready_prefix" \
	"$node_bin" "$repo_root/web/node_modules/@playwright/test/cli.js" test \
	--config playwright.s03-live.config.ts tests/s03-live-dashboard.spec.ts >"$browser_log" 2>&1 &
browser_pid=$!
browser_state="$(s03_wait_for_core_identity "$browser_pid" "$node_bin" "$$" 50)" || {
	cat "$browser_log" >&2 || true
	printf 'Cannot capture the owned Playwright process identity: %s\n' "$browser_state" >&2
	exit 1
}
browser_start="${browser_state#RUNNING }"

browser_ready=0
for _ in $(seq 1 900); do
	if [[ -f "${ready_prefix}-chromium.json" && -f "${ready_prefix}-webkit.json" && -f "${ready_prefix}-firefox.json" ]]; then
		browser_ready=1
		break
	fi
	browser_state="$(s03_core_process_state "$browser_pid" "$node_bin" "$$" "$browser_start")"
	case "$browser_state" in
		"RUNNING $browser_start"|UNKNOWN) sleep 0.2 ;;
		*) cat "$browser_log" >&2 || true; printf 'Playwright exited before all three engines authenticated and became ready (%s).\n' "$browser_state" >&2; exit 1 ;;
	esac
done
if (( browser_ready == 0 )); then
	cat "$browser_log" >&2 || true
	printf 'The three real browser projects did not become ready within 180s. Evidence: %s\n' "$browser_log" >&2
	exit 1
fi
printf 'All three browser projects are authenticated against the real loopback-origin Core; starting the QEMU guest now.\n'

browser_done=0
browser_exit=0
for _ in $(seq 1 1200); do
	browser_state="$(s03_core_process_state "$browser_pid" "$node_bin" "$$" "$browser_start")"
	case "$browser_state" in
		"RUNNING $browser_start"|UNKNOWN) sleep 0.5 ;;
		GONE|"ZOMBIE $browser_start")
			if wait "$browser_pid"; then browser_exit=0; else browser_exit=$?; fi
			browser_pid=""
			browser_done=1
			break
			;;
		"ZOMBIE "*|OTHER\ *|STARTING\ *)
			printf 'Playwright process changed ownership or identity; refusing to signal/wait it (%s).\n' "$browser_state" >&2
			browser_exit=1
			break
			;;
		*) printf 'Invalid Playwright process state: %s\n' "$browser_state" >&2; browser_exit=1; break ;;
	esac
done
cat "$browser_log"
if (( browser_done == 0 )); then
	printf 'Real guest dashboard browser run exceeded its 600s bound. Evidence: %s\n' "$browser_log" >&2
	exit 1
fi

browser_status="$(python3 - "$evidence_prefix" "$browser_exit" <<'PY'
import json
import pathlib
import sys

prefix = pathlib.Path(sys.argv[1])
exit_code = int(sys.argv[2])
results = []
for engine in ("chromium", "webkit", "firefox"):
	path = prefix.with_name(prefix.name + f"-{engine}.json")
	try:
		results.append(json.loads(path.read_text(encoding="utf-8")))
	except (OSError, json.JSONDecodeError):
		results.append({"engine": engine, "status": "FAIL", "reason": "missing or invalid evidence"})
statuses = [item.get("status", "FAIL") for item in results]
if exit_code or "FAIL" in statuses:
	status = "FAIL"
elif all(value == "PASS" for value in statuses):
	status = "PASS"
else:
	status = "NOT_READY"
summary = {"status": status, "engines": results}
summary_path = prefix.with_name(prefix.name + "-summary.json")
summary_path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
summary_path.chmod(0o600)
print(f"S03 browser summary: {summary_path} status={status}")
print("S03 browser engines: " + ", ".join(f"{item.get('engine')}={item.get('status')}" for item in results))
PY
)"
printf '%s\n' "$browser_status"
case "$browser_status" in
	*"status=PASS"*) script_exit=0 ;;
	*"status=NOT_READY"*) script_exit=2 ;;
	*) script_exit=1 ;;
esac
exit "$script_exit"
