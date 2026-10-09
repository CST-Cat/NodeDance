# 对总体实施计划的已授权修订

本文记录后续明确的环境要求变更；原始182个 `Sxx-yy` 编号全部保留。此文早期修订曾要求连续三次验收，该要求已被后续用户明确的单次完整运行规则取代；当前所有阶段以 [plan.md](plan.md) 和 [registry.json](../tests/registry.json) 为准。失败或代码变更后仅重跑受影响测试套件。

## CI、主控机/被控机和用户部署时机

用户授权以 GitHub Actions 做构建和自动测试，完成 Core 与 Agent 后再部署到自己的服务器。不得要求用户在实现阶段提供 VPS，也不得由开发者提前安装到用户自己的生产服务器。测试环境应使用干净的 GitHub-hosted Linux amd64 与 arm64 runner，Docker 场景使用锁定 digest、专属 socket、独立资源根目录的 Docker-in-Docker Engine 28/29。S00 workflow 只证明它实际执行的 S00 项目；workflow 文件存在、未运行、运行取消或运行中均不能记录 CI PASS。正式交付前用户再按文档在自己的主控机和被控机安装。

## 移动环境验收

用户明确表示不要求开发者处理真实 iPad/Android 设备；Web 需要响应式。响应式和触屏交互由 GitHub CI 中 Playwright 的 Chromium、WebKit、Firefox 浏览器自动化与移动视口/触屏仿真覆盖，例如宽度375、768、1440 CSS像素。报告只能写“浏览器与视口/触屏仿真通过”，不能写“真实 iPad/Android 已验证”。

本修订影响原用例：

| 原 ID | 修订后执行环境 |
|---|---|
| S06-11 | CI 浏览器 375/768/1440 视口和可访问性/溢出检查 |
| S06-12 | Playwright mobile viewport + touch emulation 的拖动和操作菜单 |
| S09-08 | Playwright Chromium/WebKit 移动视口、键盘输入及终端辅助控制仿真 |
| S17-01 | 干净、一次性的 CI Linux 环境完成安装；用户VPS在交付后自行安装 |
| S17-03 | GitHub-hosted Linux amd64/arm64 runner 真实启动二进制；交叉编译不足以通过 |
| S17-04 | CI Chromium/WebKit/Firefox 桌面流程和响应式/触屏视口仿真；不要求真实平板/手机 |

这些环境变更不能放宽 S17 的72小时稳定性、备份恢复、性能、依赖、Docker 故障、数据持久化或其它原用例要求。未来安装及浏览器/触屏测试依赖需在对应阶段锁定精确版本，并纳入 CI 报告。

## S00 CI 结构

S00 workflow 使用官方 GitHub-hosted `ubuntu-24.04` 与 `ubuntu-24.04-arm` runner，并把 Engine 28/29 作为独立矩阵维度。每个工作组合执行 `make test-stage STAGE=S00`，该目标先运行 `make check` 和双架构 Core/Agent 构建，再在一次完整运行中逐项执行8项原始 S00 用例。早期规则要求连续三遍的旧运行仅保留为历史证据，不构成当前验收门槛或当前 CI PASS。CI 报告和本地报告的状态区分明确；本地尚无 GitHub Actions 运行时，不能声称 Actions 已通过。

## S01 CI 与浏览器证据

S01 的三个浏览器项目由 Playwright 精确版本锁定；CI 在具备浏览器二进制的 runner 上逐项运行 Chromium、WebKit、Firefox。每个项目都由独立临时 Core/SQLite 数据目录承载，执行 S01-12 setup/login/外观/Session/改密流程及绕过前端文件选择器的真实 HTTP 恶意 SVG/HTML 上传。单个浏览器缺失时保留其他已执行证据，但该浏览器和完整阶段必须是 `NOT_READY`，不能由另外的浏览器替代。

S01-01 至 S01-11 均启动真实 Core 进程并操作真实 SQLite。S01-11 先验证迁移冲突回滚及原行保留，再解除冲突，在同一目录恢复启动；不可写目录测试用不同非 root 用户启动 Core，不能用 root 用户仅 chmod 目录后假设不可写。短 Session/限流/Socket 检查参数仅在 `--dev` 测试配置启用，不改变生产默认值。完整 S01 本机或 CI 报告要求原12项在一次完整运行中全部通过；没有浏览器的本机运行可记录 HTTP/SQLite 结果，但整体必须保持 `NOT_READY`。
