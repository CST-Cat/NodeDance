# Docker 部署 / Docker deployment

Core 与 Agent 分别提供镜像，支持 Linux amd64 / arm64。可以从源码构建；发布工作流会在对应 `core-v*` / `agent-v*` 标签通过测试后推送 GHCR 镜像。使用已发布的完整版本标签：`ghcr.io/cst-cat/nodedance-core:core-vMAJOR.MINOR.PATCH`、`ghcr.io/cst-cat/nodedance-agent:agent-vMAJOR.MINOR.PATCH`。将占位符替换为实际已发布版本，两个组件分别选定版本。

Core and Agent have separate Linux amd64/arm64 images. Build from source, or use the full published Core and Agent tags shown above. Release workflows publish GHCR images after native tests. Set `NODEDANCE_CORE_IMAGE` or `NODEDANCE_AGENT_IMAGE` to a published image, run `docker compose … pull`, then `docker compose … up -d --no-build`. First publication may require the repository owner to make the GHCR packages public.

## Core

在仓库根目录执行 / From the repository root:

```sh
docker compose -f deploy/docker/compose.core.yml up -d --build
docker compose -f deploy/docker/compose.core.yml exec core cat /var/lib/nodedance/initial-admin.json
```

Core 默认监听宿主机 `127.0.0.1:8180`，以 UID/GID `10001` 运行，数据卷 `core-data` 持久保存数据库、密钥、备份及首次登录凭证。首次后台启动自动生成高强度随机账号名和密码；凭证文件为 `0600`，数据目录为 `0700`，首次成功登录后凭证文件删除。重建容器保留账号；不要删除数据卷。

Core binds to host `127.0.0.1:8180`, runs as UID/GID 10001, and stores its database, keys, backups, and initial credentials in the `core-data` volume. Unattended startup generates a strong random username and password. The credentials file is mode 0600 in a mode 0700 directory and is deleted after successful login. Container replacement preserves the account; retain the data volume.

希望首次部署时自定义账号与密码，先构建再交互启动，每项直接回车自动生成；完成初始化后按 Ctrl-C 停止，再后台启动：

To customize credentials on first deployment, build and start interactively; press Enter to generate either value. Stop with Ctrl-C after initialization and restart in the background:

```sh
docker compose -f deploy/docker/compose.core.yml build
docker compose -f deploy/docker/compose.core.yml run --rm --service-ports core
docker compose -f deploy/docker/compose.core.yml up -d
```

通过 HTTPS 反向代理访问 Web。可通过 Compose 覆盖文件设置 `NODEDANCE_PUBLIC_ORIGIN=https://nodes.example.com` 与 `NODEDANCE_TRUSTED_PROXIES`（实际代理 IP/CIDR）。代理须转发 WebSocket。宿主机代理访问 `127.0.0.1:8180`；容器代理应与 Core 加入同一 Docker 网络，转发至 `core:8180`。需要改变宿主机端口或绑定地址时设置 `CORE_PORT` / `CORE_BIND_ADDRESS`。Core 本身不需要 Docker socket 或特权。

Access the Web console through an HTTPS reverse proxy with WebSocket forwarding. Set `NODEDANCE_PUBLIC_ORIGIN` and `NODEDANCE_TRUSTED_PROXIES` (the actual proxy IP/CIDR) using a Compose override. A host proxy connects to 127.0.0.1:8180; a container proxy should share a Docker network and connect to core:8180. `CORE_PORT` / `CORE_BIND_ADDRESS` change the published port/address. Core requires no Docker socket or privileges. See the [transport configuration](installation.en.md).

## Agent

Agent 管理宿主机，需要 **Linux rootful Docker、root 用户、privileged、host PID 与网络、宿主机根目录挂载**。入口通过 `nsenter` 进入宿主机命名空间与根目录，让指标、终端、文件服务和 Docker/Compose 操作使用真实宿主机环境。Docker Desktop 的容器只能看到其 Linux 虚拟机，不能用此方式管理桌面宿主机。不要同时运行该节点的 systemd Agent 与 Docker Agent。

Agent manages the host and requires **rootful Docker on Linux, root, privileged mode, host PID/network, and a host-root bind mount**. Its entrypoint uses `nsenter` to enter the host namespaces and root, so metrics, terminals, file management, and Docker/Compose operate on the actual host. Docker Desktop exposes its Linux VM rather than the desktop host. Run one Agent per node; stop a systemd Agent before replacing it with a Docker Agent.

在目标主机准备私有目录并构建镜像 / Prepare private host state and build on the target host:

```sh
sudo install -d -o root -g root -m 0700 /var/lib/nodedance-agent
docker compose -f deploy/docker/compose.agent.yml build
```

在 Core 的“添加 Agent”中生成一次性注册令牌，然后在 **Bash** 中运行（输入令牌不回显，且不放入命令行参数、环境或历史）：

Generate a one-time enrollment token in Core's Add Agent screen, then run in **Bash** (the token is hidden and is passed through stdin rather than arguments, environment, or history):

```bash
read -r -s -p 'Enrollment token: ' enrollment_token; printf '\n'
printf '%s\n' "$enrollment_token" | docker compose -f deploy/docker/compose.agent.yml run --rm -T agent \
  enroll --server https://nodes.example.com --token-stdin --config /var/lib/nodedance-agent/agent.json
unset enrollment_token
docker compose -f deploy/docker/compose.agent.yml up -d
```

配置文件持久保存在宿主机 `/var/lib/nodedance-agent/agent.json`，必须 root 所有、普通文件、`0600`，目录为 `0700`。入口进入宿主机后，`--config`、`--ca-file` 等路径均为 **宿主机路径**。私有 CA 请先放置在宿主机，再传入 `--ca-file`。恢复注册时将 `enroll` 换成 `recover`，使用 Core 新签发的恢复令牌。容器重启直接读取现有配置，不需要再次注册。

Config persists on the host at `/var/lib/nodedance-agent/agent.json`, root-owned, regular and mode 0600 in a mode 0700 directory. After namespace entry, `--config`, `--ca-file`, and other paths refer to **host paths**. Put private CA files on the host before passing `--ca-file`. For recovery use `recover` with a newly issued recovery token. Restarts reuse the existing config.

Agent 使用宿主机的 CA、shell、Docker socket 及 Docker CLI / Compose 插件；Compose 管理要求宿主机已安装相应 CLI/插件。文件服务继续阻止目录穿越、符号链接、Agent 凭证和内核/运行时目录。部署不会安装或配置 Tailscale。

Agent uses the host CA store, shells, Docker socket, and Docker CLI/Compose plugin. Install the host CLI/plugin for Compose management. File management continues to reject traversal, symlinks, Agent credentials, and protected kernel/runtime paths. Deployment does not install or configure Tailscale.

## 更新与运行检查 / Updates and checks

```sh
docker compose -f deploy/docker/compose.core.yml ps
docker compose -f deploy/docker/compose.core.yml logs --tail 50 core
docker compose -f deploy/docker/compose.agent.yml logs --tail 50 agent
```

更新源码后重新 `up -d --build`；使用发布镜像时更新固定标签，`pull` 后 `up -d --no-build`。更新 Core 前备份数据；保留 Core 数据卷及 Agent 宿主机状态目录。停止服务用对应 Compose 文件执行 `down`；Core 的 `down -v` 会删除其数据卷。

For source builds, rebuild with `up -d --build`. For published images, update the pinned tag, pull, and use `up -d --no-build`. Back up Core before updates and preserve its volume and the Agent host state directory. Use `down` with the respective Compose file to stop; Core's `down -v` deletes its data volume.

## 本地验证 / Local verification

```sh
docker build -f deploy/docker/Dockerfile.core -t nodedance-core:docker-test .
docker build -f deploy/docker/Dockerfile.agent -t nodedance-agent:docker-test .
python3 scripts/test-docker.py
```

该测试创建并清理专用临时容器、数据卷和主机目录，验证首次账号生成、持久化、登录、Agent 注册、宿主机指标/文件、保护规则与重启重连。CI 分别在原生 amd64 和 arm64 Linux runner 上执行。

This opt-in test creates and cleans up dedicated containers, a volume, and a host directory. It checks initial credentials, persistence, login, Agent enrollment, host metrics/files, access protection, and reconnects. CI runs it on native amd64 and arm64 Linux runners.
