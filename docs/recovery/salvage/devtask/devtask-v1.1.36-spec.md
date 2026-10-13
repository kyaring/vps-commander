**目标**
- 让 GPT 能通过 Hub 把编码任务派给 `global:deepseek-v4.1-flash`，在 wjyhk 的隔离 worktree 里实现、自测、提交到任务分支，GPT 审 diff、Hark 独立审核、用户拍板合并部署
- Hub 只管任务的派发、状态、权限和审计；调模型、改代码由独立的非 root runner 完成，网关 key 和长任务都不进 Hub 进程
- 版本：v1.1.34 已跳过，v1.1.35 开发中；本模块排在其后为 v1.1.36，开关 `DEVTASK_ENABLED` 默认关闭

**不做的事（防偏移）**
- 不提供 merge / push / deploy 工具，runner 拿不到任何远程凭据
- 不让 GPT 传任意仓库路径或任意命令，只能选已登记项目
- 不调用、不切换、不探测白名单外的模型，也不读网关模型列表去挑模型
- 不把 runner 的逐步操作写进 audit_logs
- 不改动现有 scope、legacy key 行为、OAuth 流程

---

**1. 权限 scope**（`internal/auth`、token 管理页、OAuth consent 页）
- `devtask:submit`：派发、返工
- `devtask:read`：查状态、diff、日志、列表
- `devtask:cancel`：取消
- `devtask:run`：只给 runner 自己的命名 token
- 不加进 legacy key 的固定 scope 集合（legacy 正在退役）
- GPT 的 OAuth token 可授 submit/read/cancel；consent 页和服务端都拒绝给 OAuth 客户端授予 `devtask:run`
- admin 隐含全部 devtask scope，但 admin token 仍不能在 OAuth consent 中被选用（沿用 v1.1.31 规则）

**2. 模型白名单**
- 新增设置 `devtask.allowed_models`，存 `hub_settings`，只有管理员能改（面板或 admin API），每次修改写审计（旧值、新值、操作人）
- 初始只有：`global:deepseek-v4.1-flash`；以后增加模型只改白名单
- GPT、OAuth token、runner 都改不了白名单
- `dev_projects.default_model` 由管理员指定，必须在白名单内；白名单移除某模型时，引用它的项目标记为需修正并拒绝新任务
- `dev_task_dispatch` 可选传 `model`：不在白名单就拒绝并返回明确错误；不传就用项目默认
- 任务创建时把模型固定进 `dev_tasks.model`，之后每轮返工都用同一个
- runner 每次调模型前再核对白名单；运行中被移出白名单 → 任务停在 `blocked_model`
- 出错一律不切换：不换到白名单里的其他模型，更不换到网关上的任何模型；要换模型重跑，只能由用户新建任务时显式指定

**3. 数据表**（`internal/storage`，迁移幂等，启动两次不重复）
- `dev_projects`（仅管理员可改）
  - name（主键）、repo_path、base_ref 默认分支、default_model
  - allowed_cmds_json：命令白名单（程序名 + 允许的参数前缀，例如 `go test`、`go vet`、`gofmt -l`、`flutter analyze`）
  - default_accept_cmds_json：默认验收命令
  - forbidden_paths_json：禁止改动路径（如 `.github/`、`deploy/`、`*.env`、`go.sum` 是否允许单独配置）
  - max_rounds、max_steps、timeout_s 默认值、enabled、created_at、updated_at
- `dev_tasks`
  - id、project、title、spec_md（任务卡原文）、accept_cmds_json、allowed_paths_json、forbidden_paths_json（项目值 ∪ 任务值，只能收紧不能放宽）
  - base_ref、base_sha、branch（`task/<id>`）、model
  - status：`queued` / `running` / `needs_review` / `failed` / `blocked_model` / `cancelled` / `done`
  - round、max_rounds、max_steps、timeout_s、parent_task_id
  - 结果：commit_sha、diff_stat、test_summary、result_md、error_code、error_detail
  - 审计字段：actor_name、oauth_client_id（与现有审计格式一致）
  - 租约：claimed_by、lease_until、heartbeat_at
  - created_at、started_at、finished_at
- `dev_task_events`
  - task_id、seq、ts、round、type（model_call / tool_call / cmd / status / model_error / note）、payload_json
  - 输出截断（单条上限，如 8 KB），key、token、密码脱敏
  - 按 task_id + seq 索引；保留策略可配置（如 30 天）
- 状态机（服务端强制，非法跳转返回 409）
  - queued → running → needs_review / failed / blocked_model
  - 任意未结束状态 → cancelled
  - needs_review → queued（revise，round+1）/ done（GPT 或用户确认）
  - running 租约过期 → queued（重新可领），超过 2 次 → failed

**4. MCP 工具**（`internal/mcp`，tools/list 按 scope 过滤）
- `dev_task_dispatch`（submit）：project、title、spec_md、accept_cmds（可选，缺省用项目默认）、allowed_paths、forbidden_paths、model（可选）→ 返回 task_id、branch、model
- `dev_task_status`（read）：状态、轮次、模型、结果摘要、错误码和错误详情
- `dev_task_list`（read）：按项目、状态、时间筛选，分页
- `dev_task_diff`（read）：相对 base_sha 的 diff，按文件或字节分页
- `dev_task_logs`（read）：分页取 events
- `dev_task_revise`（submit）：只对 needs_review / failed 有效，附审核意见，同一分支开新一轮；超过 max_rounds 拒绝
- `dev_task_cancel`（cancel）
- 所有参数做长度限制；spec_md 上限（如 64 KB）

**5. REST 接口**（`internal/api`）
- `/api/v1/devtasks`：上面 7 个操作的对应接口
- runner 专用，只认 `devtask:run`：
  - `POST /api/v1/devtasks/claim`：原子领取一个 queued 任务，写租约
  - `POST /api/v1/devtasks/{id}/heartbeat`：续租
  - `POST /api/v1/devtasks/{id}/events`：批量上报步骤
  - `POST /api/v1/devtasks/{id}/complete`：交结果或报错，校验 claimed_by
- `/api/v1/devtask-projects`、`/api/v1/devtask-settings`：仅 admin

**6. runner**（新增 `cmd/devtask-runner`）
- 部署：wjyhk 上专用用户 `devtask`，systemd 服务；读不到 `/etc/vps-commander/hub.env` 和 `/opt/vps-commander/data`
- 配置 `/etc/devtask/runner.env`（属主 devtask，600）：网关地址、网关 key、Hub 地址、runner token；不含模型名，模型来自任务
- worktree：`/srv/devtask/worktrees/<id>`，从 base_sha 建 `task/<id>`；任务结束保留，按策略清理
- 给模型的工具：`list_files`、`read_file`、`search`（grep，限制结果数）、`write_file`、`edit_block`、`run`
- 路径牢笼：所有路径 realpath 后必须在 worktree 内，不跟随指向外部的 symlink；forbidden_paths 直接拒绝；`.git/` 禁止写
- `run`：按项目白名单解析成 argv 直接执行，不经过 shell；限制单条超时和输出长度；环境变量白名单，去掉 HOME 下的凭据
- git：只允许在 `task/<id>` 上 commit，作者固定为 `devtask-bot`；worktree 无远程凭据，`GIT_TERMINAL_PROMPT=0`
- 每轮结束 runner 自己跑验收命令（不依赖模型声称通过），结果写入 test_summary
- 限制：max_steps、max_rounds、timeout_s，超限 → failed 并说明原因
- 模型报错（429、限额、鉴权失败、模型不可用、超时、返回格式异常）→ `blocked_model`，记录模型名、HTTP 状态码、响应摘要、第几轮第几步，立即停止，不重试别的模型
- 系统提示词固定写入：只按任务卡做、不改范围外文件、不擅自加依赖、完成后写 result.md（改了什么、验收结果、未解决问题）
- 并发先限 1 个任务

**7. 通知**（`internal/notify`）
- 新事件：`devtask.needs_review`、`devtask.failed`、`devtask.blocked_model`
- 内容：任务 id、项目、标题、模型、轮次、错误码或测试摘要
- 以后并入通知系统重构的“自定义事件”

**8. 面板**（`internal/web`）
- 开发任务列表：状态、项目、模型、轮次、更新时间
- 详情页：任务卡、步骤日志、diff、验收结果、取消按钮
- 项目登记管理页、模型白名单设置（仅管理员）

**9. 审计与安全**
- dispatch / revise / cancel / 白名单修改 / 项目修改写 audit_logs，带 actor_name 和 oauth_client_id
- runner 逐步操作只进 `dev_task_events`
- 日志脱敏：key、token、密码、Authorization 头
- wjyhk 上的 runner 不以 root 运行，同时为后续 hub 主机权限边界加固铺路

**10. 任务卡模板**（dispatch 的 spec_md 必须包含）
- 目标：一句话说明要做成什么
- 背景：相关模块和现有模式（给出参考文件）
- 改动范围：允许改的文件或目录
- 禁止事项：不许改的文件、不许新增的依赖
- 接口约定：函数签名、API 字段、错误码
- 必须覆盖的边界情况：逐条列出（防止测试配合实现）
- 验收命令：逐条列出，全部 exit 0 才算完成
- 交付物：commit + result.md

**11. 测试**
- 存储迁移幂等、状态机合法和非法跳转
- scope 拦截：无权限 token 调不动；普通 token 不能 claim；OAuth 客户端拿不到 run
- 白名单：dispatch 传白名单外模型被拒；非管理员改白名单 403；运行中移出模型 → blocked_model
- 路径牢笼和命令白名单：symlink 逃逸、`../`、绝对路径、shell 注入、写 `.git/`
- 用假的 OpenAI 兼容服务做 E2E：成功、工具报错、429 → blocked_model 且没有第二个模型的请求、超时、取消、超过最大轮次、runner 掉线后租约过期重新领取
- `go vet`、全量测试、`-race`
- CI 不调真实模型；上线前用真实网关和 `global:deepseek-v4.1-flash` 跑一个冒烟任务

**12. 分期与验收**
- v1.1.36-A：scope、白名单、数据表、REST、MCP、runner、测试；验收 = 第 11 节全部通过 + 真实冒烟任务走完 dispatch → needs_review → revise → done
- v1.1.36-B：面板、通知事件
- 本模块涉及权限和安全，由 GPT 实现，Hark 审核；推送、发版、部署由用户确认
