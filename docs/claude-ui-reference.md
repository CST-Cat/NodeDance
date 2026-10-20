# Open Claude UI 参考研究

研究日期：2026-10-15。本页记录此前对用户指定的 [Caho1/open-claude-ui](https://github.com/Caho1/open-claude-ui) 的研究与实现。用户现已指定 [MCP Apps for Claude Figma 文件](https://www.figma.com/community/file/1597641111449594397/mcp-apps-for-claude) 为最高优先级；本页及 DESIGN.md 均为次要参考。该 Figma 文件现已通过 MCP 读取；实际导出数据与读取限制见 [Figma 参考记录](figma-design-reference.md)。本页只记录次要参考的研究范围；当前配色与控件由 Figma 规范决定。保留浅色、深色、跟随系统及终端独立深色配色。

本地参考位于 `.references/open-claude-ui`，由 `.gitignore` 忽略。检查版本为 `2834bc158a4849f21c97cbb2e0b10525f6e646f1`。它是第三方项目，其逆向来源与还原精度属于作者声明，不能视为 Anthropic 官方应用规范或本次独立验证结果。

## 已阅读与实际观察

- `code-examples/cds-theme.css`：完整浅深色变量、表面层级、边框透明度、浮层阴影、交互填充、字重补偿。
- `CdsButton.tsx`、`SegmentedControl.tsx`、`ConfirmationDialog.tsx`：按钮等级、分段选中、浮层与交互。
- `ContainerResponsiveBar.tsx`：按容器宽度调整标签与工具栏密度。
- `ClaudeMotionPresets.ts`：按钮、菜单、模态框、侧栏的不同动效参数。
- 实际用 Chromium 打开 `docs/claude-components-storybook.html`，切换其自身的主题按钮，查看浅深色截图。截图在忽略目录 `.build/claude-ui-reference/`；这个演示页依赖外部 CDN，不是完全自包含的离线应用。

## 当前使用范围

Figma 原始文件是当前视觉主参考，完整实现映射见 [Figma 参考记录](figma-design-reference.md)。此前第三方配色已不再作为产品主题。

本项目只在 Figma 未指定的浮层和侧栏动画中补充使用上述源码的参数，保留 reduced-motion、键盘焦点、弹窗焦点管理及 Core 操作语义。不得把这些补充参数说成来自 Figma，也不把社区项目称为官方规范。

如复制代码，继续遵循仓库 MIT 许可并保留版权声明。

## 2026-10-18 补充采用

本地快照 `638800b8a4cee4513c8763c3df0ecf8a77b32379` 已检查源码与浅／深色 Storybook。新增采用矩形分段指示器、原位复制反馈和配置差异展示思路；统一进入 NodeDance 的 `UiSegments`、`clipboardFeedback`／`UiCopyFeedback`、`UiConfigDiff` 与原有 `UiDialog`。没有引入整包主题、Tailwind、Lucide 或示例中的业务数据。

差异计算复用锁定版本 jsdiff 9.0.0，保留原文、换行与行号；预览计算和行数有边界，超出时明确不可预览，不截断后假装完整。它不改变配置读取、校验、保存任务或冲突处理。代码许可见 `/licenses/ui-notices.txt`。本次没有重新读取 Figma；颜色继续映射当前仓库已有的 Figma 语义变量。
