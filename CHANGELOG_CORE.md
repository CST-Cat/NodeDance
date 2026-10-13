# Core changelog / Core 更新日志

## [Unreleased]

### English

### 简体中文

## [0.1.19]

### English

- Redesign VPS Docker management around a selectable, paginated container table and one shared top toolbar. Remove drag sorting and repeated row action buttons; provide batch lifecycle actions and deletion with independent task results, stable retry identities and exact per-target confirmations.
- Keep selected-container details, terminal, logs/monitoring, display preferences, audit and rebuild accessible from the toolbar. Add isolated browser regressions for selection, filtering, pagination, changed-state confirmation, Compose restrictions, stale/offline guards, focus and eight bilingual light/dark/mobile layouts.

### 简体中文

- 将 VPS Docker 管理改为支持多选、分页的容器表格与统一顶部工具栏，移除拖拽排序和逐行重复操作按钮；批量生命周期操作与删除保留独立任务结果、稳定重试身份及逐目标完整 ID 确认。
- 从工具栏访问所选容器详情、终端、日志／监控、显示设置、审计与重建；新增隔离浏览器回归，覆盖选择、筛选、分页、确认期间状态变化、Compose 限制、陈旧／离线数据保护、焦点及八种双语浅色／深色／移动端布局。

## [0.1.18]

### English

- Add Core-hosted RSS/Atom feed subscriptions with adjustable polling from one minute to one day, a 30-minute default, shared CLI/TUI operations and a dedicated Web screen.
- Improve alert delivery, backups, metrics ownership, file and container operations, update recovery, Tailscale workflows and request handling; remove obsolete duplicate paths and add targeted regressions.
- Refine node details and service-probe navigation, improve release/build workflows and Web accessibility, and strengthen guest, file-manager, interface and feed browser audits.

### 简体中文

- 在 Core 内置 RSS／Atom 订阅，支持 1 分钟至 1 天多档轮询、默认 30 分钟，并通过共享 CLI／TUI 操作及独立 Web 页面管理。
- 改进告警投递、备份、指标归属、文件与容器操作、更新恢复、Tailscale 流程及请求处理；移除过时重复路径并补充针对性回归。
- 优化节点详情与服务探测导航、发布／构建流程及 Web 可访问性，并加强游客、文件管理、接口与订阅浏览器审计。

## [0.1.17]

### English

- Raise versioned UTF-8 file editing to 8 MiB with operation-derived terminal input/response limits and bounded text transport; support ZIP/TAR/TAR.GZ creation plus TAR.BZ2 extraction, and expose formats in VueFinder menus/dialogs. Replace recursive regex tokenization for large escaped JSON and retain full-size browser/CLI regressions.
- Audit VueFinder integration: use discovered upload limits, align size filters and ZIP naming, keep upload queues progressing after item failures, preserve original edit versions across refresh, and support modal focus/full-screen keyboard controls. Expand isolated browser and file-envelope regressions.
- Restore solid, theme-aware file preview/editor dialogs outside the application container; improve mobile dialog spacing and add real-browser opacity/contrast regression checks.

### 简体中文

- 将版本化 UTF-8 文件编辑提高到 8 MiB，终端输入／响应限制跟随契约，文本传输保持有界；支持创建 ZIP／TAR／TAR.GZ 及解压 TAR.BZ2，并同步 VueFinder 菜单／弹窗。替换大正文会栈溢出的 JSON 正则扫描，保留满上限浏览器／CLI 回归。
- 完整核查 VueFinder 接入：上传上限跟随发现契约，统一大小过滤与 ZIP 命名，单项上传失败后继续队列，刷新目录仍保留编辑原版本，补齐弹窗焦点与全屏键盘操作；扩展隔离浏览器及文件控制消息回归。
- 修复文件预览／编辑弹窗脱离应用容器后丢失主题、背景透明的问题；改善移动端弹窗留白，并增加真实浏览器不透明背景／文字对比度回归检查。

## [0.1.16]

### English

- Integrate complete VueFinder 4.7.6 with lazy editor/assets, shared file tasks, paginated directory/search, no-overwrite copy/move, bounded ZIP archive/extraction, versioned 128 KiB text edits and sandboxed passive media previews. Preserve file-task recovery, guest isolation and browser directory history; add real Core/root Agent browser regression and bundled license notices.
- Restore command-history cleanup through the audited HTTP entry, stop delayed enrollment-grant follow-up reads after guest entry, and preserve specialized batch command/probe retry identities across reload using the shared operation intent store.
- Support browser Back/Forward, bookmarks and reload with browser-local view URLs for workspace pages, node details/sections, alert tabs and host management tabs/node selection. Revalidate administrator sessions on management entry, preserve the public guest projection on URL restoration, and prevent stale navigation responses or history restoration from resubmitting tasks.
- Give guest display its own navigation/entry icon, simplify node cards and detail headings by hiding internal UUIDs, and recover alert metric catalog loading through Refresh. Reject empty JSON responses and missing required payloads instead of exposing JavaScript property errors; verify empty/204 responses and recovery in a real browser.

### 简体中文

- 完整接入 VueFinder 4.7.6 与按需加载编辑器；补齐目录分页／搜索、禁止覆盖的复制移动、有界 ZIP 压缩解压、128 KiB 版本化文本编辑和受控媒体预览。沿用统一文件任务与恢复机制，保留游客隔离和浏览器目录历史，增加真实 Core／root Agent 浏览器回归及第三方许可证。
- 修复命令历史清理在 HTTP 审计入口报错；阻止创建／撤销入网授权的迟到响应在游客模式中触发私有读取；批量命令／探测共用执行意图存储，刷新后重试仍复用原执行身份。
- 为工作区页面、节点详情／标签、告警标签和主机管理标签／节点选择增加浏览器本地视图 URL，支持前进／后退、书签和刷新恢复；进入管理页面重新验证管理员会话，游客 URL 恢复仍仅加载公开数据，阻止迟到导航响应覆盖新页面及历史恢复重复提交任务。
- 为游客展示设计独立导航／入口图标，移除节点卡片与详情标题中的内部 UUID；告警刷新同步重试指标目录，拒绝空 JSON 响应和缺少必需字段的响应，避免展示 JavaScript 属性错误；用真实浏览器验证空响应／204 及恢复。

## [0.1.15]

### English

- Include embedded release manifest fields in the discovered update response contract so CLI/TUI accept real update checks; verify the shared update workflow with the complete release response.
- Withdraw deleted probes and revoked nodes from warm guest caches immediately, allow disabling with unavailable selections, and reject malformed public queries. Add 438 real-browser adversarial checks across anonymous management, forged sessions, WebSockets, CSRF, stored HTML injection and bounded refresh storms.
- Add opt-in guest entry on the login page, explicitly selected node/metric/history/service display, server-filtered public data, and shared CLI/TUI/Web publication settings with administrator preview. Keep all controls private and verify default denial, field isolation, immediate disable and light/dark/mobile behavior.

### 简体中文

- 在动态更新响应契约中包含内嵌 release manifest 字段，修复 CLI／TUI 拒绝实际更新检查的问题；用完整 release 响应验证共用更新流程。
- 即时从游客缓存移除已删除探测项和已撤销节点，允许保留失效选择直接关闭游客展示，并拒绝畸形公开查询；增加 438 项真实浏览器攻击检查，覆盖匿名管理、伪造会话、WebSocket、CSRF、存储型 HTML 注入及有界并发刷新。
- 增加登录页游客入口、明确选择的节点／指标／历史／服务展示及 Core 筛选后的公开数据；CLI／TUI／Web 共用公开配置，支持管理员预览；所有控制操作保持私有，验证默认不公开、字段隔离、即时关闭及浅色／深色／移动端布局。

## [0.1.14]

### English

- Bound JSON/transfer body reads and concurrent requests; preserve precise integer inputs, stream uploads, response schemas and byte-bound headers across the discovered interfaces. Recover batch observation and browser request keys without resubmission; retain uncertain batches and verify root/Docker fault recovery plus a 100-node, 10-minute simulated soak.

### 简体中文

- 限制请求正文读取时间及并发数，统一整数精度、流式上传、响应结构和请求头字节限制；支持批次观察及浏览器请求键恢复且不重复提交，保留不确定批次；验证 root／Docker 故障恢复及 100 模拟节点 10 分钟运行。

## [0.1.13]

### English

- Run ordinary Core/Agent updates directly without a second confirmation in CLI, TUI and Web. Reserve interactive confirmation and the CLI `--yes` option for rollback.
- Expand the discovered Core operation contract with shared request schemas, upload fields and limits, allowed headers, response shapes and completion metadata. Reuse request types and structural validation across Core and terminal clients.
- Give CLI and TUI shared operation dispatch, finite execution, local maintenance/update workflows, interactive and JSONL terminals, and task observation without repeating submissions. Generate and verify coverage for 113 remote operations and 15 local/workflow operations.
- Fix Web multipart uploads, empty optional fields, metric catalog parsing, file version headers, uncertain-retry idempotency keys, credential masking and exact large-integer request values. Align enrollment defaults and support contract-driven completion observation.
- Synchronize the Web logo with the favicon and bilingual README headings. Add real Core/browser interface checks and terminal parity tests; verify light, dark and mobile layouts.

### 简体中文

- CLI、TUI、Web 的普通 Core／Agent 更新直接执行，不再要求二次确认；交互确认和 CLI `--yes` 仅用于回滚。
- 扩展 Core 动态操作契约，统一请求结构、上传字段与限制、允许的请求头、响应结构和任务完成元数据；Core 与终端客户端共用请求类型及结构校验。
- CLI、TUI 共用操作分发、有限请求执行、本地维护与更新流程、交互式及 JSONL 终端，并支持不重复提交的任务观察；生成并核对 113 项远程操作和 15 项本地维护／工作流的能力对照。
- 修复 Web multipart 上传、可选空字段、指标目录解析、文件版本头、不确定重试的幂等键、敏感凭据显示及大整数请求精度；统一注册默认值，按契约观察任务完成。
- 将 Web logo 同步到 favicon 和中英文 README 标题；新增真实 Core／浏览器接口回归及终端对等测试，验证浅色、深色和移动端布局。

## [0.1.12]

### English

- Read host Tailscale peers through the authenticated deployment supervisor, fixing network discovery for existing Core containers without adding mounts or changing Tailscale. Web, CLI and TUI share the same discovery operation; built-in Core updates refresh the supervisor and preserve deployment configuration.

### 简体中文

- 通过认证的宿主机控制服务只读查询 Tailscale 节点，修复现有 Core 容器的网络发现，无需新增挂载或修改 Tailscale。Web、CLI、TUI 共用发现操作；内置 Core 更新同步升级宿主机服务并保留部署配置。

## [0.1.11]

### English

- Resolve the system color scheme before first paint, remember theme/sidebar preferences, and add sidebar collapse and resizing.
- Collapse Docker details by default in VPS cards; keep terminal shortcuts in overview modals and open node details from VPS names.
- Detect existing Tailscale binaries outside the service PATH or through the existing daemon socket; distinguish empty Tailnets and actionable discovery errors.
- Reattach Agent-managed tmux sessions through the shared Core contract in Web, CLI and TUI; keep authentication, idle expiry and explicit end controls, and remove custom output replay.

### 简体中文

- 首屏绘制前识别系统深浅色，记忆主题与侧栏偏好，支持侧栏折叠及拖动宽度。
- VPS 卡片默认折叠 Docker 信息；总览中的终端直接弹窗，点击 VPS 名称进入详情。
- 从服务 PATH 之外的标准路径或现有 daemon socket 检测 Tailscale，区分空 Tailnet 与可操作的发现错误。
- Web、CLI、TUI 通过统一 Core 契约重新附着 Agent 管理的 tmux 会话；保留权限、空闲清理与主动结束控制，删除自写输出补发。

## [0.1.10]

### English

- Expose extended Agent metrics, settings, diagnostics and durable commands through the shared operation contract, with synchronized Web, CLI and TUI access and bilingual Host management screens.
- Add batch scripts and probes, bounded shared enrollment grants, asset expiry and traffic quota reminders, GPU/host metric history and alerts, and probe latency/failure/loss/jitter statistics.
- Add the authenticated HTTP Agent channel with sequencing, retry deduplication and acknowledged polling; expand the current monitoring schema while preserving existing data. Upgrade Agents to agent-v0.1.8 to enable the new metrics and host controls.

### 简体中文

- 通过统一操作契约提供 Agent 扩展指标、配置、诊断及持久化命令，同步 Web、CLI、TUI，并增加双语主机管理页面。
- 新增批量脚本与探测、限时限次数共享注册密钥、资产到期与流量配额提醒、GPU/主机指标历史与告警，以及探测延迟、失败率、丢包率和抖动统计。
- 新增带帧序号、重试去重及轮询确认的认证 HTTP Agent 通道；扩展当前监控数据结构并保留已有数据。升级 Agent 至 agent-v0.1.8 后可使用新指标与主机控制。


## [0.1.9]

### English

- Audit all Web screens against DESIGN.md: unify warm interaction states, typography, cards and controls; theme confirmations, bundle trimmed fonts, load charts/terminal on demand and fix the initial update check with connected nodes.

- Add a shared Core/Agent update API, Web Update center, nodedance update command and automatically discovered TUI operations. Verify component releases and checksums, preserve deployments and private state, persist update jobs, and recover or roll back failed updates through an independent root supervisor.

### 简体中文

- 按 DESIGN.md 全面审查 Web：统一暖色交互状态、字体、卡片和控件；统一确认弹窗，打包裁剪字体，按需加载图表/终端，并修复存在已连接节点时的首次更新检查。

- 新增共享 Core/Agent 更新 API、Web 更新中心、nodedance update 命令及 TUI 自动发现操作；校验组件版本与下载、保留部署配置和私有状态，通过独立 root 控制服务持久化进度并恢复或回滚失败更新。

## [0.1.8]

### English

- Style the shared Operations interface with the Web console's light/dark theme, accessible form controls, responsive layout, and bounded result panels.

### 简体中文

- 为共享操作中心补齐 Web 控制台的明暗主题、可访问表单控件、响应式布局和有界结果面板。

## [0.1.7]

### English

- Publish one Core operation contract and discover it dynamically in Web, CLI, and TUI. Generate Web operation forms, reuse canonical routes for all HTTP and WebSocket transports, and verify server-only operations are available without client changes.

- Share the Agent deployment-method request and schema across Web, CLI, and TUI. Select native/systemd or Docker in CLI with `--deployment-method` and in dedicated TUI enrollment prompts; return the selected command and both installation alternatives.
- Validate enrollment methods in Core before issuing a token, document the shared request, and test CLI request forwarding and TUI selection.

### 简体中文

- Core 发布统一操作契约，Web、CLI、TUI 动态读取。Web 自动生成操作表单，所有 HTTP 与 WebSocket 传输复用契约路由，并验证新增服务端操作无需客户端改动即可使用。
- Web、CLI、TUI 共用 Agent 部署方式请求与操作 schema。CLI 使用 `--deployment-method`，TUI 使用专用注册表单，均可选择直接/systemd 安装或 Docker，并返回选中命令及两种安装方案。
- Core 在签发令牌前验证部署方式，更新共享请求文档并测试 CLI 请求传递与 TUI 选择。

## [0.1.6]

### English

- Rebuild the Web console with the Claude design reference: warm surfaces, coral accents, responsive navigation, and coordinated light/dark themes.
- Generate a strong random administrator username and password on first unattended deployment; interactive startup supports custom values or Enter to generate either. Require username and password at login and remove the private initial-credentials file after successful login.
- Include initial administrator credentials in protected backups and update Web, CLI, and TUI authentication flows.
- Offer both direct/systemd and Docker Agent enrollment commands in the Web console, with hidden token input and published Agent image tag pinning.
- Add a non-root Core Docker image, persistent Compose deployment, native Docker integration checks, and version-pinned amd64/arm64 GHCR publication.
- Allow HTTP listeners behind HTTPS reverse proxies while retaining browser origin, CSRF, and secure-session protections.
- Add Chinese-first and English README pages, installation guidance, and bilingual Docker deployment documentation.

### 简体中文

- 参考 Claude 设计重构 Web：暖色背景、珊瑚色强调、响应式导航与统一的明暗主题。
- 首次无终端部署自动生成高强度随机管理员账号名与密码；交互启动支持自定义或回车生成。登录要求账号名与密码，首次成功登录后删除私有初始凭证文件。
- 受保护的备份包含初始管理员凭证，并同步更新 Web、CLI、TUI 认证流程。
- Web 注册页面同时提供直接/systemd 安装和 Docker 部署命令，隐藏输入注册令牌并固定已发布的 Agent 镜像版本。
- 增加非 root Core Docker 镜像、持久化 Compose 部署、原生 Docker 集成检查与固定版本的 amd64/arm64 GHCR 镜像发布。
- 支持在 HTTPS 反向代理后运行 HTTP 监听，保留浏览器 Origin、CSRF 与安全会话保护。
- 增加简体中文默认 README、English 入口、安装指南及双语 Docker 部署文档。

## [0.1.5]

### English

- Make the Core installer resolve the latest stable Core release by default while keeping optional tag pinning.
- Document the latest-release install command; Agent enrollment already resolves the latest stable Agent release.

### 简体中文

- Core 安装脚本默认解析最新正式版，同时保留指定 tag 安装的选项。
- 文档改为默认安装最新 Core；Agent 注册流程继续自动解析最新正式版 Agent。

## [0.1.4]

### English

- Publish gzip-compressed Linux amd64/arm64 Core archives and a version-pinned installer that verifies SHA256 before installing.
- Keep the raw Core binaries available and include both raw binaries and archives in `SHA256SUMS`.

### 简体中文

- 发布 Linux amd64/arm64 Core gzip 压缩包和固定版本的安装脚本；安装前校验 SHA256。
- 保留 Core 裸二进制，并将裸二进制和压缩包都纳入 `SHA256SUMS`。

## [0.1.3]

### English

- Build and test amd64 and arm64 releases concurrently on native runners; publish only after both pass.

- Apply connection-local SQLite limits, immutable indexed operation schemas, and byte-based protocol decoding.

- Add a shared operation catalog for CLI and TUI, interactive terminal access, and explicit Core/Agent capability negotiation.
- Support English and Simplified Chinese in Web, CLI and TUI; preserve operation identifiers and JSON data contracts.
- Release Core with `core-vMAJOR.MINOR.PATCH` independently of Agent. Begin each release description with a pinned link to the latest published stable Agent release.

### 简体中文

- 使用原生 runner 并行构建、测试 amd64 和 arm64，两个架构全部通过后才发布。

- 应用 SQLite 连接限额、不可变操作目录索引缓存，以及直接字节协议解码。

- CLI 和 TUI 共用操作目录，支持交互式终端及明确的 Core/Agent 能力协商。
- Web、CLI 和 TUI 支持简体中文和 English，操作标识符及 JSON 数据契约保持稳定。
- Core 使用 `core-vMAJOR.MINOR.PATCH` 独立发布；每次发布说明开头固定链接到最新已发布的正式 Agent release。
