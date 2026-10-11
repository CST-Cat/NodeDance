# Independent releases / 独立发布

Core and Agent are versioned independently:

| Component / 组件 | Tag / 标签 | Changelog / 更新日志 | Assets / 产物 |
| --- | --- | --- | --- |
| Core | `core-vMAJOR.MINOR.PATCH` | `CHANGELOG_CORE.md` | Embedded Web + CLI/TUI Linux amd64/arm64 binaries and `.tar.gz` archives, latest-resolving installer, SHA256SUMS, version-pinned GHCR Docker image |
| Agent | `agent-vMAJOR.MINOR.PATCH` | `CHANGELOG_AGENT.md` | Root Agent Linux amd64/arm64 binaries, installer, SHA256SUMS, version-pinned GHCR Docker image |

Core 的 Web、CLI、TUI 同时打包发布。Agent 独立发布，只有 Agent 相关改动才需要更新 Agent 版本。Agent 和 Core 的运行互通取决于协议版本及能力协商，无需发布版本号相同。

Every version entry needs reviewed `### English` and `### 简体中文` sections. Maintain new changes in each component's `[Unreleased]` entry, then prepare a version before creating its tag:

每个版本必须包含经审阅的 `### English`、`### 简体中文` 内容。变更先记录到相应组件的 `[Unreleased]`，创建标签前准备版本条目：

```sh
# Example versions; choose the intended component/version.
python3 scripts/release-notes.py promote agent agent-v0.1.0
python3 scripts/release-notes.py render agent agent-v0.1.0
# Review and commit the changelog, then create/push the matching tag.
# Publish Agent first if the repository has no stable Agent release yet.
python3 scripts/release-notes.py promote core core-v0.1.0
```

Only `agent-v*` tags trigger `.github/workflows/agent-release.yml`; only `core-v*` tags trigger `.github/workflows/core-release.yml`. Both reject invalid semver or missing bilingual notes. The Core workflow lists every release page, excludes drafts and prereleases, selects the stable Agent release with the latest publication timestamp, and places its **exact tag URL as the first line** of the Core release description. If none exists, Core publication fails before building/publishing. Publishing Agent marks `--latest=false`; Core marks `--latest=true`.

两个工作流分别由自己的标签触发，并拒绝无效版本或缺少双语说明的发布。Core 工作流读取所有发布页，排除草稿和预发布，按发布时间选取最新正式 Agent，并在 Core release 说明第一行固定其**确切标签链接**。没有正式 Agent 时，Core 发布会在构建和发布前失败。Agent 不占用仓库全局 latest，Core 为全局 latest。

Agent installers resolve a published `agent-v*` tag and pin it. They do not use the repository-wide `/releases/latest` URL, which belongs to Core. Credentials remain root-owned, with a 0700 state directory and 0600 regular config file.

Agent 安装器解析并固定已发布的 `agent-v*` 标签，不使用指向 Core 的仓库 `/releases/latest`。Agent 凭据继续要求 root 所有、0700 状态目录和 0600 普通配置文件。

## Native parallel builds / 原生并行构建

Both workflows use a build matrix: `ubuntu-24.04` for amd64 and `ubuntu-24.04-arm` for arm64. Each runner verifies its native Go architecture, compiles its own binary without a `GOARCH` override, executes that release binary to check its version, and runs Go tests natively. Tests run with root privileges so root-only Agent and integration cases are exercised. Build caches are separate by runner architecture; `-count=1` makes every release execute tests rather than reuse cached test results.

两个发布工作流都使用 matrix：amd64 对应 `ubuntu-24.04`，arm64 对应 `ubuntu-24.04-arm`。两台机器并行确认原生 Go 架构、编译自身架构的二进制（不覆盖 `GOARCH`）、执行发布二进制检查版本，并运行原生 Go 测试。测试以 root 运行，覆盖 root 专用 Agent 与集成测试。Go 缓存按 runner 架构分别保存；`-count=1` 保证每次发布实际执行测试，不复用测试结果缓存。

Core prepares and checks Web assets once, then shares the same assets with both native builds. Release publication waits for **both** matrix jobs to pass, merges their artifacts, restores executable permissions lost during artifact transfer, creates one `.tar.gz` archive per architecture, and generates one checksum manifest covering both raw binaries and archives. The Core installer is published alongside these assets and resolves the latest stable Core release by default; it can also install an explicitly pinned Core tag. A failed build or test blocks publication; the other architecture still completes to provide diagnostics.

Core 的 Web 资源只构建和检查一次，两台原生 runner 使用相同资源。发布必须等待**两个架构**全部通过，随后汇总产物、恢复 artifact 传输丢失的执行权限、为每个架构创建一个 `.tar.gz`，并为裸二进制和压缩包生成统一校验清单。Core 安装脚本也会随 release 发布，默认解析最新正式 Core 版本，也可指定 tag 安装。任一架构构建或测试失败都会阻止发布，另一架构继续运行以保留诊断结果。

## Docker images / Docker 镜像

Release workflows publish `ghcr.io/cst-cat/nodedance-core:core-vX.Y.Z` and `ghcr.io/cst-cat/nodedance-agent:agent-vX.Y.Z` for Linux amd64/arm64 after native binary tests pass. GitHub Release publication also waits for the image job. Images have separate component tags; deployment examples and container integration checks are described in [Docker deployment](docker.md).

原生二进制测试通过后，发布工作流推送两个架构的固定版本 GHCR 镜像；GitHub Release 也等待镜像任务成功。Core 与 Agent 使用各自的标签，部署示例与容器集成检查详见 [Docker 部署](docker.md)。
