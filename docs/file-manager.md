# File manager / 文件管理

Web uses React. elFinder 2.1.70 is mounted and destroyed by the React file screen; its stock toolbar, dialogs, tree, search, selection, text editor and archive actions supply the file-manager UI. The official elFinder editor integration loads TOAST UI Image Editor 3.15.3 for passive image formats. All scripts, styles, icons and license notices ship in Core's embedded Web assets, without an external CDN, PHP connector, new listener or iframe app. Filesystem execution remains in the root Agent through the discovered Core operation contract. The overview card's File shortcut opens this same manager inside the shared wide dialog used by Docker details (Escape and the close button return focus to the shortcut); the node-detail Files tab keeps the inline presentation. Both entries mount one manager with identical commands and semantics; the card dialog keeps the last browsed directory across reopens.

Web 使用 React；文件管理界面采用 elFinder 内置功能，图片编辑采用其官方集成的成熟 TOAST UI 编辑器。React 负责生命周期、主题与 Core 传输，不自行实现文件管理界面。许可证见 `/licenses/file-manager-notices.txt`。总览卡片的"文件"快捷键在 Docker 详情共用的宽弹窗中打开同一文件管理器（Escape 与关闭按钮把焦点还给快捷键）；节点详情的"文件"标签保留内嵌呈现。两个入口挂载同一管理器，命令与语义完全一致；卡片弹窗在重开时保留上次浏览的目录。

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

Directory/search pages contain at most 100 entries and fit the bounded file control envelope (256 KiB); `nextAfter` is the last path and is empty at completion. Memory during scanning is bounded to a page plus directory batches. Each scan examines at most 100,000 entries with depth at most 64 and respects context cancellation. The Web transport reads successive pages into elFinder, up to the shared scan bound. The current component buffers the resulting entries; it does not promise virtualized rendering for 100,000 files. Concurrent directory changes can alter subsequent pages: this is not a filesystem snapshot. Refresh to reconcile a changing directory.

The uploader takes its maximum size from the connected Core contract. One conflicting file does not strand later upload entries. The stock Web search uses filename search. Size filters remain available through the discovered generic operation interface, CLI and TUI.

Text editing is UTF-8, at most 8 MiB, with the original `expectedVersion`. Large text has a dedicated 12 MiB control/dispatch bound under a 16 MiB frame ceiling; directory controls remain 256 KiB and ordinary task payloads remain 768 KiB. Core limits pending edit bodies to 64 MiB. CLI/TUI consume the discovered input/response limits. Directory refresh does not replace the editor's original version; unmounting the file manager clears its editing baselines. Text read versions hash the exact returned bytes. After saving, Web binds the next editing baseline only to readback matching the saved content; an external replacement requires reopening rather than granting overwrite permission to unseen contents. Saving without a loaded original version requires reopening the file. Stock Save As creates a new file through the same operation and binds its first save only after reading back the known empty bytes; later replacements retain normal conflict checks. File transfer, total copy bytes, archive input bytes and extracted bytes follow the Agent's configured transfer limit (default 1 GiB, maximum 16 GiB). Archives accept at most 100 selected sources and 32 KiB of source-path metadata; directory contents count toward the scan/expanded-size limits. Extraction rejects absolute/traversal paths, symlinks, special entries, duplicate entries and existing top-level targets. It checks the complete manifest before writing staged files. Active HTML/SVG are download-only; passive media previews have sandbox CSP, nosniff and authenticated range responses.

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

The file audit builds a disposable real Core/root Agent, uses private Agent state under `/run`, confines all file changes to temporary fixtures and runs real Chromium. It checks pagination, recursive search, copy/move, archive formats, full-size 8 MiB saves/readback and editor rendering, escaped CLI responses, stable task identity, edit conflicts including replacement during read and after save, missing-download status codes, preview authorization/ranges, actual elFinder dialogs, browser history/reload and light/dark/mobile layouts. Preview/edit dialogs must have opaque surfaces, readable text contrast and fit both mobile and desktop viewports. The suite also probes the production Driver in a test-only browser module, 11 MiB uploads, mixed upload failures, size filters, refreshed-directory edit conflicts, image version headers, actual archive/extraction dialogs, full screen, locale switching and keyboard focus. No test module or hook is shipped in Web assets. It does not install systemd services or perform Docker mutations. It requires existing dependencies, Chromium and permission to run its temporary Agent as root. The guest security audit continues to use a protocol-only simulated Agent, discovers private operations dynamically and verifies late file-manager responses after guest entry. Keep scripts; generated databases, binaries and screenshots are disposable.

文本编辑上限为 8 MiB，仍校验 UTF-8 与原文件版本。可创建 ZIP、TAR、TAR.GZ／TGZ；可解压这些格式及 TAR.BZ2／TBZ2。格式由共用 Core 契约公布，归档内容仍受相同路径、链接、权限和大小规则约束。

### Directory browsing cost / 目录浏览开销

List and search versions are metadata fingerprints: browsing never reads file contents. Explicit stat and text reads retain content fingerprints for edit/upload conflict detection. Web coalesces concurrent identical browsing requests and limits them to two reads per mounted manager, reserving Core transfer capacity. Ancestor directory metadata is reused for up to ten seconds (512 entries maximum); mutations invalidate it. Directory listings themselves are always refreshed, and pagination/search bounds remain unchanged.

列表与搜索使用元数据指纹，浏览目录不读取文件内容；显式 stat 与文本读取保留内容指纹，用于编辑和上传冲突检查。Web 合并同时发生的相同浏览请求，每个文件管理实例最多并发两次读取，给文件传输保留容量。父目录元数据最多复用十秒、上限512项，修改操作使缓存失效；目录内容始终重新读取，分页和搜索边界不变。

Cold startup loads the dependent jQuery UI registrations as one lazy bundle. The three self-hosted elFinder scripts download concurrently and execute in insertion order, retaining the same CSP and local-only assets. The real-browser audit checks this with delayed vendor responses so localhost latency cannot hide a serial loading waterfall.

首次初始化将相互依赖的 jQuery UI 注册打包为一次懒加载；三个本地 elFinder 脚本并行下载、按插入顺序执行，保留原 CSP 与本地资源。真实浏览器回归延迟脚本响应，检查并行下载，避免本机低延迟掩盖串行加载问题。

Authenticated node-detail views prepare the static file-manager interface before its tab is selected, without fetching any remote directory or content. The file panel reports loading immediately while its first directory request is pending; the busy state ends only after elFinder opens the directory. Guest/read-only views do not start this warmup.

管理态节点详情提前准备文件管理界面资源，不预读远程目录或文件；首次目录请求未完成时立即显示加载状态，目录打开后才结束忙碌状态。游客／只读详情不启动预加载。

## Loading and long names / 加载与长文件名

Initial reads show the shared loading structure immediately; subsequent reads retain the existing native list with an updating indicator. The documented elFinder cwd name template uses the shared path splitter to keep compound extensions visible. Native Selection Info remains the full-path view, with a shared inline copy control that copies the original decoded file identity, including Unicode, rather than elFinder’s visual zero-width separators. No connector or file-operation semantics change.

首次读取立即显示加载结构，刷新保留已有原生列表。长文件名保留扩展名，完整路径使用 elFinder 原生信息弹窗查看，并复用共享复制反馈。复制原始路径，不包含显示时插入的零宽字符；编辑、重命名、搜索、分页及安全校验沿用原实现。

原地解压返回的 `added` 仅包含操作前不存在的条目，`changed` 仅包含元数据确有变化的旧条目；不把整个目录报告为新增／变更，避免无关文件被全选及额外确认。文件浏览器回归使用 `make build` 生成的 Core／Agent，不再重复编译两份测试二进制。
