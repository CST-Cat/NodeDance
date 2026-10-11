# Core changelog / Core 更新日志

## [Unreleased]

### English

### 简体中文

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
