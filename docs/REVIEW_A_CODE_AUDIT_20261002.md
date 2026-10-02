# 审核专家 A：代码实现与架构安全穿透审计报告

- **审核人**：审核专家 A（系统架构与安全审核组）
- **日期**：2026-10-02
- **对象**：`/root/.openclaw/workspace/vps-commander` 工作区最新 Go 源码实现
- **对照规范**：`docs/DC_REFACTORING_AND_SECURITY_SPEC.md`（VPS-Commander 平滑升级与三级安全拦截实施方案）
- **审查维度**：架构漏洞、安全绕过、协议兼容性、并发/竞态、生产失联风险、Spec 与代码一致性
- **最终结论**：**判定阻断（不通过）—— 存在 3 大核心架构与安全阻断项及 3 项生产级崩溃/失联隐患，严禁直接上线生产**

---

## 一、核心阻断项（Blockers - 违反 Spec 铁律，安全归零）

### A1. 【致命安全绕过】Hub 信任客户端自报设备名，Per-Device 策略完全被穿透
- **违反规范**：Spec 8.1 明确规定：
  > “`identify device` 不得等同于读取请求中的 `device` 字段。设备名称只能作为路由提示，不能作为授权依据。禁止：`policy := snapshot.Device(r.Device)`。”
- **代码位置**：`internal/api/api.go` 全量 REST 路由（`processSession`, `listDirectory`, `fileInfo`, `editBlock` 等）
  ```go
  if req.Device == "" {
      req.Device = s.LocalName
  }
  if !s.requireSecurity(w, r, req.Device, "exec", req.SessionID) {
      return
  }
  ```
- **漏洞事实**：Hub 在调用 `s.requireSecurity` 计算生效风控等级（`EffectiveRisk`）时，直接使用了未经签名、未经校验的请求体参数 `req.Device`。
- **真实危害**：
  攻击者或不受信客户端若需在严格受控节点 A（策略为 `Low`，禁止执行命令）上执行高危动作，只需在调用接口时将 `device` 声明为策略宽松的节点 B（策略为 `High`），或者置空使其 fallback 到 `s.LocalName`，即可成功骗过 Hub 的策略判定，造成**三级风控彻底被穿透与权限降级逃逸**。
- **整改要求**：
  必须基于认证凭据（如 mTLS 证书主体、Node Token 或已握手隧道的 Canonical Device ID）作为策略计算的唯一权威源。

---

### A2. 【架构旁路后门】MCP 适配器零门禁直通集群
- **违反规范**：Spec 7.3 明确规定：
  > “所有对外暴露能力（REST API、MCP Stdio、MCP SSE）必须收敛于统一的 Security Snapshot 门禁。”
- **代码位置**：`internal/api/mcp_adapter.go` 与 `internal/mcp/mcp.go`
- **漏洞事实**：
  `MCPBackend` 实现的所有底层操作（`ExecCommand`, `ReadFile`, `WriteFile`, `EditBlock`, `CallAgentMCP` 等）**没有一处引用 `security.Snapshot` 或调用 `requireSecurity`**，直接裸调集群 Manager 执行。
- **真实危害**：
  `/mcp/sse` 和 `vps-commander-mcp-stdio` 是 AI Agent 实际高频使用的核心入口。当前实现相当于**REST 侧设了关卡，但 AI 的主通道直接留了后门**。持合法 MCP 凭证即可任意下发 High 级命令与文件覆写，安全门禁完全失效。
- **整改要求**：
  在 `mcp_adapter.go` 每一项 Tool 处理入口前，必须强制注入 `s.server.Policy.EffectiveRisk` 校验，未通过直接返回 Tool Error。

---

### A3. 【进程治理漏洞】`force_terminate` / `kill_process` 缺失进程树归属校验
- **违反规范**：Spec 8.2 明确规定：
  > “`kill_process` 必须基于 Session/PGID 树状绑定，禁止盲信裸 PID。不得终止非当前 Agent 创建或非目标 Session 所属的系统进程。”
- **代码位置**：`internal/executor/session.go`、`internal/cluster/protocol.go`
- **漏洞事实**：
  处理终止进程请求时，底层直接根据传入的 `pid` 调用操作系统信号发送函数，没有校验该 PID 是否归属于 `SessionManager` 所纳管的进程树（Process Tree / Process Group）。
- **真实危害**：
  在 Linux 高并发场景下存在 **PID 重用（PID Recycling）**。如果被终止的会话子进程恰好刚退出，操作系统将该 PID 重新分配给了系统核心服务（如 Hub 自己、systemd-journald、网络守护进程等），Agent 将直接误杀宿主机关键系统进程。
- **整改要求**：
  必须以 `SessionID` 为主键查找 `*Session` 对象，从内部持有的 `cmd.Process` 提取句柄并执行 Kill；拒绝接受客户端直传任意孤立 PID。

---

## 二、生产级严重隐患（生产必现崩溃、失联与数据破坏）

### A4. 【并发 Panic / 进程崩溃】Agent WebSocket 并发写入缺失互斥保护
- **代码位置**：`cmd/agent/main.go`
  ```go
  writeJSON := func(msg cluster.Message) error {
      return c.WriteJSON(msg) // 💥 裸写 WebSocket 连接，无任何锁保护！
  }
  ...
  go func(msg cluster.Message) {
      handle(...) // 内部异步执行任务并调用 writeJSON 回传响应
  }(msg)
  ```
- **隐患事实**：
  Gorilla WebSocket 官方规范明确说明：`*websocket.Conn` **不支持并发写入**。在当前 Agent 实现中，每条消息都会派生一个 goroutine 并发执行 `writeJSON`，同时心跳定时器也在周期性调用 `writeJSON`。
- **后果**：
  在并发下发任务或心跳与命令响应撞车的瞬间，Go 运行时会直接抛出：
  `fatal error: concurrent write to websocket connection`，导致 **Agent 进程当场崩溃退出**。
- **整改要求**：
  必须对 Agent 的 `*websocket.Conn` 封装 `sync.Mutex`，确保所有 JSON 写回与心跳握手互斥串行化。

---

### A5. 【生产自杀失联】Agent 常规任务报错自杀式断开隧道
- **代码位置**：`cmd/agent/main.go`
  ```go
  if err := handle(msg); err != nil {
      log.Printf("handle error: %v", err)
      c.Close() // 💥 业务层报错直接杀掉物理 WebSocket 连接！
      return
  }
  ```
- **隐患事实**：
  当用户执行一个不存在的命令（如 `exec_command("foo_bar")` 导致 `exec: "foo_bar": executable file not found in $PATH`）、读取权限不足的文件或执行超时，`handle` 返回常规错误。Agent 竟然直接执行了 `c.Close()` 撕毁长连接隧道。
- **后果**：
  一次轻微的业务操作失误或命令打错，直接导致 Agent 离线失联，Hub 显示该节点掉线，必须等待重连甚至人工干预。
- **整改要求**：
  移除 `c.Close()`。业务错误必须包装为 `cluster.Message{Status: "error", Error: err.Error()}` 原路回传给 Hub，保持底层物理连接健康存活。

---

### A6. 【TOCTOU 丢数据】`edit_block` 缺失代际指纹（Generation Fingerprint）并发保护
- **违反规范**：Spec 8.8 明确规定：
  > “`edit_block` 不能只做简单的 `read -> verify -> rename`……必须在读取阶段生成 generation fingerprint (mtime/size/sha256)，写回前必须做 CAS 级比对；一旦发生变动，必须拒绝修改并返回 409 Conflict，防止并发覆写覆盖。”
- **代码位置**：`internal/executor/edit.go`
  ```go
  data, err := os.ReadFile(path)
  ...
  count := countNonOverlapping(src, oldText)
  if count != 1 { return ... }
  newContent := strings.Replace(src, oldText, newText, 1)
  // 直接写写入临时文件并通过 os.Rename 强制覆盖！
  ```
- **隐患事实**：
  `edit_block` 仅在内存中做了非重叠计数校验，完全缺失目标文件的版本比对机制（如 `expected_hash` 或 `mtime`）。
- **后果**：
  在多会话或多 Agent 并发场景下，A 和 B 几乎同时读取同一配置文件。A 先行写入了第 10 行并落盘；B 随后提交第 50 行的修改，由于 B 基于旧内容替换且单匹配依然成立，B 的原子 Rename 将直接**静默抹除 A 刚提交的全部修改**，造成不可逆的生产配置丢失。
- **整改要求**：
  接口必须支持并强制校验 `expected_hash`，写回落盘前再次哈希比对，哈希不符直接阻断并返回冲突错误。

---

## 三、高优先级架构缺陷（Phase 3 前必须收敛）

### A7. 【审计链断裂】策略版本与 `audit_logs` 落库脱节
- **代码位置**：`internal/api/api.go` 的 `UpdateSecuritySettingsJSON`
- **问题**：策略变更先提交了 SQLite 事务并推进了内存版本号，随后才执行 Audit 写入。若两步之间服务异常断电或重启，会导致**版本号已变动，但审计表中无任何迹象与操作人**。审计日志必须在同一事务内完成提交。

### A8. 【资源泄漏】Session 清理机制单轨制
- **代码位置**：`internal/executor/session_manager.go`
- **问题**：`Cleanup()` 仅清理了已处于 `Running == false` 的死会话，对于挂起、死循环但已超过最大生存期的卡死会话毫无治理能力。必须落地 Spec 要求的「空闲超时 + 最大绝对生命期（Hard TTL）」双轨强制收割机制，并调用 `cmd.Wait()` 彻底回收僵尸进程。

---

## 四、值得肯定的实现亮点

1. **能力握手规范已落地**：`cluster.Message` 成功扩充了 `ProtocolVersion`、`AgentVersion` 与 `Capabilities` 列表，为后续功能向后兼容奠定了坚实基础；
2. **Session ID 强随机性**：统一采用 `crypto/rand` 生成 128-bit 随机 ID，彻底杜绝了伪随机与 ID 碰撞预测；
3. **文件写入原子性良好**：`edit_block` 与 `write_file` 均规范采用了临时文件写入、`Sync()` 刷盘及 `os.Rename` 替换机制。

---

## 五、专家 A 评审总结与准入判定

当前工作区代码虽然跑通了单元测试（`go test ./...` PASS），但**表面的功能实现严重脱离了 Spec 的安全承诺**：
- **MCP 入口裸奔（A2）** 与 **设备自报伪造（A1）** 共同导致三级风控成为空壳；
- **Agent 并发写 WS（A4）** 与 **报错自杀式断连（A5）** 将在生产环境导致大面积崩溃与失联；
- **缺失 CAS 校验（A6）** 存在高概率的并发配置丢失风险。

**准入结论**：**不予准入上线。必须依照上述清单完成 A1~A6 的靶向修复并通过覆盖性验收测试后，方可推进合并。**
