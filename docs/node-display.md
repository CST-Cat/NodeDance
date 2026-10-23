# Node names, logos and country / 节点名称、图标与国家

The node display name is the shared `alias` in `nodes.save-preference`; it does
not rename the host or Docker resources. Live metric updates retain unsaved
form values. A saved preference is reflected immediately and survives reload.
Clearing alias restores the node's registered display name.

An empty icon automatically follows the reported Linux distribution. Explicit
choices override it. Built-in choices include general symbols, Linux/BSD logos,
and service brands such as Docker, Kubernetes, Nginx, PostgreSQL, MySQL and Redis.
Core's `dashboard.PreferenceIcons()` defines valid IDs and publishes the same enum
in `/api/v1/operations`. Web discovers that enum and uses installed shadcn Registry
controls with local logo previews. CLI and TUI discover exactly the same choices;
there is no separate client validation or operation list.

```sh
nodedance describe nodes.save-preference --server https://core.example
nodedance call nodes.preferences --server https://core.example --params '{"nodeId":"NODE_UUID"}'
# Submit the current preference object, changing alias/icon, using --body - or @file.
nodedance call nodes.save-preference --server https://core.example --params '{"nodeId":"NODE_UUID"}' --body @/private/preference.json
nodedance nodes list --server https://core.example
```

Private `nodes.list` responses include a `country` object with `status`, and when
known, `code`, `name`, `source` and `sampledAt`. CLI/TUI expose these fields through
the same discovered read operation. Core prefers the Agent-reported public exit
IP; when unavailable it uses the authenticated Agent enrollment audit IP. It
never geolocates the administrator's browser or guesses from a node name. NAT
nodes use the public exit, not their private interface address. Offline nodes may
retain their last reported IP. A node that changes exit IP gets a new lookup.

Core looks up country through HTTPS `ipwho.is`, asynchronously, without cookies
or credentials. Only public IP literals are accepted; private, loopback, CGNAT,
link-local, documentation and reserved addresses are excluded. There are at most
four concurrent requests and 1,000 lookups per rolling day, with a bounded shared
cache. Successful results cache for 24 hours; failures cache for 15 minutes.
Failures, unavailable IPs and pending lookups remain `unknown`; they never block
node metrics or turn an unknown location into a guessed flag. Country is IP-based
geolocation, not a physical-location guarantee.

管理端节点卡片左上角显示国家标记，右上角显示发行版/自选图标。所有图标槽位共用
`--product-logo-size: 28px`，包含国旗、系统图标、服务 Logo 及选项预览。
识别不了国家时使用同尺寸占位。国旗和 Logo 均随 Web 打包，浏览器不访问
第三方图标或定位服务。许可见 `web/public/licenses/*-notices.txt`。

Country metadata is private management data and is not added to guest projections.
Existing explicit guest names, metric opt-in and anonymous API boundaries remain
unchanged.

Verification: `make build`, `node web/scripts/audit-preferences.cjs`, Go tests for
`internal/core/geolocation`, `internal/control`, `internal/core/server` and
`internal/core/cli`, plus the isolated guest browser security suite. The preference
browser audit uses a real temporary Core and protocol-only simulated Agents,
checks drafts across live updates, submits every discovered icon through CLI,
and verifies save/reload, automatic default, brand rendering and equal-size flags
in light/dark and narrow/wide layouts. Country provider tests inject transport;
no external lookup or real remote resource is needed by the suites.

公网 IP 探测由现有 Agent 配置 `publicIpUrl` 控制。若 Agent 尚未提供公网地址，
且注册记录仅有内网/代理地址，国家为 `unknown`。可通过同一 Core 操作
`agents.configure` 将 `publicIpUrl` 设为 `https://api.ipify.org`，保留其他配置；
Web 的 Agent 配置、CLI 和 TUI 均支持这一字段。读取节点列表本身不会静默修改
Agent 配置。离线且从未提供公网地址的节点需恢复连接后才能完成识别。

### 手动国家 / 地区

节点偏好新增 `countryCode`：空字符串为自动识别，ISO 3166-1 两位大写代码为手动指定（例如 `CN`、`JP`）。在节点设置的“国家 / 地区”选择器中选择并保存即可；手动值优先于公网 IP 识别，本地或仅 Tailscale 连接的 Debian 节点同样可显示国旗，Linux 发行版图标保持独立。切回“自动（公网 IP）”会清除手动覆盖。国旗沿用本地资源和统一 28px 尺寸。

Core 的 `nodes.save-preference` 请求 schema 发布完整国家枚举；CLI 与 TUI 的同一操作支持该字段，读取 `nodes.preferences` 可获取保存值，`nodes.list` 的 `country.source` 为 `manual` 时表示手动设置。提交偏好时需保留其他现有字段。国家偏好仅用于管理展示，不进入游客投影。

节点总览卡片顶部采用三列布局：国旗在左上角，名称居中间列，Linux 发行版或自选服务图标在右上角；两侧使用相同 28px 图标栏和对称边距，版本信息对齐名称下方。

国旗素材采用 Twemoji 的圆角矩形旗面，与 Komari 的 `Flag.tsx` / 中国国旗 SVG 相同：SVG 画布 36×36，常见可见旗面 36×26（18:13），在本项目 28×28 图标槽内等比显示为约 28×20.22，不拉伸或裁切。保留尼泊尔等特殊旗形。固定素材来源为 `jdecked/twemoji@b6b55fef1e8636b540a6d016a4729ca8cdf2e60b`，按 Core 支持的 ISO 3166-1 国家清单打包，许可与作者归属见 `web/public/licenses/country-flags-notices.txt`（CC BY 4.0）。
