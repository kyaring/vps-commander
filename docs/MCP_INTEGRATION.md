# VPS-Commander MCP (Model Context Protocol) 接入指南

## 一、概述
为了应对 OpenAI Custom GPTs 退役及新一代 Plugins/Skills 规范转型，VPS-Commander 现已全面支持标准 **MCP (Model Context Protocol)** 协议。

- **零侵入**：原有 REST API、OpenAPI 3.1、Web 面板及 WebSocket 多节点连接池 100% 保持不变。
- **纯 Go 原生**：零外部庞大框架依赖，常驻内存基线依然保持在极低水平。
- **双模支持**：同时支持 **远程 SSE 模式** 与 **本地 stdio 模式**。

---

## 二、支持的 4 大 MCP Tools

| Tool 名称 | 参数说明 | 描述 |
| :--- | :--- | :--- |
| `list_devices` | 无 | 列出当前所有已注册的受控 VPS 节点（如本机 `wjyhk` 及远程 `m4-live-agent` 等）及在线状态 |
| `exec_command` | `device` (可选), `command` (必填), `workdir` (可选), `timeout` (可选, 默认30) | 在指定受控节点执行任意 Shell 命令并返回退出码与输出 |
| `read_file` | `device` (可选), `path` (必填), `offset` (可选), `limit` (可选) | 读取指定受控节点的文件内容（支持安全分页读取） |
| `write_file` | `device` (可选), `path` (必填), `content` (必填) | 在指定受控节点创建或覆写文件 |

---

## 三、接入方式

### 方式 1：远程 SSE 模式（适用于远程 Agent、OpenAI 新生态端点）

Hub 原生提供 MCP SSE 端点（需带 Bearer Token 鉴权）：
- **SSE 建立端点**：`GET https://vpstool.kory.kdns.fr/mcp/sse`
- **消息交互端点**：`POST https://vpstool.kory.kdns.fr/mcp/message?sessionId=<sessionId>`
- **请求头**：`Authorization: Bearer <YOUR_API_KEY>`

### 方式 2：本地 stdio 模式（适用于 Claude Desktop、Cursor、VS Code）

直接使用编译好的极轻量 Go 二进制 `vps-commander-mcp-stdio-linux-amd64`。

#### 客户端配置示例 (`claude_desktop_config.json` 或 Cursor MCP 设置)：

```json
{
  "mcpServers": {
    "vps-commander": {
      "command": "/root/.openclaw/workspace/vps-commander/bin/vps-commander-mcp-stdio-linux-amd64",
      "args": [
        "-hub", "https://vpstool.kory.kdns.fr",
        "-key", "YOUR_API_KEY"
      ]
    }
  }
}
```
