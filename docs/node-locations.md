# 节点城市定位 / Node city locations

默认使用自动定位。Core 从 Agent 已上报的公网 IPv4/IPv6（不可用时使用接入公网 IP）读取 ipwho.is 的国家、城市、州／省和 WGS84 经纬度。保留既有异步缓存、四个并发查询、每天最多 1000 次查询和超时限制。私网、Tailscale/CGNAT、回环与文档地址不会外查。城市或坐标缺失时不会伪造城市，国家仍可单独识别。IP 城市是近似值，可能指向运营商登记位置，管理员可覆盖。

Web 节点设置中的“位置来源”支持：

- **自动定位**：使用 Core 解析结果。选择此项并保存会清除手动位置和手动国家，恢复完整自动定位。
- **选择城市**：在本地城市目录按中英文名称／别名搜索，可使用国家筛选；选择后保存该城市的国家、州／省与坐标。
- **手动输入经纬度**：指定国家、城市名称、州／省和坐标。适用于目录未收录的机房地点与本地节点；纬度范围 -90 到 90，经度 -180 到 180，0 是合法坐标。

所有字段使用已通过 shadcn CLI 安装的 UIArc Input／Dropdown Menu，保留原生模态遮挡、键盘选择和焦点行为。搜索只访问 Core，不从浏览器联系定位商。目录来自 GeoNames cities15000 与 admin1CodesASCII，CC BY 4.0；包含 Core 支持的国家／地区，打包在 Core 中，最多返回 50 条，不需要额外账户。目录不是全部村镇，未收录地点可输入坐标。目录可用 `python3 scripts/build-city-directory.py cities15000.zip admin1CodesASCII.txt readme.txt` 重建，加 `--check` 核对当前记录与源文件；无运行时城市目录下载。出处和变换说明见 `internal/core/geolocation/cities-NOTICE.txt` 与 Web 的 `/licenses/city-locations-notices.txt`。

## 统一操作与数据

连接中的 `/api/v1/operations` 为唯一契约来源，Web、CLI、TUI 均发现并使用：

| 操作 | 输入／用途 |
| --- | --- |
| `locations.search` | 可选查询 `q`、`countryCode`；搜索本地城市目录，返回 `cities` |
| `locations.get` | 路径参数 `id`；读取完整城市记录与坐标 |
| `nodes.list` | 节点 `location` 包含 `status`、`source`、`position`；国家旗帜使用同一位置的国家 |
| `nodes.preferences` | 读取持久化偏好中的手动 `location`（为空表示自动） |
| `nodes.save-preference` | 保存偏好；嵌套 `location` 为手动坐标，`null` 恢复自动城市定位 |

手动 `location` 格式：

```json
{
  "countryCode": "US",
  "city": "Los Angeles",
  "region": "California",
  "latitude": 34.0522,
  "longitude": -118.2437
}
```

`region` 可省略；其余字段必填。显式设置 `countryCode` 偏好时必须与手动位置一致，否则 Core 拒绝整次写入。空位置不会把缺失坐标变为 0，非法范围和非节点资源的位置均被拒绝。保存仍是完整偏好替换：修改位置前读取并合并原偏好，避免丢失名称、图标、SSH 或分组。

## CLI 与批量操作

先登录当前 Core。以下示例中的 `$CORE_URL` 和 `$NODE_ID` 由操作者填写；参数／输入均可来自 JSON 文件或标准输入，适用于 LLM 自动化。

```sh
nodedance operations locations.search --server "$CORE_URL"
nodedance operations nodes.save-preference --server "$CORE_URL"
nodedance call locations.search --server "$CORE_URL" --query '{"q":"Los Angeles","countryCode":"US"}'
nodedance call locations.get --server "$CORE_URL" --params '{"id":"5368361"}'
nodedance call nodes.list --server "$CORE_URL"
```

保留其他偏好后应用位置（需要 jq）：

```sh
umask 077
jq -n --arg id "$NODE_ID" '{nodeId:$id}' > node-params.json
nodedance call nodes.preferences --server "$CORE_URL" --params @node-params.json > current-preferences.json
jq --arg id "$NODE_ID" '
  (.data.preferences | map(select(.targetKind == "node"))[0]
   // {nodeId:$id,targetKind:"node",identity:("node:"+$id),visible:true,pinned:true,sortOrder:-1,sshPort:22})
  | .countryCode="US"
  | .location={countryCode:"US",city:"Los Angeles",region:"California",latitude:34.0522,longitude:-118.2437}
' current-preferences.json > node-preference.json
nodedance call nodes.save-preference --server "$CORE_URL" --params @node-params.json --body @node-preference.json
```

恢复自动（保留其他字段）：

```sh
jq '.location=null | .countryCode=""' node-preference.json > automatic-preference.json
nodedance call nodes.save-preference --server "$CORE_URL" --params @node-params.json --body @automatic-preference.json
```

批量操作仍用已有 `batch`。每个节点先合并其偏好，再生成 `{ "operation":"nodes.save-preference", "params":{"nodeId":"..."}, "body":{...完整偏好...} }` JSONL：

```sh
nodedance batch --server "$CORE_URL" --input locations.jsonl --dry-run
nodedance batch --server "$CORE_URL" --input locations.jsonl
```

## TUI

运行 `nodedance tui --server "$CORE_URL"`，搜索 `locations.search`，按提示输入城市查询和国家；`locations.get` 可查看指定目录 ID。搜索 `nodes.save-preference`，输入 `nodeId`，按发布的 schema 粘贴完整偏好 JSON 或提供私有文件。`location` 对象、任意合法坐标、`null` 恢复自动与 CLI 完全相同；通过 `nodes.preferences`／`nodes.list` 观察，重试同一保存即可。批量预检和执行使用 `client.batch`，不是调用另一界面实现覆盖。

## 地球与隐私

地球优先使用节点的真实解析／手动经纬度，同国不同城市分别绘制标记。同一坐标（四位小数）聚合、依次展示节点；标记标签与卡片显示城市和国家。无城市坐标但有国家时保留国家代表点，不声称精确机房位置；国家也未知时列为待定位。弧线仍只用于展示关联。

位置数据仅向授权管理界面提供。游客数据投影不包含国家、城市、经纬度或手动位置；新增城市目录操作也要求登录。验证使用隔离 Core、模拟 Agent、模拟定位商响应和真实 CLI／TTY TUI／浏览器，不查询生产节点、不控制真实远端资源。

验证命令：`make build`；`go test ./internal/control ./internal/core/server ./internal/core/cli ./internal/core/storage ./internal/core/geolocation`；`node web/scripts/audit-locations.cjs`；`node web/scripts/audit-global.cjs`；`NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs`；`python3 scripts/interface-coverage.py --check`。
