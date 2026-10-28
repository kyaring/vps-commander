# 客户端接入（MCP / REST / stdio）

> **版本说明（2026-10-28）**：工具与异步 exec 行为以本地 `main` 为准；生产部署可能尚未包含 Unreleased 项（见 `CHANGELOG.md`）。

适用版本：v1.1.30。示例域名为 `hub.example.com`，`<TOKEN>` 为命名令牌占位符。

先在面板“接入与令牌”中创建命名令牌（见 [security.md](security.md)）。面板的“AI 接入向导”可以直接生成下面各种配置并测试连通。

---

## 1. 选择传输方式

| 客户端类型 | 推荐 | 地址 |
| :--- | :--- | :--- |
| 原生 MCP 客户端（桌面 / 移动端 / Kotlin、TypeScript、Python SDK） | Streamable HTTP | `https://hub.example.com/mcp` |
| 只支持旧版 SSE 的客户端 | SSE | `https://hub.example.com/mcp/sse` |
| Skills、脚本、curl、GPT Actions | REST | `https://hub.example.com/api/v1/*` |
| 本机桌面客户端，想走本地进程 | stdio | `vps-commander-mcp-stdio` |

三种方式共用同一令牌、scope、设备白名单、限流和审计，Hub 不限制令牌使用哪种方式。REST 无会话、一次调用一个请求，最适合脚本类客户端。

## 2. MCP Streamable HTTP（推荐）

```http
POST /mcp
Authorization: Bearer <TOKEN>
Content-Type: application/json
Accept: application/json, text/event-stream

{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"example","version":"1.0"}}}
```

- 支持 `initialize`、`tools/list`、`tools/call`；响应为 `application/json`。
- 客户端请求 `2025-03-26` 时按该版本协商，其他版本按 `2024-11-05` 回应；响应带 `Mcp-Protocol-Version`。
- initialize 返回 `Mcp-Session-Id`，后续请求携带该头；会话绑定创建它的令牌，空闲 30 分钟过期，`DELETE /mcp` 主动结束。
- 通知（无 `id`）返回 `202`，无正文。请求体上限 1 MiB，非 `application/json` 返回 `415`。
- `GET /mcp` 返回 `405`（不提供服务端推送流），这是规范允许的行为，客户端会自动忽略。

通用 JSON 配置：

```json
{
  "mcpServers": {
    "vps-commander": {
      "type": "http",
      "url": "https://hub.example.com/mcp",
      "headers": { "Authorization": "Bearer <TOKEN>" }
    }
  }
}
```

### 常见错误

| 报错 | 原因 |
| :--- | :--- |
| `Streamable HTTP error: method not allowed`（405） | 地址填成了 `/mcp/sse`。Streamable HTTP 必须用 `/mcp`。 |
| 404 | 地址结尾多了 `/`（`/mcp/`）。 |
| 401 | 令牌错误、已吊销，或 `Authorization` 头缺失 / 格式不对。 |
| 403 `scope denied` | 令牌缺少该工具所需 scope。 |
| 429 | 超过令牌限速、会话数或 exec 并发，按 `Retry-After` 重试。 |
| 会话失效 / 404 session | 会话过期或 Hub 重启，重新 `initialize`。 |

## 3. MCP SSE（旧版兼容）

```text
GET https://hub.example.com/mcp/sse
Authorization: Bearer <TOKEN>
```

连接后服务端推送 `endpoint` 事件，给出 `POST /mcp/message?sessionId=<id>`，之后的 JSON-RPC 请求都发到这个地址，响应经 SSE 流返回。

- 协议版本 `2024-11-05`；会话空闲 30 分钟关闭。
- 发送队列满时最多等待 5 秒，超时关闭会话并返回 `503`。
- 长连接容易被网络中断；出现 404 时重新建立 SSE 会话。
- 反代需对 `/mcp/sse` 关闭缓冲。

```json
{
  "mcpServers": {
    "vps-commander-sse": {
      "type": "sse",
      "url": "https://hub.example.com/mcp/sse",
      "headers": { "Authorization": "Bearer <TOKEN>" }
    }
  }
}
```

## 4. REST

```bash
curl -s https://hub.example.com/api/v1/exec \
  -H "Authorization: Bearer $VPS_COMMANDER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"device":"node-01","command":"uptime","timeout":30}'
```

返回 `{device, exit_code, stdout, stderr, duration_ms}`。完整接口见 [api.md](api.md)，OpenAPI 描述在 `GET /openapi.json`（可直接导入 GPT Actions，认证选 Bearer）。

脚本客户端建议：显式检查 HTTP 状态码；`401`、`403` 不要重试或换凭据绕过；`429` 按 `Retry-After` 退避。

## 5. stdio

```json
{
  "mcpServers": {
    "vps-commander": {
      "command": "/opt/vps-commander/vps-commander-mcp-stdio",
      "args": ["-hub", "https://hub.example.com"],
      "env": { "VPS_COMMANDER_API_KEY": "<TOKEN>" }
    }
  }
}
```

`-key` 参数也可以传令牌，但会出现在进程列表中，推荐用环境变量 `VPS_COMMANDER_API_KEY`（这里填命名令牌即可）。不要把令牌提交到 Git。

## 6. 工具与参数

调用前先用 `list_devices` 确认节点名称与在线状态，并在每次调用中显式指定 `device`。

| 工具 | scope | 风险 | 参数 |
| :--- | :--- | :--- | :--- |
| `list_devices` | `devices:read` | Low | 无 |
| `read_file` | `files:read` | Low | `device`、`path`；`offset`；`limit` 默认 256 KiB，最大 1 MiB |
| `read_multiple_files` | `files:read` | Low | `device`、`paths`（数组）、`limit`（单文件）；返回项含 `truncated`（截断时为 true） |
| `diagnostic_command` | `files:read` | Low | `device`、`action`（`docker_ps`、`docker_logs`、`docker_inspect`、`systemctl_status`、`journalctl`、`ss_listen`、`df`、`free`、`uptime`）、`target`、`lines`（1–500） |
| `list_agent_mcp` | `files:read` | Low | `device` |
| `list_processes` | `process` | Low | `device` |
| `create_directory` | `files:write` | Medium | `device`、`path` |
| `move_file` | `files:write` | Medium | `device`、`source`、`destination` |
| `edit_block` | `files:write` | Medium | `device`、`path`、`old_text`（须唯一匹配）、`new_text`；原子写回；符号链接写入真实目标（链接保持不变）；悬空链接报错；硬链接返回 `hardlinked file refused` |
| `write_file` | `files:write` | Medium | `device`、`path`、`content`；创建或整体覆写；原子写入（临时文件 + rename），保留已有文件的权限与属主，新建文件为 0644；符号链接写入真实目标；硬链接与非普通文件被拒绝 |
| `call_agent_mcp` | `mcp:call` | Medium | `device`、`server`、`tool`、`arguments`（对象） |
| `exec_command` | `exec` | High | `device`、`command`、`workdir`、`timeout`（秒，最大 300） |

`diagnostic_command` 只执行白名单内的只读诊断，不接受任意 Shell，因此 Medium 等级的节点也能用；`exec_command` 始终需要 High。

### 使用建议

- 修改配置优先用 `edit_block`，而不是 `write_file` 整体覆写。
- 需要长期运行的后台命令请自行重定向输出：`nohup cmd > /var/log/cmd.log 2>&1 < /dev/null &`。一次性 exec 中未重定向的后台子进程不会让调用一直等待：命令本身正常退出后，调用最多再等 2 秒即返回 shell 的退出码；只有超时才会连同进程组一起被 SIGKILL（返回 `exit_code=124`）。
- 重启服务、删除文件、改防火墙等高风险操作，客户端应先向人确认。

## 7. Agent MCP

Hub 可以把 MCP 服务挂载到指定 Agent 上（面板“接入与令牌”→ MCP 卡片，需 `admin`），客户端通过 `list_agent_mcp` / `call_agent_mcp` 经 Hub 调用，不直接连接 Agent。`call_agent_mcp` 同样经过 scope、白名单和安全等级检查。

## 6. 任务回报（必须）

交回任何开发、测试、运维、巡检或审核任务时，都必须推送 `task-reported`：完成用 `done`，受阻用 `blocked` 并在摘要写明原因及需要谁处理，失败用 `failed`。通知首行以 `【工号】` 开头。

```sh
scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status done --summary '完成实现和全部验收测试。' --employee SWS-E002-gpt --project vps-commander --ref commit-abc123
scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status blocked --summary '受阻：check-task 发现允许范围外文件；需要 Hark 修正任务书后继续。' --employee SWS-E002-gpt --project vps-commander --ref T-20261014-10
```
