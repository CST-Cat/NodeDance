#!/usr/bin/env bash

# Process identity and bounded cleanup helpers for the S03 live Core harness.
# This is intentionally scoped to a direct child started by the calling shell.

s03_core_process_state() {
	python3 - "$1" "$2" "$3" "${4:-}" <<'PY'
import os
import pathlib
import sys

pid = int(sys.argv[1])
expected_binary = os.path.realpath(sys.argv[2])
expected_parent = int(sys.argv[3])
expected_start = sys.argv[4]
process = pathlib.Path("/proc") / str(pid)
try:
	stat_line = (process / "stat").read_text(encoding="ascii")
except FileNotFoundError:
	print("GONE")
	raise SystemExit(0)
except OSError:
	print("UNKNOWN")
	raise SystemExit(0)

closing = stat_line.rfind(")")
if closing < 0:
	print("UNKNOWN")
	raise SystemExit(0)
fields = stat_line[closing + 1:].split()
if len(fields) <= 19:
	print("UNKNOWN")
	raise SystemExit(0)
state = fields[0]
start_time = fields[19]
try:
	status_lines = (process / "status").read_text(encoding="ascii").splitlines()
except FileNotFoundError:
	print("GONE")
	raise SystemExit(0)
except OSError:
	print("UNKNOWN")
	raise SystemExit(0)

values = {}
for line in status_lines:
	if line.startswith(("Tgid:", "PPid:")):
		key, value = line.split(":", 1)
		try:
			values[key] = int(value.strip())
		except ValueError:
			print("UNKNOWN")
			raise SystemExit(0)
if "Tgid" not in values or "PPid" not in values:
	print("UNKNOWN")
	raise SystemExit(0)
if values["Tgid"] != pid or values["PPid"] != expected_parent:
	print(f"OTHER {start_time}")
	raise SystemExit(0)

# A zombie has no usable cmdline or executable link. The caller may reap it
# only when a previous exact RUNNING identity established the same start time.
if state == "Z":
	print(f"ZOMBIE {start_time}")
	raise SystemExit(0)
if expected_start and start_time != expected_start:
	print(f"OTHER {start_time}")
	raise SystemExit(0)

try:
	command = tuple(part.decode(errors="replace") for part in (process / "cmdline").read_bytes().split(b"\0") if part)
	executable = os.readlink(process / "exe")
except FileNotFoundError:
	print("GONE")
	raise SystemExit(0)
except OSError:
	print("UNKNOWN")
	raise SystemExit(0)

# The PID is our direct child and has not changed TGID/parent. During fork/exec
# the command may briefly be the shell's pre-exec image; treat that as a bounded
# transition and never signal it until both argv[0] and /proc/PID/exe match.
if command and command[0] == sys.argv[2] and executable == expected_binary:
	print(f"RUNNING {start_time}")
else:
	print(f"STARTING {start_time}")
PY
}

s03_wait_for_core_identity() {
	local pid="$1" expected_binary="$2" expected_parent="$3" attempts="$4"
	local attempt state
	for ((attempt = 0; attempt < attempts; attempt++)); do
		state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent")"
		case "$state" in
			RUNNING\ *) printf '%s\n' "$state"; return 0 ;;
			STARTING\ *|UNKNOWN) sleep 0.1 ;;
			GONE|ZOMBIE\ *|OTHER\ *) printf '%s\n' "$state"; return 1 ;;
			*) printf 'Invalid Core process state while capturing identity: %s\n' "$state" >&2; return 1 ;;
		esac
	done
	printf 'UNKNOWN\n'
	return 1
}

s03_reap_verified_core_child() {
	local pid="$1" expected_binary="$2" expected_parent="$3" expected_start="$4"
	local state
	state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent" "$expected_start")"
	case "$state" in
		GONE|"ZOMBIE $expected_start") wait "$pid" 2>/dev/null || true; return 0 ;;
		"RUNNING $expected_start") return 1 ;;
		"ZOMBIE "*) printf 'Core child PID %s became a zombie with a different start time (%s); refusing to wait it.\n' "$pid" "$state" >&2; return 1 ;;
		OTHER\ *) printf 'Core child PID %s changed ownership/identity (%s); refusing to signal or wait it.\n' "$pid" "$state" >&2; return 1 ;;
		STARTING\ *|UNKNOWN) printf 'Core child PID %s identity is unprovable after cleanup (%s); leaving it untouched.\n' "$pid" "$state" >&2; return 1 ;;
		*) printf 'Invalid final Core process state for PID %s: %s\n' "$pid" "$state" >&2; return 1 ;;
	esac
}

s03_terminate_core_child() {
	local pid="$1" expected_binary="$2" expected_parent="$3" captured_start="$4" evidence="$5"
	local state start_time="$captured_start" attempt
	[[ -n "$pid" ]] || return 0

	if [[ -z "$start_time" ]]; then
		for ((attempt = 0; attempt < 50; attempt++)); do
			state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent")"
			case "$state" in
				RUNNING\ *) start_time="${state#RUNNING }"; break ;;
				STARTING\ *|UNKNOWN) sleep 0.1 ;;
				GONE)
					printf 'Core child PID %s exited before identity capture; no signal was sent.\n' "$pid" >&2
					return 0
					;;
				ZOMBIE\ *|OTHER\ *)
					printf 'Cannot prove Core child PID %s identity during cleanup (%s); no signal or wait was issued. Evidence: %s\n' \
						"$pid" "$state" "$evidence" >&2
					return 1
					;;
				*) printf 'Invalid Core process state during cleanup: %s\n' "$state" >&2; return 1 ;;
			esac
		done
		if [[ -z "$start_time" ]]; then
			printf 'Core child PID %s did not reach a verifiable executable within 5s; no signal or wait was issued. Evidence: %s\n' \
				"$pid" "$evidence" >&2
			return 1
		fi
	fi

	state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent" "$start_time")"
	case "$state" in
		"RUNNING $start_time") ;;
		GONE|"ZOMBIE $start_time") s03_reap_verified_core_child "$pid" "$expected_binary" "$expected_parent" "$start_time"; return $? ;;
		"ZOMBIE "*)
			printf 'Core child PID %s became a zombie with a different start time (%s); refusing to signal or wait it. Evidence: %s\n' \
				"$pid" "$state" "$evidence" >&2
			return 1
			;;
		OTHER\ *)
			printf 'Core child PID %s changed identity before TERM (%s); no signal or wait was issued. Evidence: %s\n' \
				"$pid" "$state" "$evidence" >&2
			return 1
			;;
		STARTING\ *|UNKNOWN)
			printf 'Core child PID %s identity became unprovable before TERM (%s); no signal or wait was issued. Evidence: %s\n' \
				"$pid" "$state" "$evidence" >&2
			return 1
			;;
		*) printf 'Invalid Core process state before TERM: %s\n' "$state" >&2; return 1 ;;
	esac

	# Recheck identity immediately before every signal. Only the exact direct
	# child with the captured start time can receive TERM or KILL.
	state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent" "$start_time")"
	if [[ "$state" != "RUNNING $start_time" ]]; then
		printf 'Core child PID %s changed identity before TERM (%s); refusing signal. Evidence: %s\n' "$pid" "$state" "$evidence" >&2
		return 1
	fi
	kill -TERM "$pid" 2>/dev/null || true
	for ((attempt = 0; attempt < 50; attempt++)); do
		state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent" "$start_time")"
		case "$state" in
			GONE|"ZOMBIE $start_time") s03_reap_verified_core_child "$pid" "$expected_binary" "$expected_parent" "$start_time"; return $? ;;
			"RUNNING $start_time") sleep 0.1 ;;
			UNKNOWN) sleep 0.1 ;;
			"ZOMBIE "*)
				printf 'Core child PID %s became a zombie with a different start time during TERM wait (%s); refusing KILL or wait. Evidence: %s\n' \
					"$pid" "$state" "$evidence" >&2
				return 1
				;;
			OTHER\ *)
				printf 'Core child PID %s changed ownership/identity during TERM wait (%s); refusing KILL or wait. Evidence: %s\n' \
					"$pid" "$state" "$evidence" >&2
				return 1
				;;
			STARTING\ *)
				printf 'Core child PID %s left its verified executable during TERM wait (%s); refusing KILL or wait. Evidence: %s\n' \
					"$pid" "$state" "$evidence" >&2
				return 1
				;;
			*) printf 'Invalid Core process state during TERM wait: %s\n' "$state" >&2; return 1 ;;
		esac
	done

	state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent" "$start_time")"
	if [[ "$state" != "RUNNING $start_time" ]]; then
		printf 'Core child PID %s did not remain the verified process at KILL timeout (%s); refusing KILL. Evidence: %s\n' \
			"$pid" "$state" "$evidence" >&2
		return 1
	fi
	state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent" "$start_time")"
	if [[ "$state" != "RUNNING $start_time" ]]; then
		printf 'Core child PID %s changed identity immediately before KILL (%s); refusing signal. Evidence: %s\n' "$pid" "$state" "$evidence" >&2
		return 1
	fi
	kill -KILL "$pid" 2>/dev/null || true
	for ((attempt = 0; attempt < 50; attempt++)); do
		state="$(s03_core_process_state "$pid" "$expected_binary" "$expected_parent" "$start_time")"
		case "$state" in
			GONE|"ZOMBIE $start_time") s03_reap_verified_core_child "$pid" "$expected_binary" "$expected_parent" "$start_time"; return $? ;;
			"RUNNING $start_time"|UNKNOWN) sleep 0.1 ;;
			"ZOMBIE "*)
				printf 'Core child PID %s became a zombie with a different start time during KILL wait (%s); refusing wait. Evidence: %s\n' \
					"$pid" "$state" "$evidence" >&2
				return 1
				;;
			OTHER\ *)
				printf 'Core child PID %s changed ownership/identity during KILL wait (%s); refusing wait. Evidence: %s\n' \
					"$pid" "$state" "$evidence" >&2
				return 1
				;;
			STARTING\ *)
				printf 'Core child PID %s no longer matches its verified executable after KILL (%s); refusing wait. Evidence: %s\n' \
					"$pid" "$state" "$evidence" >&2
				return 1
				;;
			*) printf 'Invalid Core process state during KILL wait: %s\n' "$state" >&2; return 1 ;;
		esac
	done
	printf 'Owned Core child PID %s survived bounded TERM/KILL cleanup. Evidence: %s\n' "$pid" "$evidence" >&2
	return 1
}
