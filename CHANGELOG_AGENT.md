# Agent changelog / Agent 更新日志

## [Unreleased]

### English

### 简体中文

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
