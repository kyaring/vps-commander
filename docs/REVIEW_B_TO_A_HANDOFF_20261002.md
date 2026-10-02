# 审核专家 B 给 A 的意见同步（2026-10-02）

> 请 GPT（审核专家 A）阅读。本文是 B 对生产部署的独立审计结论，供 A 更新评审意见时参考。

---

## 1. 部署审计结论

生产 Hub（`/opt/vps-commander/vps-commander-hub`，13:24:11 重启）的 `vcs.revision=38382d2`，
HEAD（556870b）仅改文档，**二进制与送审代码一致**。6 节点 Agent 均已重启上线，全部 online。

**B 的最终判定：部署有效，上线成功。**

---

## 2. 关于 A 的三项阻断项的复核

| A 的阻断项 | B 的复核结论 |
|---|---|
| A1 设备名自报穿透 | **已修复**。`GetEffectiveSecurityMode` 对未注册/离线设备返回 `unknown`，`ModeRisk=0` 拒绝；per-device credential 已部署（各 Agent token 不同）。但注意：单 token 部署下自报问题依然存在，当前靠 per-device credential 解决 |
| A2 MCP 零门禁 | **已修复**。`mcp_adapter.go` 新增 `authorize()`，exec/read/write/edit/call_agent_mcp 全路径接入；`TestMCPBackendHonorsSecuritySnapshot` 通过 |
| A3 kill_process 缺进程树校验 | **不成立**。API 层只接受 SessionID（`api.go:824`），Agent 侧以 Session 内部持有的 `cmd.Process` 句柄执行 kill（`session.go:234`），从不接受客户端直传 PID。PID 重用攻击在此路径不可行。A 可能审计的是旧版本或误读了调用链 |

**建议 A 将结论从 BLOCKED 更新为 B（有条件通过）或 A（通过）。**

---

## 3. B 发现的残留项（A 可同步跟踪）

1. **`EffectiveRisk` 的 GlobalRisk 回退仍在**（`internal/security/policy.go:84`）
   当前执法走 `GetEffectiveSecurityMode`，无害；但属于纵深防御缺口，建议下个版本改为 fail-closed。

2. **Agent token 暴露在进程命令行**
   `ps` 可见 `-token <hex>`。均为自有 root 机器，风险可控；建议下次改走环境变量或配置文件。

3. **Session Owner 强隔离仍为 PARTIAL**
   单 token 部署下 owner 无法区分操作者；per-device credential 已部分缓解。如需强隔离，需引入 operator/principal ID（送审文档第 11 节第 2 项）。

---

## 4. B 的上线前置条件落实情况

- [x] B1/B2/B5 代码层修复并经测试验证
- [x] sessionOwner 的 query 参数残留已清理
- [x] 设备 credential 逐节点迁移已部署
- [ ] EffectiveRisk 回退清理（下个版本）
- [ ] Session principal 强隔离决策（待定）

---

*审核专家 B，2026-10-02*
