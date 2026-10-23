# 总览全球 / Global overview

入口位于个人设置上方，URL 为 `#/global-overview`。桌面左侧约 2/3 显示点阵地球，右侧约 1/3 显示随当前可见区域切换的节点信息；窄屏按地球、卡片顺序纵向排列。

四项统计在宽屏与窄屏均为同一窄行，删除说明副文案；直接复用原 MetricCard 内部的 AnimatedCounter，并保留 animateOnView、原数字滚轮和弹簧参数。仅外层排版与字号适配紧凑空间，组件源码不变。

参考仅查看本地 `.references/cobe`（`7e94076fa8c38707f4c9cd114ff4f7c5c880971e`）和 `.references/komari-theme-commander`（`cdf811df675920dd21f31d2dd6026dc7e3e70674`）。Commander 的 `src/data/regionCoords.ts` 使用国家代表坐标，`src/components/Globe.tsx` 给出 Cobe 相机角度约定。本页面沿用该坐标含义，不声称国家标记为机房城市或精确地址。

地球通过 shadcn CLI 安装至 `web/src/components/ui/globe.tsx`，底层已升级至固定版本 Cobe 2.0.1。保留原组件旋转速度、拖拽和弹簧参数；使用 v2 的 `update()` 生命周期，逐帧只更新相机，标记、弧线、颜色按配置变化更新，窗口／侧栏尺寸变化由 ResizeObserver 更新。DPR 只由 Cobe 应用一次；离开页面取消帧循环、释放 WebGL 和原生锚点，并恢复 React 的画布 DOM 所有权。

配置直接使用 Cobe 2 的 `COBEOptions`，支持 `arcs`、单条弧线 `color`、`arcColor`、`arcWidth`、`arcHeight`、`markerElevation`。原生 `id` 生成 CSS 锚点，组件 `children` 通过 portal 进入原生 wrapper，可放置现有 shadcn/UIArc 标签。`onFrame` 只提供当前相机数据用于卡片联动，不保留旧版 `onRender` 参数。

页面以当前展示地区为中心连接其他国家标记，连线仅用于展示关联，不推断真实拓扑、时延或节点间流量。视野内每台 VPS 的名称标签使用原生锚点跟随位置，同一坐标的名称纵向错开、现有 UIArc Badge 显示内容。Cobe 2.0.1 的可见性 CSS 变量输出非数字 token；页面通过已有相机朝向计算标签前后可见性，不把该 token 当作 opacity。未实现 CSS Anchor Positioning 的浏览器不绘制标签，右侧节点信息仍完整可用。卡片、统计、按钮、徽标、进度条、空状态和骨架均复用现有组件，不增加 UI 库或另一套动画。

只读取 Core 契约中的 `nodes.list`、`nodes.preferences`、`nodes.metrics`。地球相机和选中节点属于浏览器本地展示状态，不创建独立后端能力，CLI/TUI 继续读取同样的 Core 数据。节点列表每 10 秒刷新，显示偏好每 30 秒读取且并发最多 4 个，指标进入页面时以最多 4 个并发预加载并按节点缓存，当前节点每次读取结束后间隔 5 秒再刷新；轮播切换保留缓存，刷新失败不清空已有内容。隐藏标签暂停新读取，离开页面中止请求并销毁地球，旧节点的异步返回不会覆盖新节点卡片。

位置优先使用 Core 已有自动公网 IP 城市定位或手动保存的经纬度。同国不同城市单独绘制；同一坐标（四位小数）的多台节点共享标记并轮流展示卡片，标签显示各 VPS 名称，卡片保留城市信息。缺少城市但已知国家时使用国家代表点；国家也未知时不绘制虚构点，列在待定位区域。计数来自真实可见节点，国家与大洲计数不随城市标记数量重复。节点隐藏偏好仍被遵守。位置设置、目录许可和 CLI／TUI 使用方法见 [节点城市定位](node-locations.md)。

只有当前连接、代次和租约有效时显示实时指标；离线、待接入或数据缺失显示缺失状态；请求失败保留已有且连接代次和租约有效的缓存，不把未知值显示成 0%。真实 0% 则保留。无 WebGL 时保留节点信息和说明。

本页仅在管理界面中渲染，未登录深链先验证登录，不读取节点数据。切换游客时立即清除页面、WebGL 和私有读取。许可证与坐标数据库说明位于 `web/public/licenses/global-overview-notices.txt`；提取的代表坐标数据以 ODbL 1.0 继续提供，源数据文件为 `web/src/country-locations.json`。
