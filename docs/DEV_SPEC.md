# VPS-Commander 开发与协议规范

## 1. 技术基线
Go + SQLite + net/http + WebSocket。
组件：cmd/hub、cmd/agent、cmd/mcp-stdio；internal/api、auth、cluster、executor、mcp、security、storage、web、notify、model。

## 2. Agent Protocol
Agent 主动连接 /agent/ws。
握手包含 protocol version、AgentVersion、capabilities、per-device credential。
Hub 依据 credential 建立 canonical device identity；device/name 只能作为路由参数。

## 3. HTTP
/api/v1/devices、/api/v1/exec、/api/v1/file/read、/api/v1/file/list、/api/v1/file/info、/api/v1/file/search、/api/v1/process/session、/api/v1/file/write、/api/v1/devices/file/edit_block、/api/v1/mcp/*、/api/v1/security/*、/api/v1/notifications/webhook*、/healthz、/openapi.json。
Panel 使用 /panel/*。

## 4. MCP
serverInfo version：1.2.0；protocolVersion：2024-11-05。
11 tools：list_devices、exec_command、read_file、read_multiple_files、create_directory、move_file、list_processes、edit_block、write_file、list_agent_mcp、call_agent_mcp。
远程 HTTP 当前为 legacy SSE：GET /mcp/sse + POST /mcp/message?sessionId=...；本地为 stdio。

## 5. MCP 授权
exec → High；read → Low；write/edit/create/move → Medium；call_agent_mcp → mcp；list_agent_mcp → read。
MCP adapter 与 direct Agent MCP 使用同一 Hub security model。

## 6. 认证
HTTP/MCP：Authorization: Bearer API_KEY。
禁止 ?token、?key、?api_key。
Agent：per-device credential；SQLite 只保存 hash；生产不依赖 Cluster Secret fallback。
Panel：浏览器只持有 Panel Session；Admin Token 在服务端 bridge。

## 7. 资源
ExecLimiter 32；SearchLimiter 8；Node.WriteMu；Node.OpMu。
Session：ring 2MB；total 8MB；hard TTL 30m；idle TTL 10m；cleanup。

## 8. 测试
go test ./...；go test -race ./...；git diff --check。
生产额外要求 clean tree、vcs.modified=false、revision 与 release commit 一致、单 Agent 灰度、Hub recovery、MCP smoke、security matrix。

## 9. v2.1
P0：EffectiveRisk fail-closed；Agent token 不进入 argv/cmdline。
P1：Principal/Operator、Session owner 强隔离、完整身份审计链。
v2.1 不改变 Low/Medium/High 语义，不恢复 Shared Secret。
