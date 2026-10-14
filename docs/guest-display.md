# Guest display / 游客展示

Guest mode is an optional read-only presentation of explicitly published Core data. It is disabled by default: node status, metric fields, nodes, services and history are all unselected. On the login page, visitors must choose **Enter guest mode** before the browser requests display data. The public entry endpoint only reports whether guest mode is enabled. The explicit `#/guest` URL enters the same public view and restores it on refresh; browser Back/Forward between guest and management rechecks the administrator session. Direct callers can access published data without pressing the button; Core's publication policy, not a browser flag, enforces the boundary.

## Configure / 配置

The login entry and publication navigation use a dedicated display-window icon, distinct from node inventory. In Web, open **Guest display**, select the display fields and specific nodes/services, and enter separate public names. Private names, notes and URLs are never copied into the public response. Save the settings, then preview the saved guest view. Enable guest mode when the selection is ready. Both preview and anonymous output use the same Core projection, including when the preview is requested with an administrator session.

CLI, TUI and Web discover the same five operations from `/api/v1/operations`:

| Operation | Access | Purpose |
| --- | --- | --- |
| `guest.settings` | Administrator | Settings, allowed metrics and history choices |
| `guest.configure` | Administrator | Save explicit publication policy |
| `guest.preview` | Administrator | Preview the saved public projection, also while disabled |
| `guest.entry` | Anonymous read | Enabled flag only |
| `guest.display` | Anonymous read | Selected display data only; disabled returns 404 |

Use `nodedance describe guest.configure --server CORE_ORIGIN` for the current schema. Read `guest.settings` first, then submit its `settings` object with the current revision. Saving increments the revision; a concurrent stale save returns 409. Publication changes are audited in the same transaction as the settings. Disabled drafts may retain unavailable selections so an administrator can always switch the display off; enabling requires all selected resources and their nodes to remain available. TUI's discovered operation browser provides the same inputs and execution as CLI; Web adds checkboxes, public-name fields and a graphical preview.

## Available display data / 可展示内容

- Explicitly selected node online/offline status.
- CPU usage, memory usage, root filesystem usage, network download/upload rate and uptime; each is independently opt-in.
- Selected historical metrics over 1, 6 or 24 hours. One-hour history uses minute aggregates; 6/24-hour history uses hour aggregates. Uptime has no history series. Disk history is mapped from the root mount into a public metric key without exposing mount metadata.

History buckets use the exact half-open interval `[from, to)`, including fractional seconds. The shared history query rounds each boundary up to a bucket boundary without first dropping subsecond precision; guest and administrator reads use the same rule.
- Selected service probe status and observed response latency, using public aliases. Disabled, stale or offline-node probes are unknown; visitors do not initiate probes.

Limits: 20 selected nodes and 20 selected probes. The Core owns metric choices and history bounds; new resources and fields do not become public automatically. Revoked/pending nodes and deleted probes are omitted when a snapshot is built. Unknown/stale/error metric values are omitted rather than reported as zero or current values.

## Boundary and load / 权限与负载

Public responses use dedicated data types containing aliases, opaque HMAC-derived display IDs and selected values. They omit internal node/Agent/probe IDs, hostnames, IPs, credentials, error reasons, private preferences, service targets, container details, logs, files and command output. All management operations retain administrator session and CSRF enforcement. The guest page exposes display filtering, chart selection and refresh only. It never requests private node APIs or sends remote operations to Agents.

Visitors cannot supply node IDs, metric keys or arbitrary time ranges to the public endpoint. Browser refresh is every 30 seconds; Core caches assembled display data for 15 seconds. Every request rereads the saved policy and checks selected resource availability with at most two bounded SQL queries. A changed revision, deleted probe or revoked node invalidates the cached snapshot immediately; services belonging to revoked nodes are omitted as well. Disabling guest mode immediately blocks subsequent requests, including a warm cache. HTTP responses use `Cache-Control: no-store`; data already received by a visitor cannot be recalled. Up to four public requests execute concurrently, independently of the Core's overall request bound. Public snapshot generation has a five-second context deadline, responses have a fifteen-second write deadline, and network writes do not hold the shared snapshot lock. Selected history keys are filtered in SQL using the existing history aggregation code.

## Verification / 验证

`make check` includes policy validation, persistence, revision/audit behavior, private-field exclusion, selected history filtering, anonymous management denial, resource opt-in, stale metric handling and immediate disable tests. `go test -race ./internal/core/server ./internal/core/guest ./internal/control ./internal/core/cli` checks shared execution and concurrency, including identical CLI/TUI policy submission with an 80-character Unicode public name. `pnpm --dir web audit:guest` starts an isolated real Core, sends metrics through a simulated protocol-only Agent and checks the anonymous browser, administrator-cookie projection equality, session-checked guest exit, browser-mode spoof denial, administrator settings/preview and direct API denial and twenty light/dark desktop/mobile layouts, including node cards and detail headings without visible internal UUIDs. It does not install an Agent or publish production data. `pnpm --dir web audit:guest-security` adds the real-browser adversarial checks documented in [guest-security.md](guest-security.md).

游客模式默认关闭，所有展示项默认不选。管理员明确选择节点、字段和服务，并单独填写公开名称；登录页点击“进入游客模式”才读取展示数据。游客仅能查看 Core 筛选后的状态、指标和历史图表，不能执行控制操作。CLI／TUI／Web 共用配置契约与校验，Web 增加图形选择和预览。匿名接口不接受任意节点、指标或时间范围，公开配置更改及关闭会立即影响后续请求。

Management components stop follow-up reads when they unmount, including grant creation/revocation completing after guest entry. 管理组件退出后停止后续读取；创建／撤销入网授权的迟到响应也不能在游客模式中触发私有列表请求。

The full file manager is administrator-only, including file search, media preview and all mutations. Its disposal aborts reads and prevents follow-up private requests after guest entry. 完整文件管理器仅供管理员使用，游客不能获取搜索、预览或修改能力；进入游客后退出文件管理组件并停止后续请求。

Expanded text editing and archive formats remain administrator-only through the same discovered Core file operation. Guest entry does not expose file data or controls.

History charts retain their canvas host during loading and render after asynchronous data arrives. Browser regressions verify actual canvas initialization on the first history load; charts also observe the shared root theme in guest and management views.

2026-10-13 收尾审查以新 Web 资产复验默认关闭、管理员 Cookie 下相同公开投影、返回管理时有效会话及策略/资源撤回的浏览器行为。测试从实际 Core operation contract 发现覆盖；见 [69 文件报告](code-review-69-files.md)。

RSS／Atom 订阅中心为管理员私有能力，不进入游客字段、订阅源或条目投影。携带管理员 cookie 的公开请求仍只得到原有游客数据，详见 [订阅权限](rss.md)。

Agent enrollment responses contain token-bearing native and Docker installation commands. These responses and reusable enrollment grants remain administrator-only; guest projections exclude tokens and installation commands even when administrator cookies are present. The isolated browser attack suite verifies anonymous enrollment denial and command/token exclusion from public display.

Deleting a node withdraws it from public display immediately, including warm cached displays. Deleted probe and node selections are filtered on each public read; administrator-only deletion and enrollment renewal controls do not appear on guest screens.

Node billing metadata (renewal price, currency, billing cycle, monthly quota, purchase date and VPS expiry), including values supplied during enrollment, remains administrator-only. Guest security regressions create an enrollment with distinct private billing markers, verify persistence through the administrator asset operation, and verify exclusion from public responses with guest and administrator cookies. Anonymous enrollment and asset operations remain covered through the discovered Core contract.

SSH discovery is administrator-only through `agents.diagnostics`. Host addresses, listener ports, account candidates and generated connection commands never enter the guest projection. Anonymous diagnostic requests remain denied through the discovered private operation matrix. Discovery observes service configuration and login metadata without collecting passwords, private keys or Agent credentials.

Administrator VPS sections use the contextual sidebar; returning home restores global navigation. The guest browser regression retains detail history, reload, section selection and administrator session checks through this sidebar. Guest display does not expose management navigation.

Compose project display names are administrator-only preferences. The isolated browser suite stores a private project-name canary and verifies that anonymous/public output does not contain it; Compose deletion remains behind the shared authenticated operation and CSRF boundary.
