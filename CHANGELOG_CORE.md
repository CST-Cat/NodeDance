# Core changelog / Core 更新日志

## [Unreleased]

### English

### 简体中文

## [0.1.54]

### English

- Pair the Core release with Agent 0.1.31, including the bounded low-memory native Agent update flow.

### 简体中文

- Core 发布配套 Agent 0.1.31，包含面向低内存原生 Agent 的有界更新流程。

## [0.1.53]

### English

- Pair the Core release with Agent 0.1.29, which streams binary backups and installation for low-memory update targets.
### 简体中文

- Core 发布配套 Agent 0.1.29；Agent 以流式备份和安装二进制，支持低内存机器更新。
## [0.1.52]

### English

- Refactor personal settings, Agent enrollment, update and subscription screens around shared local shadcn/UIArc components, compact aligned rows, native control styles and monochrome interaction states.
- Add configurable monitoring-history retention through shared Core operations, paginated sample details and complete CSV/JSON exports of the loaded history.
- Remove redundant client and internal validation, repeated profile reads and coupled page loading; cancel stale requests and share in-flight global-overview metric reads.
- Fix idle task-dispatch polling, repeated route parsing and unnecessary schema/snapshot copies; validate and annotate incoming telemetry in one admission path while preserving identity and sequence protections.
- Fix IPv6 probe target editing, uint64 traffic presentation and ownership of newly reported interface and mount values; preserve precise inputs, missing-data semantics and atomic retention cleanup.
- Simplify guest resource resolution without expanding public permissions, and retain browser security, file-management and focused data-integrity regressions.

### 简体中文

- 个人设置、Agent 接入、更新与订阅页面统一复用本地 shadcn／UIArc 组件，采用紧凑对齐排版、原版控件样式及黑白交互高亮。
- 通过共享 Core 操作新增可配置监控历史保留时长、分页采样明细，以及当前查询全部数据的 CSV／JSON 导出。
- 移除重复的客户端与内部校验、重复资料读取及页面加载耦合；取消过期请求，并在全球总览共用进行中的指标读取。
- 修复空闲任务派发循环、重复路由解析及无用 schema／快照拷贝；监控采样在统一接收流程中校验并添加 Core 派生指标，保留身份与序号保护。
- 修复 IPv6 探针目标编辑、uint64 流量展示及新增网卡和挂载点数据的引用归属，保留精确输入、缺失数据语义和原子历史清理。
- 精简游客资源读取，不扩大公开权限，保留浏览器安全、文件管理及聚焦数据完整性的回归验证。

## [0.1.51]

### English

- Add domain and certificate management with ACME issuance, renewal, revocation, private export, DNS providers, durable jobs and notifications through the shared Web/CLI/TUI operation contract.
- Add schema-driven JSONL batch operations with shared CLI/TUI validation, execution, observation and private downloads.
- Add global fleet overview with COBE 2.0.1, city markers, visible VPS names, rotating node cards and prefetched per-node metrics that stay populated during handoff.
- Add automatic public-IP city positioning and manual city/coordinate preferences, a bundled searchable city directory, custom country flags and service logos; keep private location data outside guest projections.
- Integrate local shadcn/UIArc components, shared dropdown and account menus, and Tailwind CSS v4 concentric radii while preserving existing theme tokens; refresh host, update, subscription, operation and guest settings screens.
- Replace scroll-triggered mobile overview expansion with responsive defaults and reusable search/view dialogs; align icons and labels and remove square borders from circular gauges.
- Extend isolated browser security and interface regressions for the new operations, private data, theme and mobile layouts.

### 简体中文

- 新增域名与证书管理，通过 Web、CLI、TUI 共享操作契约提供 ACME 签发、续期、撤销、私钥导出、DNS 提供商、持久任务与通知。
- 新增基于发现 schema 的 JSONL 批量操作，CLI／TUI 共用校验、执行、观察与私有下载流程。
- 新增 COBE 2.0.1 全球节点总览、城市标记、可见 VPS 名称与轮播卡片；按节点预加载并缓存指标，切换时保留内容。
- 新增公网 IP 自动城市定位、手动城市／坐标偏好、本地可搜索城市目录、自定义国家旗帜和服务图标；游客投影不包含私有位置数据。
- 接入本地 shadcn／UIArc 组件、统一下拉与账号菜单及 Tailwind CSS v4 同心圆角，沿用现有主题 Token；更新主机、更新、订阅、操作与游客设置页面。
- 节点总览改用响应式默认状态与可复用搜索／视图弹窗，移除滚动触发收缩展开；对齐图标文字并移除环形仪表的方形边框。
- 扩展隔离浏览器安全与界面回归，验证新增操作、私有数据及深浅色和手机布局。

## [0.1.50]

### English

- Refine mobile navigation with a stable full-screen reveal, no dimming overlay, no initial closing animation, and hidden mobile branding and account footer.
- Follow the browser language by default and move the persisted language preference into a single aligned personal-settings dropdown.
- Keep node counts in one centered row on all screen sizes, simplify count labels and decoration, and place refresh feedback beside the page title.
- Remove redundant login copy and spacing, and remove the duplicate blue focus ring inside node search while retaining its outer focus indicator.

### 简体中文

- 优化窄屏全屏菜单开合，修复闪烁及首次进入的关闭动画，移除压暗遮罩，并隐藏窄屏品牌和账号底栏。
- 默认跟随浏览器语言，将可保存的语言偏好移至个人设置中的单个对齐下拉框。
- 节点统计在所有屏幕保持四项一行、各自居中，简化标签与装饰，将刷新提示移至标题右侧。
- 移除登录页冗余文案及其间距，去掉节点搜索框内重复的蓝色焦点框，保留外层焦点提示。

## [0.1.49]

### English

- Make the default Core update a root-only local maintenance operation independent of saved login sessions or Core API connectivity; retain authenticated discovered operations for explicit remote updates and share the workflow with TUI.
- Keep CLI/TUI login sessions valid across inactivity and client restarts, while preserving logout, revocation, password/account security changes, browser idle expiry and guest API boundaries.

### 简体中文

- 默认 Core 更新改用 root 本机维护通道，不依赖已保存登录态或 Core API 连接；显式远程更新继续使用经认证的发现契约，TUI 共用相同流程。
- CLI／TUI 登录态不再因闲置或客户端重启过期，保留退出、撤销、密码及账号安全变更、普通浏览器闲置过期和游客 API 边界。

## [0.1.48]

### English

- Share compact real-state progress across updates, Agent/Compose deployment and rebuilds, preserving cancellation, retries, rollback and error details. Retain content during refresh and use shared loading placeholders; preserve long filename extensions and add exact full-path copy to native file information.
- Share inline clipboard feedback and rectangular segmented controls across SSH, enrollment, host information, telemetry and appearance; align overlay surfaces and responsive headers.
- Add a read-only Compose configuration diff using the shared dialog, preserving exact contents, validation and conflict checks.
- Replace the VPS Manage shortcut and inline Docker expansion with a Docker details button using the shared modal dialog, showing the complete container inventory with its existing sorting and actions; remove the obsolete preview-count setting across Core and Web.
- Give narrow screens a full-screen navigation menu with a morphing hamburger toggle, wrapping shared headers instead of stretching them.

### 简体中文

- 更新、Agent／Compose 部署与重建共用真实任务进度，保留取消、重试、回滚及错误详情；统一首次加载与保留内容的刷新反馈，长文件名保留扩展名，原生文件信息弹窗支持完整路径复制。
- SSH、安装命令、主机信息共用原位复制反馈，网络／IO 与外观共用矩形分段滑块，统一浮层表面和可换行标题。
- Compose 配置增加共享弹窗内的只读差异对照，保留原文、校验与版本冲突检查。
- VPS 卡片移除管理按钮及行内 Docker 展开区，原位置改为 Docker 详情按钮，复用共享弹窗展示完整容器列表及操作，不再套用卡片预览数量或可见性筛选。
- 窄屏改用全屏导航菜单和汉堡按钮开合动画,共享标题在面板内换行而不是被拉伸。

## [0.1.47]

### English

- Abort private file requests before view teardown and fix elFinder destruction after failed initialization, preserving guest entry during unavailable directory reads.
- Stack narrow VPS headers with version and uptime aligned to the name; remove duplicate online/Engine badges from the file-manager heading.
- Preserve Core file-operation errors in elFinder’s expected transport format instead of showing an undefined HTTP status.
- Replace the VPS header tile with an automatically detected operating-system logo from 22 bundled Linux/BSD distribution icons, using existing theme colors and a generic fallback.
- Prepare file-manager UI assets from authenticated node details without remote file reads and show immediate loading feedback while the first directory is pending.
- Collapse the file-manager cold-start widget waterfall into one lazy bundle and fetch the ordered self-hosted scripts concurrently.

### 简体中文

- 文件管理销毁前先中止私有请求，修复初始化失败后 elFinder 卸载报错，确保目录不可用时仍可进入游客页。
- 窄屏 VPS 标题分行，版本与运行时间和名称左侧对齐；移除文件管理标题旁重复的在线／Engine 状态。
- 文件操作错误按 elFinder 要求的传输格式保留原始提示，不再显示未定义的 HTTP 状态。
- VPS 标题灰底图块改为自动识别的操作系统 Logo，内置22个 Linux／BSD 发行版图标，沿用项目主题配色并提供通用回退。
- 管理态节点详情提前准备文件管理界面资源，不预读远程文件；首次目录等待期间立即反馈加载状态。
- 文件管理首次加载的组件依赖合并为一个懒加载包，本地脚本并行下载并保持执行顺序，消除逐文件串行加载。

## [0.1.46]

### English

- Remove the kernel-memory card and reduce CPU scheduling to steal, I/O wait, user and system. Coalesce concurrent file-browser reads, bound read concurrency and briefly reuse ancestor metadata while always refreshing directory contents.
- Add explicitly estimated four-hour download/upload and disk read/write totals below the shared history charts; missing history stays unknown.
- Persist network and disk throughput in sparse five-second buckets with 24-hour retention, cached-report deduplication, bounded four-hour queries and incremental five-second Web refresh. Existing minute/hour histories remain available.
- Read the latest four hours of persisted Core five-second history for overview and detail network/disk I/O charts, with bounded refresh concurrency and explicit missing-data gaps instead of browser-session buffers.
- Consolidate diagnostic timestamps and system metadata into their existing cards; compact Hardware and addresses while retaining both public IP families and omitting empty rows and duplicate/container addresses.
- Combine load, CPU, memory, disk, Swap and billing traffic in one rounded resource rectangle with six columns on wide screens and three on narrow screens.
- Adapt shared per-core rings to core count and card space, hide single-core cards, and add billing-quota rings using existing traffic accounting settings.
- Remove CPU pressure and connection tracking cards and the dedicated conntrack incident trigger; display CPU steal without translation.

### 简体中文

- 移除内核内存卡片，CPU 调度精简为 steal、I/O wait、user 和 system。文件浏览合并并发重复读取、限制读取并发，短暂复用父目录元数据，目录内容始终重新获取。
- 详情网络和磁盘 I/O 曲线下新增四小时下行／上行及读取／写入累计估算；无历史数据保持未知。
- 网络与磁盘速率新增稀疏五秒历史，保留24小时、去重缓存上报，查询最多四小时，Web 每五秒增量刷新；原有分钟／小时历史继续提供。
- 总览和详情网络／磁盘 I/O 曲线改读 Core 最近四小时的持久化五秒历史，限制刷新并发，缺失时段断线，不再依赖本次浏览器会话缓冲。
- 采集时间与系统信息合并到既有卡片；硬件与地址改为紧凑布局，保留公网双栈，省略无数据空行、重复地址和容器网桥。
- 系统负载、CPU、内存、磁盘、Swap 与账期流量合并为一个圆角矩形，宽屏六列、窄屏三列。
- 每核心复用环形仪表，按核心数量和卡片空间自适应，单核隐藏；账期流量复用现有额度与计费口径显示百分比环。
- 删除 CPU 压力、连接跟踪卡片及连接跟踪专用异常触发；CPU steal 保留英文名称。

## [0.1.45]

### English

- Remove the Application metrics, TCP quality, Filesystems, System services, Swap activity, Memory pressure, I/O pressure and Container resources cards, their metric/catalog configuration and dedicated incident triggers; retain the shared square-card layout for the eight remaining diagnostic groups.

### 简体中文

- 移除应用指标、TCP 质量、文件系统、系统服务、Swap 换页、内存压力、I/O 压力、容器资源八张卡片及对应指标／配置和专用异常触发；其余八组诊断继续复用同一套方形卡片布局。

## [0.1.44]

### English

- Extend the shared operation contract with diagnostic definitions, bounded incident recordings, capacity forecasts, performance baselines, change comparisons and cross-node correlations; retain unknown results when evidence is insufficient.
- Preserve the original eight overview cards and add sixteen diagnostic square cards using the same card class, rounded corners, sizing and spacing, with four columns on wide screens and two on narrow screens. Keep existing gauges and charts, remove redundant captions and enlarge gauge details to the title size.
- Reuse historical monitoring for diagnostic series, Host management for collection configuration and the discovered operation interface for analyses; keep Web, CLI and TUI on the same Core operations.
- Persist source-aware diagnostic history and HTTP probe phases, bound raw recording retention and response sizes, deduplicate cached samples and keep all diagnostic extensions private from guest projections.
- Maintain browser regressions for shared card geometry, account-menu sign-out, direct installation-command copying, session safety and guest boundaries; prevent password-eye press animation from escaping its input.

### 简体中文

- 共享操作契约新增诊断定义、有界故障录像、容量预测、性能基线、变更对照与跨节点关联；证据不足时保留未知结果。
- 保留原有八张总览卡片，新增十六张诊断方形卡片，直接共用原卡片类名、圆角、尺寸和间距；宽屏四列、窄屏两列。保留既有仪表和统计图，移除重复说明，仪表下方读数字号与标题一致。
- 新指标曲线沿用历史监控，采集配置沿用主机管理，分析沿用操作中心；Web、CLI、TUI 使用同一套 Core 操作。
- 持久化带来源时间的诊断历史与 HTTP 探测阶段信息；限制原始录像保留量和响应大小，去重缓存样本，所有诊断扩展继续隔离于访客公开投影。
- 维护卡片几何、账号菜单退出、直接复制安装命令、会话安全和访客边界的浏览器回归；防止密码小眼睛的按压动画超出输入框。

## [0.1.43]

### English

- Reuse the VPS card's complete circular reading component for CPU, memory, disk, swap and load, including the original gauge size, typography and captions. Load uses the same percentage formula; all eight square cards retain their top titles.
- Share live charts and inline readings between the VPS overview and metric cards. Network and process/connection charts retain at most 60 real samples, leave gaps for unavailable data and reset after node restarts or generation changes.
- Add a reusable eye control to the sign-in password field: reveal for five seconds, hide on a second click, focus departure, submit or backgrounding, and preserve keyboard access and the typed value.
- Remove the redundant coverage note and UTC labels from billing traffic without changing the accounting or displayed time values. Clarify component reuse and title preservation in the design rules.

### 简体中文

- CPU、内存、磁盘、Swap、负载直接复用 VPS 卡片的完整圆环读数组件，保持原有仪表尺寸、字体和下方说明；负载使用相同百分比公式，八张正方形卡片均保留顶部标题。
- VPS 总览与指标卡片共用实时图表和行内读数。网络、进程与连接图表最多保留 60 个真实采样点，数据不可用时断线，节点重启或连接代次变化后重新采样。
- 登录密码框新增可复用的小眼睛按钮：点击显示五秒，再次点击、离开输入区、提交或切到后台时隐藏；支持键盘操作并保留填写内容。
- 去掉账期流量的重复覆盖说明和 UTC 标记，不改变流量核算与时间值；设计规范明确组件直接复用和保留卡片标题。

## [0.1.42]

### English

- Add administrator and read-only accounts with contract-discovered account management, password-verified switching, private profiles and per-account sessions; enforce permissions and immediate session, terminal and transfer revocation in Core.
- Recover login submissions safely after session/CSRF changes and preserve the active session on failed login or account switching.
- Simplify personal settings with shared card spacing, responsive paired panels, compact controls, reusable account dialogs and readable device summaries.
- Present the eight realtime metrics as equal rounded squares in four columns or two on narrow containers; remove redundant status badges, duplicate interface rates and the GPU panel, and compact overview host badges.
- Existing single-account development databases require an explicitly authorized, backed-up one-time conversion before deployment; this release does not include an automatic storage migration.

### 简体中文

- 新增管理员与只读账号，共用操作契约提供账号管理、验证密码后切换、私有资料及独立会话；Core 强制权限校验，及时撤销失效会话及关联终端和文件传输。
- 安全恢复会话／CSRF 变化后的登录提交，登录或切换账号失败时保留原会话。
- 精简个人设置：统一卡片间距、宽屏并排窄屏堆叠、紧凑控件、复用账号弹窗，并以简洁设备名称展示会话。
- 八项实时指标改为等大圆角正方形，宽屏四列、窄容器两列；移除重复状态胶囊、网卡重复速率和 GPU 模块，收紧首页主机徽标。
- 旧单账号开发数据库部署前需明确授权、备份并进行一次性转换；本版本不包含自动存储迁移。

## [0.1.41]

### English

- Separate the host metric grid (swap, load, processes and billing traffic) from the realtime grid so the bordered cards no longer touch.

### 简体中文

- 主机指标网格（Swap、系统负载、进程与连接、账期流量）与实时指标网格之间增加间距，带边框的卡片不再紧贴。

## [0.1.40]

### English

- Wrap each realtime metric (CPU, memory, disk, network, swap and load) in the shared card surface with the standard card radius and border, matching the node cards on the overview.

### 简体中文

- 实时指标的每个指标（CPU、内存、磁盘、网络、Swap、系统负载）统一包进共享卡片表面，使用标准卡片圆角与边框，与总览的节点卡片一致。

## [0.1.39]

### English

- Streamline the Agent enrollment page: the generated command copies automatically with a confirmation dialog, the copy action sits beside the right-aligned generate button, and the usage hint moves under the section heading.
- Remove redundant text across the settings page and simplify its layout; drop decorative title periods and align the back action with the section heading on the enrollment page.
- Refine the overview host-info card: the Core/Agent target badge aligns with the header actions, the node name and online state move below it, and node cards place the version and uptime beside the node name with narrow-screen wrapping.

### 简体中文

- 精简 Agent 注册页：生成的命令自动复制并以弹窗确认，复制按钮位于右对齐的生成按钮旁，使用说明移到区块标题下方。
- 清理设置页的冗余文字并简化布局；去掉装饰性标题句点，注册页返回按钮与区块标题水平对齐。
- 优化总览主机信息卡：Core/Agent 目标徽标与标题操作对齐，节点名称与在线状态移到其下方，节点卡片将版本号与运行时间移到名称右侧并在窄屏自动换行。

## [0.1.38]

### English

- Resolve built-in update releases from the published static version files and construct release download URLs directly, removing the GitHub REST API from Core update checks so per-IP rate limits cannot block upgrades.

### 简体中文

- 内置更新的版本解析改用发布时写好的静态版本文件并直接构造发布下载地址，Core 更新检查不再调用 GitHub REST API，出口 IP 的 API 配额限制不会再阻塞升级。

## [0.1.37]

### English

- Publish the ten supported billing currencies in the operation contract as a schema enum shared by Web, CLI and TUI, render the currency field as a select control fed from that contract, and reject asset bodies with values outside the enum.
- Make Agent enrollment tokens single-use without a time limit: a token stays valid until a device enrolls, consumption invalidates it immediately, and renewing an enrollment replaces the outstanding token.
- Simplify the Agent enrollment page by removing the redundant heading texts and dividers, drop the decorative title periods, size the language selector by its content so labels never truncate, and unify every radius on the shared card and control tokens.
- Update the Core installer to resolve releases from the static `core-latest.txt` version file instead of the GitHub REST API.

### 简体中文

- 在操作契约中发布十种受支持币种的 schema 枚举，Web/CLI/TUI 共用；币种字段改为按契约渲染的下拉框，枚举之外的资产值会被拒绝。
- Agent 注册令牌改为无时间限制的单次使用：令牌在设备完成注册前一直有效，注册成功立即失效；重新生成安装命令会替换未使用的旧令牌。
- 精简 Agent 注册页：移除冗余标题文字与分割线，去掉标题装饰句点，语言选择器按内容自适应宽度不再截断，全部圆角统一到共享的卡片与控件 token。
- Core 安装器改为读取静态 `core-latest.txt` 版本文件解析最新版本，不再调用 GitHub REST API。

## [0.1.36]

### English

- Run Core natively on the controller: remove Core Docker image and Compose deployment paths, and make the Core installer create, enable and start the root `nodedance.service` on `0.0.0.0:8180` while preserving existing data. Core updates now target the native service; Agent Docker deployment remains available.
- Keep Tailscale peer discovery available to native Core through the existing read-only host query without changing the Tailscale service or its configuration.

### 简体中文

- Core 改为在主控机原生运行：移除 Core Docker 镜像与 Compose 部署路径；安装器自动创建、启用并启动 root `nodedance.service`，监听 `0.0.0.0:8180`，并保留现有数据。Core 内置更新改为更新原生服务；Agent 仍支持 Docker 部署。
- 原生 Core 继续通过现有只读主机查询发现 Tailscale 节点，不修改 Tailscale 服务或配置。

## [0.1.35]

### English

- Include the saved custom SSH connection (node preference host/user/port) in the shared account-choice dialog: it stays selectable while discovery is pending or unavailable, discovered alternatives are preserved without duplicating an identical command, and the saved entry is the preferred preselected default while still requiring the dialog's explicit "Copy SSH" confirmation.

### 简体中文

- 将已保存的自定义 SSH 连接（节点偏好中的主机／用户／端口）纳入共享账号选择对话框：识别进行中或不可用时仍可选择，保留已识别账号且不重复相同命令，已保存项作为默认预选，仍需对话框内“复制 SSH”的显式确认。

## [0.1.34]

### English

- Copy SSH always opens a shared account-choice dialog first: preselect the saved or detected account, focus the checked option, copy only through the dialog's explicit "Copy SSH" action, and keep Cancel or Escape from touching the clipboard. Detection without candidates reports the unavailability instead of an empty dialog.
- Show SSH copy feedback as a transient overlay anchored above the clicked Copy SSH button; remove the persistent in-card status line, the card account picker and the card footer with its full-details button. Card body clicks inspect without entering detail navigation, and pending nodes show their enrollment state instead of uptime.
- Add a shared choice dialog with radio account options and per-account command details, edge-fade masks for overflowing panels (dialog, overview rail, container logs) and streaming log auto-follow with a "jump to latest" pill; centralize motion presets and keep press feedback, reduced-motion and touch growth rules on one token set.
- Revert an in-progress sidebar drag with Escape, reset the sidebar width on double-click, always expand the overview host-info and memo sections, and register a stream-panel browser regression suite while extending SSH shortcut and node navigation coverage.

### 简体中文

- “复制 SSH”始终先打开共享账号选择对话框：预选已保存或已识别账号并聚焦选中项，仅通过对话框内的“复制 SSH”动作写入剪贴板，取消或 Escape 不触碰剪贴板；识别无候选时提示不可用，而不是弹出空对话框。
- SSH 复制反馈改为锚定在所点“复制 SSH”按钮上方的短暂浮层；移除卡片内常驻状态行、账号下拉选择和带“查看完整详情”按钮的底部区域。点击卡片空白处即可查看信息且不进入详情导航，待注册节点显示注册状态而非运行时长。
- 新增带单选账号选项与逐账号命令详情的共享选择对话框，为溢出面板（对话框、总览侧栏、容器日志）添加边缘渐隐遮罩，并支持流式日志自动跟随与“跳到最新”提示；集中维护动效预设，按压反馈、减弱动效与触控放大共用同一套规则。
- 侧栏拖动中按 Escape 恢复原宽度，双击重置侧栏宽度，总览主机信息与备忘录始终展开；注册流面板浏览器回归用例，并扩展 SSH 快捷入口与节点导航覆盖。

## [0.1.33]

### English

- Migrate the management Web application to React while preserving discovered Core operations, enrollment, container/Compose controls, terminal sessions and guest isolation.
- Replace VueFinder with the mature elFinder file manager, backed by the existing protected Agent file service; retain uploads, downloads, search, pagination, native dialogs, archives, editing and conflict recovery.
- Rebuild shared light/dark tokens, controls, navigation and responsive sizing from the supplied Claude Figma file, with open-claude-ui as a secondary motion reference. Keep the terminal dark, simplify VPS card headers and preserve full-width readable node names.
- Determine update eligibility from stable component tags only; remove protocol/capability/storage fingerprint gates and release manifest assets while retaining checksums, architecture selection, authentication, request identity, health checks and failure recovery. Pair with Agent agent-v0.1.21.

### 简体中文

- 将管理 Web 应用迁移到 React，保留动态发现的 Core 操作、注册、容器／Compose 管理、终端会话及访客隔离。
- 使用成熟 elFinder 替换 VueFinder，复用受保护的 Agent 文件服务，保留上传下载、搜索、分页、原生对话框、归档、编辑与冲突恢复。
- 以提供的 Claude Figma 文件重构共享浅深色 Token、控件、导航及响应式尺寸，open-claude-ui 仅补充动效参考；终端保留深色，简化 VPS 卡片顶部并保持名称完整可读。
- 更新资格仅对比稳定组件 tag，移除协议／能力／存储指纹门槛与发布清单附件，保留校验和、架构选择、认证、请求身份、健康检查及失败恢复；配对 Agent agent-v0.1.21。

## [0.1.32]

### English

- Rebuild the node overview with a flat count strip, compact full-width VPS cards, left-aligned load/CPU/memory/disk rings and bounded live network/disk IO charts. Keep host information and the shared memo in a sticky desktop rail and below the VPS list on narrow screens; inspect cards without entering their management workspace.
- Add administrator-only Core runtime information and revision-protected shared memo operations to the discovered Web/CLI/TUI contract. Provide header display settings, refresh, sensitive-information masking, safe copy and memo edit/save/cancel actions; preserve drafts on save conflicts.
- Improve desktop field sizing and alignment across enrollment, billing, alerts, feeds, Compose, Docker, images, probes, updates and operation forms, while retaining mobile layouts. Keep the language selector compact and remove decorative load-label underlining.
- Carry Agent disk read/write rates through freshness handling, numeric history and alert subjects; extend isolated browser checks for overview interaction, responsive layouts, memo conflicts, masked clipboard output and public-data isolation.

### 简体中文

- 重构节点总览：使用平面计数区、紧凑整行 VPS 卡片、从左到右的负载／CPU／内存／磁盘圆环和有界实时网络／磁盘 IO 曲线。桌面端主机信息与共享备忘录在右侧跟随，窄屏移至 VPS 列表下方；查看卡片信息无需进入管理工作区。
- 将管理员专用的 Core 运行信息、带版本冲突保护的共享备忘录纳入 Web／CLI／TUI 动态操作契约。提供标题栏显示设置、刷新、敏感信息遮蔽、安全复制以及备忘录编辑／保存／取消；保存冲突保留草稿。
- 调整注册、账单、告警、订阅、Compose、Docker、镜像、探测、更新和操作表单的桌面宽度与对齐，保留手机布局；收紧语言选择器，并去掉负载文字的装饰下划线。
- 将 Agent 磁盘读写速率接入指标时效、数值历史和告警项；扩展隔离浏览器验证，覆盖总览交互、响应式布局、备忘录冲突、遮蔽后复制与公开数据隔离。

## [0.1.31]

### English

- Compact the operational console: VPS cards use container-queried three-column label-value readings, dense Docker rows without per-item dividers, and scoped hover/focus styles; monitoring panels drop repeated card borders and oversized values; network interfaces render as single-line rows with rates and status inline; Compose project cards separate by spacing instead of lines.
- Consolidate global card overrides into component styles and codify the compact-density rules (padding, spacing, typography, container queries) in DESIGN.md.

### 简体中文

- 压缩运维控制台密度：VPS 卡片改为容器查询驱动的三列标签数值读数，Docker 展开为无逐行分割线的紧凑行，悬停与焦点样式收敛到组件内；监控面板去除重复卡片边框与超大数值；网卡以单行呈现速率与状态；Compose 项目卡改用间距而非横线分隔。
- 将全局卡片覆盖样式合并进组件样式，并在 DESIGN.md 中固化紧凑密度规范（内边距、间距、字号、容器查询）。

## [0.1.30]

### English

- Rebuild light/dark surface and control roles around the supplied Claude reference, flatten node metrics and management sections, present Compose/images/discovery as continuous resources, and preserve readable focus/selection and embedded tool themes.
- Localize discovered operation descriptions, parameter labels, header hints and enum presentation; support Chinese search while preserving API identifiers and submitted values.

### 简体中文

- 按提供的 Claude 参考重构深浅主题表面与控件角色，压平节点指标和管理区，Compose／镜像／发现使用连续资源行，保留可读的焦点、选中态和嵌入工具主题。
- 汉化动态发现的操作说明、参数标签、请求头提示和枚举显示，支持中文搜索，保留 API 标识与提交值。

## [0.1.29]

### English

- Keep alert form controls inside their cards and align subscription fields, interval inputs and notification settings across desktop and mobile layouts.

### 简体中文

- 修复告警表单控件超出卡片的问题，统一订阅表单字段、刷新间隔和通知设置的桌面／移动端对齐。

## [0.1.28]

### English

- Replace native select presentation throughout Web with a shared themed accessible listbox; add custom checkbox/radio feedback and smoother sidebar motion while preserving form semantics, immediate resize, reduced motion and guest portal cleanup.

### 简体中文

- Web 下拉框统一改为主题化无障碍列表框，增加自定义复选／单选反馈与平滑侧栏动画，保留表单语义、即时拖动、减少动态效果和游客切换浮层清理。

## [0.1.27]

### English

- Add project-wide Compose multi-selection and shared Docker action controls, independent task outcomes/retries, exact deletion confirmation, persisted display aliases, and compact UUID-free mobile layouts; align network interface health with its name and prioritize Overview → Docker details → Compose projects in the VPS sidebar.
- Add an inline directory-only VueFinder picker to Compose creation, sharing Agent directory creation and retaining the deployment draft.

### 简体中文

- 增加 Compose 项目级多选与共享 Docker 操作按钮，支持逐项目任务结果／重试、精确删除确认、持久显示名称和不显示 UUID 的紧凑移动布局；网卡状态与名称同行右对齐，VPS 侧边栏优先排列总览 → Docker 详情 → Compose 项目。
- Compose 新建表单内置仅目录的 VueFinder，复用 Agent 目录创建，保留部署草稿。

## [0.1.26]

### English

- Replace horizontal VPS detail tabs with contextual sidebar navigation, placing Back to home first and sharing global sidebar width, collapse, mobile drawer and accessible reduced-motion transitions.

### 简体中文

- 将 VPS 详情横向标签移至节点专属侧边栏，顶部提供返回主页面，复用全局侧边栏宽度、收起和移动端抽屉，并支持减少动态效果的过渡动画。

## [0.1.25]

### English

- Discover SSH connection addresses, listening ports and account candidates from the root Agent through shared diagnostics; automatically prepare safe connection commands and retain browser account choice across node state changes.

### 简体中文

- 通过 root Agent 的共享诊断自动发现 SSH 地址、监听端口及账号候选，自动生成安全的连接命令；节点状态变化时保留浏览器中的账号选择。

## [0.1.24]

### English

- Add monthly traffic quota, purchase date, expiry date, renewal price and currency to Agent enrollment and node settings; support year, month, hour and minute billing, persist enrollment assets atomically, and place the editor immediately before node deletion.
- Show only summary-selected main network interfaces and round byte rates to integers or one decimal in larger units; verify mobile layouts, asset editing and clearing, shared operation discovery, and private billing exclusion from guest data.
- Correct installer guidance to describe resolving the latest stable Agent at execution time.

### 简体中文

- 在生成 Agent 注册命令和节点设置时增加每月流量额度、购买日期、到期时间、续费价格及币种；支持年、月、小时、分钟计费，注册资产以同一事务保存，编辑入口位于删除节点之前。
- 网络面板仅显示参与汇总的主接口，字节速率取整数、更大单位保留一位小数；验证移动端布局、资产修改与清空、共享操作发现及游客数据不泄露费用信息。
- 修正安装提示，明确安装命令执行时解析最新正式 Agent 版本。

## [0.1.23]

### English

- Move node deletion from the detail heading to the bottom of Node settings, keeping confirmation and failure feedback together; verify light/dark mobile layouts and the existing deletion and guest-security regressions.

### 简体中文

- 将删除节点入口从详情页顶部移到节点设置最下方，集中显示确认操作和失败反馈；验证浅色／深色移动端布局及既有删除、游客安全回归。

## [0.1.22]

### English

- Initialize the built-in observation worker command executor so default update preflight can read the running version; verify native and real Docker checks without taking the transaction lock.

### 简体中文

- 初始化内置观察进程的命令执行器，使默认更新预检查能够读取运行版本；覆盖无需获取事务锁的原生及实际 Docker 检查。

## [0.1.21]

### English

- Standardize maintenance commands as `nodedance update` and `nodedance agent update`; generate native/Docker Agent installation commands that select the latest stable Agent at execution time.

- Build update execution into the Core binary with automatic deployment discovery and transient workers; remove separate updater assets and setup. Standard managed deployments provide the host permissions needed for update and recovery.

### 简体中文

- 统一维护入口为 `nodedance update` 与 `nodedance agent update`；原生／Docker 安装命令在执行时自动选择最新正式 Agent。

- 将更新执行内置于 Core 二进制，自动识别部署并按需启动临时工作进程；移除独立 updater 发布产物和配置步骤。标准受管理部署提供完成更新与恢复所需的宿主机权限。

## [0.1.20]

### English

- Add node deletion and pending-node enrollment renewal through the discovered Core operation contract, with credential revocation, state cleanup and immediate guest publication withdrawal.
- Simplify native and Docker enrollment to complete copyable commands pinned to the paired Agent release; show enrollment expiry in Beijing time and retain installation access on pending-node details.
- Expand isolated browser coverage for enrollment, node removal, guest boundaries and bilingual light/dark/mobile layouts; include paired release metadata in source Docker builds.

### 简体中文

- 通过 Core 发现契约增加节点删除与待注册节点安装命令重新生成，撤销凭据、清理节点状态并立即撤回游客公开数据。
- 简化原生与 Docker 注册为完整可复制命令，固定配对 Agent 发布版本；以北京时间展示凭据有效期，并在待注册节点详情保留安装入口。
- 扩展注册、节点删除、游客边界及双语浅色／深色／移动端的隔离浏览器回归；源码 Docker 构建包含配对发布元数据。

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
