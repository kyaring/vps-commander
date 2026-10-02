# 已知问题：MCP Streamable HTTP 传输不受支持（待排期）

- **发现日期**：2026-10-02
- **发现人**：审核专家 B（生产问题分析）
- **状态**：待排期，未修复

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

服务端没有 `POST /mcp` 路由。客户端 POST 打到 `/mcp/sse` 得 405，打到 `/mcp` 得 404，
两种情况都会触发该异常。SDK 的错误信息为空（未携带 HTTP 状态码），需抓包/curl 确认。

## 验证

```bash
curl -i -X POST https://vpstool.kory.kdns.fr/mcp/sse \
  -H "Content-Type: application/json" -d '{}'
# 预期：405 method not allowed（坐实协议不匹配）
```

## 修复选项（待决策）

1. **客户端改（快）**：Android 端改用 `SseClientTransport`，URL 指向 `/mcp/sse`，与服务端现状匹配。
2. **服务端加（彻底）**：实现 Streamable HTTP（`POST /mcp`，MCP 2025-03-26），属新功能开发。

## 备注

- 与 2026-10-02 bug hunt 的 4 个 bug 无关，是独立的协议兼容性问题。
- 决策前不要动生产端点。
