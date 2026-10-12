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

The client stores a server-bound session in an owner-owned `0700` directory and `0600` regular file, rejects symlink sessions, verifies TLS, and refuses HTTP redirects. Use different state directories for different Core servers. Session expiration, revocation, administrator lockouts and audit rules remain enforced by Core.

Network discovery uses `nodedance discovery peers` or the TUI's discovered `discovery.peers` operation. A supervised Core reads existing host Tailscale peers through its protected deployment service, including Docker deployments without a Tailscale socket mount. Unsupervised Core reads its own environment. Both use the same Core discovery and enrollment rules as Web; NodeDance never installs or configures Tailscale.

网络发现使用 `nodedance discovery peers` 或 TUI 自动发现的 `discovery.peers` 操作。已配置宿主机控制服务的 Core 读取宿主机已有 Tailscale 节点，Docker 部署无需额外挂载 Tailscale socket；未配置时读取 Core 自身环境。CLI、TUI、Web 共用发现与注册规则，NodeDance 不安装或配置 Tailscale。

## Coverage

| Domain | Operations |
| --- | --- |
| Core | Serve, health, protocol descriptor, version, backup, restore |
| Authentication | Setup status, setup, login, current identity, password change, logout, sessions, revocation |
| Nodes/dashboard | Nodes, metrics, history, preferences, settings, live dashboard |
| Agents | Enrollment including pinned installer command, revoke, rotate; local enrollment, recovery, runtime, state/file-root validation and root systemd lifecycle |
| Docker | Inventory, inspect, create, start/stop/restart/pause/resume/rename/delete, rebuild plan/rebuild/cleanup, logs, stats |
| Images | Paginated listing, authenticated pull, deletion |
| Compose | Projects, create/deploy, config read/validate/save, start/stop/restart |
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

Create an enrollment with `agents.enrollment` and a body containing `displayName` and optionally `coreUrl` (the URL reachable from the Agent). Choose `deploymentMethod: "native"` or `"docker"` in the shared JSON request, or pass CLI `--deployment-method native|docker`. The response contains the token, expiry, the selected `installCommand`, plus `nativeInstallCommand` and `dockerInstallCommand`. TUI provides node-name, reachable Core URL, and deployment-method prompts (Enter defaults to Docker). The token is separate from the command.

```sh
nodedance agents enrollment --server https://core.example \
  --body '{"displayName":"My node","coreUrl":"https://core.example"}' \
  --deployment-method docker
# Use --deployment-method native for the direct/systemd installer.
nodedance describe agents.enrollment
```

 The generated installer resolves a published `agent-vMAJOR.MINOR.PATCH` tag, pins its downloads, verifies the release checksum, and installs the root Agent. NodeDance never installs or configures Tailscale.

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

See [bundled tmux](bundled-tmux.md) for provenance and licenses. `nodedance-agent --tmux-version` reports the pinned version. Native Agent, Docker image, installer and updater use the same embedded build without additional runtime downloads.

## Language and releases / 语言与发布

支持 English 和简体中文。Web 顶部切换语言；CLI 使用 `--lang en|zh-CN`，TUI 还可按 `l` 切换。Core 使用 `core-v*`，Agent 使用 `agent-v*` 独立发布，发布说明提供中英双语。

[语言约定](i18n.md) · [发布流程](releases.md) · [Komari 优化学习](komari-optimizations.md)

## First deployment

An interactive `nodedance serve` on a new data directory prompts for the administrator username and a hidden password. Press Enter at either prompt to generate that field securely. Without a terminal, Core generates both fields automatically. Read the private `initial-admin.json` file at the path printed by Core and sign in with both fields; Core removes the file after the first successful login. Initialized data directories retain their existing account on restart.

For an API setup against an uninitialized Core, `auth.setup` requires the one-time setup credential. `username` and `password` are optional; omitted or empty values generate random credentials. The setup response returns a generated username and, when generated, the password in `initialCredentials`; save these before discarding the response. `displayName` is optional and is separate from the login username.

## Shared Core operation contract / 共享 Core 操作契约

The connected Core publishes `/api/v1/operations`. CLI calls and TUI menus discover their operations, schemas, routes, and transports from this endpoint. Web uses the same contract for every request, stream, terminal, upload, and download; its Operations page generates forms from the catalog. Adding a Core operation makes it immediately available in all three generic interfaces without updating client operation lists. Core and Agent remain responsible for validation, authorization, auditing, and destructive-action confirmation. Specialized pages only customize presentation. `nodedance operations` without a server provides offline inspection of the contract compiled into that Core binary; use `--server` to inspect the running Core.

连接的 Core 发布 `/api/v1/operations`，CLI 调用与 TUI 菜单从该入口读取操作、schema、路由及传输类型。Web 的请求、流、终端、上传和下载使用同一契约；操作中心依据目录自动生成表单。Core 增加操作后，三端通用入口自动提供，无需分别维护客户端操作清单。验证、授权、审计和危险操作确认仍由 Core 与 Agent 处理，专用页面只定制展示。未指定服务器的 `nodedance operations` 用于离线查看当前 Core 二进制内的契约；使用 `--server` 查看运行中 Core 的实际契约。

正式更新 / Updates: [Web、CLI、TUI 更新、回滚与控制服务配置](updates.md).

## Agent monitoring and fleet operations

The connected Core now exposes `agents.settings/configure/diagnostics`, `commands.submit/list/get/cancel`, `commands.batch/batches/batch-status/batch-cancel`, `probes.batch/quality`, `metrics.catalog`, `nodes.asset/save-asset` and `agents.create-grant/grants/revoke-grant`. They appear automatically in the CLI catalog, TUI operation browser and Web operation console. Use `nodedance describe OPERATION` for the current request schema. Root scripts require `confirm:true` and an idempotency key; accepted commands must be polled to completion. Reuse the same key after an uncertain response to avoid repeating execution. The Web Host management screen adds forms for the same operations.

See [Agent capabilities](agent-capabilities.md) for runnable examples, shared registration, ICMP quality, billing traffic, GPU requirements and root state protections.
