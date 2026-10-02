# VPS-Commander MCP（Model Context Protocol）接入指南

本文介绍 VPS-Commander 当前 MCP 能力、客户端接入方式、工具权限以及后续 transport 演进方向。

---

## 一、MCP 总体能力

VPS-Commander 当前提供两种 MCP 接入模式：

- **远程 HTTP SSE 模式**：适用于远程 MCP Client。
- **本地 stdio 模式**：适用于 Claude Desktop、Cursor、VS Code 等本地客户端。

当前 MCP Server：
- JSON-RPC 2.0。
- server version：`1.2.0`。
- initialize protocolVersion：`2024-11-05`。

当前远程 SSE 属于兼容 transport；后续新能力优先考虑 Streamable HTTP。

---

## 二、当前支持的 11 大 MCP Tools

| Tool 名称 | 风险等级 | 参数/功能说明 |
| :--- | :--- | :--- |
| `list_devices` | Low | 列出设备、状态及资源画像 |
| `exec_command` | High | 指定节点执行 Shell |
| `read_file` | Low | 分页读取文件 |
| `read_multiple_files` | Low | 批量读取多个文件 |
| `create_directory` | Medium | 创建目录 |
| `move_file` | Medium | 移动/重命名文件 |
| `list_processes` | Low | 查看进程 |
| `edit_block` | Medium | 唯一匹配 + 原子写回 |
| `write_file` | Medium | 创建或覆写文件 |
| `list_agent_mcp` | Low | 查询 Agent MCP 服务 |
| `call_agent_mcp` | Medium | 调用 Agent MCP 工具 |

风险等级不是客户端提示，而是 Hub 强制执行的授权门禁。

---

## 三、统一安全链路

```text
MCP Client
    │
    ▼
Bearer Authentication
    │
    ▼
MCP JSON-RPC
    │
    ▼
Tool / Action Mapping
    │
    ▼
Authenticated Device Identity
    │
    ▼
Security Snapshot + Tool Risk
    │
    ├── DENY
    │
    ▼
Local Executor / Agent WSS
    │
    ▼
Audit
```

`call_agent_mcp` 同样必须进入 MCP risk gate，不能因为目标是 Agent MCP 就绕过 Hub 授权。

---

## 四、远程 SSE 模式

### 4.1 SSE 建立

```text
GET https://your-domain.example/mcp/sse
Authorization: Bearer <YOUR_API_KEY>
```

服务器建立 MCP Session 后提供消息端点：

```text
POST /mcp/message?sessionId=<sessionId>
Authorization: Bearer <YOUR_API_KEY>
```

### 4.2 注意事项
- URL 中禁止放 Token。
- `?token=`、`?key=`、`?api_key=` 不作为认证方式。
- SSE 当前作为 legacy compatibility transport。

---

## 五、本地 stdio 模式

适用于 Claude Desktop、Cursor、VS Code 等本地客户端。

```json
{
  "mcpServers": {
    "vps-commander": {
      "command": "/opt/vps-commander/vps-commander-mcp-stdio",
      "args": [
        "-hub", "https://your-domain.example",
        "-key", "YOUR_API_KEY"
      ]
    }
  }
}
```

API Key 不应提交到 Git 仓库。

---

## 六、主要工具参数

### 6.1 exec_command
- `device`：目标节点，可选。
- `command`：必填。
- `workdir`：可选。
- `timeout`：默认 30 秒，最大 300 秒。

### 6.2 read_file
- `device`、`path` 必填。
- `offset` 可选。
- `limit` 默认 256 KiB，最大 1 MiB。

### 6.3 read_multiple_files
- `device` 可选。
- `paths` 必填数组。
- 当前 Hub 最多接受 32 个路径。

### 6.4 create_directory
- `device`、`path`。

### 6.5 move_file
- `device`、`source`、`destination`。

### 6.6 list_processes
- `device` 可选。

### 6.7 edit_block
- `device`、`path`、`old_text`、`new_text`。
- `old_text` 必须唯一匹配。
- 使用 generation check + atomic write。

### 6.8 write_file
- `device`、`path`、`content`。

### 6.9 list_agent_mcp
- `device` 必填。

### 6.10 call_agent_mcp
- `device`、`server`、`tool` 必填。
- `arguments` 可选 JSON 对象。

---

## 七、Agent MCP

Hub 可以发现远程 Agent 暴露的 MCP Service。

```text
MCP Client
   ↓
Hub Authentication
   ↓
Security Gate
   ↓
Authenticated Agent
   ↓
Agent MCP Service
```

客户端不直接连接 Agent MCP。

---

## 八、后续演进

1. Streamable HTTP。
2. 保留 SSE fallback。
3. Capability-based Tool Advertisement。
4. Principal Identity 纳入 MCP Audit Context。
5. 长任务引入显式 Operation / Session Handle。
