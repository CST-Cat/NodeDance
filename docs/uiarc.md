# UIArc local components / UIArc 本地组件

The existing React 19 + TypeScript application lives in `web/`; Vite is its build tool. No application was recreated. UIArc is installed as local source under `web/src/components/arc/`. Existing pages, Core workflows and product theme tokens are retained.

现有 React 应用位于 `web/`，Vite 仅负责开发和构建。本次安装没有重建项目或替换页面。

The installation selects every entry with `meta.tier == "free"` and type `registry:ui` or `registry:block` from the [official registry](https://github.com/kuratlielia/arc-library/blob/main/registry.json): 115 components and 22 blocks at installation time. `web/uiarc-manifest.json` records names, types and file targets. Shared foundation/motion/provider helpers and the upstream MIT license are included. Configuration is in `web/components.json`; `@/*` resolves to `web/src/*` in TypeScript and Vite.

根据官方清单安装了全部 137 个免费项目；名称与文件清单保存在 `web/uiarc-manifest.json`。不包含 Pro 项目。

## Use / 使用

```tsx
import { Button } from "@/components/arc/button/button";

export function Example() {
  return <div className="arc-ui" data-theme="light">
    <Button onClick={() => console.log("clicked")}>Continue</Button>
  </div>;
}
```

`src/main.tsx` imports foundation once. Its tokens and focus rules are scoped to `.arc-ui` using CSS nesting, with the upstream `:root` selectors mapped to `&`. Put `data-theme="light"` or `"dark"` on that boundary using the application's current theme; `data-accent` remains optional. Portal containers must be inside that boundary, or have their own `.arc-ui` boundary with the same theme. Keep this local scoping when updating foundation. Existing application elements outside the boundary receive no Arc theme or focus overrides. Tailwind source detection excludes Arc source because Arc uses CSS Modules.

共享样式已在根入口导入一次，并限定在 `.arc-ui` 内，不覆盖现有页面。使用组件时设置当前主题；浮层容器同样放在此作用域内。更新 foundation 时保留本地作用域处理。

Import components by their installed paths. Component CSS Modules load with each imported component; installing the library does not import every component into the application bundle. Free blocks include example data and simulated interactions. Wire future product actions to the existing Core operation contract. Sample `/media/...` images are not included by the upstream registry; replace those sample URLs with product assets before using image-based blocks.

按需从本地路径导入。Blocks 的演示数据、模拟事件与图片路径需要在实际接入时替换；本次未将任何演示登录或操作接入业务页面。

## Dependencies and local adjustments / 依赖与本地调整

- Registry-required Radix primitives and Lucide were installed. `motion@14.2.0` reuses the existing `framer-motion@14.2.0`; React and the existing motion implementation remain unchanged.
- Three blocks (`site-header`, `newsletter-signup`, `changelog-feed`) use the installed `ArcImage` instead of `next/image`. Image URLs use the current React/Vite string-source contract; Next.js was not installed.
- `blog-grid` has one conditional transition definition, eliminating an upstream duplicate JSX prop.
- Tabs align icons and labels in one flex row. The original installed shared-layout selection and panel transitions are retained; page styling prevents competing hover/pressed fills and keeps tab labels free of outlines and underlines.
- Input, Textarea and Select allow external labels without inserting an empty nested label. Checkbox respects external labels instead of forcing a generic accessible name. Card accepts a React heading. Select preserves empty option values in the controlled model and keeps its required validation; portals stay inside the product theme.
- Select and Dialog accept an optional portal container, allowing product dialogs and dropdowns to inherit the page’s scoped theme tokens.
- Signature replay uses a reverse point search instead of `Array.findLast`, preserving the existing ES2022 target.

## Verification / 验证

`make build` checks every installed TypeScript module and builds fresh Web/Core/Agent assets. A separate temporary library build in `.build/uiarc-install-check/` imports all 154 installed TypeScript modules and compiles their CSS Modules, including components not imported by current pages. All declared free-item file targets were checked against the registry manifest. Browser regressions use isolated Core/Agent fixtures; no demo page or product route is added.

`make build` 与全部模块单独编译已通过；安装清单中的文件已逐项确认存在。开发浏览器检查使用隔离实例，不增加产品页面或路由。现有工具栏／弹窗／圆环浏览器回归、访客安全套件及 foundation 深浅色作用域检查均通过。

Further additions should run from the React application directory:

```sh
cd web
pnpm dlx shadcn@4.22.1 add @uiarc/button
```

Use normal conflict review when modifying an already installed file; foundation and the locally adjusted source files carry the integration choices above. See the [official installation guide](https://uiarc.dev/docs/installation) for registry usage.


## Management pages / 管理页面

Agent onboarding, personal settings, host management, updates, subscriptions, operations and guest display settings use the installed UIArc primitives through `ManagementUI.tsx`. This module only supplies the shared product theme, scoped portal container and current form value bindings. Core discovery, request construction, permission checks, task observers, retry intent and confirmation remain in the existing page workflows. No new runtime UI dependency is added. `ArcTheme.css` maps the components to existing Figma theme tokens; tabs and menus use 12px outer and 8px inner corners with the installed transitions.

五个管理页面复用同一套已安装组件和主题边界。主机管理使用 Tabs；更新使用 Card、Accordion、Skeleton 和 Badge；订阅使用表单组件和列表卡片；操作中心继续从 Core 动态生成表单；游客设置保留显式资源选择和公开名称。原生语义内容标签与现有业务控制器保留。

Run `make build`, then `node web/scripts/audit-feeds.cjs`, `node web/scripts/audit-interfaces.cjs` and `NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs`. These use temporary Core data and protocol-only fixtures, never production resources. Browser helpers operate the actual Radix controls and inspect rendered option values; they do not bypass selection by manipulating hidden form inputs.

The existing Select accepts optional decorative icon previews while keeping plain
text accessible labels and Radix keyboard behavior. PreferenceEditor uses an
embedded ManagementPage theme/portal boundary, the same installed form controls,
and Core-discovered icon enum. SVG logo/flag assets are local, not another UI library.

User Menu and Dropdown Menu were already installed by the shadcn Registry; this integration adds no packages. The account footer uses `arc/user-menu`; all product selection fields delegate to `arc/dropdown-menu` with `RadioGroup`/`RadioItem`. The `arc/select` field only supplies labels and native form validation, and `UiSelect` supplies existing typed option values. Their retired custom floating-list implementation and animations have been removed. The installed User Menu supports localized account/sign-out labels, disabled/current account rows, scoped portals and size-aware upward placement; target-password verification reuses the installed Dialog and the existing Core operation.

Dropdown Menu 保留原组件默认的模态遮挡、焦点约束和键盘交互；不启用非模态模式，不自定义 Tab 关闭规则。

总览全球使用 shadcn CLI 安装的 `components/ui/globe`（Cobe renderer），其余界面复用现有 UIArc 组件。底层升级为 Cobe 2.0.1，保留默认旋转／拖拽，以原生 update、弧线和 CSS 锚点绑定业务数据，并映射现有项目配色；无新的 motion 或 UI 库依赖。坐标数据提取为本地文件，不保留额外的 world-countries 运行时依赖。

Agent onboarding now uses one connection draft and one actual form. The discovered target list changes the selected host and adds SSH verification fields in place; it does not mount the old enrollment and discovery pages in tabs. Operation execution lives in the shared enrollment/discovery controllers, including request identity, stale-response protection and task recovery. Personal settings renders account, security, sessions and publication together. The publication resource list combines nodes and service checks while keeping explicit resource types, separate public names and the existing Core revision boundary.

Add Agent and personal settings use the registry styling directly through `ManagementPage registryStyles`, without the legacy management control overrides. Their shared 880px canvas uses structural label/control rows, with stock Avatar, Input, Textarea, SegmentedControl, ColorPicker, PasswordField, Checkbox, Select, Button and Accordion. Layout CSS does not change registry internals. Password visibility retains the current five-second, blur and submission protections through a surrounding lifecycle binding. The visual reference is the official Codex desktop Appearance screenshot; project theme colors remain authoritative.

The current grouped settings composition uses stock shadcn Card and Separator installed by the CLI. SettingsGroup supplies only structural row composition and project color bindings; it does not replace registry component geometry or interaction. Mobile action containers use full-width, balanced grid tracks.

The personal appearance theme selector uses the installed UIArc SegmentedControl unchanged. Its controlled value updates the existing appearance draft; arrow keys, Home and End select and focus an option using the default component behavior. The existing appearance operation saves the preference. Native wrapper rules prevent legacy button fills and press transforms from masking registry feedback. Only background colors are adapted: the track uses the project surface, and the selected background derives from surface-muted with OKLCH lightness reduced by 0.05. Language and color controls follow the selector’s measured width.

Updates and subscriptions remain independent administrator pages and reuse the personal settings layout and SettingsGroup composition. Personal settings and these two pages retain registry radius tokens and element radii; global product radius mappings exclude their theme boundaries and controls. Their workflows still use the discovered Core operations.
