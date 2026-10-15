# Claude Figma 设计参考

最高优先级：[MCP Apps for Claude UI kit v1.1](https://www.figma.com/design/IO4JYNqClXN6Lx2AtYybth/MCP-Apps-for-Claude--Community-?node-id=0-1)。open-claude-ui 仅补充 Figma 未指定的部分，DESIGN.md 补充运维任务约束。

当前设计数据来自用户提供的完整原始 `.fig`。所有页面、组件集及状态均已在本地可查询；本次重构不再调用有额度限制的 Figma MCP。81 个本地变量保存在 [figma-tokens.json](figma-tokens.json)，桌面、移动、共享组件的尺寸、颜色、状态和交互参数保存在 [figma-component-specs.json](figma-component-specs.json)。文件包含基础规范、图标、Inline card / carousel / fullscreen、桌面与移动组件及宣传素材；聊天内容和宣传图不属于 NodeDance 的管理需求。

已取得的 Figma 导出 SVG CaretDown / Check 分别按 16 px / 12.5 px 原始尺寸使用，位于 `web/public/design/claude/`。箭头使用 CSS 背景绘制，保留游客页面禁止动态图片节点的回归边界；运行时不使用临时 Figma URL。品牌保持 NodeDance。

## 2026-10-15：用户提供的原始 .fig 已离线解码

收到 `MCP Apps for Claude (Community).fig`，60,259,973 字节，SHA256 `aaecfa3d343ab44c561ad0e60088c48f35d95733ccbd27bb7c27cad95826c4df`。文件和离线解析工具保存在已忽略的 `.references/claude-figma/`，没有加入产品依赖或调用 Figma MCP。

- ZIP CRC 完整性检查通过；62 个归档条目，58 个图片文件。
- `canvas.fig` 为版本 106，两个完整块：内嵌 Kiwi schema 和 Zstandard 压缩消息。653 个 schema 定义，消息全部 17,995,555 字节均被解码器消费，无尾部未读数据。
- 26,880 个节点 ID 唯一，全部父引用可解析，没有父链循环。
- 17 个文档页面（含分隔页）与 1 个内部资源画布，和此前 MCP 取得的页面目录对应。
- 81 个当前有效的本地变量，逐项比对此前 MCP 导出的变量 ID、名称、模式和值全部一致。427 个原始变量记录包含引用资源及软删除记录，不能把这个数量当成当前本地变量数量。
- 5,009 个矢量数据块，所有检查到的 commandsBlob 引用有效；图片引用与实例组件定义引用均无缺失。
- 解码 → 编码 → 再解码后的对象深度相等。重新编码后的二进制并非逐字节相同，因此只宣称对象结构往返一致，未宣称二进制无损重写。

原始结构在 `document-raw.json`，按页拆分位于 `pages/`，清单与验证结果在 `inventory.json`。此前未取得的 Sidebar、Radio 等组件及其变体的原始数据现已在本地可查询。此前不完整读取的阻碍已由原始文件解析解决；这不意味着所有页面、组件状态和渲染效果已逐一人工审阅，也不意味着 Web 已完成重构。

用户提出的 `page.exportAsync({format: "JSON_REST_V1"})` 是官方 Plugin API 的单节点导出方式，需要在真实 Figma 插件上下文执行；本次成功的是原始 `.fig` 离线解码，没有声称在 Node.js 中执行过该 API。整文件插件导出须覆盖各页面，并单独保存变量/集合、样式、图像及所需矢量导出；单页 JSON 不能证明全部资源齐全。官方说明：https://developers.figma.com/docs/plugins/api/ExportSettings/

## 当前重构映射

已完整检索桌面/移动/共享组件集、变体、交互目标及引用变量；导出 `figma-component-specs.json`（保留来源 SHA256）。特别解析 `Claude/Light` / `Claude/Dark`，避免把旧引用库的默认浅色值当成深色值；变量颜色透明度不与存储的同值 paint opacity 重复相乘。

离线渲染器 `@grida/refig@0.0.7` 仅安装在忽略的参考目录，未进入产品依赖。实际查看桌面 Controls、Navigation、App、移动 Controls/Examples、Inline desktop 和 Fullscreen desktop。渲染结果保存于 `.references/claude-figma/renders/`；该第三方渲染器对示例中的深色变量/文字解析存在局限，因此颜色以原始变量及其模式解析为准，不能把本地预览称为 Figma 官方截图。

产品共享浅深色主题、默认/次要/危险按钮、输入、下拉、复选框/单选框、焦点、禁用、侧栏宽度与行密度、标题和编辑区配色采用 Figma 数据。主操作从陶土填充改为黑白反色，陶土留作品牌。桌面紧凑控件与移动 44 px/胶囊变体分别处理；保留既有字段分组、卡片和管理入口。终端在自己的容器定义独立深色变量，其他代码编辑区跟随页面主题。

VPS 卡片是产品专用布局：移除主机信息按钮和状态标签，名称以 14 px 独占卡片顶部全宽，避免窄屏和较长状态挤压名称。这项布局按用户要求调整，不冒称为 Figma 原有服务器组件；浏览器回归检查各宽度下名称保持全宽；点击卡片仍查看主机信息，点击名称仍进入详情。

Figma 明确提供的 100–150 ms 交互参数优先使用；未提供的弹层/侧栏参数以 open-claude-ui 补充。Anthropic 字体未分发，Inter 用真实 400–700 范围作本地回退。没有修改 Core 操作接口、权限、任务、CLI/TUI 工作流，也没有递增版本或发布。
