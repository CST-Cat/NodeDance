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
