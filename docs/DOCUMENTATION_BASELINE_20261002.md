# VPS-Commander 全仓库文档基线与索引

本文档作为 2026-10-02 之后的文档总入口，用于区分当前设计、历史记录与下一版本研发计划。

---

## 一、当前项目定位

VPS-Commander 是自托管、多节点、AI Agent 友好的 Linux 运维控制中枢。

核心链路：
`AI / MCP / Web Panel → Hub → Local Executor 或 Agent WSS → Target Host`。

---

## 二、当前架构基线

### 2.1 Hub
- HTTP API。
- MCP。
- Web Panel。
- Agent Router。
- Security Snapshot。
- Tool Registry。
- Session。
- SQLite。
- Audit / Webhook。

### 2.2 Agent
- 主动 WSS 回连。
- per-device Credential。
- Protocol / AgentVersion / Capabilities。
- Host-level File / Shell / Process 执行。

### 2.3 双通道
- 控制通道：HTTP / MCP → Hub → Executor。
- 节点通道：Agent → WSS → Hub。

---

## 三、当前 MCP 基线

12 个工具：
`list_devices`、`exec_command`、`read_file`、`read_multiple_files`、`create_directory`、`move_file`、`list_processes`、`edit_block`、`write_file`、`list_agent_mcp`、`call_agent_mcp`。

远程：legacy SSE。
本地：stdio。
下一阶段：Streamable HTTP。

---

## 四、当前安全基线

- HTTP/MCP：Bearer API Key。
- Agent：per-device Credential。
- SQLite：Credential Hash。
- Low / Medium / High Tool Risk。
- Policy Snapshot。
- Shared Secret fallback：生产关闭。
- MCP 与 HTTP：统一授权门禁。
- Panel Admin Token：服务端 bridge。

---

## 五、文档分层

### 5.1 当前规范
- `README.md`：项目入口与能力总览。
- `docs/ARCHITECTURE.md`：架构与设计。
- `docs/DEV_SPEC.md`：研发与协议。
- `docs/MCP_INTEGRATION.md`：MCP 接入。
- `deploy/README.md`：生产部署。
- `docs/ACCEPTANCE_CRITERIA.md`：验收门禁。
- `docs/PROGRESS_TRACKING.md`：当前进度。
- `docs/VPS_COMMANDER_V2_1_SECURITY_HARDENING_SPEC.md`：v2.1 安全加固。

### 5.2 审计 / 整改材料
- `docs/BUGHUNT_20261002.md`。
- `docs/DC_REFACTORING_AND_SECURITY_SPEC.md`。
- `docs/DC_REFACTORING_SECURITY_REVIEW_HANDOFF_20261002.md`。
- `docs/REVIEW_B_TO_A_HANDOFF_20261002.md`。

### 5.3 历史记录
- `REVIEW_A_CODE_AUDIT_20261002.md`。
- `REVIEW_B_CODE_AUDIT_20261002.md`。
- `INCIDENT_TIMEOUT-20260930.md`。
- `M4_FINAL_ACCEPTANCE_REPORT.md`。
- `DC_PHASE1_IMPLEMENTATION.md`。

历史文件用于记录演进过程，不覆盖当前规范。

---

## 六、当前生产基线

当前生产集群包括：
- `wjyhk`
- `N3450`
- `hwhk`
- `hwsg`
- `m4-live-agent`
- `qnvn`

Hub：`/opt/vps-commander/vps-commander-hub`。
Database：`/opt/vps-commander/data/commander.db`。
Listen：`127.0.0.1:9521`。

2026-10-02 审计发现生产二进制曾 `vcs.modified=true`，因此下一次发布必须 clean rebuild 后重新对齐生产现场。

---

## 七、v2.1 基线

### P0
- EffectiveRisk 完全 fail-closed。
- Agent Credential 脱离 argv / cmdline。

### P1
- Principal / Operator Identity。
- Session Owner 强隔离。
- principal → credential → session → device → operation 审计链。

---

## 八、文档维护规则

新增能力必须同时检查 README、架构、开发规范、MCP、部署、验收、进度以及 OpenAPI Contract。
历史报告只追加新的后续报告，不覆盖原始证据。
