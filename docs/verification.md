# Engineering verification / 工程验证

Run the smallest checks relevant to the change. The commands below describe maintained verification entrypoints, not a record that every suite passed for the current checkout. Use temporary data, loopback listeners and disposable fixtures; browser suites never target production or real remote resources. Report skipped root, kernel or Docker cases as skipped.

按改动范围验证，不将旧环境、旧操作数量、截图数量或一次压测结果当作当前版本的验收证明。文档变更仅检查内容、链接、命令与差异，不构建 Web 或部署生产。

## Build and shared contract / 构建与契约

- `make build` checks and builds Web before embedding it in the Core and building both Go binaries. Do not independently build Go with stale Web assets.
- `make check` runs frontend checks, Python tests, Go tests and generated interface coverage. Root-only tests need root; integration-specific requirements are described by the tests.
- `python3 scripts/interface-coverage.py --check` compares the generated interface table with the shared catalogue. Discover actual request schemas from the connected Core; do not freeze operation counts.
- `go test -race ./internal/control ./internal/core/cli` checks the shared client/executor, precision, upload and terminal workflows. Run relevant package tests for the code changed.

## Browser behavior / 浏览器行为

First run `make build`; browser scripts use the resulting `.build/nodedance` with fresh embedded assets. Installing Chromium is environment setup, not a product change. Existing suites include:

| Area | Command |
| --- | --- |
| Shared interfaces and CLI/TUI-facing contract | `node web/scripts/audit-interfaces.cjs` |
| Guest authentication, publication and adversarial boundaries | `NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs` |
| Root Agent file fixtures, dialogs and mutation recovery | `node web/scripts/audit-files.cjs` |
| Account management and sessions | `node web/scripts/audit-accounts.cjs` |
| Update submission, history, rollback and backup dialogs | `node web/scripts/audit-updates.cjs` |
| RSS/Atom administration | `node web/scripts/audit-feeds.cjs` |
| Domains and certificates | `node web/scripts/audit-domains.cjs` |

The guest suite discovers operation coverage dynamically and retains anonymous, forged-identity, CSRF, WebSocket, stored-injection, concurrent-refresh and immediate-withdrawal checks. Keep its scripts and [security guide](guest-security.md). File-manager work also requires the maintained file audit and `node web/scripts/file-notices.cjs --check`; see [file management](file-manager.md).

Functional assertions, geometry, optional screenshots, manual visual review, device/touch checks and deployed-asset checks are distinct evidence. Browser suites do not generate screenshots by default. Add only a focused regression for a reproduced defect; do not multiply page/theme/width/language combinations.

## Root and Docker execution / root 与 Docker 执行

Root command, terminal, file and update tests use isolated state and retain credentials/path protections. The Docker integration entrypoint is `TEST_CORE_BINARY=.build/nodedance python3 scripts/test-docker.py`; see [Docker](docker.md) for prerequisites and scope. Local image tests do not establish that a newly published release downloads successfully. Transfer tests additionally require their documented Landlock support; [file transfers](file-transfers.md) distinguishes local/virtual NAT tests from real-provider verification.

## Capacity / 容量

The maintained soak uses protocol-simulated Agents, a real isolated Core and bounded assertions:

```sh
NODEDANCE_TEST_SOAK_NODES=100 NODEDANCE_TEST_SOAK_SECONDS=600 \
  go test ./internal/core/server -run '^TestCoreCapacitySoak$' -count=1 -v
```

Record the actual environment, run duration, workload, latency and resource measurements when running it. Results include the load generator's process resources. A simulated-node run does not establish a physical-host limit, WAN behavior, 24-hour stability or production SLA.

## Delivery / 交付

Follow [AGENTS.md](../AGENTS.md): commit and push verified changes on `main`. Implementation delivery uses the standard Makefile installation/restart flow, followed by service, health, running-binary and local/public asset checks. Documentation-only changes do not rebuild or deploy. Never modify production data or the service unit as part of a test.
