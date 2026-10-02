# VPS-Commander v2.1 安全纵深加固与身份隔离升级规范

> 版本：v2.1 Draft
> 日期：2026-10-02
> 来源：REVIEW_B_TO_A_HANDOFF_20261002.md
> 状态：下一版本升级输入，当前生产 v2.0 不因本规范降级或回滚

## 1. 升级定位

v2.0 已完成双通道架构、安全门禁、per-device credential、MCP 授权及生产上线。
v2.1 不重新设计已上线架构，而是针对生产审计发现的三个残留项做纵深加固：
1. EffectiveRisk 全部路径 fail-closed，彻底消除 GlobalRisk 回退放行可能。
2. Agent credential 脱离进程命令行，改用环境变量或受限配置文件。
3. 引入 operator/principal 身份，使 Session Owner 从 PARTIAL 升级为强隔离。

## 2. 非目标

- 不改变现有 Low / Medium / High 风险分级语义。
- 不取消现有 per-device credential。
- 不把 v2.0 的已通过安全门禁重新实现一遍。
- 不要求一次性引入 mTLS；mTLS 可作为后续增强方案。
- 不允许为了升级方便而重新启用全局 Shared Secret。

## 3. P0：EffectiveRisk Fail-Closed

### 3.1 现状

`internal/security/policy.go` 的 `EffectiveRisk` 仍存在 GlobalRisk fallback。
当前实际执法主要通过 `GetEffectiveSecurityMode`，因此生产风险已被控制，但该 fallback 属于纵深防御缺口。

### 3.2 目标行为

策略计算必须以经过认证的 Canonical Device ID 为唯一设备身份来源。
以下任何情况均不得回退 GlobalRisk 放行：
- Device ID 未注册；
- Device ID 已注册但当前不可用；
- credential 不匹配；
- policy snapshot 中不存在该设备；
- snapshot/version 无法验证；
- device identity 与请求声明不一致。

上述情况统一返回 `unknown` / deny，风险值为 0 或显式拒绝状态。

### 3.3 实现要求

- `EffectiveRisk` 默认 fail-closed。
- `GlobalRisk` 只能作为已认证设备不存在专属覆盖时的业务默认值；不得用于未知设备。
- 将“身份解析”和“策略计算”分成两个明确阶段。
- 策略函数不得直接信任 HTTP/MCP 请求体中的 `device` 字段。
- 增加单元测试覆盖未知设备、离线设备、错误 credential、空 snapshot、version regression。

## 4. P0：Agent Token 脱离命令行

### 4.1 现状

当前 Agent 使用 `-token <hex>` 启动参数。虽然生产机器均为自有 root 主机且风险可控，但 token 会出现在 `ps` 等进程信息中。

### 4.2 目标

生产 Agent 默认从受保护环境或配置文件读取 credential，命令行不再出现明文 token。
推荐优先级：systemd `EnvironmentFile` / `LoadCredential` > root-only 配置文件 > 其他安全存储。

### 4.3 实现要求

- 支持 `VPS_COMMANDER_AGENT_TOKEN` 环境变量。
- systemd ExecStart 不得包含 `-token <secret>`。
- 若保留 `-token` 兼容参数，仅允许开发/迁移场景，不作为生产推荐路径。
- `/etc/vps-commander/agent.env` 权限保持 root-only，并验证权限。
- 日志、诊断、panic、审计输出不得打印 token。
- 增加进程列表检查，确保生产启动后 `ps` 不出现 token 明文。
- 轮换 token 时支持无感迁移或可回滚切换。

## 5. P1：Session Principal 强隔离

### 5.1 现状

当前 Session Owner 基于认证上下文指纹，并支持 `X-Operator-ID`；但单 API token 部署下多个操作者仍可能共享同一 owner 身份。
因此现状态为 PARTIAL，而不是完整 operator/principal isolation。

### 5.2 目标身份模型

建立明确的 Principal：
- `principal_id`：稳定、不可由请求目标设备伪造的操作者身份。
- `principal_type`：至少区分 admin / operator / service。
- `credential_id`：用于追踪实际认证凭据。
- `device_id`：目标设备身份，不能替代 principal。

Session ownership 必须绑定 Principal，而不是只绑定全局 token。

### 5.3 授权规则

- 创建 Session 时记录 principal_id。
- 读取、输入、关闭、kill 等 Session 操作必须校验 owner principal。
- admin 是否可以接管其他 principal 的 Session 必须成为显式策略，而非隐式绕过。
- 一个 principal 不得通过修改请求中的 device/name/operator 字段伪造另一 principal。
- 审计日志记录 principal_id、credential_id、session_id、device_id、risk level。
- 敏感身份字段继续执行现有脱敏规则。

## 6. Principal 认证方案

v2.1 第一阶段建议保持现有 API Key 兼容，同时增加独立 Principal credential 层。
API authentication 与 authorization 必须继续分离：认证证明“是谁”，授权决定“能做什么”。

推荐数据模型：
- `principals(id, type, name, status, created_at, updated_at)`
- `principal_credentials(id, principal_id, credential_hash, status, created_at, updated_at)`
- Session 保存 `principal_id` 与创建时 credential_id。

不在数据库保存明文 credential。

## 7. 向后兼容与迁移

- v2.0 Agent 必须可在迁移窗口内继续连接，但新部署不得重新引入 Shared Secret。
- 现有 per-device credential 保持有效。
- 现有 Session 在升级过程中不得被无条件清空；升级后旧 Session 应进入兼容/只读或明确失效状态。
- Principal migration 可先将现有 API key 映射为一个系统 service principal，再逐步增加 operator principal。
- 每一步均提供回滚点，不允许因身份系统异常导致全部 Agent 失联。

## 8. 验收标准

### P0-1 EffectiveRisk

- 未知 device 永远 deny。
- 错误 credential 永远 deny。
- GlobalRisk=High 时，未知 device 仍不得获得 High。
- policy version 回退不得被接受。
- MCP、HTTP API、direct call_agent_mcp 三条路径结果一致。

### P0-2 Credential

- `ps` / `/proc/<pid>/cmdline` 不包含 Agent token。
- systemd 配置中不出现明文 token 参数。
- root-only credential 文件权限正确。
- token 轮换后旧 token 按策略失效，新 token 可连接。

### P1 Principal

- Principal A 创建的 Session，Principal B 默认无法读/写/kill。
- 修改 device、operator、session 参数不能越权。
- admin 接管能力若启用，必须有明确授权检查与审计记录。
- 审计可追溯 principal → session → device → operation。

## 9. 测试矩阵

必须同时通过：
- `go test ./...`
- `go test -race ./...`
- `git diff --check`
- EffectiveRisk fail-closed 单元测试。
- MCP 多路径授权一致性测试。
- token 不出现在 argv 的集成测试。
- Principal A/B Session 隔离集成测试。
- Hub 重启后 Agent 自动恢复测试。
- credential 轮换及回滚测试。

## 10. 发布门禁

v2.1 只有在 P0-1 与 P0-2 全部通过后才允许进入生产灰度。
P1 Principal 强隔离必须在正式发布前完成设计评审；若实现延期，不得伪称为完整强隔离。

生产发布必须遵循：备份 → 新版本旁路构建 → 单节点灰度 → Hub 重启恢复验证 → 全节点滚动 → 审计复核 → 正式验收。
任何一步发现 Agent 失联、权限越界或 credential 泄露迹象，立即停止扩散并回滚。

## 11. 与 v2.0 审计结论的关系

本规范来源于 2026-10-02 B→A 独立生产审计交接。
v2.0 的上线判定不因这些残留项改变；它们属于已识别、风险可控、下一版本必须处理的纵深安全事项。

原始依据：`docs/REVIEW_B_TO_A_HANDOFF_20261002.md`
