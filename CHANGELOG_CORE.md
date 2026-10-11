# Core changelog / Core 更新日志

## [Unreleased]

### English

### 简体中文

## [0.1.8]

### English

- Style the shared Operations interface with the Web console's light/dark theme, accessible form controls, responsive layout, and bounded result panels.

### 简体中文

- 为共享操作中心补齐 Web 控制台的明暗主题、可访问表单控件、响应式布局和有界结果面板。

## [0.1.7]

### English

- Publish one Core operation contract and discover it dynamically in Web, CLI, and TUI. Generate Web operation forms, reuse canonical routes for all HTTP and WebSocket transports, and verify server-only operations are available without client changes.

- Share the Agent deployment-method request and schema across Web, CLI, and TUI. Select native/systemd or Docker in CLI with `--deployment-method` and in dedicated TUI enrollment prompts; return the selected command and both installation alternatives.
- Validate enrollment methods in Core before issuing a token, document the shared request, and test CLI request forwarding and TUI selection.

### 简体中文

- Core 发布统一操作契约，Web、CLI、TUI 动态读取。Web 自动生成操作表单，所有 HTTP 与 WebSocket 传输复用契约路由，并验证新增服务端操作无需客户端改动即可使用。
- Web、CLI、TUI 共用 Agent 部署方式请求与操作 schema。CLI 使用 `--deployment-method`，TUI 使用专用注册表单，均可选择直接/systemd 安装或 Docker，并返回选中命令及两种安装方案。
- Core 在签发令牌前验证部署方式，更新共享请求文档并测试 CLI 请求传递与 TUI 选择。

## [0.1.6]

### English

- Rebuild the Web console with the Claude design reference: warm surfaces, coral accents, responsive navigation, and coordinated light/dark themes.
- Generate a strong random administrator username and password on first unattended deployment; interactive startup supports custom values or Enter to generate either. Require username and password at login and remove the private initial-credentials file after successful login.
- Include initial administrator credentials in protected backups and update Web, CLI, and TUI authentication flows.
- Offer both direct/systemd and Docker Agent enrollment commands in the Web console, with hidden token input and published Agent image tag pinning.
- Add a non-root Core Docker image, persistent Compose deployment, native Docker integration checks, and version-pinned amd64/arm64 GHCR publication.
- Allow HTTP listeners behind HTTPS reverse proxies while retaining browser origin, CSRF, and secure-session protections.
- Add Chinese-first and English README pages, installation guidance, and bilingual Docker deployment documentation.

### 简体中文

- 参考 Claude 设计重构 Web：暖色背景、珊瑚色强调、响应式导航与统一的明暗主题。
- 首次无终端部署自动生成高强度随机管理员账号名与密码；交互启动支持自定义或回车生成。登录要求账号名与密码，首次成功登录后删除私有初始凭证文件。
- 受保护的备份包含初始管理员凭证，并同步更新 Web、CLI、TUI 认证流程。
- Web 注册页面同时提供直接/systemd 安装和 Docker 部署命令，隐藏输入注册令牌并固定已发布的 Agent 镜像版本。
- 增加非 root Core Docker 镜像、持久化 Compose 部署、原生 Docker 集成检查与固定版本的 amd64/arm64 GHCR 镜像发布。
- 支持在 HTTPS 反向代理后运行 HTTP 监听，保留浏览器 Origin、CSRF 与安全会话保护。
- 增加简体中文默认 README、English 入口、安装指南及双语 Docker 部署文档。

## [0.1.5]

### English

- Make the Core installer resolve the latest stable Core release by default while keeping optional tag pinning.
- Document the latest-release install command; Agent enrollment already resolves the latest stable Agent release.

### 简体中文

- Core 安装脚本默认解析最新正式版，同时保留指定 tag 安装的选项。
- 文档改为默认安装最新 Core；Agent 注册流程继续自动解析最新正式版 Agent。

## [0.1.4]

### English

- Publish gzip-compressed Linux amd64/arm64 Core archives and a version-pinned installer that verifies SHA256 before installing.
- Keep the raw Core binaries available and include both raw binaries and archives in `SHA256SUMS`.

### 简体中文

- 发布 Linux amd64/arm64 Core gzip 压缩包和固定版本的安装脚本；安装前校验 SHA256。
- 保留 Core 裸二进制，并将裸二进制和压缩包都纳入 `SHA256SUMS`。

## [0.1.3]

### English

- Build and test amd64 and arm64 releases concurrently on native runners; publish only after both pass.

- Apply connection-local SQLite limits, immutable indexed operation schemas, and byte-based protocol decoding.

- Add a shared operation catalog for CLI and TUI, interactive terminal access, and explicit Core/Agent capability negotiation.
- Support English and Simplified Chinese in Web, CLI and TUI; preserve operation identifiers and JSON data contracts.
- Release Core with `core-vMAJOR.MINOR.PATCH` independently of Agent. Begin each release description with a pinned link to the latest published stable Agent release.

### 简体中文

- 使用原生 runner 并行构建、测试 amd64 和 arm64，两个架构全部通过后才发布。

- 应用 SQLite 连接限额、不可变操作目录索引缓存，以及直接字节协议解码。

- CLI 和 TUI 共用操作目录，支持交互式终端及明确的 Core/Agent 能力协商。
- Web、CLI 和 TUI 支持简体中文和 English，操作标识符及 JSON 数据契约保持稳定。
- Core 使用 `core-vMAJOR.MINOR.PATCH` 独立发布；每次发布说明开头固定链接到最新已发布的正式 Agent release。
