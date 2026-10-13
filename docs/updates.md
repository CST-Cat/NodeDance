# Updates / 正式更新

Web 的“更新中心”、`nodedance update` 与 TUI 中的 `updates.*` 使用连接的 Core `/api/v1/operations` 契约，提交相同的更新请求。Core 与 Agent 分别解析 `core-vMAJOR.MINOR.PATCH` 和 `agent-vMAJOR.MINOR.PATCH` 正式版本，忽略预发行版，不使用仓库通用 latest 地址。

The Web **Update center**, `nodedance update`, and TUI `updates.*` operations all use the connected Core's operation contract. Core and Agent select stable releases from their separate tag namespaces; prereleases and repository-wide latest URLs are excluded.

## Commands / 命令

先登录 Core，使用已保存的 CLI 会话 / Sign in once using the CLI's saved session:

```sh
export NODEDANCE_SERVER=https://nodes.example.com
nodedance auth login --body @/private/login.json
nodedance update --check
nodedance update
nodedance update --component agent --node-id NODE_UUID --check
nodedance update --component agent --node-id NODE_UUID
nodedance update --status
nodedance update --status --job-id JOB_ID
nodedance update --rollback JOB_ID --yes
```

`update` 默认直接更新到最新正式版本，无需额外确认；只有回滚会交互确认，`--yes` 可跳过回滚确认。`--check` 不执行更新。`--version core-vX.Y.Z` 或 `--component agent --version agent-vX.Y.Z` 固定目标正式版本。默认等待任务完成，服务重启期间自动重试进度查询；`--no-wait` 仅返回已接受的任务。若提交时连接中断，可用 stderr 输出的 `--idempotency-key KEY` 重试原请求，避免重复更新。Agent 进度和回滚命令也需传 `--component agent --node-id NODE_UUID`。

`update` directly applies the latest stable release without an extra confirmation. Only rollback asks for confirmation; `--yes` skips that rollback prompt. `--check` is read-only. `--version` pins a published component tag. By default, the command waits for completion and retries progress reads during restarts; `--no-wait` returns the accepted job. Reuse the printed `--idempotency-key` when retrying an interrupted submission. Agent status and rollback commands also require the component and node ID.

Web 选择组件和节点，检查版本并阅读说明，点击更新即执行；回滚才需要勾选确认。页面保留提交重试键并轮询持久化进度。TUI 从 Core 自动发现四项操作，使用同一请求 schema；`updates.apply` / `updates.rollback` 的请求需要 `confirm: true`。

Web selects the component/node, checks the release, displays notes and applies on clicking Update. Only rollback requires the confirmation checkbox. It keeps an interrupted submission's retry key and polls durable progress. TUI discovers all four operations automatically and uses the same request schemas and confirmation requirement.

## One-time supervisor setup / 首次配置更新控制服务

更新需要部署主机上的独立 root 服务 `nodedance-updater.service`。Core 继续以非 root 运行，只能通过受 UID/GID 限制的 Unix socket 请求已注册组件的更新，没有 Docker socket 权限或任意命令入口。该服务也提供一个固定的只读 Tailscale 节点查询，供已注册 Core 读取宿主机网络能力，无需修改容器挂载。Agent 使用现有认证 WebSocket 代理更新请求，控制服务独立于 Agent 重启。

A separate root supervisor owns the deployment transaction. Core remains unprivileged and accesses only a UID/GID protected Unix socket; it receives no Docker socket or arbitrary command execution. A fixed read-only Tailscale peer query also lets registered Core deployments discover the host network without new container mounts. Remote Agent requests travel through its authenticated generation-bound channel. The supervisor remains running when the application restarts.

从组件正式 release 下载 `install-updater.sh`，脚本校验同版本更新器的 SHA256。执行一次，注册实际运行的容器或 systemd 单元、数据目录、健康检查地址；禁止注册任意 systemd 服务。

Download `install-updater.sh` from the component's published release. It verifies the updater artifact's SHA256, then registers the existing deployment. Use the actual container, private data directory and local health URL. Only the NodeDance systemd units can be registered.

Core Docker 示例 / Core Docker example (replace the volume's absolute host path and published port):

```sh
sudo bash install-updater.sh --release-tag core-v0.1.9 \
  --component core --mode docker --container nodedance-core \
  --data-dir /var/lib/docker/volumes/nodedance-core-data/_data \
  --health-url http://127.0.0.1:8180/api/v1/health \
  --compose-file /etc/nodedance/docker/compose.yml
```

将宿主机 `/run/nodedance-updater` **整个目录**只读挂载到 Core 的同一路径，然后重新创建一次 Core 容器。不要单独挂载 socket 文件，因为控制服务重启会替换 socket。Compose 文件须 root 所有、0600；控制服务会在成功更新后保存新的固定镜像标签。Core UID/GID 默认 10001，可按实际服务账户设置 `--core-uid` 和 `--core-gid`。

Bind the **entire** `/run/nodedance-updater` host directory read-only into the Core container at the same path, then recreate Core once. Mounting only the socket inode would break access after supervisor restart. The registered Compose file must be root-owned and private (0600); successful updates persist its new pinned image tag. Default Core UID/GID is 10001 and can be set explicitly.

```yaml
services:
  core:
    volumes:
      - type: bind
        source: /run/nodedance-updater
        target: /run/nodedance-updater
        read_only: true
        bind:
          create_host_path: false
```

原生 Core / Native Core:

```sh
sudo bash install-updater.sh --release-tag core-v0.1.9 \
  --component core --mode systemd --unit nodedance.service \
  --binary /usr/local/bin/nodedance --data-dir /var/lib/nodedance \
  --health-url https://127.0.0.1:8180/api/v1/health \
  --health-ca-file /etc/nodedance/tls/ca.pem \
  --core-uid CORE_UID --core-gid CORE_GID
```

健康检查验证 TLS；自签发证书须配置受信任 CA，证书也须匹配注册的本地 IP。Core 数据目录须 0700，账户继续拥有数据。Agent 安装器和 Core 生成的 Docker 部署命令会自动安装更新服务；已部署 Agent 可单独配置，保留现有身份，无需重新注册：

Health checks verify TLS, including the registered local IP; use a trusted CA for a private certificate. Core data remains owned by its service account in a 0700 directory. The native Agent installer and Core-generated Docker enrollment command install the supervisor automatically. An existing Agent can register it separately without enrolling again:

```sh
sudo bash install-updater.sh --release-tag agent-v0.1.7 \
  --component agent --mode docker --container nodedance-agent \
  --data-dir /var/lib/nodedance-agent --core-uid 0 --core-gid 0
# Native Agent: replace mode/container with:
# --mode systemd --binary /usr/local/bin/nodedance-agent --unit nodedance-agent.service
```

## Transaction and recovery / 更新与恢复

1. 解析正式 release 和更新清单，检查协议、当前存储结构契约及部署状态；不兼容版本拒绝更新。
2. 原生部署下载并校验 SHA256，Docker 拉取固定组件标签并固定镜像 ID，下载失败不停止旧服务。
3. 停止已注册服务，完整备份私有状态，再原子切换原生二进制或替换容器；保留原有环境、端口、挂载、网络、权限与重启策略。
4. 验证运行版本、Core 健康检查或 Agent 相同身份重新上线，Docker 还验证运行状态和配置的健康检查。
5. 验证失败自动恢复旧运行版本。Core 恢复更新前数据；Agent 保留当前凭据、轮换状态和任务日志。人工回滚同样遵循此规则。
6. 任务阶段持久写入 root 私有目录 `/var/lib/nodedance-updater`；控制服务重启后恢复中断任务。保留备份和旧容器供回滚，不能手动删除仍需回滚的资源。

The transaction resolves and preflights a published release, verifies downloads before stopping anything, takes an offline private state backup, replaces only the registered runtime, then checks the reported version and service health or same-identity Agent reconnection. Failure restores the prior runtime automatically. Core data returns to its pre-update snapshot; Agent credentials and task state remain current. The update supervisor is also verified and refreshed with the release. Durable root-owned job journals recover interrupted transactions after supervisor restart. Backups and stopped prior containers are retained for rollback.

控制器关闭时立即拒绝新任务，取消正在进行的任务接收检查，并等待接收检查和已启动的更新工作结束后才释放进程锁。发布查询不能在关闭后重新接收任务，避免新旧控制器同时接管更新。

Shutdown rejects new work and cancels pending admission checks. The controller retains its process lock until both admission and execution finish, so a late release lookup cannot accept work after ownership has been released.

Core 回滚会撤销更新后发生的数据变更，执行前必须明确确认。若外部工具已替换容器或版本，控制服务拒绝覆盖它。更新服务注册文件为 `/etc/nodedance-updater/config.json`（root、0600）；Agent 状态始终 root、0700，凭据为普通 0600 文件。

Confirm Core rollback with the understanding that data changes since the update will be undone. The supervisor refuses to overwrite a runtime replaced externally. Its registration file is root-owned 0600; Agent state and credentials retain their root ownership and 0700/0600 permissions.

Docker Agent 进入宿主机挂载命名空间运行，更新器通过已注册容器的运行进程读取实际可执行文件版本，不依赖容器内原始路径。

The Docker Agent runs in the host mount namespace. The updater reads the actual executable version through the registered container's running process, without relying on its original image path.

Agent 在实际 WebSocket 握手中声明 `agent.update.v1`，Core 协商该能力后，Web、CLI 和 TUI 才能调用远端更新服务。

The Agent advertises `agent.update.v1` in its runtime WebSocket hello. Once Core negotiates that capability, Web, CLI and TUI can call the remote update supervisor.
