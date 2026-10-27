---
name: vps-commander
description: 通过 VPS-Commander Hub 管理分布式 Linux 节点（查设备、执行命令、读写文件、诊断）。任何 AI 客户端（MCP、REST、stdio）接入 VPS-Commander 时使用。
---

# VPS-Commander 客户端 skill v1.2-recovery

适用 Hub 版本：891904a（v1.1.35）及以后。Hub：`https://vcs.king.nyc.mn`。
本文件是 vps-commander 仓库 `docs/client-skill/SKILL.md` 的副本；接口、工具或规则变化时随代码一起更新。

## 1. 接入

| 客户端 | 方式 | 地址 |
|---|---|---|
| 原生 MCP（桌面 / 移动端 / SDK） | Streamable HTTP（推荐） | `https://vcs.king.nyc.mn/mcp` |
| 只支持旧版 SSE | SSE（若服务端启用） | `https://vcs.king.nyc.mn/mcp/sse` |
| 脚本、curl、GPT Actions、skill | REST | `https://vcs.king.nyc.mn/api/v1/*`（OpenAPI：`/openapi.json`） |
| 本机进程 | stdio | `vps-commander-mcp-stdio -hub <Hub地址>`，令牌放环境变量 `VPS_COMMANDER_API_KEY` |

```json
{
  "mcpServers": {
    "vps-commander": {
      "type": "http",
      "url": "https://vcs.king.nyc.mn/mcp",
      "headers": { "Authorization": "Bearer <你的命名令牌>" }
    }
  }
}
```

令牌：
- 每个客户端用自己的命名令牌（面板「接入与令牌」创建，OPC 发放），scope 和设备白名单按需最小化。不要共用令牌，不要用 legacy key。
- 令牌只放客户端配置或环境变量；不打印、不写进日志、提交、任务书或聊天。
- `401` / `403` 不重试、不换凭据绕过，如实报告给人。

Streamable HTTP 要点：地址必须是 `/mcp`（不带结尾斜杠）；initialize 返回 `Mcp-Session-Id`，后续请求带上；会话失效时重新 initialize。

## 2. 运维工具

先 `list_devices` 确认节点名和在线状态，每次调用显式写 `device`。

| 工具（MCP） | REST 路由（代码已注册） | scope / 风险 | 参数要点 |
|---|---|---|---|
| `list_devices` | `GET /api/v1/devices` | devices:read / 低 | 无 |
| `exec_command` | `POST /api/v1/exec` | exec / 高 | device、command、workdir、timeout；output_mode、max_bytes、dedup_window_seconds（两种接口字段一致） |
| `read_file` | `POST /api/v1/file/read` | files:read / 低 | device、path、offset、limit（默认 256 KiB，最大 1 MiB） |
| `read_multiple_files` | 无独立 REST 路由 | files:read / 低 | device、paths、limit |
| `create_directory` | 无独立 REST 路由 | files:write / 中 | device、path |
| `move_file` | 无独立 REST 路由 | files:write / 中 | device、source、destination |
| `list_processes` | `POST /api/v1/process/session?action=list` | process / 低 | device |
| `diagnostic_command` | 无独立 REST 路由 | files:read / 低 | action：docker_ps / docker_logs / docker_inspect / systemctl_status / journalctl / ss_listen / df / free / uptime；target；lines 1–500 |
| `edit_block` | `POST /api/v1/devices/file/edit_block` | files:write / 中 | old_text 必须唯一匹配；原子写回 |
| `write_file` | `POST /api/v1/file/write` | files:write / 中 | 创建或整体覆写 |
| `list_agent_mcp` | `GET /api/v1/mcp/list` | mcp:call / 低 | device |
| `call_agent_mcp` | `POST /api/v1/mcp/call` | mcp:call / 中 | device、server、tool、arguments |

### 会话便签：避免重复核验

当已完成只读核验、需要后续轮次复用结论时，使用 `memo_set` 把简短结论与对应 SHA 写入当前会话；后续用 `memo_get` 读取。不要把令牌、密码或其他秘密写入便签。`memo_get` 不传 `key` 只返回 `key/bytes/updated_at` 元数据，不会返回 value；要读值必须显式提供 key。便签只在当前会话内存中有效，Hub 重启会清空，且有 4 KiB 单值、32 条、32 KiB 总量限制。

例：`memo_set({"key":"service-check","value":"服务 active；NRestarts=0；sha=abc123"})`，后续 `memo_get({"key":"service-check"})`，避免重复查询同一状态。

`exec_command` 默认 `output_mode=raw`、`max_bytes=4096`，超限会保留尾部并返回 `truncated/total_bytes/total_lines/sha256`；`head` 保留头部，`count` 只返回字节数、行数和退出码，`none` 不返回正文。需要大输出时显式传 `max_bytes`（最大 1048576）；只读状态查询建议传 `dedup_window_seconds` 以减少重复正文。MCP 对非零退出不标 isError，必须自己看 `exit_code`。

输出被截断后，完整输出保存在 Hub 上，返回的 `spill_path` 即其位置。用 `read_file` 读取：`device` 填 `@hub-spill`，`path` 填 `spill_path`，按 `offset` 与 `limit` 分页；返回的 `has_more` 为 false 即已读完。`dedup_window_seconds` 默认为 0。只有在同一轮核验中重复运行同一条命令、且中间没有任何写操作时，才设置它。


## 3. 安全规则
1. 重启服务、重启机器、改防火墙、删除文件、覆写已有文件等高风险操作：先把确切命令给人看，得到明确同意再执行。
2. 改配置优先 `edit_block`，不用 `write_file` 整体覆写。
3. 只读排查优先 `diagnostic_command`，不用 exec。
4. 生产主机（Hub 所在主机）上的任何变更都要 OPC 逐步批准。
5. 只用 OPC 指定的 AI 模型，不自行切换。

## 4. exec 写法规范
1. 纯查询不制造失败：结尾是 grep 时加 `|| true` 或用 `grep -c`。
2. 补丁脚本找不到锚点必须非零退出；替换前确认匹配次数为 1。
3. 构建 / 测试链用 `set -e`；查询链不用。
4. 一次 exec 只做一件可判定的事，查询和修改分开。
5. 先 `mkdir -p` 再写文件；长文本、含引号或中文的内容先写文件或 base64。
6. 不假定工具存在（部分节点没有 rg、bc、/usr/bin/time），路径不确定先 ls。
7. 后台长期进程必须重定向：`nohup cmd > /var/log/x.log 2>&1 < /dev/null &`，否则调用会挂到超时并被杀。
8. 长诊断（ping、traceroute、curl）显式设 timeout。

审计 result 含义：`ok` 命令退出码 0，或非命令操作的 HTTP 状态 2xx；`exit_nonzero` 命令执行完但非零退出；`error` Hub / agent / 工具本身失败；`denied` 被 scope、白名单或安全等级拒绝。

## 5. 状态码
`401` 令牌无效 / 吊销；`403` scope 不足、设备不在白名单或安全等级拒绝；`404` 路径错或 MCP 会话失效；`409` 节点离线；`429` 限速 / 会话数 / exec 并发超限，按 `Retry-After` 退避；`503` 内部组件不可用。

## 6. DevTask
DevTask 未恢复，恢复后随代码补回。

## 7. 参与 vps-commander 开发时
仓库根目录 `AGENTS.md` 和 `docs/process/DEV-PROCESS.md` 是开发规范，开工前必读：没有任务书不改代码，只改任务书允许的路径，不 push / tag / 部署，交付前跑 `sh docs/process/check-task.sh <任务编号>`。完成通知、审核通知的发送方式见 AGENTS.md。

## 8. 任务回报（必须）

每次交回任务都必须推送 `task-reported`，不能只在聊天中报告：完成使用 `--status done`；受阻使用 `--status blocked`，并在摘要中写明阻塞原因和需要谁来处理；失败使用 `--status failed`。第一行会以 `【工号】` 开头。脚本读取 `VPS_COMMANDER_EMPLOYEE` / `VPS_COMMANDER_PROJECT` 作为缺省员工和项目；未提供时由 Hub 按令牌名和默认值填充。

```sh
scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status done --summary '实现完成；gofmt、vet、build、全量测试和 check-task 通过。' --employee SWS-E002-gpt --project vps-commander --ref commit-abc123
scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status blocked --summary '受阻：等待 Hark 确认 API 的 severity 授权规则；需要 Hark 处理后才能继续。' --employee SWS-E002-gpt --project vps-commander --ref T-20261014-10
```


## 长任务：用异步 exec（start + status）

预计超过 30 秒的命令用 `exec_start` 启动，本机与远端节点都可以（`device` 填节点名，缺省为本机），拿到 `job_id` 后用 `exec_status` 轮询，直到 `status` 不是 `running`：

1. `exec_start`，`command` 填需要运行的命令，`device` 缺省为本机。
2. 每隔几秒调用一次 `exec_status`（传入 `job_id`）。
3. `status=exited` 时读取 `exit_code` 与 `stdout`；完整输出在 `spill_path`，用 `read_file`（`device=@hub-spill`，`path=spill_path`）分页读取。
4. `status=killed` 时看 `reason`：`timeout` 表示超过上限（本机 60 分钟，远端 30 分钟）；`cancel` 表示已被取消；`agent_session_ttl` 表示远端 agent 先结束了会话。
5. `status=lost` 表示 Hub 重启（`hub_restart`）、节点离线（`node_offline`）或会话已不存在（`session_gone`），进程状态未知，需要重新运行。

限制：远端的输出每个流最多保留 2 MiB；每个节点最多 4 个会话，已结束的会话要等 10 分钟闲置期过后才会释放，会话满时 `exec_start` 会报错。异步 job 只能被创建它的令牌查询和取消。运行中的 job 占用该令牌的并发 exec 名额，直到进程确认结束；名额满时 `exec_start` 会报错，此时先取消或等待已有 job 结束。取消返回 `kill failed` 或 `not been confirmed` 时，job 仍在运行，可以稍后再次取消或查询。job 结束 24 小时后输出被清空，状态显示 `expired`，需要的输出要在此之前读取。
