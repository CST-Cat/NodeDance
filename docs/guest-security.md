# Guest browser security verification

Run `pnpm --dir web audit:guest-security`. The script builds an isolated real Core with a temporary database and a protocol-only simulated Agent, signs in a separate administrator browser context, publishes explicit aliases/fields, and enters guest mode through the login button in Chromium. Attack requests originate from that guest page. Production servers, real Agent installations and Docker resources are not targets.

The pnpm command is the existing project convenience entry, not a requirement of the security tests. With Web dependencies and Playwright Chromium already installed, an equivalent direct run from the repository root is:

```sh
cd web
node node_modules/vite/bin/vite.js build --config vite.config.ts --outDir ../internal/core/webassets/dist --emptyOutDir
NODEDANCE_GUEST_ATTACKS=1 node scripts/audit-guest.cjs
```

If current Web assets have already been built, run `NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs` from the repository root. The audit itself compiles the Core using Go and runs Chromium through Playwright.

The suite discovers private operations from the connected Core's operation contract rather than keeping another operation list. Responses, not hidden buttons, determine whether an attack succeeds. Administrator requests create the fixture and withdraw its resources; they are not counted as successful guest operations.

## Verified cases

The table below records a historical run; current operation and attack totals are discovered at runtime. The browser suite also checks persisted guest history points against the exact `[from, to)` timestamps. Go regressions cover fractional-second minute/hour boundaries for both full and selected-key history queries. Node detail regressions select tabs by accessible name, verify service-probe reload and browser Back, and ensure the full probe panel does not remain on overview or history.

| Group | Cases | Expected result |
| --- | ---: | --- |
| Discovered private operations, including commands/files/containers/updates/configuration | Discovered at runtime | 401 despite spoofed role/mode/user headers and an anonymous CSRF token |
| Internal and opaque public IDs substituted into private resource routes | 15 | 401 |
| Public scope expansion, arbitrary history, malformed queries and parameter pollution | 12 | 400 |
| Public method changes and method override | 10 | 405 |
| Private method changes, including HEAD and OPTIONS | 21 | 401 |
| Encoded paths, traversal, NUL, repository/config and host-file paths | 12 | 401 or 404; no private data |
| Forged session cookies | 5 | 401 |
| Forged Bearer, Enrollment and Basic credentials | 3 | 401 |
| Dashboard, logs, stats, terminal-ticket and Agent WebSockets | 5 | Handshake fails; connection never opens |
| Cross-origin browser form with a real administrator session | 1 | 403 |
| HTML injection in display title, node alias and service alias | 1 | Literal text; no injected image or script execution |
| Management request during guest refresh storm | 1 | 200 within two seconds |
| Ten waves of 24 concurrent public refresh requests | 240 | 200 or bounded 503; no unselected fields/private hostname |
| Warm-cache field withdrawal, probe deletion, Agent revocation, disable with unavailable selections, global disable | 5 | Updated public scope immediately; disabled display returns 404 |
| **Total** | **438** | **No failed cases after fixes** |

The same run checks default-disabled entry, absence of display requests before guest selection, identical public projections with administrator cookies, session validation when leaving guest mode, browser-mode spoof denial, administrator save/preview, a dedicated guest icon distinct from node inventory, and twenty light/dark/mobile layouts, including node cards and detail headings without visible internal UUIDs. The browser checks also cover Back/Forward and reload in guest mode, forged history markers, administrator-cookie guest URL restoration without private requests, a missing session on returning to management, and a delayed authenticated response arriving after a newer guest navigation. These existing checks are separate from the 438 attack cases. Counts grow when the Core operation contract grows.

## Findings and fixes

The initial run reproduced three failures: a deleted probe remained in the 15-second snapshot, a revoked node remained in that snapshot, and resource-existence validation prevented switching guest mode off while unavailable resources remained selected. These were publication withdrawal and disable problems; the private-operation matrix did not expose administrator access.

Public requests now recheck selected resource availability even when metric data is cached. The cache key includes the current available publication scope as well as the saved settings, and revoked nodes' service probes are also omitted. Database failures return an unavailable response instead of the old cached projection. Preview uses the same resource filter. Disabled drafts can retain unavailable selections, while enabling still requires available resources and valid fields. Raw query strings are rejected, including malformed queries that Go's parsed query map would omit.

Backend regression tests warm the cache, delete a probe or revoke its Agent through the actual stores, verify immediate removal and matching preview, verify that unavailable probes cannot be republished, and disable without clearing the saved selection. The browser attack run then repeats the original failures against HTTP and passes.

## Scope of the result

This is reproducible verification of the listed routes, inputs and browser flows, not proof that every possible vulnerability is absent. It does not measure distributed denial of service or test production network/TLS configuration. Previously delivered public data cannot be recalled, and requests already in flight when an administrator withdraws publication may finish with the scope they had already read.

Generated JSON results and layout screenshots live under `.build/guest-audit/` for inspection and can be removed after use. No credentials are included in the report.

Grant creation/revocation responses are held in the real browser while navigating to guest mode. Releasing them after host-management unmount must issue no private follow-up reads, including enrollment-grant listing.

真实浏览器在创建／撤销入网授权请求未返回时切入游客模式；退出主机管理页面后释放响应，必须不再触发私有后续读取，包括入网授权列表。

The contract includes paginated file search and passive preview; direct anonymous access remains denied. A real unavailable file-list response from the protocol-only simulated Agent is delayed across guest entry, and releasing it must start no private follow-up requests. 文件搜索／预览仍要求管理员权限；文件管理器退出进入游客后，迟到目录响应不得触发后续私有读取。

Archive format denial cases are derived from the connected `files.action.format` enum, including TAR and TAR.GZ. No case count is frozen in the scripts.

Management deployment polling stops after its view unmounts; a late SSH fingerprint response is bound to the peer it requested. The fresh-assets guest suite also checks first-load history canvas initialization without adding private guest data reads.

## 69 文件收尾审查验证

2026-10-13，使用新构建 Web 资产和隔离真实 Core/协议模拟 Agent，再次通过完整访客安全套件。公共投影、匿名边界和撤销语义保持原约束；当前 Core 终端/更新响应复用严格有界 JSON 解码。实际 WebSocket 回归确认尾随 JSON 不会作为终端输入转发，跨会话和撤销测试保留。详细结果及覆盖范围见 [69 文件报告](code-review-69-files.md)。

RSS 增量回归从连接的 Core 契约发现全部 feeds.* 私有操作，验证匿名拒绝；另创建含私密标记的停用订阅，检查管理员 cookie 与游客公开响应一致且不包含源名称或 URL 令牌，并核验删除。临时订阅不执行外部抓取。

Agent enrollment responses contain token-bearing native and Docker installation commands. These responses and reusable enrollment grants remain administrator-only; guest projections exclude tokens and installation commands even when administrator cookies are present. The isolated browser attack suite verifies anonymous enrollment denial and command/token exclusion from public display.

Web places node deletion at the bottom of Node settings and invokes the confirmed Core operation. Node deletion and pending-enrollment renewal are private Core operations and are covered by the discovered anonymous/forged-identity operation matrix. Renewed tokens and commands never enter the public projection. The browser suite warms publication, deletes a selected node, verifies immediate withdrawal and verifies that its enrollment credential is rejected.

内置更新使用相同的私有 `updates.*` 契约、管理员会话、CSRF 和节点权限检查。临时工作进程不新增公开更新入口，也不因游客访问而执行；游客浏览器矩阵继续逐操作验证拒绝边界。

Built-in updates retain the private `updates.*` contract, administrator session, CSRF and node checks. Transient workers add no public update endpoint and are not executed by guest entry; the browser matrix continues to discover and deny private operations.

终端默认连接信息仅从当前用户受保护的会话文件恢复；显式选择不同 Core 时拒绝携带旧会话，HTTP 仍仅允许已明确选择的开发模式与字面量回环地址。

Terminal defaults are restored only from the current user’s protected session file. An explicit different Core rejects the saved identity; HTTP remains restricted to explicit development selection and literal loopback addresses.

Node billing metadata (renewal price, currency, billing cycle, monthly quota, purchase date and VPS expiry), including values supplied during enrollment, remains administrator-only. Currency values are published in the Core operation contract as an enum shared by Web, CLI and TUI, and Core rejects asset bodies with values outside that enum; the browser attack suite keeps a forged-currency rejection case. Guest security regressions create an enrollment with distinct private billing markers, verify persistence through the administrator asset operation, and verify exclusion from public responses with guest and administrator cookies. Anonymous enrollment and asset operations remain covered through the discovered Core contract.

SSH discovery is administrator-only through `agents.diagnostics`. Host addresses, listener ports, account candidates and generated connection commands never enter the guest projection. Anonymous diagnostic requests remain denied through the discovered private operation matrix. Discovery observes service configuration and login metadata without collecting passwords, private keys or Agent credentials.

Administrator VPS sections use the contextual sidebar; returning home restores global navigation. The guest browser regression retains detail history, reload, section selection and administrator session checks through this sidebar. Guest display does not expose management navigation.

Compose project display names are administrator-only preferences. The isolated browser suite stores a private project-name canary and verifies that anonymous/public output does not contain it; Compose deletion remains behind the shared authenticated operation and CSRF boundary.

The browser guest regression opens a shared dropdown before guest entry and verifies immediate removal of its portaled UIArc dropdown menu. Custom controls keep the existing Core operation, opt-in policy and session boundaries.

The shared light/dark theme uses semantic surface, text, control and state roles documented in `DESIGN.md`. Visual flattening does not change operation discovery, public projection, authorization or task semantics. Browser audits use fresh assets and isolated Core/Agent resources; the theme regression checks real persisted/system switching and actual rendered contrast.

共享深浅主题使用 `DESIGN.md` 中的表面、文字、控件和状态语义。视觉扁平化不改变操作发现、公开投影、权限或任务行为。浏览器回归使用新构建资源与隔离 Core／Agent，主题回归检查实际保存／跟随系统切换及渲染对比度。

## Overview context privacy

The Core runtime host information (`dashboard.core-info`) and shared overview memo (`dashboard.memo`, `dashboard.save-memo`) are administrator-only operations. They are not projected into public node cards, including requests with administrator cookies. Forged `memo`/`core` query flags are rejected with HTTP 400. The security matrix discovers these operations from Core and checks anonymous/forged-identity denial. A distinct private memo marker is also checked against guest and administrator-cookie public responses.

React lifecycle verification: specialized pages use local Suspense boundaries, so entering the guest route unmounts private file views immediately. The elFinder instance and its Core transport are disposed together; pending reads are aborted and late component initialization is discarded. The browser suite retains late file responses and administrator-session exit checks against an isolated Core and protocol-only simulated Agent.

Shared select decoration uses a static self-hosted SVG background, not an image element in the public display. The stored-title/node/service injection case continues to require zero image nodes and zero executed payloads; the Figma theme refactor does not relax this assertion.

The sidebar account card is administrator-only. It opens upward and reuses the existing personal settings and sign-out actions; guest views never render account information or account controls. Browser regression checks verify Escape/focus recovery and real session invalidation after sign-out. 左下角账号卡片仅在管理界面显示，向上展开并复用个人设置与退出登录；游客页不显示账号信息或入口。浏览器回归验证 Esc 焦点恢复以及退出后的真实会话失效。

Multi-account operations (`accounts.*`, `auth.switch`, `profile.*`) remain private. Anonymous callers cannot enumerate accounts or access private avatars. Read-only accounts cannot change guest policy or shared branding. The browser suite checks account-menu navigation and real logout, and account lifecycle tests verify isolated sessions, role enforcement, password reset/disable withdrawal and the final-administrator guard. 多账号信息和私有头像不进入游客投影，只读账号不能修改公开策略或共享外观。

登录恢复：有效会话下再次登录使用该会话的 CSRF 校验，成功验证密码后轮换并撤销旧会话；失败保留原会话。Web 对提交前被拒绝的登录安全令牌仅刷新并重试一次，保留输入；来源拒绝、密码错误不重试。游客权限保持不变。隔离浏览器回归覆盖旧登录页、自动恢复、重试上限、旧会话撤销和伪造来源／令牌拒绝。

Login recovery validates the current session CSRF token when present, verifies the password and rotates the replaced session only on success. Web retries an explicitly rejected login security token once, retaining input; origin and password failures are not retried. Anonymous boundaries remain unchanged and are checked by the isolated guest security suite.

登录页密码框复用 UiPassword 与 UiIcon：点击眼睛显示 5 秒，再次点击、离开输入区域、提交、切到后台或窗口失焦时隐藏。显示开关不提交表单、不改变凭据内容或认证校验；访客安全浏览器脚本验证该交互及深浅色／宽窄屏布局。

Observability operations (`nodes.recording`, `nodes.incidents`, `nodes.analysis`, `nodes.correlations`) are authenticated viewer reads discovered from the Core contract; collection configuration remains an administrator operation. Diagnostic entities and probe measurement extensions are excluded from public DTOs. Anonymous method/path/query and forged-session checks discover these routes with every other operation. Node deletion cascades recordings, incidents, epoch and sample cursors; publication withdrawal continues to use the existing guest policy. / 诊断操作只允许已认证的只读用户或管理员读取，采集设置只允许管理员修改。诊断实体及探测扩展字段不进入公开 DTO；新增路由纳入按契约发现的匿名、伪造身份与方法路径绕过检查。删除节点同时删除录像、异常、采集周期状态和去重游标。

Diagnostic collection settings contain only the detail interval and process limits. The retained diagnostics remain private; simplifying the diagnostic catalog does not expand guest access. / 诊断采集设置仅包含明细周期与进程数量限制；保留的诊断指标仍为私有数据，指标目录精简不会扩大访客权限。

Five-second network/disk history stays private behind nodes.history; the anonymous browser suite explicitly verifies denial for resolution=5s. Guest history continues to use only its configured public minute/hour projection. / 网络与磁盘五秒历史仅通过私有 nodes.history 提供；匿名浏览器回归显式验证 resolution=5s 仍被拒绝。游客历史保持配置允许的公开分钟／小时投影。

File-manager disposal aborts its private manager before destroying elFinder. The vendored destroy path does not chain its event dispatcher, because failed initialization disables that dispatcher. The browser suite retains the unavailable-initial-directory → guest-entry regression and verifies no private follow-up reads after the transition.

文件管理卸载先中止私有管理器，再销毁 elFinder；内置销毁路径不再链式调用初始化失败后被禁用的事件分发器。浏览器套件保留初始目录不可用后进入游客页的回归，并校验切换后不再发起私有读取。

The discovered dashboard settings schema no longer includes a container preview limit. The full Docker inventory dialog remains administrator-only; anonymous boundaries are checked against the current Core operation contract. 仪表盘设置契约已移除容器预览上限；完整 Docker 清单弹窗仍仅供管理员使用，匿名边界继续根据当前 Core 操作契约验证。

本机 Core／Agent 更新由 root 独立执行，无需登录；公网更新 API 仍必须通过管理员认证和 CSRF。CLI/TUI 的 `persistent` 登录策略只在凭据验证成功后记录，不授予额外权限；伪造 User-Agent、会话策略或身份请求头不能访问管理 API。普通 Web 登录继续闲置过期，退出、撤销、账号停用和密码变更同样撤销永久会话。游客安全回归覆盖伪造策略以及实际永久会话的撤销。

页面语言自动跟随浏览器，不再使用顶部语言选择器。游客切换仍验证真实操作下拉浮层在进入公开页时立即移除；语言偏好不改变游客数据边界。

The narrow-screen stale-login regression verifies the authenticated administrator nodes screen and a successful real `auth.me` request, rather than requiring the account menu that the mobile design hides. CSRF retry counts and stale-session revocation assertions remain unchanged.

窄屏旧登录页回归检查管理员已认证的节点管理页和真实 `auth.me` 请求成功，不依赖移动设计隐藏的账号菜单。CSRF 重试次数和旧会话失效断言保持不变。

Domain/certificate operations are administrator-only and discovered by the anonymous/forged-identity operation matrix. Inventory labels, DNS provider metadata and tokens never enter public projections, even with administrator cookies. The isolated browser fixture stores distinctive domain/provider/token markers and asserts their absence from guest JSON. Certificate/private-key exports require administrator authentication, CSRF, confirmation and a matching revision; lists and jobs expose metadata only. `audit-domains.cjs` verifies the anonymous deep link performs no private reads and private PEM never enters browser storage.

The management component refactor retains the same guest policy operations and explicit resource opt-in. Installed Radix checkbox controls preserve boolean/array values and accessible names; changing presentation does not publish drafts. The browser regression now opens the real scoped Radix operation menu and verifies it disappears immediately on guest entry, while retaining all API, forged identity, withdrawal and administrator-cookie projection assertions.

The node display regression adds a private `country` metadata object to the
Core node-list contract and publishes the supported preference icon enum. Neither
country metadata nor administrator-selected logos enter the guest projection.
The guest browser audit explicitly rejects a `country` property in public nodes;
the existing anonymous-operation matrix discovers the updated Core contract.
Geolocation queries run only inside Core for public node IP literals, never from
browser/guest requests to a third-party origin.

手动国家 `countryCode` 与自动定位结果同属管理员节点展示偏好，不向游客投影；保存通过既有 `nodes.save-preference` 私有操作执行。

账号菜单已接入安装的 UIArc User Menu；切换账号在 UIArc Dialog 中继续调用 `auth.switch` 验证目标密码。游客安全回归保留私有菜单立即清除、账号信息不可见、Esc 焦点恢复和真实退出后的会话失效断言。

“总览全球”是私有管理视图，复用同一 Core 节点数据，不加入游客公开投影。浏览器回归验证未登录深链不读取节点，以及进入游客时清除地球、卡片和私有请求。

城市定位同属私有节点管理数据：`location`、城市、州／省、经纬度与手动覆盖不进入游客投影。浏览器安全回归保存私有城市记录并检查匿名和管理员 Cookie 下公开数据均不泄露；城市目录新操作从 Core 契约发现后纳入匿名拒绝矩阵。
