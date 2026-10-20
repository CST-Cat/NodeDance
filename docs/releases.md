# Independent releases / 独立发布

Core and Agent are versioned independently:

| Component / 组件 | Tag / 标签 | Changelog / 更新日志 | Assets / 产物 |
| --- | --- | --- | --- |
| Core | `core-vMAJOR.MINOR.PATCH` | `CHANGELOG_CORE.md` | Embedded Web + CLI/TUI Linux amd64/arm64 binaries and `.tar.gz` archives, native systemd installer, SHA256SUMS |
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
# Publish Agent first; after it is stable, rehearse and publish the paired Core.
```

Only `agent-v*` tags trigger `.github/workflows/agent-release.yml`; only `core-v*` tags trigger `.github/workflows/core-release.yml`. Both reject invalid semver or missing bilingual notes. `agent-release.version` pins the exact Agent paired with Core at the tagged commit. Core validates that pairing and its changelog before building, without a release API dependency. Core publication also rejects a pairing older than the newest stable published Agent. Only final publication waits for that exact stable, published Agent (up to ten minutes); a different/newer release cannot replace the pairing. Its **exact tag URL is the first line** of the Core description. Publishing Agent marks `--latest=false`; Core marks `--latest=true`.

## Release rehearsal / 发布彩排

A tag is never the first execution of release validation. Before pushing an `agent-v*` or `core-v*` tag, rehearse it:

tag 绝不是发布校验的第一次执行。推送 `agent-v*` 或 `core-v*` 标签之前必须先彩排：

```sh
python3 scripts/release-rehearsal.py run agent agent-v0.1.22
python3 scripts/release-rehearsal.py run core core-v0.1.37 --agent-tag "$(cat agent-release.version)"
```

The rehearsal runs every check the release workflows perform without creating a tag, release or artifact: tag format, reviewed bilingual changelog entry, committed (not staged) changelog and pairing file, exact Agent pairing in `agent-release.version`, Core pairing against the newest stable published Agent, and that the tag/release does not exist yet locally or on origin. A passing rehearsal is recorded in `.release-rehearsals`; both release workflows verify that record in their publish job and refuse to publish an unrehearsed tag. If a rehearsal fails, stop, fix the cause and rehearse again — pushing the tag anyway wastes the tag, because a failed publication leaves the tag behind.

彩排以只读方式执行发布工作流的全部校验（tag 格式、双语 changelog 条目、changelog 与配对文件已提交、`agent-release.version` 精确配对、Core 配对为最新稳定 Agent、tag/release 尚不存在），不创建任何 tag、release 或产物。通过的彩排记录在 `.release-rehearsals`；两个发布工作流在 publish 阶段核对该记录，拒绝发布未彩排的 tag。彩排失败时停止并修复后重新彩排——直接推 tag 一旦失败，tag 会残留，造成浪费。

## Enrollment tokens / 注册令牌

Enrollment tokens are single-use with no time limit. A token stays valid until a device enrolls with it, consumption invalidates it immediately, and renewing an enrollment replaces the outstanding token. There is no ten-minute expiry to race against.

注册令牌仅可单次使用，不设时间限制。令牌在设备完成注册前一直有效，注册成功立即失效；重新生成安装命令会替换未使用的旧令牌。不存在需要抢时间的十分钟过期窗口。

两个工作流分别由自己的标签触发，并拒绝无效版本或缺少双语说明的发布。创建 Core tag 前，`agent-release.version` 必须写明本次配对的 Agent tag。构建与测试不等待 Agent 发布；最终发布最多等待十分钟，确认这个确切版本已正式发布、不是草稿或预发布，再写入说明第一行。不会自动换成恰好最新的其他版本。Agent 必须先发布为稳定版，随后才可完成 Core 配对彩排并推送 Core tag；超时只阻止 Core 最终发布，不修改配对版本。Agent 不占用仓库全局 latest，Core 为全局 latest。

Installers resolve the latest stable release from the static version files (`agent-latest.txt`, `core-latest.txt`) that the release workflows publish to `main` after a release is confirmed; they download everything else from `releases/download`. The install path uses only plain HTTPS file downloads — no GitHub REST API and no per-IP rate quota. `--release-tag` pins an explicit release and bypasses resolution entirely. Installers never use the repository-wide `/releases/latest` URL. Agent credentials remain root-owned, with a 0700 state directory and 0600 regular config file.

Agent 安装器解析并固定已发布的 `agent-v*` 标签，不使用指向 Core 的仓库 `/releases/latest`。Agent 凭据继续要求 root 所有、0700 状态目录和 0600 普通配置文件。

The Core installer verifies the archive checksum and member list, then executes the staged binary's `--version` and requires the exact pinned version before atomically replacing the installed executable. It also creates, enables, and starts the root-owned native `nodedance.service`, bound to `0.0.0.0:8180` with state under `/var/lib/nodedance`; existing data is preserved. It refuses to replace an unmarked systemd unit. A failed execution or version mismatch leaves the previous executable intact.

Core 安装器校验归档 checksum 和文件列表后，会执行暂存程序的 `--version`，要求与固定的发布版本完全一致，再原子替换已安装程序。执行失败或版本不符时保留旧程序。

## Native parallel builds / 原生并行构建

Both workflows use a build matrix: `ubuntu-24.04` for amd64 and `ubuntu-24.04-arm` for arm64. Each runner verifies its native Go architecture, compiles its own binary without a `GOARCH` override, executes that release binary to check its version, and runs Go tests natively. Tests run with root privileges so root-only Agent and integration cases are exercised. Build caches are separate by runner architecture; `-count=1` makes every release execute tests rather than reuse cached test results.

两个发布工作流都使用 matrix：amd64 对应 `ubuntu-24.04`，arm64 对应 `ubuntu-24.04-arm`。两台机器并行确认原生 Go 架构、编译自身架构的二进制（不覆盖 `GOARCH`）、执行发布二进制检查版本，并运行原生 Go 测试。测试以 root 运行，覆盖 root 专用 Agent 与集成测试。Go 缓存按 runner 架构分别保存；`-count=1` 保证每次发布实际执行测试，不复用测试结果缓存。

Core prepares and checks Web assets once, then shares the same assets with both native builds. Release publication waits for **both** matrix jobs to pass, merges their artifacts, restores executable permissions lost during artifact transfer, creates one `.tar.gz` archive per architecture, and generates one checksum manifest covering both raw binaries and archives. The Core installer is published alongside these assets and resolves the latest stable Core release by default; it can also install an explicitly pinned Core tag. A failed build or test blocks publication; the other architecture still completes to provide diagnostics.

Core 的 Web 资源只构建和检查一次，两台原生 runner 使用相同资源。发布必须等待**两个架构**全部通过，随后汇总产物、恢复 artifact 传输丢失的执行权限、为每个架构创建一个 `.tar.gz`，并为裸二进制和压缩包生成统一校验清单。Core 安装脚本也会随 release 发布，默认解析最新正式 Core 版本，也可指定 tag 安装。任一架构构建或测试失败都会阻止发布，另一架构继续运行以保留诊断结果。

## Agent Docker image / Agent Docker 镜像

Core releases publish native Linux binaries and the installer only. The Agent release workflow packages the exact tested root Agent binary into `ghcr.io/cst-cat/nodedance-agent:agent-vX.Y.Z`; `scripts/release_images.py verify` checks its architecture, runtime identity, version, entrypoint, and SHA256 equality with the native artifact. The Agent Docker image remains available for managed hosts that need container deployment. Core itself has no Docker image or Compose deployment.

Core release 只发布原生 Linux 二进制及安装器。Agent 发布工作流将经测试的 root Agent 二进制打包到 `ghcr.io/cst-cat/nodedance-agent:agent-vX.Y.Z`；`scripts/release_images.py verify` 校验架构、运行身份、版本、入口及与原生产物一致的 SHA256。Agent Docker 镜像继续供需要容器部署的受管主机使用；Core 不提供 Docker 镜像或 Compose 部署。

The paired `agent-release.version` file is embedded into Core for release publication checks. Generated native/Docker installation commands do not hardcode it: the installer resolves the latest stable published Agent at execution time from `agent-latest.txt`. If `scripts/install-agent.sh` changes, publish the Agent containing that script and update the pairing before publishing Core. The one-command installer accepts `--mode native|docker` and `--token`; Core must pair with an Agent release containing this installer.

`agent-release.version` 随 Core 编译嵌入，用于发布配对检查。生成的原生／Docker 安装命令不固定该标签，安装时自动解析最新正式 Agent release。修改 `scripts/install-agent.sh` 后，必须发布包含该脚本的 Agent，并在发布 Core 前更新配对版本。一条命令安装入口使用 `--mode native|docker` 和 `--token`，Core 必须配对包含此安装脚本的 Agent 发布。

Core and Agent releases include their component binaries, installer, update manifest and checksums. Updates run through the component binary; no separate updater assets or installation are required. / Core 与 Agent 的更新由组件二进制执行，发布包不包含独立 updater 程序或安装脚本。
