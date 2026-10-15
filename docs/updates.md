# Updates / 内置更新

更新已内置于 Core 和 Agent。正常部署后直接运行 `nodedance update`，默认升级连接的 Core 到最新正式版本；不需要另外安装程序、注册更新服务或添加参数。Web 更新中心与 TUI 使用同一份 Core `/api/v1/operations` 契约、请求和执行流程。CLI 自动复用已保存的 Core 地址、Origin、CA 证书路径和管理员会话，更新不会跳过登录、权限或 CSRF 检查。

Updates are built into Core and Agent. After normal deployment, `nodedance update` updates the connected Core to its newest stable release with no extra parameters or update-service installation. Web, CLI and TUI use the same discovered Core operation contract. CLI restores the saved server, origin and CA path from its private session file; administrator authentication remains required.

```sh
nodedance update        # 更新 Core / Update Core
nodedance agent update  # 更新本机 Agent / Update the local Agent
```

`nodedance agent update` 默认更新本机 Agent，需要 root，由 `nodedance` 直接调用共用的内置更新流程。通过 Core 更新指定远端 Agent 时仍需选择节点，这是更新目标的选择，不是安装更新器：

The `nodedance agent update` command updates the local managed Agent and requires root. It runs the shared built-in transaction directly from `nodedance`. Selecting a remote Agent through Core still requires its node ID:

```sh
nodedance update --component agent --node-id NODE_UUID
```

## Observation and recovery / 观察与恢复

`--check` 只检查，`--version core-vX.Y.Z`／`agent-vX.Y.Z` 可固定版本。默认等待完成；`--no-wait` 在持久接受后返回。提交前输出 `Idempotency-Key`；连接中断后用 `--idempotency-key KEY` 重试原请求，避免重复执行。`--status` 查看历史，`--status --job-id JOB_ID` 查看进度。Core 回滚使用 `--rollback JOB_ID`，交互确认后执行；Agent 本地回滚同样交互确认，`--yes` 可跳过确认。远端 Agent 的观察和回滚沿用相同的 `--component agent --node-id NODE_UUID` 目标选择。

`--check` is read-only; `--version` optionally pins a release. Updates wait by default; `--no-wait` returns after durable acceptance. Reuse the printed `--idempotency-key` after interrupted submission. `--status` lists history and `--job-id` selects a job. Rollback requires confirmation; Agent local rollback also asks for confirmation; `--yes` skips the prompt. Remote Agent observation and rollback keep the same component/node selection.

## Execution / 执行

- 从各自 `core-v*`／`agent-v*` 命名空间选择正式版本；仅比较 tag 判断是否有新版，不比较协议号、能力、API 或存储指纹，不要求 `nodedance-update.json`。
- 切换前下载并校验 SHA256，或拉取固定 Docker 标签并保存精确镜像 ID；下载失败不停止旧程序。
- 自动识别当前 NodeDance systemd 服务或容器、私有数据目录和 Core 健康检查。更新请求不能指定任意服务或执行任意命令。
- 由同一个 Core／Agent 二进制按需启动临时工作进程：原生部署使用临时 systemd 单元，Docker 使用当前产品镜像的临时容器。没有独立更新二进制、安装脚本或常驻更新服务。
- 临时工作进程独立于目标服务的生命周期，完成停止、备份、切换、启动、健康核验；失败自动恢复旧运行版本。Core 恢复更新前数据；Agent 保留当前凭据和任务状态。
- 任务日志与备份位于数据目录的同级私有目录 `.<数据目录名>-updates`，root 所有、0700；日志为普通 0600 文件。观察可在更新持锁期间继续进行，后续请求恢复没有活跃工作进程的中断任务。

Version eligibility is determined solely by the stable component tag. Protocol, API, capability and storage fingerprints never block installation; the authenticated Agent update channel also works without optional update-capability negotiation. Session generation and request identity checks remain required; release checksums, artifact architecture, runtime health and failure recovery remain required.

The installed product binary owns the transaction through an on-demand transient systemd unit or container. It survives the target restart, verifies releases, backs up private data, preserves deployment settings, checks health/reconnection and rolls back failure. It installs no separate update daemon. Root-private journals and backups live beside the data directory, outside the data backup itself. Keep the retained backups and old containers while rollback is needed.

## Deployment permissions / 部署权限

正常原生服务以 root 运行，才能替换受保护的程序并管理自身 systemd 单元。Core 的标准 Docker Compose 以 root 运行、保持只读根文件系统，并挂载宿主机 Docker socket；内置更新据此启动固定的临时工作容器。Docker socket 赋予 Core 宿主机容器管理权限，这扩大了 Core 的权限边界。Agent 始终 root 专用，已有的 0700 状态目录、0600 凭据和文件路径保护保持不变。NodeDance 不改变 Docker socket 的权限。

Native managed services run as root to replace their protected executables and manage their systemd units. Standard Core Compose runs root with a read-only filesystem and a host Docker socket for built-in update workers. Docker socket access grants host container-management authority; this is an expanded Core privilege boundary. Agent remains root-only with protected state and credentials. NodeDance does not change Docker socket permissions.

Tailscale 仍是可选能力。原生 Core 读取现有宿主机状态，Docker Core 通过固定的临时宿主机查询读取；NodeDance 不安装、配置或修改 Tailscale。

Tailscale remains optional and is only queried on the deployment host. NodeDance does not install or configure it.
