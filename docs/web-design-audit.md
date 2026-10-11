# Web design audit / Web 设计复审

The audit uses [DESIGN.md](../DESIGN.md) for the warm palette, regular serif display type, quiet borders, control states and responsive spacing. It covers the application shell and every Web component. Operational screens adapt the marketing guide’s spacing to monitoring density; NodeDance retains its own identity.

本次复审以 DESIGN.md 为基准，逐项检查应用框架和全部 Web 组件。监控界面按信息密度调整营销页留白，保留 NodeDance 自有标识。

## Findings and repairs / 问题与修复

| Finding / 问题 | Repair / 修复 |
| --- | --- |
| Default blue/visited links and unthemed selection / 默认蓝色链接、选择色 | Shared warm link, selection, keyboard focus and pressed states / 共用暖色交互状态 |
| Bold display headings and undefined font aliases / 粗体展示标题与错误字体变量 | Regular serif display; 400–500 sans UI; shared font names / 常规衬线标题、统一字体变量 |
| Inconsistent controls, cards and heavy shadows / 控件、圆角和阴影不一致 | 8px controls, 12px cards, at least 40px standard controls; remove decorative shadows / 统一尺寸，移除装饰性重阴影 |
| Green navigation and primary actions / 绿色导航及主操作 | Coral actions and warm selected navigation; semantic health remains distinct / 珊瑚色操作、暖色选中态，保留健康状态语义 |
| Scoped parent buttons missing in Compose children / Compose 子组件遗漏按钮样式 | Shared action classes and a browser-control baseline / 共享操作按钮样式 |
| Native confirmation/prompt chrome / 浏览器默认确认、输入弹窗 | Shared themed modal, native dialog focus handling, cancel resolves without submitting / 统一弹窗，保留取消和身份确认校验 |
| Terminal heading unreadable on dark surface in light theme / 浅色主题下终端标题对比不足 | Dedicated dark product tokens; focus returns on closing, terminal keystrokes preserved / 共用深色产品变量、关闭恢复焦点 |
| Initial update result discarded when a node is connected / 有节点时首次更新结果被丢弃 | Watch the selected update target, guard stale asynchronous results / 监听实际更新目标并丢弃过时结果 |
| Unreliable local font fallbacks and excessive first load / 字体回退不稳定、首屏过重 | Bundle subset fonts; load terminal/chart when opened; import only required chart modules / 本地裁剪字体、按需加载与按模块引入 |
| Cool browser theme color / 浏览器主题栏冷色 | Follow light/dark canvas / 跟随浅色与深色背景 |

## Browser coverage / 浏览器覆盖

The browser audit renders production assets with isolated API fixtures. It reads the operation catalog from a running Core; fixture actions never reach that Core. It checks English and Chinese at 1440px, 390px and 320px in both themes, headings, links, keyboard focus, standard control heights, horizontal overflow and browser exceptions.

| Area / 区域 | Coverage / 覆盖 |
| --- | --- |
| App shell / 框架 | Desktop and mobile navigation, settings, theme and language controls / 导航、设置、主题及语言控件 |
| Authentication / 认证 | Login, setup and Core unavailable / 登录、初始化和 Core 不可用 |
| Nodes / 节点 | Empty, populated, online, offline, pending; overview and metrics / 空数据、有数据、在线、离线、待注册、概览与指标 |
| Node detail / 详情 | All nine tabs, service probe form, container creation, image list, Compose creation/editor, rebuild preview, file editor, history/chart, task history and preferences / 全部九个标签和对应表单 |
| Enrollment / 安装 | Docker/native command results / Docker 与原生安装命令展示 |
| Discovery / 发现 | Unavailable state, peer and SSH deployment form / 不可用状态、设备与 SSH 部署表单 |
| Alerts / 告警 | All six tabs, rule/channel/window forms and empty states / 全部六个标签、规则、渠道、维护窗口表单及空状态 |
| Updates and operations / 更新及通用操作 | Initial check with connected nodes, release links, disabled confirmation, contract operation selection / 已连接节点的首次检查、发布链接、确认前禁用、契约操作选择 |
| Dialogs and terminal / 弹窗与终端 | Confirmation/prompt cancellation and initial values; terminal error, close and focus handling / 取消确认、输入初始值、终端错误、关闭与焦点 |

Verification: production Core assets passed 447 browser snapshots across the documented matrix; frontend types, bilingual strings and operation discovery passed. Font glyph/axis checks and repeat-build checksums passed.

验证结果：生产 Core 内嵌资源通过 447 个页面与状态截图检查；前端类型、双语文案、操作契约发现及字体字形/字重/重复构建校验均通过。

## Reproduce / 复现

```sh
make frontend
pnpm --dir web exec playwright install chromium
# Serve the production frontend and run a current Core on separate local ports.
python3 -m http.server 5191 --bind 127.0.0.1 --directory internal/core/webassets/dist
# In another terminal; NODEDANCE_CORE_AUDIT_URL must expose /api/v1/operations.
NODEDANCE_WEB_AUDIT_URL=http://127.0.0.1:5191 \
NODEDANCE_CORE_AUDIT_URL=http://127.0.0.1:18192 \
pnpm --dir web run audit:design
```

Screenshots and a machine-readable result are written to `.build/web-design-audit/`. Fixtures verify layout and presentation; they do not certify live SSH, Docker, SMTP or mutation execution.

截图和检查结果保存在 `.build/web-design-audit/`。该检查验证布局与展示，不代替真实 SSH、Docker、SMTP 或变更操作测试。

## Font scope / 字体范围

Copernicus and StyreneB are licensed fonts. The bundle uses DESIGN.md’s permitted EB Garamond/Inter substitutes and JetBrains Mono. The three font files total 51,628 bytes (54.6% smaller than their initial Latin sources). Only supported weights, printable ASCII, Latin-1 and common punctuation are retained; other characters, including Chinese, use system fallbacks. Native file/color pickers and expanded single-select menus remain platform controls.

商业字体未随项目分发；使用文档允许的开源替代字体。中文及其他未打包字符通过系统字体显示。文件、颜色选择器及展开的原生下拉菜单由操作系统绘制。
