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

Node billing metadata (renewal price, currency, billing cycle, monthly quota, purchase date and VPS expiry), including values supplied during enrollment, remains administrator-only. Currency values are published in the Core operation contract as an enum shared by Web, CLI and TUI, and Core rejects asset bodies with values outside that enum; the browser attack suite keeps a forged-currency rejection case. Guest security regressions create an enrollment with distinct private billing markers, verify persistence through the administrator asset operation, and verify exclusion from public responses with guest and administrator cookies. Anonymous enrollment and asset operations remain covered through the discovered Core contract.

SSH discovery is administrator-only through `agents.diagnostics`. Host addresses, listener ports, account candidates and generated connection commands never enter the guest projection. Anonymous diagnostic requests remain denied through the discovered private operation matrix. Discovery observes service configuration and login metadata without collecting passwords, private keys or Agent credentials.

Administrator VPS sections use the contextual sidebar; returning home restores global navigation. The guest browser regression retains detail history, reload, section selection and administrator session checks through this sidebar. Guest display does not expose management navigation.

Compose project display names are administrator-only preferences. The isolated browser suite stores a private project-name canary and verifies that anonymous/public output does not contain it; Compose deletion remains behind the shared authenticated operation and CSRF boundary.

The browser guest regression opens a shared dropdown before guest entry and verifies immediate removal of its portaled UIArc dropdown menu. Custom controls keep the existing Core operation, opt-in policy and session boundaries.

The shared light/dark theme uses semantic surface, text, control and state roles documented in `DESIGN.md`. Visual flattening does not change operation discovery, public projection, authorization or task semantics. Browser audits use fresh assets and isolated Core/Agent resources; the theme regression checks real persisted/system switching and actual rendered contrast.

共享深浅主题使用 `DESIGN.md` 中的表面、文字、控件和状态语义。视觉扁平化不改变操作发现、公开投影、权限或任务行为。浏览器回归使用新构建资源与隔离 Core／Agent，主题回归检查实际保存／跟随系统切换及渲染对比度。

## Overview context privacy

The Core runtime host information (`dashboard.core-info`) and shared overview memo (`dashboard.memo`, `dashboard.save-memo`) are administrator-only operations. They are not projected into public node cards, including requests with administrator cookies. Forged `memo`/`core` query flags are rejected with HTTP 400. The security matrix discovers these operations from Core and checks anonymous/forged-identity denial. A distinct private memo marker is also checked against guest and administrator-cookie public responses.

The sidebar account card is administrator-only. It opens upward and reuses the existing personal settings and sign-out actions; guest views never render account information or account controls. Browser regression checks verify Escape/focus recovery and real session invalidation after sign-out. 左下角账号卡片仅在管理界面显示，向上展开并复用个人设置与退出登录；游客页不显示账号信息或入口。浏览器回归验证 Esc 焦点恢复以及退出后的真实会话失效。

Multi-account operations (`accounts.*`, `auth.switch`, `profile.*`) remain private. Anonymous callers cannot enumerate accounts or access private avatars. Read-only accounts cannot change guest policy or shared branding. The browser suite checks account-menu navigation and real logout, and account lifecycle tests verify isolated sessions, role enforcement, password reset/disable withdrawal and the final-administrator guard. 多账号信息和私有头像不进入游客投影，只读账号不能修改公开策略或共享外观。

登录恢复：有效会话下再次登录使用该会话的 CSRF 校验，成功验证密码后轮换并撤销旧会话；失败保留原会话。Web 对提交前被拒绝的登录安全令牌仅刷新并重试一次，保留输入；来源拒绝、密码错误不重试。游客权限保持不变。隔离浏览器回归覆盖旧登录页、自动恢复、重试上限、旧会话撤销和伪造来源／令牌拒绝。

Login recovery validates the current session CSRF token when present, verifies the password and rotates the replaced session only on success. Web retries an explicitly rejected login security token once, retaining input; origin and password failures are not retried. Anonymous boundaries remain unchanged and are checked by the isolated guest security suite.

Diagnostic readings, per-core/process entities, probe phase timings and certificate dates, incident recordings, capacity predictions and cross-node correlations are private management data. They are never added to the guest projection or guest metric registry by enabling diagnostic collection. / 诊断指标、每核心及进程明细、探测阶段耗时与证书日期、故障录像、容量预测和跨节点关联均为私有管理数据，启用采集不会将其加入游客投影或可公开指标。

Five-second network/disk history stays private behind nodes.history; the anonymous browser suite explicitly verifies denial for resolution=5s. Guest history continues to use only its configured public minute/hour projection. / 网络与磁盘五秒历史仅通过私有 nodes.history 提供；匿名浏览器回归显式验证 resolution=5s 仍被拒绝。游客历史保持配置允许的公开分钟／小时投影。

文件管理初始化未完成或失败时进入游客模式，也会先中止私有读取，再卸载界面；目录服务不可用不应阻止游客页出现。
Entering guest mode during pending or failed file-manager initialization aborts private reads before view teardown; directory unavailability must not prevent the guest page from rendering.

Administrator Docker details use the complete private inventory in the shared dialog, without a dashboard preview-count setting. Container inventory and display preferences remain excluded from the public projection. 管理员 Docker 详情复用共享弹窗展示完整私有清单，不再提供首页预览数量设置；容器清单与展示偏好仍不属于游客公开数据。

Compose configuration comparisons are administrator-only browser-local views of the already authorized file and draft; they do not add configuration content to guest data. 配置差异仅在管理态比较已授权读取的原文与草稿，不向游客数据加入配置内容。

本机 Core／Agent 更新由 root 独立执行，无需登录；公网更新 API 仍必须通过管理员认证和 CSRF。CLI/TUI 的 `persistent` 登录策略只在凭据验证成功后记录，不授予额外权限；伪造 User-Agent、会话策略或身份请求头不能访问管理 API。普通 Web 登录继续闲置过期，退出、撤销、账号停用和密码变更同样撤销永久会话。游客安全回归覆盖伪造策略以及实际永久会话的撤销。

页面语言自动跟随浏览器，不再使用顶部语言选择器。游客切换仍验证真实操作下拉浮层在进入公开页时立即移除；语言偏好不改变游客数据边界。

域名库存、证书、签发账号、DNS 提供商、密钥和任务均不属于游客投影。域名证书页面仅在管理界面出现；匿名深链转到登录，不读取这些私有资源。

游客展示设置采用 shadcn CLI 安装的本地卡片和表单组件。资源勾选与公开名称仍由同一 Core 策略保存；预览读取已保存策略，未保存草稿不会自动公开。控件重构没有改变匿名公开页面的数据范围或自动选择新资源的行为。

Node management country metadata and selected brand/OS icons remain private;
this presentation change does not add them to guest display policy or projection.
See [node display](node-display.md).

手动国家 `countryCode` 与自动定位结果同属管理员节点展示偏好，不向游客投影；保存通过既有 `nodes.save-preference` 私有操作执行。

账号菜单已接入安装的 UIArc User Menu；切换账号在 UIArc Dialog 中继续调用 `auth.switch` 验证目标密码。游客安全回归保留私有菜单立即清除、账号信息不可见、Esc 焦点恢复和真实退出后的会话失效断言。

“总览全球”是私有管理视图，复用同一 Core 节点数据，不加入游客公开投影。浏览器回归验证未登录深链不读取节点，以及进入游客时清除地球、卡片和私有请求。

城市定位同属私有节点管理数据：`location`、城市、州／省、经纬度与手动覆盖不进入游客投影。浏览器安全回归保存私有城市记录并检查匿名和管理员 Cookie 下公开数据均不泄露；城市目录新操作从 Core 契约发现后纳入匿名拒绝矩阵。
