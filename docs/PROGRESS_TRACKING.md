# VPS-Commander 项目进度与路线图

本文档记录当前版本真实进度。历史审计文件继续保留原始证据，不直接作为当前状态判断依据。

---

## 一、当前总体状态

| 模块 | 状态 |
| :--- | :--- |
| Hub / Agent 双通道 | ✅ 已完成 |
| 多节点持久化 | ✅ 已完成 |
| Per-device Credential | ✅ 已完成 |
| Security Snapshot | ✅ 已完成 |
| Low / Medium / High Gate | ✅ 已完成 |
| MCP 安全门禁 | ✅ 已完成 |
| edit_block 原子安全 | ✅ 已完成 |
| Session 资源限制 | ✅ 已完成 |
| Web Panel | ✅ 已完成 |
| Webhook | ✅ 已完成 |
| MCP 11 Tools | ✅ 已完成源码实现 |
| Clean Production Rebuild | 🟡 下一次发布收口 |
| EffectiveRisk 完全 Fail-closed | 🔵 v2.1 |
| Agent Token 脱离 argv | 🔵 v2.1 |
| Principal 强隔离 | 🔵 v2.1 |

---

## 二、MCP 当前能力

当前共 12 个 Tools：
1. `list_devices`
2. `exec_command`
3. `read_file`
4. `read_multiple_files`
5. `create_directory`
6. `move_file`
7. `list_processes`
8. `edit_block`
9. `write_file`
10. `list_agent_mcp`
11. `call_agent_mcp`

早期“4 个 MCP Tools”的文档已经废止。

---

## 三、v2.0 当前收口

当前架构已经形成：
- Hub 中央控制。
- Agent WSS 回连。
- Per-device Credential。
- Policy Snapshot。
- Tool Registry。
- MCP / HTTP Unified Gate。
- SQLite Audit。
- Session Resource Limits。
- Web Management Panel。

---

## 四、2026-10-02 审计发现

### 4.1 生产构建漂移
生产 Hub / Agent 曾以 `vcs.modified=true` 构建。
这会破坏源码 → revision → binary 的可追溯链。

### 4.2 代码兼容问题
- process/session API 存在 snake_case 请求字段兼容问题。
- Hub 本地 process/session 路径缺少 local fallback。

在源码、测试和生产现场全部验证之前，上述项目不标记为“已修复”。

---

## 五、v2.1 路线

### 5.1 P0-1：EffectiveRisk
- Canonical Device Identity。
- unknown / offline / bad credential → risk 0。
- GlobalRisk 只作为已认证设备的业务默认。
- Unknown 不得继承 High。

### 5.2 P0-2：Agent Credential
- EnvironmentFile / LoadCredential。
- ExecStart 不出现 secret。
- `/proc/*/cmdline` 不可读取 token。
- 支持 rotation + rollback。

### 5.3 P1：Principal
- `principal_id`。
- `principal_type`。
- `credential_id`。
- `device_id`。
- Session Owner 绑定 Principal。
- takeover 必须显式授权并审计。

---

## 六、标准发布顺序

```text
Backup
  ↓
Clean Build
  ↓
Single-node Gray Release
  ↓
Hub Restart Recovery
  ↓
Rolling Agent
  ↓
MCP / Security / Audit
  ↓
Full Acceptance
```

---

## 七、文档同步规则

任何新功能或安全变更必须同步检查：
- README
- ARCHITECTURE
- DEV_SPEC
- MCP_INTEGRATION
- deploy/README
- ACCEPTANCE_CRITERIA
- PROGRESS_TRACKING
- openapi.json（如影响 HTTP Contract）

历史审核报告保持原貌，不回写成当前状态。
