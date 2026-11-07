# Detailed setup and operations / 详细设置与运维

[简体中文 README](../README.md) · [English overview](../README_EN.md)

NodeDance is a self-hosted Go Core and per-host Go Agent for managing Linux servers. The Web console provides administrator sign-in, multi-node host metrics, Docker inventory and control, Compose management, terminal and file access, service probes, alerts, and optional Tailscale-assisted Agent enrollment.

NodeDance 是一个自托管的 Go Core 与每主机一个的 Go Agent，用于管理 Linux 服务器。Web 控制台提供管理员登录、多节点主机指标、Docker 清单与控制、Compose 管理、终端与文件访问、服务探测、告警，以及可选的 Tailscale 辅助 Agent 注册。

The Core owns administrator identity, Agent enrollment, task records, history, preferences, and SQLite data. Each Agent opens an outbound authenticated WebSocket connection and reports host state. Docker access is optional: an Agent without access to Docker continues to report host metrics and can provide host file and terminal features that are enabled for it.

Core 负责管理员身份、Agent 注册、任务记录、历史、偏好与 SQLite 数据。每个 Agent 打开一条出站认证 WebSocket 连接并上报主机状态。Docker 访问是可选的：无 Docker 访问权的 Agent 仍会上报主机指标，并继续提供已为它启用的主机文件与终端功能。

Core and Agent have independent stable release tags. Source builds report `dev` unless built at their component tag or given an explicit build version. See [built-in updates](updates.md) for Stable/Dev selection and rollback, and the data schema note below before replacing a binary.

Core 与 Agent 使用独立的稳定发布标签。源码构建除非在其组件标签处构建或显式指定构建版本，否则报告 `dev`。Stable/Dev 选择与回滚参见[内置更新](updates.md)；替换二进制前请先阅读下文的数据兼容性说明。

For Agent container deployment, see [Agent Docker deployment](docker.md). Core runs natively on the controller host.

Agent 容器部署参见 [Agent Docker 部署](docker.md)。Core 以原生方式运行在控制机主机上。

## Requirements / 环境要求

- Linux for the Core host and Agent hosts.
- Go `1.26.8`, Node.js `22.23.3`, and pnpm `12.10.1` for source builds; see [../.tool-versions](../.tool-versions).
- Docker Engine on an Agent host only when Docker management is needed.
- A TLS HTTPS endpoint for production browser access and Agent connections. A reverse proxy may terminate TLS and forward WebSocket connections to the Core.

- Core 主机与 Agent 主机均为 Linux。
- 源码构建需要 Go `1.26.8`、Node.js `22.23.3` 与 pnpm `12.10.1`；见 [../.tool-versions](../.tool-versions)。
- 仅当需要 Docker 管理时，Agent 主机才需要 Docker Engine。
- 生产浏览器访问与 Agent 连接需要 TLS HTTPS 端点。反向代理可终止 TLS 并将 WebSocket 连接转发给 Core。

Docker is not required to run the Core or collect host metrics. / Docker 不是运行 Core 或采集主机指标的必要条件。

## Build from source / 从源码构建

```sh
make deps
make build
```

The binaries are written to `.build/nodedance` and `.build/nodedance-agent`. `make check` runs the frontend checks and Go tests. `sudo make install` installs only the Core binary; `sudo make install-agent` explicitly installs the Agent binary. Both targets copy an existing build to `$(PREFIX)/bin` (default `/usr/local/bin`) without rebuilding as root. `DESTDIR` can stage the install for packaging.

二进制输出到 `.build/nodedance` 与 `.build/nodedance-agent`。`make check` 运行前端检查与 Go 测试。`sudo make install` 仅安装 Core 二进制；`sudo make install-agent` 显式安装 Agent 二进制。两个目标都把既有构建复制到 `$(PREFIX)/bin`（默认 `/usr/local/bin`），不会以 root 重新构建。`DESTDIR` 可为打包暂存安装。

To install the latest published Core release on Linux amd64 or arm64: / 在 Linux amd64 或 arm64 上安装最新已发布的 Core 版本：

```sh
curl -fsSL https://github.com/CST-Cat/NodeDance/releases/latest/download/install-core.sh | sudo bash
```

The installer resolves the latest stable `core-vMAJOR.MINOR.PATCH` tag and verifies its architecture-specific archive against `SHA256SUMS`. It installs `/usr/local/bin/nodedance`, creates the root-owned `nodedance.service`, enables it at boot, and starts it. The service listens on `0.0.0.0:8180` and stores data under `/var/lib/nodedance`. Pass `--release-tag core-vX.Y.Z` to pin a release. Existing Core data is preserved; the installer does not install an Agent or print credentials. It refuses symlinks, directories, and units not marked as managed by NodeDance.

安装器解析最新稳定的 `core-vMAJOR.MINOR.PATCH` 标签，并对照 `SHA256SUMS` 校验对应架构的压缩包。它安装 `/usr/local/bin/nodedance`，创建 root 属主的 `nodedance.service`，设置开机自启并启动。服务监听 `0.0.0.0:8180`，数据保存在 `/var/lib/nodedance`。传 `--release-tag core-vX.Y.Z` 可固定版本。既有 Core 数据会被保留；安装器不安装 Agent，也不打印凭据。它拒绝符号链接、目录以及未标记为 NodeDance 管理的 unit。

## Run the Core

For local development, start the Core on loopback with development cookies enabled: / 本地开发时，以开发 Cookie 在回环地址启动 Core：

```sh
go run ./cmd/nodedance serve --dev --listen 127.0.0.1:8180 --data-dir ./data
```

On the first terminal start, Core asks for an administrator username and a hidden password. Press Enter at either prompt to generate that value automatically, or enter your own. Generated usernames contain 128 random bits; generated passwords contain 256 random bits. Custom usernames must contain 3–80 ASCII letters, digits, underscores or hyphens; custom passwords must contain 12–1024 UTF-8 bytes. The login username is separate from the editable display name.

首次在终端启动时，Core 会要求输入管理员用户名与隐藏密码。任一提示直接按回车即自动生成，也可自行输入。生成的用户名含 128 位随机熵，生成的密码含 256 位随机熵。自定义用户名须为 3–80 个 ASCII 字母、数字、下划线或连字符；自定义密码须为 12–1024 个 UTF-8 字节。登录用户名与可编辑的显示名相互独立。

Core saves the initial login credentials in `initial-admin.json` inside its private `0700` data directory, with file permissions `0600`, and prints only the file path. Read this file locally, open `http://127.0.0.1:8180`, and sign in with both the username and password. The database stores an Argon2id password hash. Core deletes `initial-admin.json` after the first successful login. Restarting an initialized Core preserves its account and password.

Core 将初始登录凭据保存在其私有 `0700` 数据目录内的 `initial-admin.json`（文件权限 `0600`），只打印文件路径。在本地读取该文件，打开 `http://127.0.0.1:8180`，用用户名和密码登录。数据库保存 Argon2id 密码哈希。首次登录成功后 Core 删除 `initial-admin.json`。重启已初始化的 Core 保留其账号与密码。

When Core starts without a terminal, including the managed systemd service, it generates both values automatically without waiting for input. The release installer starts the service immediately; read the private initial credential file locally for the first login.

Core 无终端启动时（包括受管的 systemd 服务）会自动生成两个值，不等待输入。发布安装器会立即启动服务；首次登录请在本地读取私有初始凭据文件。

The release installer above creates the persistent service automatically. It uses root-owned `/var/lib/nodedance` with mode `0700`, an optional `/etc/default/nodedance` environment file for `NODEDANCE_PUBLIC_ORIGIN` and `NODEDANCE_TRUSTED_PROXIES`, and a root-managed `nodedance.service`. On first unattended start, Core generates an administrator username and password in `/var/lib/nodedance/initial-admin.json` (mode `0600`); read it locally and sign in, then Core removes the file after the first successful login. Existing accounts remain in the database across service restarts and binary upgrades.

上文发布安装器会自动创建持久服务。它使用 root 属主、`0700` 模式的 `/var/lib/nodedance`，一个可选的 `/etc/default/nodedance` 环境文件（用于 `NODEDANCE_PUBLIC_ORIGIN` 与 `NODEDANCE_TRUSTED_PROXIES`），以及 root 管理的 `nodedance.service`。首次无终端启动时，Core 会在 `/var/lib/nodedance/initial-admin.json`（模式 `0600`）生成管理员用户名与密码；在本地读取并登录后，Core 会在首次登录成功时删除该文件。既有账号跨服务重启与二进制升级保留在数据库中。

Configure the HTTPS reverse proxy for WebSocket upgrades on `/ws/` and forward to `127.0.0.1:8180` or the host's private address. The Core listener itself uses HTTP; use a trusted private network or terminate HTTPS at the proxy rather than exposing plain HTTP to the public Internet.

为 `/ws/` 上的 WebSocket 升级配置 HTTPS 反向代理，并转发到 `127.0.0.1:8180` 或主机私有地址。Core 监听本身使用 HTTP；请使用可信私有网络或在代理处终止 HTTPS，不要向公共互联网暴露明文 HTTP。

Manage the service with `sudo systemctl restart nodedance.service`, `sudo systemctl stop nodedance.service`, and `sudo journalctl -u nodedance.service`. A local Agent can run on the same Linux host. Production Agent connections require HTTPS/WSS and a certificate trusted by the Agent host; the Agent uses the system trust store, so no custom CA file is needed for ordinary public certificates. A private CA can be supplied with `--ca-file`. Plain HTTP/WS is accepted only for a literal loopback Core address in development mode. The Agent connects outbound over WSS and reconnects automatically after temporary Core or network outages.

使用 `sudo systemctl restart nodedance.service`、`sudo systemctl stop nodedance.service` 与 `sudo journalctl -u nodedance.service` 管理服务。同一 Linux 主机上可以运行本地 Agent。生产 Agent 连接要求 HTTPS/WSS 与 Agent 主机信任的证书；Agent 使用系统信任库，普通公共证书无需自定义 CA 文件。私有 CA 可通过 `--ca-file` 提供。仅开发模式下对字面回环 Core 地址接受纯 HTTP/WS。Agent 通过 WSS 出站连接，并在 Core 或网络短暂中断后自动重连。

The Core reads JSON configuration from `--config` or `NODEDANCE_CONFIG`; its default is `$XDG_CONFIG_HOME/nodedance/config.json` (or `~/.config/nodedance/config.json` on a typical Linux host). CLI flags take precedence over supported environment variables, which take precedence over the config file. Important options include:

Core 从 `--config` 或 `NODEDANCE_CONFIG` 读取 JSON 配置；默认为 `$XDG_CONFIG_HOME/nodedance/config.json`（典型 Linux 主机为 `~/.config/nodedance/config.json`）。CLI 标志优先于受支持的环境变量，环境变量优先于配置文件。重要选项包括：

| Setting | Purpose | 用途 |
| --- | --- | --- |
| `listen` / `--listen` / `NODEDANCE_LISTEN` | Core bind address; app fallback is `127.0.0.1:8180`, installed service uses `0.0.0.0:8180` | Core 绑定地址；应用回退 `127.0.0.1:8180`，安装服务使用 `0.0.0.0:8180` |
| `data_dir` / `--data-dir` / `NODEDANCE_DATA_DIR` | Private Core state directory | Core 私有状态目录 |
| `public_origin` / `--public-origin` / `NODEDANCE_PUBLIC_ORIGIN` | Browser HTTPS origin used for request-origin checks | 用于请求来源校验的浏览器 HTTPS origin |
| `trusted_proxies` / `--trusted-proxies` / `NODEDANCE_TRUSTED_PROXIES` | Proxy IPs or CIDRs trusted for client address forwarding | 信任客户端地址转发的代理 IP 或 CIDR |
| `max_file_transfer_bytes` / `--max-file-bytes` / `NODEDANCE_MAX_FILE_BYTES` | Maximum size of one file transfer | 单个文件传输的大小上限 |
| `non_metric_history_retention_days` / `--history-retention-days` / `NODEDANCE_HISTORY_RETENTION_DAYS` | Retention for task, event, and audit history | 任务、事件与审计历史的保留期 |

The Core data directory contains the SQLite database and authentication/encryption keys. Keep it private and include the whole directory in Core backups. / Core 数据目录包含 SQLite 数据库与认证/加密密钥。请保持其私密，并将整个目录纳入 Core 备份。

## Enroll and run an Agent / 注册并运行 Agent

After signing in to the Core console, choose **添加 Agent**, enter a node display name, and generate a one-time enrollment. Choose the URL the target Agent can reach; it defaults to the current console origin and may be a different Core address on a private network. Native/systemd and Docker are side-by-side choices, visible before generating the command. Copy the complete command to the target Linux host and run it; it includes the single-use token and does not prompt for it again. This token has no time limit; successful enrollment consumes it, and regeneration invalidates the previous token. Shared enrollment grants separately enforce their configured expiry and use limit.

登录 Core 控制台后，选择**添加 Agent**，输入节点显示名并生成一次性注册。选择目标 Agent 可访问的 URL；默认为当前控制台 origin，也可以是私有网络上另一个 Core 地址。原生/systemd 与 Docker 是并列选项，生成命令前即可见。把完整命令复制到目标 Linux 主机执行；命令已包含一次性令牌，不会再次询问。该令牌没有时限；注册成功即被消费，重新生成会使先前令牌失效。共享注册授予单独执行其配置的过期与使用上限。

The generated command downloads the current installation script and defaults to the latest stable published Agent at execution time, without a hardcoded tag. The installer reads `agent-latest.txt` over HTTPS and accepts only a stable `agent-vMAJOR.MINOR.PATCH` tag; it does not query the GitHub REST API or require Python for release resolution. The selected release supplies the matching binary, checksum manifest and Docker image. `agent-release.version` remains a Core publication pairing check. The installer supports Linux `amd64` and `arm64`, verifies the matching Agent binary against that release's SHA256 manifest, prepares root-owned state, enrolls through `nodedance-agent enroll --token-stdin`, then installs, enables, and starts the root systemd service or the rootful Docker container, according to the selected mode. It refuses existing Agent state, binaries, or a same-name service, and will not overwrite existing config or service files. If enrollment may have reached Core but the response was lost, it preserves the protected config and provides the recovery command.

生成的命令会下载当前安装脚本，并在执行时默认选择最新已发布的稳定 Agent，不硬编码标签。安装器通过 HTTPS 读取 `agent-latest.txt`，只接受稳定的 `agent-vMAJOR.MINOR.PATCH` 标签；不查询 GitHub REST API，也不需要 Python 解析发布。所选发布提供匹配的二进制、校验清单与 Docker 镜像。`agent-release.version` 保留为 Core 发布配对校验。安装器支持 Linux `amd64` 与 `arm64`，对照该发布的 SHA256 清单校验 Agent 二进制，准备 root 属主状态，通过 `nodedance-agent enroll --token-stdin` 注册，然后按所选模式安装、启用并启动 root systemd 服务或 rootful Docker 容器。它拒绝既有的 Agent 状态、二进制或同名服务，也不会覆盖既有配置或服务文件。若注册可能已到达 Core 但响应丢失，它会保留受保护配置并提供恢复命令。

Agent publication uses the existing `.github/workflows/agent-release.yml` and an explicitly authorized `agent-v*` tag after a passing rehearsal. See [release procedure](releases.md). Generated commands select the published stable Agent at execution time. An unavailable or invalid version file stops installation before enrollment or container startup; `--release-tag` can select an explicit published Agent tag.

Agent 发布使用既有 `.github/workflows/agent-release.yml`，在彩排通过后由显式授权的 `agent-v*` 标签触发。参见[发布流程](releases.md)。生成的命令在执行时选择已发布的稳定 Agent。版本文件不可用或非法会在注册或容器启动前停止安装；`--release-tag` 可显式选择已发布的 Agent 标签。

The Agent runs as root for full host management; `enroll`, `recover`, and `run` reject non-root callers. It verifies the config remains root-owned, regular, and mode `0600` inside a `0700` state directory. Root file management exposes host paths while blocking kernel/runtime trees and Agent credentials. NodeDance does not change Docker socket permissions.

Agent 以 root 运行以获得完整主机管理能力；`enroll`、`recover` 与 `run` 拒绝非 root 调用方。它校验配置保持 root 属主、常规文件，且位于 `0700` 状态目录内权限为 `0600`。root 文件管理暴露主机路径，同时拦截内核/运行时目录与 Agent 凭据。NodeDance 不更改 Docker socket 权限。

For same-host development against a loopback Core, build with `make build` and use `sudo .build/nodedance-agent enroll --dev --server http://127.0.0.1:8180 --config /var/lib/nodedance-agent/agent.json --token-stdin`. `--dev` is restricted to literal loopback HTTP. All non-development Agent endpoints require HTTPS.

对回环 Core 做同主机开发时，先用 `make build` 构建，再执行 `sudo .build/nodedance-agent enroll --dev --server http://127.0.0.1:8180 --config /var/lib/nodedance-agent/agent.json --token-stdin`。`--dev` 仅限字面回环 HTTP。所有非开发 Agent 端点要求 HTTPS。

The installer writes a NodeDance-managed root service without a filesystem/system write sandbox. It refuses unrelated or unmarked units, reloads systemd, and can enable/start the Agent. Inspect it with `sudo systemctl status nodedance-agent.service`; restart or stop it with `sudo systemctl restart nodedance-agent.service` or `sudo systemctl stop nodedance-agent.service`. If enrollment may have reached Core but the response was lost, retain the private config and run `sudo nodedance-agent recover --config /var/lib/nodedance-agent/agent.json`. Recovery checks the saved device credential and does not replay the one-time token. `sudo nodedance-agent uninstall-systemd` stops/removes only the current NodeDance-managed root unit; it preserves the binary and credentials.

安装器写入 NodeDance 管理的 root 服务，不带文件系统/系统写入沙箱。它拒绝无关或未标记的 unit，重载 systemd，并可启用/启动 Agent。用 `sudo systemctl status nodedance-agent.service` 检查；用 `sudo systemctl restart nodedance-agent.service` 或 `sudo systemctl stop nodedance-agent.service` 重启或停止。若注册可能已到达 Core 但响应丢失，保留私有配置并运行 `sudo nodedance-agent recover --config /var/lib/nodedance-agent/agent.json`。恢复会校验已保存的设备凭据，不会重放一次性令牌。`sudo nodedance-agent uninstall-systemd` 仅停止/移除当前 NodeDance 管理的 root unit；保留二进制与凭据。

Use `--file-root /absolute/path` to limit file-manager access to a specific directory, or `--no-file-root` to disable file management. Docker access is optional. NodeDance does not change Docker socket permissions. If Docker is absent or unavailable, host monitoring continues.

使用 `--file-root /absolute/path` 将文件管理访问限制到特定目录，或用 `--no-file-root` 关闭文件管理。Docker 访问是可选的。NodeDance 不更改 Docker socket 权限。Docker 缺失或不可用时，主机监控继续。

### Optional Tailscale discovery and SSH-assisted Agent installation / 可选的 Tailscale 发现与 SSH 辅助 Agent 安装

Tailscale is optional. Core startup does not install, configure, or require it. If the Core host has the Tailscale CLI installed and is logged in, an administrator can open **发现节点** in the console; discovery reads that host's local `tailscale status --json`. If the CLI is absent or not logged in, ordinary Core management and manual Agent enrollment continue to work.

Tailscale 是可选的。Core 启动不安装、不配置也不要求它。若 Core 主机装有 Tailscale CLI 且已登录，管理员可在控制台打开**发现节点**；发现读取该主机本地的 `tailscale status --json`。CLI 缺失或未登录时，普通 Core 管理与手动 Agent 注册照常工作。

SSH-assisted Agent deployment is available only for visible, online Linux peers and requires a signed Agent artifact on the Core. The installer creates no Agent service account: it prepares root-owned credentials and installs the same root-only systemd service used by manual enrollment. Build Linux `amd64` and/or `arm64` Agent binaries, sign each binary with an Ed25519 release key kept outside the Core host, and place each binary with its detached signature at `linux-amd64/nodedance-agent[.sig]` or `linux-arm64/nodedance-agent[.sig]` under a private artifact directory. Configure the Core service environment with the artifact directory and the base64-encoded raw Ed25519 public key, then restart Core:

SSH 辅助 Agent 部署仅对可见、在线的 Linux peer 开放，并要求 Core 上存在已签名的 Agent 制品。安装器不创建 Agent 服务账号：它准备 root 属主凭据，并安装与手动注册相同的 root-only systemd 服务。构建 Linux `amd64` 和/或 `arm64` Agent 二进制，用保存在 Core 主机之外的 Ed25519 发布私钥为每个二进制签名，并把每个二进制及其分离签名放到私有制品目录下的 `linux-amd64/nodedance-agent[.sig]` 或 `linux-arm64/nodedance-agent[.sig]`。用制品目录与 base64 编码的原始 Ed25519 公钥配置 Core 服务环境变量，然后重启 Core：

```ini
Environment=NODEDANCE_AGENT_ARTIFACT_DIR=/var/lib/nodedance-agent-artifacts
Environment=NODEDANCE_AGENT_SIGNING_PUBLIC_KEY=<base64-encoded-raw-ed25519-public-key>
```

Add those lines in `sudo systemctl edit nodedance.service`, save the override, then run `sudo systemctl daemon-reload && sudo systemctl restart nodedance.service`.

在 `sudo systemctl edit nodedance.service` 中加入这些行，保存覆盖，然后执行 `sudo systemctl daemon-reload && sudo systemctl restart nodedance.service`。

The public key is not secret; keep the signing private key off the Core host. In the UI, refresh discovery, select a Linux peer, read its SSH host-key fingerprint, and verify it through an independent trusted channel before confirming. Credentials are held only for the active deployment request and are not stored in task history. The UI displays a manual SSH recovery command. If signed artifacts are not configured, use the generic **添加 Agent** enrollment flow above. Tailscale and SSH deployment should be used only when you administer those systems.

公钥不是秘密；签名私钥请保存在 Core 主机之外。在 UI 中刷新发现，选择一个 Linux peer，读取其 SSH 主机密钥指纹，并在确认前通过独立可信渠道核验。凭据仅保存到当前部署请求结束，不会存入任务历史。UI 会显示手动 SSH 恢复命令。若未配置签名制品，请使用上文的通用**添加 Agent**注册流程。Tailscale 与 SSH 部署只应在你自己管理的系统上使用。

## CLI and terminal UI / CLI 与终端 UI

All administrator operations are discoverable from `nodedance operations` as JSON, including parameter/body schemas. CLI and TUI share the same operation catalog and authenticated API client:

所有管理员操作都可从 `nodedance operations` 以 JSON 发现，包括参数/请求体 schema。CLI 与 TUI 共享同一操作目录与认证 API 客户端：

```sh
export NODEDANCE_SERVER=https://nodedance.example
nodedance auth login --body @/private/login.json
nodedance nodes list
nodedance describe containers.action
nodedance tui --timeout 0
```

Remote CLI operations return JSON; logs, metrics and terminal connections use JSONL. The TUI provides searchable operations, parameter forms, file transfers, paginated results and an interactive host/container shell. Local Agent lifecycle commands are available through `sudo nodedance agent ...`, which invokes the separate Agent binary. The built-in `sudo nodedance agent update` workflow is executed directly by Core without requiring that wrapper dispatch. Read [CLI/TUI usage](cli.md) for authentication, task idempotency, streams and examples, and [the Komari communication study](komari-communication.md) for the protocol design.

远程 CLI 操作返回 JSON；日志、指标与终端连接使用 JSONL。TUI 提供可搜索操作、参数表单、文件传输、分页结果与交互式主机/容器 shell。本地 Agent 生命周期命令通过 `sudo nodedance agent ...` 提供，它调用独立的 Agent 二进制。内置的 `sudo nodedance agent update` 流程由 Core 直接执行，无需该包装分发。认证、任务幂等、流与示例见 [CLI/TUI 用法](cli.md)，协议设计见 [Komari 通信研究](komari-communication.md)。

## Use probes and alerts / 使用探测与告警

Service probes are configured from a node's service-probe panel and execute from that node's Agent. Supported targets are HTTP, HTTPS, TCP, and IPv4/IPv6 ICMP (`icmp://host`). HTTPS certificate validation is enabled; redirects are not followed. Probe timeouts are bounded, and offline or unsupported Agents produce an unknown result rather than a service failure. Three consecutive failures mark a probe unhealthy; two consecutive successes restore healthy status.

服务探测在节点的服务探测面板配置，由该节点的 Agent 执行。支持的目标为 HTTP、HTTPS、TCP 与 IPv4/IPv6 ICMP（`icmp://host`）。HTTPS 证书校验启用；不跟随重定向。探测超时有界，离线或不支持的 Agent 产生 unknown 结果而非服务失败。连续三次失败标记探测不健康；连续两次成功恢复健康。

The Alert Center manages rules, maintenance windows, notification channels, and delivery history. Webhooks use administrator-configured HTTP(S) URLs. SMTP requires STARTTLS or implicit TLS; plaintext SMTP is rejected, including on loopback. Notification secrets are encrypted in the Core database using a key stored in the private data directory.

告警中心管理规则、维护窗口、通知渠道与投递历史。Webhook 使用管理员配置的 HTTP(S) URL。SMTP 要求 STARTTLS 或隐式 TLS；拒绝明文 SMTP，包括回环地址。通知密钥使用保存在私有数据目录中的密钥在 Core 数据库内加密。

## Back up and restore Core data / 备份与恢复 Core 数据

Create a consistent SQLite snapshot and a private archive with: / 创建一致的 SQLite 快照与私有归档：

```sh
nodedance backup --data-dir /var/lib/nodedance --output /secure/backups/nodedance-2026-10-09.tar
```

The output path must not already exist and must be outside the Core data directory. The backup archive contains the SQLite database and required authentication/encryption keys, plus optional first-run credential or Tailscale state files when present; it is created with mode `0600`. Store it in a restricted location, and protect any off-host copy as sensitive data.

输出路径必须不存在，且必须在 Core 数据目录之外。备份归档包含 SQLite 数据库与必需的认证/加密密钥，以及存在时的首次运行凭据或 Tailscale 状态文件；以 `0600` 模式创建。请存放在受限位置，并将任何离机副本作为敏感数据保护。

Restore to a new or empty directory on Linux: / 恢复到 Linux 上的新目录或空目录：

```sh
nodedance restore \
  --input /secure/backups/nodedance-2026-10-09.tar \
  --data-dir /var/lib/nodedance-restored
```

Restore validates the archive, file hashes, required keys, and SQLite integrity before atomically installing the data. It refuses a non-empty destination. Stop the Core before switching it to the restored directory. Verify administrator login, node identities, and required notification settings before returning the restored Core to service.

恢复会先校验归档、文件哈希、必需密钥与 SQLite 完整性，再原子安装数据。它拒绝非空目标。把 Core 切换到恢复目录前请先停止 Core。在恢复的 Core 重新服务前，核验管理员登录、节点身份与必需的通知设置。

The database snapshot keeps administrator and Agent credentials, node identities, dashboard preferences, tasks, probes, alerts, notification history, and their related records together. The alert-channel encryption key is included so restored notification secrets remain decryptable.

数据库快照将管理员与 Agent 凭据、节点身份、仪表盘偏好、任务、探测、告警、通知历史及其相关记录保存在一起。告警渠道加密密钥一并包含，恢复后的通知密钥仍可解密。

## Upgrade and data compatibility / 升级与数据兼容性

Run `nodedance update` to select the newest stable Core tag. Protocol, capability, API and SQLite fingerprints do not gate version selection. The built-in transaction verifies the release artifact, preserves data for recovery, switches the runtime and checks its health; failed startup restores the previous runtime and pre-update data. A raw binary replacement alone does not restore data changed by the newer runtime. See [updates](updates.md).

运行 `nodedance update` 选择最新稳定 Core 标签。协议、能力、API 与 SQLite 指纹不作为版本选择的门槛。内置事务校验发布制品、保留数据用于恢复、切换运行时并检查其健康；启动失败会恢复先前运行时与更新前数据。仅裸替换二进制不会恢复被新运行时改变的数据。参见[更新](updates.md)。

Core initializes the current SQLite schema directly and does not migrate retired development layouts. Preserve the complete private data directory in a backup before an upgrade. Restore targets a new or empty directory and does not convert older schemas. Product release versions do not establish database schema compatibility.

Core 直接初始化当前 SQLite schema，不迁移已退役的开发期布局。升级前请将完整私有数据目录纳入备份。恢复针对新目录或空目录，不转换旧 schema。产品发布版本不构成数据库 schema 兼容性承诺。

Agent systemd installation is root-only. It recognizes only the current NodeDance-managed root unit and refuses to overwrite unrelated, unmarked, shadowed, or changed units. Uninstall removes that unit while preserving the Agent config and identity.

Agent systemd 安装仅限 root。它只识别当前 NodeDance 管理的 root unit，拒绝覆盖无关、未标记、被遮蔽或已更改的 unit。卸载移除该 unit，同时保留 Agent 配置与身份。

## Troubleshooting / 故障排查

- **Browser setup or login does not persist:** use HTTPS in production so secure session cookies are accepted. `--dev` is for loopback development only.
- **Agent remains offline:** check the configured Core HTTPS/WSS URL, outbound connectivity, enrollment state, and Agent service log with `systemctl status nodedance-agent` and `journalctl -u nodedance-agent`.
- **Host metrics work but Docker is unavailable:** check Docker Engine status and whether the Agent can access its socket. NodeDance intentionally leaves socket permissions unchanged.
- **File access is unavailable:** the root Agent enables full-host browsing by default, except `/proc`, `/sys`, `/dev`, `/run`, and the Agent credential state directory. Check for `NODEDANCE_AGENT_FILE_ACCESS=disabled`; `--file-root` can narrow access.
- **A probe reports unknown:** check that the node is online and negotiated the probe capability. Unknown means there is no current result; it is not treated as a failed service.
- **Restore is rejected:** use a complete private backup archive, an existing parent directory, and a destination directory that is absent or empty. Restore does not overwrite existing non-empty data.

- **浏览器初始化或登录不持久：** 生产环境使用 HTTPS 以接受安全会话 Cookie。`--dev` 仅用于回环开发。
- **Agent 一直离线：** 检查配置的 Core HTTPS/WSS URL、出站连通性、注册状态与 Agent 服务日志（`systemctl status nodedance-agent` 与 `journalctl -u nodedance-agent`）。
- **主机指标正常但 Docker 不可用：** 检查 Docker Engine 状态与 Agent 能否访问其 socket。NodeDance 有意不更改 socket 权限。
- **文件访问不可用：** root Agent 默认开放全主机浏览，`/proc`、`/sys`、`/dev`、`/run` 与 Agent 凭据状态目录除外。检查是否存在 `NODEDANCE_AGENT_FILE_ACCESS=disabled`；`--file-root` 可缩小访问范围。
- **探测报告 unknown：** 检查节点在线并已协商探测能力。unknown 表示暂无当前结果，不视为服务失败。
- **恢复被拒绝：** 使用完整私有备份归档、已存在的父目录以及不存在或为空的目标目录。恢复不会覆盖既有非空数据。

For local API and Web debugging, set `VITE_CORE_TARGET` when the Core is not at `http://127.0.0.1:8180`; Vite forwards `/api` and `/ws` requests to that target.

本地 API 与 Web 调试时，若 Core 不在 `http://127.0.0.1:8180`，设置 `VITE_CORE_TARGET`；Vite 会把 `/api` 与 `/ws` 请求转发到该目标。

## Language and releases / 语言与发布

支持 English 和简体中文。Web 在个人设置中切换语言；CLI 使用 `--lang en|zh-CN`，TUI 还可按 `l` 切换。Core 使用 `core-v*`，Agent 使用 `agent-v*` 独立发布，发布说明提供中英双语。

[语言约定](i18n.md) · [发布流程](releases.md) · [Komari 优化学习](komari-optimizations.md)

Pending nodes retain an Installation command entry on their detail page. Reopening it lets you regenerate a fresh single-use credential with no time limit for the same node and repeatedly copy either deployment command. Core stores only token digests, so regeneration invalidates previously issued commands. Once registered, enrollment regeneration is rejected. Delete node is located at the bottom of the Web Node settings tab and is available in every node state and removes Core-owned records and credentials, without uninstalling the remote Agent or claiming to stop dispatched work.

待注册节点在其详情页保留一条安装命令入口。重新打开可为同一节点重新生成新的无时限一次性凭据，并反复复制任一部署命令。Core 只保存令牌摘要，重新生成会使先前发布的命令失效。节点注册后，再次生成注册会被拒绝。删除节点位于 Web 节点设置标签页底部，在每种节点状态下都可用，并移除 Core 侧记录与凭据；它不卸载远端 Agent，也不声称停止已下发的工作。
