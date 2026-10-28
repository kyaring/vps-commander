# 架构与协议

> **2026-10-28**：`cmd/devtask-runner` 与 `internal/devtask` 在 DevTask feature 分支上开发，**尚未合入 main**。通知子系统已不止 Webhook（含通道/outbox/Telegram 等，见 CHANGELOG v1.1.35）。

适用版本：v1.1.30。

## 1. 定位

VPS-Commander 是自托管的多节点 Linux 运维控制面：AI 客户端、脚本和管理员通过一个自己掌控的 Hub，对多台 VPS 执行 Shell、文件、进程和 Agent MCP 操作，所有请求统一认证、授权、限流和审计，不依赖第三方中继。

## 2. 组件

| 组件 | 说明 |
| :--- | :--- |
| `cmd/hub` | 控制面：REST、MCP（Streamable HTTP / SSE）、Web 面板、Agent 路由、策略、审计、通知、SQLite |
| `cmd/agent` | 节点执行端：主动 WSS 回连 Hub，执行 Shell / 文件 / 进程 / Agent MCP，上报资源画像 |
| `cmd/mcp-stdio` | 本地 stdio MCP 桥，把桌面客户端的 stdio 请求转发到 Hub |
| `cmd/loadtest` | 压测工具，模拟大量 Agent 与 MCP 客户端（见 [loadtest.md](loadtest.md)） |

```text
internal/
├── api/        REST 路由、scope 检查、令牌限流、MCP 后端适配、指标、OpenAPI
├── auth/       Bearer 认证、Principal、客户端 IP 解析
├── auditutil/  审计内容脱敏与截断
├── cluster/    Agent 连接管理、握手限流、RPC、资源画像批量落库
├── executor/   本机执行器、文件操作、Session（环形缓冲与 TTL）
├── mcp/        MCP JSON-RPC、Streamable HTTP / SSE 传输、工具定义
├── model/      公共数据结构
├── notify/     Webhook 通知
├── security/   Tool Registry 与策略快照
├── storage/    SQLite（设备、凭据、令牌、策略、审计、Webhook、MCP 服务）
├── sysinfo/    资源画像采集
└── web/        内嵌管理面板
```

## 3. 两条通道

```text
控制通道：客户端 ──HTTPS──▶ 反代 ──▶ Hub（认证 → 限流 → scope → 白名单 → 安全等级）──▶ 本机执行器 / Agent
节点通道：Agent ──WSS /agent/ws──▶ Hub（设备凭据认证，建立规范设备身份）
```

两条通道共享同一设备身份、策略与审计上下文。Agent MCP 也必须经 Hub 授权，客户端从不直接连接 Agent。

## 4. Agent 协议

- Agent 用每设备独立凭据通过 WSS `Authorization` 头认证；Hub 以凭据确定设备身份，请求中自报的设备名只用于路由，不能作为授权依据。
- 握手携带协议版本、Agent 版本与能力；Hub 通过心跳下发资源画像上报间隔，旧版 Agent 忽略该字段。
- Hub 把授权后的操作转换为 Agent RPC，结果返回后写审计再回给调用方。
- 设备状态在内存维护：上线 / 离线立即落库，资源画像批量合并写入。离线设备保留身份与历史数据，被吊销的设备凭据失效。
- 握手限流、重连退避与帧大小限制见 [deploy/agent.md](deploy/agent.md#4-连接行为)。

## 5. 授权模型

详见 [security.md](security.md)。要点：

- 令牌 scope + 设备白名单决定“谁可以调用什么”；
- Tool Registry 给每个工具固定的 Low / Medium / High 风险，节点安全等级决定上限；
- 策略保存在 SQLite，运行时读取内存快照，版本单调递增；
- 未注册 / 离线的远程节点一律拒绝。

## 6. 执行模型

- **一次性 exec**：本机或 Agent 上执行，默认超时 30 秒、最长 300 秒，输出上限 1 MiB；超时向整个进程组发送 `SIGKILL`。全局并发默认 32，单令牌默认 2。 命令正常退出后若后台子进程仍占用输出管道，最多再等 2 秒（`WaitDelay`）后按 shell 退出码结算。
- **Session**（`/api/v1/process/session`）：环形缓冲单会话 2 MiB、总计 8 MiB，硬 TTL 30 分钟、空闲 TTL 10 分钟；会话绑定调用方（令牌 + 客户端），终止只作用于会话内部进程句柄，不接受任意 PID。
- **文件修改**：`edit_block` 唯一匹配 + 临时文件 + fsync + 原子 rename，保留权限，失败不破坏原文件；符号链接写入真实目标（链接保持不变），悬空链接与硬链接（nlink > 1）报错拒绝。`file_write` 同样经临时文件原子写入，保留已有文件的权限与属主，新建文件为 0644。仅保留 9 位权限、属主与属组；不保留 setuid / setgid / sticky 位、扩展属性（xattr）与 ACL。
- **搜索**：单文件超过 10 MiB、前 8 KiB 含 NUL 字节的文件，以及 FIFO、设备等非普通文件都被跳过；读取以 10 MiB 为硬上限，不会把大文件整体读入内存。
- **审计写入**：单写协程异步批量落库（队列 1024，每 100 条或 500 ms 刷写；队列满时同步写入），Hub 正常退出时刷完队列。

## 7. MCP 实现

- JSON-RPC 2.0，serverInfo 版本 `1.2.0`，12 个工具（见 [mcp-clients.md](mcp-clients.md#6-工具与参数)）。
- Streamable HTTP：`POST /mcp`，协议 `2025-03-26`（其他版本回退 `2024-11-05`），会话绑定令牌，空闲 30 分钟过期。
- SSE：`GET /mcp/sse` + `POST /mcp/message`，协议 `2024-11-05`，写超时与背压保护，Hub 关闭时主动结束全部会话。
- stdio：`vps-commander-mcp-stdio` 作为本地进程运行，通过 `-hub` 与令牌调用 Hub。

## 8. 技术选型

Go 1.24（标准库 `net/http`）、SQLite（WAL）、gorilla/websocket、Go embed 内嵌面板。单二进制部署，发布构建 `CGO_ENABLED=0`，覆盖 Linux amd64 / arm64 / armv7。
