# VPS-Commander 文档基线与现状索引

> 基线日期：2026-10-02
> 本文件是仓库文档的导航入口。历史审核记录保留原貌，不作为当前实现契约。

## 当前定位

VPS-Commander 是自托管的多节点 Linux 运维控制中枢。
链路：AI/客户端 → Hub → Local Executor 或 Agent WebSocket → 目标主机。

当前目标：
- 多节点统一身份、路由与状态管理；
- 文件、进程、Shell、MCP 运维能力；
- 认证、三级风险授权与审计；
- 可回滚、可持续升级的生产部署；
- 面向 AI Agent 的 MCP 与 HTTP 接口。

## 当前架构

```text
AI / MCP / Web
      |
 HTTPS/WSS
      |
Reverse Proxy
      |
Hub 127.0.0.1:9521
   |          |
Local      Agent WSS
Executor      |
              +-- N3450
              +-- hwhk
              +-- hwsg
              +-- m4-live-agent
              +-- qnvn
      |
SQLite: state / policy / audit
```

Hub 不直接公网监听；Agent 只主动回连 Hub。

## 当前安全模型

- HTTP/MCP：Bearer API Key；
- Agent：per-device credential；
- Policy Snapshot：设备级有效策略；
- Tool Registry：Low / Medium / High；
- MCP、HTTP、direct Agent MCP 统一授权；
- SQLite 审计；
- 生产 Shared Secret fallback 已关闭。

## 当前 MCP

11 个工具：
list_devices、exec_command、read_file、read_multiple_files、
create_directory、move_file、list_processes、edit_block、
write_file、list_agent_mcp、call_agent_mcp。

当前 HTTP MCP 是 legacy HTTP+SSE；本地是 stdio。
Streamable HTTP 是后续演进方向。

## 当前 HTTP

主要接口：
/api/v1/devices
/api/v1/exec
/api/v1/file/read
/api/v1/file/list
/api/v1/file/info
/api/v1/file/search
/api/v1/process/session
/api/v1/file/write
/api/v1/devices/file/edit_block
/api/v1/mcp/*
/api/v1/security/*
/api/v1/notifications/webhook*
/agent/ws
/healthz
/openapi.json
/panel/*

## 文档分层

当前规范：
- README.md
- docs/ARCHITECTURE.md
- docs/DEV_SPEC.md
- docs/MCP_INTEGRATION.md
- deploy/README.md
- docs/ACCEPTANCE_CRITERIA.md
- docs/PROGRESS_TRACKING.md
- docs/VPS_COMMANDER_V2_1_SECURITY_HARDENING_SPEC.md

审计/整改输入：
- docs/BUGHUNT_20261002.md
- docs/DC_REFACTORING_AND_SECURITY_SPEC.md
- docs/DC_REFACTORING_SECURITY_REVIEW_HANDOFF_20261002.md
- docs/REVIEW_B_TO_A_HANDOFF_20261002.md

历史记录：
- REVIEW_A_CODE_AUDIT_20261002.md
- REVIEW_B_CODE_AUDIT_20261002.md
- INCIDENT_TIMEOUT-20260930.md
- M4_FINAL_ACCEPTANCE_REPORT.md
- DC_PHASE1_IMPLEMENTATION.md

历史报告解释演进原因，不覆盖当前规范。

## 生产状态

2026-10-02 审计确认 6 个设备在线：
wjyhk、N3450、hwhk、hwsg、m4-live-agent、qnvn。

同时发现生产二进制存在 vcs.modified=true 的构建漂移。
因此必须区分“源码当前状态”和“生产现场状态”。
下一次发布必须 clean build、校验 revision/modified，再滚动部署。

## v2.1

P0：EffectiveRisk fail-closed；Agent Token 脱离 argv/cmdline。
P1：Principal/Operator 身份、Session 强隔离、完整身份审计链。
这些属于下一版本，不伪装成 v2.0 已完成能劘，不伪装成 v2.0 已完成能力。

