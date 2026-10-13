# 开发规则（所有 agent 开工前必读）

按顺序读：
1. `docs/process/DEV-PROCESS.md`：通用流程与角色
2. `docs/process/PROJECT-PROFILE.md`：本项目的验收命令、受保护路径、开发者、指定模型
3. `docs/tasks/<任务编号>.md`：你的任务书。没有任务书，不要改代码。

角色由派活方指定；没指定就按「开发者」处理。

开发者硬性规则：
- 只做任务书写的事，只改「改动范围」允许的路径；需要越界先问，不要自己扩大范围。
- 从任务书指定的基线新建分支。不 push、不打 tag、不部署、不登录或修改生产主机和生产数据。
- 只用项目档案指定的 AI 模型，不自行切换；受限或失败如实报告。
- 不读取、不打印、不提交任何密钥、令牌、密码。
- 不修改 `docs/process/`、`docs/tasks/BOARD.md` 和本段规则。
- 交付前运行 `sh docs/process/check-task.sh <任务编号>`，全部通过后按任务书「交付」一节提交审核。

审核方：独立复跑 `check-task.sh` 和验收命令；不替开发者改代码，不自行合并或部署。

# VPS-Commander agent completion notifications

When working on VPS-Commander tasks, use the custom event API only at the final acceptance boundary, not for every intermediate commit or partial fix.

## Development completion

After the implementation task is complete, required tests/build checks pass, and the final commit SHA is known, run the non-blocking workflow helper. It skips successfully if runtime credentials are absent:

```sh
scripts/notify-agent-completion.sh development-completed \
  --task-id '<stable-task-id>' --commit '<final commit SHA>' \
  --summary '<concise result and tests>'
```

## Review completion

After an independent review has a final `PASS` or `FAIL` decision, run the helper using the reviewed commit SHA:

```sh
scripts/notify-agent-completion.sh review-completed \
  --task-id '<review-task-id>' --commit '<reviewed commit SHA>' \
  --result PASS --summary '<concise review result>'
```

Use `FAIL` for a final failed review, not for a transient test failure that is still being fixed. Keep summaries short and never include API tokens, passwords, private keys, webhook URLs, or full sensitive payloads. Do not request P1 or severity escalation unless the caller is explicitly authorized as admin.

If the event token or Hub URL is not configured, the helper prints `SKIP` and exits successfully; do not block development or claim that a notification was sent. The helper is an explicit final-boundary action, not an automatic hook on every commit.

Notifications go to WeCom (development.completed to the dev group, review.completed to the review group). Use your own events:emit token via environment variables only; never put it in the repo, logs or remote exec command lines.

## Remote exec conventions

Before running commands through VPS-Commander (`exec_command` / `POST /api/v1/exec`), follow `docs/ops/agent-exec-guidelines.md`: make expected "no match" probes exit 0, make patch/check scripts fail with a non-zero exit, avoid tools missing on the node (`rg`, `bc`, `/usr/bin/time`), and create directories before writing. Audit results distinguish `ok`, `exit_nonzero` (command finished with a non-zero exit), `error` (Hub/agent/tool failure) and `denied`.
