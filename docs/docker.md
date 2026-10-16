# Agent Docker deployment / Agent Docker 部署

NodeDance Core runs natively on the controller host as the root-owned `nodedance.service`; Docker is supported only for per-host Agents. The Agent image is published for Linux amd64 and arm64 as `ghcr.io/cst-cat/nodedance-agent:agent-vMAJOR.MINOR.PATCH`.

NodeDance Core 必须原生安装在主控机上，由 root 运行 `nodedance.service`；Docker 仅用于各主机上的 Agent。Agent 镜像支持 Linux amd64 和 arm64，发布地址为 `ghcr.io/cst-cat/nodedance-agent:agent-vMAJOR.MINOR.PATCH`。

## Requirements / 环境要求

The Agent needs rootful Docker on Linux, root, privileged mode, host PID/network namespaces, and a read-only bind of the host root. Its entrypoint enters the host namespaces so monitoring and file/terminal operations target the real host. Docker Desktop exposes its Linux VM rather than the desktop host. Run only one Agent per host; stop the systemd Agent before switching that host to Docker.

Agent 需要 Linux 上的 rootful Docker、root、privileged、宿主机 PID／网络命名空间，以及只读挂载的宿主机根目录。入口进入宿主机命名空间，确保监控、文件和终端功能作用于真实主机。Docker Desktop 暴露的是 Linux 虚拟机；切换 Docker Agent 前，先停止同一主机上的 systemd Agent。

Prepare root-owned private state on the target host and build the image there:

```sh
sudo install -d -o root -g root -m 0700 /var/lib/nodedance-agent
docker compose -f deploy/docker/compose.agent.yml build
```

Core 的“添加 Agent”页面提供直接安装（systemd）和 Docker 部署选项。选择 Docker 后，复制完整生成命令并在目标 Linux 主机运行。命令不固定 tag，执行时解析最新稳定 Agent release、校验 SHA256、完成注册并启动 root 容器。安装主机需要 Python 3 解析分页后的 release 列表。

The Core Add Agent page offers native/systemd and Docker deployment. Choose Docker, copy the generated command, and run it on the target Linux host. It resolves the latest stable Agent release at execution time, verifies SHA256, enrolls, and starts the root container. Python 3 is required to parse the paginated release list.

The configuration is stored on the host at `/var/lib/nodedance-agent/agent.json`, as a root-owned regular file with mode `0600` in a `0700` directory. After namespace entry, configuration and CA paths are host paths. Restarts reuse the saved credentials; recovery uses a newly issued Core recovery token. The installer does not install or configure Tailscale.

Agent 配置保存在宿主机 `/var/lib/nodedance-agent/agent.json`，要求 root 所有、普通文件 `0600`，目录 `0700`。进入宿主机命名空间后，配置和 CA 路径均指宿主机路径。重启会复用现有凭据；恢复注册使用 Core 新签发的恢复 Token。安装器不会安装或配置 Tailscale。

## Updates and checks / 更新与验证

Agent updates are built in. `nodedance agent update` updates the local Agent; the Core update center and `nodedance update --component agent --node-id NODE_UUID` can update a remote Docker Agent through the existing rootful Docker runtime. No separate updater image or daemon is installed.

Agent 更新已内置：`nodedance agent update` 更新本机 Agent；Core 更新中心和 `nodedance update --component agent --node-id NODE_UUID` 可通过现有 rootful Docker runtime 更新远端 Docker Agent。不会安装独立 updater 镜像或守护进程。

To run the isolated Agent Docker integration against a locally built native Core:

```sh
docker build -f deploy/docker/Dockerfile.agent -t nodedance-agent:docker-test .
docker compose -f deploy/docker/compose.agent.yml config -q
TEST_CORE_BINARY=.build/nodedance python3 scripts/test-docker.py
```

The integration uses a temporary Core data directory, one temporary Agent container, and disposable host fixtures. It checks protected state, enrollment, root execution, host metrics and files, path protections, and reconnect behavior.

集成测试只使用临时 Core 数据目录、Agent 容器及临时主机 fixture，验证凭据保护、注册、root 运行、主机指标与文件、路径保护和重连。
