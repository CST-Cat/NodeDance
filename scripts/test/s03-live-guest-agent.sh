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

cleanup() {
	if [[ -n "$core_pid" ]] && kill -0 "$core_pid" 2>/dev/null; then
		kill -TERM "$core_pid" 2>/dev/null || true
		wait "$core_pid" 2>/dev/null || true
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$repo_root"
GOTOOLCHAIN=local "$go_bin" build -mod=readonly -trimpath -o "$work_dir/bin/s03-guest-core-harness" ./scripts/test/s03-guest-core-harness
NODEDANCE_S03_GUEST_CORE_WORK="$work_dir/core" \
	"$work_dir/bin/s03-guest-core-harness" >"$core_log" 2>&1 &
core_pid=$!

manifest_path=""
for _ in $(seq 1 200); do
	ready_json="$(sed -n 's/^READY //p' "$core_log" | head -n 1)"
	if [[ -n "$ready_json" ]]; then
		manifest_path="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["manifest"])' "$ready_json")"
		break
	fi
	if ! kill -0 "$core_pid" 2>/dev/null; then
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
