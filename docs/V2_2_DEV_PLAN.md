# VPS-Commander v2.2 开发方案：令牌治理 / 连接扩展 / 功能完善 / 文档

- 编写：代码审核员 C　日期：2026-10-09
- 适用代码：`/root/.openclaw/workspace/vps-commander`（基线 `7377e30` + 工作区未提交改动）
- 交付方式：按下文 Phase 拆分提交，每个 Phase 完成后提供 commit hash，由审核员 C 按「验收标准」逐条实测。审核通过前**不得**替换 `/opt/vps-commander` 生产二进制、不得重启 `vps-commander-hub` / `vps-commander-agent`。

---

## 0. 现状（2026-10-09 实测）

| 项 | 现状 | 问题 |
|---|---|---|
| 认证 | `internal/auth` 只有**一个**全局 API Key（env `VPS_COMMANDER_API_KEY`），REST、MCP SSE、MCP message 共用；面板可 rotate | 无法区分是谁接入；轮换会让所有客户端同时失效；无权限范围 |
| 审计 | `audit_logs(timestamp, caller_ip, device, action, command, exit_code, duration_ms, error_msg)`，约 8k 行，exec 占 86% | 没有「操作人 / 操作对象类型」；只能看 IP；无索引、无保留策略、无查询过滤 |
| `Store.Audit` 调用点 | 约 25 处（api.go、mcp_adapter.go、notify.go），参数为位置参数 | 改字段需统一重构 |
| Agent 心跳 | Agent 每 15s 发一次 `ping` 且**携带完整 profile**；Hub 每次收到都 `UpsertRemote` 写 SQLite | N 台设备 = N/15 次/秒同步写库，规模上去后与审计写入争抢单写锁 |
| Agent 重连 | 指数退避 1s→30s，**无抖动** | Hub 重启时所有 Agent 同步重连（惊群） |
| MCP 传输 | 仅旧版 SSE（`GET /mcp/sse` + `POST /mcp/message`）；无 Streamable HTTP（见 `docs/KNOWN_ISSUE_STREAMABLE_HTTP_20261002.md`） | 每个客户端占一条长连接；Kotlin SDK 等新客户端接不进来 |
| MCP 会话 | `sessions map` 无上限、无空闲超时；会话不绑定令牌；channel 满时 `select default` **静默丢弃响应** | 连接泄漏风险；客户端偶发「请求无响应」；理论上持有任一有效 key 者可向他人 session 投递 |
| 限流 | 全局 `ExecLimiter=32`、`SearchLimiter=8` | 无按令牌限流，一个失控客户端可占满 |
| 请求日志 | 每个请求 `log.Printf("[REQ] ...")` | 量大时 journald 压力，且无结构化 |
| 面板 | `probe.js` 每 5s 轮询 `/probe/summary`（**无需登录**，返回设备名/国家/profile） | 多开页面放大压力；公开探针是否保留需决策 |
| 仓库卫生 | 工作区有 3 个未提交改动（OAuth 下发 API Key 漏洞修复、`local` 别名、MCP 端点强制鉴权），**生产二进制已包含但 git 未提交**；`gitea` remote URL 内嵌明文访问令牌 | 见 Phase 0 |

---

## Phase 0：开工前必须完成（P0，单独 1 个提交）

1. 将工作区 3 个未提交文件（`cmd/hub/main.go`、`internal/api/api.go`、`internal/mcp/oauth.go`）按现状提交，提交信息注明「与生产 2026-10-07 二进制一致」。
2. 从 `.git/config` 移除 `gitea` remote 中的明文令牌（改用 credential helper 或 SSH），并在 Gitea 上**作废该令牌**重新生成。
3. 清理 `docs/*.bak-*`、`*.pre-review-fix-*` 备份文件（移入 `docs/archive/` 或删除）。
4. `go build ./... && go vet ./... && go test ./...` 全绿作为后续所有 Phase 的基线。

---

## Phase A：命名 MCP / API 令牌 + 审计「操作人 / 操作对象」（需求 1）

### A1. 数据模型（新增表，迁移幂等）

```sql
CREATE TABLE IF NOT EXISTS api_tokens (
  id            TEXT PRIMARY KEY,           -- ulid/uuid
  name          TEXT NOT NULL UNIQUE,       -- 用户自定义名称，如 "hark-mobile"、"claude-desktop-home"
  prefix        TEXT NOT NULL UNIQUE,       -- 明文前 8 位，用于展示与快速查找
  token_hash    TEXT NOT NULL,              -- sha256(完整令牌)，hex
  scopes_json   TEXT NOT NULL,              -- ["devices:read","files:read","files:write","exec","process","mcp:call","admin"]
  devices_json  TEXT,                       -- 允许的设备白名单，NULL = 全部
  rate_rps      INTEGER NOT NULL DEFAULT 10,
  max_sessions  INTEGER NOT NULL DEFAULT 10,
  note          TEXT,
  created_at    INTEGER NOT NULL,
  created_by    TEXT NOT NULL,              -- "panel:admin" / "env:bootstrap"
  expires_at    INTEGER,                    -- NULL = 不过期
  last_used_at  INTEGER,
  last_used_ip  TEXT,
  revoked_at    INTEGER,
  is_legacy     INTEGER NOT NULL DEFAULT 0  -- 由 env API Key 迁移而来
);
```

- 令牌格式：`vpc_<prefix8>_<32 位 base62 随机>`，**仅在创建/轮换时返回一次**明文，库中只存 sha256。
- 校验：按 prefix 查行 → `subtle.ConstantTimeCompare` 比较 hash → 检查 revoked/expired。
- `last_used_at/ip` 在内存中合并，**每 60s 批量落库**，不得每请求写库。

### A2. 兼容旧 API Key

- 首次启动若 `api_tokens` 为空且 env `VPS_COMMANDER_API_KEY` 存在：插入一条 `name="legacy-env"`、`is_legacy=1`、全部 scope 的记录（存 hash，不存明文）。
- 旧 key 继续可用；面板对 legacy 令牌显示黄色警告「建议为每个客户端创建独立令牌」。
- 新增 env `VPS_COMMANDER_LEGACY_KEY=off` 可禁用。
- 现有面板「Rotate API Key」改为对 legacy 令牌的轮换，不影响其它命名令牌。

### A3. 认证中间件与 Principal

- `auth.Middleware` 改为解析出 `Principal{Type, ID, Name, Scopes, Devices, TokenID}` 并放入 `context`；类型：`api_token` / `panel` / `agent` / `system`。
- 面板登录会话的 Principal 为 `panel:admin`（后续可扩展多用户）。
- 鉴权 = **令牌 scope 允许** AND **现有安全策略（Low/Medium/High 门禁）允许** AND **设备在白名单内**；任一拒绝返回 403 并记审计 `result=denied`。
- scope 与动作映射集中在 `internal/security/registry.go`（已有 Tool Registry，扩展字段 `scope`），REST 与 MCP 共用同一张表，禁止各处硬编码。
- MCP 工具 scope 映射：`list_devices -> devices:read`；`exec_command -> exec`；`read_file/read_multiple_files/diagnostic_command/list_agent_mcp -> files:read`；`list_processes -> process`；`create_directory/move_file/edit_block/write_file -> files:write`；`call_agent_mcp -> mcp:call`。其中 `list_processes` 使用既有 `process` scope，`list_agent_mcp` 使用 `files:read`，作为当前兼容设计明确记录。

### A4. MCP 会话绑定令牌

- `HandleSSE` 建立会话时记录 `session.ownerTokenID`；`HandleMessage` 必须校验请求令牌 == 会话 owner，否则 403。
- 每令牌会话数 ≤ `max_sessions`，全局 ≤ `VPS_COMMANDER_MAX_MCP_SESSIONS`（默认 500），超出返回 429。

### A5. 审计表扩展

```sql
ALTER TABLE audit_logs ADD COLUMN actor_type  TEXT;   -- api_token / panel / agent / system
ALTER TABLE audit_logs ADD COLUMN actor_id    TEXT;   -- token id
ALTER TABLE audit_logs ADD COLUMN actor_name  TEXT;   -- 令牌名称（冗余存储，令牌删除后仍可读）
ALTER TABLE audit_logs ADD COLUMN transport   TEXT;   -- rest / mcp / panel / stdio / internal
ALTER TABLE audit_logs ADD COLUMN target_type TEXT;   -- device / file / process / token / webhook / policy / mcp_service
ALTER TABLE audit_logs ADD COLUMN target      TEXT;   -- 设备名、文件路径、令牌名等
ALTER TABLE audit_logs ADD COLUMN result      TEXT;   -- ok / denied / error
ALTER TABLE audit_logs ADD COLUMN request_id  TEXT;
ALTER TABLE audit_logs ADD COLUMN session_id  TEXT;
CREATE INDEX IF NOT EXISTS idx_audit_ts     ON audit_logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_actor  ON audit_logs(actor_name, timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_device ON audit_logs(device, timestamp);
```

- 迁移需先检测列是否存在（`PRAGMA table_info`），可重复执行；旧数据 `actor_name='unknown(legacy)'`。
- `Store.Audit(...)` 位置参数改为 `Store.Audit(ctx, AuditEntry{...})`，从 ctx 自动取 Principal / transport / request_id；**所有约 25 个调用点全部迁移**，编译期不允许遗留旧签名。
- 令牌自身的创建、轮换、吊销、删除、scope 修改均写审计（`target_type=token`）。
- 审计中**永不**出现令牌明文；`redactAudit` 增加对 `vpc_` 前缀的脱敏。

### A6. 接口与面板

- REST（需 `admin` scope）：`GET/POST /api/v1/tokens`、`POST /api/v1/tokens/{id}/rotate`、`POST /api/v1/tokens/{id}/revoke`、`DELETE /api/v1/tokens/{id}`、`PATCH /api/v1/tokens/{id}`（名称、scope、设备、限流、过期）。
- 审计查询：`GET /api/v1/audits?actor=&device=&action=&result=&from=&to=&cursor=&limit=`，游标分页，`limit` ≤ 500；`GET /api/v1/audits/export.csv`。
- 面板新增「访问令牌」页（列表：名称、前缀、scope、最后使用时间/IP、状态）与审计页的「操作人」列、筛选器。
- 审计保留：env `VPS_COMMANDER_AUDIT_RETENTION_DAYS`（默认 90），每天清理一次。

### A 验收标准

1. 新建令牌 `reviewer-c`（仅 `devices:read`）：`GET /api/v1/devices` 200；`POST /api/v1/exec` 403，且审计出现 `actor_name=reviewer-c, result=denied`。
2. 用令牌 X 通过 MCP 执行 `exec`，审计行同时有 `actor_name=X, transport=mcp, target_type=device, target=<设备>`。
3. 令牌 Y 向令牌 X 的 `sessionId` POST 消息 → 403。
4. 吊销令牌后 5s 内该令牌所有请求 401；已建立的 SSE 会话被关闭。
5. 旧 env Key 仍可用并在审计中显示为 `legacy-env`；`VPS_COMMANDER_LEGACY_KEY=off` 后 401。
6. `sqlite3 commander.db "select count(*) from api_tokens where token_hash like 'vpc_%'"` = 0；全库、日志 grep 不到任何令牌明文。
7. 对旧库副本执行两次启动迁移，无报错、列不重复。

---

## Phase B：Hub 连接压力优化（需求 2）

目标：单 Hub 支撑 **500 Agent + 50 个并发 MCP 客户端**。

### B1. 心跳与写库

- Agent：拆分 `ping`（15s，不带 profile）与 `profile`（默认 60s，可由 Hub 下发间隔；仅在变化超过阈值时上报完整字段）。
- Hub：设备状态全部在内存维护；SQLite 采用**写后合并**——状态变化（上线/离线）立即写，profile 每 60s 一个事务批量 flush。
- 审计写入改为单写协程：buffered channel（1024），每 500ms 或 100 条一批 `INSERT`；channel 满时同步写并计数告警，**不得丢审计**。
- SQLite PRAGMA：`busy_timeout=5000`、`synchronous=NORMAL`、`wal_autocheckpoint` 合理设置；启动时打印。

### B2. 重连与握手

- Agent 退避使用 full jitter：`sleep = rand(0, min(cap, base*2^n))`，首次重连额外附加 0–10s 随机延迟；读取握手响应状态码，遵守 `Retry-After`。
- Hub `/agent/ws` 握手限流：全局 `50/s`、突发 `200`；单 IP `5/s`、突发 `20`。超限返回 `429 Too Many Requests` + `Retry-After: 1`，Agent 遵守。限流和 GeoIP 统一使用 `auth.ClientIP`：默认仅使用连接地址；开启 `VPS_COMMANDER_TRUST_PROXY=on` 后按该函数规则读取代理头。反代必须正确覆盖/追加客户端 IP，不能直接信任客户端伪造的头。共用同一公网 IP/NAT 的 Agent 共享单 IP 桶，集体重连时可能触发 429 并依赖退避恢复。
- WebSocket：升级后立即调用 `SetReadLimit(8<<20)`（8 MiB）；每节点单写协程 + 有界发送队列，替代 `WriteMu` 直接写。

### B3. MCP 传输

- 新增 **Streamable HTTP** 单端点 `POST /mcp`（MCP 2025-03-26），优先实现无状态模式：请求直接以 JSON 响应，不占长连接；支持 `Mcp-Session-Id` 可选会话。保留旧 SSE 端点兼容。解决 `KNOWN_ISSUE_STREAMABLE_HTTP_20261002.md`。
- 旧 SSE：会话空闲 30 分钟自动关闭；channel 满时改为「最长阻塞 5s，超时则关闭会话并返回错误」，**删除静默丢弃**。

### B4. 限流与资源

- 按令牌令牌桶（`rate_rps`）+ 按令牌并发 exec 上限；超限返回 429 + `Retry-After`，记审计 `result=denied, error_msg=rate_limited`。
- 保留全局 `ExecLimiter/SearchLimiter`，改为可配置 env。

### B5. 面板与日志

- `/probe/summary` 与 `/panel/api/devices` 结果缓存 2s，支持 `ETag/If-None-Match`（304）；前端页面不可见时（`visibilitychange`）暂停轮询。
- 请求日志改为结构化（`log/slog`），默认跳过 `/healthz`、`/static/`、304；env `VPS_COMMANDER_LOG_LEVEL`。
- CORS：`Access-Control-Allow-Origin` 改为 env `VPS_COMMANDER_CORS_ORIGINS` 白名单，默认不放开 `/api/`。

### B6. 可观测性

- `GET /metrics`（需 `admin` scope，Prometheus 文本）：在线 Agent 数、MCP 会话数（按传输）、在途 exec、审计队列深度、SQLite 写延迟、429 计数、Goroutine 数、RSS。

### B7. 压测工具

- 新增 `cmd/loadtest`：模拟 N 个假 Agent（WS 握手 + 心跳 + 响应 exec）与 M 个 MCP 客户端（SSE 与 Streamable HTTP 各半）。

### B 验收标准（审核员 C 在测试机上用 `cmd/loadtest` 复现）

1. 500 假 Agent + 50 MCP 客户端稳定运行 10 分钟：Hub CPU 平均 < 1 核，RSS < 300MB，`/api/v1/devices` p99 < 100ms，无心跳误判离线。
2. 稳态下 SQLite 写入 ≤ 每秒 15 次事务（`/metrics` 读取）。
3. 重启 Hub：500 Agent 的重连分布在 ≥ 30s 窗口内，Hub 无 5xx 风暴，2 分钟内全部恢复在线。
4. 单令牌超限得到 429，其它令牌不受影响。
5. Kotlin SDK `StreamableHttpClientTransport`（或等价 curl 序列）`initialize → tools/list → tools/call` 全通；旧 SSE 客户端仍可用。
6. 人为让 SSE 客户端不读取：Hub 不 OOM，会话在超时后被关闭，审计/日志有记录。
7. 旧版 Agent（当前生产版本）可连接新 Hub 正常工作（协议向后兼容）。

---

## Phase C：功能完善（需求 3）

### C1. 设备接入

- 面板「添加设备」向导：输入设备名/标签 → 生成**一次性注册令牌**（30 分钟有效、单次使用、绑定设备名）→ 展示可复制的安装命令（`install.sh --hub ... --name ... --enroll-token ...`）。
- Agent 首次连接用注册令牌换取长期设备凭据（复用现有 `device_credentials`），注册令牌立即失效。
- 设备状态：`pending / online / offline / revoked`；支持标签/分组、重命名、删除；显示 Agent 版本并标记落后版本。
- 所有接入、吊销、删除写审计（`target_type=device`）。

### C2. 通知

- 新表 `notification_rules(id, name, event_type, severity_min, devices_json, targets_json, cooldown_sec, quiet_hours, enabled)`；现有离线/恢复/早晚报迁移为默认规则。
- 新事件：CPU/内存/磁盘阈值（持续 N 分钟）、Agent 版本落后、`security_denied` 突增、令牌创建/吊销、令牌从新 IP 首次使用、高风险操作（High 模式下的 exec/file_write）、审计队列积压。
- 投递：失败重试（指数退避 3 次）、`notification_deliveries` 投递记录、面板可查看与一键静音。

### C3. 日志分析

- 面板「审计分析」：按操作人/设备/动作的日趋势、失败率、被拒操作 Top、耗时 Top、最活跃令牌。
- 规则化异常检测（不引入 ML）：非常用时段操作、单令牌 1 分钟内操作 > 阈值、新 IP，命中即发 C2 通知。
- 可选只读能力 `log_query`（Agent 侧 `journalctl -u <unit> --since ... | grep`，输出上限 1MB、超时 30s），纳入 Tool Registry，scope 为 `logs:read`，默认 Low 风险。

### C 验收标准

1. 全新 VM 通过向导命令 2 分钟内接入并在线；同一注册令牌第二次使用失败。
2. 用 `stress` 将测试机 CPU 拉满 5 分钟 → 收到一次告警，冷却期内不重复，恢复后收到恢复通知。
3. 新令牌从新 IP 调用 → 收到「新 IP」通知。
4. 审计分析页数字与 `sqlite3` 手工统计一致。

---

## Phase D：部署与使用文档（需求 4，可与 A/B/C 并行）

目录重组：

```
README.md                 概览 + 5 分钟快速开始
CHANGELOG.md
docs/deploy/hub.md        系统要求、systemd、env 全表、Nginx/Caddy 反代示例（WebSocket Upgrade、SSE 关闭缓冲 proxy_buffering off、超时）
docs/deploy/agent.md      安装、注册令牌、升级、卸载、离线排查
docs/mcp-clients.md       创建命名令牌 → Claude Desktop / Cursor / Codex / Hark / Kotlin SDK 配置；SSE 与 Streamable HTTP 区别
docs/security.md          令牌 scope、设备白名单、安全模式、轮换与吊销、审计保留
docs/operations.md        升级/回滚、数据库备份、日志、/metrics、常见故障 FAQ
docs/api.md               由 /openapi.json 生成
docs/archive/             现有带日期的评审/交接文档全部移入
```

### D 运维文档补充：执行器后台进程边界

- 会话通过 `cmd.WaitDelay = 2s` 限制后台子进程继承 stdout/stderr 管道导致的等待。若 shell 退出时仍有后台子进程持有管道，会话完成最多额外等待约 2 秒；超过该等待窗口后，后台子进程的后续输出不保证被会话捕获。
- 运维文档必须明确：**需要长期运行的后台命令应自行重定向 stdout/stderr**，例如 `nohup command > /var/log/command.log 2>&1 < /dev/null &`；不要依赖已结束会话继续收集后台进程输出。
- **一次性 exec（REST `/api/v1/exec` / MCP `exec`）注意：** `Local.Run` 当前未设置 `WaitDelay`。后台子进程未重定向输出时，`cmd.Wait()` 可能一直等待到 context 超时；超时路径会向整个进程组发送 `SIGKILL`，通常返回 `exit_code=124`，后台进程也会被杀掉。请使用上述 `nohup` 重定向方式，或改用会话执行。
- 退出码以 shell 的 `ProcessState.ExitCode()` 为准；单纯因 `WaitDelay` 到期而产生的 `exec.ErrWaitDelay` 不应将 shell 的成功退出误记为失败。
- 工程遗留（低风险）：会话结束后再次调用 `Kill()` 仍可能尝试向原进程组发送信号。后续可增加“会话已结束则跳过”的保护，降低进程组成员退出后 PID/PGID 被复用时误发信号的理论风险；当前不作为本阶段阻塞项。

### D 部署安全说明

- `VPS_COMMANDER_TRUST_PROXY=on` 时，`auth.ClientIP` 只读取 `X-Forwarded-For` 最右侧的有效 IP，不再信任 `X-Real-IP`。因此 Lucky 必须覆盖或正确追加 XFF 中的实际客户端 IP；同时必须确保 Hub 后端不能被不经 Lucky 的不可信客户端直接访问，否则请求方仍可伪造 XFF。
- 部署前通过 Lucky 的回显/诊断路径验证 XFF 最右侧确实是客户端公网 IP，并验证设备列表与限流归属一致；未完成此检查不得上线。
- MCP 与 REST 统一使用 `auth.ClientIP` 记录 caller IP；MCP 审计 transport 统一为 `mcp`，device 使用 Hub 归一后的节点名。

### D 验收标准

1. 审核员 C 在一台全新 Debian VM 上**只按文档**完成：Hub 部署 → 反代 → 接入 1 台 Agent → 创建命名令牌 → MCP 客户端连上并执行命令；任何一步需要看源码才能继续即不通过。
2. 文档中的每个 env 变量都与代码 `os.Getenv` 一一对应（审核时 grep 核对）。
3. 文档内不出现任何真实令牌、密码、内网地址。

---

## 提交与审核约定

- 提交顺序：Phase 0 → A → B → C，D 随行；每个 Phase 可多个提交，但每个提交需能独立编译通过。
- 每次提交附：`go build ./... && go vet ./... && go test ./...` 输出、新增/修改的测试列表。
- 新增单测最低要求：令牌校验与吊销、scope × 安全策略组合、审计迁移幂等、会话 owner 校验、限流、心跳写后合并、注册令牌单次使用。
- 数据库迁移前自动备份 `commander.db` 到 `data/backup-<ts>.db`。
- 待用户决策（开发先按默认实现，可配置）：
  1. `/probe/summary` 公开探针：默认**保留公开但去掉 profile 细节**，env `VPS_COMMANDER_PUBLIC_PROBE=full|basic|off`。
  2. 审计默认保留 90 天。
