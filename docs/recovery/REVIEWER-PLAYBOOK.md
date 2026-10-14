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
分支：基线 <main 上的 sha>（**worktree 必须从「含本卡的 main」分出，不要从基线 sha 分出**——否则 check-task 读不到任务书），新建 <type>/<name>
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

### 5b. 复审（复核别人的审核报告）——Hark 2026-10-15 定
1. **强度必须 ≥ 原卡**：原卡写 `-count=50` 就跑到 50；原卡要求 `-race -shuffle=on` 跑三次就跑三次；原卡要求的**每一项**反向验证都要抽查（不能只跑其中一项）。
2. **记录位置**：复审明细写入被审报告的末尾新增一节「复审（<复核人>，<时间>）」；**看板单元格只写一句结论 + 提交号**，不要把明细塞进单元格（塞进去无法审计）。
3. **必须给问题定性**：区分「**会误报失败**（flaky）」与「**只漏检变异**（测试强度/覆盖不达标）」——这个定性直接决定结论给**返工**还是**带注记通过**。判据要落到卡片原文（例如"卡第 N 行要求覆盖 X，而用例只概率性满足"），不要只引用上游报告。
4. **状态用词**：复核人写 **`rechecked-pass`**；**`verified` 属于终签**，留给 Hark 出。责任链要能一眼看出是谁签的哪一段。

## 6. 交接文档
- 当前正本：docs/recovery/HANDOFF-RikkaHub-1-20261014-1740.md（持有人 Hark，22:29 自 RikkaHub-1 接班；此前为 docs/recovery/HANDOFF-Hark-20261014-0535.md，已移交）。**L2-A 起：正本只保留 YAML 摘要头 + ≤4KB 正文**（判定：`wc -c` ≤ 4096）。
- **明细一律追加到 `docs/recovery/HANDOFF-ARCHIVE.md` 顶部**（新的在上），不要写进正本；正本只放摘要。
- 摘要头字段：`updated` `holder` `main` `production_sha256` `production_started` `service` `open_cards` `next_batch` `archive`。
- 每次回复 OPC 之前都要更新正本并提交、推送。
- 正文固定五节：当前状态（摘要）、进行中、下一批卡、非阻塞遗留（摘要）、坑（要点，完整见 §7）。

## 7. 常见坑
- Cloudflare 1010 会拦 Python-urllib 的默认 UA，脚本里要自带 UA。
- exec 审计的 command 字段会被截到 512 字符，探针要单独作为一条短命令跑。
- 面板实际用的静态文件在 internal/web/static/，根目录下的 web/static/ 是旧副本。
- 节点上没有 node，前端 JS 只能靠 Go 测试做字符串断言。
- check-task 只能在任务分支上跑，在 main 上会把其他任务的提交算作越界。
- **卡被修订后，开发分支必须先把 main 合进来（`git rebase main`）再跑 check-task**：check-task 读的是**工作区里的任务书**（`docs/tasks/<编号>.md`），若分支基线早于卡修，它会按旧允许范围判定越界。不要为了更新任务书而把 `docs/tasks/` 检出到工作区（那本身就会越界）。T-20261014-24 的改名卡点就是这么来的。另注：`git rebase` 要求工作区干净，而开发分支上往往已经攒了未提交的改动（例如上一轮尝试留下的 `git mv` 暂存改名）——**正确顺序是先用 `git commit --amend --no-edit` 把已有改动并入原提交（树随即干净），再 rebase**，不要在给开发的指令里写「必须先干净」而不给出把树弄干净的办法，那会造成死锁。
- 「不构建」指不产出发布物、不跑 release-build；`go build ./...` 是编译检查，属于验收（`docs/process/acceptance.sh` 第 8 行就会跑），**必须执行**。T-20261014-22 曾因这条措辞歧义误停。
- **执行节点名一律用 `wjy5010u`**（当前唯一注册设备；也可省略 `device`，默认在 Hub 所在节点执行）。`wjyhk` 是**旧节点名**、也是 go.mod 里的模块路径 `github.com/wjyhk/vps-commander`，**不是可用设备**——指定它会被安全策略拒绝：`security mode unknown denies exec`（T-20261014-30 实际发生）。它只该出现在历史文档与测试夹具里。
- **`go` 与 `gofmt` 都在 `/usr/local/go/bin`，默认 PATH（`/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin`）里没有它们——`/usr/bin/gofmt` 根本不存在**。所以**每条用到 go/gofmt 的命令都要自己 `export PATH=/usr/local/go/bin:$PATH`，或直接用绝对路径**。此坑已造成 4 次阻塞（T-18、T-30、T-31 等）。根治：T-20261014-32 把两者软链到 `/usr/local/bin`。
- **改正本的脚本要把提交挂在断言之后**（用 `&&` 串联，或脚本内 `set -e`）：否则 `assert ≤4096` 失败后仍会照常 commit，把超限文件推上去（本轮发生过一次，正本一度到 4290 字节）。
- **卡里写的验收/核验命令，作者必须先在节点上跑一遍确认语义正确**：`gofmt -l /dev/null` 这类「看着合理、其实必然报错」的命令，会让执行者误判为失败而停下（T-20261014-32 首轮就因此被误判成受阻）。不确定就用 `command -v <工具>` 这种不可能误报的写法。
- **把多步合成一条脚本一次执行**（`bash -s <<'EOF' … EOF`），减少往返：本项目 exec 通道偶发**内部错误 / 会话失效**（一个会话里已出现 6 次），往返越少越安全，也越省上下文。
- **每次 exec 都是新 shell，`export PATH` 不跨调用保留**：每条用到 go 的命令都要自己带 `export PATH=/usr/local/go/bin:$PATH`，或直接用绝对路径 `/usr/local/go/bin/go`。本项目已因此踩坑三次（T-20261014-18 的 acceptance.sh、T-20261014-30 的 build-info 核验）。
- **可复现构建不嵌入 `vcs.revision`**：`go version -m <bin>` 里查不到 commit；commit 只在 `scripts/release-build.sh` 写的 `$OUT/build-info.txt`（含 `commit=` 与 `sha256=`）。部署卡要核验 commit 时，看这个文件，不要看 `go version -m`。
- 部署卡的「正式部署」那一步**必须后台执行并写日志**：`setsid nohup /bin/sh …/deploy-hub-local.sh "$BIN" --sha256 "$HASH" > /tmp/deploy.log 2>&1 &`，再轮询日志。因为部署会重启 Hub，而调用方的 exec 会话正是 Hub 承载的——前台执行会被切断，表现为 **`exit_code=-1` 且无输出**（T-20261014-26 实际发生，部署其实已成功，只是调用方看不到结果）。遇到这种返回，不要重试部署，先**分诊现场**（服务是否 active、生产二进制 sha256 是新的还是旧的、healthz、备份目录是否存在）再决定。
- **`scripts/notify-agent-completion.sh` 是相对路径**：必须先 `cd` 到仓库或 worktree 根再用，或用绝对路径（T-20261014-31 曾因 CWD 不对而找不到脚本）。
- 推送交付通知前**必须先** `set -a; . /root/.config/vps-commander/notify.env; set +a`（不要打印该文件），否则 `scripts/notify-agent-completion.sh` 会输出 SKIP、**静默跳过且退出码仍为 0**。T-20261014-21 因此漏发通知（卡修时才发现）。
- 开发卡的 check-task 必须在分支提交之后运行（它要求工作区干净）；运行前先执行 `export PATH=/usr/local/go/bin:$PATH`，否则内部的 `docs/process/acceptance.sh` 会报 `gofmt: not found`；若 check-task 失败，用 `git commit --amend` 修正，保持分支上只有一个提交。
