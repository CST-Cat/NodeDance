# Agent changelog / Agent 更新日志

## [Unreleased]

### English

- Agents detect their node line type at installation — homelab from desktop chassis or wireless interfaces, nat from container virtualization, kvm only when an interface holds a public address — and report it during enrollment; machines behind provider NAT stay unlabeled rather than guessing, and the label is editable in node settings.
- Support automatic bounded tar.gz packaging and validated atomic extraction for directory peer transfers; regular files stream unchanged. Unify transfer bandwidth as decimal MB/s across the contract and clients.

### 简体中文

- Agent 安装时识别节点线路类型——桌面机箱或无线为 `homelab`，容器虚拟化为 `nat`，仅网卡直接持有公网地址才判 `kvm`，其余不猜测保持未标注——并在注册时上报；标签可在节点设置中修改。
- 文件夹对传自动进行有界 tar.gz 打包、校验和原子解包，普通文件保持原始传输；契约与客户端统一使用可带小数的 MB/s 限速。

## [0.1.34]

### English

- Add regular-file rsync between Agents in the mode selected per transfer: Agent direct WSS or Core relay (dedicated Core rsync relay, with the independent chunk relay when rsync is unavailable), with no automatic mode switching or fallback between modes. Direct attempts cover direct TCP, reverse and ICE connectivity (including coordinated TCP punching) and can use a node-declared listen/public port pair. Persistent WSS carries control and progress; retained private partials support retries. Receivers require a runtime argument template, Landlock confinement, one-use credentials, protected paths and SHA-256 verification before atomic publication.
- Embed a trimmed static rsync 3.4.1 for Linux amd64 and arm64. Native CI and Docker builds compile their architecture's bundle; availability checks, parameter probes and transfer processes execute only the verified, root-protected embedded executable, without a system rsync lookup or fallback.
- Refactor built-in update admission and recovery: ignore unknown journal and update metadata fields, skip corrupt history records individually, share history loading, submit manual updates without duplicate preflight and permit reinstallation, explicit downgrades and channel switches. Missing current-version labels do not block installation; native callers bound reply waits. Preserve artifact SHA-256, root credentials, authorization, request identity, backups, runtime health, reconnection and rollback.
- Preserve negotiated terminal colors and hyperlink capabilities through bundled tmux, and count default-route interfaces in network summaries. Local updates accept the shared channel selector while retaining Agent-only Dev validation.

### 简体中文

- 新增 Agent 间普通文件 rsync，按每次传输所选模式运行：Agent 直连 WSS 或 Core 中继（独立 Core rsync 中继，rsync 不可用时走独立分片中继），模式之间不自动切换、不互相兜底。直连尝试涵盖直连 TCP、reverse 与 ICE 连通（含双端协调的 TCP 打洞），并可使用节点声明的监听/对外端口对。常驻 WSS 承载控制与进度，私有 partial 保留供重试复用；接收端启动前验证运行时参数模板、Landlock、一次性凭证与路径围栏，提交前校验 SHA-256 并原子落盘。
- 为 Linux amd64、arm64 内置裁剪后的静态 rsync 3.4.1，原生 CI 与 Docker 按架构编译捆绑。可用性检查、参数探针和传输进程只执行经过校验、root 保护的包内程序，删除系统 rsync 查找与兜底。
- 重构内置更新准入与恢复：日志及更新元数据忽略未知字段，坏历史直接跳过，共用日志读取；手工更新直接提交，移除重复预检，允许重装、显式降级和通道切换。当前版本标记缺失不阻断安装；原生调用方限制回复等待。保留制品 SHA-256、root 凭据、授权、重试身份、备份、运行健康、重连及回滚。
- 经内置 tmux 保留协商后的终端颜色与超链接能力，网络汇总按默认路由接口计数；本机更新使用共用通道选择器，并保留 Agent 专属 Dev 校验。

## [0.1.33]

### English

- Add an opt-in Dev update channel: an Agent can follow per-commit Dev builds identified by the full commit SHA (`agent-dev-<sha>`), published as OCI artifacts on GHCR and resolved and downloaded anonymously through the OCI registry without any GitHub API calls; downloads verify SHA-256 and size and reuse the existing transactional update, runtime verification and rollback. Stable remains the default channel, and Dev builds never create release tags or GitHub Releases.
- Fix overwriting an existing file (upload commit and text save) failing with "operation not permitted" on hosts where changing the replacement file's owner is restricted even when it already matches the target's owner (for example unmapped ids in a user namespace): the owner is now only changed when it actually differs.

### 简体中文

- 新增可选的 Dev 更新通道：Agent 可跟随以完整提交 SHA 标识的逐提交 Dev 构建（`agent-dev-<sha>`），其制品以 OCI 形式发布于 GHCR，经 OCI 仓库匿名解析与下载，全程不调用 GitHub API；下载经 SHA-256 与大小校验，并复用既有的事务化更新、运行版本核验与回滚。Stable 仍为默认通道，Dev 构建不产生发布标签与 GitHub Release。
- 修复覆盖已有文件（上传落盘与文本保存）在属主变更受限环境下必定失败的问题：当替换文件属主与目标文件本就一致时（如用户命名空间中未映射的 id），仍发出 chown 系统调用会得到「operation not permitted」；现改为仅在属主确实不同时才变更。

## [0.1.32]

### English

- Reduce Agent update worker memory use with a 2 MiB Go soft target and one execution thread; stream downloads and backups through bounded buffers for constrained VPSs.

### 简体中文

- Agent 更新进程采用 2 MiB Go 软内存目标和单执行线程；下载与备份使用有界缓冲，降低小内存 VPS 的更新占用。

## [0.1.31]

### English

- Apply the 8 MiB Go soft memory target to Agent update workers when the runtime has no configured limit, while respecting any lower existing limit.

### 简体中文

- Agent 更新工作进程未配置内存上限时启用 8 MiB Go 软内存目标，并保留已有的更低限制。

## [0.1.30]

### English

- Bound native Agent download and binary replacement memory use for small VPSs, and remove the extra pre-install version process while retaining post-start runtime checks.

### 简体中文

- 限制原生 Agent 下载与二进制替换的内存占用，适配小内存 VPS；移除安装前额外启动的版本检查进程，保留启动后的运行状态核验。

## [0.1.29]

### English

- Copy and install release binaries through bounded streams so updates fit on low-memory (64 MB) Agents without whole-file heap copies.
### 简体中文

- 发布备份与安装改为限量流复制，避免把完整二进制读入堆内存，使更新可用于 64 MB 等低内存 Agent。
## [0.1.28]

### English

- Process typed Compose and image requests after their authenticated admission, removing repeated decoding and validation while preserving generation, capability and request-identity checks.
- Keep Compose save validation in the atomic Agent save transaction, including original-version conflict detection and candidate validation before replacement.
- Bound dense diagnostic entities with a binary search for the largest fitting ordered prefix, avoiding repeated full serialization while preserving payload limits and truncation metadata.

### 简体中文

- Compose 与镜像请求通过认证入口后使用类型化队列，移除重复解码和校验，保留代次、能力和请求身份保护。
- Compose 保存校验保留在 Agent 原子保存事务中，替换前检查原始版本冲突并验证候选配置。
- 密集诊断实体改用二分查找保留可容纳的最大有序前缀，避免重复序列化全部数据，保留载荷上限和截断标记。

## [0.1.27]

### English

- Keep root Agent local updates independent of Core login and connectivity, using the shared local maintenance workflow and regression coverage for release selection, observation and rollback cancellation.

### 简体中文

- root Agent 本机更新继续独立于 Core 登录与连接，统一使用本机维护流程，并验证版本选择、进度观察与回滚取消。

## [0.1.26]

### English

- Directory browsing computes metadata-only versions without reading file contents; explicit reads and mutations retain content-based conflict checks.
- Remove CPU pressure and connection tracking collection and metric definitions.

### 简体中文

- 目录浏览仅计算元数据版本，不读取文件内容；显式读取与修改保留内容版本冲突检查。
- 删除 CPU 压力和连接跟踪采集及指标定义。

## [0.1.25]

### English

- Remove application exporters, systemd service and filesystem detail scans, TCP quality counters, Swap paging rates, memory/I/O PSI and cgroup container collection, including dedicated settings and entity units. Retain CPU PSI, conntrack and process diagnostics.

### 简体中文

- 删除应用 exporter、systemd 服务和文件系统明细扫描、TCP 质量计数、Swap 换页速率、内存／I/O PSI 和 cgroup 容器采集，同时移除专用配置和实体单位字段；保留 CPU PSI、conntrack 和进程诊断。

## [0.1.24]

### English

- Add bounded host diagnostics for CPU steal and per-core usage, PSI, swap paging, disk IOPS and mean latency, inode usage, TCP retransmissions, conntrack and failed services.
- Collect bounded process rankings, cgroup v2 container throttling/OOM and configured application exporter metrics on a separate detail cadence; report observer cost and preserve unsupported, failed, warming and reset states.
- Add explicit IPv4/IPv6 probe selection and HTTP DNS/connect/TLS/first-byte timings with verified certificate expiry; explicit address families never fall back to another family.
- Read process short names directly without expanding command arguments, and bound diagnostic identities, payloads and scans.

### 简体中文

- 新增有界主机诊断：CPU steal 与每核心使用率、PSI、Swap 换页、磁盘 IOPS 与平均延迟、inode、TCP 重传、conntrack 和失败服务。
- 进程排行、cgroup v2 容器限流／OOM、配置的应用 exporter 指标使用独立明细周期并限制数量；上报采集器自身开销，区分未支持、失败、预热和重置状态。
- 探测可显式选择 IPv4／IPv6，并上报 HTTP DNS、连接、TLS、首字节耗时及经验证证书的到期时间；指定地址族后不跨族回退。
- 进程名称直接读取短名称，不展开命令参数；限制诊断标识、上报载荷和扫描开销。

## [0.1.23]

### English

- Resolve built-in update releases from the published static version files and construct release download URLs directly, removing the GitHub REST API from Agent update checks so per-IP rate limits cannot block upgrades.

### 简体中文

- 内置更新的版本解析改用发布时写好的静态版本文件并直接构造发布下载地址，Agent 更新检查不再调用 GitHub REST API，出口 IP 的 API 配额限制不会再阻塞升级。

## [0.1.22]

### English

- Resolve the latest stable Agent release from the static `agent-latest.txt` version file published at release time instead of the rate-limited GitHub REST API, keeping the installer on plain HTTPS downloads with an explicit `--release-tag` fallback.

### 简体中文

- 最新稳定 Agent 版本改为读取发布时写好的静态 `agent-latest.txt` 版本文件，不再依赖受限流配额的 GitHub REST API；安装路径只使用普通 HTTPS 下载，并保留 `--release-tag` 显式指定兜底。

## [0.1.21]

### English

- Allow built-in manual and automatic Agent updates based on stable Agent tags without protocol, capability, API, manifest or storage fingerprint eligibility gates; retain authenticated request identity, generation checks, artifact checksums, architecture selection, health checks and failure recovery.
- Remove the obsolete release manifest artifact and keep updates inside the existing product binary, with no separate updater package or persistent service.

### 简体中文

- 内置手动和自动 Agent 更新仅以稳定 Agent tag 判断资格，不再使用协议、能力、API、清单或存储指纹阻止更新；保留认证请求身份、代次检查、制品校验、架构选择、健康检查及失败恢复。
- 移除废弃发布清单附件，更新仍内置于产品二进制，不增加独立 updater 包或持久服务。

## [0.1.20]

### English

- Collect disk read/write throughput from elapsed-time deltas of whole hardware block devices, excluding partition and stacked-device duplicate counting. Report warm-up, counter reset and device changes as unavailable intervals instead of fabricated zero rates; retain sample age and independent snapshot ownership.

### 简体中文

- 根据整块硬件磁盘计数器的时间差采集读写速率，排除分区与叠加设备的重复计数。首帧预热、计数器重置和设备变化显示为不可用区间，不伪造零速率，并保留采样时效及快照独立性。

## [0.1.19]

### English

- Add confirmed Compose deployment deletion through the shared task journal; preserve volumes, images and configuration, verify container removal, and never replay uncertain mutations.

### 简体中文

- 通过共享任务日志增加已确认的 Compose 部署删除，保留数据卷、镜像与配置，验证容器已撤下，不重放不确定操作。

## [0.1.18]

### English

- Observe OpenSSH listeners, socket activation, login accounts and active sessions without conversation memory or manually entered SSH configuration; exclude forwarded sockets and unprivileged daemon impersonation.

### 简体中文

- 从机器上的 OpenSSH 监听端点、socket 激活、登录账号和活动会话识别 SSH，无需聊天记忆或手工填写连接配置；排除转发监听及非 root 进程伪装。

## [0.1.17]

### English

- Limit default disk capacity sampling to root and persistent storage mounts before statfs; exclude EFI, memory filesystems, kernel pseudo-filesystems and Docker runtime mounts while retaining real storage errors and explicit mount selections.
- Cover default and explicit mount selection, excluded runtime mounts, actual disk failures and container root storage with regression tests.

### 简体中文

- 默认磁盘容量采样在 statfs 前筛选根目录与持久存储挂载点，排除 EFI、内存文件系统、内核伪文件系统及 Docker 运行时挂载，同时保留真实存储故障与显式挂载点选择。
- 回归验证默认与自定义挂载点筛选、运行时挂载排除、实际磁盘读取失败及容器根目录存储。

## [0.1.16]

### English

- Initialize the built-in observation worker command executor so default update preflight can read the running version; verify native and real Docker checks without taking the transaction lock.

### 简体中文

- 初始化内置观察进程的命令执行器，使默认更新预检查能够读取运行版本；覆盖无需获取事务锁的原生及实际 Docker 检查。

## [0.1.15]

### English

- Default installation to the latest stable published Agent without a hardcoded tag, and explicitly run Docker enrollment/runtime as root.

- Add a default built-in Agent update command and use the same binary for transient update workers; remove updater installation and separate release assets while preserving transaction checks and recovery.

### 简体中文

- 安装默认选择最新正式 Agent，不在命令中固定 tag；Docker 注册及运行显式使用 root。

- 增加默认内置 Agent 更新命令，并使用同一二进制执行临时更新任务；移除 updater 安装和独立发布产物，保留更新校验与故障恢复。

## [0.1.14]

### English

- Unify root-only native and Docker installation in the published installer with an explicit deployment mode and supplied enrollment token; preserve protected state and recovery diagnostics on partial failures.
- Verify and install the update supervisor for both deployment modes, retaining pinned release artifacts and root credential protection.

### 简体中文

- 在发布安装器中统一 root 专用原生与 Docker 安装，显式指定部署方式并使用命令携带的注册凭据；部分失败时保留受保护状态与恢复诊断。
- 两种部署方式均校验并安装更新监督器，继续固定发布产物并保护 root 凭据。

## [0.1.13]

### English

- Harden container rebuild recovery, task lifecycles, terminal and stream cleanup, probe execution, metrics persistence and systemd handling; remove redundant compatibility paths and add focused regressions.
- Improve file service safety and bounded transfer behavior, correct host/network metric reporting, and make agent release images use native build artifacts with reusable build inputs.

### 简体中文

- 加固容器重建恢复、任务生命周期、终端与流清理、探测执行、指标持久化及 systemd 处理；移除冗余兼容路径并补充针对性回归。
- 改进文件服务安全与有界传输、修正主机／网络指标，并让 Agent 发布镜像复用原生构建产物与构建输入。

## [0.1.12]

### English

- Add shared ZIP/TAR/TAR.GZ archive creation and ZIP/TAR/gzip/bzip2 extraction with complete manifest validation, link/special-file rejection, expansion bounds and directory modes; expand versioned text editing to 8 MiB.
- Size file browse pages after version metadata is attached, and preserve basic directory permissions during copy and ZIP extraction. Add escaped long-path envelope and restrictive-directory regressions.

### 简体中文

- 增加 ZIP／TAR／TAR.GZ 创建及 ZIP／TAR／gzip／bzip2 解压，共用完整清单检查、链接／特殊文件拒绝、展开上限与目录权限保留；版本化文本编辑提高到 8 MiB。
- 附加版本信息后再限制目录分页消息大小；复制与 ZIP 解压保留目录基本权限，增加长路径转义消息与严格目录权限回归。

## [0.1.11]

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
