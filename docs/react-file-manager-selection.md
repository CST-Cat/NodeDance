# React file manager / React 文件管理

文件管理主体采用项目已安装的 shadcn／UIArc 组件；网格按用户明确确认的要求，使用隔离加载的 elFinder 原版图标视图和默认主题。当前实现和验证说明见 [file-manager.md](file-manager.md)。

- 总览、节点详情和 Compose 目录选择复用同一个 React 文件管理组件。
- 列表、目录树、工具栏、右键菜单、上传、搜索、分页、编辑器和所有弹窗使用本地安装组件，保留原版样式及键盘行为，只绑定业务数据并安排外部布局。
- 图片编辑保留本地 TOAST UI 无内置 UI 的画布引擎，其参数、工具、撤销重做及保存均由 shadcn 控件呈现。
- Web、CLI、TUI 继续发现同一 Core 操作契约；所有文件执行、权限、路径保护、版本检查和任务语义由现有 Core／root Agent 提供。
- 原版网格只接收当前文件元数据并把操作交回 React，不提供独立文件服务或旧 connector API。
