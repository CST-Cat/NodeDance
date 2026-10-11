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

`resource action`, `resource.action`, and `call resource.action` select the same operation. `--params`, `--query`, and `--body` accept literal JSON, `@file`, or `-` for stdin. Only one input may consume stdin. Passwords, credentials, registry secrets, and single-use terminal tickets should come from private files or stdin. For example, supply `{"password":"..."}` on stdin to `nodedance auth login --body -`; passwords need not enter shell history or process arguments.

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

Create an enrollment with `agents.enrollment` and a body containing `displayName` and optionally `coreUrl` (the URL reachable from the Agent). The response contains the token, expiry, and `installCommand`. The token is separate from the command. The generated installer resolves a published `agent-vMAJOR.MINOR.PATCH` tag, pins its downloads, verifies the release checksum, and installs the root Agent. NodeDance never installs or configures Tailscale.

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

The TUI uses the same catalog and client as CLI. Search with `/text`, move between pages with `n`/`p`, enter an operation number, and quit with `q`. Select `auth.login` first; password entry is hidden. Forms request path/query parameters, JSON bodies or private input files, and idempotency keys. Results paginate. Binary operations prompt for local filenames. Local lifecycle operations request native argument arrays (for example `["--data-dir","/var/lib/nodedance","--output","/private/core.tar"]`); Agent enrollment reads its token in a hidden prompt.

Select `terminal.open` for a real interactive host/container shell; it forwards keyboard input and terminal resizing. Press **Ctrl-]** to return to NodeDance. Other streams print JSONL and accept `/stop` to return. TUI input requires an actual terminal; LLMs should use the noninteractive CLI.

## Language and releases / 语言与发布

支持 English 和简体中文。Web 顶部切换语言；CLI 使用 `--lang en|zh-CN`，TUI 还可按 `l` 切换。Core 使用 `core-v*`，Agent 使用 `agent-v*` 独立发布，发布说明提供中英双语。

[语言约定](i18n.md) · [发布流程](releases.md) · [Komari 优化学习](komari-optimizations.md)
