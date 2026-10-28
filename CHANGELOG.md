# 变更记录

格式：按发布 tag 倒序。完整提交历史见 GitHub Releases。

> **阅读说明（2026-10-28）**：生产 Hub 二进制以运维部署为准（约 2026-10-27，sha256 前缀 `bd409c230b44`）。本地 `main` 可能领先生产；下列「Unreleased」仅描述已合入本地 main、**尚未推送/部署** 的能力。

## Unreleased（本地 main，未部署）

- MCP 返回层收敛、溢出文件与会话预算相关行为（T-20261016-01）。
- 本机异步 exec（`exec_start` / `exec_status` / `exec_cancel`）（T-20261016-02）。
- 远端节点异步 exec（经 agent 会话进程；最长约 28–30 分钟、每节点会话名额限制）（T-20261027-06）。
- DevTask（v1.1.36-A/B）实现位于独立 feature 分支，**尚未合入 main**（见 `docs/roadmap.md`）。

## 已部署（无独立 tag 说明，按日期）

### 2026-10-27

- executor / Hub / Agent 文件与搜索加固：exec 等待上限、符号链接与硬链接拒绝、限长读取、原子写入、Search 单文件上限与跳过二进制（T-20261027-01/02/04/05）；行为说明见文档同步卡 T-20261027-03。Hub 已部署；**Agent 需各节点升级后才全量生效**。

### 2026-10-16

- 删除公开探针页面与 `/probe/summary`（T-20261016-06）。
- OAuth 授权端点兼容缺省 `resource` / `scope`（Grok 连接器）（T-20261016-07）。

## v1.1.35（2026-10-11）

- 通知系统 v1：通道（Webhook、Telegram Bot；QQ 官方机器人待确认主动推送能力，暂不投递）、事件模板、任务三层配置，面板新增“通知系统”管理与投递记录（每次尝试结果、单条手动重试）。
- 自定义事件：`POST /api/v1/events`（`events:emit` scope），按令牌限流 30 次/分钟，支持 `Idempotency-Key`；内置“开发完成”“审核完成”模板与 `scripts/emit-notification-event.py`、`scripts/notify-agent-completion.sh`。
- 任务策略：重复间隔、单次事件周期内最多提醒次数（恢复后重置）、时区静默时段、合并窗口、恢复事件关闭告警周期及冷却。
- 投递：持久化 outbox，至少一次投递，失败按 1s/5s/30s 重试，停机中断的投递在重启后继续；Telegram 凭据使用 `VPS_COMMANDER_NOTIFICATION_KEY` 加密存储。
- 兼容：旧 Webhook 目标自动迁移为通道 + 只读兼容任务（仅内置事件）；内置事件默认仍走旧发送器（`VPS_COMMANDER_NOTIFICATION_V1_BUILTIN_MODE=legacy`），可选 `shadow` / `v1`。
- 审计：命令执行完成但退出码非 0 记为 `exit_nonzero`，`error` 仅表示 Hub / agent / 工具失败；stderr 为空时保存 stdout 尾部；面板可按 `exit_nonzero` 筛选。新增 `docs/ops/agent-exec-guidelines.md`。

## v1.1.33（2026-10-11）

- OAuth：刷新令牌滑动有效期，闲置 30 天失效，绝对有效期最长 360 天；授权确认提交防双击；忽略未知扩展 scope（如 `offline_access`），仍拒绝 `admin`；授权记录显示最后使用时间，审计日志可按 OAuth 客户端筛选。
- 审计：系统 Webhook 事件统一归属 `system:notify` / `internal`，启动时幂等修正旧记录；记录 legacy 鉴权成功与拒绝，安全页展示近 7 天使用情况并提供无需重启的 legacy 软停用开关。
- 通知：离线提醒限为 1 分钟、10 分钟、1 小时；早晚报告列出离线节点；资源超限最多提醒 3 次，恢复只提醒一次，持续问题进入早晚报告。
- 通知配置：面板可配置提醒时间点、报告时间及 CPU / 内存 / 磁盘阈值；Webhook 失败重试一次，并在连续失败时显示异常状态。
- 面板：审计表整合 OAuth 客户端显示，移除重复目标列并保留有额外信息的目标；补齐响应式布局验收。

## v1.1.32（2026-10-11）

- 修复 OAuth 授权确认页的 CSP `form-action` 阻止浏览器跳转到已验证客户端回调 URI 的问题；仅放行已验证的回调源。

## v1.1.31（2026-10-11）

- 新增 OAuth 2.1 Authorization Code + PKCE 流程，支持 `/mcp` 的 OAuth 客户端授权、令牌交换与授权管理。

## v1.1.30（2026-10-10）

- 面板：新建令牌后在一次性密钥框中显示完整密钥，提供复制按钮与“我已保存，隐藏”；刷新、编辑、再次创建前清除密钥，未复制时先确认；剪贴板失败时降级为选中文本并提示手动复制；密钥不写入 URL、localStorage 或控制台。
- 面板：复制按钮恢复各自原文字，复制失败提示只显示在所在区域。

## v1.1.29（2026-10-10）

- 面板改为分区（设备、控制台、接入与令牌、安全与通知、日志）；令牌权限模板（只读 / 运维 / 全部，admin 二次确认）；AI 接入向导（GPT Actions、MCP、REST 配置生成与连通测试）。

## v1.1.28（2026-10-10）

- 发布产物新增 Linux armv7 的 Hub 与 Agent；安装 / 升级脚本支持 armv7。

## v1.1.27（2026-10-10）

- 命名令牌：scope、设备白名单、限速、最大会话数；REST 与 MCP 统一 scope 与白名单检查，拒绝写审计。
- MCP Streamable HTTP（`POST /mcp`）；SSE 会话写超时、背压与过期会话处理。
- 令牌级限流与 exec 并发、全局 exec / 搜索并发上限；面板数据缓存与 ETag；CORS 改为精确白名单；结构化请求日志。
- 管理员 `/metrics`（Prometheus）。
- Agent 重连 full jitter 与握手令牌桶限流；资源画像批量写库。
- 审计浏览器：操作人 / 传输方式 / 结果 / 目标，筛选、游标分页、CSV 导出；脱敏后截断；只读列表审计开关。
- 压测工具 `cmd/loadtest`。
- `VPS_COMMANDER_TRUST_PROXY` 只信任 `X-Forwarded-For` 最右侧 IP。

## v1.1.21 – v1.1.26（2026-10-02）

- 一键安装 / 升级脚本重写：SHA256 校验、从 tty 读取输入、Agent 环境变量写入 systemd、启动与 Hub 连接确认、失败自动回滚。

## v1.1.15 – v1.1.20（2026-10-02）

- 开放 `write_file`；新增白名单只读诊断 `diagnostic_command`；修复 Session API 与本机 Session 回退。

## v1.1.8 – v1.1.14（2026-09-30 – 2026-10-02）

- 设备离线生命周期持久化、Agent MCP 集中挂载与调度、安全策略（Low / Medium / High）、每设备凭据双通道上线、面板管理桥接修复、面板 UI 改版。

## v1.0.0 – v1.1.7（2026-09-28）

- 首批发布：Hub / Agent、节点资源画像、设备吊销、面板主题、发布流水线与一键安装。
