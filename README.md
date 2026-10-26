<h1 align="center"><img src="web/public/logo.svg" width="32" height="32" alt="" /> NodeDance</h1>

<p align="center"><a href="README.md">简体中文</a> | <a href="README_EN.md">English</a></p>

NodeDance 是一套自托管的 Linux 多主机监控与管理系统，由 **Core** 和部署在各主机上的 **Agent** 组成。

Web、CLI、TUI 共用 Core 操作契约；CLI 与 TUI 完整覆盖同一终端能力，Web 可提供图形化交互。参见[接口能力与验证对照](docs/interface-coverage.md)及[终端使用说明](docs/cli.md)。

[Agent 扩展能力与 Komari 对照](docs/agent-capabilities.md)：扩展指标、GPU、账期流量、ICMP、批量脚本与探测、资产提醒、共享注册、HTTP 回退和自动更新，统一通过 Web / CLI / TUI 管理。

## 安装

### Core

在 Linux x86_64 或 ARM64 服务器上运行，安装脚本会自动选择最新稳定版：

```sh
curl -fsSL https://github.com/CST-Cat/NodeDance/releases/latest/download/install-core.sh | sudo bash
```

该命令安装 Core 程序并自动创建、启用和启动 root `nodedance.service`，监听 `0.0.0.0:8180`，数据保存在 `/var/lib/nodedance`。首次无终端启动会自动生成账号和密码，写入私有数据目录内 `0600` 权限的 `initial-admin.json`，首次登录成功后删除。生产环境还需配置 HTTPS 反向代理，详见[部署指南](docs/installation.en.md#run-the-core)。

### Agent

登录 Core 后进入 **添加 Agent**，填写节点名称并生成安装命令。选择左右排列的直接安装或 Docker 部署，复制完整命令到目标 Linux 主机运行即可，无需再次输入 Token。命令执行时自动解析最新稳定 Agent release；Agent 以 root 运行，Token 过期时间显示北京时间。

Core 原生安装到主控机，安装器会自动创建并启动 root `nodedance.service`，监听 `0.0.0.0:8180`，数据保存在 `/var/lib/nodedance`。仅 Agent 支持 [Docker 部署](docs/docker.md)。

## 功能

- **主机监控**：查看多台 Linux 主机的 CPU、内存、磁盘、网络和运行状态。
- **Docker 管理**：查看和管理容器、镜像及 Compose 项目。Docker 是可选能力；没有 Docker 时主机监控仍可使用。
- **远程运维**：通过 Web 管理终端和文件，并使用共享操作层提供的命令行与终端界面。
- **服务探测与告警**：执行 HTTP(S) 和 TCP 探测，配置告警规则、维护窗口及通知。
- **备份与恢复**：备份并恢复 Core 数据、密钥、节点身份和历史记录。
- **文件管理**：shadcn／UIArc 列表与网格，共用自绘右键菜单，elFinder 仅接入命令能力，支持分页／搜索、复制移动、ZIP 压缩解压、版本化编辑、文件／目录上传和受控预览；共用 Core／Agent 文件服务，见[文件管理说明](docs/file-manager.md)。右键支持文件／文件夹分享，提供有效期、次数、密码、直链、二维码、撤销及文件收集，见[文件分享](docs/file-sharing.md)。
- **终端恢复**：Agent 内置完整 tmux，Web、CLI/TUI 断线后重新附着原会话，由 tmux 重绘画面；输入命令不重放。见 [终端恢复说明](docs/cli.md#terminal-recovery--终端恢复)。
- **Web 使用**：主题在首屏绘制前识别系统深浅色，并记住已保存的主题；桌面侧边栏可折叠、拖动调整宽度（200–400px），并在当前浏览器记忆。节点卡片默认收起 Docker 信息，点击展开；点击 VPS 名称进入详情，终端快捷按钮在当前总览弹窗。移动端保留抽屉导航。
- **Agent 部署**：在 Core 中注册节点并生成一键安装命令；检测到 Tailscale 时可使用现有网络能力辅助发现与部署。NodeDance 不安装或配置 Tailscale。

Agent 注册、恢复、运行、文件服务和 systemd 服务均要求 root 权限。

## 文档

- [详细部署与运维指南](docs/installation.en.md)
- [命令行与终端界面](docs/cli.md)
- [节点总览、主机信息与备忘录](docs/overview.md)
- [架构说明](docs/architecture.md)
- [版本发布说明](docs/releases.md)

正式更新 / Updates: [Web、CLI、TUI 更新、回滚与控制服务配置](docs/updates.md).

Core 内置 [RSS／Atom 订阅中心](docs/rss.md)，支持多档刷新（最低 1 分钟，默认 30 分钟）、共享阅读状态、关键词通知和摘要。

节点详情页顶部提供“删除节点”，支持等待注册、在线、离线和已撤销节点。删除会移除 Core 数据及凭据，不会卸载远端 Agent 或停止已下发任务。等待注册节点可打开“安装命令”，重新生成并重复复制 systemd/Docker 命令；原节点保持不变，旧 Token 立即失效。
