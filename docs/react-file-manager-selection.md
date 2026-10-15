# React 迁移：文件管理器选型

状态：用户已选择 elFinder；Web 已迁移 React，文件界面由 React 生命周期挂载 elFinder 2.1.70。日期：2026-10-15。

用户要求迁移 React，同时文件管理必须使用成熟第三方组件的内置功能，不自行实现文件管理界面。保留当前 Core 操作契约、权限、版本冲突和任务恢复语义。

已核查官方文档并克隆源码至 `.references/`（Git 忽略）：

| 方案 | 实现与许可 | 内置能力与差距 | 判断 |
| --- | --- | --- | --- |
| [SVAR React File Manager](https://github.com/svar-widgets/react-filemanager) | 原生 React，MIT；所查源码 package 2.6.0 | 目录树、面包屑、列表/图块、双栏、多选、搜索、上传下载、复制移动、重命名、删除、主题和本地化；所查 API 与组件没有文本编辑、压缩、解压功能 | 原生 React 候选；目前不能作为 VueFinder 完整等价替换 |
| [Cubone React File Manager](https://github.com/Saifullah-dev/react-file-manager) | 原生 React，MIT | 常规文件操作、导航、多选、上传与媒体预览；没有在所查源码中发现完整文本编辑及压缩解压界面 | 同样有功能缺口，不能靠自写补齐后宣称完全内置 |
| [elFinder](https://github.com/Studio-42/elFinder) | jQuery / jQuery UI，BSD-3-Clause | 内置编辑、压缩、解压、搜索、预览、多选、复制移动等；已有 archive/extract/edit 命令与对话框实现 | 功能完整候选，可由 React 生命周期挂载成熟控件；不是原生 React 组件 |
| [Syncfusion React File Manager](https://ej2.syncfusion.com/react/documentation/file-manager/file-operations) | 商业组件，社区许可需满足条件 | 内置常规文件操作、搜索和图片预览；多文件 ZIP 下载不等于任意服务器目录内压缩与解压，文本编辑不能按文件操作文档假定内置 | 不凭组件知名度认定功能等价，也不默认引入付费授权 |

## 需要确定的取舍

- 已采用：React 内嵌 elFinder，复用其原生界面，不自写压缩、编辑和选择对话框。
- 原生 React 优先：SVAR 可覆盖常规文件管理，但需要另行选用成熟编辑器等组件；研究阶段没有发现单个组件完整覆盖这些操作的证据，因此本次没有采用它们。

用户已明确选择功能完整的 elFinder。目录树、工具栏、菜单、编辑、压缩解压和目录选择均使用其内置 UI。图片编辑使用 elFinder 官方默认编辑器集成中的 TOAST UI Image Editor 3.15.3；资源在本地托管，不使用外部 CDN。React 只负责挂载、销毁、主题和已发现的 Core 契约传输。

## 接入边界与验收

第三方组件提供界面，不替换 NodeDance Agent 文件执行服务，也不安装组件示例自带的 PHP/Node 服务。所有操作从连接 Core 的 `/api/v1/operations` 发现并调用；编辑保存继续携带原始 `expectedVersion`，上传限制由契约读取，结果不确定时观察原任务而非重发。Core 与 root Agent 继续拒绝越界路径、符号链接、凭据与受保护目录。

Compose 目录选择器使用同一个 elFinder，以配置限制为目录浏览/新建目录/选择，不自行实现一套文件树，也不把编辑删除等权限带入目录选择流程。

迁移时更新并保留 `audit-files.cjs`，使用隔离 Core/root Agent、真实组件对话框和临时目录检查分页、搜索、上传、复制移动、压缩解压、编辑冲突、被动预览、主题、移动端和焦点；另运行游客安全套件与许可证检查。不能仅把 VueFinder CSS 选择器改名就视为迁移验证完成。
