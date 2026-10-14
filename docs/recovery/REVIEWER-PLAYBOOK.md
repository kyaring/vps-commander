# 审核岗上手手册（Hark 编写，2026-10-14）

给下一位接班的审核、规划、运维 agent。先读本手册，再读最新的交接文档 docs/recovery/HANDOFF-*.md 和看板 docs/tasks/BOARD.md。

## 1. 角色与铁律
- OPC（SWS-H000-opc）是唯一决策人。合并、推送以外的生产变更、部署、吊销令牌，每一步都要 OPC 明确批准：先给方案和回退，等回复「可以」或「直接下卡」。
- 审核岗负责规划、写任务卡、独立审核、复核运维结果。不改业务代码；合并和推送只在 OPC 批准后做。
- 开发者目前只有 GPT（SWS-E002-gpt），经 OAuth 接入。所有卡都写「开发者：GPT」，一张卡只派一个开发者，各卡之间改动的文件不重叠。
- 外接 agent 不会自己领任务。每次开卡、修卡、出审核意见后，都要给 OPC 一段可以直接转发的交接消息（模板见第 4 节）。
- 敏感信息：令牌、密码、私钥、webhook 地址一律不得进入卡、提交、日志、审核意见或聊天。不读 /etc/vps-commander/hub.env 和 notify.env 的内容，需要时只在子 shell 里 source 后使用。
- 每次回复 OPC 前，先更新交接文档并推送（第 6 节）。

## 2. 环境
- 节点 wjy5010u（192.168.20.132），仓库 /root/work/vps-commander，生产二进制 /opt/vps-commander/vps-commander-hub，数据库 /opt/vps-commander/data/commander.db，服务名 vps-commander-hub。
- 公网 https://vcs.king.nyc.mn（/mcp、/api/v1/*）；内网 http://192.168.20.132:9521。
- 审核岗的访问方式：用远端 MCP 令牌（无 admin 权限）调 exec_command(device=wjy5010u, timeout≤300)。长脚本包成 bash -s <<"EOF" … EOF 传进去；敏感值不写进命令行。
- 审核岗推送 `review-completed` 事件的凭据文件是 `/etc/vps-commander/review-events.env`（权限 `600 root:root`，含 `VPS_COMMANDER_HUB_URL` 与 `VPS_COMMANDER_EVENT_TOKEN`）；只可在子 shell 中 source，不得 cat、echo、打印内容或将凭据写进命令行。
- `hub.env` 中的 `VPS_COMMANDER_API_KEY`（`legacy-env`）scope 虽包含 `admin`，但不含 `events:emit`，不能用来发送事件；事件类探针必须使用具备 `events:emit` scope 的令牌。
- 需要 admin 时：在子 shell 里 source hub.env，取 VPS_COMMANDER_API_KEY，通过 curl -H @- 从 stdin 传 Bearer 头，只调本机 9521。用之前先告诉 OPC。
- 节点上非登录 shell 的 PATH 里没有 go，先执行 export PATH=/usr/local/go/bin:$PATH。
- Hub 重启后 MCP 会话会失效，要重新建会话。
- 耗时长的命令（全量测试、race）用 setsid nohup 放到后台、写日志再读，否则 MCP 调用会超时。
- 提交身份：git -c user.name=Hark -c user.email=hark@reviewer.local（接班的人把名字换成自己的）。推送用节点 SSH key，推到 origin main。

## 3. 任务卡风格（check-task.sh 会解析，格式不能错）
文件：正本放 docs/tasks/T-YYYYMMDD-NN.md，看板 docs/tasks/BOARD.md 加一行；工作区副本放 plans/cards/。

固定开头几行：
```
任务：T-xx 一句话标题
编号：T-YYYYMMDD-NN
开发者：GPT
状态：ready（谁、几点批准；和哪张卡有依赖）
分支：基线 <main 上的 sha>，新建 <type>/<name>
约束：不 push、不打 tag、不产出发布物、不运行 release-build、不部署；不动生产数据库和生产配置（`go build ./...` 是编译检查、属于验收步骤，必须执行）
改动范围：允许 a b c；禁止 x y；补充说明写在第二个分号之后
```
格式规则（踩过的坑）：
- 「分支：」那一行必须写基线 sha。
- 改动范围里的路径用空格隔开，不能用顿号。说明文字里不要出现「允许」二字，因为 check-task 的 sed 是贪婪匹配，会误判越界。
- 开发卡的 worktree 从含本卡的 main 分出（git worktree add /root/work/vc-<编号> -b <branch> main）。如果从基线 sha 分出，check-task 会读不到任务书。
- 运维卡和部署卡不要写死 HEAD=sha，写成「基线是 HEAD 的祖先，且差异只在 docs/」；构建放在基线 sha 的 detached worktree 里做。
- 凡是要用 go 的步骤，都写上 export PATH。

正文分节（照抄这个顺序）：
1. 背景：写 Hark 核对时的 sha，并给出 文件:行号 级别的证据，说明根因。
2. 要求：编号列出，写到可以直接验证的程度；最后单列「不做」。
3. 测试：新测试只放在新文件里，现有测试不改；要求做反向验证（把修复改回去，测试必须失败）。
4. 验收：gofmt -l . 无输出；go vet、go build、go test -count=1 ./... 通过；涉及的包跑 -race；在任务分支上跑 bash docs/process/check-task.sh <编号>（要先把 go 加进 PATH）。
5. 交付：在分支上提交一次，不 push；推送完成通知（无论完成还是受阻都要推）；向 OPC 回复【提交审核】，写明分支、sha、改动文件、验收摘要。
6. 回退。

运维卡还要写：前置检查（生产 sha256 前缀、服务 active、依赖资源是否存在，不满足就停）；每一步只执行一次，结果不符合预期就停下报告，不重试；执行前在线备份（sqlite .backup，integrity_check 必须是 ok，备份文件权限 600）；必须在备份目录里留存每一步关键 API 的原始响应，文件名带步骤号（例如 `$B/step2-routes.json`），权限 600；最后做只读核对清单。部署卡写明可复现构建和 sha256 比对，备份目录命名为 backup-pre-<新sha前12位>-<时间戳>。

卡要小：一个根因一张卡。热修卡的改动范围就是一两个文件。

## 4. 转发给开发者的交接消息模板（text_template）
```
【新任务】<编号> <标题>（开发者：GPT）
任务书：<正本路径>（卡提交 <sha>），请完整阅读后严格按卡执行。
分支：基线 <sha>，从含本卡的 main 新建 <branch>，worktree 放在 /root/work/vc-<编号>
要点：<两三行>
验收：<命令>
交付：分支提交一次；推送通知前先 `set -a; . /root/.config/vps-commander/notify.env; set +a`（不打印该文件），再跑 `scripts/notify-agent-completion.sh ...`；向 OPC 回复【提交审核】
红线：不 push/tag/构建/部署，不动生产；任何输出不得含令牌或 webhook；结果不符合预期就停下报告，不要重试。
```
审核意见同样附一段：【审核结论】PASS 或 CHANGES REQUESTED，加上编号清单和需要重新验收的项。

## 5. 审核流程
1. 收到【提交审核】后，开临时 worktree /tmp/rvXX，在里面只读检出开发者的提交。
2. 检查 git diff 基线..提交 的文件清单是否在改动范围内，并跑 check-task。
3. 独立重跑全部验收：gofmt、vet、build、全量测试、race，以及卡里点名的定向测试。
4. 做反向验证或独立探针：把关键修复改回去，确认测试会失败；对生产相关的行为写最小探针去验证。若某项验收依赖某次 API 响应，而备份目录没有对应的原始响应文件，则视为证据不足。
5. 写结论：PASS，或 CHANGES REQUESTED（编号列出问题）。非阻塞的建议另列，并记入交接文档的「遗留问题」。
6. 当场更新 BOARD.md，提交（用审核岗的身份）。推送 review-completed 到审核群：POST /api/v1/events，template_id=review-completed，variables 只能放 {result,commit,summary} 三项（任务号写进 summary 里），带上 severity 和 source，Idempotency-Key 写成 review:<任务>:<sha>:<结果>。
7. 删掉 /tmp/rvXX。等 OPC 批准后再合并：merge --no-ff，合并提交用审核岗身份；推送 origin main；看板改成 merged。
8. 有生产影响的，另开部署卡。部署完成后做复核：独立构建并比对 sha256；检查服务 active、NRestarts=0；内网和公网 healthz；跑功能探针；确认备份 integrity 为 ok。通过后看板改成 verified，并推送审核事件。

## 6. 交接文档
- 当前正本：docs/recovery/HANDOFF-Hark-20261014-0535.md（工作区副本 plans/）。每次回复 OPC 之前都要更新：在工作区改副本，用 base64 写回节点，然后提交、推送。
- 内容固定五节：当前状态（main sha、生产 sha256、备份目录）；进行中的卡；下一批卡；非阻塞遗留问题；坑。

## 7. 常见坑
- Cloudflare 1010 会拦 Python-urllib 的默认 UA，脚本里要自带 UA。
- exec 审计的 command 字段会被截到 512 字符，探针要单独作为一条短命令跑。
- 面板实际用的静态文件在 internal/web/static/，根目录下的 web/static/ 是旧副本。
- 节点上没有 node，前端 JS 只能靠 Go 测试做字符串断言。
- check-task 只能在任务分支上跑，在 main 上会把其他任务的提交算作越界。
- **卡被修订后，开发分支必须先把 main 合进来（`git rebase main`）再跑 check-task**：check-task 读的是**工作区里的任务书**（`docs/tasks/<编号>.md`），若分支基线早于卡修，它会按旧允许范围判定越界。不要为了更新任务书而把 `docs/tasks/` 检出到工作区（那本身就会越界）。T-20261014-24 的改名卡点就是这么来的。
- 「不构建」指不产出发布物、不跑 release-build；`go build ./...` 是编译检查，属于验收（`docs/process/acceptance.sh` 第 8 行就会跑），**必须执行**。T-20261014-22 曾因这条措辞歧义误停。
- 推送交付通知前**必须先** `set -a; . /root/.config/vps-commander/notify.env; set +a`（不要打印该文件），否则 `scripts/notify-agent-completion.sh` 会输出 SKIP、**静默跳过且退出码仍为 0**。T-20261014-21 因此漏发通知（卡修时才发现）。
- 开发卡的 check-task 必须在分支提交之后运行（它要求工作区干净）；运行前先执行 `export PATH=/usr/local/go/bin:$PATH`，否则内部的 `docs/process/acceptance.sh` 会报 `gofmt: not found`；若 check-task 失败，用 `git commit --amend` 修正，保持分支上只有一个提交。
