# Documentation / 文档索引

这里说明当前实现与维护入口。Core 的运行中 `/api/v1/operations` 是 Web、CLI、TUI 请求结构与权限的权威来源；开发约束见 [AGENTS.md](../AGENTS.md)，界面规则见 [DESIGN.md](../DESIGN.md)。历史发布记录保留在 [Core changelog](../CHANGELOG_CORE.md) 和 [Agent changelog](../CHANGELOG_AGENT.md)，不作为当前命令说明。

These guides describe the current implementation. Discover request schemas and permissions from the connected Core; historical changelogs describe their tagged releases.

| 主题 / Topic | 文档 / Guide |
| --- | --- |
| 安装与运行 | [Installation](installation.md)、[Docker Agent](docker.md) |
| 命令、通道与恢复 | [CLI/TUI](cli.md)、[Stable/Dev updates and rollback](updates.md)、[Releases and rehearsal](releases.md) |
| 架构与能力 | [Architecture](architecture.md)、[Generated interface coverage](interface-coverage.md)、[Shared capability coverage](core-convergence-audit.md)、[Agent capabilities](agent-capabilities.md) |
| 文件 | [File manager](file-manager.md)、[Strict direct / Core relay transfers](file-transfers.md)、[Sharing](file-sharing.md)、[Bundled rsync](bundled-rsync.md) |
| 终端 | [Terminal recovery](cli.md#terminal-recovery--终端恢复)、[Bundled tmux](bundled-tmux.md) |
| 监控与位置 | [Overview](overview.md)、[Diagnostic monitoring](observability.md)、[Names and country](node-display.md)、[City locations](node-locations.md)、[Global overview](global-overview.md) |
| 管理 | [Accounts](accounts.md)、[Domains and certificates](domain-certificates.md)、[RSS/Atom](rss.md) |
| 游客与安全 | [Guest display](guest-display.md)、[Guest security verification](guest-security.md) |
| Web 与验证 | [Navigation](web-navigation.md)、[Languages](i18n.md)、[UIArc](uiarc.md)、[Corner rules](web-concentric.md)、[Verification](verification.md) |
| 参考范围 | [Figma](figma-design-reference.md)、[Supplementary UI reference](claude-ui-reference.md)、[Protocol study](komari-communication.md)、[Optimization study](komari-optimizations.md) |

Core 与 Agent 的开发通道均为 `dev`；默认更新为 Stable。本机 Core：`sudo nodedance update --channel dev`；本机 Agent：`sudo nodedance agent update --channel dev`。Core Dev 强制同步源码至 `origin/main` 后构建；需要保留的工作先提交并推送。Agent Dev 拉取已发布开发构建。

验证说明列出可复现的检查与边界，不代表本次文档清理重新执行了浏览器、生产部署或真实远端更新。文档变更只需文档／差异检查；功能变更按 [AGENTS.md](../AGENTS.md) 执行适用验证。
