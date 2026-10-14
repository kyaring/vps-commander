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


| 接口 | 方法 | scope | 请求体 / 说明 |
| :--- | :--- | :--- | :--- |
| `/api/v1/devices` | GET | `devices:read` | 设备列表、状态、架构、国家、资源画像 |
| `/api/v1/session/stats` | GET | 任意有效令牌 | 当前调用方会话的工具调用数、响应字节、粗估 token 数、回传字节最多的 5 个工具与预算状态；不需要新增 scope |
| `/api/v1/exec` | POST | `exec` | `{device, command, workdir?, timeout?, output_mode?, max_bytes?, dedup_window_seconds?}`；`timeout` 秒默认 30、最大 300；输出参数见下文 |
| `/api/v1/file/read` | POST | `files:read` | `{device, path, offset?, limit?}`，`limit` 默认 256 KiB、最大 1 MiB；返回 `content, bytes, total_size, has_more, eof` |
| `/api/v1/file/list` | POST | `files:read` | `{device, path, recursive?, max_entries?}` |
| `/api/v1/file/info` | POST | `files:read` | `{device, path}` |
| `/api/v1/file/search` | POST | `files:read` | `{device, root, query, max_results?}` |
| `/api/v1/file/write` | POST | `files:write` | `{device, path, content}`，创建或整体覆写 |
| `/api/v1/devices/file/edit_block` | POST | `files:write` | `{device, path, old_text, new_text}`，`old_text` 必须唯一匹配 |
| `/api/v1/process/session?action=start\|read\|input\|stop\|list` | POST | `process` | `{device, session_id?, command?, workdir?, data?}` |
| `/api/v1/mcp/list` | GET | `mcp:call` | `?device=`，列出该 Agent 挂载的 MCP 服务 |
| `/api/v1/mcp/call` | POST | `mcp:call` | `{device, server, tool, arguments}` |
| `/api/v1/mcp/test` | POST | `mcp:call` | `{device, service}`，测试一个 MCP 服务配置 |

执行类与文件修改类接口还受节点安全等级约束（见 [security.md](security.md)）。


## 会话预算统计与超阈值提醒

`GET /api/v1/session/stats` 接受任意有效令牌，不要求额外 scope；只返回该令牌当前会话的数据。MCP Streamable HTTP 会话使用 `Mcp-Session-Id`，并且会校验会话归属；普通 HTTP API 请求按令牌与当前连接（`RemoteAddr`）归并。统计只保存在 Hub 内存中，Hub 重启后清零，不写数据库。响应不超过 1.5 KiB，且 `session_id` 是会话标识，不包含令牌或凭据。

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

## 2. 管理接口（需要 `admin`）

| 接口 | 方法 | 说明 |
| :--- | :--- | :--- |
| `/api/v1/tokens` | GET / POST | 列出 / 创建命名令牌（创建时返回完整密钥，仅此一次） |
| `/api/v1/tokens/{id}` | PATCH | 修改名称、备注、scope、设备白名单、限速、会话数 |
| `/api/v1/tokens/{id}/rotate` | POST | 轮换密钥 |
| `/api/v1/tokens/{id}/revoke` | POST | 吊销 |
| `/api/v1/audits` | GET | 审计查询：`actor`、`device`、`action`、`result`、`transport`、`from`、`to`，游标分页，每页最多 500 条 |
| `/api/v1/audits/export.csv` | GET | 按同样条件导出 CSV（有行数上限，超出时末尾带截断标记） |
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

## 3. 无需认证的端点

| 接口 | 说明 |
| :--- | :--- |
| `/healthz` | 健康检查 |
| `/openapi.json` | OpenAPI 描述 |
| `/probe/summary` | 公共探针摘要（2 秒缓存，支持 `ETag`） |
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
