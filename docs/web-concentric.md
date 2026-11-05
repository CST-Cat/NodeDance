# Corner rules / 圆角边界

Web uses Tailwind CSS v4 through `@tailwindcss/vite`. `web/src/style.css` imports theme and utilities without Preflight, then `tailwindcss-concentric`; source detection is limited to product source and excludes installed Arc CSS Modules.

已安装组件的原版圆角与交互由 [DESIGN.md](../DESIGN.md) 和 [AGENTS.md](../AGENTS.md) 约束。不得全局覆盖 radius Token、改写 SegmentedControl、添加胶囊状态，或为追求同心圆角修改组件内部。状态以无边框、无底色文字显示。外部布局仅安排间距、宽度与对齐。

Existing explicit `rounded-concentric-[gap]` classes describe local nested geometry. The plugin cannot infer padding or borders from arbitrary CSS. Inspect the actual component before changing such a class; its presence does not authorize replacing installed geometry. Plain form controls in spaced rows do not inherit their enclosing Card's corner radius.

`pnpm --dir web audit:radii` is a static class audit and does not verify rendered geometry or interaction. Relevant browser checks live in `web/scripts/ui-controls-regressions.cjs`, used by `audit:interfaces`, and require a fresh `make build`. Run checks appropriate to an actual component change; do not add theme/width matrices or screenshots by default. See [verification](verification.md).
