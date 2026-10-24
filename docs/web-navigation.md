# Web browser navigation / 浏览器导航

Web records page changes in browser history. Back and Forward restore the corresponding view, and refreshing or bookmarking its URL keeps that view. The fragment routes are browser-local; the Core API and its discovered operation contract continue to handle all management requests.

Web 会将页面切换记入浏览器历史。后退、前进可恢复对应页面，刷新和书签也保留当前视图。URL 片段只表示浏览器本地页面；管理请求仍使用 Core 发布的操作契约。

| View / 视图 | Example / 示例 |
| --- | --- |
| Node overview / 节点总览 | `#/nodes` |
| Node detail / 节点详情 | `#/nodes?node=NODE_UUID` |
| File directory / 文件目录 | `#/nodes?node=NODE_UUID&section=files&path=%2Fsrv` |
| Node history / 节点历史 | `#/nodes?node=NODE_UUID&section=history` |
| Alert rules / 告警规则 | `#/alerts?tab=rules` |
| Host assets / 主机资产 | `#/host-management?node=NODE_UUID&tab=assets` |
| Guest display / 游客展示 | `#/guest` |
| Add Agent, including discovered targets / 添加 Agent（含网络发现） | `#/agent-enrollment` |
| Personal settings and guest publication / 个人设置与游客公开配置 | `#/settings` |

Add Agent uses one connection form across manual and discovered targets. Settings presents profile, security, sessions, accounts and administrator publication together; neither view uses page tabs. Connection drafts and credentials stay out of the URL.

添加 Agent 在手动目标与发现目标之间共用连接表单。个人设置连续呈现资料、安全、会话、账号和管理员公开配置；两者均不使用页面标签切换，连接草稿与凭据不写入 URL。

Node UUIDs identify the selected resource in a private detail URL. Names and versions remain the visible identifiers in cards and headings. Switching to a different top-level page closes the mobile navigation and displays the page from the top. Repeatedly selecting the current view does not add duplicate history entries. Before an internal page change, there is no additional NodeDance history entry for Back to restore, so the browser keeps its normal behavior.

节点 UUID 在私有详情 URL 中定位资源；卡片与标题展示名称和版本。切换页面会关闭移动端导航并从页首展示；重复选择当前视图不会增加重复历史记录。尚未切换过内部页面时，后退仍按浏览器的正常历史行为执行。

First entry and return from guest mode check `auth.me` before activating a private view. Authenticated in-app navigation reuses the established session, including private history navigation. Core authorizes every API request, and a 401 clears management state. Login resumes the requested view; logout/session expiry replace the location with login. Restoring a cached document through `pageshow` revalidates the session and clears password inputs. URLs and forged `history.state` markers do not grant access.

首次进入及从游客模式返回管理页时检查 `auth.me`；已登录后的页内切换与私有历史导航复用当前会话。Core 对每次 API 请求鉴权，401 会清除管理状态。登录后恢复请求的视图，退出或会话失效时替换为登录页。通过 `pageshow` 恢复缓存文档时重新验证会话并清除密码输入。URL 或伪造的历史标记不能赋予权限。

An explicit guest URL only fetches the public guest contract and projection, including with an administrator cookie and after reload. Leaving guest mode requires the same session check as any management entry. Navigation sequence checks prevent an older asynchronous permission response from replacing a newer guest view.

明确访问游客 URL 时只加载公开操作契约与游客投影；携带管理员 Cookie、刷新页面时也如此。离开游客模式进入管理页必须通过会话检查。导航序号检查可防止旧的异步权限响应覆盖新的游客页面。

History stores view identifiers only. Credentials, form contents, confirmations and submitted commands are never written into routes or history. Restoring a view performs reads and observation; creating, retrying, canceling or rolling back shared work still requires an explicit user action through its Core operation. Interactive terminals are not recreated from a URL.

历史只保存视图标识，不写入凭据、表单内容、确认信息或已提交命令。恢复视图执行读取和观察；创建、重试、取消、回滚共享任务仍需用户明确调用对应 Core 操作。交互终端不会由 URL 自动重新创建。

`web/scripts/audit-interfaces.cjs` verifies page/tab history and reload while asserting that previously submitted tasks, updates and rollbacks are not resubmitted. `web/scripts/audit-guest.cjs` verifies node detail/section restoration and guest history/session boundaries, including a delayed permission response. The scripts run isolated Core fixtures and real Chromium.

更新和订阅保持独立的管理员视图 `#/updates`、`#/feeds` 及侧栏入口，复用当前已登录会话，每次管理请求仍由 Core 鉴权。订阅地址和表单不写入导航历史。详见 [RSS／Atom 订阅](rss.md)。

VPS 服务探测位于详情侧边栏的独立页面 `#/nodes?node=NODE_UUID&section=probes`，可刷新和直接定位。完整配置、状态和历史仅在此页面显示；离开时停止 Web 列表轮询，Core／Agent 后台探测继续运行。总览只在已配置探测时显示简短状态摘要和入口，标明最近读取时间，进入总览读取一次，不保持探测列表轮询。没有配置时不显示空面板。

Docker 容器管理采用表格与顶部统一工具栏，支持按名称／状态筛选、排序、每页 20／50／100 项及多选。当前页全选只选择本页；跨页选择会保留，筛选隐藏或清单移除的容器会取消选择。启动、停止、重启、暂停、恢复、删除按每个目标调用现有 Core 操作，各自显示任务结果；只有全部所选目标都满足当前操作条件时才可执行。删除必须先停止，并确认每个完整容器 ID；确认期间刷新改变运行状态时不提交。重命名、终端、日志／监控、显示设置、审计及重建使用单选目标，Compose 重命名／重建及回滚保留资源限制不变。Docker 拖拽排序已移除。

`web/scripts/docker-regressions.cjs` 随 `audit:interfaces` 执行，使用隔离 Core 动态契约与全拦截 Docker 响应，验证多选目标、分页、失联／陈旧数据、确认期间状态变化、逐目标重试身份及中英文浅色／深色／移动端布局，不连接 Docker daemon 或真实 Agent。

Entering a VPS detail replaces global sidebar links with that VPS’s sections. **Back to home** is the first navigation item and restores the main node overview and global sidebar. Detail navigation shares sidebar width, collapse preferences, keyboard resizing, and the mobile drawer; section selection closes the mobile drawer. Context changes fade with a slight blur, and reduced-motion preferences disable the transition. URLs, refresh and browser history preserve the selected node and section.

进入 VPS 详情后，侧边栏切换为该节点的功能导航，第一项为**返回主页面**；返回节点总览后恢复全局导航。详情侧边栏复用原有宽度、收起设置、键盘宽度调节和移动端抽屉，选择页面后关闭移动端抽屉。导航切换使用轻微模糊淡入淡出，减少动态效果设置下关闭动画。URL、刷新和浏览器历史保留所选节点与页面。

`web/scripts/node-navigation-regressions.cjs` runs with the isolated real Core interface audit, covering card entry, context restoration, shared width/collapse, history/reload, mobile dismissal/focus, reduced motion, and light/dark/mobile layouts.

Compose uses whole projects as the selection unit. Web shares the Docker detail action-button component, with select all/clear, multi-project start/stop/restart/deploy/delete, single-project config editing and display-name changes. Every project receives its own Core task and submission identity; a failed submission can retry just that project with the same intent key. Selection is revalidated after confirmation, and stale, unavailable or busy projects prevent bulk mutation.

Compose 以整个项目为勾选单位，复用 Docker 详情操作按钮组件，支持全选／清空以及多项目启动、停止、重启、部署和删除；编辑配置和修改显示名称仅允许单选。每个项目对应独立 Core 任务与请求身份，失败提交可单独重试并保留原身份；确认后重新读取并验证项目，陈旧、不可用或忙碌的选择不能提交批量操作。

Deletion removes the deployment with `docker compose down --remove-orphans`, retaining volumes, images, config files and working directories. The saved project/config entry remains available for redeployment. Core requires `deleteConfirmationKey` to match the route's exact project key. Unknown/interrupted outcomes are observed through the existing task journal and are never automatically replayed. Display names use node-scoped Core preferences and never change Compose resource names, YAML, labels or directories.

删除通过 `docker compose down --remove-orphans` 撤下部署，保留数据卷、镜像、配置文件与工作目录；已保存的项目／配置入口保留，便于重新部署。Core 要求 `deleteConfirmationKey` 精确匹配路由中的项目身份。中断或不确定结果由现有任务日志观察，不自动重复执行。显示名称通过 Core 节点范围偏好保存，不改变 Compose 资源名、YAML、标签或目录。

`web/scripts/compose-regressions.cjs` covers whole-project selection, per-project outcomes/retries, confirmation freshness, persisted aliases, exact deletion confirmation and mobile instance layout against intercepted operations on an isolated real Core.

The VPS sidebar starts with Overview → Docker details → Compose projects, followed by service probes and the remaining sections. VPS 侧边栏优先排列总览 → Docker 详情 → Compose 项目，再接服务探测等其他功能。

Compose new-project forms include an inline directory-only elFinder picker. Browse folders, create a folder using the existing Core `files.action` mkdir task, then choose the current directory to fill the working-directory field. Files and unrelated file operations are omitted. Directory selection/cancellation preserves the project/YAML draft and Compose URL and returns focus to the browse button. The full file-manager screen retains its existing capabilities.

Compose 新建项目表单内置仅目录的 elFinder 选择器：浏览文件夹，通过现有 Core `files.action` mkdir 任务新建文件夹，再将当前目录回填工作目录。省去文件和其他文件操作；选择或取消保留项目／YAML 草稿及 Compose 页面地址，并恢复浏览按钮焦点。完整文件管理页面继续提供原有能力。

Shared form dropdowns, including country/icon choices and filters, render through the installed UIArc Dropdown Menu. Selection fields use radio menu items and keep native form value/change/required validation, numeric values, disabled fieldsets and local browser settings. UIArc supplies keyboard navigation, typeahead, pointer highlighting and transitions. Portals inherit product tokens and remain inside native dialogs when necessary; unmount removes private menus immediately.

表单统一使用主题化自定义下拉列表，保留原有表单选值、change 和校验语义；弹出层在屏幕范围内定位，支持触摸、键盘、首字母检索、禁用字段组和减少动态效果。离开管理页立即清除浮层与监听器。复选框保留原生键盘、选中、半选和禁用行为，采用共享主题绘制。侧栏收起使用平滑弹性过渡，拖动宽度仍即时跟随。

Lucian references: [#031 custom select](https://lucianlabs.dev/uxui-prompt/#031), [#014 checkbox feedback](https://lucianlabs.dev/uxui-prompt/#014), [#015 radio feedback](https://lucianlabs.dev/uxui-prompt/#015), [#040 sidebar motion](https://lucianlabs.dev/uxui-prompt/#040). Colors, form density and radii follow `DESIGN.md`; sample forms, gradients and task-list decoration are omitted. License notices remain with adapted code. `web/scripts/ui-controls-regressions.cjs` runs in the isolated interface audit.

Alert forms use shrinkable date columns and field widths within the card padding. Subscription labels and controls align at the top of each row; preset/custom refresh intervals share a row, with notification fields following the same spacing.

告警表单日期双列与控件宽度限制在卡片内边距范围内。订阅表单标签和控件按行顶部对齐，预设／自定义刷新间隔同排显示，通知设置采用相同间距。布局回归覆盖手机、中等和宽屏、深浅主题及所有通知模式。

Operation Center translates discovered descriptions and form labels at presentation time and searches both localized descriptions and API identifiers. It retains raw field names beside Chinese labels, preserves enum values and JSON results, and displays new unknown descriptions directly from the Core contract. Translation dictionaries do not determine operation availability, schema or execution. The isolated interface audit verifies discovered translation coverage, Chinese search, enum preservation and language/theme/mobile layouts.

操作中心在展示时翻译动态发现的操作说明与表单标签，搜索同时匹配中文说明和 API 标识。中文标签旁保留原始字段名，枚举提交值及 JSON 结果保持原样；新发现且尚未翻译的说明直接来自 Core 契约。翻译字典不决定可用操作、请求结构或执行行为。隔离界面回归检查翻译覆盖、中文搜索、枚举值和语言／主题／手机布局。

The shared light/dark theme uses semantic surface, text, control and state roles documented in `DESIGN.md`. Visual flattening does not change operation discovery, public projection, authorization or task semantics. Browser audits use fresh assets and isolated Core/Agent resources; the theme regression checks real persisted/system switching and actual rendered contrast.

共享深浅主题使用 `DESIGN.md` 中的表面、文字、控件和状态语义。视觉扁平化不改变操作发现、公开投影、权限或任务行为。浏览器回归使用新构建资源与隔离 Core／Agent，主题回归检查实际保存／跟随系统切换及渲染对比度。

窄屏全屏导航使用裁切过渡，初次进入保持关闭且静止，只有开关时才过渡；菜单不添加背景压暗遮罩。菜单按钮本身不绘制边框，键盘焦点仍通过图标颜色反馈，其他按钮保持原有样式。

Mobile full-screen navigation starts closed without an entry animation and transitions only when toggled. It has no dimming scrim. The navigation toggle has no border; keyboard focus retains icon-color feedback and other controls keep their existing styles.

Run `make build` followed by `node web/scripts/audit-navigation.cjs` for the focused navigation browser regression with an isolated temporary Core and no Agent. It also runs within the interface audit.

窄屏菜单仅在内容列表内滚动，外层固定在视口中；自动聚焦不触发滚动，快速关闭后不会执行延迟的聚焦。导航浏览器回归逐帧检查低位置选中项及连续反向开关，验证面板位置、滚动位置和展开方向不跳变。

窄屏导航隐藏 Logo／NodeDance 字标及品牌下方分割线，功能项纵向留白适度收紧并保留至少 44px 点击高度；桌面品牌布局不变。

个人设置的主题、语言、背景颜色各占一行，标题在左、控件在右；三个控件使用主题选择器的实际宽度并共用右边缘。窄屏保留水平设置行，不改成标题上置；控件沿用原组件字号、圆角与交互。语言偏好仅保存在当前浏览器，自动模式继续跟随浏览器语言。

窄屏功能项采用整行底部浅分割线，参考本地 `design-study/anthropic/pages/home.html` 的 `.nav_links_item.is-mobile` 结构及 `assets/main.css`：线宽沿用 `.0625rem`。原站主菜单使用 ivory-medium（`#F0EEE6`），与本项目当前菜单背景同色，因此映射为同一源码 `.nav_dropdown_link_block` 使用的 slate-faded-10（`#1414131a`），保证浅色分隔可见；不恢复已隐藏的品牌／账号分割线。样式集中在窄屏菜单规则中，不保留会被 `border: 0` 清除的旧顶部边框声明，桌面菜单不变。

节点总览按宽度固定呈现：宽屏保留完整计数卡、搜索框和内联视图偏好；900px 及以下使用紧凑计数与搜索／偏好／添加 Agent 图标。滚动方向不触发收缩、展开或逐帧变换，不保留旧弹簧与文档高度补偿逻辑。文字和图标使用自然 flex 对齐。

窄屏搜索和视图偏好复用 `UiDialog`：支持 Escape、关闭按钮、焦点恢复和对话框内下拉选择。共享弹窗仅在真正打开后记录并恢复触发焦点，关闭状态的 effect 清理不再抢夺其他按钮的焦点。宽窄屏共享搜索值、筛选、排序和保存处理；改变宽度不丢失输入或创建独立业务操作。环形百分比统一使用 `.resource-gauge`，避免语义类 `.ring` 与 Tailwind 的外圈工具冲突。

The overview is always expanded on wide screens and compact at widths of 900px or less. Scrolling never changes its layout. Narrow-screen search and view preferences use the existing `UiDialog`, sharing values and Core workflows with desktop controls. Resource gauges use a dedicated class to avoid Tailwind utility collisions.

运行 `make build` 后执行 `node web/scripts/audit-overview-scroll.cjs`。隔离真实 Core 与协议模拟 Agent 检查代表性桌面浅色与窄屏深色、稳定滚动、文字图标对齐、搜索和偏好弹窗、选择和保存、Escape／焦点恢复、宽度切换、中文、减少动态效果和所有环形指标无外框。默认不生成截图，真实手机触感仍需设备确认。

域名证书位于节点总览下方，管理员可管理域名、证书、CA 账号、Cloudflare DNS 提供商和后台任务。页面通过 Core 契约调用全部操作，未登录深链进入登录页且不读取私有 API。仅使用经 shadcn CLI 安装的本地 UIArc 组件；浮层留在项目主题边界内，支持窄屏内部滚动。详见 [域名证书](domain-certificates.md)。

主机管理、更新、订阅中心、操作中心、游客展示设置已统一使用通过 shadcn CLI 安装的 UIArc 本地组件。管理界面的卡片、按钮、字段、下拉、复选框、状态提示与标签切换沿用现有项目 Token，菜单浮层限制在主题边界中。页内标签保留原组件过渡，宽窄屏共享相同表单值、Core 操作、确认和任务流程。操作中心仍按连接 Core 的契约动态发现能力；游客展示的公开 DTO、默认不公开与显式发布要求不变。

Node display preferences use the installed shadcn Registry Input, Select, Textarea,
Checkbox and Button components. Telemetry updates must not replace an unsaved
name or icon draft when the persisted preference content is unchanged. Empty node
icon follows the Linux distribution; an explicit icon takes precedence. The icon
menu reads `nodes.save-preference.bodySchema.properties.icon.enum` from Core,
with local SVG artwork and decorative previews. See [node display](node-display.md).

The sidebar account footer uses the already-installed UIArc User Menu. It lists enabled/disabled accounts and their roles, retains current-account indication, Settings and Sign out, and opens an installed UIArc Dialog for target-account password verification through the unchanged `auth.switch` operation. Anonymous/guest pages render no account control; mobile retains the existing hidden account-footer design.

“总览全球”位于个人设置上方，宽屏 2:1 地球／信息卡片，窄屏纵向排列，按真实已定位节点联动。详见 [总览全球](global-overview.md)。

更新、订阅中心遵循个人设置的设置行、分隔线和操作布局，保留本地组件原版圆角，不应用全局圆角映射。更新仍通过 Core 契约检查、执行、观察及回滚；订阅仍使用原有 revision、刷新取消、条目阅读、分页及通知重试操作。

个人设置、更新中心及订阅中心在 320px 起保持标题／控件两列布局。右列按空间限定宽度，Core／Agent 选择器维持 140px 并靠右；订阅的复合间隔输入在右列内纵向排列，公开指标选择集在右列内单列排列。标题允许自然换行，表单控件与右边缘保持对齐。

Authenticated in-app navigation renders the destination immediately, reuses the current profile, and does not issue a route-level `auth.me` request. Settings sessions refresh independently; late responses cannot update a later route or another user. First entry, return from guest mode and browser back/forward-cache restoration still validate the session before displaying private pages. Core continues to authorize every API request, and a 401 clears the management view. Lazy page modules preload on navigation hover/focus and share the same import promise with rendering; preloading does not fetch private page data.

The operation console receives the authenticated role from the application instead of looking it up again. Generic forms serialize values without client-side business validation. The request layer no longer validates request bodies, headers or successful responses against schemas. Core owns business validation and permissions; client transport retains exact numeric decoding, idempotency, timeouts and error handling.

Compose edits save directly without a separate validation request or a successful-validation flag. The existing config-save task validates the candidate and checks its base hash on the Agent before atomic replacement. The standalone `compose.validate` operation remains available through the discovered generic operation interfaces. Web does not recursively validate successful responses or reject catalog definitions and payload sizes before sending; Core and Agent enforce those limits.

Agent enrollment submits its origin and name to Core without maintaining a second browser URL/transport policy. Core remains responsible for origin, development-loopback and enrollment-name validation. SSH host-key trust and credential handling remain unchanged.

JSON responses without potentially unsafe integer literals reuse native parsing directly. Responses containing long integers retain the exact-integer decoder.

Update submission does not require a prior browser eligibility check. The Apply operation performs the authoritative release/runtime check in Core; Web submits the selected version or lets Core resolve the latest stable release. Explicit checks remain available without blocking Apply. Release notes and history load independently, without automatic runtime preflight on entry or target changes.

After successful sign-in, Web uses the authenticated user already returned by `auth.login`, rather than immediately repeating `auth.me` and loading unrelated session history. Setup still reads its complete account through `auth.me`, without a fallback sign-in for a retired setup response. Opening Settings directly renders after authentication/profile loading while session history refreshes independently.

Image uploads rely on Core decoding the actual raster bytes, rather than a browser MIME/filename gate. Valid PNG/JPEG/GIF data without a recognized filename or MIME can upload; Core still rejects SVG, malformed content and oversized images and normalizes accepted data to PNG.

Business text fields no longer impose browser UTF-16 `minLength`/`maxLength` limits. Core applies its Unicode/byte limits and returns the actual error; 80-character supplementary-Unicode names are submitted intact. The generic operation form also disables browser constraint validation and forwards raw JSON unchanged, while typed JSON fields still require parsing for serialization.

Alert thresholds accept any Core-supported fractional value instead of imposing a browser-only 0.1 step. Service-probe request conversion no longer repeats the store's configuration validation; invalid ordinary creates remain HTTP 400 and are not persisted. Batch probe setup retains its preflight before any node mutation.

The traffic-quota field converts decimal GB to integer bytes with exact arithmetic, including fractional/scientific notation and byte rounding. It preserves the full uint64 range across enrollment and asset editing; other asset fields can be saved without changing quota digits. Blank input retains the existing zero/unlimited behavior. Browser input remains the installed local Input component.

Host-management reads no longer use the mutation busy gate. Changing the selected node cancels the old read, and a late response cannot fill or save another node's settings. Settings/assets load for their selected node; command history and enrollment grants load only in their respective sections. List refreshes after mutation replace older in-flight reads instead of waiting for unrelated data.

Network and disk I/O history projections and observed totals are cached by selected series and recomputed only when the history response or time range changes. Live-value, loading and error updates reuse those arrays instead of repeating date parsing and sample checks. Missing, invalid and stale-data semantics remain unchanged.

Alert Center reads the current tab’s data and its dependencies rather than fetching every tab on every refresh. Each successful result renders independently; delayed events do not block alert history. Tab changes cancel obsolete reads. SMTP security settings are submitted to Core without a second browser enum gate; Core still rejects missing or unsupported TLS modes.

For request lifecycle changes, run `node web/scripts/audit-navigation.cjs --reads-only` after `make build`. It checks warm navigation, direct settings entry, login response reuse, current-host reads and alert-tab dependencies without running unrelated language/mobile presentation cases. The default command retains those presentation regressions.

Monitoring keeps uint64 traffic counters and quotas exact in decoded data. Utilization ratios and human-readable byte labels convert at presentation time, so values beyond JavaScript’s safe-integer range do not crash the page or appear unavailable; byte labels support EiB. Editing/submission retain exact integer bytes.

Authenticated cold navigation no longer fetches public appearance before the private profile. Public branding loads for sign-in/setup, and stale profile replies cannot overwrite appearance belonging to another account or guest mode. Successful Core appearance responses are used directly without repeating server color/theme validation in the browser.

SMTP sender/recipient inputs use the installed local Input component with email keyboard hints. They submit Core-supported display names and Unicode mailbox names without the browser’s narrower email-address grammar; Core remains the mail parser and TLS policy owner.

Live and historical metric views follow replacement of their response objects instead of recursively observing every nested sample. Transport refreshes still replace those objects, so freshness and chart updates remain driven by new responses without an extra deep walk of unchanged history.

Probe list polling reuses an in-flight read; explicit refreshes and node changes replace it. Leaving the page aborts list/history/statistics reads. Probe runs and quality statistics render independently, so unavailable statistics do not hide completed runs. Editing a probe preserves its saved target; changing its kind converts the protocol only on that explicit interaction and preserves bracketed IPv6 hosts.

Domains and certificates publish each successful inventory read independently. A delayed or failed notification-channel read cannot hold back the domain list or erase successfully loaded certificates. Job polling skips an in-flight refresh instead of canceling and restarting it.

Global overview shares each in-flight metric read between fleet prefetch and the selected-node card, keyed by node identity and generation. Tour handoff reuses the pending response and keeps cached readings; leaving the view or explicitly refreshing aborts its pending metric requests.

Multiline management fields use the shared installed Popover + Button + Textarea composition. The collapsed trigger follows the ordinary control column; expanded text stays in the page draft and submits through the original Core operation. Keyboard activation, Escape/focus return, disabled fields and simple native required checks remain available. No new frontend format, length or business validators are added. The Nodes overview memo keeps its inline editor. 多行填写框统一点击展开，备忘录保持原样；必填仅做简单非空检查并明确提示。
