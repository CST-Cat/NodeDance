#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$repo_root/scripts/test/s03-core-process-guard.sh"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/nodedance-s03-process.XXXXXX")"
chmod 700 "$tmp_dir"
cleanup_test_children() {
	local status=$?
	local pid
	trap - EXIT
	for pid in "${child_pids[@]:-}"; do
		if [[ -n "$pid" ]]; then
			kill -KILL "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	rm -rf -- "$tmp_dir"
	exit "$status"
}
trap cleanup_test_children EXIT
declare -a child_pids=()

track_child() { child_pids+=("$1"); }
forget_child() {
	local target="$1" pid
	local -a retained=()
	for pid in "${child_pids[@]}"; do
		[[ "$pid" == "$target" ]] || retained+=("$pid")
	done
	child_pids=("${retained[@]}")
}

sleep_bin="$(readlink -f "$(command -v sleep)")"
python_bin="$(readlink -f "$(command -v python3)")"
[[ -x "$sleep_bin" && -x "$python_bin" ]]

assert_no_live_child() {
	local pid="$1"
	if kill -0 "$pid" 2>/dev/null; then
		printf 'child %s remains after bounded cleanup\n' "$pid" >&2
		return 1
	fi
}

# A direct child starts as a shell image, then execs the exact target. The
# missing-start cleanup path must retry STARTING and capture the later RUNNING
# identity instead of abandoning cleanup on the first observation.
bash -c 'sleep 1; exec "$1" 60' _ "$sleep_bin" &
startup_pid=$!
track_child "$startup_pid"
startup_state="$(s03_core_process_state "$startup_pid" "$sleep_bin" "$$")"
case "$startup_state" in
	STARTING\ *) ;;
	*) printf 'expected startup transition, got %s\n' "$startup_state" >&2; exit 1 ;;
esac
start_capture="$(date +%s%N)"
s03_terminate_core_child "$startup_pid" "$sleep_bin" "$$" "" "$tmp_dir/startup.log"
elapsed_ms=$((($(date +%s%N) - start_capture) / 1000000))
(( elapsed_ms < 7000 )) || { printf 'startup-race cleanup exceeded bound: %sms\n' "$elapsed_ms" >&2; exit 1; }
assert_no_live_child "$startup_pid"
forget_child "$startup_pid"
printf 'startup transition and missing-start cleanup: PASS (%sms)\n' "$elapsed_ms"

# A normal child exits on TERM and is reaped only after the exact captured
# process identity is observed as gone/zombie.
"$sleep_bin" 60 &
normal_pid=$!
track_child "$normal_pid"
normal_state="$(s03_wait_for_core_identity "$normal_pid" "$sleep_bin" "$$" 50)"
normal_start="${normal_state#RUNNING }"
[[ "$normal_state" == "RUNNING $normal_start" && -n "$normal_start" ]]
s03_terminate_core_child "$normal_pid" "$sleep_bin" "$$" "$normal_start" "$tmp_dir/normal.log"
assert_no_live_child "$normal_pid"
forget_child "$normal_pid"
printf 'verified normal TERM cleanup: PASS\n'

# A different start time is an ownership change. The helper must not signal or
# wait that PID; the test then cleans up its own still-running child correctly.
"$sleep_bin" 60 &
changed_pid=$!
track_child "$changed_pid"
changed_state="$(s03_wait_for_core_identity "$changed_pid" "$sleep_bin" "$$" 50)"
changed_start="${changed_state#RUNNING }"
wrong_start="${changed_start}x"
if s03_terminate_core_child "$changed_pid" "$sleep_bin" "$$" "$wrong_start" "$tmp_dir/changed.log" 2>"$tmp_dir/changed.err"; then
	printf 'identity-change cleanup unexpectedly succeeded\n' >&2
	exit 1
fi
[[ -s "$tmp_dir/changed.err" ]]
kill -0 "$changed_pid"
s03_terminate_core_child "$changed_pid" "$sleep_bin" "$$" "$changed_start" "$tmp_dir/changed-reap.log"
assert_no_live_child "$changed_pid"
forget_child "$changed_pid"
printf 'changed identity is left untouched: PASS\n'

# A TERM-ignoring owned child must reach the bounded KILL path and be reaped.
"$python_bin" -c 'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)' &
ignore_pid=$!
track_child "$ignore_pid"
disown "$ignore_pid"
ignore_state="$(s03_wait_for_core_identity "$ignore_pid" "$python_bin" "$$" 50)"
ignore_start="${ignore_state#RUNNING }"
ignore_capture="$(date +%s%N)"
s03_terminate_core_child "$ignore_pid" "$python_bin" "$$" "$ignore_start" "$tmp_dir/ignore-term.log"
ignore_elapsed_ms=$((($(date +%s%N) - ignore_capture) / 1000000))
(( ignore_elapsed_ms < 8000 )) || { printf 'TERM-ignore cleanup exceeded bound: %sms\n' "$ignore_elapsed_ms" >&2; exit 1; }
assert_no_live_child "$ignore_pid"
forget_child "$ignore_pid"
printf 'TERM ignored then bounded KILL cleanup: PASS (%sms)\n' "$ignore_elapsed_ms"

# Once a direct child becomes a zombie, a different start time must be an
# explicit ownership failure; it must not be silently waited/reaped.
cat >"$tmp_dir/zombie-parent.py" <<'PY'
import os
import signal
import sys
import time

pid_path, sleep_bin = sys.argv[1:]
ready = False
def release(_signum, _frame):
	global ready
	ready = True

signal.signal(signal.SIGUSR1, release)
child = os.fork()
if child == 0:
	os.execv(sleep_bin, [sleep_bin, "0"])
with open(pid_path, "w", encoding="ascii") as output:
	output.write(str(child))
while not ready:
	time.sleep(0.02)
os.waitpid(child, 0)
PY
"$python_bin" "$tmp_dir/zombie-parent.py" "$tmp_dir/zombie.pid" "$sleep_bin" &
zombie_parent=$!
track_child "$zombie_parent"
for _ in $(seq 1 50); do
	[[ -s "$tmp_dir/zombie.pid" ]] && break
	sleep 0.02
done
[[ -s "$tmp_dir/zombie.pid" ]] || { printf 'zombie parent did not publish child pid\n' >&2; exit 1; }
zombie_pid="$(cat "$tmp_dir/zombie.pid")"
wait_for_zombie=0
for _ in $(seq 1 50); do
	zombie_state="$(s03_core_process_state "$zombie_pid" "$sleep_bin" "$zombie_parent")"
	if [[ "$zombie_state" == "ZOMBIE "* ]]; then wait_for_zombie=1; break; fi
	sleep 0.02
done
(( wait_for_zombie == 1 )) || { printf 'could not observe direct-child zombie state\n' >&2; exit 1; }
if s03_terminate_core_child "$zombie_pid" "$sleep_bin" "$zombie_parent" "different-start" "$tmp_dir/zombie.log" 2>"$tmp_dir/zombie.err"; then
	printf 'different-start zombie cleanup unexpectedly succeeded\n' >&2
	exit 1
fi
grep -q 'zombie with a different start time' "$tmp_dir/zombie.err"
[[ -r "/proc/$zombie_pid/stat" ]] || { printf 'helper reaped an unowned zombie\n' >&2; exit 1; }
kill -USR1 "$zombie_parent"
wait "$zombie_parent"
forget_child "$zombie_parent"
printf 'different-start zombie is explicitly rejected: PASS\n'
