# Figma reference / Figma 参考边界

用户指定参考为 [MCP Apps for Claude](https://www.figma.com/community/file/1597641111449594397/mcp-apps-for-claude)。用户指令、[AGENTS.md](../AGENTS.md) 和 [DESIGN.md](../DESIGN.md) 的现行组件与偏好规则优先；[open-claude-ui](claude-ui-reference.md) 仅为补充参考。

当前项目主题映射位于 `web/src/figma-tokens.css`；仓库的 [figma-tokens.json](figma-tokens.json) 保留本地参考数据。这些文件不是运行中 Figma 文档的实时镜像。只有实际取得相应文件或 MCP 数据后，才可声称读取过组件、变量或状态；本次文档清理没有重新读取 Figma。

New and refactored Web controls use locally installed shadcn/UIArc sources. Preserve their native radii, focus, keyboard and modal behavior; map colors to existing product tokens and arrange only external layout. Product interactions use black/white highlights without blue accents. Statuses are plain text without pills or Badge. Reference measurements do not override these rules.

参考仅指导展示。请求结构、权限、确认、任务执行和恢复仍由 Core 的共享操作契约负责。详细组件接入见 [UIArc](uiarc.md)，行为验证见 [verification](verification.md)。
