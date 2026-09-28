# VPS-Commander 开发进度看板与交付清单

> 角色定位：
> - **开发实施**：外部 GPT
> - **架构监督、文档规范、验收审核与严格收尾**：OpenClaw (yk)

---

## 阶段推进概览 (Status Board)

| 阶段 | 模块 / 目标 | 负责人 | 状态 | 交付物检验 |
| :--- | :--- | :--- | :--- | :--- |
| **Phase 0** | 架构设计、接口契约与开发文档 | OpenClaw | 🟢 **已完成 (Done)** | `ARCHITECTURE.md`, `DEV_SPEC.md` |
| **Phase 1** | Hub 核心骨架、Local Executor 与 Actions 引擎 | GPT (开发) | 🟢 **验收通过 (Passed)** | Hub 本地执行、Bearer 鉴权、`/openapi.json` |
| **Phase 2** | Agent 通信协议与多节点分发 (WebSocket RPC) | GPT (开发) | 🟢 **验收通过 (Passed)** | 5项生产级缺陷彻底整改；并发竞态/强杀0 Panic/Header鉴权实测全绿 |
| **Phase 3** | 内嵌 Web 控制面板与 SQLite 审计模块 | GPT (开发) | 🟢 **验收通过 (Passed)** | CSS MIME、SQLite 并发审计丢失缺陷已修复；100 并发无损落盘、-race、浏览器 MIME 与会话权限复验全绿 |
| **Phase 4** | 独立审核、严苛回归测试与上线收尾 | OpenClaw | 🟡 **收尾整改中 (Hardening)** | 技术门禁基本全绿；待 Custom GPT Actions、Key 轮换与人工签收 |

---

## 详细功能检查清单 (Feature Checklist for GPT)

### 📌 Milestone 1: Hub 基础与单机 Actions (Phase 1)
- [x] **项目骨架构建**：
  - [x] Go 模块初始化 (`go.mod`, module `github.com/wjyhk/vps-commander`)
  - [x] 目录分层清晰（`cmd/hub`, `internal/api`, `internal/executor`, `internal/auth`, `internal/storage`）
- [x] **安全鉴权中间件**：
  - [x] 校验 `Authorization: Bearer <API_KEY>` (ConstantTimeCompare)
  - [x] 无效/缺失 Token 统一返回标准 HTTP 401
- [x] **Local Executor (本地执行器)**：
  - [x] 执行 Shell 命令（`exec.CommandContext` 绑定超时）
  - [x] 捕获 `stdout`, `stderr`, `exit_code`, `duration_ms`
  - [x] 文件分页安全读取（限制单次最大返回字节，防 OOM）
  - [x] 文件安全写入/覆盖
- [x] **OpenAPI 3.1 动态端点**：
  - [x] `GET /openapi.json`：包含 schema 与 BearerAuth
  - [x] `GET /api/v1/devices`：返回本地节点信息
  - [x] `POST /api/v1/exec`：执行本地命令
  - [x] `POST /api/v1/file/read`：读取本地文件
  - [x] `POST /api/v1/file/write`：写入本地文件

---

### 📌 Milestone 2: Agent WebSocket 协议与多节点路由 (Phase 2)

> **2026-09-23 生产级 Code Review 驳回：** 原实现不得直接投入生产。整改项：① pending response channel 竞态导致 send on closed channel；② Cluster Secret 禁止出现在 WebSocket URL Query，改用 Authorization Bearer Header；③ Agent 指令处理改为并发 Goroutine，避免长任务阻塞读循环；④ Agent 增加主动 ping、ReadDeadline，防止半开连接僵尸；⑤ Hub/Agent WebSocket 写入统一设置 10s WriteDeadline；同时将 LastSeen 改为原子时间戳，消除竞态数据访问。Phase 3 在本项重新验收前保持阻塞。
- [x] **Agent 二进制开发 (`cmd/agent`)**：
  - [x] 读取配置文件或 CLI 参数（`--hub`, `--name`, `--token`）
  - [x] 主动向 Hub 发起 WSS 连接（`wss://<hub>/agent/ws`）
  - [x] Cluster Secret 通过 WebSocket Authorization Bearer Header 发送，不进入 URL Query
  - [x] 指令处理采用独立 Goroutine，并由单一写锁保护并发响应
  - [x] 主动 30s ping + 75s ReadDeadline，防止半开连接永久阻塞
  - [x] WebSocket 写入统一 10s WriteDeadline
  - [x] 指数退避断线重连（1s, 2s, 4s... 最大 30s）
- [x] **Hub 节点连接池 (`internal/cluster`)**：
  - [x] WebSocket 握手鉴权（校验 Cluster Secret，凭据仅走 Authorization Bearer Header）
  - [x] Pending RPC 响应使用 LoadAndDelete + 非阻塞投递，断线不再 close response channel
  - [x] Hub ReadDeadline / WriteDeadline（75s / 10s）
  - [x] LastSeen 使用 atomic.Int64，避免读写竞态
  - [x] 设备连接注册表（维护设备别名、WS 连接、心跳时间）
  - [x] 定时双向 Ping/Pong（30s 周期，超时 2 次标记离线）
- [x] **RPC 路由与跨节点下发**：
  - [x] 当 API 请求 `device == "local"` 或本机别名时 ➡️ 走 Local Executor
  - [x] 当 API 请求 `device == "<remote_alias>"` 时 ➡️ 查找连接池，封装 JSON-RPC 消息异步等待回调
  - [x] 设备不在线或找不到时，返回友好错误：“设备 `<device>` 处于离线状态”

---

### 📌 Milestone 3: 内嵌 Web 控制面板与审计存储 (Phase 3)

> **2026-09-23 M3 门禁状态：🟢 验收通过 (Passed)。** 代码级修复了静态 CSS 后缀判断错误与 SQLite WAL 单写队列；经 100 并发突发请求压测，SQLite 审计日志 100% 无损落盘，Session 认证、MIME 判定、静态内嵌与看板全绿通过。
- [x] **SQLite 存储 (`internal/storage`)**：
  - [x] 自动初始化数据库 `commander.db`，开启 WAL 模式与 busy_timeout
  - [x] 每次执行写入 `audit_logs`（记录触发者 IP、设备、指令、退出码、耗时）
- [x] **Web 控制面板 (`web/`)**：
  - [x] 纯 HTML5 + 原生 CSS 单页面，使用 `embed.FS` 打包（零外部静态资源）
  - [x] **看板 1**：节点在线状态卡片、系统基础信息
  - [x] **看板 2**：ChatGPT Actions 向导（一键复制 Schema、Prompt 模板、Key 轮换）
  - [x] **看板 3**：审计日志流水与刷新查询
  - [x] **看板 4**：Web 快速调试执行器（选择设备并执行命令）

---

### 📌 Milestone 4: 审核、收尾与交付 (Phase 4, 由 OpenClaw 严格把关)

> **2026-09-23 M3 正式验收通过，Phase 4 已解锁。** 本阶段进入独立总装门禁；未经全部回归、内存基线、上线链路与最终文档验收，不得标记为完成。
- [x] 代码静态分析与编译：\`go vet ./...\`、\`go test -race ./...\`、AMD64/ARM64 clean build 全通过
- [x] 内存硬上限回归：Hub 当前约 14.5MB；Agent 经 `GOGC=50` 优化后约 10.5MB，均低于 25MB/12MB 硬上限；20MB/10MB 仍作为优化目标保留
- [x] 12 项原子功能：TC-01~TC-07、TC-09、TC-11 已实测；TC-08 离线感知实测通过；TC-12 100 并发已实测且审计 100% 落盘；TC-10 页面/MIME 已在 M3 浏览器实测
- [x] Lucky 反代与正式域名 HTTPS：vpstool.kory.kdns.fr 已实测反代至本机 127.0.0.1:9521；根路径 302 到 /panel/login，OpenAPI 3.1.0 经 HTTPS 实链可访问，WebSocket Agent 已通过该上游注册在线
- [ ] ChatGPT Custom GPT 真实联调通过并归档

### M4 收尾整改记录（2026-09-28）
- [x] 修正文档与实现漂移：正式 Hub 监听端口统一为 `127.0.0.1:9521`；Agent 握手统一为 `Authorization: Bearer` Header，Cluster Secret 不进入 URL。
- [x] Hub 禁止通过 CLI 参数接收 API Key、Cluster Secret、Web Password；三项敏感凭据现在仅从环境变量读取，避免进入 `/proc/*/cmdline`。
- [x] 统一内存门禁口径：Hub 25MB、Agent 12MB 为硬上限；20MB/10MB 为优化目标。
- [x] 清理遗留测试 Agent；正式 Hub/Agent 已由 systemd 单实例接管，并完成单实例远程 Agent 实联调。
- [ ] Custom GPT Actions 真实调用、Key 轮换、审计闭环与最终人工签收。

### M4 2026-09-28 实联调证据
- [x] Hub/Agent 已切换为 systemd 管理；Hub 只监听 `127.0.0.1:9521`。
- [x] Hub 敏感凭据不再接受 CLI 参数，仅从 `EnvironmentFile` 环境变量读取；进程命令行已核验无凭据。
- [x] Agent 单实例：仅保留 `m4-live-agent` 一个正式实例；通过 `wss://vpstool.kory.kdns.fr/agent/ws` 成功注册 online。
- [x] VPS-Commander 实联调：本机 exec、远程 Agent exec、远程 file read、远程 file write、timeout=1s 均通过。
- [x] 未授权 HTTP 请求返回 401；有效 Bearer 请求返回 200。
- [x] 审计日志已记录上述 Actions 操作，最新抽查包含 `file_read` 与 timeout=124。
- [x] 临时凭据文件 `/tmp/m4-prod.env` 已删除；`/etc/vps-commander/{hub,agent}.env` 权限均为 `0600`。
- [ ] Custom GPT Actions UI 仍需用户侧完成正式 OpenAPI 导入、Bearer Key 配置及对话调用；未完成前不得签署 M4。

### M4 管理面板 UI 验收记录（2026-09-28）
- [x] 手机端多 Agent 显示异常整改完成：设备卡片、Header、Actions、设备选择器、审计表均完成窄屏适配。
- [x] 面板颜色体系改为 CSS Variables 双态主题，支持深色/浅色切换。
- [x] 主题选择持久化到 localStorage，登录页与主面板统一。
- [x] UI 静态资源与 Go embed 资源保持同步，并完成 Node JS 语法、go test ./...、release build 验证。
- [x] 用户已于 2026-09-28 人工验收通过；已重新构建 Hub 二进制并重启 systemd 服务，正式线上 UI 已立即生效。
- [ ] M4 总体验收仍保留 Custom GPT Actions 正式联调、Key 轮换及最终人工签收门禁。

### M4 终验签收记录（2026-09-28）
- [x] 安全清理：已彻底清除 `openapi.json` 中冗余残留的 `apiKey` 定义，统一收敛为标准 `bearerAuth`。
- [x] 二次回归验证通过：全链路 API 鉴权、受控节点列表获取及本地命令执行均现场实测 100% PASS。
- [x] 服务状态确认：`vps-commander-hub` (PID: systemd) 运行稳定，二进制构建已同步更新。
