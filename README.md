# VPS-Commander

> 当前文档基线：2026-10-02

自托管、多节点、AI Agent 友好的 Linux 运维控制中枢。

## 研发最初目标
- 不依赖第三方运维 Relay，建立用户自己的私有控制面；
- 从单机运维扩展到多 VPS 统一调度；
- 在主机级能力之上建立认证、授权、审计、资源限制和可回滚发布体系。

当前系统已经从早期的 OpenAPI + Cluster Secret + 4 个 MCP 工具，演进为：
Hub/Agent 双通道 + per-device credential + Policy Snapshot + Tool Registry + MCP/HTTP 统一安全门禁 + SQLite audit + Web Panel。

## 当前架构
AI / MCP / Web Panel → HTTPS/WSS → Reverse Proxy → Hub 127.0.0.1:9521
Hub → Local Executor，或 Hub → Agent WSS → Remote VPS。
Hub 不直接公网监听；Agent 只主动回连 Hub。

## 当前 MCP：11 个工具
list_devices、exec_command、read_file、read_multiple_files、create_directory、move_file、list_processes、edit_block、write_file、list_agent_mcp、call_agent_mcp。

风险：Low=读取/观察；Medium=文件修改/目录移动/Agent MCP；High=Shell/进程控制。
远程 HTTP MCP 当前为 legacy HTTP+SSE；本地客户端为 stdio。Streamable HTTP 属于后续演进方向。

## 当前 HTTP
/api/v1/devices、/api/v1/exec、/api/v1/file/read、/api/v1/file/list、/api/v1/file/info、/api/v1/file/search、/api/v1/process/session、/api/v1/file/write、/api/v1/devices/file/edit_block、/api/v1/mcp/*、/api/v1/security/*、/api/v1/notifications/webhook*、/agent/ws、/healthz、/openapi.json、/panel/*。

## 安全
HTTP/MCP 使用 Bearer API Key；Agent 使用每设备独立 credential，Hub 只保存 hash。
生产 Shared Secret fallback 已关闭；URL token 不作为认证方式。
MCP、HTTP、direct Agent MCP 共用 Hub security model；未知设备必须 fail-closed。
Panel Admin Token 由服务端内部 bridge，不下发浏览器。

## 部署
Hub：/opt/vps-commander/vps-commander-hub，监听 127.0.0.1:9521，数据库 /opt/vps-commander/data/commander.db。
Agent：/opt/vps-commander/vps-commander-agent，配置 /etc/vps-commander/agent.env，通过 WSS 回连 Hub。

发布必须：clean tree → go test ./... → go test -race ./... → clean build → vcs.modified=false → 单节点灰度 → Hub recovery → rolling Agent → MCP/security/audit 验收。

## 文档
docs/DOCUMENTATION_BASELINE_20261002.md、docs/ARCHITECTURE.md、docs/DEV_SPEC.md、docs/MCP_INTEGRATION.md、deploy/README.md、docs/ACCEPTANCE_CRITERIA.md、docs/PROGRESS_TRACKING.md、docs/VPS_COMMANDER_V2_1_SECURITY_HARDENING_SPEC.md。

## 当前生产注意
2026-10-02 Bug Hunt 发现生产二进制曾以 vcs.modified=true 构建，因此生产现场不能直接视为 HEAD 的可复现产物。
下一次发布必须 clean rebuild，并把 revision/modified 作为发布证据。

## v2.1
P0：EffectiveRisk 完全 fail-closed；Agent credential 不进入 argv/cmdline。
P1：Principal/Operator 身份、Session owner 强隔离、principal→credential→session→device→operation 审计链。
