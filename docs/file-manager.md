# File manager / 文件管理

The React file manager uses installed shadcn/UIArc controls for toolbar buttons, sortable selection table, tree, context/dropdown menus, search fields, pagination, upload queues, image tools and dialogs. At the user’s explicit request, Grid preserves the elFinder 2.1.70 icon-view layout with the registered NodeDance icon theme in an isolated local frame. Its selection, open, clipboard and menu events bind to the same current React/Core workflows; it has no independent connector, remote mutation API, or alternate file permissions. jQuery and jQuery UI load only inside that frame. The grid frame mounts on the first switch to Grid and stays alive afterwards: list/grid switching only toggles its visibility, the theme loads once per instance, and same-directory paging and refresh update through incremental sync instead of rebuilding the instance. The same component serves overview dialogs, the inline node-detail screen and Compose directory selection. The image editor uses the locally bundled TOAST UI engine without its built-in UI; only canvas rendering comes from that engine. Every visible image control is an installed shadcn/UIArc component. Assets and notices ship with Core at `/licenses/file-manager-notices.txt`.

文件管理界面的工具栏、可排序多选列表、目录树、右键／下拉菜单、搜索、分页、上传队列、图片工具和弹窗，使用已安装的 shadcn／UIArc 组件；用户明确指定的网格例外，保留 elFinder 2.1.70 原版图标视图布局，使用注册的 NodeDance 重绘图标主题，在隔离的本地 frame 中加载，选择、双击和菜单操作仍交回现有 Core 流程。网格 frame 在首次切到网格时挂载并保持存活：列表／网格切换只切换可见性，主题仅在实例首次初始化时加载一次，同一目录的翻页和刷新通过增量同步更新而非重建实例。总览弹窗、节点详情和 Compose 目录选择复用同一实现。图片处理保留本地无内置 UI 的 TOAST UI 画布引擎，所有可见参数和操作仍为 shadcn 控件；深浅色使用项目现有配色。文件操作和安全边界保持在 Core／root Agent。

## Shared operations / 统一操作

Web, CLI and TUI discover the connected Core contract. `files.list` reads a directory page; `files.search` performs filename search, optional recursion and size filtering; `files.preview` provides authenticated passive media preview. Existing `files.stat`, `files.text`, `files.download`, `files.upload`, `files.action` and `tasks.*` remain the single execution path.

`files.action` supports `mkdir`, `rename`, `move`, `copy`, `archive`, `extract`, `delete` and `save_text`. `copy` and `move` use `path` and `newPath`; move is a no-overwrite filesystem rename. Cross-filesystem moves fail without deleting the source. `archive` writes ZIP, TAR or TAR.GZ at `path` from `paths`. Optional `format` accepts `zip`, `tar`, `tar.gz`; omitted format is inferred from `.zip`, `.tar`, `.tar.gz`, `.tgz` (other names default to ZIP). `extract` detects ZIP, TAR, gzip-compressed TAR and bzip2-compressed TAR from content and writes into the existing directory `newPath`. Both clients' generic terminal operation interfaces accept the same fields, headers, observations and applicable cancellation as Web. The Web archive dialog accepts `.zip`, `.tar`, `.tar.gz` and `.tgz`; TAR.BZ2/TBZ2 are extract-only. RAR/7z/XZ codecs are not included.

```sh
nodedance describe files.action
nodedance call files.list --params '{"nodeId":"NODE_UUID"}' --query '{"path":"/srv","after":""}'
nodedance call files.search --params '{"nodeId":"NODE_UUID"}' --query '{"path":"/srv","filter":"conf","recursive":"true"}'
nodedance call files.action --params '{"nodeId":"NODE_UUID"}' --body '{"action":"copy","path":"/srv/example.conf","newPath":"/srv/example.conf.copy"}' --idempotency-key COPY_KEY
nodedance call files.action --params '{"nodeId":"NODE_UUID"}' --body '{"action":"archive","path":"/srv/config.zip","paths":["/srv/config"]}' --idempotency-key ARCHIVE_KEY
nodedance call files.action --params '{"nodeId":"NODE_UUID"}' --body '{"action":"archive","path":"/srv/config.tar.gz","format":"tar.gz","paths":["/srv/config"]}' --idempotency-key TAR_KEY
```

Web 的菜单、右键操作、复制移动、ZIP／TAR 系列压缩解压、文本编辑、图片编辑、上传下载和搜索均使用这些契约。主题与语言跟随 NodeDance；列表／网格、目录树、全屏、导航历史属于浏览器本地视图能力。目录路径写入私有视图 URL，支持前进／后退和刷新。不另维护业务权限或确认规则。

## Bounds and outcomes / 边界与结果

Directory/search pages contain at most 100 entries and fit the bounded file control envelope (256 KiB); `nextAfter` is the last path and is empty at completion. Memory during scanning is bounded to a page plus directory batches. Each scan examines at most 100,000 entries with depth at most 64 and respects context cancellation. The Web client reads successive Core pages up to the shared scan bound and presents 50 rows per browser page. It buffers the resulting entries; it does not promise virtualized scanning for 100,000 files. Concurrent directory changes can alter subsequent pages: this is not a filesystem snapshot. Refresh to reconcile a changing directory.

The uploader takes its maximum size from the connected Core contract. One conflicting file does not strand later upload entries. The Web search supports recursive filename search and size filters through the same operations available in CLI and TUI.

Text editing is UTF-8, at most 8 MiB, with the original `expectedVersion`. Large text has a dedicated 12 MiB control/dispatch bound under a 16 MiB frame ceiling; directory controls remain 256 KiB and ordinary task payloads remain 768 KiB. Core limits pending edit bodies to 64 MiB. CLI/TUI consume the discovered input/response limits. Directory refresh does not replace the editor's original version; unmounting the file manager clears its editing baselines. Text read versions hash the exact returned bytes. After saving, Web binds the next editing baseline only to readback matching the saved content; an external replacement requires reopening rather than granting overwrite permission to unseen contents. Saving without a loaded original version requires reopening the file. Changing the destination in the editor performs Save As with no overwrite permission; saving the existing destination includes its loaded version. Text readback must match the saved draft before another save is allowed. File transfer, total copy bytes, archive input bytes and extracted bytes follow the Agent's configured transfer limit (default 1 GiB, maximum 16 GiB). Archives accept at most 100 selected sources and 32 KiB of source-path metadata; directory contents count toward the scan/expanded-size limits. Extraction rejects absolute/traversal paths, symlinks, special entries, duplicate entries and existing top-level targets. It checks the complete manifest before writing staged files. Active HTML/SVG are download-only; passive media previews have sandbox CSP, nosniff and authenticated range responses.

Copy and archive extraction preserve basic file/directory mode bits; new entries are owned by the root Agent. Ownership, ACLs and extended attributes are not replicated.

Every file root uses one owned directory handle and Linux `openat2` with symlink rejection; a failed initialization closes that handle. File operations do not fall back to a second filesystem implementation after shutdown. Unit regressions cover rejected-root descriptor cleanup and protected-path symlink replacement; the browser fixture also rejects symlink reads and traversal through the real Core/Agent path.

Copy and archive outputs are staged and published with no-overwrite rename. Extraction stages the tree, then publishes each top-level entry without replacement; it is not an all-or-nothing multi-file transaction. If publishing is interrupted after some entries move, the outcome is unknown and already-published entries remain. Multi-selection operations likewise consist of independent tasks: completed items are not rolled back if a later item fails. Root filesystem symlinks, credential paths and kernel/runtime paths remain protected. Renaming/deleting an ancestor of a protected path is rejected as well.

The Web driver stores uncertain intent keys and bounded task observations, never file content. Reload and re-entering the same mutation reuse the original intent. Accepted tasks are observed until a real terminal result; an unknown result remains observable and never becomes an automatic new execution. Upload cancellation aborts browser transfer; it does not claim to undo a committed file. `tasks.cancel` retains Core's existing semantics for undelivered work. Agent restart never replays a started file mutation; reconciliation removes deterministic staging remnants while retaining unknown identity.

目录不再因为超过 100 项而直接无法浏览，而是逐页读取。读取期间目录变化可能影响后续页，刷新可重新协调。文本保存保留版本冲突检查；复制和压缩先暂存再发布；解压或多选操作发生部分成功时保留真实结果，不伪造原子事务。结果不确定时观察原任务，不自动重做；离开页面或进入游客模式后停止后续私有读取。开发阶段 Core 与 Agent 应来自同一源码修订，不递增产品或能力版本。

## Verification / 验证

Build fresh Web assets, then run:

```sh
node web/scripts/audit-files.cjs
NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs
node web/scripts/audit-interfaces.cjs
node web/scripts/file-notices.cjs --check
```

The file audit uses a disposable real Core/root Agent, private Agent state under `/run`, temporary file fixtures and real Chromium. It verifies pagination/search, copy/move and mode preservation, all supported archive formats, full-size text and escaped CLI responses, edit conflicts before and after saving, image versions, protected paths, missing-file responses, authenticated/ranged passive previews, real installed dialogs, uploads/downloads, directory selection, local assets, language changes and light/dark/narrow layouts. Test-only client probes are not shipped. No production files, systemd units or Docker resources are targeted.

真实浏览器回归使用隔离 Core／root Agent 与临时文件，覆盖现有文件操作、安全和数据完整性边界，以及实际 shadcn 控件和弹窗；不使用生产资源，不安装 systemd 服务，不执行 Docker 修改，不默认生成截图。

## Browsing, editing and recovery / 浏览、编辑与恢复

Concurrent identical reads are coalesced, with at most two reads per mounted manager to reserve transfer capacity. Grid uses the actual upstream elFinder view, not a recreated icon layout. File panes use vertical scrolling only; grid columns adapt to the pane, and the list reduces visible columns when space is narrow while full metadata stays in File information and sorting stays in View. Long names show their beginning and ending with the middle clipped. The single address row shows breadcrumbs by default. Clicking the current directory or remaining address space replaces it with the installed Input, selecting the full path. Enter navigates, Escape cancels, and clicking outside restores breadcrumbs; Ancestor directories remain fully visible when they fit. Actual control widths determine a middle-only collapsed range only after overflow, retaining as much of both ends as fits and restoring directories when the address row widens. Folded directories remain navigable from the menu. Directory changes within the same node preserve the page scroll position. The directory tree and file pane share the available viewport height regardless of entry count, including their visible frames. Table rows retain their natural height; remaining space is blank above the fixed item count bar, without extending the sorted-column tint. Overflow scrolls vertically inside each pane. Loading stays inside the file pane. Both panes use the installed UIArc Scroll Area overlay scrollbars; remaining scroll surfaces share the project scrollbar colors. File dialogs portal into the top-level theme boundary so transformed page sections cannot crop the overlay or offset a scrolled dialog. Nested dialogs retain their own stacking order. Dialog headers have no divider; long dialog bodies scroll in the installed Scroll Area while the title and close control stay visible. Directory contents are read fresh. Preparing a static screen fetches only the discovered operation contract, never remote files. Initial browsing shows loading immediately; refreshing retains the existing listing. Node changes and unmounts abort old reads and prevent late results from replacing another node's view.

同一管理器合并并发重复读取，最多同时读取两次，为文件传输预留容量；目录内容不使用过期缓存。预加载只准备操作契约和界面，不读取远程文件。首次读取立即显示加载状态，刷新保留原列表。切换节点或关闭界面取消旧读取，过期结果不能覆盖新节点。

Text and image editors retain the original file version, keep drafts after failed saves and ask before discarding unsaved changes. Image editing keeps crop, flip, rotation, resize, freehand/line drawing, shapes, text, icons, masks, filters, object removal, zoom, undo/redo and JPEG quality controls. HTML and SVG remain inert text, and passive previews use the authenticated Core endpoint. Pending task identities and observations remain bounded in session storage without storing file contents; re-observation uses the original task rather than resubmitting unknown work. Cancel retains Core's existing semantics and does not promise to undo committed changes.

文本和图片编辑沿用原文件版本，失败保留草稿，丢弃未保存修改前确认。图片保留裁剪、翻转、旋转、尺寸、自由绘图／线条、形状、文字、图标、蒙版、滤镜、对象删除、缩放、撤销重做和 JPEG 质量。HTML／SVG 仅以惰性文本呈现，媒体预览仍使用鉴权 Core 地址。任务观察和不确定请求身份有界保存在会话存储中，不存储文件内容；恢复观察不自动重做未知操作，取消不声称撤销已提交文件。

文件网格图标通过 elFinder 官方 `themes`／`theme` 接口注册 `nodedance` 主题，Manifest 位于 `web/public/vendor/elfinder/themes/nodedance/manifest.json`。主题包含用户提供的 36 图 Sprite 与 10 种独立卷图标，各提供 SVG 和 PNG；主题 CSS 仅替换原有图像引用，保留官方偏移、布局和交互。旧官方基础大图、卷图标及基础 CSS 中对应的图片引用已移除，避免加载旧图；官方网格布局、图标偏移和交互保持原样。elFinder 保留来源、版本与许可证元信息，不再对其前端资源执行官方 SHA-256 清单校验。

Text previews and editing share a lazy-loaded CodeMirror 6 engine with its full basic editing setup and the complete installed language-data catalog. Language parsers load on demand by filename; unknown formats remain plain text. Line numbers, history, search/replace, folding, bracket matching, completion and keyboard editing use the editor engine; enclosing dialogs and file actions remain installed shadcn/UIArc controls. No linter or formatting validation is enabled. Editor transactions preserve untouched LF, CRLF and mixed line endings, and saves retain the existing Core version/conflict/readback contract.

文本预览与编辑共用按需加载的 CodeMirror 6，启用完整基础编辑功能及语言目录；外部撤销、重做和查找按钮复用已安装控件。支持 Ctrl/Cmd+S 保存，查找栏打开时 Escape 先关闭查找，不触发外层草稿丢弃确认。未安装或启用语法校验器、格式校验和自动格式化，保存仍由 Core 返回结果。

## Search and creation / 搜索与新建

The file toolbar exposes icon-only Refresh, Search and Upload controls with accessible labels. New groups folder creation, ten server file presets (TXT, Shell, YAML, JSON, TOML, INI, CONF, ENV, systemd service and Dockerfile) and arbitrary custom filenames. Presets only initialize the filename; creation remains the same Core operation with no overwrite bypass or extra client validation.

Search is a separate installed Dialog with filename queries, recursive current-directory/selected-directory/current-node-root scopes, existing Core size limits, local type filtering, pagination and sort. Scope and selected-result paths reuse the file manager's measured middle-folding breadcrumb. Results support selection, context-menu Open, Show in directory, Edit, Download and Info; reveal navigates to the containing directory and selects the file on the correct page. The footer has the installed font-size Slider, right-preview toggle and a page-local pin that keeps the search window open while allowing background interaction. Pinning does not make the browser window stay above other applications.

Read-only search previews use the existing text/image/PDF/media presentation. Preview reads do not update an editor's expected file version. Search and preview requests are bounded by the shared file client and canceled when closed or superseded. Search operates on filenames, not file contents; no unsupported content-search controls are shown. Guest access, protected paths and file mutation checks remain enforced by Core/root Agent.

工具栏刷新、搜索、上传使用带可访问名称的图标按钮。新建合并文件夹、十种服务器常用文件和自定义文件名入口，仅预填名称，沿用 Core 创建流程。搜索弹窗支持当前目录、指定目录、本机根目录的递归文件名搜索、类型及大小筛选、分页排序、右侧预览、字号调整与页面内置顶；作用域和选中文件路径复用现有按实际宽度折叠中间层级的面包屑。直达会打开所在目录、跳到目标分页并选中文件。被动预览不会改写编辑版本基线；关闭或替换搜索会取消旧请求。

## File sharing / 文件分享

Both list and grid right-click menus provide Share for regular files and directories; Manage shares opens settings and file collection using the same Core contract. Folder shares preserve browsable relative paths. Node shares publish verified snapshots, and revocation never deletes source files. See [file sharing](file-sharing.md) for operations, password/direct-link rules, lifecycle, bounds and verification. The file browser audit includes the maintained sharing browser regression.
