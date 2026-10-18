# 账号管理 / Account management

Core 支持管理员和只读用户。首次启动只创建初始管理员，后续账号由管理员创建。新增、编辑、停用、删除和重置密码均通过 Core 操作完成，Web、CLI、TUI 共用 `/api/v1/operations` 发布的请求 schema、角色要求和执行入口。

Administrators manage accounts and remote resources. Read-only users can observe the node dashboard and metric history, use the allowed read operations, and maintain their own profile, avatar, password and sessions. They cannot configure shared appearance, publish guest data, access enrollment credentials, run commands or terminals, read remote files, change Docker resources, or manage other accounts. The contract's `requiredRole` is authoritative; Core rejects unauthorized requests regardless of presentation or forged client identity.

个人设置移除重复的左侧账号区域。点击侧栏左下角账号，向上展开账号矩阵卡片；当前账号有选中标记，停用账号不能切换。选择其他账号后必须输入目标账号密码。切换成功会轮换浏览器 Session 和 CSRF，并撤销被替换的 Session；密码错误保留原账号，不保存密码。卡片也提供个人设置和退出登录入口，支持 Esc、点击外部关闭、键盘操作，以及折叠侧栏和移动端导航。

账号管理提供新增、编辑登录名／显示名／角色、启用／停用、删除和重置密码。编辑、重置、停用、删除会撤销目标账号的所有 Session，并结束这些 Session 对应的终端和文件传输。个人修改密码只撤销自己的 Session。会话列表和撤销操作严格按当前账号隔离。不能删除当前账号，也不能停用或降级最后一个启用的管理员。

Shared sign-in background and public branding remain administrator-owned. Account names, themes and private avatars are account-specific. Public guest projections never include account lists, private profiles or sessions. New accounts do not change the guest publication policy.

## Shared operations

| Operation | Access | Behavior |
| --- | --- | --- |
| `accounts.list` | Signed in | Account selection cards; disabled accounts visible only to administrators |
| `accounts.create` | Administrator | Username, display name, role and initial password |
| `accounts.update` | Administrator | Complete editable account fields; `confirmed: true` required |
| `accounts.delete` | Administrator | Delete selected account; `confirmed: true` required |
| `accounts.reset-password` | Administrator | New password; `confirmed: true` required |
| `auth.switch` | Signed in | Verify target username/password, replace current browser Session |
| `profile.get`, `profile.update` | Signed in | Own display name and theme |
| `profile.avatar`, `profile.upload-avatar` | Signed in | Own private avatar download/upload |
| `auth.password`, `sessions.list`, `sessions.revoke` | Signed in | Own password and sessions |

CLI examples (passwords belong in private files or stdin):

```sh
nodedance call accounts.list
nodedance call accounts.create --body @/private/new-account.json
nodedance call accounts.update --params '{"id":"2"}' --body @/private/account-update.json
nodedance call accounts.reset-password --params '{"id":"2"}' --body @/private/password-reset.json
nodedance call auth.switch --body @/private/target-login.json
nodedance call profile.get
```

TUI discovers the same operations and secret fields from the connected Core. Web's Operations page also discovers these operations and filters administrator-only entries for read-only users. Specialized account cards and forms do not grant authority.

This development implementation uses the current multi-account storage schema directly. No retired single-account adapter or storage migration is added. Existing development preview data requiring the former schema must be backed up and reinitialized; do not point tests at production data.

会话设备名称以设备类型、系统和浏览器主版本显示；macOS／Windows 不根据可能被固定的 User-Agent 推测具体系统版本，不展示不可靠的手机型号。会话列表不展示原始 User-Agent 或详情入口；宽屏下 IP 位于设备名称右侧，窄屏下位于名称下方；保留最近活动时间、右侧当前按钮和撤销功能。这里只整理显示信息，不改变 Core 会话数据或权限。

Session titles summarize device type, OS and browser major version. Raw User-Agent text and detail controls are omitted from the session list; unknown agents use a neutral label. This is presentation only and does not change session data, permissions or revocation.

新增与编辑账号复用 UiDialog 弹窗，表单不再展开占据账号卡片；Esc／取消关闭后焦点回到触发按钮。角色选择、保存／删除确认与失败提示均在弹窗流程内完成，Core 操作和权限校验不变。

Account creation and editing reuse UiDialog modals, retaining Core validation, nested confirmations, error messages and native focus restoration.

## Verification

`pnpm --dir web audit:accounts` builds fresh assets and exercises a temporary real Core, account forms, confirmation cancellation, password-verified switching, failed-switch identity retention, disabled accounts, isolated profiles, real logout, and light/dark desktop/mobile/collapsed layouts. `pnpm --dir web audit:guest-security` retains the anonymous and adversarial boundary suite. Core integration tests cover cross-account Session denial, reset/disable revocation, own-password scope, and the last-enabled-administrator guard.
