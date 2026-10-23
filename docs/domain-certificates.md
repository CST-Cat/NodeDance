# 域名与证书 / Domains and certificates

“域名证书”位于节点总览下方，仅管理员可用。Web 使用经 shadcn CLI 安装的本地 UIArc 组件，并将配色、焦点和圆角映射到项目现有 Token。全部管理行为来自 Core 的 `/api/v1/operations`，CLI、TUI 和 Web 操作中心自动发现同一组能力。

## 能力与边界

- 域名：IDNA 规范化、备注、修订号更新、最多 500 条原子批量录入、DNS A/AAAA/CNAME 查询。已有同名同内容记录可重用；冲突不会覆盖。
- 证书：匹配 PEM 证书链与私钥导入、SAN 覆盖校验、有效期与状态、公共链下载、需确认和修订号的私钥 ZIP 导出、策略修改与删除。
- ACME：Let’s Encrypt 正式环境和测试环境，HTTP-01 与 Cloudflare DNS-01，EC P-256 与 RSA-2048 签发、续期、撤销。通配符必须使用 DNS-01。当前不提供其他 CA、其他 DNS 服务商或向远程服务自动安装证书的能力。
- 后台任务：持久队列、稳定请求标识、观察、取消、显式重试；续期失败保留旧证书和私钥。Core 重启将中断任务标记失败，待用户检查后重试。
- 自动化：按证书配置提前 1–90 天续期；通过已有且启用的通知渠道发送到期提醒。默认提前 30 天续期、14 天提醒；成功通知按渠道每日去重，失败渠道五分钟后重试。通知失败记录在证书元数据。

仅录入域名或创建 CA 账号不会联系 CA。签发／续期／撤销提交后由 Core 后台执行，会产生真实外部效果；先使用测试环境验证配置。导入不代表证书链被公共信任：Core 校验链的顺序和签名及私钥匹配，不代替浏览器信任库验证。

## 验证配置

HTTP-01 要求域名的公网 **80 端口**将 `/.well-known/acme-challenge/` 转发到 Core，保留原始 Host。Core 仅为进行中的挑战返回对应域名的临时令牌，其余请求返回 404；不访问磁盘文件。反向代理的重定向、访问控制和 Cloudflare 策略需要允许 CA 访问该路径。

DNS-01 使用指定 Cloudflare Zone ID 和仅限该区域 DNS 编辑权限的 API Token；区域必须覆盖申请域名。Core 等待 TXT 可解析后验证，只清理由本系统创建且带所有权标记的 TXT，不删除用户已有记录。清理失败保留持久记录并后台重试，未清理完时阻止删除对应提供商。

CA 账号需要联系邮箱、环境和明确的服务条款确认。账号密钥、DNS Token、证书私钥仅保存在 Core 的私有数据库中，目录 0700、数据库 0600；备份也应视为密钥材料。列表、任务结果和审计不包含这些秘密。更新 DNS 提供商时省略 Token 可保留原值。

## CLI：给 human 和 LLM 的稳定输入输出

先登录，再从当前 Core 获取真实 schema，避免凭记忆拼请求：

```sh
nodedance auth login --body @/private/login.json
nodedance operations
nodedance describe certificates.issue
nodedance describe certificates.export
nodedance call domains.batch --body @/private/domains.json
```

`domains.json` 示例：

```json
{"domains":[{"name":"example.com","label":"主站"},{"name":"*.example.com"}]}
```

保存创建响应里的 UUID，使用 `certificates.account-create` 和 `certificates.provider-create` 的发现 schema 准备私有 JSON 文件。秘密放在 0600 文件或标准输入，避免进入命令行历史。以下 `issue.json` 中的 ID 必须替换为实际创建结果：

```json
{"name":"主站证书","domainIds":["DOMAIN_UUID"],"accountId":"ACCOUNT_UUID","challenge":"dns-01","providerId":"PROVIDER_UUID","keyType":"ec256","autoRenew":true,"renewBeforeDays":30,"notifyBeforeDays":14,"channelIds":[],"confirm":true}
```

```sh
nodedance call certificates.issue --body @/private/issue.json \
  --idempotency-key website-issue-001 --wait --timeout 0
nodedance call certificates.jobs
nodedance call certificates.job --params '{"id":"JOB_UUID"}'
nodedance call certificates.job-cancel --params '{"id":"JOB_UUID"}' --body '{"confirm":true}'
nodedance call certificates.job-retry --params '{"id":"JOB_UUID"}' \
  --body '{"confirm":true}' --idempotency-key website-retry-001 --wait --timeout 0
nodedance call certificates.export --params '{"id":"CERTIFICATE_UUID"}' \
  --body '{"confirm":true,"revision":1}' --output /private/certificate.zip
```

重用请求标识只用于**完全相同的意图**；相同标识的不同内容被拒绝。停止等待不会取消后台任务，取消必须调用取消操作。取消不回滚已经完成的外部 CA／DNS 请求；遇到结果不确定的撤销或签发，先观察任务与 CA 状态再决定是否重试。先读取当前修订号，再执行更新、续期、撤销、删除或私钥导出。导出以独占创建方式写入 0600 文件，不覆盖已有文件。终态任务保留 90 天；不要依赖超过保留期的请求标识去重。

## JSONL 批量操作与 TUI

```sh
nodedance batch --input /private/operations.jsonl --dry-run
nodedance batch --input /private/operations.jsonl --timeout 0
nodedance batch --input - --continue-on-error < /private/operations.jsonl
```

```jsonl
{"operation":"domains.create","body":{"name":"one.example.com"}}
{"operation":"domains.create","body":{"name":"two.example.com"}}
{"operation":"certificates.issue","bodyFile":"/private/issue.json","idempotencyKey":"website-issue-001","wait":true}
```

每行一个对象，字段支持 `operation`、`params`、`query`、`body` 或 `bodyFile`、`headers`、`idempotencyKey`、`fileVersion`、`inputFile`、`outputFile`、`wait`。最多 1000 条、总计 64 MiB；所有条目先按连接 Core 的 schema 校验，再顺序执行。`--dry-run` 只验证请求形状，不能代替服务端权限、资源状态或外部验证。输出每行带 `index`、`operation`、HTTP 状态与结果；任意失败返回非零。默认首次执行失败停止，`--continue-on-error` 继续执行其他条目但仍返回非零。批处理不提供跨操作事务，也不会将前一行返回的 UUID 自动插入后一行：关联操作分阶段生成下一批。二进制下载必须提供 `outputFile`，流式终端使用专用入口。

TUI 搜索相同的 `domains.*`、`certificates.*` 操作，填写同一 JSON/schema，使用同一执行器、确认、修订号、任务观察与下载逻辑。选择 `client.batch`，输入原生命令参数 JSON 数组，例如 `["--input","/private/operations.jsonl","--dry-run"]`，即可执行与 CLI 完全相同的批量流程。

## 验证

`make build` 后运行 `node web/scripts/audit-domains.cjs`。隔离真实 Core 验证管理员边界、导航顺序、录入／批量／导入／私钥下载、CA 账号设置、浏览器不持久化私钥以及中英文／深浅色／手机布局。Go 测试覆盖真实 ACME 协议与模拟 CA、Cloudflare 所有权清理、任务失败恢复、通知退避和 CLI/TUI 契约执行。测试不向真实 CA 或 DNS 服务提交任务。
