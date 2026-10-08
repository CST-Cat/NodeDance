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

version="$("$go_bin" version)"
if [[ "$version" != *"go1.26.8"* ]]; then
	printf 'Expected Go 1.26.8, found: %s\n' "$version" >&2
	exit 2
fi

output_dir="${S03_TEST_OUTPUT_DIR:-}"
if [[ -z "$output_dir" ]]; then
	output_dir="$(mktemp -d "${TMPDIR:-/tmp}/nodedance-s03-tests.XXXXXX")"
else
	mkdir -p "$output_dir"
fi
printf 'Pinned toolchain: %s\n' "$version"
printf 'Test output: %s\n' "$output_dir"
printf 'Using the checked-in read-only module graph; no module files will be rewritten.\n'

cd "$repo_root"
export GOTOOLCHAIN=local
"$go_bin" test -mod=readonly -race -count=1 -v ./internal/agent/metrics 2>&1 | tee "$output_dir/go-test.log"
"$go_bin" vet -mod=readonly ./internal/agent/metrics 2>&1 | tee "$output_dir/go-vet.log"

printf 'Deterministic collector checks exited successfully. Live host tests are opt-in and remain NOT_READY until scripts/test/s03-live-collector.sh runs them. Logs: %s\n' "$output_dir"
