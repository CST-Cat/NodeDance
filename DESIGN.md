# NodeDance design system

NodeDance is a server management console. Preserve the node overview, direct card-to-VPS navigation, contextual sidebar, shared width/collapse controls and operation contract. UI changes must improve scanning, comparison and action discovery without hiding management capabilities.

## Reference and scope

Use the Claude visual language requested by the user: warm neutral surfaces, restrained terracotta accents, readable text and low decorative density. The [Anthropic brand guidelines](https://github.com/anthropics/skills/blob/main/skills/brand-guidelines/SKILL.md) document brand colors, not a complete application theme. The dark surface sequence below comes from the user's supplied [ClaudeOS theme record](https://github.com/heytcass/claudeos/blob/main/docs/THEME.md), a third-party record, not an official specification. Operational states and accessible control boundaries are NodeDance adaptations. Do not import another product's visual theme or claim these values are verified live Claude CSS.

## Theme roles

Use shared semantic variables in `web/src/style.css`. Both light and dark define complete role sets; neither is a color inversion of the other.

| Role | Light | Dark |
| --- | --- | --- |
| Canvas | `#faf9f5` | `#262624` |
| Recessed / sidebar | `#f0eee6` | `#1f1e1d` |
| Resource card / elevated surface | `#f5f0e8` | `#30302e` |
| Hover | `#eee9df` | `#393936` |
| Primary text | `#3d3d3a` | `#faf9f5` |
| Secondary text | `#4d4b45` | `#c2c0b6` |
| Muted text | `#6c6a64` | `#9c9a92` |
| Accent fill | `#d97757` | `#d97757` |
| Accent foreground | `#141413` | `#141413` |

Separate `--control-bg`, `--control-border`, `--control-disabled`, `--focus`, `--accent-soft`, `--primary-hover`, `--surface-hover` and status roles. Decorative separators may be subtle; controls and keyboard focus must remain identifiable. Keep ordinary text at least 4.5:1 against its actual surface and essential non-text indicators at least 3:1. Do not lower readable content contrast just to make the page quiet.

Success, danger, warning and information have independent semantics. Pair colors with labels/icons. Never use terracotta as both the brand accent and an error signal. Orange buttons use dark text; do not place normal-sized warm-white text on the brand fill.

## Layout and visual structure

1. Structural nesting does not imply visual nesting.
2. Every visible boundary must represent an object, selection, distinct scroll/editor area, input or confirmation responsibility.
3. Prefer aligned columns, spacing and subtle row separators over nested cards.
4. Keep one object card per VPS on the overview. Inside it, metrics are aligned readings, not individual cards. Docker previews are continuous rows.
5. Overview summary counts form a single flat strip. Do not turn each count into a competing card.
6. Docker uses its continuous table with existing selection/batch toolbar. Compose uses selectable project rows; services remain inside their project and share the existing task semantics. Images, discovery peers and task histories use continuous rows.
7. Node metrics and settings are flat sections. CPU/memory readings, disk/network rows, form groups and toolbars share the page's alignment. Do not wrap the entire node workspace in another decorative panel.
8. Editors, file management, terminal, menus and dialogs keep purposeful boundaries. Preserve selection, drag targets, keyboard focus and independent scrolling.
9. Forms align labels and controls at the top; hints do not stretch neighboring inputs. Date and grid columns must shrink without overflowing. Keep dangerous actions at the end of settings.
10. Optimize useful density, not empty space. Do not remove required data, controls or affordances to produce a cleaner screenshot.

Management pages share a maximum 1320 px content width, 32 px top spacing and responsive gutters. Use 28 px between major sections, 16 px between fields and 8 px inside an associated control group. On phones, use one-column forms, readable resource rows and wrapping toolbars; preserve all actions.

## Typography

Use the existing humanist sans font for navigation, page titles, tables, inputs and metrics. Reserve the serif display face for the brand and limited welcome/login presentation. Do not turn machine names, IPs or operational data into oversized editorial headings.

Page titles: 26–34 px, weight 500. Section titles: 20 px, weight 500. Body and controls: 13–16 px. Metadata may use 12 px when readable. Numbers use tabular figures. Paths, identifiers, YAML, JSON, logs and terminal data use the existing monospace face and wrap/scroll appropriately.

## Interaction states

Define rest, hover, pressed, focus-visible, disabled and selected states. Hover is transient neutral emphasis; selection is persistent accent emphasis plus a checkbox/current marker. Keyboard focus uses an explicit outline. Never distinguish states with color alone. Do not use an orange left-edge bar for selection in navigation or resource rows; use a persistent surface, text/icon emphasis and explicit checked/current state.

Custom selects retain contract/form values and full keyboard operation. Floating menus use an elevated surface, stronger boundary and restrained shadow. Flat content sections do not use decorative shadows. Sidebar motion remains modest; dragging is immediate. Respect reduced motion.

## Embedded surfaces

VueFinder, its dialogs and CodeMirror use the same semantic theme, including hover versus selection and disabled controls. Charts use theme text, separators, tooltip and area colors. Terminal keeps its independent dark content palette and ANSI meanings in either application theme. Browser color-scheme and theme-color must match the selected light/dark/system mode.

## Compact Operational UI

NodeDance is a dense operational console, not a spacious marketing dashboard or a chat application. Maximize useful information density while minimizing visual decoration.

### Information hierarchy

- Preserve resource boundaries (one card per VPS), not arbitrary component boundaries.
- Do not give every metric, property, or row a separate card.
- Do not replace removed cards with repeated divider lines.
- Do not create spacious layouts merely to appear minimal.

### Compact geometry

- Use 12–16 px padding as a starting point for VPS cards. Do not apply page-level section spacing (28 px) inside cards.
- Use 4–8 px spacing between closely related readings (label ↔ value, adjacent metrics).
- Reserve larger spacing (12–20 px) for genuinely separate functional areas.
- Avoid oversized numeric typography and stretched progress bars. Prefer compact aligned values over full-width progress bars.
- Use the component's available width (CSS container queries) to determine layout, not the viewport width.
- Never force heterogeneous metrics (CPU percentage, network throughput, uptime) into five equal-width columns.

### Resource layouts

- VPS overview: one compact card per server. Metrics are inline label–value pairs in a 3-column grid, not stacked blocks.
- CPU/RAM/Disk: aligned readings without inner cards or progress bars by default.
- Network/Uptime: compact secondary readings on the second metric row.
- Network interfaces: single-line rows (name · note · rates · status pill), no per-row dividers.
- Docker preview: dense rows with gap-based spacing; no per-item border-bottom dividers. Preserve all existing actions.
- Docker/Compose management: scannable resource lists or tables with 36–40 px row height.
- Metrics detail: grouped data by information tier, not repeated bordered blocks.

### Boundaries and interaction

- Use background surfaces only for meaningful regions (resource objects, editors, dialogs).
- Keep essential input, focus, selection and dialog boundaries.
- Ordinary rows should not automatically receive dividers; use gap and proximity.
- Preserve keyboard navigation and safe operation controls.
- Do not remove information or functionality to simplify UI.

### Implementation requirements

- Refactor markup and CSS together; do not just delete borders without reorganising content.
- Avoid appending layers of global override rules that conflict with scoped component styles.
- Consolidate duplicate style definitions.
- Test populated and expanded states, not only empty or collapsed pages.
- Validate card widths independently from viewport widths.

## Verification

Build fresh assets. Review light/dark and mobile/desktop layouts with populated, empty, loading, error, disabled and selected states. Verify containment, alignment, actual text/control contrast, keyboard focus, reduced motion and system-theme transitions. Run the real Core interface audit, guest security matrix and isolated root Agent file-manager audit for applicable visual changes. Keep those scripts and security assertions intact. Test presentation without touching production remote resources.
