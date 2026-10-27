# REST API

适用版本：v1.1.30。机器可读描述：`GET /openapi.json`（OpenAPI 3.1，可导入 GPT Actions）。

所有 `/api/v1/*` 与 `/metrics` 需要 `Authorization: Bearer <TOKEN>`；POST 请求使用 `Content-Type: application/json`。`device` 省略时默认 Hub 本机（别名 `local`），建议始终显式指定。

---

## 1. 运维接口

### Exec 输出参数

`POST /api/v1/exec` 与 MCP `exec_command` 使用相同的可选参数：

- `output_mode`: `raw`（默认）、`tail`、`head`、`count` 或 `none`。
- `max_bytes`: 输出内容字节预算，默认 `4096`，范围 `1..1048576`。超限时 `head` 保留头部，其它正文模式保留尾部，并在正文首行插入 `[truncated: kept <X> of <Y> bytes / <L> lines; sha256=<hex12>]`。
- `dedup_window_seconds`: 默认 `0`（关闭）；大于 0 时，同设备、工作目录、命令、输出模式和字节预算在窗口内重复调用返回 `unchanged/hash/first_seen/cached_bytes/exit_code`，不重发正文。

正文模式返回 `truncated`、`total_bytes`、`total_lines` 和 `sha256` 元数据。`count` 只返回 `total_bytes`、`total_lines`、`exit_code`；`none` 返回退出码与元数据，不返回正文。需要大输出时显式提高 `max_bytes`（最大 1 MiB）。

exec 的返回字段在所有 `output_mode` 下都完全相同，字段始终存在：`device`、`exit_code`、`stdout`、`stderr`、`duration_ms`、`truncated`、`total_bytes`、`total_lines`、`sha256`、`spill_path`、`spill_device`。`sha256` 为 stdout 与 stderr 拼接后的字节流的前 12 位十六进制（格式与之前相同）。`count` 与 `none` 模式下 `stdout`、`stderr` 均为空串。MCP 的 `tools/call` 结果在 exec 成功时同时返回 `structuredContent`（与 text 内容相同的对象）。

输出被截断（`truncated=true`）时，Hub 把完整的 stdout+stderr 写入数据库文件所在目录下的 `mcp-spill/`：目录权限 0700，文件名随机，文件权限 0600。返回的 `spill_path` 为其绝对路径，`spill_device` 固定为 `@hub-spill`。读取方式：`read_file`（或 `POST /api/v1/file/read`）传入 `device=@hub-spill`、`path=<spill_path>`、`offset`、`limit` 分页读取。`@hub-spill` 不要求在令牌的设备白名单中，但仍需要 `files:read` scope。溢出文件保留 24 小时，目录总量上限 256 MiB，超出时先删除最旧的文件。Hub 重启后溢出文件不再可读。溢出文件只对写入它的令牌可见：其他令牌读取、文件不存在、已过期三种情况返回同一个错误 `spill file not found or expired`。溢出文件写入失败时 `spill_path` 为空串，命令结果照常返回。



| 接口 | 方法 | scope | 请求体 / 说明 |
| :--- | :--- | :--- | :--- |
| `/api/v1/devices` | GET | `devices:read` | 设备列表、状态、架构、国家、资源画像 |
| `/api/v1/session/stats` | GET | 任意有效令牌 | 该令牌所有未过期会话的工具调用数、响应字节、粗估 token 数、回传字节最多的 5 个工具与预算状态；不需要新增 scope |
| `/api/v1/session/memo` | GET / POST | 任意有效令牌 | 会话级内存便签；不需要新增 scope；GET 带 key 读值，不带 key 只列元数据 |
| `/api/v1/exec` | POST | `exec` | `{device, command, workdir?, timeout?, output_mode?, max_bytes?, dedup_window_seconds?}`；`timeout` 秒默认 30、最大 300；输出参数见下文 |
| `/api/v1/file/read` | POST | `files:read` | `{device, path, offset?, limit?}`，`limit` 默认 256 KiB、最大 1 MiB；返回 `content, bytes, total_size, has_more, eof` |
| `/api/v1/file/list` | POST | `files:read` | `{device, path, recursive?, max_entries?}` |
| `/api/v1/file/info` | POST | `files:read` | `{device, path}` |
| `/api/v1/file/search` | POST | `files:read` | `{device, root, query, max_results?}` |
| `/api/v1/file/write` | POST | `files:write` | `{device, path, content}`，创建或整体覆写；原子写入（临时文件 + fsync + rename）；保留已有文件的权限与属主，新建文件为 0644；符号链接写入真实目标；硬链接与非普通文件被拒绝 |
| `/api/v1/devices/file/edit_block` | POST | `files:write` | `{device, path, old_text, new_text}`，`old_text` 必须唯一匹配；符号链接写入真实目标（链接保持不变）；悬空链接报错；硬链接（nlink > 1）拒绝修改，返回 `hardlinked file refused` |
| `/api/v1/process/session?action=start\|read\|input\|stop\|list` | POST | `process` | `{device, session_id?, command?, workdir?, data?}` |
| `/api/v1/mcp/list` | GET | `mcp:call` | `?device=`，列出该 Agent 挂载的 MCP 服务 |
| `/api/v1/mcp/call` | POST | `mcp:call` | `{device, server, tool, arguments}` |
| `/api/v1/mcp/test` | POST | `mcp:call` | `{device, service}`，测试一个 MCP 服务配置 |

执行类与文件修改类接口还受节点安全等级约束（见 [security.md](security.md)）。


## 会话预算统计与超阈值提醒

`GET /api/v1/session/stats` 接受任意有效令牌，不要求额外 scope；只返回该令牌当前会话的数据。MCP 调用方使用服务端签发的 `Mcp-Session-Id`，并校验会话归属；普通 HTTP 调用方统一按「认证令牌 + 客户端 IP」归并，**不包含 TCP 源端口**。HTTP 会话默认空闲 30 分钟后过期；超过空闲阈值后下一次请求会建立新会话，统计重置且会话 memo 清空。阈值可由 `VPS_COMMANDER_HTTP_SESSION_IDLE_MINUTES`（正数分钟）覆盖。会话统计和 memo 共用同一身份口径。统计和 memo 只保存在 Hub 内存中，Hub 重启后清零，不写数据库。响应不超过 1.5 KiB，且 `session_id` 是会话标识，不包含令牌或凭据。

响应字段：`tool_calls` 为本会话工具调用次数；`bytes_returned` 为回传给调用方的响应正文累计字节；`est_tokens` 是 `bytes_returned / 4` 向下取整的**粗估值**，不是精确 tokenizer 结果；`top_tools` 最多 5 项，按累计响应字节降序；`budget.limit_bytes` 为阈值，默认 `2097152` 字节，可由环境变量 `VPS_COMMANDER_SESSION_BUDGET_BYTES` 设置；`used_pct` 为已用字节占阈值的百分比，`warn` 仅在累计字节**超过**阈值时为 `true`。

示例响应：

```json
{
  "session_id": "http-opaque-session-id",
  "started_at": "2026-10-14T15:00:00Z",
  "tool_calls": 12,
  "bytes_returned": 98304,
  "est_tokens": 24576,
  "top_tools": [{"name": "exec", "calls": 8, "bytes": 80000}],
  "budget": {"limit_bytes": 2097152, "used_pct": 4, "warn": false}
}
```

MCP 工具响应以及 JSON 对象形式的 HTTP API 响应，在会话累计字节已经超过阈值后，会在后续响应中额外包含 `budget_warning` 字段。例如：

```json
{
  "budget_warning": "session budget exceeded: 2.4MB / 2.0MB; consider narrower queries (output_mode=count, dedup_window_seconds)"
}
```

该字段只在超阈值后的后续响应中出现；首次使累计值超过阈值的响应本身不带警告。既有响应字段与含义不变。`/api/v1/session/stats` 本身不计入预算，也不附加 `budget_warning`，避免统计接口递归计数。

## 会话级 scratchpad（memo_set / memo_get）

便签只存在 Hub 内存，重启后清空，不写数据库。MCP 会话按 `Mcp-Session-Id` 归并并校验令牌归属；普通 HTTP 按「令牌 + `RemoteAddr`」归并。任何有效令牌均可使用，不新增 scope。

Streamable HTTP 的 `Mcp-Session-Id` 处理：未知或已过期的会话 id 不再返回 404。请求在新会话中照常执行，响应头 `Mcp-Session-Id` 返回新 id，新会话归当前令牌所有。会话存在但属于其他令牌时仍返回 403，不会降级或复用。无效令牌仍返回 401。`DELETE` 一个未知或已结束的会话返回 204（幂等，不新建会话）。旧版 SSE 端点 `/mcp/message` 的行为不变。

预算统计按令牌汇总：`/api/v1/session/stats` 与预算告警合计该令牌所有未过期会话的字节。某会话的闲置超时到期后，其字节不再计入。`session_id` 为请求所用的会话，`started_at` 为该令牌最早会话的开始时间。


- `POST /api/v1/session/memo`：写入或覆盖。JSON：`{"key":"checked_sha","value":"已核对结论；sha=abc123","ttl_seconds":3600}`；`ttl_seconds` 可省略，设置后到期惰性清理。成功返回 `{"key":"checked_sha","bytes":27,"expires_at":"2026-10-14T16:30:00Z"}`，未设置 TTL 时省略 `expires_at`。
- `GET /api/v1/session/memo?key=checked_sha`：读取指定便签，返回 `key`、`value`、`bytes`、`updated_at`；不存在返回 `404`。
- `GET /api/v1/session/memo`：仅返回 `{"memos":[{"key":"checked_sha","bytes":27,"updated_at":"..."}]}`，绝不返回 `value`。
- MCP `memo_set` 参数与 POST 一致；MCP `memo_get` 传 `key` 读取，省略 `key` 只返回上述元数据列表。
- 配额：每个 value 最多 4096 字节、每会话最多 32 条、每会话总计最多 32768 字节。任一配额超限返回 HTTP `413` / MCP 工具错误，并说明当前用量和上限。
- 审计只记录 key、字节数与状态，不记录 value；便签不用于保存令牌、密码或其他秘密。

示例：

```sh
curl -X POST "$HUB/api/v1/session/memo" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"key":"checked_sha","value":"结论：已核对；sha=abc123","ttl_seconds":3600}'
curl "$HUB/api/v1/session/memo" -H "Authorization: Bearer $TOKEN"  # 仅元数据
curl "$HUB/api/v1/session/memo?key=checked_sha" -H "Authorization: Bearer $TOKEN"  # 读取值
```

## 2. 管理接口（需要 `admin`）

| 接口 | 方法 | 说明 |
| :--- | :--- | :--- |
| `/api/v1/tokens` | GET / POST | 列出 / 创建命名令牌（创建时返回完整密钥，仅此一次） |
| `/api/v1/tokens/{id}` | PATCH | 修改名称、备注、scope、设备白名单、限速、会话数 |
| `/api/v1/tokens/{id}/rotate` | POST | 轮换密钥 |
| `/api/v1/tokens/{id}/revoke` | POST | 吊销 |
| `/api/v1/audits` | GET | 审计查询：`actor`、`device`、`action`、`result`、`transport`、`from`、`to`，游标分页，每页最多 500 条；列表中的长文本字段会截断并标记 `truncated: true` |
| `/api/v1/audits/{id}` | GET | 获取单条审计记录的完整详情（需要 `admin`） |
| `/api/v1/audits/export.csv` | GET | 按同样条件导出 CSV（有行数上限，超出时末尾带截断标记；字段保持完整） |
| `/api/v1/security/settings` | GET | 全局与各节点安全等级 |
| `/api/v1/security/settings/update` | POST | 修改安全等级 |
| `/api/v1/security/device-credentials/provision` | POST | 为设备签发独立凭据 |
| `/api/v1/mcp/services` | GET / POST | Agent MCP 服务列表 / 保存（热更新到 Agent） |
| `/api/v1/mcp/service` | DELETE | 删除 Agent MCP 服务 |
| `/api/v1/notifications/webhooks` | GET | Webhook 列表 |
| `/api/v1/notifications/webhook/update` | POST | 新增 / 修改 Webhook |
| `/api/v1/notifications/webhook/delete` | DELETE | 删除 Webhook |
| `/api/v1/notifications/webhook/test` | POST | 测试 Webhook |
| `/metrics` | GET | Prometheus 指标 |

### HTTP 请求日志字段

`http_request` 日志包含 `actor_type` 与 `actor`：鉴权成功时记录调用方类型和令牌名称（没有名称时使用身份 ID），面板会话记录为 `panel`；未鉴权请求使用 `-`。已解析的 MCP `tools/call` 请求额外记录 `mcp_tool`（仅工具名）。日志不记录令牌值、令牌哈希、Authorization、Cookie 或 MCP 参数；未鉴权的 `/mcp` 请求不会读取请求体来推断工具名。

## 3. 无需认证的端点

| 接口 | 说明 |
| :--- | :--- |
| `/healthz` | 健康检查 |
| `/openapi.json` | OpenAPI 描述 |
| `/.well-known/oauth-*` | OAuth Authorization Server / Protected Resource Metadata；OAuth 仅在配置 `VPS_COMMANDER_PUBLIC_URL` 后启用 |
| `/agent/ws` | Agent WebSocket（使用设备凭据认证） |
| `/panel/*` | 管理面板（面板密码登录，含 OAuth 客户端管理和授权撤销） |

## 4. OAuth 端点

OAuth 端点只有在配置合法的 `VPS_COMMANDER_PUBLIC_URL` 后启用；未配置时返回 `404`。

| 接口 | 认证 / 说明 |
| :--- | :--- |
| `/oauth/authorize` | 浏览器端点；要求有效面板会话，用户选择命名令牌并明确同意 |
| `/oauth/token` | 服务端端点；使用 `client_secret_basic` 或 `client_secret_post`，支持授权码兑换与刷新令牌轮换 |
| `/api/v1/oauth/clients` | 管理员 API；创建、查询静态 OAuth 客户端 |
| `/api/v1/oauth/grants` | 管理员 API；查询授权记录 |
| `/api/v1/oauth/grants/{id}/revoke` | 管理员 API；撤销指定授权 |

## 5. 状态码

| 状态码 | 含义 |
| :--- | :--- |
| `401` | 未认证或令牌无效 / 已吊销 |
| `403` | scope 不足、设备不在白名单，或节点安全等级拒绝 |
| `404` | 路径不存在（注意结尾斜杠）、MCP 会话已失效；文件读取时目标节点离线也返回 404 |
| `409` | 目标节点离线（`device offline`） |
| `405` | 方法不对 |
| `429` | 超过令牌限速、会话数或 exec 并发；见 `Retry-After` |
| `503` | 服务内部组件不可用，或 SSE 会话发送队列超时 |

## 5. 指标

`/metrics` 包含：`vps_commander_online_agents`、`vps_commander_mcp_sessions`（按传输方式）、`vps_commander_in_flight_exec`、`vps_commander_audit_queue_depth`、`vps_commander_sqlite_write_latency_seconds`（最近一次审计写入）、HTTP 429 计数、`vps_commander_goroutines`、`vps_commander_process_resident_memory_bytes`、`vps_commander_process_cpu_seconds_total`。

## 6. 通用任务回报：`task-reported`

交回任务时使用 `POST /api/v1/events`，`template_id` 为 `task-reported`。`variables` 接受 `employee`、`project`、`task`、`type`、`status`、`ref`、`summary`。标题为 `【{{employee}}】{{type}}{{status}}：{{project}}`，正文包含任务、关联和摘要三行。

- `status` 必填，只接受 `done`、`blocked`、`failed`，存储及渲染前分别转换为「完成」「受阻」「失败」。`task` 和 `summary` 必填。
- 缺省值：`employee` 使用调用令牌名称；名称为空时使用 `source`。`project` 和 `ref` 默认为 `-`，`type` 默认为「任务」。
- `employee` / `project` 最多 32 个 Unicode rune，`type` 最多 16 个，`ref` 最多 64 个。上述字段会去除首尾空白，不允许控制字符或 `【`、`】`，否则返回 `400`。
- 未显式传 `severity` 时，`done` 为 `P3`，`blocked` / `failed` 为 `P2`；显式传入时按传入值处理，但仍受既有 P1 管理员权限及严重级别升级授权规则约束。
- 幂等哈希按缺省值填充前的原始变量计算；`data_json` 保存填充和状态转换后的变量。重复提交同一 `Idempotency-Key` 和原始请求会返回既有事件。

完成示例：

```sh
scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status done --summary '实现完成；全量测试和 check-task 通过。' --employee SWS-E002-gpt --project vps-commander --ref commit-abc123
```

受阻示例（摘要须说明原因及需要谁处理）：

```sh
scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status blocked --summary '受阻：等待 Hark 处理权限规则冲突，处理后继续。' --employee SWS-E002-gpt --project vps-commander --ref T-20261014-10
```

### Retire legacy webhook

`POST /api/v1/notifications/legacy/retire` requires the `admin` scope. Request JSON:

```json
{"legacy_webhook_id":"wecom-dev","channel_id":"wecom-dev-bot","name":"企业微信-开发群"}
```

All three fields are required. `channel_id` follows the notification channel ID rules and must not start with `legacy-`. The source URL must be a valid WeCom bot URL. The operation is atomic: it encrypts the URL into the new `wecom_bot` channel's `secrets.webhook_url`, moves non-legacy tasks, disables the legacy task and channel, clears the legacy endpoint/config, makes pending/in-flight deliveries terminal failures, and deletes the compatibility webhook rows. Historical delivery references require the old channel and task to remain as disabled rows; they are not physically deleted. The response never includes the URL or secrets.

Successful responses include `ok`, `channel_id`, `moved_tasks`, `retired_task`, and `already_retired`. Repeating the same retirement after the source row has been removed returns HTTP 200 with `already_retired: true`, provided the destination and disabled legacy records match the completed retirement. Error statuses: `400` for missing/invalid input or an invalid WeCom URL, `404` when the source webhook does not exist and no completed retirement can be identified, and `409` when the destination channel ID is already occupied. Unexpected internal failures return a generic `500` without exposing endpoint data.

`GET /api/v1/notifications/webhooks` masks each URL to its scheme and host followed by `/…`; the path and query are not returned.

### `GET /api/v1/health/assertions`

Read-only aggregated Hub status assertions. Requires either `devices:read` or `admin`; no new scope is introduced. The response contains `generated_at`, `summary` (`pass` / `fail` counts), and up to 32 `{id, ok, detail}` assertions. The v0.1 set includes service active/restart state, internal/public `/healthz`, retired legacy notification configuration, absence of configured endpoint values and legacy targets, enabled notification routes, pending/failed delivery counts, and SQLite integrity. SQLite integrity results are cached for at least 60 seconds. Individual check failures are reported as `ok: false` with a short sanitized detail; they do not prevent other checks from running. The response is limited to 1.5 KB and never returns endpoint URLs, webhook URLs, secrets, or tokens.

Example:

```json
{
  "generated_at": "2026-10-14T12:00:00Z",
  "summary": {"pass": 9, "fail": 0},
  "assertions": [
    {"id": "service.active", "ok": true, "detail": "active"},
    {"id": "service.restarts", "ok": true, "detail": "NRestarts=0 restarts"},
    {"id": "hub.healthz_internal", "ok": true, "detail": "HTTP 200"},
    {"id": "hub.healthz_public", "ok": true, "detail": "HTTP 200"},
    {"id": "notification.legacy_retired", "ok": true, "detail": "0 active legacy channels or targets"},
    {"id": "notification.no_plaintext", "ok": true, "detail": "0 configured URLs; 0 legacy targets"},
    {"id": "notification.routes", "ok": true, "detail": "9 enabled routes"},
    {"id": "notification.deliveries", "ok": true, "detail": "0 pending; 0 failed deliveries"},
    {"id": "db.integrity", "ok": true, "detail": "ok"}
  ]
}
```


## 异步 exec（MCP exec_start / exec_status / exec_cancel）

预计超过 30 秒的命令（例如 `go test -race ./...`）用异步方式运行，避免单次工具调用一直等待。本机与远端节点都支持；远端节点通过 agent 的会话进程（`start_process`、`read_process_output`、`force_terminate`）运行，不需要升级 agent。

- `exec_start`：参数 `device`（缺省为本机）、`command`、`workdir`。返回 `job_id` 与 `status=running`。
- `exec_status`：参数 `job_id`。轮询直到 `status` 不再是 `running`。
- `exec_cancel`：参数 `job_id`。结束进程（本机结束整个进程组；远端调用 agent 的 `force_terminate`，即 SIGKILL 进程组）。只有确认进程已结束，`status` 才变为 `killed`，`reason=cancel`。

状态取值：`running`、`exited`（正常结束，`exit_code` 为退出码）、`killed`（超时或被取消，`reason` 为 `timeout`、`cancel` 或 `agent_session_ttl`）、`lost`（`reason` 为 `hub_restart`、`node_offline` 或 `session_gone`）、`expired`（结束超过 24 小时）。运行中 `exit_code` 为 -1。

返回字段与同步 exec 相同（`stdout`、`stderr`、`truncated`、`total_bytes`、`total_lines`、`sha256`、`spill_path`、`spill_device`、`timed_out`），另加 `job_id`、`status`、`reason`、`started_at`、`ended_at`、`max_runtime_s`、`remote`。完整输出始终写入溢出文件，用 `read_file` 且 `device=@hub-spill` 分页读取 `spill_path`。

限制与行为：

- 最长运行时间：本机 60 分钟；远端 30 分钟。远端的上限来自 agent 的会话寿命（`executor.DefaultSessionTTL`，30 分钟）：agent 到期会杀掉会话，`start_process` 没有 TTL 参数。Hub 同样在 30 分钟时自行结束进程，原因记为 `timeout`；如果 agent 先结束，原因记为 `agent_session_ttl`。以上数字来自代码，尚未在真实节点上实测。
- 远端输出：输出在 agent 端的环形缓冲中保留，每个流最多 2 MiB（`DefaultRingCapacity`）；更多的输出会被丢弃。
- 远端会话数：每个节点最多 4 个会话（`MaxSessionsPerManager` = 8 MiB / 2 MiB），包括本机其他会话进程。已结束的会话在闲置 10 分钟后才会被 agent 回收，期间仍占用一个会话位；会话位满时 `exec_start` 返回 agent 的错误。
- 取消的确认：远端取消先下发 `force_terminate`；agent 拒绝时返回 `kill failed, job still running`，job 保持 `running`；已下发但 20 秒内未确认进程结束时返回 `kill sent but the process has not been confirmed ended`，job 保持 `running`，之后由 Hub 监控结算。
- 名额：运行中的 job 占用创建它的令牌的并发 exec 名额，直到进程确认结束或 job 被判定为 `lost`，之后才释放。名额满时启动返回 `token concurrent exec limit reached`。
- 节点离线：远端 job 标记为 `lost`（`reason=node_offline`），名额释放。进程可能仍在 agent 上运行，直到 agent 的会话到期。Hub 重启：运行中的 job 标记为 `lost`（`reason=hub_restart`），远端进程同样可能继续运行到会话到期。
- 归属：job 只属于创建它的令牌（按 `TokenID`）。其他令牌查询或取消时，与不存在的 job 返回同一个错误 `job not found`。
- 保留：job 结束后 24 小时内，状态和输出可用；超过 24 小时，状态显示 `expired`，输出被清空，溢出文件由后台清扫删除。job 记录本身保留到结束后 7 天。
- 审计：启动与取消各记一条（`exec_start`、`exec_cancel`）；job 结束时补记一条 `exec` 审计，即使客户端已断开。

为什么同步 exec 无法取消、远端异步 job 如何取消：agent 为 `exec` 请求使用 `context.Background()` 加自身超时，协议里也没有取消 exec 的消息，所以 Hub 无法结束一个正在运行的同步 exec。agent 对每个请求各起一个 goroutine，并没有对节点加锁。另一方面，Hub 进程内的节点操作锁（`cluster` 的 `OpMu`）会串行化同一节点上的高风险动作（`exec`、`start_process`、`force_terminate` 等）：同一节点上一个同步 exec 运行期间，异步 job 的取消会排队等到它结束。远端异步 job 的 `start_process` 和 `force_terminate` 只短暂持有该锁，因此不会因为其他 job 而阻塞。

同步 exec 超时时，返回 `timed_out=true`，审计结果记为 `timeout`。

MCP `tools/list` 变更：`exec_start` 的描述更新为覆盖远端节点；tools/list 响应为 6455 字节（此前 6427 字节），sha256 `50d4dafbdf0001b31207b150235180858b6e7777437e0f7684dbf448a5cd6342`（此前 `221832ceb72739dbf1b39fafdab3fb901519534121d09b51b5588ec61c0435a2`）。
