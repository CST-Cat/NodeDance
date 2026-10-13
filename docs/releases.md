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
python3 scripts/release-notes.py promote core core-v0.1.0
# Set agent-release.version to the intended exact Agent tag before tagging Core.
# Agent and Core tags may then be pushed together; builds run independently.
```

Only `agent-v*` tags trigger `.github/workflows/agent-release.yml`; only `core-v*` tags trigger `.github/workflows/core-release.yml`. Both reject invalid semver or missing bilingual notes. `agent-release.version` pins the exact Agent paired with Core at the tagged commit. Core validates that pairing and its changelog before building, without a release API dependency. Only final publication waits for that exact stable, published Agent (up to ten minutes); a different/newer release cannot replace the pairing. Its **exact tag URL is the first line** of the Core description. Publishing Agent marks `--latest=false`; Core marks `--latest=true`.

两个工作流分别由自己的标签触发，并拒绝无效版本或缺少双语说明的发布。创建 Core tag 前，`agent-release.version` 必须写明本次配对的 Agent tag。构建与测试不等待 Agent 发布；最终发布最多等待十分钟，确认这个确切版本已正式发布、不是草稿或预发布，再写入说明第一行。不会自动换成恰好最新的其他版本。两种 tag 可以一起推送，使构建并行；超时只阻止 Core 最终发布，不修改配对版本。Agent 不占用仓库全局 latest，Core 为全局 latest。

Agent installers resolve a published `agent-v*` tag and pin it. They do not use the repository-wide `/releases/latest` URL, which belongs to Core. Credentials remain root-owned, with a 0700 state directory and 0600 regular config file.

Agent 安装器解析并固定已发布的 `agent-v*` 标签，不使用指向 Core 的仓库 `/releases/latest`。Agent 凭据继续要求 root 所有、0700 状态目录和 0600 普通配置文件。

The Core installer verifies the archive checksum and member list, then executes the staged binary's `--version` and requires the exact pinned version before atomically replacing the installed executable. A failed execution or version mismatch leaves the previous executable intact.

Core 安装器校验归档 checksum 和文件列表后，会执行暂存程序的 `--version`，要求与固定的发布版本完全一致，再原子替换已安装程序。执行失败或版本不符时保留旧程序。

## Native parallel builds / 原生并行构建

Both workflows use a build matrix: `ubuntu-24.04` for amd64 and `ubuntu-24.04-arm` for arm64. Each runner verifies its native Go architecture, compiles its own binary without a `GOARCH` override, executes that release binary to check its version, and runs Go tests natively. Tests run with root privileges so root-only Agent and integration cases are exercised. Build caches are separate by runner architecture; `-count=1` makes every release execute tests rather than reuse cached test results.

两个发布工作流都使用 matrix：amd64 对应 `ubuntu-24.04`，arm64 对应 `ubuntu-24.04-arm`。两台机器并行确认原生 Go 架构、编译自身架构的二进制（不覆盖 `GOARCH`）、执行发布二进制检查版本，并运行原生 Go 测试。测试以 root 运行，覆盖 root 专用 Agent 与集成测试。Go 缓存按 runner 架构分别保存；`-count=1` 保证每次发布实际执行测试，不复用测试结果缓存。

Core prepares and checks Web assets once, then shares the same assets with both native builds. Release publication waits for **both** matrix jobs to pass, merges their artifacts, restores executable permissions lost during artifact transfer, creates one `.tar.gz` archive per architecture, and generates one checksum manifest covering both raw binaries and archives. The Core installer is published alongside these assets and resolves the latest stable Core release by default; it can also install an explicitly pinned Core tag. A failed build or test blocks publication; the other architecture still completes to provide diagnostics.

Core 的 Web 资源只构建和检查一次，两台原生 runner 使用相同资源。发布必须等待**两个架构**全部通过，随后汇总产物、恢复 artifact 传输丢失的执行权限、为每个架构创建一个 `.tar.gz`，并为裸二进制和压缩包生成统一校验清单。Core 安装脚本也会随 release 发布，默认解析最新正式 Core 版本，也可指定 tag 安装。任一架构构建或测试失败都会阻止发布，另一架构继续运行以保留诊断结果。

## Docker images / Docker 镜像

Each native matrix job packages its tested artifacts using the Dockerfile's `release` target. The `runtime` stage is shared with the default `source` target, so user IDs, packages, entrypoints and health checks have one definition. Release packaging has no Go/Web compilation dependency. `scripts/release_images.py verify` checks the image's architecture, runtime UID, executable versions and byte-for-byte SHA256 equality with the tested native artifacts. Agent updater binaries are checked as well.

每个原生 runner 在测试通过后，直接将同一份产物装入 `release` 目标镜像；arm64 的系统包安装也在 arm64 上执行，不启用 QEMU。镜像内二进制必须与经过测试的 artifact 字节一致，架构、版本、运行 UID 和 Agent 更新器都会验证。Core 的 Web 只在 prepare 构建一次，并已嵌入 Core 二进制。

Matrix jobs push immutable image digests and upload the verified digests. Final publication combines both into the component tag using `docker buildx imagetools create`; any native test, packaging or verification failure blocks the version tag and GitHub Release. Images remain `ghcr.io/cst-cat/nodedance-core:core-vX.Y.Z` and `ghcr.io/cst-cat/nodedance-agent:agent-vX.Y.Z`.

运行镜像的缓存存入各自 GHCR 仓库的 `buildcache-amd64` / `buildcache-arm64`，使用 `mode=min`。这些缓存不受 GitHub Actions tag ref 的读取隔离，也不导出 Go/Node 构建中间层。首次发布仍会冷构建；缓存是优化，不能替代 artifact 一致性验证。不同架构使用不同缓存引用，避免彼此覆盖。

The release jobs use architecture-specific registry caches with `mode=min`; they no longer export large GHA `mode=max` caches. See [GitHub's cache access restrictions](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching#restrictions-for-accessing-a-cache) and [Docker registry cache](https://docs.docker.com/build/cache/backends/registry/). This removes the tag-to-tag GHA cache dependency; actual cold/warm release duration still needs measurement in Actions.

Source builds remain available through the default target. The native Docker CI builds those source images, then `python3 scripts/release_images.py test` extracts their binaries and packages them in a temporary context with **no Go or Web sources**, exercising both release targets and artifact equality. [Docker deployment](docker.md) documents runtime integration checks. Do not infer an end-to-end speedup from local packaging alone: measure runner queue time, native tests, registry cache import/export and final publication on the next authorized release.

The paired `agent-release.version` file is embedded into Core and supplies the tag in generated native/Docker installation commands. If `scripts/install-agent.sh` changes, publish the Agent containing that script and update the pairing before publishing Core. The one-command installer accepts `--mode native|docker` and `--token`; Core must pair with an Agent release containing this installer.

`agent-release.version` 随 Core 编译嵌入，生成的直接／Docker 安装命令使用其中的固定 Agent 标签。修改 `scripts/install-agent.sh` 后，必须发布包含该脚本的 Agent，并在发布 Core 前更新配对版本。一条命令安装入口使用 `--mode native|docker` 和 `--token`，Core 必须配对包含此安装脚本的 Agent 发布。
