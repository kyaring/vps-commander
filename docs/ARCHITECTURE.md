# VPS-Commander（自托管私有化多节点运维管家）架构与开发设计规范

## 1. 项目立项背景与目标
### 1.1 背景与痛点
- 当前市面方案（如 Desktop Commander）严重依赖公共第三方中继（SaaS Relay），存在严重的命令与数据泄漏风险、Token 明文暴露风险以及第三方额度/速率限制。
- 现有纯开发网关（如 CodePier）采用项目目录沙箱隔离，无法进行系统级运维（如查看全局进程、管理 Lucky/Docker、修改全盘系统配置等），且不支持 ChatGPT 原生 Actions 插件生态。

### 1.2 核心目标
- **100% 私有化部署**：数据仅在 OpenAI 官方服务器和用户私有 VPS 之间传输，零第三方中转。
- **全权宿主运维 (Host-centric)**：提供全局 Shell、进程管理、文件读写能力。
- **多节点别名调度**：通过单一 Hub 统一管理多台 VPS/客户端节点，支持在 ChatGPT 中通过自然语言昵称（如 "hwsg"、"wjyhk"）随时切换目标节点。
- **原生 ChatGPT 插件集成**：提供标准 OpenAPI 3.1 规范与 Actions 端点，在 ChatGPT 手机端/网页端呈现为原生交互卡片。
- **超轻量 Go 架构**：Hub 常驻 RSS 目标 ≤ 20MB、硬上限 25MB；Agent 常驻 RSS 目标 ≤ 10MB、硬上限 12MB。

---

## 2. 总体架构设计

```text
[ 用户在 ChatGPT APP / Web 端发指令 ]
                │
                ▼ (HTTPS POST + Bearer API Key)
      [ 用户私有域名反代 (Lucky SSL) ]
                │
                ▼ (反向代理至 127.0.0.1:28800)
    ┌────────────────────────────────────────────────────────┐
    │              VPS-Commander Hub (中央管理网关)          │
    │                                                        │
    │  ├─ OpenAPI / Actions Engine (/openapi.json, /api/v1)  │
    │  ├─ 鉴权与安全过滤中间件 (Auth Middleware)            │
    │  ├─ 设备拓扑注册表 (Device Registry & State Machine)   │
    │  ├─ 审计日志存储 (SQLite: audit.db)                    │
    │  └─ 路由分发器 (Router Dispatcher)                     │
    └───────────┬────────────────────────────────┬───────────┘
                │                                │
      (本地进程直连: target="local")   (安全 WebSocket 长连接 / mTLS)
                │                                │
                ▼                                ▼
     [ 本机 Local Executor ]             [ 远程 Agent (Node: hwsg) ]
     - bash -c ...                       - Client Agent Daemon
     - 文件/进程直接操作                  - 接收 JSON-RPC 指令并执行
```

---

## 3. 核心接口与协议规范 (OpenAPI 3.1)

ChatGPT Actions 识别的标准接口集合：

### 3.1 `/api/v1/devices` (GET)
- **说明**：获取当前所有在线/受管设备列表及健康状态。
- **返回**：`[{"name": "wjyhk", "is_local": true, "status": "online"}, {"name": "hwsg", "status": "online"}]`

### 3.2 `/api/v1/exec` (POST)
- **说明**：在指定节点执行 Shell 命令。
- **请求体**：
  ```json
  {
    "device": "hwsg",        // 节点昵称，缺省默认 local / wjyhk
    "command": "free -h",    // 要执行的命令
    "workdir": "/root",      // 可选工作目录
    "timeout": 30            // 超时时间（秒）
  }
  ```
- **响应体**：
  ```json
  {
    "device": "hwsg",
    "exit_code": 0,
    "stdout": "...",
    "stderr": "",
    "duration_ms": 120
  }
  ```

### 3.3 `/api/v1/fs/read` (POST)
- **说明**：受控读取指定设备的文件内容。
- **参数**：`device`, `path`, `offset_bytes`, `limit_bytes`（防止大文件撑爆模型上下文）。

### 3.4 `/api/v1/fs/write` (POST)
- **说明**：在指定设备创建或覆写文件。
- **参数**：`device`, `path`, `content`。

---

## 4. Hub 与 Agent 节点通信规范

1. **Agent 注册与鉴权**：
   - Agent 启动时通过 `wss://hub.domain.com/agent/ws` 发起长连接。
   - 握手使用 `Authorization: Bearer <cluster-secret>` Header；Cluster Secret 不进入 URL。
   - 握手成功后，Hub 内存注册表更新该设备为 `online`，并启动双向 Heartbeat ping/pong（30s 周期）。

2. **指令下发 (JSON-RPC)**：
   - Hub 收到 ChatGPT 请求 ➡️ 生成 `task_id` ➡️ 通过 WS 发送 `{"action": "exec", "task_id": "...", "command": "..."}`。
   - Agent 执行完毕后返回 `{"task_id": "...", "result": {...}}`。
   - Hub 审计日志落库后返回给 ChatGPT。

---

## 5. 项目工程结构与技术选型

- **开发语言**：Go 1.22+（高性能、低内存、单二进制交叉编译友好）
- **Web 框架**：Gin 或纯标准库 `net/http` + `gorilla/websocket`
- **存储**：SQLite (纯粹用于审计日志与持久化配置)

```
vps-commander/
├── cmd/
│   ├── hub/            # 中央控制网关主入口
│   └── agent/          # 节点客户端主入口
├── internal/
│   ├── api/            # ChatGPT Actions HTTP 接口
│   ├── auth/           # Bearer Token 与 Secret 鉴权
│   ├── cluster/        # 节点连接池、心跳与 WebSocket 管理
│   ├── executor/       # 本地/远程执行器抽象
│   └── storage/        # 审计日志与设备配置持久化
├── web/                # 极简设备状态面板（纯 HTML/CSS 嵌入二进制）
├── openapi/            # 供 ChatGPT 导入的 openapi.json
├── Makefile            # 多架构编译 (linux/amd64, linux/arm64)
└── README.md           # 部署与 ChatGPT 配置指南
```

---

## 6. Hub Web 控制面板设计规范

### 6.1 定位与设计原则
- **极简独立**：前端采用纯 HTML5 + TailwindCSS/原生CSS + Vanilla JS，不引入复杂 node 构建工具链，通过 Go 1.16+ `embed.FS` 直接打包进单一二进制产物。
- **开箱即用**：零外部静态资源依赖，访问 `https://hub.yourdomain.com/` 即可直接加载。
- **低资源消耗**：Web 面板由 Hub 服务原生托管，静态文件常驻开销基本为零。

### 6.2 核心功能模块
1. **设备看板 (Device Overview)**
   - 受管设备列表：别名（Alias）、当前状态（Online/Offline）、系统架构与内核、IP 地址、最后心跳（Heartbeat）。
   - 快速连接指引：点击“添加节点”，自动生成带 Token 的单行一键安装/运行命令（如 `curl -sL https://... | bash -s -- --hub=... --token=...`）。
   - 设备启停与踢出操作：支持在面板主动注销失效/弃用节点。

2. **ChatGPT Actions 配置与向导中心**
   - 托管与展示 `/openapi.json` 规范预览。
   - 提供专属 API Key 生成、轮换与撤销功能。
   - 提供直接导入 ChatGPT Custom GPT 的一键复制 Schema 按钮与预设 System Prompt 建议。

3. **实时操作审计与日志 (Audit Log)**
   - 记录来源（ChatGPT / Web 手动）、目标设备、动作（exec / file_read / file_write）、执行内容、执行耗时、退出码与结果摘要。
   - 支持按设备、关键字实时搜索过滤。

4. **Web 快速调试终端 (Quick Terminal / Console)**
   - 提供快速测试输入框，选择目标设备后可直接输入命令测试执行并查看即时输出，无需打开 ChatGPT 即可验证节点联通性。
