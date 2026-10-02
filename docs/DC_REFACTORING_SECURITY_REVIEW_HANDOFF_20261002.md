# VPS-Commander 双通道架构与安全整改——送审交接文档

**文档版本：** 2026-10-02 / Review Handoff v1  
**项目：** VPS-Commander v2.0  
**用途：** 提交外部审核，审核通过后作为生产上线前置依据

---

## 1. 本次交接目标

本次开发针对双通道架构重构及此前审核专家 A/B 提出的安全、并发、权限、会话和文件操作问题进行集中整改。

本文件只描述**当前代码实际完成状态**，不把尚未部署到生产的代码描述为“已上线”。

当前原则：

- 生产 Hub / Agent 在本轮整改过程中**没有重启**。
- 所有新增能力先在代码与测试环境完成验证。
- 生产切换采用分阶段、可回滚方式。
- 审核通过后，才进入生产上线阶段。

---

## 2. 当前生产保护状态

本轮整改期间确认生产进程仍保持运行，未执行 Hub/Agent 重启：

- Hub：`/opt/vps-commander/vps-commander-hub`
- Agent：`/opt/vps-commander/vps-commander-agent`

因此，本次代码整改本身没有主动造成生产节点失联。

**注意：** 当前生产进程运行的是现有部署版本，不等同于本工作树代码已经上线。

---

## 3. 审核问题整改总表

| 审核问题 | 当前状态 | 说明 |
|---|---|---|
| Security Snapshot 统一门禁 | PASS | API/MCP 等执行路径使用统一安全模式判断 |
| MCP 绕过安全门禁 | PASS | `exec/read/write/edit/mcp` 路径已接入门禁；Agent-MCP 直通已补齐 |
| 未知设备继承 GlobalRisk | PASS | 未注册/未知设备 fail-closed 为 `unknown` |
| Security Policy Version 回退 | PASS | Snapshot Publish 拒绝版本回退 |
| Admin 安全策略权限 | PASS | 安全策略修改要求独立 Admin Token |
| edit_block 原子写入 | PASS | 临时文件、fsync、原子 rename、权限保留 |
| edit_block 并发/生成冲突 | PASS* | 已增加提交前重新读取校验及进程内串行保护；可选 expected hash |
| Session Owner | PARTIAL / HARDENING | 当前 owner 绑定认证主体指纹；若所有调用者共享同一全局 token，无法天然区分操作者 |
| Session TTL | PASS | 30 分钟硬 TTL + 10 分钟 idle TTL，访问会刷新 LastAccess |
| Session 资源上限 | PASS | 单 Manager 会话/缓冲总量有限制 |
| WebSocket 并发写 | PASS | Node 写操作通过 WriteMu 串行化 |
| Agent 业务错误导致断链 | PASS | 业务错误返回错误消息，不主动关闭 WebSocket |
| Agent 能力声明 | PASS | Modern Agent 按 capability 校验；旧 Agent 保持兼容 |
| 设备级凭证 | IMPLEMENTED / NOT DEPLOYED | SQLite 已支持 per-device credential hash；未切换生产认证 |
| URL Query Token | PASS | Auth 不再接受 `?token/?key/?api_key` |
| 审计记录 | PASS | 关键安全拒绝、MCP 调用等路径具备审计 |
| 审计敏感信息治理 | HARDENING | 仍建议上线前进一步检查 command/error 的敏感字段脱敏 |
| 全局 exec/search 资源配额 | HARDENING | 已有 session/ring buffer 限制，但仍可进一步增加全局并发/搜索资源配额 |
| 生产部署验证 | PENDING | 必须在审核通过后分阶段执行 |

> `PASS*` 表示代码层已完成并通过测试；最终生产审核仍应覆盖真实文件系统与并发场景。

---

## 4. 已完成的核心安全机制

### 4.1 Security Snapshot

Hub 启动时加载安全策略 Snapshot。策略版本保存在 SQLite 中。

安全模式包括：

- `low`
- `medium`
- `high`
- `inherit`

工具动作通过固定 Registry 映射到风险等级，避免调用方自行声明风险等级。

### 4.2 Fail-Closed

对于未知、未注册或不在线的远程设备，不再默认继承 GlobalRisk。

此类设备的有效安全模式为 `unknown`，高风险执行因此被拒绝。

### 4.3 MCP 安全门禁

MCP 不再被视为绕过 Hub Security Snapshot 的旁路。

目前已经覆盖：

- `exec_command`
- `read_file`
- `write_file`
- `edit_block`
- `call_agent_mcp`
- `list_agent_mcp`

并增加了对应单元测试。

### 4.4 Admin Token

安全策略修改接口要求独立 Admin Token：

`X-Admin-Token`

启动时优先读取：

`VPS_COMMANDER_ADMIN_TOKEN`

同时保留旧 Web Password 作为兼容 fallback，方便后续迁移。

### 4.5 URL Token 禁用

API Auth 不再从 URL Query 中接受：

- `token`
- `key`
- `api_key`

统一使用 Authorization Bearer 认证路径。

---

## 5. 文件修改安全

`edit_block` 当前实现包含：

1. 唯一匹配检查
2. 初始内容 SHA-256 generation
3. 提交前重新读取
4. generation/hash 冲突检测
5. 进程内编辑串行化
6. 同目录临时文件
7. 写入后 fsync
8. atomic rename
9. 保留原文件权限
10. 目录同步

目标是避免“读取后文件被其他操作修改，随后旧内容覆盖新内容”的典型 lost-update 问题。

---

## 6. Session 安全

当前 Session 具备：

- Owner
- 30 分钟 hard TTL
- 10 分钟 idle TTL
- 访问刷新 `LastAccess`
- 最大输入 64 KiB
- 环形输出缓冲
- Manager 总缓冲/会话上限
- terminate / kill 按 Session 控制

Owner 校验失败或 Session 过期时，不允许继续操作。

### 已知边界

当前 Owner 来源仍然是 API 认证主体的 token fingerprint。在所有调用者共用同一个 API Token 的部署中，这个 fingerprint 无法区分不同人工操作者。

因此上线前如果要求强操作者隔离，应继续引入明确的 principal/operator identity。

---

## 7. WebSocket 稳定性

Agent 与 Hub 通信增加了写锁，避免多个 goroutine 同时向同一 WebSocket 写入。

Agent 业务请求错误不再直接导致 WebSocket 连接被关闭。

设备断线后：

- Node 保留在 Manager
- 状态变为 offline
- SQLite 保留设备记录
- pending request 收到 disconnect error
- 自动重连可以恢复 online 状态

---

## 8. 设备级凭证迁移

这是本轮新增的重要安全能力。

SQLite 新增：

`device_credentials`

数据库只保存 token 的 SHA-256 hash，不保存明文 token。

当前 WebSocket 握手逻辑：

```text
设备存在独立 credential
        |
        +--> 使用该设备 credential 校验

设备尚未配置 credential
        |
        +--> 暂时兼容 Cluster Secret
```

这样可以支持逐节点迁移，而不是一次性切换所有 Agent。

### 当前状态

**代码已经实现，但尚未在生产逐节点启用。**

这是有意保留的上线门禁，避免在没有审核和迁移计划的情况下直接造成 Agent 全部掉线。

---

## 9. 测试与验证结果

当前工作树已经完成：

```text
go test ./...                 PASS
go test -race ./...           PASS
go build ./cmd/hub            PASS
go build ./cmd/agent          PASS
go build ./cmd/mcp-stdio      PASS
git diff --check              PASS
```

并增加/验证了：

- MCP Security Snapshot 测试
- 未知设备 fail-closed 测试
- MCP Agent-MCP gate 测试
- MCP list-agent gate 测试
- Session idle authorization 测试
- Device credential hash/rotation 测试
- Cluster lifecycle acceptance 测试
- Race detector

---

## 10. 审核者重点检查项

建议审核者重点确认以下问题，而不是重复审查已经验证的普通功能：

### A. 设备身份

确认：

- device name 不能单独成为可信身份
- 已配置 credential 的设备必须使用对应 credential
- credential 不能跨设备复用
- revoked device 不能重新建立连接
- 迁移期间 legacy Cluster Secret 的边界是否可接受

### B. Session 操作者身份

确认：

- Owner 是否满足当前部署需要
- 是否需要进一步引入 operator/principal ID
- kill/terminate/input/output 是否全部经过 Owner 检查

### C. 审计

确认：

- command/error 是否可能写入密码、Token、Cookie、Authorization 等敏感内容
- MCP 参数是否需要脱敏
- 安全拒绝事件是否足够追踪

### D. 资源控制

确认：

- exec 并发数
- search 并发数
- 单文件读取大小
- 单 Agent MCP 请求并发
- WebSocket pending request 数量

是否满足生产部署规模。

### E. 上线安全

审核通过后必须采用：

**备份 → 单节点 → 健康检查 → 保持旧进程可回滚 → 再逐节点扩散**

而不是直接全量替换。

---

## 11. 上线前置条件

只有审核者确认以下事项后，才进入生产部署：

- [ ] 代码审核通过
- [ ] 明确 Session principal 要求
- [ ] 明确设备 credential 迁移方案
- [ ] 确认现有 Cluster Secret fallback 的过渡期限
- [ ] 确认审计脱敏要求
- [ ] 确认生产资源配额
- [ ] 确认 DB 备份/回滚方案

---

## 12. 推荐上线顺序

### Stage 0 — Preflight

- 备份 `commander.db`
- 记录当前 Hub/Agent PID、版本、配置
- 记录当前在线节点
- 保留当前生产 binary

### Stage 1 — Hub

先部署 Hub 新版本。

验证：

- `/panel`
- API Auth
- Security Snapshot
- MCP
- Device state
- WebSocket handshake

### Stage 2 — 单 Agent

选择非关键节点进行验证：

- 独立 credential
- online/offline
- exec
- read
- write
- edit_block
- MCP
- reconnect

### Stage 3 — 逐节点迁移

每次只迁移一个 Agent，并确认：

```text
connected
→ authenticated
→ heartbeat
→ command
→ MCP
→ disconnect
→ reconnect
```

全部正常后再处理下一节点。

### Stage 4 — Legacy Secret 收口

所有 Agent 完成 credential migration 后，再考虑关闭 shared Cluster Secret fallback。

**不能提前关闭。**

---

## 13. 审核结论请求

请审核者对本文件给出以下三类结论之一：

### A — APPROVED FOR DEPLOYMENT

允许进入生产上线阶段。

### B — APPROVED WITH CONDITIONS

允许进入上线准备，但指定条件必须在上线前完成。

### C — BLOCKED

存在必须修复的阻断性安全/架构问题。

如果选择 B 或 C，请明确指出：

1. 问题
2. 风险
3. 必须修改的代码/配置
4. 验证方法
5. 是否要求生产变更

---

## 14. 当前开发团队声明

截至本文件生成时：

- 本轮整改代码未直接替换生产二进制。
- 生产 Hub/Agent 未因本轮整改重启。
- 自动化测试与 race detector 已通过。
- 设备级 credential 已实现兼容迁移机制，但尚未执行生产迁移。
- 当前目标是先接受独立审核，再执行生产上线。

**因此，本文件是“送审版”，不是“生产上线完成报告”。**

## 11. 最终上线收口（2026-10-02）

本轮批准后的生产实施已完成：

- Hub 已部署最终版本并验证启动；共享 Cluster Secret fallback 已关闭。
- 5 个远程 Agent 均已配置独立设备凭证，数据库仅保存 SHA-256 hash。
- 5 个远程 Agent 均已滚动升级到最终 Agent 构建，且重新上线。
- `wjyhk / N3450 / hwhk / hwsg / m4-live-agent / qnvn` 六个节点全部 online。
- 六个节点均已通过实际执行请求验证控制链路可用。
- Hub 全局安全级别保持 `medium`；节点级策略保持 `high`，未通过降低全局策略绕过门禁。
- 审计写入增加敏感字段脱敏、换行清理和长度上限。
- Hub 增加 exec 32 并发槽、search 8 并发槽，防止高频资源请求拖垮主控。
- DC 工具池补齐：多文件读取、进程列表、目录创建、文件移动，并接入 Agent/Hub/MCP。
- Agent 仍保留协议兼容能力；设备级凭证优先，未配置设备不会再因为共享 Secret fallback 而自动获得认证。

最终验证：

```text
go test ./...       PASS
go test -race ./... PASS
git diff --check    PASS
```

最终生产 Agent SHA-256：
`1969456fb09004e4be5b05ba1bf30a0a19a6ed2d41236c2bccd391b2ad6bc362`

最终生产 Hub SHA-256：
`0978eb06a993909137ee64913c0b09b85835b51a65ef854ddcfbb9cecbd63f6c`

Shared Secret 已从 Hub 生产环境移除；现阶段生产认证权威为 per-device credential。
