# Agent changelog / Agent 更新日志

## [Unreleased]

### English

- Extend the existing file service with bounded cursor browsing/search, staged no-overwrite copy and ZIP archive/extraction, 128 KiB versioned text editing and recovery cleanup. Pin directory descriptors for all file roots and reject credential ancestors during rename/delete.

### 简体中文

- 扩展现有文件服务，支持有界游标浏览／搜索、暂存后禁止覆盖的复制与 ZIP 压缩解压、128 KiB 版本化文本编辑及恢复清理；所有文件根目录使用固定目录描述符，重命名／删除同时拒绝凭据所在的父目录。

## [0.1.10]

### English

- Extend agent.host.v1 with configurable command history retention and a shared cleanup operation. Compact completed command output into a private SQLite archive while retaining retry identities; preserve uncertain jobs and report result-persistence failures honestly.

### 简体中文

- 扩展现有 agent.host.v1，增加命令历史保留设置与共用清理操作；将已完成命令压缩到私有 SQLite 归档并保留去重身份，保留不确定任务，明确报告结果写盘失败。

## [0.1.9]

### English

- Embed complete upstream tmux 3.7c static builds for Linux amd64/arm64, with pinned checksums, terminfo and all dependency notices; no runtime installation is required.
- Manage host and Docker terminal sessions with dedicated tmux servers; detach/attach preserves the original shell and jobs, while new Docker panes stay in the selected container.
- Use the running Agent executable inode for Docker bridge children after host namespace entry; terminate every Docker Exec before closing its connection on session end.
- Exercise real shell/job recovery, full pane support, Docker bridge cleanup and root-only state in native release checks; retain credential and filesystem protections.

### 简体中文

- 内置完整上游 tmux 3.7c，支持 Linux amd64/arm64 静态构建，固定校验值并附带终端描述及所有依赖许可证，无需运行时安装。
- 主机及 Docker 终端使用独立 tmux 服务器；分离/附着保留原 shell 和任务，Docker 新窗格继续进入选定容器。
- 进入宿主机命名空间后，Docker 桥接子进程使用运行中 Agent 的实际可执行文件；结束会话时先终止所有 Docker Exec，再关闭连接。
- 原生发布检查覆盖真实 shell/任务恢复、完整分屏、Docker 桥接清理及 root 私有状态，保留凭据和文件系统保护。

## [0.1.8]

### English

- Add extended host/GPU metrics, selectable memory accounting, configurable sampling and networking, custom/public IP reporting, and durable billing-period traffic with explicit partial coverage.
- Add durable, idempotent root script jobs and cancellation, remote-control disabling, redacted diagnostics and optional supervised automatic updates; preserve private root state and existing file protections.
- Support authenticated HTTP fallback and compression alongside WebSocket, plus IPv4/IPv6 ICMP probes. Extended metrics use the new agent.metrics.v2 capability; deploy the matching Core to consume them.

### 简体中文

- 新增主机/GPU 扩展指标、可选内存统计、动态采样与网络配置、自定义/公网地址及持久化账期流量，并明确标记不完整覆盖。
- 新增持久化幂等 root 脚本任务与取消、远程控制禁用、脱敏诊断及可选的受控自动更新；保留 root 私有状态和现有文件保护。
- 支持认证 HTTP 回退与压缩，以及 IPv4/IPv6 ICMP 探测。扩展指标采用新的 agent.metrics.v2 能力，需部署配套 Core 使用。


## [0.1.7]

### English

- Advertise the update capability in the actual Agent connection handshake so Web, CLI and TUI can reach the installed supervisor through Core; cover the real runtime hello in a regression test.

### 简体中文

- 在 Agent 实际连接握手中声明更新能力，使 Web、CLI 和 TUI 能通过 Core 调用已安装的更新服务；补充实际运行握手的回归测试。

## [0.1.6]

### English

- Fix Docker Agent update checks and restart verification by reading the executable of the registered container process after it enters the host mount namespace; reject stopped containers and invalid process IDs.

### 简体中文

- 修复 Docker Agent 进入宿主机挂载命名空间后更新检查及重启验证无法找到程序的问题，改为读取已注册容器运行进程的可执行文件；拒绝已停止的容器及无效进程编号。

## [0.1.5]

### English

- Add authenticated generation-bound update requests and an independent root supervisor for Docker and systemd deployments, with verified releases, credential-preserving restart checks, durable recovery and rollback. Publish updater artifacts and install the supervisor during native and generated Docker enrollment.

### 简体中文

- 新增通过认证且绑定连接代次的更新请求及 Docker/systemd 独立 root 更新控制服务，支持版本校验、保留凭据与重连验证、持久化恢复和回滚；发布更新器并在原生安装及生成的 Docker 注册命令中自动安装。

## [0.1.4]

### English

- Add a root-only Docker image and Compose deployment that enter the host namespaces and root for host metrics, terminals, file management, and Docker/Compose operations.
- Preserve root-owned 0700 state and 0600 credentials through enrollment, recovery, restarts, and protected host file access.
- Publish version-pinned amd64/arm64 GHCR images from Agent release tags and verify container enrollment, host access protections, and reconnects in native Docker integration CI.
- Document Docker deployment, hidden stdin enrollment tokens, persistent state, and host CA/CLI requirements in Chinese and English.

### 简体中文

- 增加仅 root 运行的 Docker 镜像和 Compose 部署，进入宿主机命名空间与根目录，提供宿主机指标、终端、文件与 Docker/Compose 管理。
- 注册、恢复及重启保留 root 所有的 0700 状态目录与 0600 凭证，并继续执行宿主机文件访问保护。
- Agent 版本标签发布固定版本的 amd64/arm64 GHCR 镜像；原生 Docker 集成 CI 验证容器注册、宿主机访问保护与重连。
- 增加双语 Docker 部署文档，说明隐藏输入注册令牌、持久状态及宿主机 CA/CLI 要求。

## [0.1.3]

### English

- Build and test amd64 and arm64 releases concurrently on native runners; publish only after both pass.

- Reuse and scrub file transfer buffers; decode protocol bytes without an intermediate string copy.

- Advertise supported protocol capabilities and validate Core welcome capabilities independently of the Core release version.
- Support English and Simplified Chinese lifecycle command help and prompts.
- Publish only from `agent-vMAJOR.MINOR.PATCH` tags, with reviewed bilingual release notes; installers resolve and pin a published Agent tag.

### 简体中文

- 使用原生 runner 并行构建、测试 amd64 和 arm64，两个架构全部通过后才发布。

- 复用文件传输缓冲区，归还前清空内容；直接解码协议字节，避免中间字符串拷贝。

- 上报支持的协议能力，独立于 Core 发布版本验证 Core welcome 能力。
- 生命周期命令帮助和提示支持简体中文与 English。
- 仅使用 `agent-vMAJOR.MINOR.PATCH` 标签发布，提供经审阅的双语发布说明；安装器解析并固定已发布的 Agent 标签。
