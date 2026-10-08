#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

tool_root="${NODEDANCE_TOOL_ROOT:-$repo_root/.tools}"
if [[ ! -x "$tool_root/node-v22.23.3/bin/node" && -x "$repo_root/../NodeDance/.tools/node-v22.23.3/bin/node" ]]; then
	tool_root="$repo_root/../NodeDance/.tools"
fi
if [[ -x "$tool_root/node-v22.23.3/bin/node" ]]; then
	export PATH="$tool_root/node-v22.23.3/bin:$tool_root/pnpm/node_modules/.bin:$PATH"
fi

node_version="$(node --version 2>/dev/null || true)"
pnpm_version="$(pnpm --version 2>/dev/null || true)"
[[ "$node_version" == "v22.23.3" ]] || { printf 'Expected pinned Node v22.23.3, found %s\n' "${node_version:-missing}" >&2; exit 2; }
[[ "$pnpm_version" == "12.10.1" ]] || { printf 'Expected pinned pnpm 12.10.1, found %s\n' "${pnpm_version:-missing}" >&2; exit 2; }

cd "$repo_root/web"

pnpm exec playwright test \
  --config playwright.s03.config.ts \
  tests/s03-metrics-panel.spec.ts \
  tests/s03-nodes-dashboard.spec.ts
