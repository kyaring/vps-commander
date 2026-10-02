# VPS-Commander 研发设计规范与协议说明

## 一、技术选型与工程原则

- **开发语言**：Go。
- **存储**：SQLite。
- **HTTP**：标准库 `net/http`。
- **节点通信**：WebSocket。
- **Web 前端**：Go `embed`。

工程原则：
1. 认证与授权分离。
2. Tool Risk 静态注册。
3. Device Policy 运行时解析。
4. 默认 fail-closed。
5. Session 必须有 TTL 与资源上限。
6. 生产二进制必须可追溯。
7. 发布必须可回滚。

---

## 二、项目工程结构

```text
vps-commander/
├── cmd/
│   ├── hub/
│   ├── agent/
│   └── mcp-stdio/
├── internal/
│   ├── api/
│   ├── auth/
│   ├── cluster/
│   ├── executor/
│   ├── mcp/
│   ├── model/
│   ├── notify/
│   ├── security/
│   ├── storage/
│   └── web/
├── docs/
├── deploy/
└── openapi.json
```

---

## 三、Agent WebSocket 协议

Agent 主动连接：`/agent/ws`。

握手包含：
- Protocol Version。
- AgentVersion。
- Capabilities。
- Per-device Credential。

Hub 根据 Credential 建立 Canonical Device Identity。
客户端请求中的 `device/name` 只能用于路由，不能作为授权身份来源。

---

## 四、HTTP API 规范

| 路径 | 方法 | 功能 |
| :--- | :--- | :--- |
| `/api/v1/devices` | GET | 设备列表 |
| `/api/v1/exec` | POST | Shell |
| `/api/v1/file/read` | POST | 文件读取 |
| `/api/v1/file/list` | POST | 目录列表 |
| `/api/v1/file/info` | POST | 文件信息 |
| `/api/v1/file/search` | POST | 文件搜索 |
| `/api/v1/process/session` | POST | Session |
| `/api/v1/file/write` | POST | 文件写入 |
| `/api/v1/devices/file/edit_block` | POST | 原子编辑 |
| `/api/v1/mcp/*` | GET/POST/DELETE | MCP |
| `/api/v1/security/*` | GET/POST | 安全策略 |
| `/api/v1/notifications/webhook*` | GET/POST/DELETE | Webhook |
| `/healthz` | GET | 健康检查 |
| `/openapi.json` | GET | OpenAPI |

---

## 五、MCP 协议规范

Server version：`1.2.0`。
Protocol Version：`2024-11-05`。

当前工具共 12 个：
`list_devices`、`exec_command`、`read_file`、`read_multiple_files`、`create_directory`、`move_file`、`list_processes`、`edit_block`、`write_file`、`list_agent_mcp`、`call_agent_mcp`。

远程 transport：legacy HTTP + SSE。
本地 transport：stdio。
未来 transport：Streamable HTTP。

---

## 六、认证与安全规范

### 6.1 HTTP / MCP
```http
Authorization: Bearer <API_KEY>
```

禁止：
- `?token=`
- `?key=`
- `?api_key=`

### 6.2 Agent
- 每设备独立 Credential。
- SQLite 只保存 Hash。
- 生产关闭 Shared Secret fallback。

### 6.3 Panel
Panel Session 在服务端认证。
Admin Token 只在 Panel → 内部 API bridge 中使用，不发送给浏览器。

---

## 七、并发与资源限制

| 项目 | 限制 |
| :--- | :--- |
| Exec | 32 并发 |
| Search | 8 并发 |
| Session Ring | 2MB |
| Session 总 Buffer | 8MB |
| Session Hard TTL | 30m |
| Session Idle TTL | 10m |
| WS Write | Node.WriteMu |
| Medium/High | Node.OpMu |

---

## 八、测试与发布规范

### 8.1 提交前
```bash
go test ./...
go test -race ./...
git diff --check
```

### 8.2 发布前
- clean Git tree。
- `vcs.modified=false`。
- revision 与 release commit 一致。
- 单 Agent 灰度。
- Hub restart recovery。
- MCP smoke test。
- security deny/allow matrix。

---

## 九、v2.1 研发计划

### 9.1 P0
- EffectiveRisk 完全 fail-closed。
- Credential 不进入 argv / cmdline。

### 9.2 P1
- Principal / Operator Identity。
- Session Owner 强隔离。
- 完整身份审计链。

v2.1 不改变 Low / Medium / High 语义，不恢复 Shared Secret。
