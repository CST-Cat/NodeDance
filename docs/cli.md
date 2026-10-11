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

CLI streams print the Core's messages as JSONL. Log and terminal byte data use base64. `terminal.open` obtains a single-use ticket and sends the open frame automatically. Its JSONL input may omit `streamId`; the client supplies it. Input frames include `{"action":"input","data":"bHMK"}` (`ls` followed by newline), `{"action":"resize","rows":40,"columns":120}`, or `{"action":"close"}`. For a container, use `targetKind: "container"` and a full `containerId`. `terminal.create`, `terminal.connect`, and `terminal.cancel` remain discoverable for clients that need to manage authorization separately.

## TUI

```sh
nodedance tui --server https://core.example --timeout 0
```

The TUI uses the same catalog and client as CLI. Search with `/text`, move between pages with `n`/`p`, enter an operation number, and quit with `q`. Select `auth.login` first and enter both the account username and the hidden password. Forms request path/query parameters, JSON bodies or private input files, and idempotency keys. Results paginate. Binary operations prompt for local filenames. Local lifecycle operations request native argument arrays (for example `["--data-dir","/var/lib/nodedance","--output","/private/core.tar"]`); Agent enrollment reads its token in a hidden prompt.

Select `terminal.open` for a real interactive host/container shell; it forwards keyboard input and terminal resizing. Press **Ctrl-]** to return to NodeDance. Other streams print JSONL and accept `/stop` to return. TUI input requires an actual terminal; LLMs should use the noninteractive CLI.

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
