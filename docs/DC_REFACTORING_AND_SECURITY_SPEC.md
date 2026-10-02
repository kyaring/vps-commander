# 《VPS-Commander 平滑升级改造与三级安全拦截实施方案》

## 1. 改造目标与背景
本项目计划全量复刻 **Desktop Commander（DC）** 在终端与文件系统上的核心工程能力（包括局部块级精准替换、后台长进程生命周期管理、交互式 stdin 注入、结构化目录遍历与只读检索），并将其无缝接入 **Low（低风险）/ Medium（中风险）/ High（高风险）** 三级安全门禁体系。

**核心红线与验收标准：**
1. **服务不中断**：升级过程不影响正在运行的 Agent 探针与 Hub 中心。
2. **协议向后兼容**：旧版 Agent 与新版 Hub 混合共存，老接口绝不废弃。
3. **性能零损耗**：杜绝动态运行时映射，安全校验在编译期即已绑定。
4. **轻量化保真**：继续保持纯 Go 静态编译的高效特性，严控内存占用（常驻内存锁定在 20MB 以内）。

---

## 2. 工具池全量复刻与三级风险定级表（静态强绑定）

所有功能根据系统破坏力与副作用大小，在**编译期**严格归属于三级风险体系：

| 风险等级 (Level) | 行为边界定义 | 包含的复刻工具 (DC 标杆) | 允许放行的安全模式 |
| :--- | :--- | :--- | :--- |
| 🟢 **Low（只读/感知）** | **纯只读、无状态改变、无系统副作用** | • `read_file` (支持切片与 Tail)<br>• `read_multiple_files`<br>• `list_directory` (结构化目录树)<br>• `get_file_info` (元数据)<br>• `start_search` / `get_more_search_results`<br>• `list_processes` / `list_sessions`<br>• `read_process_output` (拉取输出) | **Low**<br>**Medium**<br>**High** |
| 🟡 **Medium（文件写）** | **文件与目录变更，但严禁拉起任意二进制或执行命令** | • `edit_block` (局部文本块精准替换，核心)<br>• `write_file` (文件新建/全量写入)<br>• `create_directory` (新建目录)<br>• `move_file` (移动/重命名) | **Medium**<br>**High**<br>*(Low 拦截)* |
| 🔴 **High（完全控制）** | **具备操作系统执行权、进程生命周期调度与管道干预** | • `exec_command` (同步短命令)<br>• `start_process` (后台异步启动长进程)<br>• `interact_with_process` (stdin 交互写入)<br>• `kill_process` / `force_terminate` (终止进程) | **High**<br>*(Low / Medium 拦截)* |

---

## 3. 架构改造与平滑过渡设计

### 3.1 协议层平滑升级（零破坏向后兼容）
在 `internal/cluster/protocol.go` 中，保留原有字段，新增可选的扩展动作：
```go
type Message struct {
    ID        string `json:"id,omitempty"`
    Action    string `json:"action,omitempty"`     // 保持原有字段: "read" / "write" / "exec" / "probe"
    SubAction string `json:"sub_action,omitempty"` // ✨ 增量动作: "edit_block", "start_process" 等
    Device    string `json:"device,omitempty"`
    Payload   any    `json:"payload,omitempty"`
    ...
}
```
* **兼容保障**：老版本 Agent 接收到带 `SubAction` 的 JSON 消息时会自动忽略多余字段，继续按老逻辑工作；升级后的 Agent 优先路由给 `SubAction` 处理。

### 3.2 Hub 门禁与高并发性能优化（读写分离）
现有系统每次请求均去 SQLite 查询策略，高并发下存在文件锁瓶颈。改造后引入 **RWMutex 内存策略快照**：
1. **读路径（Fast Path，纳秒级）**：
   ```go
   // 常量数值定义，直接内联比较
   const ( RiskLow = 1; RiskMedium = 2; RiskHigh = 3 )

   func (s *Server) requireSecurity(w http.ResponseWriter, r *http.Request, device string, requiredRisk int, detail string) bool {
       allowed := s.getEffectiveRiskLevel(device) // 查内存 RLock，耗时 < 10ns
       if allowed >= requiredRisk {
           return true
       }
       // 记录 audit_logs ("security_denied", 403) 并返回拦截响应
       return false
   }
   ```
2. **写路径（Slow Path，低频）**：
   * 仅在管理员通过 Web 界面更改安全策略时，写一次 SQLite 并原子更新内存缓存。
3. **老 API 零改动**：
   * 原有 `/api/v1/devices/exec`、`/read`、`/write` 继续保留，老脚本老客户端零感知。

### 3.3 Agent 端核心引擎实现（防资源膨胀设计）
* **`edit_block` 引擎**：
  * 读取原文件后校验 `old_text` 全局唯一性，唯一时单次替换并使用临时文件进行原子覆写（`os.Rename`）。
  * 相比原有全量上传，网络传输和内存开销下降 95%。
* **长进程与 PTY 会话引擎（`start_process` 等）**：
  * Agent 内部建立轻量 `SessionManager`，挂接内核标准输入输出管道。
  * **硬编码内存锁（Circular RingBuffer）**：每个进程会话分配最大 2MB 环形日志缓冲区，达到上限自动覆盖最早输出，**从物理上杜绝日志死循环导致的内存溢出（OOM）**。

---

## 4. 资源开销评估与性能上限控制

得益于纯 Go 原生静态编译，系统坚决不引入任何 Node.js/Python 运行时，资源开销保持极致克制：

| 评估指标 | 改造前基准（实测） | 改造后预期 | 性能/资源防护机制 |
| :--- | :--- | :--- | :--- |
| **Agent 静默内存** | ~13 MB | **~15 MB - 18 MB** | 零后台轮询，事件驱动 |
| **Agent 峰值内存** | 随命令大小波动 | **基准 + (2MB × 活跃会话数)** | 环形缓冲区硬顶锁定，防内存泄露 |
| **Hub 服务端内存** | ~16 MB | **~18 MB - 22 MB** | 内存门禁仅占不到 10KB |
| **二进制体积** | ~9.4 MB | **~11 MB - 13 MB** | 静态单文件编译，极易分发部署 |
| **安全判定延迟** | 毫秒级（查 SQLite） | **纳秒级（内存读锁）** | 单机可轻松支撑 10W+ QPS 门禁并发 |

---

## 5. 四阶段实施与回滚路线图（稳健落地）

* **Phase 1：服务端门禁升级与内存读缓存**
  * 在 Hub 端落地 `RWMutex` 安全策略缓存；
  * API 入口统一接入 `requireSecurity(..., RiskLevel)`；
  * *验证标准*：旧版 4 项基础功能与 Web 面板完好，并发查询 QPS 大幅提升。
* **Phase 2：落地中风险神器 `edit_block`**
  * Agent 端实现块级精准替换算法；
  * Hub 暴露 `/api/v1/devices/file/edit_block` 并注册至 MCP Adapter；
  * *验证标准*：在 Medium 模式下可正常修配置文件，在 Low 模式下被精确阻断并记入审计。
* **Phase 3：落地高风险长进程引擎**
  * Agent 端引入带 RingBuffer 的 `SessionManager`；
  * 实现 `start_process`、`read_process_output`、`interact_with_process`、`kill_process`；
  * *验证标准*：跑耗时任务不会超时断连，支持实时 Tail 查看输出与注入 stdin。
* **Phase 4：落地只读结构化文件与系统感知（Low）**
  * 实现 `list_directory`、`get_file_info` 及后台异步只读搜索。

---

## 6. 评估与审计重点建议
1. **安全维度**：Low/Medium/High 对能力的切分是否彻底杜绝了提权与逃逸风险？
2. **性能维度**：使用内存 RWMutex + 编译期静态安全级别判定，是否彻底解决了单文件 SQLite 高频读瓶颈？
3. **稳定性维度**：为每个长进程会话限定 RingBuffer 内存上限，是否有效防止了边缘 VPS 探针被日志撑爆？

---

# 7. 架构收敛修订（2026-10-02）

> 本章节在进入代码实施前增加，用于两位外部 Agent 审核。原方案的工具分级、四阶段路线和向后兼容原则继续保留；本章节对安全权威、策略一致性、工具注册以及高风险 Session 生命周期作进一步约束。

## 7.1 安全等级的正确边界：静态工具风险 + 运行时设备策略

原方案中“静态强绑定”应准确解释为：**工具所需风险等级在编译期固定，是否允许执行在运行时由 Hub 的权威策略决定。**

不得将设备当前 Low/Medium/High 策略编译进 Agent，也不得允许 Agent 自行提升自身安全等级。

### 7.1.1 工具风险等级

每个工具在代码注册表中拥有不可运行时修改的 `RequiredRisk`：

```go
const (
    RiskLow    = 1
    RiskMedium = 2
    RiskHigh   = 3
)

type ToolSpec struct {
    Name        string
    RequiredRisk int
    ReadOnly    bool
}
```

示例：

```text
read_file / list_directory / search     -> Low
edit_block / write_file / move_file     -> Medium
exec_command / start_process / stdin    -> High
```

禁止通过 Web 面板、Agent 配置文件或远端请求动态修改工具自身的 `RequiredRisk`。

### 7.1.2 设备有效策略

设备的 `EffectiveRisk` 是 Hub 运行时策略，不属于 Agent 自主能力。

请求授权模型：

```text
Request
  -> identify device
  -> identify ToolSpec.RequiredRisk
  -> read Hub PolicySnapshot
  -> compare EffectiveRisk >= RequiredRisk
  -> allow / deny
```

因此完整安全边界为：

**编译期固定“工具需要什么权限” + 运行时决定“该设备当前获得什么权限”。**

---

## 7.2 Policy Store 与 Policy Snapshot 一致性模型

SQLite 继续作为持久化权威源（durable source of truth），但请求热路径禁止每次查询 SQLite。

### 7.2.1 数据流

```text
Admin Web
   |
   v
SQLite transaction
   |
   v
Build new immutable PolicySnapshot
   |
   v
Atomic publish / swap
   |
   v
Request fast path
```

请求只读取当前不可变 snapshot；不在请求过程中修改策略对象。

### 7.2.2 更新原则

策略修改必须满足：

1. SQLite 写入成功后才允许发布新 snapshot。
2. SQLite 写失败时，内存策略保持旧版本。
3. snapshot 发布必须是原子操作，不允许出现半更新状态。
4. Hub 启动时必须从 SQLite 完整重建 snapshot。
5. snapshot 应携带单调递增 `Version`，便于审计和诊断。
6. 审计记录应包含 `device`, `oldRisk`, `newRisk`, `version`, `actor`, `timestamp`。

推荐结构：

```go
type PolicySnapshot struct {
    Version  uint64
    Devices  map[string]DevicePolicy
    CreatedAt time.Time
}
```

热路径原则：

```text
load current snapshot
    -> lookup device
    -> compare integer risk
    -> allow/deny
```

不得在高频 API 请求中重新读取 SQLite 策略表。

### 7.2.3 故障安全

如果 Hub 无法加载有效策略 snapshot：

- 不得默认放行 High。
- 不得从请求参数接受临时风险等级。
- 对需要 Medium/High 的操作默认拒绝。
- Low 只读能力是否可继续开放，必须由启动时的明确安全默认策略决定，并记录审计事件。

---

## 7.3 Tool Registry：统一能力与风险注册表

避免各 API handler 自己实现不同的安全判断。所有能力必须进入统一 Tool Registry。

建议：

```go
type ToolSpec struct {
    Name         string
    RequiredRisk int
    Category     string
    ReadOnly     bool
    Description  string
}
```

例如：

```text
read_file              Low
read_multiple_files    Low
list_directory         Low
get_file_info          Low
start_search           Low
get_more_search_results Low
list_processes         Low
list_sessions          Low
read_process_output    Low

edit_block             Medium
write_file             Medium
create_directory       Medium
move_file              Medium

exec_command           High
start_process          High
interact_with_process  High
kill_process           High
force_terminate        High
```

API、MCP Adapter 和内部调用均通过 Registry 获取 `RequiredRisk`，不得在多个入口复制风险数字。

---

## 7.4 Agent 信任边界

Agent 是执行者，不是安全策略的最终裁决者。

Hub 是设备授权策略的权威来源。

但 Agent 仍必须执行本地最低限度安全校验，包括：

- 请求格式与参数校验；
- session 所有权校验；
- session 是否仍然有效；
- 单次输入/输出大小限制；
- 超时和资源限制；
- 不接受客户端自行声明的 `RiskLevel` 作为授权依据。

Agent 不得因为请求中携带：

```json
{"risk":"high"}
```

或类似字段而自行获得 High 权限。

如果协议未来需要携带授权信息，应使用 Hub 生成的受保护授权上下文，并绑定：

```text
device + request/session + policy version + expiry
```

具体签名/令牌方案在实现阶段单独评审。

---

## 7.5 High Risk SessionManager 安全模型

`start_process`、`interact_with_process`、`kill_process` 不应被视为普通 HTTP 命令，而应视为一个受生命周期管理的高风险 Session。

### 7.5.1 Session 属性

每个 Session 至少包含：

```go
type ProcessSession struct {
    ID          string
    Owner       string
    Device      string
    PID         int
    CreatedAt   time.Time
    LastAccess  time.Time
    ExpiresAt   time.Time
    State       string
    OutputBytes uint64
}
```

Session ID 必须不可预测，并且不能复用操作系统 PID 作为唯一身份。

### 7.5.2 所有权

只有创建 Session 的授权调用方才能：

- 读取该 Session 输出；
- 向 stdin 写入数据；
- 终止该 Session。

管理员级操作如果未来需要跨 Owner 接管 Session，应定义明确的审计事件和单独权限，不得通过普通 session API 隐式获得。

### 7.5.3 生命周期

```text
start_process
      |
      v
   RUNNING
    /    \
   /      \
EXITED   EXPIRED
   |        |
   +----+---+
        v
     CLEANUP
```

Agent 重启后所有旧 Session 必须失效，不得从持久化文件自动恢复可写 stdin 的旧 Session。

### 7.5.4 时间限制

Session 必须具备：

- 最大生命周期；
- 空闲超时；
- stdin 单次大小限制；
- stdin 速率限制；
- 输出缓冲区上限。

具体默认数值在实现前通过压测确定，不在设计阶段硬编码未经验证的数值。

---

## 7.6 RingBuffer 与全局资源上限

原方案中的“2MB × 活跃 Session”只能作为单 Session 上限，不应视为 Agent 的总内存预算。

必须同时存在：

```text
per-session output cap
+
max active sessions
+
agent-wide session buffer cap
+
process creation rate limit
```

例如：

```text
单 Session：最大 2MB output buffer
全局：最大 N 个活跃 Session
全局：最大 M MB Session buffer
```

当达到任意全局上限时，新 Session 必须被拒绝并记录审计，而不是继续消耗内存。

RingBuffer 的覆盖行为只能覆盖旧输出，不得覆盖 session 元数据、错误状态或安全审计记录。

---

## 7.7 审计要求

至少记录以下安全事件：

```text
security_denied
policy_changed
session_created
session_expired
session_killed
session_owner_denied
resource_limit_denied
```

High Risk 操作的审计信息必须包含：

```text
device
actor / owner
request id
session id（如适用）
tool
result
policy version
timestamp
```

审计日志不能因为 Session RingBuffer 达到上限而被覆盖。

---

## 7.8 协议兼容补充约束

旧 Agent 与新 Hub 混合运行期间：

1. 原有 `action=read/write/exec/probe` 字段继续有效。
2. 新 `SubAction` 为可选字段。
3. 新字段不能改变旧 Agent 对旧消息的既有语义。
4. Hub 必须根据 Agent capability/version 判断是否可以下发新 SubAction。
5. 对不支持新能力的 Agent，返回明确的 capability error，而不是让旧 Agent 猜测。
6. 安全门禁必须在能力路由之前执行，避免通过旧 API 绕过新 Tool Registry。

---

## 7.9 实施前必须完成的设计验收

在进入 Phase 1 代码改造前，两位审核 Agent 应重点审查：

### A. 权威性

- Hub 是否始终是 EffectiveRisk 的唯一权威来源？
- Agent 是否存在任何自升权路径？
- 旧 API 是否可能绕过 Tool Registry？

### B. 一致性

- SQLite commit 与 snapshot publish 顺序是否正确？
- Hub 重启能否确定性恢复策略？
- snapshot version 是否足以定位策略变更？

### C. Session 安全

- session ownership 是否不可伪造？
- Agent 重启后旧 session 是否必然失效？
- stdin、stdout、session 数量是否存在全局资源上限？

### D. 向后兼容

- 老 Agent + 新 Hub 是否继续工作？
- 新 Hub 是否能识别 Agent capability？
- 新能力是否可能通过旧接口绕过门禁？

### E. 性能

- 热路径是否完全脱离 SQLite？
- snapshot 是否真正 immutable / atomic？
- session ring buffer 是否存在隐藏的额外复制？

**只有上述问题通过审核后，才进入 Phase 1 实现。**

---

# 8. 审核意见收敛修订（2026-10-02）

本章节根据第一轮 Agent 审核结果补充。**Phase 1 在本章节阻断项完成前不得进入代码实施。**

## 8.1 阻断项一：设备身份认证

`identify device` 不得等同于读取请求中的 `device` 字段。设备名称只能作为路由提示，不能作为授权依据。

### 8.1.1 权威身份模型

每个 Agent 必须拥有唯一设备身份以及与之绑定的设备凭证。Hub 必须先完成设备认证，再建立：

```text
authenticated agent identity
        ↓
device ID
        ↓
PolicySnapshot lookup
        ↓
EffectiveRisk
```

推荐优先采用 **per-agent credential**；部署条件允许时使用 mTLS。若采用 Agent Token，Token 必须：

- 与单一 Agent 身份绑定；
- 可撤销；
- 不得由客户端自行指定 device 后复用；
- 传输必须经过受保护通道；
- 认证失败不得进入安全策略查询阶段。

`device` 请求字段不得覆盖认证得到的 canonical device identity。

### 8.1.2 身份与策略绑定

策略查询必须使用认证层产生的 canonical device ID：

```go
identity := authenticateAgent(r)
if !identity.OK {
    deny()
}
policy := snapshot.Device(identity.DeviceID)
```

禁止：

```go
policy := snapshot.Device(r.Device)
```

设备注册、凭证轮换、凭证撤销必须产生审计事件。具体凭证格式与密钥轮换流程在 Phase 1 实施设计中单独冻结。

---

## 8.2 阻断项二：kill_process 作用域

`kill_process` / `force_terminate` **绝不能接受任意 PID 作为授权对象**。

High Risk Session 必须建立进程归属关系：

```text
Session
  └── Process Group / Process Tree
        ├── root process
        ├── child
        └── child...
```

默认允许终止的对象仅为该 Session 创建的进程树。

必须拒绝：

- 不属于当前 Session 的 PID；
- Hub / Agent 自身关键进程；
- 未经单独授权的其他用户进程；
- 通过伪造 PID、session ID 或 owner 字段进行的跨 Session 操作。

实现时应记录 root PID / process group ID，并通过 OS 级关系验证目标进程仍属于该 Session，而不是只相信客户端提供的 PID。

未来若需要管理员跨 Session 终止能力，必须作为独立能力和独立审计事件设计，不复用普通 `kill_process`。

---

## 8.3 阻断项三：Agent capability/version 握手

协议必须正式增加 capability/version 元数据，使 7.8 的混合版本策略可以实际落地。

建议扩展：

```go
type Message struct {
    ID         string `json:"id,omitempty"`
    Action     string `json:"action,omitempty"`
    SubAction  string `json:"sub_action,omitempty"`
    Device     string `json:"device,omitempty"`
    Payload    any    `json:"payload,omitempty"`

    ProtocolVersion string   `json:"protocol_version,omitempty"`
    AgentVersion    string   `json:"agent_version,omitempty"`
    Capabilities    []string `json:"capabilities,omitempty"`
}
```

为避免旧 Agent 解析失败，新增字段必须保持 optional；旧 Agent 忽略未知字段即可继续处理旧消息。

更严格的 capability 协商流程：

```text
Agent connect
    ↓
identity authentication
    ↓
protocol/version handshake
    ↓
capability registry
    ↓
Hub records effective capabilities
    ↓
request routing
```

Hub 不得仅根据版本字符串猜测能力。实际可用能力以 capability set 为准。

如果 Agent 不支持某个 `SubAction`：

```text
capability_missing
```

必须成为明确错误，不得静默降级到语义不同的危险操作。

---

## 8.4 威胁模型

三级安全体系主要解决的是 **调用方对受控设备的越权操作风险**，而不是保证已经被完全攻陷的 Agent 本身可信。

### T1：恶意调用方

目标：冒充设备、伪造风险等级、跨设备操作、跨 Session 操作。

防护：

- Agent 身份认证；
- canonical device ID；
- Hub authoritative PolicySnapshot；
- Session ownership；
- Tool Registry；
- 审计。

### T2：有 Bug 的调用方

目标：错误调用 High API、重复创建 Session、资源耗尽。

防护：

- 参数验证；
- per-device 并发限制；
- rate limit；
- Session/global resource cap；
- fail-closed。

### T3：被攻陷的 Agent

如果攻击者已经取得 Agent 进程的等价本地执行权，则 Agent 内部校验不能被视为可信边界。

因此：

> Hub 门禁保护的是“未经授权的远程调用不能获得设备能力”，而不是“Agent 被完全攻陷后仍能阻止本地攻击者”。

Agent 被攻陷属于独立威胁域，应依赖最小权限、系统账户隔离、凭证撤销、节点隔离和重新注册等机制处理。

文档不得把 Agent 侧校验描述为能够抵御完全控制 Agent 的攻击者。

---

## 8.5 审计敏感数据脱敏

现有 `audit_logs.command` 等字段不得无条件记录完整命令或参数。

审计系统必须采用结构化事件优先：

```text
command template / tool
arguments metadata
result
risk
actor
request id
```

对于可能包含 Secret 的字段，必须在进入持久化审计前脱敏，包括但不限于：

```text
password
passwd
token
access_token
refresh_token
api_key
secret
authorization
cookie
private_key
```

Shell 命令中的敏感值不能仅依赖字段名识别；实现阶段应定义命令审计策略，至少做到：

1. 不默认记录完整 stdin；
2. 对已识别敏感参数替换为 `[REDACTED]`；
3. Secret 不得通过错误消息重新写入 audit；
4. 原始命令仅在明确安全策略允许时保留。

---

## 8.6 回滚与混合版本路线

原第 5 节标题为“回滚路线图”，现补充实际策略。

### 8.6.1 Hub 升级失败

- 新版本不得在旧 schema 尚未兼容时直接删除旧字段；
- DB migration 必须可检测版本；
- migration 失败则 Hub 不进入正常服务状态；
- 策略 snapshot 可以从旧兼容 schema 重建；
- 升级前保留 DB 备份/快照。

### 8.6.2 新 Hub + 旧 Agent

旧 Agent 继续使用旧 API/旧 Action。

Hub 必须：

```text
认证旧 Agent
   ↓
读取其 capability
   ↓
只下发它声明支持的能力
```

不能因为 Hub 已升级就要求旧 Agent 自动支持新 SubAction。

### 8.6.3 新 Agent + 旧 Hub

新 Agent 必须保持旧协议兼容能力；若 Hub 不支持 capability handshake，新 Agent 使用 legacy compatibility mode，不得因此自行启用 High 新能力。

### 8.6.4 数据降级原则

数据库 schema 新增字段应尽量可选；新功能数据不能成为旧版本启动的硬依赖。

任何不可逆 migration 必须在正式 Phase 前单独评审。

---

## 8.7 Session ID 与进程安全参数

Session ID 必须使用 `crypto/rand` 生成，最低 **128-bit entropy**，并以不暴露内部 PID 的方式编码。

PID 仅作为内部 process identity 的组成部分，不作为 session credential。

Session authorization 必须同时验证：

```text
authenticated device
+ session ID
+ owner
+ session state
+ expiry
```

---

## 8.8 edit_block 的 TOCTOU 防护

`edit_block` 不能只做：

```text
read -> verify -> rename
```

因为读取后到替换前存在 TOCTOU 窗口。

实现至少需要以下一种一致性保护：

- 文件锁；或
- generation / mtime / size / hash 校验后再提交；或
- OS 支持的原子条件更新机制。

推荐：读取时计算 generation fingerprint，写回前重新验证；若文件已经发生变化则拒绝修改并返回 conflict，而不是覆盖第三方更新。

---

## 8.9 内存预算重新约束

原方案“Agent 常驻 20MB 以内”与 `2MB × N sessions` 必须统一解释。

新的约束是：

> **20MB 是 Agent 基础常驻预算；Session buffer 必须从剩余预算反推最大并发数，而不是无限叠加。**

因此实现前必须定义：

```text
BaseMemoryBudget
SessionBufferBudget
MaxActiveSessions
PerSessionBuffer
```

满足：

```text
BaseMemory + MaxSessionBufferBudget + safety margin
    <= approved Agent memory ceiling
```

如果单 Session 仍采用 2MB，则 `MaxActiveSessions` 必须由压测和实际内存预算计算得出。

---

## 8.10 性能指标修正

删除或修正“整机 10W+ QPS”可能造成的误导。

准确表述应为：

> **安全门禁自身的策略读取路径不以 SQLite 为热路径，单次判定成本应保持在内存级别；端到端 API 吞吐仍受网络、Agent、磁盘、进程创建和实际 IO 限制。**

性能验收应分别测量：

1. PolicySnapshot lookup latency；
2. authorization decision latency；
3. API end-to-end latency；
4. Agent execution throughput；
5. process/session creation rate。

---

## 8.11 进程交互传输模型

`interact_with_process` 的实时 stdin/stdout 通道必须在实现前冻结。

设计目标：

```text
Hub / MCP
   ↓ stdin
Agent Session
   ↑ stdout/stderr stream
```

传输协议必须明确：

- session binding；
- reconnect 行为；
- backpressure；
- output cursor / offset；
- heartbeat / timeout；
- disconnect 后 Session 是否继续运行；
- reconnect 后 owner 如何重新认证。

如果采用 HTTP/SSE、WebSocket 或轮询，必须在 Phase 3 实施设计中明确其安全和资源模型，不能由 handler 临时决定。

---

## 8.12 非 Session 类资源限制

全局资源控制不仅适用于 `start_process`。

### exec_command

必须增加：

- per-device concurrent exec limit；
- 单命令执行时间上限；
- 输出大小上限；
- 创建速率限制。

防止通过大量同步 `exec_command` 造成 fork/process exhaustion。

### list_processes

进程命令行可能包含密码、Token、Cookie 等敏感信息。返回结果必须执行敏感信息处理，并限制返回规模。

### start_search / get_more_search_results

后台搜索属于全盘 IO 风险，应具备：

- per-device search concurrency；
- 最大搜索范围/深度；
- 最大结果数；
- 超时；
- 可取消任务；
- 全局 IO 预算。

---

## 8.13 Phase 1 红队验收

除原 A–E 正向验收外，Phase 1 必须增加以下对抗用例：

1. 伪造 `{"risk":"high"}` 是否被拒绝；
2. 使用合法 Agent A 凭证请求 device B 是否被拒绝；
3. 旧 API 是否能绕过 Tool Registry；
4. Session A 的 ID 是否能访问 Session B；
5. Session A 是否能 kill 非自身进程；
6. Agent 重启后旧 Session 是否失效；
7. Policy snapshot 更新过程中是否出现半更新状态；
8. SQLite 更新失败时是否错误发布新策略；
9. 旧 Agent + 新 Hub 是否保持旧功能；
10. 新能力是否在 capability 缺失时被错误下发；
11. audit 是否泄露 password/token/cookie/private key；
12. 并发 exec 是否能够超过 per-device 上限；
13. search 是否能够无限占用 IO；
14. RingBuffer / session 上限是否能防止内存线性增长。

---

## 8.14 Phase 1 开工闸门

Phase 1 只有在以下条件全部满足后才能开始：

```text
[ ] Agent identity authentication model frozen
[ ] Canonical device identity frozen
[ ] kill_process process-tree scope frozen
[ ] protocol capability/version handshake frozen
[ ] Threat model accepted
[ ] Audit redaction policy accepted
[ ] DB migration / rollback strategy accepted
[ ] Session ID generation frozen
[ ] edit_block consistency strategy frozen
[ ] Agent memory budget reconciled
[ ] Streaming transport model frozen or explicitly deferred to Phase 3
[ ] exec/search resource limits defined
[ ] Red-team test plan accepted
```

**本章节是 Phase 1 的设计闸门，不是可选建议。任何阻断项未关闭时，不得以“先实现再补安全”为理由进入代码改造。**
