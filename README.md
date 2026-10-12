<h1 align="center">NodeDance</h1>

<p align="center"><a href="README.md">简体中文</a> | <a href="README_EN.md">English</a></p>

NodeDance 是一套自托管的 Linux 多主机监控与管理系统，由 **Core** 和部署在各主机上的 **Agent** 组成。

[Agent 扩展能力与 Komari 对照](docs/agent-capabilities.md)：扩展指标、GPU、账期流量、ICMP、批量脚本与探测、资产提醒、共享注册、HTTP 回退和自动更新，统一通过 Web / CLI / TUI 管理。

## 安装

### Core

在 Linux x86_64 或 ARM64 服务器上运行，安装脚本会自动选择最新稳定版：

```sh
curl -fsSL https://github.com/CST-Cat/NodeDance/releases/latest/download/install-core.sh | sudo bash
```

该命令安装 Core 程序。首次交互启动时可自定义登录账号名与密码，每项直接回车即自动生成高强度随机值；systemd 等无终端启动会自动生成两项，并保存在数据目录的 `0600` 权限文件 `initial-admin.json` 中，首次登录成功后删除。生产环境还需要配置 HTTPS 反向代理并运行 Core 服务，详见[部署指南](docs/installation.en.md#run-the-core)。

### Agent

登录 Core 后进入 **添加 Agent**，填写节点名称并生成安装命令。在目标 Linux 主机上运行该命令，再按提示输入一次性注册令牌。命令会自动选择并固定到最新已发布的 Agent 版本；Agent 以 root 服务运行。

Core 和 Agent 也支持 [Docker 部署](docs/docker.md)，提供独立镜像、Compose 示例和版本化发布。

## 功能

- **主机监控**：查看多台 Linux 主机的 CPU、内存、磁盘、网络和运行状态。
- **Docker 管理**：查看和管理容器、镜像及 Compose 项目。Docker 是可选能力；没有 Docker 时主机监控仍可使用。
- **远程运维**：通过 Web 管理终端和文件，并使用共享操作层提供的命令行与终端界面。
- **服务探测与告警**：执行 HTTP(S) 和 TCP 探测，配置告警规则、维护窗口及通知。
- **备份与恢复**：备份并恢复 Core 数据、密钥、节点身份和历史记录。
- **终端恢复**：Agent 内置完整 tmux，Web、CLI/TUI 断线后重新附着原会话，由 tmux 重绘画面；输入命令不重放。见 [终端恢复说明](docs/cli.md#terminal-recovery--终端恢复)。
- **Web 使用**：主题在首屏绘制前识别系统深浅色，并记住已保存的主题；桌面侧边栏可折叠、拖动调整宽度（200–400px），并在当前浏览器记忆。节点卡片默认收起 Docker 信息，点击展开；点击 VPS 名称进入详情，终端快捷按钮在当前总览弹窗。移动端保留抽屉导航。
- **Agent 部署**：在 Core 中注册节点并生成一键安装命令；检测到 Tailscale 时可使用现有网络能力辅助发现与部署。NodeDance 不安装或配置 Tailscale。

Agent 注册、恢复、运行、文件服务和 systemd 服务均要求 root 权限。

## 文档

- [详细部署与运维指南](docs/installation.en.md)
- [命令行与终端界面](docs/cli.md)
- [架构说明](docs/architecture.md)
- [版本发布说明](docs/releases.md)

正式更新 / Updates: [Web、CLI、TUI 更新、回滚与控制服务配置](docs/updates.md).
