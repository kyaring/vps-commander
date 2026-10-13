# 项目档案：vps-commander

| 项 | 内容 |
|----|------|
| 主仓库 | wjy5010u:/root/work/vps-commander（主线 main，GitHub kyaring/vps-commander） |
| 语言 | Go |
| 开发者 | GPT（经 MCP，主做）；workbuddy-2（只接测试类任务）；DeepSeek flash（DevTask 未恢复，暂停） |
| 审核 | Hark |
| 指定模型 | 仅 global:deepseek-v4.1-flash |
| 当前阶段 | 恢复期：每次合并后推送 GitHub main；不构建、不打 tag、不发 release，除非 OPC 指定（覆盖 DEV-PROCESS 铁律 4 的“不 push”）。合并与推送由 OPC 批准后执行，开发者不自行推送。 |

## 验收命令
```sh
gofmt -l .            # 无输出
go vet ./...
go build ./...
go test -count=1 ./...
go test -race <相关包>
git diff --check
```

## 受保护路径
deploy/、scripts/、.github/、data/、bin/、go.mod、go.sum、docs/process/、docs/tasks/BOARD.md

## DevTask
未恢复（恢复规划阶段 3），恢复前不派 flash 任务；恢复后 flash 只接远端模型 API（无内置 key，可能走外网），端点与 key 可配置、key 只经保密注入。

## 构建与发布
新机构建/部署脚本由 T-20261013-03 重建；在此之前不部署。

## 通知
企业微信已接入：development.completed → 新开发群，review.completed → 新审核群。开发者交付时自己推送开发完成（用自己带 events:emit 的令牌，在节点上先 set -a; . /root/.config/vps-commander/notify.env; set +a（不要打印该文件），再运行 scripts/notify-agent-completion.sh development-completed，或直接 POST /api/v1/events，template_id=development-completed）；审核方审核完推送 review-completed。令牌不得写进仓库、任务书、日志或远程执行的命令行。
任务交回另有强制通用回报：所有任务完成、受阻或失败都必须推送 `task-reported`（`done` / `blocked` / `failed`）；受阻时摘要必须写明原因和需要谁处理，通知第一行以 `【工号】` 开头。开发者示例：`scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status done --summary '实现和验收通过。' --employee SWS-E002-gpt --project vps-commander --ref commit-abc123`。受阻示例：`scripts/notify-agent-completion.sh task-reported --task-id T-20261014-10 --type 开发 --status blocked --summary '受阻：等待 Hark 处理权限规则冲突，处理后继续。' --employee SWS-E002-gpt --project vps-commander --ref T-20261014-10`。员工、项目也可通过 `VPS_COMMANDER_EMPLOYEE` / `VPS_COMMANDER_PROJECT` 环境变量提供。令牌不得写进仓库、任务书、日志或远程执行的命令行。

- Legacy webhook endpoints are retired through the admin-only `/api/v1/notifications/legacy/retire` API; legacy channel/task rows remain disabled for delivery-history foreign keys, and webhook URLs are stored only as encrypted channel secrets after retirement.
