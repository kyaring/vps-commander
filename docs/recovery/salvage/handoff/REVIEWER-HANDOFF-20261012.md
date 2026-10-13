# 审核员交接文档（2026-10-12，由 Hark 交出）

## 角色与铁律
- 你是审核方：复跑验收、逐条核对要求、发审核通知、更新看板。不改代码、不自行合并/部署；每一步生产变更都要 OPC 明确批准。
- 只用 OPC 指定的模型（DevTask 仅 global:deepseek-v4.1-flash）；快速迭代期只做本地提交，不 push / tag。
- 一个任务一个开发者；不给 flash 派与 GPT 在做的任务重叠的卡。卡、提交、日志、审核意见里不得出现密钥。
- 开工前读：/root/vc-main/AGENTS.md、docs/process/SKILL.md、docs/client-skill/SKILL.md、docs/tasks/BOARD.md。

## 访问
- Hub：https://vpstool.kory.kdns.fr，REST /api/v1（exec 字段是 timeout，最大 300 秒）。用你自己的命名令牌，不要复用 hark。
- 主机 wjyhk。Hub 重启会切断正在执行的 exec，部署用 systemd-run 异步跑，之后再查日志。

## 线上现状
- Hub = main fc2991c（T-03 审计 2xx 修复 + T-04 开发完成通知首行【开发者】）。runner 二进制 = 467b500。
- 发布目录保留最近 3 个：/root/vc-release-{467b500,53e0527,fc2991c}；备份 /opt/vps-commander/backup-pre-<sha>-*。
- 数据库 /opt/vps-commander/data/commander.db，只用 sqlite3 -readonly 查。

## 仓库与主线
| 项目 | 路径 | 主线 | 备注 |
|---|---|---|---|
| vps-commander | /root/vc-main | main | 开发在 /root/vc-<主题> 工作树；/root/.openclaw/workspace/vps-commander 只读 |
| flymusic-fusion-go | /root/work/flymusic-research/flymusic-fusion-go | main | DevTask 镜像 /srv/devtask/repos/flymusic-fusion-go；adapter 联网测试在 integration 标签后 |
| cloudotp-cloud | /root/work/cloudotp/cloudotp-cloud | master | 未跟踪 .bak、data/、.env.bak* 绝不能动或提交 |
| am-downloader | /root/.openclaw/workspace/am-downloader | master | 检出在 feat/cap-captcha，同步规范要用临时工作树 |
| address-tool | /root/.openclaw/workspace/address-tool | main | 档案草案待 OPC 确认 |
- 通用规范正本：/root/.openclaw/workspace/dev-process（本地 fb8f1bd，未 push）。改规范：先改正本，再 sync.sh <仓库> 并在各项目提交。

## 审核清单
1. 新建工作树检出待审 SHA。
2. gofmt -l .（无输出）、go vet ./...、go test -count=1 ./...、按卡要求跑 -race、git diff --check <基线> HEAD、扫描密钥字样。
3. sh docs/process/check-task.sh <编号>（主线自动识别 main/master，可用 MAINLINE 覆盖；必要时第二参数传基线）。
4. 逐条对照卡里的要求和测试；能线上实测就实测（不发真实通知的方式优先，如校验失败的 400）。
5. 试合并到当前主线看是否冲突。
6. 结论：PASS / PASS+建议 / CHANGES REQUESTED。

## 审核通知
在 wjyhk 上：source /etc/vps-commander/review-events.env，POST http://127.0.0.1:9521/api/v1/events，Bearer $VPS_COMMANDER_EVENT_TOKEN，
body：{"template_id":"review-completed","source":"<你的名字>","variables":{"result","commit","summary"}}（只允许这三个变量）。
开发完成通知（开发者发）必须带 developer 和 project，首行显示「【开发者】开发完成：项目」。

## 看板
每个仓库 docs/tasks/BOARD.md + docs/tasks/<ID>.md。每次状态变化都更新并提交：ready → in_dev → approved / changes_requested → merged → released。
卡头必须是纯文本「键：值」行（编号、开发者、分支、约束、改动范围：允许 …；禁止 …），不能加列表符号。

## DevTask（flash）
- 令牌：source /etc/vps-commander/devtask-hark.env（DEVTASK_TOKEN）；项目管理用 /etc/vps-commander/hub.env 的旧管理密钥。
- 接口：POST /api/v1/devtasks（project,title,spec_md,model,accept_cmds,allowed_paths,forbidden_paths）；GET /{id}、/{id}/diff；POST /{id}/revise {review_md}；POST /{id}/confirm。
- 路径是纯前缀，不支持通配；allowed_paths 也限制读取；沙箱无外网。
- 每次合并后、下次派单前，先同步镜像：git -C <镜像> fetch --update-head-ok <源仓库> main:main && git -C <镜像> reset --hard main && chown -R devtask-runner <镜像>。
- 审 flash 提交：git fetch <镜像> task/<id>:review/<名字>。合并用 cherry-pick 到主线，然后 confirm。

## 发布（每步等 OPC 批准）
1. 主线 cherry-pick 已审提交，复跑 vet + 相关测试。
2. 在临时工作树按提交构建：CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /root/vc-release-<sha>/vps-commander-hub ./cmd/hub；复制 /root/vc-deploy/deploy.sh；生成 SHA256SUMS。
3. systemd-run --unit=vc-deploy-<sha> --on-active=3 /bin/bash /root/vc-release-<sha>/deploy.sh（备份 + healthz + 在线节点数检查 + 自动回滚）。
4. 约 2 分钟后看 deploy.log 是否出现 DEPLOY OK，做线上实测，更新看板，删掉最旧的发布目录。

## 进行中 / 待办
- T-20261012-01（workbuddy-2，auth 测试，35d55a7）：PASS，待 OPC 决定合并。
- 可清理的工作树：/root/vc-fix-audit-http-2xx、/root/vc-feat-dev-notify-developer、/root/vc-t-20261012-02（已合并）。
- 历史 58 条 202 审计记录保持原样，不改历史数据。
- 延后：hub 主机权限加固、Lucky→Caddy；wjyhk 的 /tmp 若又变回 755 要查原因。
- workbuddy-2 表现达标，可接 GPT 级（≤600 行/12 文件）任务。
