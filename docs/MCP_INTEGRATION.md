# VPS-Commander MCP 接入与能力

## 1. 当前状态
MCP JSON-RPC 2.0；serverInfo 1.2.0；initialize protocolVersion 2024-11-05。
远程 HTTP 当前是 legacy HTTP+SSE；本地是 stdio。Streamable HTTP 是后续演进方向。

## 2. 11 个工具
| Tool | Risk | 作用 |
|---|---|---|
| list_devices | Low | 设备列表 |
| exec_command | High | Shell |
| read_file | Low | 文件读取 |
| read_multiple_files | Low | 批量读取 |
| create_directory | Medium | 创建目录 |
| move_file | Medium | 移动/重命名 |
| list_processes | Low | 进程观察 |
| edit_block | Medium | 原子文本修改 |
| write_file | Medium | 文件写入 |
| list_agent_mcp | Low | Agent MCP 列表 |
| call_agent_mcp | Medium | 调度 Agent MCP |

## 3. 远程接入
GET /mcp/sse → sessionId → POST /mcp/message?sessionId=<id>。
请求头：Authorization: Bearer <API_KEY>。
HTTP+SSE 仅作为兼容层，不作为未来新能力的主要 transport。

## 4. stdio
启动 vps-commander-mcp-stdio，参数 -hub <Hub URL> -key <API_KEY>。
API Key 不提交到仓库。

## 5. 参数
exec_command：device、command、workdir、timeout。
read_file：device、path、offset、limit；默认 256 KiB，最大 1 MiB。
read_multiple_files：device、paths、limit；paths 最多 32 个。
create_directory：device、path。
move_file：device、source、destination。
list_processes：device。
edit_block：device、path、old_text、new_text；old_text 必须唯一匹配。
write_file：device、path、content。
list_agent_mcp：device。
call_agent_mcp：device、server、tool、arguments。

## 6. 安全链路
MCP Client → Bearer auth → JSON-RPC → tool mapping → authenticated device → Policy Snapshot → Tool Registry → allow/deny → Local/Agent → audit。
未知设备不得因为 GlobalRisk 而获得 High。

## 7. Agent MCP
Hub 发现远程 Agent MCP service；客户端不直连 Agent。
Hub 负责 device authentication、risk gate、routing、audit。

## 8. 下一步
Streamable HTTP、SSE fallback、capability-based advertisement、Principal audit context、显式 operation/session handle。
