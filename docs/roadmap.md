# 进度与路线图

更新于 2026-10-28。生产 Hub 仍以已部署二进制为准；下列「本地 main 已合并、未推送/未部署」与生产可能不一致。

## 1. 已完成（含已部署）

| 能力 | 说明 |
| :--- | :--- |
| Hub / Agent 双通道、多节点持久化、离线生命周期、设备吊销 | v1.1.0 – v1.1.8 |
| 每设备独立凭据、策略快照、Low / Medium / High 门禁、Agent MCP 挂载 | v1.1.8 – v1.1.11 |
| MCP 工具集、Session API、Streamable HTTP | v1.1.15 – v1.1.27 |
| 一键安装 / 升级（SHA256、自动回滚） | v1.1.21 – v1.1.26 |
| v2.1 安全加固：未知设备 fail-closed、凭据脱离 argv、操作人进审计 | v1.1.26 – v1.1.27 |
| v2.2 Phase A/B：命名令牌、心跳批量写库、限流与并发、审计浏览器、`/metrics` | v1.1.27 |
| 面板分区、令牌模板、AI 接入向导、一次性密钥框 | v1.1.29 – v1.1.30 |
| 删除公开探针页与 `/probe/summary`（T-20261016-06） | 已部署 |
| OAuth 授权缺省 resource/scope 兼容（T-20261016-07，Grok 连接器） | 已部署 |
| executor / Agent / Hub 文件与搜索加固（T-20261027-01/02/03/04/05） | Hub 已部署；**Agent 发版视节点升级** |
| MCP 返回层、本机异步 exec、远端异步 exec（T-20261016-01/02、T-20261027-06） | **本地 main 已合并，未推送、未部署** |
| 企微机器人通道、离线持久化、task-reported 等通知能力 | 已部署（更早批次） |

## 2. 进行中

### 2.1 DevTask（v1.1.36-A / B，分支已开发、待审核合并）

| 阶段 | 分支（示例提交） | 内容 |
| :--- | :--- | :--- |
| D1 storage | 已入相关基线 | 表、状态机、模型白名单 |
| D2 auth/admin | `feat/devtask-d2-auth-api` | scope、OAuth 拒绝 run、admin API |
| D3 REST+租约 | `feat/devtask-d3-rest-lease` | 用户 REST、claim/heartbeat/complete |
| D4 MCP | `feat/devtask-d4-mcp` | 七个 `dev_task_*` 工具 |
| D5 runner | `feat/devtask-d5-runner` | `cmd/devtask-runner` 骨架 |
| D6 E2E+文档 | `feat/devtask-d6-e2e-docs` | 假 Hub/模型 E2E、`docs/devtask.md` |
| B 面板+通知 | `feat/devtask-b-panel-notify` | 面板分区、complete 通知事件 |

开关默认关闭（`DEVTASK_ENABLED`）；不 push、不部署直至审核通过。

### 2.2 其它交付中 / 待合并

- **T-20261015-04**：测试日志 buffer 回归与哨兵自检（`test/leftovers-1015`，待审核）。
- **T-20261028-03**：非测试 `_ =` 忽略错误分类报告（阶段一已交付；阶段二改行为待 OPC 决策）。
- **T-20261016-03**：MCP 工具集 `outputSchema` / annotations / 废弃 memo（看板仍 blocked；T-02 已合并，可解阻后开工）。
- 客户端迁命名令牌：观察 legacy 归零后关闭或轮换 legacy 密钥。

## 3. 待开发

### 3.1 v2.2 Phase C（产品能力）

- **设备接入**：一次性注册令牌、状态机、标签分组、重命名、Agent 版本落后标记。
- **通知规则扩展**：版本落后、拒绝激增、令牌创建/吊销、新 IP、高风险操作、审计积压等（基础投递与 outbox 已有）。
- **审计分析**：趋势、失败率、Top、异常检测；可选 `log_query`（`logs:read`）。

### 3.2 工程与流程

- **L2 上下文预算**（曾排队）：交接文档 ≤4KB（YAML 摘要头 + ARCHIVE）、会话预算统计、会话级 scratchpad（memo 已部分存在）。
- **审计忽略错误阶段二**：按 T-20261028-03 报告中的 C 类，由 OPC 指定要改为返回错误的条目。
- **本地已合并批次推送与部署**：MCP 返回层 + 本机/远端异步 exec 等与生产对齐。
- **Agent 发版**：将 10-27 加固与原子写推到各节点。

## 4. 已知遗留

- Hub 主机执行端以 root 运行；授予本机读/exec/写等同高权限；长期目标 Agent 非 root 与密钥/数据隔离。
- `EffectiveRisk` 对已注册在线设备回退全局等级（设计如此）；未注册/离线在授权层拒绝。
- 会话结束后再次 `Kill()` 的 PGID 复用风险（低）。
- 内嵌 `openapi.json` 的 `servers` 为固定地址；计划按请求 Host 动态生成。
- 仓库尚无 `LICENSE` 文件。
- 远端 async job：节点长时间离线时远端进程可能有界残留（见 T-20261027-06 审核备注）。

原 v2.1 / v2.2 设计文档与历次评审记录见 [archive/](archive/)。DevTask 说明见 [devtask.md](devtask.md)（在对应功能分支上）。
