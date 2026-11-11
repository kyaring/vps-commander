# 异步 Job 独立配额与交互槽位解耦设计方案

- **任务编号**：T-20261111-05
- **所属阶段**：阶段 3（长任务与交互槽位解耦，设计预研）
- **关联计划**：`docs/plan/concurrency-slot-remediation-plan.md`
- **关联查证**：`docs/tasks/T-20261111-06.md`、`docs/recovery/CONCURRENCY-PROBE-20261107.md`
- **状态**：定稿设计（只写设计，不引入代码变更）

---

## 一、背景与问题陈述

### 1.1 现状与痛点
在 VPS-Commander 当前架构中，Hub 对命令执行实施了基于令牌的并发控制（`tokenLimiter.AcquireExec`），默认并发上限为 2。该限制旨在保护 Hub 和受管 Agent 节点免受突发流量雪崩与连接耗尽。

然而，当前的并发槽位分配存在严重的**混部争用**问题：
1. **短耗时交互命令（Interactive Exec）**：例如探针诊断、文件检索、系统负载检查、单步运维命令，通常在毫秒至数秒内完成。
2. **长耗时长任务（Async Job / Background Session）**：通过 `exec_start` 或 MCP 启动的异步后台命令（如长时间全量构建、测试、日志追踪、`go test -race`），通常持续数十秒甚至数十分钟。
3. **槽位长期占用**：在现行实现中（`internal/api/exec_jobs.go:169` 与 `internal/api/exec_jobs_remote.go:57`），异步 job 在启动瞬间调用 `acquireTokenExec` 获取执行槽位，且该槽位**在整个 job 运行周期内一直被持有**，直到进程退出、会话结束或超时才被 `release()`。

### 1.2 故障模式
当某个令牌启动了 2 个后台异步任务时，该令牌的所有交互执行槽位被 100% 占满。此时：
- 客户端或 Agent 发起的任何只读诊断、短命令交互均被直接拦截并返回 HTTP 429（`concurrency_limited`）。
- AI 客户端在感知到 429 后通常会发起指数退避重试，在槽位长期被占满的情况下，重试只会进一步放大 Hub 负载和审计日志堆积。
- 本质上，**长任务吞噬了交互通道的可用性**，违反了“后台批处理不应阻塞前台交互”的基本可用性原则。

---

## 二、Job 配额的数据模型与架构

为了彻底隔离长异步任务与短交互命令，系统引入**双轨独立配额机制**：交互式命令使用 `concurrent_exec_limit`，异步 Job 使用独立的 `concurrent_job_limit`。

```
                    ┌───────────────────────────────────────────────┐
                    │               Client Request                  │
                    └───────┬───────────────────────────────┬───────┘
                            │                               │
                [POST /api/v1/exec]              [POST /api/v1/exec/start]
              (Interactive Exec Path)              (Async Job Path)
                            │                               │
                            ▼                               ▼
                 AcquireExec(tokenID)              AcquireJob(tokenID)
                            │                               │
                ┌───────────┴───────────┐       ┌───────────┴───────────┐
                │ concurrent_exec_limit │       │ concurrent_job_limit  │
                │ (Default: 2, live)    │       │ (Default: 4, live)    │
                └───────────────────────┘       └───────────────────────┘
                            │                               │
                            ▼                               ▼
                   Ephemeral Execution             Background Runner
                   (Releases on exit)              (Releases on job done)
```

### 2.1 存储层模型（Storage Schema）
在数据库 `api_tokens` 表中新增增量列：
```sql
ALTER TABLE api_tokens ADD COLUMN concurrent_job_limit INTEGER NOT NULL DEFAULT 0;
```
- **字段定义**：`concurrent_job_limit`（`INTEGER NOT NULL DEFAULT 0`）。
- **语义约束**：
  - `0`：表示继承系统全局默认值（由环境变量 `VPS_COMMANDER_TOKEN_JOB_CONCURRENCY` 控制，默认建议设为 `4`）。
  - `> 0`：表示该令牌专属的异步 job 最大并发运行数。
- **平滑兼容**：DDL 采用幂等检查方式（通过 `PRAGMA table_info(api_tokens)`），升级时不阻塞服务，旧版二进制或降级时不影响其他列的读取与解析。

### 2.2 内存计数器模型（In-Memory Limiter）
在 `internal/api/token_limiter.go` 中扩展 `tokenLimitState`：
```go
type tokenLimitState struct {
    // 交互式命令并发状态（阶段 2 已实现）
    execInFlight      int
    customExecLimit   int
    hasCustomExec     bool

    // 异步 Job 并发状态（阶段 3 引入）
    jobsInFlight      int
    customJobLimit    int
    hasCustomJob      bool
    runningJobs       map[string]time.Time // jobID -> startTime，用于死锁防范与泄漏诊断
}
```

配套方法：
- `AcquireJob(tokenID, jobID string, now time.Time) (release func(), allowed bool)`
- `SetTokenJobLimit(tokenID string, limit int)`
- `MaxConcurrentJobsForToken(tokenID string) int`
- `RunningJobsCount(tokenID string) int`

该计数器完全由 `tokenLimiter.mu` 保护，支持无缝热更新，不需要重启 Hub 进程。

---

## 三、提交 Job 后交互槽位的释放时机

解耦设计的核心是**明确划分“提交/握手阶段”与“长期运行阶段”**。

### 3.1 槽位流转与释放生命周期

| 阶段 | 动作 | 交互槽位（Exec Slot） | 异步 Job 槽位（Job Slot） | 说明 |
|---|---|---|---|---|
| **Phase 0: 准入判定** | 收到 `exec_start` 请求 | **不申请、不占用** | `AcquireJob(tokenID, jobID)`（`jobsInFlight++`） | 若 Job 槽位已满，立即返回 429 并记录 `job_concurrency_limited` |
| **Phase 1: 进程拉起** | Hub 向 Agent 发送启动命令并等待握手 | **不占用** | 持续持有 | 本地调用 `cmd.Start()` 或远端接收 `session_id`；远端另受每节点会话上限 4 保护 |
| **Phase 2: 握手成功** | 获得 PID / Session ID，确认 Job 进入 `running` 状态 | **不占用** | 持续持有 | 不存在交互槽位的临时持有或释放步骤 |
| **Phase 3: 运行期间** | Job 处于运行状态，输出进入环形缓冲 | **无占用（0）** | 持续持有（1） | 期间该令牌可自由执行交互式 `exec` 命令 |
| **Phase 4: 任务终态** | 进程退出 / 被 cancel / 节点离线超时标记 lost | **无占用（0）** | **完全释放（Release）** | 触发 `releaseJob()`，归还 Job 配额 |

### 3.2 关键代码路径改造细节
1. **本地 Job 启动路径 (`internal/api/exec_jobs.go`)**：
   - 现行逻辑：`release, ok := s.acquireTokenExec(p)`，并在退出时 `defer release()`。
   - 改版逻辑：
     ```go
     releaseJob, allowed := tokenLimiter.AcquireJob(p.TokenID, j.ID, time.Now())
     if !allowed {
         return execJob{}, fmt.Errorf("%w: token concurrent job limit reached", mcp.ErrRateLimited)
     }
     // exec_start 全程不申请、不占用交互式 exec 槽位；仅由 releaseJob 管理 Job 配额生命周期
     ```
2. **远端 Agent Job 启动路径 (`internal/api/exec_jobs_remote.go`)**：
   - 与本地路径一致，远端 `exec_start` 全程只持有独立 `AcquireJob(tokenID, jobID)` 配额，不申请临时交互槽位；远端节点另由每节点会话上限 4 保护。
   - 当任务在 `streamRemoteJob` 中完成、或由 `cancelRemote` 取消、或 Agent 断开连接导致超时被 `markLost` 时，确保在 `sync.Once` 内安全调用 `releaseJob()`。

---

## 四、与 T-06 查证结果的关系

### 4.1 T-06 的查证定位
任务 `T-20261111-06` 对 2026-11-07 07:40–08:10 期间 `claude-2` 的全部 exec 记录与 journal 429 进行了只读溯源比对，输出产物为 `docs/recovery/CONCURRENCY-PROBE-20261107.md`。

查证的核心问题是：**在历史事件中，429 是不是因为长耗时任务（如异步 job / 长命令）占住槽位导致的？**

### 4.2 查证结论对本设计的指导意义
T-06 对 `claude-2`（`actor_id=Br51PEdY7TbD4CWE`）的实查结果如下：

- SQLite 窗口内有 20 条审计记录，全部为 `exec`；没有 `exec_start`。窗口前一小时也没有结束时间跨越 07:40:00 的 `exec` / `exec_start`。
- journal 窗口内有 28 条该身份 HTTP 请求，其中 6 条为 429：07:47:51.333、07:47:54.604、07:49:52.389、07:53:13.363、08:05:51.106、08:06:29.814。六条响应均为 `bytes=32`，`duration_ms` 分别为 1、1、1、1、3、1。
- 前两次 429（07:47:51.333、07:47:54.604）发生时，审计 ID 13378（07:46:41–07:47:55）与 ID 13379（07:47:48–07:48:51）两条 `exec` 同时处于执行区间内，时间上与令牌并发 exec 槽位已满相符。
- 后四次 429（07:49:52.389、07:53:13.363、08:05:51.106、08:06:29.814）均不落在任何已记录的活动 `exec` 区间内。journal 仅记录响应字节数而不记录响应体或 limiter 分类，因此不能仅凭 `bytes=32` 将六次 429 全部归因为并发拒绝；后四次的具体限流原因在现有证据下未能确定。

**设计含义**：本次 T-06 实证了目标窗口内存在两次与两条长耗时 `exec` 重叠的 429，说明长任务占用交互槽位确实可能造成并发拒绝；但它并未证明该窗口六次 429 全部由长任务占槽导致。独立 Job 配额的价值是让 `exec_start` 不再占用交互式 `exec` 配额，避免异步长任务挤占短交互命令的并发容量；它不替代请求速率限制，也不能解释本次窗口内其余四次 429。

**与 Dave-1 事件的边界**：2026-11-08 至 2026-11-10 Dave-1 的 67 次 429 属于不同 actor、不同日期区间的另一组事件。T-06 本次只查证 2026-11-07 的 `claude-2`，没有查询或验证 Dave-1 的 67 次记录；不得把本节 `claude-2` 的六次 429 当作 Dave-1 事件的证据，也不得把 Dave-1 事件的归因反向套用到本次 `claude-2` 数据。Dave-1 事件仍应以其自身的 SQLite 与 journal 原始证据独立判断。

---

## 五、对现有 exec_status、exec_cancel 及相关功能的影响

### 5.1 对 `exec_status` 的影响
- **现状**：`exec_status` 用于轮询异步 job 的运行进度、状态（`running` / `finished` / `failed`）及最新输出。
- **影响评估**：
  - `exec_status` 属于轻量级只读查询，走通用 API 请求速率限制（RPS），**从来不消耗、也不应该消耗 exec 并发槽位**。
  - 本设计保持 `exec_status` 行为完全不变，不增加额外配额检查。

### 5.2 对 `exec_cancel` 的影响
- **现状**：客户端调用 `exec_cancel` 终止正在运行的 job。
- **影响评估**：
  - 取消逻辑必须确保配额正确归还。
  - 在本设计中，当 `exec_cancel` 成功向本地进程发送信号或向远端节点下发取消命令并置状态为 `cancelled` 时，触发 `releaseJob()`。
  - 增加保护：若 `cancel` 发生网络异常或延迟，通过超时保护兜底回收 Job 槽位，防止配额泄漏。

### 5.3 审计日志与错误响应结构
1. **审计动作区分**：
   - 交互命令被限流：保持动作 `concurrency_limited`，错误信息 `concurrency_limit`。
   - 异步 Job 被限流：新增独立动作 `job_concurrency_limited`，错误信息 `job_concurrency_limit`。
2. **429 响应体区分**：
   - 结构化 JSON 中明确区分限制类型：
     ```json
     {
       "error": "rate_limited",
       "limiter": "job_concurrency",
       "limit": 4,
       "in_use": 4,
       "retry_after_ms": 1000
     }
     ```

---

## 六、兼容期开关与回滚方案

### 6.1 渐进式启用开关（Feature Flag）
引入环境变量控制开关：
```bash
VPS_COMMANDER_SEPARATE_JOB_QUOTA=true  # 默认值：初期设为 false，全网灰度时开启
VPS_COMMANDER_TOKEN_JOB_CONCURRENCY=4 # 异步 job 默认配额
```
- **开关关闭时（`false`）**：保持与当前行为 100% 一致，异步 job 继续调用 `acquireTokenExec`，使用交互槽位。
- **开关开启时（`true`）**：启用本设计的独立 Job 配额与生命周期解耦。

### 6.2 回滚方案（Rollback Plan）
1. **运行时快速回滚**：
   - 无需重新部署二进制或恢复数据库。
   - 只需将 `VPS_COMMANDER_SEPARATE_JOB_QUOTA` 环境变量修改为 `false` 并热重载或重启 Hub 服务，即刻退回旧逻辑。
2. **二进制级回滚**：
   - 若回滚到旧版本二进制，由于数据库新增的 `concurrent_job_limit` 列允许 `DEFAULT 0`，旧二进制直接忽略该未知列，数据库无需执行任何降级回滚操作。

---

## 七、测试清单（Test Checklist）

在实现阶段（阶段 3 实施），必须完成以下测试套件以确保设计的健壮性：

### 7.1 单元测试（Unit Tests）
- [ ] `TestTokenLimiter_AcquireJob_DefaultLimit`：未配置时正确继承全局 Job 默认配额（如 4）。
- [ ] `TestTokenLimiter_AcquireJob_CustomLimit`：配置自定义 `concurrent_job_limit` 时准确拦截超额请求。
- [ ] `TestTokenLimiter_JobLimit_IndependentFromExec`：占满 Job 配额后，交互式 `AcquireExec` 仍能 100% 成功获取槽位。
- [ ] `TestTokenLimiter_ExecLimit_IndependentFromJob`：占满 Exec 槽位后，`AcquireJob` 仍能成功提交异步任务。
- [ ] `TestTokenLimiter_JobReleaseOnCompletion`：任务正常执行结束，Job 配额被准确释放归还。
- [ ] `TestTokenLimiter_JobReleaseOnCancel`：调用 `exec_cancel` 后，Job 配额被立即释放归还。
- [ ] `TestTokenLimiter_JobReleaseOnTimeoutOrLost`：远端 Agent 离线导致 Job 变为 lost 状态时，配额自动回收，无泄漏。

### 7.2 集成与端到端测试（Integration / E2E Tests）
- [ ] `TestE2E_AsyncJobDoesNotBlockInteractiveExec`：启动 2 个长耗时异步 Job，并发发起 `POST /api/v1/exec` 快速诊断命令，验证返回 HTTP 200 而非 429。
- [ ] `TestE2E_JobConcurrencyLimitAudit`：当超出 Job 上限时，验证审计日志产生 `job_concurrency_limited` 事件，且字段完整。
- [ ] `TestE2E_CompatibilityFlagToggle`：开关开启与关闭状态下的行为切换验证。

---

## 八、总结与实施建议

本设计通过数据模型与生命周期的清晰解耦，根除了“长任务占槽瘫痪交互通道”的架构隐患。配合阶段 1 的结构化审计与阶段 2 的令牌并发在线配置，VPS-Commander 将形成**短交互高可用、长任务可预期、全链路透明可见**的稳健并发控制体系。
建议在 OPC 评审通过本设计且 T-20261111-06 查证数据归档后，正式立项安排编码实施。
