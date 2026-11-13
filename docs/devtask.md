# DevTask（v1.1.36-A）

让已授权的 AI / 客户端通过 Hub **派发编码任务**，由独立的非 root **runner** 在隔离目录中实现与自测；Hub 只负责任务状态、权限与审计。

## 开关与回滚

| 项 | 说明 |
|----|------|
| 环境变量 | `DEVTASK_ENABLED=on`（或 `true` / `1`）时开启 |
| 默认 | **关闭**；关闭时全部 DevTask REST / MCP 工具返回 404 或不出现在 tools/list |
| 回滚 | 去掉该变量或设为非 on 后重启 Hub，功能完全停用；不删库 |

数据表在 Hub 启动迁移中幂等创建（`dev_projects` / `dev_tasks` / `dev_task_events`），关开关不会丢历史任务行。

## 模块与提交拆分

| 阶段 | 内容 |
|------|------|
| D1 | storage：表、状态机、模型白名单 |
| D2 | scope、OAuth 拒绝 `devtask:run`、admin 项目/白名单 API |
| D3 | 用户 REST + runner claim/heartbeat/events/complete |
| D4 | MCP：`dev_task_dispatch` 等 7 工具 |
| D5 | `cmd/devtask-runner` |
| D6 | 假网关 E2E + 本文档 |

v1.1.36-B（面板、通知事件）在 A 验收并经用户确认后再做。

## 权限 scope

| scope | 用途 |
|-------|------|
| `devtask:submit` | 派发、返工、确认 done |
| `devtask:read` | 列表、详情、日志 |
| `devtask:cancel` | 取消 |
| `devtask:run` | **仅 runner 命名令牌**；OAuth 不可授予 |

admin 隐含 `devtask:submit`、`devtask:read`、`devtask:cancel`，但**不隐含 `devtask:run`**（必须显式赋予命名令牌）。legacy key **不包含**这些 scope。

## 模型白名单

- 设置键：`devtask.allowed_models`（JSON 数组）
- 初始：`["global:deepseek-v4.1-flash"]`
- 仅 admin 可改；派发时校验；运行中失败不切换模型 → `blocked_model`
- 空白名单语义：若白名单为空数组，拒绝一切模型派发（fail-closed）

## Runner 部署要点

详见 [devtask-runner.md](./devtask-runner.md)。

1. 创建系统用户（非 root），工作目录如 `/var/lib/devtask-runner`。
2. 在 Hub 创建仅含 `devtask:run` 的命名令牌。
3. 环境变量：`HUB_URL`、`RUNNER_TOKEN`、`WORK_ROOT`、`MODEL_API_BASE`、`MODEL_API_KEY`（密钥只进环境，不进仓库）。
4. systemd 示例（**占位，勿填真实密钥**）：

```ini
[Service]
User=devtask
Environment=HUB_URL=http://127.0.0.1:9521
Environment=RUNNER_TOKEN=vpc_xxx
Environment=WORK_ROOT=/var/lib/devtask-runner
Environment=MODEL_API_BASE=https://example.invalid/v1
EnvironmentFile=-/etc/devtask-runner.env
ExecStart=/usr/local/bin/devtask-runner
Restart=on-failure
```

5. Hub 开启 `DEVTASK_ENABLED=on` 并登记项目（admin API 或后续面板）。

## 不做的事

- 不提供 merge / push / deploy 工具
- 不允许任意仓库路径或任意 shell 命令
- 不请求白名单外模型
- runner 逐步操作只进 `dev_task_events`，不进 `audit_logs`

## 测试

- 单元：storage 状态机、scope、runner 路径牢笼与白名单
- E2E（假 Hub / 假模型）：`go test ./internal/devtask/ -run E2E`
- 上线前冒烟：真实网关 + `global:deepseek-v4.1-flash` 走通 dispatch → needs_review（由人工确认后执行）

## 规格裁定（2026-11-11）

- **admin 与 runner 权限隔离**：admin 不隐含 `devtask:run`；该权限仅供 runner 命名令牌持有，OAuth 不可申请或下发。
- **空白名单 fail-closed**：模型白名单为空时拒绝一切模型派发，不可回退至默认模型。
- **模型凭据方案 B1 安全边界**：环境变量 `MODEL_API_KEY` 仅由 runner 在本地读取并直接与模型网关交互，Hub 不代理模型密钥也不落库；安全边界已复核接受，见 `docs/devtask-spec-amendment-1028.md`。

## v1.1.36-B（面板与通知）

### 通知事件

| 事件类型 | 模板 ID | 触发 |
|----------|---------|------|
| `devtask.needs_review` | `devtask-needs-review` | runner complete → needs_review |
| `devtask.failed` | `devtask-failed` | complete → failed |
| `devtask.blocked_model` | `devtask-blocked-model` | complete → blocked_model |

在通知任务中订阅上述 `event_type` 并绑定通道即可投递。通知 payload 仅含任务元数据（ID、标题、状态、模型、轮次、执行摘要），不含任务卡与结果全文。

### 面板

「开发任务」分区：任务列表与详情；admin 可登记项目与编辑模型白名单。未开启 `DEVTASK_ENABLED` 时显示提示。

## Runner 如何取模型凭据

Runner 领取任务后，使用同一个命名 runner token 调用 `POST /api/v1/devtasks/{id}/credentials`。Hub 会核对当前租约持有者、租约有效期、任务模型白名单以及 provider 启用状态；Runner 不得在请求体中指定或伪造 `claimed_by`。请求必须通过 HTTPS（或回环连接）。

成功后仅在进程内存中使用返回的 `base_url` 和 `api_key`，不得写入 runner 配置、日志、任务事件、临时文件或其他磁盘介质。凭据获取失败时，任务应进入 `blocked_model` 状态并报告错误，不得自动切换到其他模型或 provider。

### 面板配置 Provider

在「开发任务」页签的「模型 Provider（admin）」卡片中登记或编辑模型网关：Provider ID 使用 `^[a-z0-9_-]{1,32}$`，显示名用于识别，`base_url` 必须为 HTTPS，密钥通过密码输入框提交。编辑时密钥留空表示不修改；保存后输入框立即清空。已有密钥只显示「已设置/未设置」，不会回显；清除密钥需要二次确认。每个 Provider 可发起连通性测试，面板只显示 HTTP 状态、耗时或错误类别，不显示上游响应内容。

模型白名单格式为 `<provider_id>:<model>`；Provider 必须先登记并启用。面板的 DevTask 管理请求直接使用同源 `/api/v1/*`，携带 `credentials: "same-origin"`，不经过 `/panel/api` 代理。
