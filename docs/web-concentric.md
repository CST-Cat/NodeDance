# Concentric corners / 嵌套圆角

The Web now uses Tailwind CSS v4 through `@tailwindcss/vite`, with the CSS-first [tailwindcss-concentric](https://tailwindcss-concentric.diip3sh.xyz/) plugin. Previously the Web used custom CSS without Tailwind. Only Tailwind's theme and utilities are imported before the plugin in `web/src/style.css`; Preflight is intentionally omitted to retain the existing reset, typography and control layouts. Source detection is restricted to `web/src`. No UI library or optional `tailwind-merge` dependency was added.

Web 原先未使用 Tailwind，现通过 Vite 接入 v4。全局样式仅导入 theme、utilities 和插件，关闭 Preflight，保留原有布局与设计 Token；不增加 UI 组件库或可选的 tailwind-merge。

## Radius review / 圆角检查

| Component | Decision / 处理 |
| --- | --- |
| `UiSelect` list and options | Publish the existing `--radius-control` outer radius using `rounded-(--radius-control)`. Options use `rounded-concentric-[7px]`: 6px CSS padding + 1px border. Current outer radius stays 8px; option radius becomes 1px. / 外层保持 8px，内层按真实间距计算为 1px。 |
| UIArc Tabs selection | Publish the local tab radius mapped to an existing design token and use `rounded-concentric-[var(--tabs-selection-inset)]` on the moving selection. The domain tabs use the existing 12px panel radius with 3px list padding and a 1px outer border: a 4px inset yields an 8px selected corner. These are local tab roles; global control and card tokens are unchanged. The installed spring transition is retained. |
| `UiSegments` indicator | Publish `--radius-control` on the track; use `rounded-concentric-[0px]` for the absolutely positioned indicator with zero inset. Both retain 8px corners. / 指示层零 inset，继续跟随轨道的 8px 圆角。 |
| Dialog/card form fields and buttons | Retain independent control tokens. These sit in spaced content and action rows, rather than following an enclosing corner. / 保留独立控件规范，不将普通后代控件全部改为同心圆角。 |
| Account grid, password visibility control | Retain existing tokens: grid cells do not trace the panel edge; the password button and input are siblings. / 不存在可直接推导的同心祖先关系，保留原样。 |
| Avatars, badges, radio controls, gauges | Retain pill/circle semantics. / 保留胶囊和圆形语义。 |
| Vendor file manager and editors | Retain vendor CSS; no file-manager layout changes. / 不改第三方文件管理器和编辑器样式。 |

Keep `figma-tokens.css`, `--radius-card`, `--radius-control` and `--radius-pill` as the design authority. The plugin reads Tailwind classes, not arbitrary CSS padding/borders/insets. Use explicit `rounded-concentric-[gap]` for CSS-defined gaps, and update the class if that spacing changes. Never apply concentric radii indiscriminately to every descendant of a rounded card.

设计 Token 和外层圆角仍由现有规范决定。插件不能读取普通 CSS 中的间距；这类场景必须显式指定 gap，并在间距变化时同步更新。

## Verification / 验证

- `pnpm --dir web audit:radii`: passed, no nested-radius findings. This scans class attributes; it does not prove custom CSS geometry. / 类属性静态审计通过，但不覆盖普通 CSS 的几何关系。
- `make build`: frontend checks and fresh embedded Web/Core/Agent builds passed. / 前端检查与完整构建通过。
- Isolated real Core + Chromium: shared-control regressions passed, including keyboard, disabled state, selection, focus restoration and reduced motion. Computed dropdown radii match actual padding/border gaps at 375px and 1440px in both themes; changing the radius token changes corners without changing width/height. Segment indicators retain their outer radius across the same matrix. / 隔离真实 Core 浏览器验证通过，覆盖浅色、深色、桌面、移动端和 Token 动态变化。
- The broader `audit:interfaces` run passed its overview, review, theme and localization stages but did not complete: the browser context closed during the form-layout stage. The focused shared-control run above completed separately. / 更大范围的接口审计在表单布局阶段浏览器上下文关闭，未完整通过；上述控件专项验证已单独完成。
- `audit:design` remains blocked by existing Compose path-control minimum heights (`ui-path-value: 0px`, `ui-path-copy: 28px`). The same failure was reproduced against the pre-integration binary/assets. Its stale user fixture was updated to the current contract, and all layout assertions remain intact. / 完整设计审计仍存在原有路径按钮最小高度问题，集成前构建同样失败；未放宽断言。

Browser assertions live in `web/scripts/ui-controls-regressions.cjs` (called by `audit:interfaces`) and `web/scripts/audit-design.cjs`. Screenshots from the focused verification are disposable artifacts in `.build/concentric-audit/`.
