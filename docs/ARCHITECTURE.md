# VPS-Commander（自托管私有化多节点运维管家）架构与开发设计规范

## 一、项目立项背景与目标

### 1.1 背景与痛点
- 早期方案过度依赖第三方 Relay，控制面与节点执行面边界不清。
- 单机开发网关通常存在项目目录沙箱，无法覆盖完整 Host-centric 运维。
- AI Agent 需要的不只是 Shell，而是节点发现、文件、进程、Session、MCP 和审计的一体化控制面。

### 1.2 核心目标
- **100% 私有化控制面**：Hub、Agent、SQLite 与反向代理均由用户控制。
- **Host-centric 运维**：Shell、文件、进程、Session、远程 MCP。
- **多节点统一调度**：一个 Hub 管理多个 VPS，节点状态持久化。
- **AI 原生接入**：HTTP、MCP、stdio、Web 管理面板。
- **安全闭环**：认证、风险授权、资源限制、审计、可回滚发布。

---

## 二、总体架构设计

```text
[ AI / MCP Client / Web Panel ]
              │
              ▼ HTTPS / MCP
       [ Reverse Proxy / TLS ]
              │
              ▼ 127.0.0.1:9521
    ┌──────────────────────────────────────────────┐
    │             VPS-Commander Hub               │
    │----------------------------------------------│
    │ Auth / API / MCP / Web Panel                │
    │ Policy Snapshot / Tool Registry             │
    │ Cluster Router / Session / Rate Limit       │
    │ SQLite / Audit / Webhook                    │
    └──────────────┬───────────────────┬───────────┘
                   │                   │
          Local Executor          Agent WSS
                   │                   │
                   ▼                   ▼
                Hub 本机            Remote VPS
                                       │
                               Host Executor
                               Shell / File / Process
```

### 2.1 控制通道
AI / MCP / HTTP 请求进入 Hub，由 Hub 完成认证、设备解析、风险判断和路由。

### 2.2 节点通道
Agent 主动通过 WSS 回连 Hub，负责设备注册、状态、能力协商及 RPC 请求/响应。

两条通道共享同一设备身份、策略和审计上下文，不允许出现 MCP 或 Agent MCP 绕过 Hub 授权的旁路。

---

## 三、Hub 与 Agent 通信规范

### 3.1 Agent 注册与鉴权
- Agent 连接：`wss://<hub>/agent/ws`。
- 使用每设备独立 Credential。
- Hub 从已认证 Credential 建立 Canonical Device Identity。
- 客户端自报 `device/name` 不能成为授权身份来源。
- 生产不再使用 Cluster Secret fallback。

### 3.2 协议握手
握手包含：
- Protocol Version；
- AgentVersion；
- Capabilities；
- Per-device Credential。

### 3.3 RPC
Hub 将经过授权的操作转换为 Agent RPC。
Agent 返回结果后，Hub 完成审计并将结果返回 HTTP/MCP 调用方。

---

## 四、安全与权限模型

### 4.1 认证
- HTTP / MCP：Bearer API Key。
- Web Panel：Panel Session，服务端内部 bridge Admin Token。
- Agent：per-device credential。

### 4.2 Tool Risk Registry

| 风险 | 典型操作 |
| :--- | :--- |
| Low | read_file、read_multiple_files、list_directory、file_info、search、list_processes |
| Medium | edit_block、write_file、create_directory、move_file、mcp_call |
| Low | diagnostic_command：严格白名单只读诊断（Docker、systemd、journal、网络监听、磁盘、内存、uptime） |
| High | exec_command、start_process、interact_with_process、kill_process、force_terminate |

### 4.3 Policy Snapshot
- 安全策略持久化到 SQLite。
- 每次更新生成单调递增 version。
- Hub 请求路径读取内存 Snapshot。
- 未知、未注册、凭据错误或身份无法解析的设备必须 deny。
- GlobalRisk 不得让未知设备获得 High。

v2.1 将彻底清理 `EffectiveRisk` 中仍存在的 GlobalRisk fallback。

---

## 五、Session 与执行安全

### 5.1 Session 资源限制
- Ring Buffer 默认 2MB。
- 总 Session buffer 上限 8MB。
- Hard TTL 30 分钟。
- Idle TTL 10 分钟。
- LastAccess 自动刷新。
- 过期与空闲 Session 自动清理。

### 5.2 进程生命周期
kill / terminate 使用 Session 内部进程句柄，不接受客户端任意 PID 作为授权对象。

### 5.3 文件修改
`edit_block` 使用唯一匹配、generation check、临时文件、fsync、atomic rename 和权限保留，失败时不破坏原文件。

---

## 六、MCP 架构

当前 MCP 共 12 个工具：
1. `list_devices`
2. `exec_command`
3. `read_file`
4. `read_multiple_files`
5. `create_directory`
6. `move_file`
7. `list_processes`
8. `diagnostic_command`：严格白名单的只读系统诊断；

8. `edit_block`
9. `write_file`
10. `list_agent_mcp`
11. `call_agent_mcp`

`call_agent_mcp` 必须经过 MCP risk gate，不允许成为远程 Agent 的权限旁路。

当前远程 transport 为 legacy HTTP + SSE，本地客户端为 stdio；Streamable HTTP 属于后续演进。

---

## 七、项目工程结构与技术选型

- **语言**：Go。
- **存储**：SQLite。
- **HTTP**：标准库。
- **Web**：Go embed。
- **节点通信**：WebSocket。

```text
cmd/
├── hub/
├── agent/
└── mcp-stdio/
internal/
├── api/
├── auth/
├── cluster/
├── executor/
├── mcp/
├── model/
├── notify/
├── security/
├── storage/
└── web/
```

---

## 八、生产部署边界

- Hub：`127.0.0.1:9521`。
- 公网由 Reverse Proxy 终止 TLS。
- Agent 仅出站连接 Hub。
- 数据库：`/opt/vps-commander/data/commander.db`。
- systemd：`vps-commander-hub.service` / `vps-commander-agent.service`。

升级必须执行：
1. backup；
2. clean build；
3. 单节点灰度；
4. Hub restart recovery；
5. rolling Agent；
6. MCP/security/audit 验收。

---

## 九、v2.1 演进方向

### 9.1 P0
- EffectiveRisk 完全 fail-closed。
- Agent Credential 不进入 argv / cmdline。

### 9.2 P1
- Principal / Operator Identity。
- Session Owner 强隔离。
- principal → credential → session → device → operation 完整审计链。
