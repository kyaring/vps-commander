# VPS-Commander 阶段验收标准与发布门禁用例矩阵

本文档定义当前 v2.0 生产架构的发布门禁，并为 v2.1 安全加固预留验收项。

---

## 一、构建与版本一致性

### 1.1 基础测试
```bash
go test ./...
go test -race ./...
git diff --check
```

### 1.2 生产构建
- Git 工作树必须 clean。
- `vcs.modified=false`。
- `vcs.revision` 必须等于 release commit。
- AMD64 / ARM64 编译成功。

---

## 二、Hub / Agent 集群验收

| 项目 | 验收标准 |
| :--- | :--- |
| Hub | 监听 `127.0.0.1:9521` |
| Agent | 5 个远程节点可正常回连 |
| Restart | Hub 重启后 Agent 自动恢复 |
| Identity | Credential 与设备身份一致 |
| Unknown | 未知设备必须 fail-closed |

---

## 三、认证与授权验收

- Bearer API Key 正常。
- URL token (`?token` / `?key` / `?api_key`) 必须拒绝。
- 每设备 Credential 独立。
- Shared Secret fallback 关闭。
- Panel Admin Token 不进入浏览器。
- Tool Registry 风险等级一致。
- Policy version 不允许回退。

---

## 四、MCP 验收矩阵

| Tool | Risk | 基础用例 |
| :--- | :--- | :--- |
| `list_devices` | Low | 返回设备列表 |
| `exec_command` | High | `echo/true` |
| `read_file` | Low | 读取 `/etc/hostname` |
| `read_multiple_files` | Low | 批量读取临时文件 |
| `create_directory` | Medium | 创建临时目录 |
| `move_file` | Medium | 移动临时文件 |
| `list_processes` | Low | 返回进程 |
| `edit_block` | Medium | 唯一匹配修改 |
| `write_file` | Medium | 写入临时文件 |
| `list_agent_mcp` | Low | 查询 Agent MCP |
| `call_agent_mcp` | Medium | 调用无害 Agent MCP |

必须同时验证：
- Medium 策略可以阻断 Medium 工具。
- Medium 策略可以阻断 High 工具。
- MCP 与 HTTP 使用同一授权逻辑。
- `call_agent_mcp` 无旁路。

---

## 五、文件与 Session 验收

### 5.1 edit_block
- old_text 唯一匹配。
- generation check。
- atomic write。
- fsync。
- 失败不破坏原文件。

### 5.2 Session
- Hard TTL。
- Idle TTL。
- Ring Buffer 上限。
- 总 buffer 上限。
- 并发访问安全。
- kill 只能作用于 Session 内部 process handle。

---

## 六、API Regression

必须覆盖：
- `/api/v1/devices`
- `/api/v1/exec`
- file read/list/info/search/write
- edit_block
- process/session
- MCP services/list/call/test
- security settings
- webhook
- Panel
- Agent WebSocket

---

## 七、发布停止条件

出现以下任一情况立即停止发布：
- Agent 断联。
- 未授权请求成功。
- unknown device 获得 High。
- MCP 存在旁路。
- Token 出现在 argv。
- 审计丢失。
- dirty binary。
- Policy version 回退。
- Session 资源无限增长。

---

## 八、v2.1 验收预留

### P0
- EffectiveRisk 完全 fail-closed。
- Agent Credential 不进入 argv / cmdline。

### P1
- Principal / Operator。
- Session Owner 强隔离。
- principal → credential → session → device → operation 审计链。
