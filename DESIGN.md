# NodeDance design system

NodeDance is a server management console. Preserve the node overview, direct card-to-VPS navigation, contextual sidebar, shared width/collapse controls and operation contract. UI changes must improve scanning, comparison and action discovery without hiding management capabilities.

## Reference and scope

Use the user-selected [MCP Apps for Claude Figma file](https://www.figma.com/community/file/1597641111449594397/mcp-apps-for-claude) as the primary visual reference. Its verified components, variables, typography, spacing, radii and component states take precedence over all other visual guidance. The [open-claude-ui](https://github.com/Caho1/open-claude-ui) reference and this document are secondary, used only where the Figma resource does not specify a design decision. Preserve NodeDance's management behavior and security requirements.

The user-supplied `.fig` has been decoded locally and inspected as the active source. Tokens are preserved in `docs/figma-tokens.json`; component/state/layout specifications are recorded in `docs/figma-component-specs.json`. Source SHA256, offline validation and the applied mapping are documented in [the Figma reference notes](docs/figma-design-reference.md). Shared theme tokens and components replace the previous palette; product-specific management layout and behavior remain.

Preserve NodeDance's resource management tasks and the explicitly requested 1Panel overview arrangement. Theme changes do not change operation discovery, permissions or execution semantics. Terminal retains its independent dark content palette.

## Theme roles

Use shared semantic variables in `web/src/style.css`. Both light and dark define complete role sets; neither is a color inversion of the other.

| Role | Light | Dark |
| --- | --- | --- |
| Canvas / tertiary | `#faf9f5` | `#141413` |
| Sidebar (resolved BG/100) | `#f9f9f7` | `#20201f` |
| Secondary surface | `#f5f4ed` | `#262624` |
| Card / primary surface | `#ffffff` | `#30302e` |
| Primary text | `#141413` | `#faf9f5` |
| Secondary text | `#3d3d3a` | `#c2c0b6` |
| Tertiary text | `#73726c` | `#9c9a92` |
| Primary action fill | `#141413` | `#faf9f5` |
| Primary action text | `#ffffff` | `#141413` |
| Checked control | `#4682d5` | `#4682d5` |

Use the Figma component-specific hover, selected, disabled and focus states and radii. Keep muted operational text readable rather than copying low-contrast decorative labels. Preserve independent success, warning and danger semantics. Figma default button hover uses 150 ms linear interpolation; radio selection uses 100 ms `cubic-bezier(0,0,.58,1)`; switch hover uses 150 ms `cubic-bezier(.4,0,.2,1)`. Where the file has no motion specification, use the secondary open-claude-ui popover/sidebar presets, respecting reduced motion. Input feedback should be short, and panel motion deliberate; reduced motion and immediate drag response remain required.

Separate `--control-bg`, `--control-border`, `--control-disabled`, `--focus`, `--accent-soft`, `--primary-hover`, `--surface-hover` and status roles. Decorative separators may be subtle; controls and keyboard focus must remain identifiable. Keep ordinary text at least 4.5:1 against its actual surface and explicit focus indicators readable. Figma specifies subtle 30% resting input borders; do not claim those all meet 3:1. Focus also uses a solid information-colored control edge and separated ring. Do not lower readable content contrast just to make the page quiet.

Success, danger, warning and information have independent semantics. Pair colors with labels/icons. Never use terracotta as both the brand accent and an error signal. Primary actions use the Figma inverse fill/text pair. Terracotta is reserved for the brand, not ordinary primary actions.

## Layout and visual structure

1. Structural nesting does not imply visual nesting.
2. Every visible boundary must represent an object, selection, distinct scroll/editor area, input or confirmation responsibility.
3. Prefer aligned columns, spacing and subtle row separators over nested cards.
4. Keep one object card per VPS on the overview. Inside it, metrics are aligned readings, not individual cards. Docker previews are continuous rows.
5. Overview uses a single flat count strip above full-width node cards in its left column. Each node card places compact load/CPU/memory/disk rings beside a bounded live network/disk IO chart; small card containers stack those two regions. The right rail shows Core or the inspected Agent's host information above a shared memo. It remains sticky on desktop and becomes expandable sections on narrow screens. Selecting a card's non-interactive area inspects it; explicit name/detail controls navigate. Scrolling follows the visible card. Missing, stale, offline and warming-up measurements never become invented zero readings.
6. Docker uses its continuous table with existing selection/batch toolbar. Compose uses selectable project rows; services remain inside their project and share the existing task semantics. Images, discovery peers and task histories use continuous rows.
7. Node metrics and settings are flat sections. CPU/memory readings, disk/network rows, form groups and toolbars share the page's alignment. Do not wrap the entire node workspace in another decorative panel.
8. Editors, file management, terminal, menus and dialogs keep purposeful boundaries. Preserve selection, drag targets, keyboard focus and independent scrolling.
9. Forms align labels and controls at the top; hints do not stretch neighboring inputs. Date and grid columns must shrink without overflowing. Keep dangerous actions at the end of settings.
10. Optimize useful density, not empty space. Do not remove required data, controls or affordances to produce a cleaner screenshot.

Management pages share a maximum 1320 px content width, 32 px top spacing and responsive gutters. Use 28 px between major sections, 16 px between fields and 8 px inside an associated control group. On phones, use one-column forms, readable resource rows and wrapping toolbars; preserve all actions.

## Responsive forms and desktop workflows

Desktop layouts must compose fields by their task and expected content, rather than stretch a mobile column across the page. Keep search and filters in wrapping toolbars; short selectors, counts and numeric fields have bounded widths. Group identity, connection, scheduling and billing fields in aligned columns. URLs and paths get more space; scripts, JSON, YAML and result views keep a wide reading/editing area. Do not apply one global max-width to all inputs. Use available container width for wrapping field grids, and reset to single-column, full-width controls on narrow layouts. Actions stay beside the relevant controls or directly below the group, rather than stretching to page width.

## Typography

Use the existing humanist sans font for navigation, page titles, tables, inputs and metrics. Reserve the serif display face for the brand and limited welcome/login presentation. Do not turn machine names, IPs or operational data into oversized editorial headings.

Page titles: 28–36 px, weight 500. Section titles: 20 px, weight 500. Body and controls: 13–16 px. Metadata may use 12 px when readable. Numbers use tabular figures. Paths, identifiers, YAML, JSON, logs and terminal data use the existing monospace face and wrap/scroll appropriately.

## Interaction states

Define rest, hover, pressed, focus-visible, disabled and selected states. Hover is transient neutral emphasis; selection is persistent accent emphasis plus a checkbox/current marker. Keyboard focus uses an explicit outline. Never distinguish states with color alone. Do not use an orange left-edge bar for selection in navigation or resource rows; use a persistent surface, text/icon emphasis and explicit checked/current state.

Custom selects retain contract/form values and full keyboard operation. Floating menus use an elevated surface, stronger boundary and restrained shadow. Flat content sections do not use decorative shadows. Sidebar motion remains modest; dragging is immediate. Respect reduced motion.

## Embedded surfaces

The selected third-party file manager and its stock dialogs/editors use the same semantic theme, including hover versus selection and disabled controls. Charts use theme text, separators, tooltip and area colors. Terminal keeps its independent dark content palette and ANSI meanings in either application theme. Browser color-scheme and theme-color must match the selected light/dark/system mode.

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

- VPS overview: one compact full-width card per server in the main column, with compact resource rings and a live chart arranged horizontally when the card has enough width.
- Load/CPU/RAM/Disk: compact accessible SVG rings and aligned capacity readings, without nested metric cards.
- Network and disk IO: a switchable live chart beside the resource rings; uptime appears in the compact metadata line.
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

## Overview source reference

The requested layout reference was inspected in [1Panel home](https://github.com/1Panel-dev/1Panel/blob/dev-v2/frontend/src/views/home/index.vue) and [status gauges](https://github.com/1Panel-dev/1Panel/blob/dev-v2/frontend/src/views/home/status/index.vue). NodeDance implements its own SVG readings, bounded live series and contextual selection; it does not import the 1Panel application store or product theme.

At widths of 1100 px or below, host information and the memo follow the entire VPS card list in document order; they do not precede or squeeze the cards. The right rail is sticky only on wider desktop layouts.

Overview section actions belong in their top-right headers: host display settings, refresh, sensitive information visibility and copy; memo edit switches to save/cancel. Header actions must remain available on narrow layouts, with accessible labels, focus states and actual behavior.


## Component geometry and responsive adaptation

- Desktop default button: 36 px high, 8 px radius, 16 px horizontal padding, 14 px semibold label. Secondary is transparent with a subtle border; danger is the dedicated red fill.
- Default text input / dropdown: 32 px high, 8 px radius, 12 px horizontal padding, 14 px text. Textareas use the same radius and aligned inner spacing.
- Desktop checkbox and radio: 20 px large variant. Checkbox radius 6 px and checked fill `Border/Info`; radio has a 14 px dot inside its 20 px outline. Focus and disabled states follow the kit.
- Sidebar: 256 px expanded / 52 px collapsed defaults, 36 px desktop rows, 8 px horizontal insets, 68 px brand row. User resizing/collapse and node-context navigation remain available.
- Mobile actions: 44 px pill variant; fields remain 44 px and check/radio controls use the 24 px round mobile variant. Text-entry fields use 16 px to avoid iOS focus zoom; the kit’s 15 px mobile label role remains available elsewhere.
- Anthropic Sans Text is not bundled. The existing licensed self-hosted Inter fallback now retains 400–700 so semibold/bold use actual font weights. JetBrains Mono remains the code face. Brand lettering and terminal content retain their purposeful independent styles.
- Keep existing responsive field grouping, compact bounded selectors, sticky host/memo rail and narrow-screen stacking. Do not reproduce chat content or promotional assets in the server management product merely because they appear in the kit.
