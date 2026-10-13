# REST API

适用版本：v1.1.30。机器可读描述：`GET /openapi.json`（OpenAPI 3.1，可导入 GPT Actions）。

所有 `/api/v1/*` 与 `/metrics` 需要 `Authorization: Bearer <TOKEN>`；POST 请求使用 `Content-Type: application/json`。`device` 省略时默认 Hub 本机（别名 `local`），建议始终显式指定。

---

## 1. 运维接口

| 接口 | 方法 | scope | 请求体 / 说明 |
| :--- | :--- | :--- | :--- |
| `/api/v1/devices` | GET | `devices:read` | 设备列表、状态、架构、国家、资源画像 |
| `/api/v1/exec` | POST | `exec` | `{device, command, workdir?, timeout?}`，`timeout` 秒，默认 30，最大 300；返回 `{device, exit_code, stdout, stderr, duration_ms}` |
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
