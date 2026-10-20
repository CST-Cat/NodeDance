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
| Guest publication settings / 游客公开配置 | `#/guest-settings` |

Node UUIDs identify the selected resource in a private detail URL. Names and versions remain the visible identifiers in cards and headings. Switching to a different top-level page closes the mobile navigation and displays the page from the top. Repeatedly selecting the current view does not add duplicate history entries. Before an internal page change, there is no additional NodeDance history entry for Back to restore, so the browser keeps its normal behavior.

节点 UUID 在私有详情 URL 中定位资源；卡片与标题展示名称和版本。切换页面会关闭移动端导航并从页首展示；重复选择当前视图不会增加重复历史记录。尚未切换过内部页面时，后退仍按浏览器的正常历史行为执行。

Entering a management view checks `auth.me` before activating it. An unavailable or expired administrator session leads to login; successful login resumes the requested view in the current sign-in flow. Logout and session expiry replace the current location with login, and older private history entries must pass the same session check. Restoring a cached document through `pageshow` revalidates the session and clears password inputs. URLs and forged `history.state` markers do not grant access.

进入管理视图会先检查 `auth.me`。会话无效或过期时显示登录页，本次登录流程成功后恢复所请求的视图。退出登录、会话失效会将当前位置替换为登录页；历史中的私有页面也必须重新通过会话检查。通过 `pageshow` 恢复缓存文档时也重新验证会话并清除密码输入。URL 或伪造的历史标记不能赋予权限。

An explicit guest URL only fetches the public guest contract and projection, including with an administrator cookie and after reload. Leaving guest mode requires the same session check as any management entry. Navigation sequence checks prevent an older asynchronous permission response from replacing a newer guest view.

明确访问游客 URL 时只加载公开操作契约与游客投影；携带管理员 Cookie、刷新页面时也如此。离开游客模式进入管理页必须通过会话检查。导航序号检查可防止旧的异步权限响应覆盖新的游客页面。

History stores view identifiers only. Credentials, form contents, confirmations and submitted commands are never written into routes or history. Restoring a view performs reads and observation; creating, retrying, canceling or rolling back shared work still requires an explicit user action through its Core operation. Interactive terminals are not recreated from a URL.

历史只保存视图标识，不写入凭据、表单内容、确认信息或已提交命令。恢复视图执行读取和观察；创建、重试、取消、回滚共享任务仍需用户明确调用对应 Core 操作。交互终端不会由 URL 自动重新创建。

`web/scripts/audit-interfaces.cjs` verifies page/tab history and reload while asserting that previously submitted tasks, updates and rollbacks are not resubmitted. `web/scripts/audit-guest.cjs` verifies node detail/section restoration and guest history/session boundaries, including a delayed permission response. The scripts run isolated Core fixtures and real Chromium.

订阅中心的私有视图为 `#/feeds`，进入时使用现有管理员会话检查。订阅地址和表单不写入导航历史。详见 [RSS／Atom 订阅](rss.md)。

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

Shared form controls use the project theme with Lucian UI-derived motion. `UiSelect` renders an accessible custom listbox, preserves the existing form value/change/validation semantics, and positions its popup within the viewport. Pointer selection, arrows, Home/End, Enter/Space, typeahead, Escape, Tab, disabled fieldsets and reduced motion are supported. Popup listeners are removed on unmount and portals are immediately removed when leaving management. Checkboxes retain native keyboard, checked, indeterminate and disabled behavior with custom theme rendering. Sidebar collapse uses a smooth spring; dragging its width remains immediate.

表单统一使用主题化自定义下拉列表，保留原有表单选值、change 和校验语义；弹出层在屏幕范围内定位，支持触摸、键盘、首字母检索、禁用字段组和减少动态效果。离开管理页立即清除浮层与监听器。复选框保留原生键盘、选中、半选和禁用行为，采用共享主题绘制。侧栏收起使用平滑弹性过渡，拖动宽度仍即时跟随。

Lucian references: [#031 custom select](https://lucianlabs.dev/uxui-prompt/#031), [#014 checkbox feedback](https://lucianlabs.dev/uxui-prompt/#014), [#015 radio feedback](https://lucianlabs.dev/uxui-prompt/#015), [#040 sidebar motion](https://lucianlabs.dev/uxui-prompt/#040). Colors, form density and radii follow `DESIGN.md`; sample forms, gradients and task-list decoration are omitted. License notices remain with adapted code. `web/scripts/ui-controls-regressions.cjs` runs in the isolated interface audit.

Alert forms use shrinkable date columns and field widths within the card padding. Subscription labels and controls align at the top of each row; preset/custom refresh intervals share a row, with notification fields following the same spacing. `web/scripts/form-layout-regressions.cjs` checks control containment and alignment at 390, 1100, 1440 and 1920 px in light/dark themes, including all notification modes.

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

个人设置的语言下拉框位于背景颜色右侧，标签顶边及控件上下边缘对齐，复用主题化下拉组件；窄屏两列按可用宽度收缩，控件不超出卡片。语言偏好仅保存在当前浏览器，自动模式继续跟随浏览器语言。

窄屏功能项采用整行底部浅分割线，参考本地 `design-study/anthropic/pages/home.html` 的 `.nav_links_item.is-mobile` 结构及 `assets/main.css`：线宽沿用 `.0625rem`。原站主菜单使用 ivory-medium（`#F0EEE6`），与本项目当前菜单背景同色，因此映射为同一源码 `.nav_dropdown_link_block` 使用的 slate-faded-10（`#1414131a`），保证浅色分隔可见；不恢复已隐藏的品牌／账号分割线。样式集中在窄屏菜单规则中，不保留会被 `border: 0` 清除的旧顶部边框声明，桌面菜单不变。

窄屏节点总览在向下滚动超过 24px 时，将现有四项计数、搜索、偏好、添加 Agent 按顺序收为一行；反向滚动超过 16px 展开，小幅抖动不会反复切换。上沿保留在原计数卡片的位置，不吸顶、不调用页面滚动；高度变化由页面底部等量空间补偿，防止接近页底时浏览器改变滚动位置。搜索和偏好复用同一份控件及状态，输入中／偏好展开时保持完整布局。桌面保持原样。

动效学习来源为本地 `design-study/anthropic` 保存的脚本与 CSS（被动滚动监听、焦点不滚动、短时长）以及 `.references/open-claude-ui/code-examples/ClaudeMotionPresets.ts` 的无过冲收敛思路；这些来源没有现成的同款节点工具栏。本项目采用局部 `--overview-ease`（Anthropic 源码实际出现的 `cubic-bezier(.165,.84,.44,1)`，亦见 Claude 参考 `CDS_EASING.DECEL`）、480ms 高度与元素几何同步过渡，避免原先 220ms expo-out 前段过快，快速反向从当前画面继续；几何过渡期间允许数字越过计数条边界，避免被横向滚动容器裁断，并关闭按钮形状变化产生的短暂底色过渡；减少动态效果时即时切换。运行 `make build` 后执行 `node web/scripts/audit-overview-scroll.cjs`，使用隔离临时 Core 验证位置、方向阈值、搜索状态、偏好、桌面、深浅主题及减少动态效果。

On narrow screens, scrolling down compacts the existing four counts and search/preferences/enrollment controls into one row; scrolling up expands them. The row remains at the original count-card upper edge without pinning or programmatic scrolling. A matching bottom-space reserve preserves document height. The original controls and handlers are reused; editing and open preferences keep the expanded layout. Desktop remains unchanged. Local Anthropic/Claude source patterns informed the short, interruptible, non-overshooting transition; reduced motion skips animation.
