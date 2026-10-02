# VPS-Commander 当前发布门禁

## 1. Build
- clean Git tree
- vcs.modified=false
- vcs.revision == release commit
- AMD64/ARM64 build 成功

## 2. Cluster
- Hub 127.0.0.1:9521
- 5 个远程 Agent online
- Hub restart 后自动恢复
- unknown device fail-closed

## 3. Auth
- Bearer auth
- URL token rejected
- per-device credential
- Shared Secret fallback disabled
- Panel Admin Token 不进浏览器

## 4. Authorization
- Tool Registry 固定
- Low/Medium/High 一致
- MCP 与 HTTP 共用 gate
- call_agent_mcp 无旁路
- policy version 不回退

## 5. File
- edit_block 唯一匹配
- atomic write
- generation check
- failure 不损坏原文件

## 6. Session
- hard TTL
- idle TTL
- ring buffer cap
- manager concurrency safe
- kill 只操作 Session 内部 process handle

## 7. MCP
11 tools 与源码一致：list_devices、exec_command、read_file、read_multiple_files、create_directory、move_file、list_processes、edit_block、write_file、list_agent_mcp、call_agent_mcp。
必须验证：Medium 可拦截写工具；Medium 可拦截 High 工具；Agent MCP 有 gate；stdio 正常；SSE 正常。

## 8. API regression
devices、exec、file read/list/info/search/write、edit_block、process/session、MCP、security、webhook、panel、agent WS。

## 9. Test
go test ./...；go test -race ./...；git diff --check。
并在 clean build 后重新执行核心 smoke。

## 10. Stop conditions
Agent 断联、认证绕过、unknown device 获得 High、MCP 旁路、token 进入 argv、审计丢失、dirty binary、policy version 回退、Session 无限制增长，任一发生即停止发布。

## 11. v2.1
P0：EffectiveRisk fail-closed；Agent credential 不进 argv。
P1：Principal、Session owner 强隔离、身份链审计。
