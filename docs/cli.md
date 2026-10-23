# CLI and TUI

`nodedance` provides Core lifecycle commands, an administrator API client, and a terminal UI. Remote operations use `internal/control`: one operation catalog, one HTTP/WebSocket client, the same session and CSRF checks as Web, and the existing Core task/audit pipeline. No client reads the Core database or connects directly to an Agent. `nodedance agent ...` invokes the separately built `nodedance-agent` binary, found on PATH or beside `nodedance`.

## Discover and call operations

```sh
nodedance operations
nodedance describe containers.action
nodedance describe files.action
nodedance describe channels.create
```

The JSON catalog includes names, descriptions, HTTP paths/methods, path parameters, allowed query parameters, body schemas, transport kinds and idempotency requirements. Body schemas describe input shape; Core remains authoritative for enum values, bounds, revisions, confirmations and permissions. Local operation schemas describe a string array of native command arguments.

```sh
export NODEDANCE_SERVER=https://core.example
nodedance auth login --body @/private/login.json
nodedance nodes list
nodedance call nodes.metrics --params '{"nodeId":"NODE_UUID"}'
nodedance containers list --params '{"nodeId":"NODE_UUID"}'
```

`resource action`, `resource.action`, and `call resource.action` select the same operation. `--params`, `--query`, and `--body` accept literal JSON, `@file`, or `-` for stdin. Only one input may consume stdin. Passwords, credentials, registry secrets, and single-use terminal tickets should come from private files or stdin. For example, supply `{"username":"...","password":"..."}` on stdin to `nodedance auth login --body -`; passwords need not enter shell history or process arguments.

Finite remote calls print one JSON object:

```json
{"operation":"nodes.list","status":200,"data":{"nodes":[]}}
```

HTTP or transfer failures put a message in `error` and exit nonzero. Usage/connection errors are reported on stderr. Accepted task submission is not completion: inspect `tasks.get` until the Core reports a terminal task status. Local lifecycle commands retain their native output and exit status.

Connection flags are available after the operation:

| Flag / environment | Meaning |
| --- | --- |
| `--server` / `NODEDANCE_SERVER` | HTTPS Core origin, without path, credentials, query or fragment |
| `--origin` / `NODEDANCE_ORIGIN` | Configured public Core origin when the client reaches Core through a different address |
| `--state-dir` / `NODEDANCE_CLIENT_STATE_DIR` | Private client session directory; defaults to the user's config directory under `nodedance-client` |
| `--ca-file` | Additional private CA certificates; system trust remains enabled |
| `--dev` | Allow HTTP only for a literal loopback address |
| `--timeout` | Request/stream deadline; default `60s`, `0` disables it |

The client stores a server-bound session in an owner-owned `0700` directory and `0600` regular file, rejects symlink sessions, verifies TLS, and refuses HTTP redirects. Use different state directories for different Core servers. CLI/TUI request the discovered `persistent` session policy at login, setup and account switch. These sessions do not expire from inactivity, including after restarting the client; logout, revocation, account changes, disable and password changes still invalidate them. Browser sign-in retains its idle timeout. / CLI/TUI 登录态不设闲置过期，重启客户端后继续有效；退出、撤销、账号变更、停用和密码变更仍会使其失效。

Network discovery uses `nodedance discovery peers` or the TUI's discovered `discovery.peers` operation. Core runs natively on the controller and reads that host's existing Tailscale peers through its built-in read-only operation. It never installs or configures Tailscale; discovery is optional.

网络发现使用 `nodedance discovery peers` 或 TUI 自动发现的 `discovery.peers` 操作。Core 原生运行在主控机，通过内置只读操作读取该主机现有的 Tailscale 节点。发现功能可选；NodeDance 不安装或配置 Tailscale。

## Coverage

| Domain | Operations |
| --- | --- |
| Core | Serve, health, protocol descriptor, version, backup, restore |
| Authentication | Setup status, setup, login, current identity, password change, logout, sessions, revocation |
| Nodes/dashboard | Nodes, metrics, history, preferences, settings, live dashboard |
| Agents | Enrollment including pinned installer command, revoke, rotate; local enrollment, recovery, runtime, state/file-root validation and root systemd lifecycle |
| Docker | Inventory, inspect, create, start/stop/restart/pause/resume/rename/delete, rebuild plan/rebuild/cleanup, logs, stats |
| Images | Paginated listing, authenticated pull, deletion |
| Compose | Projects, create/deploy, config read/validate/save, start/stop/restart/delete |
| Files | List, stat, text read/save, mkdir, rename, confirmed delete, upload, download |
| Terminal | Host or container interactive shell, authorization, cancellation, JSONL connection |
| Probes | List, get, create, update, delete, run history |
| Alerts | Active/history/events, acknowledge, silence, rules/defaults, channels/test, maintenance windows, delivery history |
| Discovery | Existing Tailscale peers, SSH host key, deployment submit/list/status/manual resolution |
| Appearance | Display settings, avatar/background upload/download |

## Tasks and files

Supply a stable `--idempotency-key` for task operations. Reuse it for a retry of the exact same intent; a new key means a new intent. The client never silently generates a new key or retries mutations.

```sh
nodedance containers action \
  --params '{"nodeId":"NODE_UUID","containerId":"FULL_CONTAINER_ID"}' \
  --body '{"action":"restart"}' --idempotency-key restart-001
nodedance tasks get --params '{"nodeId":"NODE_UUID","taskId":"TASK_UUID"}'

nodedance files upload --params '{"nodeId":"NODE_UUID"}' \
  --query '{"path":"/srv/config.json"}' --input ./config.json \
  --file-version EXPECTED_VERSION --idempotency-key upload-001
nodedance files download --params '{"nodeId":"NODE_UUID"}' \
  --query '{"path":"/srv/config.json"}' --output ./downloaded-config.json
```

Downloads create new `0600` files, refuse existing destinations, and remove partial files on failure. Uploads use the existing transfer API. File mutations retain Agent traversal/symlink/protected-tree/credential-path checks. File deletion requires `deleteConfirmed: true` and an exact `confirmationPath`. Container/image deletion and rebuild retain their existing exact-target confirmation rules. Probe/rule/channel edits use the current revision.

Create an enrollment with `agents.enrollment` and a body containing `displayName` and optionally `coreUrl` (the URL reachable from the Agent). Choose `deploymentMethod: "native"` or `"docker"` in the shared JSON request, or pass CLI `--deployment-method native|docker`. The response contains the token, expiry, the selected `installCommand`, plus `nativeInstallCommand` and `dockerInstallCommand`. TUI provides node-name, reachable Core URL, and deployment-method prompts using the default declared by Core. Both installer commands contain the issued enrollment token, so the copied command installs and enrolls without an extra token prompt. They default to the latest stable published Agent at execution time without a hardcoded tag; Web displays expiry in Beijing time (UTC+8), and API timestamps remain ISO UTC.

```sh
nodedance agents enrollment --server https://core.example \
  --body '{"displayName":"My node","coreUrl":"https://core.example"}' \
  --deployment-method docker
# Use --deployment-method native for the direct/systemd installer.
nodedance describe agents.enrollment
```

 The generated installer resolves the newest stable published `agent-vMAJOR.MINOR.PATCH` release at execution time, verifies the release checksum, and installs the root Agent. NodeDance never installs or configures Tailscale.

## Streams and terminals

```sh
nodedance dashboard watch --timeout 0
nodedance containers logs --timeout 0 \
  --query '{"nodeId":"NODE_UUID","containerId":"FULL_CONTAINER_ID","tail":"200"}'
nodedance containers stats --timeout 30s \
  --query '{"nodeId":"NODE_UUID","containerId":"FULL_CONTAINER_ID"}'
nodedance terminal open --timeout 0 \
  --params '{"nodeId":"NODE_UUID"}' --body '{"targetKind":"host"}' --input -
```

CLI streams print the Core's messages as JSONL. Log and terminal byte data use base64. `terminal.open` obtains a single-use ticket; Core opens the shell once and the client sets its initial dimensions. Its JSONL input may omit `streamId`; the client supplies it. Input frames include `{"action":"input","data":"bHMK"}` (`ls` followed by newline), `{"action":"resize","rows":40,"columns":120}`, or `{"action":"close"}`. For a container, use `targetKind: "container"` and a full `containerId`. `terminal.create`, `terminal.connect`, `terminal.resume`, and `terminal.cancel` remain discoverable for clients that need to manage authorization separately.

## TUI

```sh
nodedance tui --server https://core.example --timeout 0
```

The TUI uses the same catalog and client as CLI. Search with `/text`, move between pages with `n`/`p`, enter an operation number, and quit with `q`. Select `auth.login` first and enter both the account username and the hidden password. Forms request path/query parameters, JSON bodies or private input files, and idempotency keys. Results paginate. Binary operations prompt for local filenames. Local lifecycle operations request native argument arrays (for example `["--data-dir","/var/lib/nodedance","--output","/private/core.tar"]`); Agent enrollment reads its token in a hidden prompt.

Select `terminal.open` for a real interactive host/container shell; it forwards keyboard input and terminal resizing. Press **Ctrl-]** to return to NodeDance. Other streams print JSONL and accept `/stop` to return. TUI input requires an actual terminal; LLMs should use the noninteractive CLI.

## Terminal recovery / 终端恢复

Agent 内置完整上游 tmux 3.7c（Linux amd64、arm64 静态构建），无需在主机或容器安装 tmux。Web、CLI 的 `terminal.open` 与 TUI 使用同一 Core 操作契约：连接断开时分离 tmux 客户端，重连时附着原会话，由 tmux 重绘终端画面。Core 每 5 秒发送 WebSocket ping；客户端重连期间暂停输入，不重放命令。总览中的终端保持弹窗，点击 VPS 名称进入详情。

Agent embeds complete upstream tmux 3.7c static builds for Linux amd64 and arm64; no host or container installation is required. Web, CLI `terminal.open` and TUI share the Core operation contract: transport loss detaches the tmux client, and reconnecting attaches the original session and lets tmux repaint the screen. Core sends WebSocket pings every five seconds. Input pauses during recovery and is never replayed. The overview terminal remains a modal; clicking a VPS name opens its details.

主机 shell 和 Docker Exec 终端均由 Agent 的独立 tmux 服务器管理。Docker 通过 root-only Agent 子进程桥接现有 Docker API，不安装 Docker CLI、不修改目标容器。完整 tmux 的窗口、分屏和复制模式保留；主机终端可直接调用内置 `tmux`。NodeDance 会在 tmux 客户端单独退出后自动重新附着；结束会话请使用 Web 的关闭按钮或 CLI/TUI 的 **Ctrl-]**。

Host shells and Docker Exec terminals run under dedicated Agent tmux servers. A root-only Agent child bridges the existing Docker API without installing Docker CLI or modifying the target container. Complete tmux window, pane and copy-mode functionality is retained; managed host shells can invoke the embedded `tmux` directly. NodeDance automatically reattaches if the tmux client alone exits. To end the managed session, use Web close or **Ctrl-]** in CLI/TUI.

每个节点最多两个终端；分离的会话仍占用名额并阻止 Agent 更新。30 分钟无输入策略、登录授权检查和审计继续生效。主动关闭终端、CLI/TUI 返回、注销、撤销权限、Core/Agent 停止或 Agent–Core 连接换代都会结束会话。这不是主机重启后的进程恢复。

Each node permits two terminals; detached sessions occupy a slot and block Agent updates. The 30-minute idle policy, login authorization checks and auditing remain in force. Explicit close, leaving CLI/TUI, logout, revocation, Core/Agent shutdown or an Agent–Core connection generation change end the managed session. Host reboot does not restore processes.

`terminal.resume` 接收空对象 `{}`（也可省略请求体），路径参数为 `nodeId`、`streamId`。Core 校验同一登录会话、节点和 Agent 连接后返回新的单次票据；调用 `terminal.connect` 即可重新附着，客户端不得发送 `open`、`attach` 或 `detach`。`terminal.cancel` 结束服务器及其所有窗口。恢复不使用输出序号、缓存或补发。

`terminal.resume` accepts `{}` or an omitted body with `nodeId` and `streamId` path parameters. After verifying the same login session, node and Agent connection, Core returns a fresh single-use ticket for `terminal.connect`. Clients must not send `open`, `attach` or `detach`. `terminal.cancel` ends the server and all its windows. Recovery uses no output cursors, history cache or replay.

构建与许可证见 [内置 tmux](bundled-tmux.md)。`nodedance-agent --tmux-version` 显示固定版本。原生 Agent、Docker 镜像、安装器和更新器使用同一个嵌入构建，不需要额外下载运行时依赖。

See [bundled tmux](bundled-tmux.md) for provenance and licenses. `nodedance-agent --tmux-version` reports the pinned version. Native Agent, Docker image, installer and built-in updates use the same embedded build without additional runtime downloads.

## Language and releases / 语言与发布

支持 English 和简体中文。Web 顶部切换语言；CLI 使用 `--lang en|zh-CN`，TUI 还可按 `l` 切换。Core 使用 `core-v*`，Agent 使用 `agent-v*` 独立发布，发布说明提供中英双语。

[语言约定](i18n.md) · [发布流程](releases.md) · [Komari 优化学习](komari-optimizations.md)

## First deployment

An interactive `nodedance serve` on a new data directory prompts for the administrator username and a hidden password. Press Enter at either prompt to generate that field securely. Without a terminal, Core generates both fields automatically. Read the private `initial-admin.json` file at the path printed by Core and sign in with both fields; Core removes the file after the first successful login. Initialized data directories retain their existing account on restart.

For an API setup against an uninitialized Core, `auth.setup` requires the one-time setup credential. `username` and `password` are optional; omitted or empty values generate random credentials. The setup response returns a generated username and, when generated, the password in `initialCredentials`; save these before discarding the response. `displayName` is optional and is separate from the login username.

## Shared Core operation contract / 共享 Core 操作契约

CLI and TUI are two presentations of the same terminal product. Their remote operations come from the connected Core; local maintenance and the update workflow come from one shared terminal registry. TUI remains usable for local maintenance when Core discovery fails; press `r` to retry discovery. Both use the same finite executor, download cleanup, completion observer, stream client and interactive terminal. Web may add graphical interactions, while shared management behavior stays in Core. See the [generated capability matrix](interface-coverage.md).

CLI 与 TUI 是同一终端产品的两种入口。远程操作读取已连接 Core 的契约，本地维护和更新流程使用同一份终端注册表。Core 不可连接时，TUI 仍可进行本地维护，按 `r` 可重新发现。两端共用有限请求、下载清理、任务观察、流与交互终端实现。Web 可增加图形化交互，共享管理行为仍由 Core 统一执行。参见[自动生成的能力对照表](interface-coverage.md)。

The contract includes semantic request headers, multipart field names and upload limits, request byte limits, sensitive-field markers, response shape, and optional completion references. `required` means a JSON property is present; an empty string is valid unless a field constraint or Core business rule rejects it. `omitempty` is an output encoding choice, not input presence. Shared request DTOs and the Core structural validator prevent separately maintained request shapes. Stateful validation, permissions, confirmations and Agent file protections remain server-side.

契约包含语义请求头、multipart 字段名和上传限制、请求大小、敏感字段、响应形状与任务完成观察引用。`required` 表示属性存在，并不自动禁止空字符串；`omitempty` 只描述输出编码。Core 请求解码与契约共用请求类型和结构校验。依赖状态的校验、权限、确认和 Agent 文件保护继续由服务器执行。

### Observation, headers and terminal input / 观察、请求头与终端输入

```sh
nodedance call containers.create --params @node.json --body @container.json \
  --idempotency-key SAME_INTENT_KEY --wait --timeout 0
nodedance call files.upload --params @node.json --query @path.json \
  --input ./config.txt --file-version OBSERVED_VERSION --idempotency-key SAME_INTENT_KEY
nodedance call terminal.open --params @node.json --body '{"targetKind":"host"}' \
  --interactive --timeout 0
```

`--headers` accepts JSON, `@file`, or stdin for headers declared by the Core contract. `--file-version` is the convenient spelling for `X-File-Version`; use one value when specifying both forms. Upload field names and limits are discovered, including custom multipart fields.

For operations with a `completion` contract, `--wait` prints acceptance first, observes the discovered read operation, and prints the final result. Failed, canceled, timed-out and unknown outcomes exit nonzero. Interrupting observation does not cancel or resubmit the task; use the discovered cancellation operation to cancel work explicitly. TUI offers the same observer and `/stop`; it also exposes the shared `client.update` workflow with native argument arrays. TUI task results and observations retain the same operation identifiers as CLI.

`--interactive` uses the same keyboard/resize/reattachment path as TUI. Without it, terminal input/output stays JSONL for automation. TUI offers interactive or JSONL mode, including an optional JSONL input file, and exposes `terminal.create/connect/resume/cancel` for explicit ticket control. Both support all low-level terminal operations. The initial JSON body can express all target options in either interface.

Web Operations supports declared headers, version conditions, upload metadata, hidden sensitive JSON, and an optional completion observer. The JSON-body option can express explicit `null` and nested values; large integer tokens are preserved. Uncertain retries retain an intent key; copy it before leaving the page. “New execution key” deliberately starts a new intent. Changing the request or selected file creates a different intent. Stopping observation leaves accepted work running. Closing a managed terminal cancels that terminal.

带 `completion` 的操作可使用 `--wait`：先输出提交结果，再观察 Core 指定的读取操作，最后输出结果；失败、取消、超时与未知结果均返回非零退出码。停止观察不会取消或重放任务，取消请调用对应操作。TUI 使用相同观察器和 `/stop`，并提供共用的 `client.update` 流程。`--interactive` 与 TUI 共用键盘、尺寸和重连逻辑，未指定时保留 JSONL 自动化输入输出。

Web 操作中心支持契约声明的请求头、版本条件、上传约定、隐藏敏感 JSON 与任务观察；JSON 模式能传入明确的 `null`，并保留大整数原始值。未知结果重试保留同一键，离开页面前请复制。“新建执行键”明确开启新意图，改变请求或所选文件也会改变意图。停止观察不会停止任务，关闭托管终端会结束该终端。

### Verify synchronization / 验证同步

`make check` regenerates neither code nor documentation: it checks the committed capability matrix against the shared catalogue and runs the Go/Web checks. `pnpm --dir web audit:interfaces` builds an isolated Core and tests actual browser behavior without Agents or privileged services. `audit:design` is the separate light/dark/mobile fixture matrix. Existing rootful Docker/Agent/update tests remain opt-in; they are not implied by browser or catalogue coverage.

The connected Core publishes `/api/v1/operations`. CLI calls and TUI menus discover their operations, schemas, routes, and transports from this endpoint. Web uses the same contract for every request, stream, terminal, upload, and download; its Operations page generates forms from the catalog. Adding a Core operation makes it immediately available in all three generic interfaces without updating client operation lists. Core and Agent remain responsible for validation, authorization, auditing, and destructive-action confirmation. Specialized pages only customize presentation. `nodedance operations` without a server provides offline inspection of the contract compiled into that Core binary; use `--server` to inspect the running Core.

连接的 Core 发布 `/api/v1/operations`，CLI 调用与 TUI 菜单从该入口读取操作、schema、路由及传输类型。Web 的请求、流、终端、上传和下载使用同一契约；操作中心依据目录自动生成表单。Core 增加操作后，三端通用入口自动提供，无需分别维护客户端操作清单。验证、授权、审计和危险操作确认仍由 Core 与 Agent 处理，专用页面只定制展示。未指定服务器的 `nodedance operations` 用于离线查看当前 Core 二进制内的契约；使用 `--server` 查看运行中 Core 的实际契约。

正式更新 / Updates: [Web、CLI、TUI 更新、回滚与控制服务配置](updates.md).

## Agent monitoring and fleet operations

The connected Core now exposes `agents.settings/configure/diagnostics`, `commands.submit/list/get/cancel/cleanup`, `commands.batch/batches/batch-status/batch-cancel`, `probes.batch/quality`, `metrics.catalog`, `nodes.asset/save-asset` and `agents.create-grant/grants/revoke-grant`. They appear automatically in the CLI catalog, TUI operation browser and Web operation console. Use `nodedance describe OPERATION` for the current request schema. Root scripts require `confirm:true` and an idempotency key; accepted commands must be polled to completion. Reuse the same key after an uncertain response to avoid repeating execution. The Web Host management screen adds forms for the same operations.

See [Agent capabilities](agent-capabilities.md) for runnable examples, shared registration, ICMP quality, billing traffic, GPU requirements and root state protections.

### Exact inputs and recovery / 精确输入与恢复

Integer request and response fields use their published native bounds. Web forms retain int64/uint64 values exactly, including `18446744073709551615`; request headers publish byte limits, including the Agent command/update key range of 8–128 bytes. CLI `--input -` reads uploads directly from stdin, and both CLI and TUI accept regular files or FIFO paths. In TUI, an upload input of `-` prompts for a JSON string or `@file`; use a file/FIFO for arbitrary binary content. Both presentations use the same executor and server transfer limits.

Web Operations records pending observations and uncertain request keys in tab session storage. Refreshing can resume the discovered read operation or restore the original key. It stores no request bodies, uploaded files or credentials; restoring a key requires re-entering the original input. Resuming observation never submits the task again. Closing the tab clears these records; remote job/batch status remains accessible through the Core operation contract.

整数按 Core 公布的原生范围验证，Web 保留 int64／uint64 的完整精度。CLI 上传支持 `--input -` 标准输入，CLI／TUI 都支持普通文件与 FIFO。TUI 输入 `-` 后可提供 JSON 字符串或 `@file`；任意二进制内容使用文件／FIFO。Web 操作台在标签页会话中保留待观察任务和不确定请求的原始键，不保存请求正文、文件或凭据；刷新后可恢复键或继续读取任务状态，观察不会重新提交。命令历史默认保留 90 天，可通过 Agent 设置调整；过期后再次使用旧键可能重新执行，因此重试应在保留窗口内使用原始键和相同输入。

Guest display is configured with the discovered `guest.settings/configure/preview` operations in CLI and TUI. Anonymous `guest.entry/display` read only the enabled flag and explicitly published projection. See [configuration, limits and behavior](guest-display.md).

File management supports pagination/search, no-overwrite copy/move, ZIP archive/extraction and versioned edits through the same discovered `files.*` and `tasks.*` operations in CLI/TUI. See [full file manager](file-manager.md) for fields, bounds and outcome semantics. 文件分页／搜索、复制移动、ZIP 压缩解压与版本化编辑由 CLI／TUI／Web 共用契约；字段、限制与结果语义见文件管理文档。

File editing supports up to 8 MiB UTF-8 text. Body input and text response bounds come from the connected operation contract, including JSON escaping overhead; CLI and TUI share the same bounded input reader and executor. `files.action.format` offers ZIP/TAR/TAR.GZ creation, while extraction also supports TAR.BZ2; see [file management](file-manager.md).

## RSS subscriptions / RSS 订阅

`feeds.*` operations are discovered from Core and available through CLI, TUI and Web. Refresh intervals accept integer minutes from 1 through 10080, default 30. `feeds.refresh --wait` uses the shared completion observer; `feeds.cancel` stops queued/running refresh. See [subscription inputs, notifications and examples](rss.md).

Node lifecycle is shared with Web: discover `nodes.renew-enrollment` to regenerate native/Docker commands for an existing pending node (body: `coreUrl`, `deploymentMethod`), and `nodes.delete` to remove any node state (body: `confirm: true`). Both use `nodeId` as a path parameter and are available in CLI/TUI generic operation forms. Renewal invalidates previous tokens; deletion does not uninstall Agent or stop already dispatched remote work.

`nodedance update` defaults to updating the local Core as root without login; `nodedance agent update` independently updates the local managed Agent as root without login. Explicit remote update targets still require authentication. Neither needs a separate update program, registration step or additional update parameters. / 两个默认更新命令均无需独立更新程序、登记步骤或额外更新参数。

Agent enrollment supports an optional `asset` object with monthly traffic quota (`trafficLimitBytes`), purchase date (`purchasedAt`, calendar date at UTC midnight), VPS expiry (`expiresAt`, ISO UTC), renewal price/currency, and `billingCycleUnit` (`year`, `month`, `hour`, `minute`, or custom `day` with `billingCycleDays`). Read the connected `agents.enrollment` / `nodes.renew-enrollment` schema; Core validates and saves the node, credential and asset atomically. Use `nodes.asset` and `nodes.save-asset` for later changes, including while the Agent is offline. A zero quota removes the limit; a null expiry clears the date. The shared discovered CLI and TUI workflows expose all these inputs.

`nodedance call agents.diagnostics --params '{"nodeId":"NODE_UUID"}'` reports an `ssh` observation and ready-to-copy command, using the same root Agent discovery and optional saved overrides as Web/TUI. The machine provides listening ports, reachable interface addresses, allowed account candidates and current SSH sessions; no conversation memory or manually entered endpoint is required. With ambiguous users, `commands` contains one complete command per detected account; the default command follows native SSH client username rules.

Compose deletion is available in CLI and TUI through the discovered `compose.action` operation: action `delete` requires `deleteConfirmationKey` equal to the selected `projectKey`. It removes the deployment and preserves volumes/configuration. Project display aliases use `nodes.save-preference` with `targetKind: compose_project`, `identity: compose_project:PROJECT_KEY` and `alias`; read them with `nodes.preferences`. Both terminal interfaces use the same discovered request schema and Core validation as Web.

### Overview context and memo

Discover `dashboard.core-info`, `dashboard.memo` and `dashboard.save-memo` from the connected Core. `nodedance call dashboard.core-info` reads the Core runtime host information. `nodedance call dashboard.memo` returns the shared memo and revision; save with `nodedance call dashboard.save-memo --body '{"content":"Maintenance plan","revision":1}'`, using the current revision returned by Core. TUI exposes the identical discovered operations and shared request workflow. A stale revision conflicts instead of overwriting another device; the Core validates UTF-8 content and its 8192-byte limit. [Overview behavior](overview.md).

## Accounts and roles

Account creation, editing, disabling, deletion, password reset and switching are shared Core operations; see [Account management](accounts.md). CLI and TUI discover `accounts.*`, `auth.switch` and `profile.*` from the connected Core. Password inputs use private files or stdin. Switching validates target credentials and rotates the saved server-bound session. Read-only users receive Core-enforced permissions; their own profile, password and Session management remain available.

## Domains, certificates and JSONL batch / 域名证书与批量操作

`domains.*` and `certificates.*` are administrator operations discovered from Core, including inventory, imports/private exports, ACME accounts/DNS providers, issuance/renewal/revocation, policy and persistent job observation/cancellation/retry. CLI and TUI use the shared executor and completion contract. `nodedance batch --input FILE_OR_- --dry-run` validates all JSONL entries before execution; remove `--dry-run` to execute sequentially, optionally using `--continue-on-error`. TUI exposes the same workflow as `client.batch`. See [configuration, security boundaries and copyable JSONL examples](domain-certificates.md).

城市定位使用 `locations.search/get`、`nodes.list/preferences/save-preference`，CLI 和 TUI 均从 Core 发现同一契约。支持默认自动、手动城市／坐标、清除覆盖、观察结果与 JSONL 批量操作；详见 [节点城市定位与完整示例](node-locations.md)。

Ordinary CLI/TUI requests no longer run the published body schema before dispatch or validate successful JSON against the response schema again. Core returns structural and business errors through the actual HTTP response. The shared transport still checks targets, headers/request identity, JSON response syntax, response bounds and private session storage. JSONL batch preflight remains: it checks every entry before the first mutation to prevent partial execution caused by a later malformed entry.

CLI/TUI only atomically persist a session when its cookies or connection settings change. Ordinary reads and status observation reuse unchanged saved state without a filesystem write or fsync. New clients still validate owner-only state before loading; changed state still requires an owner-owned 0700 directory and is written as a 0600 regular file. Concurrent saves are serialized and failed writes do not update the saved-state digest.

Terminal JSONL processing parses input objects and compacts output frames without a separate full JSON-validity scan first. Malformed frames are still rejected before output, and large JSON integer literals retain their original digits.

Shared enrollment grants validate their prefix/use/lifetime input once in the Agent repository. The HTTP handler maps invalid input to 400 without issuing a grant; use counts, expiry, revocation and atomic consumption remain unchanged.

### Monitoring history / 监控历史

`monitoring.settings` returns the shared maximum retention and supported day choices. Administrators use `monitoring.configure` with `{"retentionDays":7}` to save it; Web personal settings and both terminal interfaces use the same Core operation. The default remains 365 days; choices are 1, 7, 30, 90, 180 and 365 days. Saving immediately expires older monitoring samples in the same database transaction. Increasing the duration cannot restore expired samples. Five-second rates retain at most 24 hours, minute aggregates at most 30 days, and hourly aggregates at most the selected duration. Incident recordings also respect the maximum while retaining their existing shorter limits.

`nodes.history` accepts `nodeId`, RFC3339 `from`/`to`, and `resolution` (`5s`, `minute`, `hour`). Its JSON output is the same data shown in the node history screen; terminal output can be redirected to a file. Web adds paginated sample details and CSV/JSON exports of the complete currently loaded response, including all metrics and pages. Exports preserve timestamps, sample counts, averages, minima and maxima; missing samples remain absent.

个人设置的“监控数据”可选择最长保存时间，默认 365 天。缩短后立即清理超期采样，延长不会恢复已清理的数据；秒级、分钟级和事件录像继续遵循各自更短的保留上限。节点历史页可查看分页明细，并导出当前查询的全部指标为 CSV 或 JSON。CLI／TUI 通过相同的 Core 操作设置时长、读取历史；不接触 Agent 凭据或 Core 数据库。
