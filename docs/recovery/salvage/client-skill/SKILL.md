---
name: vps-commander
description: 通过 VPS-Commander Hub 管理分布式 Linux 节点（查设备、执行命令、读写文件、诊断）以及提交 / 查看 DevTask 开发任务。任何 AI 客户端（MCP、REST、stdio）接入 VPS-Commander 时使用。
---

# VPS-Commander 客户端 skill v1.1

适用 Hub 版本：467b500 及以后。Hub：`https://vpstool.kory.kdns.fr`。
本文件是 vps-commander 仓库 `docs/client-skill/SKILL.md` 的副本；接口、工具或规则变化时随代码一起更新。

## 1. 接入

| 客户端 | 方式 | 地址 |
|---|---|---|
| 原生 MCP（桌面 / 移动端 / SDK） | Streamable HTTP（推荐） | `https://vpstool.kory.kdns.fr/mcp` |
| 只支持旧版 SSE | SSE | `https://vpstool.kory.kdns.fr/mcp/sse` |
| 脚本、curl、GPT Actions、skill | REST | `https://vpstool.kory.kdns.fr/api/v1/*`（OpenAPI：`/openapi.json`） |
| 本机进程 | stdio | `vps-commander-mcp-stdio -hub <Hub地址>`，令牌放环境变量 `VPS_COMMANDER_API_KEY` |

```json
{
  "mcpServers": {
    "vps-commander": {
      "type": "http",
      "url": "https://vpstool.kory.kdns.fr/mcp",
      "headers": { "Authorization": "Bearer <你的命名令牌>" }
    }
  }
}
```

令牌：
- 每个客户端用自己的命名令牌（面板「接入与令牌」创建，OPC 发放），scope 和设备白名单按需最小化。不要共用令牌，不要用 legacy key。
- 令牌只放客户端配置或环境变量；不打印、不写进日志、提交、任务书或聊天。
- `401` / `403` 不重试、不换凭据绕过，如实报告给人。

Streamable HTTP 要点：地址必须是 `/mcp`（不带结尾斜杠，填 `/mcp/sse` 会 405）；initialize 返回 `Mcp-Session-Id`，后续请求带上；会话空闲 30 分钟过期，404 session 就重新 initialize。

## 2. 运维工具

先 `list_devices` 确认节点名和在线状态，每次调用显式写 `device`。

| 工具（MCP） | REST | scope | 风险 | 参数要点 |
|---|---|---|---|---|
| `list_devices` | `GET /api/v1/devices` | devices:read | 低 | 无 |
| `read_file` | `POST /api/v1/file/read` | files:read | 低 | device、path、offset、limit（默认 256 KiB，最大 1 MiB） |
| `read_multiple_files` | — | files:read | 低 | device、paths、limit |
| `diagnostic_command` | — | files:read | 低 | action：docker_ps / docker_logs / docker_inspect / systemctl_status / journalctl / ss_listen / df / free / uptime；target；lines 1–500 |
| `list_processes` | `POST /api/v1/process/session?action=list` | process | 低 | device |
| `create_directory` / `move_file` | — | files:write | 中 | device、path / source、destination |
| `edit_block` | `POST /api/v1/devices/file/edit_block` | files:write | 中 | old_text 必须唯一匹配；原子写回 |
| `write_file` | `POST /api/v1/file/write` | files:write | 中 | 创建或整体覆写 |
| `list_agent_mcp` / `call_agent_mcp` | `/api/v1/mcp/list`、`/api/v1/mcp/call` | files:read / mcp:call | 低 / 中 | device、server、tool、arguments |
| `exec_command` | `POST /api/v1/exec` | exec | 高 | device、command、workdir、timeout（秒，默认 30，最大 300；字段名是 `timeout`） |

exec 返回 `{device, exit_code, stdout, stderr, duration_ms}`。MCP 对非零退出不标 isError，必须自己看 `exit_code`。

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
7. 后台长期进程必须重定向：`nohup cmd > /var/log/x.log 2>&1 < /dev/null &`，否则调用会挂到超时（exit_code 124）并被杀。
8. 长诊断（ping、traceroute、curl）显式设 timeout。

审计 result 含义：`ok` 退出码 0；`exit_nonzero` 命令执行完但非零退出；`error` Hub / agent / 工具本身失败；`denied` 被 scope、白名单或安全等级拒绝。

## 5. 状态码
`401` 令牌无效 / 吊销；`403` scope 不足、设备不在白名单或安全等级拒绝；`404` 路径错（注意结尾斜杠）或 MCP 会话失效；`409` 节点离线；`429` 限速 / 会话数 / exec 并发超限，按 `Retry-After` 退避；`503` 内部组件不可用。

## 6. DevTask 开发任务（需 devtask:* scope）

| 工具 | scope | 用途 |
|---|---|---|
| `dev_task_dispatch` | devtask:submit | 提交任务：project、title、spec_md；可选 model、accept_cmds、allowed_paths、forbidden_paths、parent_task_id |
| `dev_task_revise` | devtask:submit | 对 needs_review / failed 任务追加 review_md（≤16 KiB），排入下一轮 |
| `dev_task_status` / `dev_task_list` | devtask:read | 查状态 / 分页列表 |
| `dev_task_diff` / `dev_task_logs` | devtask:read | 分页读 diff（默认 16 KiB，最大 64 KiB）/ 事件 |
| `dev_task_cancel` | devtask:cancel | 取消 |

要点：
- model 必须在允许列表内（当前只有 `global:deepseek-v4.1-flash`）；accept_cmds 必须在项目允许命令内并原样照写。
- allowed_paths / forbidden_paths 是真实路径前缀，不支持通配；allowed_paths 同时限制读取。
- 分支和基线 SHA 由 Hub 决定；提交任务只是排队，不会合并、push 或部署。确认合并（confirm）由 OPC / 审核方决定。

## 7. 参与 vps-commander 开发时
仓库根目录 `AGENTS.md` 和 `docs/process/SKILL.md` 是开发规范，开工前必读：没有任务书不改代码，只改任务书允许的路径，不 push / tag / 部署，交付前跑 `sh docs/process/check-task.sh <任务编号>`。完成通知、审核通知的发送方式见 AGENTS.md。
