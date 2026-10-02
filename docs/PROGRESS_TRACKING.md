# VPS-Commander 当前进度与路线图

> 更新时间：2026-10-02

## 1. 已完成
- Hub/Agent 双通道
- 多节点状态持久化
- per-device credential
- Security Snapshot
- Low/Medium/High gate
- MCP 安全门禁
- edit_block 原子安全
- Session 资源限制
- Web Panel
- Webhook
- MCP 11 tools

## 2. 当前 MCP
11 个：list_devices、exec_command、read_file、read_multiple_files、create_directory、move_file、list_processes、edit_block、write_file、list_agent_mcp、call_agent_mcp。
早期文档中的“4 个 MCP 工具”已废止。

## 3. v2.0 当前定位
Hub、Agent WSS、per-device credential、Policy Snapshot、Tool Registry、MCP/HTTP unified gate、SQLite audit、Panel、Session limits 已形成完整架构。

## 4. 当前发布问题
2026-10-02 Bug Hunt：
1. 生产 binary 曾由 dirty tree 构建；
2. 造成生产 Agent 与 HEAD 能力漂移；
3. process/session snake_case 请求字段存在兼容问题；
4. Hub 本地 process/session 缺少 local fallback。
在源码、测试、生产现场同时确认前，不把这些项目标记为已修复。

## 5. v2.1 P0
EffectiveRisk：canonical device identity；unknown/offline/bad credential → risk 0；GlobalRisk 仅用于已认证设备；unknown 不得继承 High。
Agent credential：EnvironmentFile/LoadCredential；ExecStart 不出现 secret；ps/cmdline 不可读 token；支持 rotation + rollback。

## 6. v2.1 P1
Principal：principal_id、principal_type、credential_id、device_id。
Session owner 绑定 Principal；takeover 必须显式授权并审计。
形成 principal → credential → session → device → operation 链。

## 7. 发布顺序
backup → clean build → single-node gray → Hub recovery → rolling Agent → MCP smoke → security matrix → audit → acceptance。

## 8. 文档同步规则
功能变化同时检查 README、ARCHITECTURE、DEV_SPEC、MCP_INTEGRATION、deploy/README、ACCEPTANCE_CRITERIA、PROGRESS_TRACKING、openapi.json。
历史审核报告保留原貌，不回写当前结论。
