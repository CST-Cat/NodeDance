#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"

go_bin="${NODEDANCE_GO_BIN:-}"
if [[ -z "$go_bin" ]]; then
	for candidate in \
		"$repo_root/.tools/go1.26.8/bin/go" \
		"$repo_root/../NodeDance/.tools/go1.26.8/bin/go"; do
		if [[ -x "$candidate" ]]; then
			go_bin="$candidate"
			break
		fi
	done
fi
if [[ -z "$go_bin" ]]; then
	printf 'Go 1.26.8 not found; set NODEDANCE_GO_BIN to the pinned executable.\n' >&2
	exit 2
fi

tool_version="$("$go_bin" version)"
if [[ "$tool_version" != *"go1.26.8"* ]]; then
	printf 'Expected Go 1.26.8, found: %s\n' "$tool_version" >&2
	exit 2
fi

output_dir="${S03_LIVE_TEST_OUTPUT_DIR:-}"
if [[ -z "$output_dir" ]]; then
	run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
	output_dir="$repo_root/.artifacts/work-s03/live-host/$run_id"
fi
mkdir -p "$output_dir"
chmod 700 "$output_dir"
json_log="$output_dir/go-test.jsonl"
text_log="$output_dir/go-test.log"
summary_file="$output_dir/summary.json"

printf 'Pinned toolchain: %s\n' "$tool_version"
printf 'Live host evidence: %s\n' "$output_dir"
printf 'Environment-dependent tests are running explicitly; any Go test skip is reported as NOT_READY.\n'

cd "$repo_root"
set +e
NODEDANCE_S03_LIVE=1 GOTOOLCHAIN=local "$go_bin" test -mod=readonly -count=1 -json \
	-run '^(TestLinuxProcAndFilesystemAgreement|TestLiveCPUAndMemoryLoadAffectsMeasurements)$' \
	./internal/agent/metrics 2>&1 | tee "$json_log" | tee "$text_log"
go_exit_code=${PIPESTATUS[0]}
set -e

set +e
python3 - "$json_log" "$summary_file" "$tool_version" "$go_exit_code" <<'PY'
import datetime
import json
import pathlib
import sys

log_path = pathlib.Path(sys.argv[1])
summary_path = pathlib.Path(sys.argv[2])
tool_version = sys.argv[3]
go_exit_code = int(sys.argv[4])
names = (
    "TestLinuxProcAndFilesystemAgreement",
    "TestLiveCPUAndMemoryLoadAffectsMeasurements",
)
tests = {name: {"status": "NOT_READY", "reason": "no test result event"} for name in names}
current_output = {}
package_failed = go_exit_code != 0

for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        continue
    name = event.get("Test")
    action = event.get("Action")
    if name in tests:
        if action == "output":
            current_output.setdefault(name, []).append(event.get("Output", "").rstrip())
        elif action in {"pass", "fail", "skip"}:
            tests[name] = {
                "status": {"pass": "PASS", "fail": "FAIL", "skip": "NOT_READY"}[action],
                "reason": "\n".join(current_output.get(name, []))[-2000:],
            }
    if event.get("Action") == "fail" and not name:
        package_failed = True

if package_failed:
    for name, result in tests.items():
        if result["status"] == "NOT_READY" and result["reason"] == "no test result event":
            tests[name] = {"status": "FAIL", "reason": "go test exited unsuccessfully before reporting this case"}

case_statuses = [result["status"] for result in tests.values()]
overall = "FAIL" if "FAIL" in case_statuses else "NOT_READY" if "NOT_READY" in case_statuses else "PASS"
summary = {
    "schema": 1,
    "stage": "S03",
    "case": "s03_live_host_collector_probes",
    "status": overall,
    "stage_status": "NOT_READY",
    "toolchain": tool_version,
    "go_test_exit_code": go_exit_code,
    "updated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "tests": tests,
    "evidence_log": str(log_path),
    "reason": "live host probes are supporting evidence; isolated guest and Agent/Core acceptance are still required",
}
summary_path.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
print(f"Live host probes: {overall}; full S03 remains NOT_READY; summary={summary_path}")
sys.exit({"PASS": 0, "FAIL": 1, "NOT_READY": 2}[overall])
PY
report_exit_code=$?
set -e

if (( go_exit_code != 0 )); then
	exit 1
fi
exit "$report_exit_code"
