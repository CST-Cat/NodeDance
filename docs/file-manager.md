# File manager / 文件管理

VueFinder 4.7.6 is a complete, lazy-loaded Web component. Its UI and CodeMirror editor ship in Core's embedded Web assets and execute in the browser. The root Agent's existing file service performs filesystem work; no extra file-server process, HTTP listener or iframe application is installed. Third-party license notices ship at `/licenses/file-manager-notices.txt`.

VueFinder 4.7.6 完整引入并按需加载；图形界面和 CodeMirror 编辑器随 Web 资源嵌入 Core，在浏览器运行。文件系统操作仍由 root Agent 的文件服务执行，不增加独立文件服务器、监听端口或 iframe 应用。第三方许可证随资源提供。

## Shared operations / 统一操作

Web, CLI and TUI discover the connected Core contract. `files.list` reads a directory page; `files.search` performs filename search, optional recursion and size filtering; `files.preview` provides authenticated passive media preview. Existing `files.stat`, `files.text`, `files.download`, `files.upload`, `files.action` and `tasks.*` remain the single execution path.

`files.action` supports `mkdir`, `rename`, `move`, `copy`, `archive`, `extract`, `delete` and `save_text`. `copy` and `move` use `path` and `newPath`; move is a no-overwrite filesystem rename. Cross-filesystem moves fail without deleting the source. `archive` writes ZIP at `path` from `paths`; `extract` reads ZIP at `path` into the existing directory `newPath`. Both clients' generic terminal operation interfaces accept the same fields, headers, observations and applicable cancellation as Web. ZIP is the archive format used by VueFinder; TAR/RAR/7z engines are not added.

```sh
nodedance describe files.action
nodedance call files.list --params '{"nodeId":"NODE_UUID"}' --query '{"path":"/srv","after":""}'
nodedance call files.search --params '{"nodeId":"NODE_UUID"}' --query '{"path":"/srv","filter":"conf","recursive":"true"}'
nodedance call files.action --params '{"nodeId":"NODE_UUID"}' --body '{"action":"copy","path":"/srv/example.conf","newPath":"/srv/example.conf.copy"}' --idempotency-key COPY_KEY
nodedance call files.action --params '{"nodeId":"NODE_UUID"}' --body '{"action":"archive","path":"/srv/config.zip","paths":["/srv/config"]}' --idempotency-key ARCHIVE_KEY
```

Web 的菜单、右键操作、复制移动、ZIP 压缩解压、文本编辑、图片编辑、上传下载和搜索均使用这些契约。主题与语言跟随 NodeDance；列表／网格、目录树、全屏、导航历史和收藏属于浏览器本地视图能力。目录路径写入私有视图 URL，支持前进／后退和刷新。不另维护业务权限或确认规则。

## Bounds and outcomes / 边界与结果

Directory/search pages contain at most 100 entries and fit the bounded file control envelope (256 KiB); `nextAfter` is the last path and is empty at completion. Memory during scanning is bounded to a page plus directory batches. Each scan examines at most 100,000 entries with depth at most 64 and respects context cancellation. The Web driver reads successive pages into VueFinder's virtualized view. Concurrent directory changes can alter subsequent pages: this is not a filesystem snapshot. Refresh to reconcile a changing directory.

Text editing is UTF-8, at most 128 KiB, with the original `expectedVersion`. File transfer, total copy bytes, archive input bytes and extracted bytes follow the Agent's configured transfer limit (default 1 GiB, maximum 16 GiB). Archives accept at most 100 selected sources and 32 KiB of source-path metadata; directory contents count toward the scan/expanded-size limits. Extraction rejects absolute/traversal paths, symlinks, special entries, duplicate entries and existing top-level targets. It checks the complete manifest before writing staged files. Active HTML/SVG are download-only; passive media previews have sandbox CSP, nosniff and authenticated range responses.

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

The file audit builds a disposable real Core/root Agent, uses private Agent state under `/run`, confines all file changes to temporary fixtures and runs real Chromium. It checks pagination, recursive search, copy/move, ZIP, stable task identity, edit conflicts, preview authorization/ranges, actual VueFinder dialogs, browser history/reload and light/dark/mobile layouts. It does not install systemd services or perform Docker mutations. It requires existing dependencies, Chromium and permission to run its temporary Agent as root. The guest security audit continues to use a protocol-only simulated Agent, discovers private operations dynamically and verifies late file-manager responses after guest entry. Keep scripts; generated databases, binaries and screenshots are disposable.
