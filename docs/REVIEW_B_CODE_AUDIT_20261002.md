# 审核专家 B：代码实现审计报告

> 文档属性：历史独立代码审计快照。不要将当时的 BLOCKER 列表直接视为当前未修复列表。

- **审核人**：审核专家 B（外部 Agent）
- **日期**：2026-10-02
- **对象**：`/root/.openclaw/workspace/vps-commander` 全量 Go 实现
- **对照文档**：《VPS-Commander 平滑升级改造与三级安全拦截实施方案》第 7 章
- **方法**：逐行审读安全、协议、执行器、API 四大核心包，非风格审查
- **结论**：**有条件不通过 —— 5 个阻断项未修复前不得进入 Phase 1**

---

## 一、阻断项（Blockers）

### B1. MCP 接口完全绕过三级安全门禁【严重】

- **位置**：`internal/api/mcp_adapter.go`、`internal/mcp/mcp.go`
- **事实**：两文件**零处**引用安全策略。`MCPBackend.ExecCommand/ReadFile/WriteFile/EditBlock/ListAgentMCP/CallAgentMCP`
  直接执行，不调用 `requireSecurity`。
- **影响**：`/mcp/sse` 是 AI Agent 实际使用的主接口——安全模型只管住了 REST，
  管 Agent 的通道完全敞开。审计过程中以普通 MCP 客户端身份调用 `exec_command` 未触发任何策略检查。
- **判定**：阻断。修复前不得向任何不可信调用方暴露 token。

### B2. 单一全局 token，可自行提权【严重】

- **位置**：`internal/api/api.go` → `UpdateSecuritySettingsJSON`（`/api/v1/security/settings/update`）
- **事实**：仅凭全局 API token 即可修改全局及任意设备的风险等级。实现中无 admin/user 角色区分，
  Spec 7.2 假设的 "Admin Web" 不存在。
- **影响**：拿到 token 即拿到 admin，可把任意设备设为 High，三级门禁形同虚设。
- **判定**：阻断。策略变更必须独立鉴权（独立 admin 凭证或 mTLS 管理身份）。

### B3. 设备身份自报，per-device 策略不可执行【严重】

- **位置**：`internal/api/api.go:467`（`device := r.URL.Query().Get("device")`）、
  `internal/cluster/manager.go:108`（`device_name` 取自 query）
- **事实**：设备名来自请求参数自报；Agent WS 仅以共享 cluster secret 认证，device_name 自报。
- **影响**：任意 token 持有者可冒充任意设备，挑选策略最松的设备执行。
- **判定**：阻断。需 per-device 凭证（mTLS 或 per-agent token）后 per-device 策略才有意义。

### B4. Session 所有权形同虚设【严重】

- **位置**：`internal/api/api.go:687` `sessionOwner(r)`
- **事实**：owner = 全局 token 的 SHA256。单 token 体系下所有调用者 owner 哈希相同，
  `Session.Authorized()` 对任何已认证调用者恒为真。
- **影响**：Spec 7.5.2 的所有权模型空转，任意调用者可读写/注入/终止他人 Session。
- **判定**：阻断。与 B2 同源，需先解决调用者身份体系。

### B5. 未知设备 fail-open【严重】

- **位置**：`internal/security/policy.go` → `EffectiveRisk()`
- **事实**：设备不在策略表中 → 回退 `GlobalRisk`。若 GlobalRisk 为 High，拼错设备名反而获得最高权限。
- **违反**：Spec 7.2.3 明确要求 fail-closed（不得默认放行 High）。
- **判定**：阻断。未知设备应默认拒绝 Medium/High，或拒绝一切非常读操作。

---

## 二、高优先级（Phase 3 前修复）

### H1. Session 清理逻辑错误
- **位置**：`internal/executor/session_manager.go` → `Cleanup()`
- **事实**：以 `StartedAt` 计算所谓“空闲超时”（实为最大生命期）；已过期但仍在运行的 session 永不清理（只删已结束的）→ 资源泄漏。
- **违反**：Spec 7.5.4 要求的空闲超时 + 最大生命期双轨制。

### H2. 无 agent 全局 Session 上限与进程创建速率限制
- **位置**：`internal/executor/session_manager.go`
- **事实**：`MaxSessionsPerManager = 16` 仅为单 manager 上限；`Start()` 存在 TOCTOU（解锁→启动进程→加锁复查），并发可短暂超限。
- **违反**：Spec 7.6。

### H3. 审计日志缺失关键字段
- **位置**：`internal/storage/storage.go:279` `Audit()`
- **事实**：无 `actor`、无 `policy_version`、无 `session_id`；命令明文入库，参数中的 secret 直接进 DB。
- **违反**：Spec 7.7。另需敏感参数脱敏。

### H4. Token 允许经 URL query 传递
- **位置**：`internal/auth/auth.go` → `Middleware.Handler`
- **事实**：接受 `?token=` / `?key=` / `?api_key=`。Token 将进入 access log、代理日志。
- **建议**：仅允许 Authorization header。

### H5. Policy version 无单调性校验
- **位置**：`internal/security/policy.go` → `Publish()`
- **事实**：接受任意版本（含回退）。
- **违反**：Spec 7.2.2。

---

## 三、值得肯定的实现

- **Capability 握手已实现**：`Message` 含 `ProtocolVersion`/`AgentVersion`/`Capabilities`，
  `manager.go:333` 按能力门控——上一轮 spec 评审的阻断项在代码中已解决。
- **Session ID 使用 `crypto/rand`**（`api.go:698`），不可预测。
- **`edit_block` 使用临时文件 + 原子 rename**（`edit.go:41-62`），含目录 fsync。
- **REST 路径 `requireSecurity` 覆盖完整**：exec/read/write/mcp/edit_block 均接入。

---

## 四、总体评价

实现层面的信任模型实质是“持有 token 即上帝”：单 token 无角色分离（B2）、
设备身份自报（B3）、MCP 主通道零门禁（B1）、Session 所有权空转（B4）。
B1–B4 任一单独成立即足以让三级门禁失效，四者并存。

**在 B1（MCP 加门禁）、B2（策略变更独立鉴权）、B3（设备身份认证）修复并复审前，
不得向不可信调用方暴露 token，不得开放 `start_process` 等新能力给 Agent。**

---

*审核专家 B，2026-10-02*
