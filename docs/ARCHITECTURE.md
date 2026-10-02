# VPS-Commander 当前系统架构规范

## 1. 研发起点
项目最初不是远程终端 UI，而是私有化 AI 运维控制面：用户自己的 Hub 作为控制中枢，多 VPS 通过 Agent 汇聚，AI 通过标准 API/MCP 发起操作，高权限能力可认证、可授权、可审计、可回滚。

## 2. 双通道
控制通道：AI/MCP/HTTP → Hub → Local Executor 或 Agent RPC。
节点通道：Agent → WSS → Hub。
Hub 同时承担身份、策略、路由、审计和资源门禁；任何 MCP 或 Agent MCP 调用都不能绕过 Hub。

## 3. Hub
生产 binary：/opt/vps-commander/vps-commander-hub。
监听：127.0.0.1:9521。
数据库：/opt/vps-commander/data/commander.db。
systemd：vps-commander-hub.service。
职责：HTTP API、MCP、Panel、设备状态、Agent RPC、Policy Snapshot、credential、audit、Webhook、本地执行。

## 4. Agent
Agent 是目标主机上的最小执行面，主动连接 /agent/ws，不要求远端开放管理端口。
握手包含 protocol version、AgentVersion、capabilities、per-device credential。
设备身份必须来自已认证 credential，不能信任客户端自报 device/name。

## 5. 安全模型
Client：Bearer API Key；Panel：Panel Session；Agent：per-device credential。
Tool Registry 定义静态风险；Policy Snapshot 定义设备运行时权限。
Low / Medium / High 是权限上限；unknown device → deny。
生产 Shared Secret fallback 已关闭。

## 6. Tool Registry
Low：read_file、read_multiple_files、list_directory、get_file_info、start_search、get_more_search_results、list_processes、list_sessions、read_process_output。
Medium：edit_block、write_file、create_directory、move_file、mcp_call。
High：exec_command、start_process、interact_with_process、kill_process、force_terminate。

## 7. Policy Snapshot
SQLite 保存策略版本，Hub 使用内存 Snapshot 处理读路径。
version 单调递增；发布失败不替换旧 Snapshot；unknown device 不继承 GlobalRisk High。
v2.1 将彻底消除 EffectiveRisk 的 GlobalRisk fail-open fallback。

## 8. Session 与文件安全
Session：默认 RingBuffer 2MB，总 buffer 8MB，hard TTL 30m，idle TTL 10m，LastAccess + cleanup。
kill/terminate 使用 Session 内部 process handle，不接受客户端 PID；Principal 强隔离属于 v2.1。
edit_block：唯一匹配、generation fingerprint、临时文件、fsync、atomic rename、保留权限。

## 9. 部署与回滚
Internet → TLS Reverse Proxy → 127.0.0.1:9521；Agent 仅出站 WSS。
生产节点：wjyhk、N3450、hwhk、hwsg、m4-live-agent、qnvn。
backup → clean build → single-node gray → Hub recovery → rolling Agent → audit → acceptance。
发现断联、授权绕过、credential 泄露或协议不兼容，立即停止 rollout 并回滚。
