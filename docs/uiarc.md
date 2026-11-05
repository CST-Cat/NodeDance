# UIArc local components / UIArc 本地组件

UIArc is installed as local source in `web/src/components/arc/`; shadcn components live in `web/src/components/ui/`. The React/TypeScript application uses Vite. `web/uiarc-manifest.json` records installed registry entries and file targets; consult it before adding a component. `web/components.json` configures `@uiarc` as `https://uiarc.dev/r/{name}.json`, and `@/*` resolves to `web/src/*`.

组件清单以仓库中的 manifest 和本地源文件为准，不以旧安装数量或临时编译报告为准。新增、替换和重构组件遵循 [AGENTS.md](../AGENTS.md) 与 [DESIGN.md](../DESIGN.md)，通过 shadcn CLI 安装并复用本地源文件。

## Installation and scope / 安装与作用域

Run from the application directory when a required component is missing:

```sh
cd web
pnpm dlx shadcn add @uiarc/button
```

Review conflicts before replacing installed files. Preserve the local theme and portal integration. `web/src/main.tsx` imports foundation once; its tokens and focus rules are scoped to `.arc-ui`. Set the current light/dark theme on that boundary and keep portals inside the same theme scope. `ArcTheme.css` maps project colors. Arc components use CSS Modules and are excluded from Tailwind source detection.

按已安装路径导入组件，不一次性引入全部示例。Blocks 的演示数据、模拟事件和 `/media/...` 图片必须在接入时替换为真实产品数据和资源。保留上游许可及现有 `ArcImage` 对 React/Vite 的适配。

## Product integration / 产品接入

`ManagementUI.tsx` supplies shared form bindings and scoped theme/portal boundaries. Selection fields use the installed Dropdown Menu; its default modal blocking, focus, keyboard behavior and transitions remain intact. Labels and accessible names stay associated with their controls. Reuse the installed Card and Separator through shared settings composition.

保持组件原生圆角、尺寸和交互；布局 CSS 仅安排外部宽度、行列和对齐。黑白交互高亮映射现有主题，不引入蓝色强调。状态使用无边框、无背景的普通文本，不使用 Badge 或胶囊。设置分组的卡片内边距和首尾／中间行留白遵循仓库规则，不全局覆盖组件几何。

Every shared management action uses the connected Core operation contract. Page controllers handle request identity, observation, cancellation, retry and recovery through shared workflows. Registry demos and specialized screens do not create separate validation, permissions or execution semantics. The global overview uses the installed `components/ui/globe`; its business data uses the same Core contract.

## Verification / 验证

For component integration changes, run `make build` and the smallest applicable browser audit from [verification](verification.md). Inspect actual visible controls, focus, keyboard and interaction states using isolated Core fixtures. Keep guest and file security checks when their behavior or contract changes. The installation manifest and TypeScript build do not by themselves prove visual quality or that every unused example block was bundled.

文档修改只做内容、路径和 diff 检查，不需要重建或部署。历史安装次数、截图数量和临时测试日志不作为当前验证结果。
