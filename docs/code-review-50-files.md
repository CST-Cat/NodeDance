# 50 文件重点深度审查

本次从原未完成清单中固定选择 50 个文件，审查前共 7,254 行。下表每项均已完整阅读；代码修改、新增测试和依赖追踪不用于凑这 50 个文件的数量。没有升级版本、创建标签或发布。

## 修复结果

- **备份取消与密钥大小**：原恢复流程在取消后仍读完 256 KiB 的测试条目；现在在分块读取中检查取消。在线 SQLite 备份使用有界页批次，哈希/归档复制共用取消检查，安装完成产物前再检查。32 字节密钥独立限流，在解包前拒绝超大条目。不能保证中断已陷入故障文件系统的内核调用。
- **配置严格解析**：拒绝第二份 JSON 和损坏尾部；使用同一 origin 验证器处理允许的 HTTPS/开发 HTTP，修复 HTTP 越界端口例外。运行时仍要求非开发环境使用 HTTPS。
- **历史时间窗口**：原先先丢掉小数秒再取整，会查询到窗口外的前一桶并漏掉应包含的最后一桶。现在按完整时间计算 `[from,to)`，统一适用于分钟、小时和访客选定键查询。
- **指标状态边界**：移除重复的 not_sampled 特判，拒绝 unknown 磁盘携带挂载值；合法的首次未采样空状态保持有效。
- **Docker 队列生命周期**：取消后不继续消费积压队列，出队清除底层槽位中的容器引用。
- **精简**：删除未使用的重复 Compose schema，复用完整容器 ID 校验，以标准排序替换手写二次排序。保留有真实隔离作用的接口、DTO 和深拷贝，不按函数长度机械拆分。

## 逐文件结果

| # | 文件 | 审查结论 |
| --- | --- | --- |
| 1 | `internal/core/auth/initial.go` | 检查独立生成用户名/密码、随机熵和显式输入校验；保留。 |
| 2 | `internal/core/auth/initial_test.go` | 检查随机凭据不重复与用户指定字段保留、非法 UTF-8 和长度拒绝场景；保留。 |
| 3 | `internal/core/auth/password.go` | 检查 Argon2id 固定参数、盐、恒定时间比较及版本拒绝；保留现有成本。 |
| 4 | `internal/core/auth/token.go` | 检查 256 位随机 token 和仅持久化摘要的设计；保留。 |
| 5 | `internal/core/agents/enrollment_request.go` | 检查 native/docker 两种当前部署输入及共享 DTO；没有添加退役模式。 |
| 6 | `internal/core/agents/grants.go` | 检查授权上限、到期、撤销、事务和审计；保留。 |
| 7 | `internal/core/agents/repository.go` | 检查单次入网/共享授权消费、凭据命名空间、轮换事务、租约 generation CAS 和撤销；保留，未确认需修改的逻辑。 |
| 8 | `internal/core/audit/audit.go` | 检查 action/outcome/target 白名单和 IP 降级，不记录任意正文或凭据；保留。 |
| 9 | `internal/core/storage/schema.sql` | 检查外键、唯一凭据、节点/监控/访客表约束及查询索引；主 schema 继续作为 Compose 表的唯一定义。 |
| 10 | `internal/core/storage/sqlite.go` | 检查 DSN 连接级参数、WAL/FULL、初始化事务和错误关闭路径；保留现有初始化。 |
| 11 | `internal/core/guest/display.go` | 检查独立公开 DTO：不暴露私有宿主元数据、真实资源 ID 或任意挂载路径字段；保留。 |
| 12 | `internal/core/guest/resources.go` | 检查每次公开读取重新过滤已撤销节点/已删除探针，并参数化资源查询；保留。 |
| 13 | `internal/core/guest/settings.go` | 检查默认关闭、资源显式选择、历史范围、revision CAS、审计与可关闭失效选择；保留。 |
| 14 | `internal/core/guest/settings_test.go` | 检查非法公开字段、重复选择、无历史指标和无内容启用等拒绝测试；保留。 |
| 15 | `internal/protocol/admin_requests.go` | 检查 Web/CLI/TUI 共用管理员请求 DTO 与 required 标记；无独立业务列表。 |
| 16 | `internal/protocol/appearance.go` | 检查头像字段名与 2 MiB 上传限制共享定义；保留。 |
| 17 | `internal/protocol/capabilities.go` | 检查当前能力交集、去重、调用方获得新切片与协商子集；保留。 |
| 18 | `internal/protocol/capabilities_test.go` | 检查可选能力、重复能力、未提供能力和允许空交集的回归；保留。 |
| 19 | `internal/protocol/channel.go` | 检查 WebSocket/HTTP 共用通道接口及上下文所有权；保留薄抽象。 |
| 20 | `internal/protocol/compose.go` | 检查项目 key 绑定路径和顺序、配置摘要、响应身份、项目服务去重及失败状态；保留。 |
| 21 | `internal/protocol/docker.go` | 检查批次水位、快照分片规则、payload 上限、严格 JSON 和归一化资源边界；保留。 |
| 22 | `internal/protocol/files.go` | 检查文件传输 UUID/generation/sequence、chunk 大小和最终摘要、8 MiB 文本边界；保留。 |
| 23 | `internal/protocol/host.go` | 检查设置/脚本/资产输入限制及主机指标投影，未引入客户端独立校验；保留。 |
| 24 | `internal/protocol/images.go` | 检查镜像页响应、过滤器和元数据边界；Core 已限制请求页码上限，未修改现有接口。 |
| 25 | `internal/protocol/metrics.go` | 修复 not_sampled 磁盘例外绕过未知状态不得带值的校验；复用 validateStatus 的现有合法初始态处理。 |
| 26 | `internal/protocol/terminal.go` | 检查方向、动作、目标 ID、输入长度和尺寸限制；保留当前终端契约。 |
| 27 | `internal/protocol/version.go` | 检查唯一 Envelope、generation 与当前 wire/capability 版本；没有升级版本。 |
| 28 | `internal/agent/containerstreams/engine.go` | 检查共享 Moby 客户端、inspect 超时与流 body 归调用方释放；保留。 |
| 29 | `internal/agent/containerstreams/logs.go` | 检查原始/复用日志帧、分块内存上限、取消关闭、tail/since/until 约束；完整容器 ID 校验改为复用协议函数。 |
| 30 | `internal/agent/docker/model.go` | 检查可公开容器模型、内部 Compose 标记不进入 JSON、指针/嵌套切片深拷贝及稳定排序；保留。 |
| 31 | `internal/agent/docker/observer.go` | 修复取消后仍消费队列，以及出队元素仍被底层数组引用；保留溢出重同步和快照分片机制。 |
| 32 | `internal/control/schema.go` | 检查精确整数边界、数值枚举、属性存在性、额外字段和 JSON 尾部拒绝；保留。 |
| 33 | `internal/control/schema_precision_test.go` | 检查 int64/uint64 极值及数值枚举回归；保留。 |
| 34 | `internal/control/contract_test.go` | 检查真实 Core 发现契约、共享输入边界、完成查询不重复提交及传输隔离；保留。 |
| 35 | `internal/control/operations_performance_test.go` | 检查调用方修改 schema 不污染共享目录，以及 lookup 基准测试；保留。 |
| 36 | `internal/agent/terminal/tmuxbundle/archive_linux_amd64.go` | 检查 amd64 build tag、嵌入资源及固定 SHA-256；通过资源校验脚本。 |
| 37 | `internal/agent/terminal/tmuxbundle/archive_linux_arm64.go` | 检查 arm64 build tag、嵌入资源及固定 SHA-256；通过资源校验脚本。 |
| 38 | `internal/agent/terminal/tmuxbundle/archive_linux_other.go` | 检查其他 Linux 架构没有错误复用 amd64/arm64 二进制，安装时校验失败；保留。 |
| 39 | `internal/agent/terminal/tmuxbundle/bundle_linux.go` | 检查 root/0700/所有者、私有工作区、归档 SHA、大小限制和失败清理；保留。 |
| 40 | `internal/agent/terminal/tmuxbundle/manifest.json` | 检查版本、双架构 ELF machine、SHA 与许可证归档元数据一致；无版本变动。 |
| 41 | `internal/agent/transferbuffer/pool_test.go` | 检查缓冲池释放清零的真实断言和分配基准；保留。 |
| 42 | `internal/core/backup/backup.go` | 修复密钥条目错误沿用数据库大小上限；备份/恢复分块复制及快照分页响应取消，最终安装前再次检查。 |
| 43 | `internal/core/backup/backup_test.go` | 检查 SQLite 在线/WAL 备份、恢复管理员/节点/密钥和 0600 权限的集成测试；保留。 |
| 44 | `internal/core/backup/install_linux.go` | 检查 NOREPLACE/EXCHANGE 原子安装、目录身份复核和回交换失败保护；保留。 |
| 45 | `internal/core/backup/install_other.go` | 检查非 Linux 明确拒绝不具备原子保证的恢复安装；不添加不安全替代路径。 |
| 46 | `internal/core/config/config.go` | 修复多份/尾部损坏 JSON 被忽略；移除手写 HTTP origin 例外，复用完整验证并拒绝越界端口。 |
| 47 | `internal/agent/taskjournal/schema.sql` | 检查持久化执行证据、未知状态保留、资源 claim 与备份外键；保留。 |
| 48 | `internal/core/tasks/schema.sql` | 检查 idempotency、资源 claim、journal 更换审查及禁止删除未解决任务的触发器；保留。 |
| 49 | `internal/core/compose/schema.sql` | 确认没有加载/嵌入/调用，且重复主 schema 的 Compose 表及索引；删除此重复定义。 |
| 50 | `internal/core/history/store.go` | 修复分钟/小时及选定键查询的小数秒边界；使用 sort.Strings 替代手写二次排序。 |

## 验证

针对性 race 回归已通过；静态检查及新构建 Web 的访客浏览器安全套件已通过（本次动态发现 109 个私有操作，443 个攻击用例零失败，20 种布局及新增历史边界断言通过）。全仓 root race 首次执行发现上轮探针取消/写入竞争，已修复并补确定性回归；探针包重复 100 次通过，最终全仓 root race 复测全部通过。

全仓复测补充：探针父上下文取消与结果写入存在竞争；关闭中的写入失败现在保留正常取消语义。写入过程取消的回归保留真实失败断言。此处属于收尾发现，不计入固定的 50 文件数量。

原 331 文件清单现已完成 **152 个**，剩余 **179 个**；新增测试另计，55 个文档/锁文件/资源文件继续单独记录。
