# B3 修复记录：MCP Streamable HTTP 传输

- **发现日期**：2026-10-02
- **发现人**：审核专家 B（生产问题分析）
- **状态**：代码已实现，待 B 系列最终压力验收（B7 后由用户实测）

---

## 现象

Android 客户端使用 MCP Kotlin SDK 的 `StreamableHttpClientTransport` 连接 vps-commander，
发送第一条消息（`initialize`）时失败：

```
io.modelcontextprotocol.kotlin.sdk.types.McpException: Error while sending message: Streamable HTTP error:
Caused by: io.modelcontextprotocol.kotlin.sdk.client.StreamableHttpError: Streamable HTTP error:
    at ...StreamableHttpClientTransport.performSend(...)
```

## 根因

传输协议代际不匹配：

| | 服务端 (vps-commander) | 客户端 (Kotlin SDK) |
|---|---|---|
| 协议 | SSE 传输（MCP 2024-11-05） | Streamable HTTP（MCP 2025-03-26） |
| 端点 | `GET /mcp/sse` + `POST /mcp/message` | `POST /mcp`（单端点） |
| 行为 | `HandleSSE` 非 GET 直接 405（`internal/mcp/mcp.go:120`） | POST 收到非 2xx 即抛 `StreamableHttpError` |

修复前服务端没有 `POST /mcp` 路由。B3 已新增该端点，采用 JSON 请求/响应并兼容无状态请求；初始化可返回可选 `Mcp-Session-Id`，后续会校验会话归属。旧 SSE 端点继续保留。仍需通过 Kotlin SDK 实测确认完整握手与工具调用。

## 验证

```bash
curl -i -X POST https://vpstool.kory.kdns.fr/mcp/sse \
  -H "Content-Type: application/json" -d '{}'
# 预期：405 method not allowed（坐实协议不匹配）
```

## B3 实现结果

- 新增受认证中间件保护的 `POST /mcp`，接受 JSON-RPC `application/json` 请求并以 JSON 响应。
- 支持 MCP `2025-03-26` initialize 协商、`Mcp-Session-Id` 会话绑定和 `Mcp-Protocol-Version` 响应头；会话按令牌归属校验，并在 30 分钟无活动后判为过期。
- 保留 `GET /mcp/sse` + `POST /mcp/message` 作为 legacy SSE 兼容传输。
- 旧 SSE 会话空闲 30 分钟自动关闭；发送队列满时最多等待 5 秒，超时关闭会话并返回 HTTP 503，不再静默丢弃。
- 单元测试覆盖 Streamable HTTP initialize、notification、Content-Type 校验、SSE 空闲期限和队列满超时。
- B 系列大规模实测仍待 B7；本记录不代表生产已部署或整体验收通过。

## 备注

- 与 2026-10-02 bug hunt 的 4 个 bug 无关，是独立的协议兼容性问题。
- 未经用户授权不修改生产端点或重启服务。
