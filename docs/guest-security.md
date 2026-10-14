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
| Discovered private operations, including commands/files/containers/updates/configuration | 109 | 401 despite spoofed role/mode/user headers and an anonymous CSRF token |
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

Node billing metadata (renewal price, currency, billing cycle, monthly quota, purchase date and VPS expiry), including values supplied during enrollment, remains administrator-only. Guest security regressions create an enrollment with distinct private billing markers, verify persistence through the administrator asset operation, and verify exclusion from public responses with guest and administrator cookies. Anonymous enrollment and asset operations remain covered through the discovered Core contract.

SSH discovery is administrator-only through `agents.diagnostics`. Host addresses, listener ports, account candidates and generated connection commands never enter the guest projection. Anonymous diagnostic requests remain denied through the discovered private operation matrix. Discovery observes service configuration and login metadata without collecting passwords, private keys or Agent credentials.

Administrator VPS sections use the contextual sidebar; returning home restores global navigation. The guest browser regression retains detail history, reload, section selection and administrator session checks through this sidebar. Guest display does not expose management navigation.

Compose project display names are administrator-only preferences. The isolated browser suite stores a private project-name canary and verifies that anonymous/public output does not contain it; Compose deletion remains behind the shared authenticated operation and CSRF boundary.

The browser guest regression opens a shared dropdown before guest entry and verifies immediate removal of its teleported listbox. Custom controls keep the existing Core operation, opt-in policy and session boundaries.

The shared light/dark theme uses semantic surface, text, control and state roles documented in `DESIGN.md`. Visual flattening does not change operation discovery, public projection, authorization or task semantics. Browser audits use fresh assets and isolated Core/Agent resources; the theme regression checks real persisted/system switching and actual rendered contrast.

共享深浅主题使用 `DESIGN.md` 中的表面、文字、控件和状态语义。视觉扁平化不改变操作发现、公开投影、权限或任务行为。浏览器回归使用新构建资源与隔离 Core／Agent，主题回归检查实际保存／跟随系统切换及渲染对比度。
