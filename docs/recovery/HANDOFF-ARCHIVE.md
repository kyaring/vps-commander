## 归档：交接正本瘦身前全文（2026-10-15 07:35，校准 #1 §2.5）

## 2026-10-16 17:55 Hark 交班给 Claude-1：旧正本 HANDOFF-RikkaHub-1-20261014-1740.md 的最后内容

---
updated: 2026-10-15 12:00
holder: Hark（10-14 22:29 接班）
main: 753191c（更新时）
production_sha256: dab72aba222d
production_started: 2026-10-14 17:05:48
service: active
archive: docs/recovery/HANDOFF-ARCHIVE.md
---

# 交接正本（判定：整个文件 wc -c ≤ 4096，超出部分移入 ARCHIVE 顶部）

必读：REVIEWER-PLAYBOOK.md → 本文 → docs/tasks/BOARD.md。
铁律：生产变更、合并、部署、密钥，每一步都要 OPC 批准；绝不写入令牌、密码、webhook；不读 hub.env、notify.env；回复 OPC 之前先更新本文件并推送。
角色：OPC=SWS-H000-opc；开发 GPT（SWS-E002）；次要审核 RikkaHub-1（10-15 07:01 起改为抽查，见 CALIBRATION-RikkaHub-1.md）；Hark 负责调度和终签。

## 在途
- 校准 #1：RikkaHub-1 的回应（72a442b）已由 Hark 答复，见 CALIBRATION 文件末尾。

## 生产与风险
- 生产是 39f3c5c 的构建，10-14 17:05 部署，active。备份为 backup-pre-dab72aba222d-20261014170547。
- 风险：sshd 允许密码登录；数据库没有异地备份；同步 exec 会占用槽位（负载考察结论见 ARCHIVE）。

## 之后
DevTask A1–A6（T-35 暂缓，看板里有两行要合成一行）；三条小尾巴单测；员工表 B、面板 C、令牌整理。

## 坑（完整见 PLAYBOOK §7）
- check-task 只认一行「改动范围：允许…；禁止…」，基线取「分支」行里的 sha；返工卡的基线要写 main 上含卡的那个提交。发卡前先在临时 worktree 里跑一遍。
- 长分支的返工按本轮增量验收。
- 每次 exec 都是新 shell，go 要写绝对路径 /usr/local/go/bin。部署放后台执行，exit=-1 时不要重试。
- 10-16 05:25 sws-vault SV-20261016-05（项目与接入方分两步 + 所有密钥页改版 + SV-04 三条 Low，RikkaHub-1 开发，Hark 全量审核）ready，main 721477f，待转发。
- 10-16 06:40 OPC 定 Hub MCP 三张卡全部交给 RikkaHub-1：T-20261016-01 ready（基线 32c2ffd，排在 SV-05 之后）；02、03 blocked，二者同批部署。
- 10-16 06:55 SV-05 @b02154b 由 Hark 全量审核：CHANGES REQUESTED（空授权列表导致越权和静默撤权、使用范围页丢失 no_client_access）；返工卡 SV-20261016-06 ready（sws-vault main 4c1dfbe）。
- 10-16 06:58 T-20261016-01 Errata 1：C 采用聚合式，D 允许改断言但必须加跨令牌拒绝的断言，A 白名单条目作废，可调用 Cluster.WriteFile（必须 0600）。
- 10-16 07:10 SV-05+06 由 Hark 终签 verified @e99d60b（feat/project-clients），待 OPC 批准合并和部署（回退目标 cb86accd754c）。
- 10-16 07:04 经 OPC 07:03 批准，sws-vault SV-05/06 以 --no-ff 合入 cc94504 并推送；部署 cc94504d633a，备份 /var/backups/sws-vault/vault-20261015-230445.sqlite3，healthz 200，description 和 note 两列迁移成功；回退目标 cb86accd754c（swsvault.prev）。
- 10-16 07:12 T-01 Errata 2：spill 改为落在 Hub 本机（0600、spill_device 字段），read_file 由 Hub 直接服务并按令牌校验归属；远端 agent 仍禁止修改。
- 10-16 07:40 OPC 07:33 定 UI v2 线框（三个标签）并要求打成一张大卡：sws-vault SV-20261016-07（RikkaHub-1，feat/ui-v2-ia，基线 e4e8a84，含 rows.Scan、Gitea 端口、upgrade 轮询等遗留）ready，待转发；Hark 全量审核。
- 10-16 07:45 RikkaHub-1 读卡后停在开工前提问；Hark 给 SV-07 发 errata 1：允许分阶段提交、每个提交都要绿、明确测试 8 的口径。等它继续开发。
- 10-16 08:00 SV-07 阶段 1 已提交 @108d8a1；errata 2 裁决：A3 不变，cmd/swsvault/main_test.go 只许改取 csrf 的那次 GET。


---
updated: 2026-10-15 07:05
holder: Hark（22:29 自 RikkaHub-1 接班）
main: 93e93c5
production_sha256: dab72aba222d
production_started: 2026-10-14 17:05:48
service: active
open_cards: [sws-vault SV-01 审核 CHANGES REQUESTED（报告 2af83c3）, sws-vault SV-02 复审 CR（2f8ca34），SV-03 已 verified @7468820，v1 待 OPC 决定建库、合并、部署, T-20261015-01 交付 434bc4e 待审, RV-T-20261015-01（GPT 审核）]
next_batch: [SWS Vault 独立项目 S-0…S-8（VAULT-PLAN v1.0）; DevTask 暂停]
archive: docs/recovery/HANDOFF-ARCHIVE.md
---

# 交接文档（RikkaHub-1；摘要头 + ≤4KB 正文；明细见 HANDOFF-ARCHIVE.md）

**规则（L2-A）**：本文件只保留 YAML 摘要头与 ≤4KB 正文；明细一律追加到 `docs/recovery/HANDOFF-ARCHIVE.md` 顶部。
规则：回复 OPC 前先更新本文件并推送；审核岗按主线直接下卡（状态写 `ready`）。
铁律：生产变更与合并逐步经 OPC 批准；卡片/提交/日志/聊天绝不出现令牌、webhook、密码；不读 `hub.env`/`notify.env`；每张卡附可直接转发给 GPT 的交接消息，由 OPC 转发。
必读：`REVIEWER-PLAYBOOK.md` → 本文 → `docs/tasks/BOARD.md`。

## 10-16 07:40 从交接正本移出
- sws-vault v1：feat/v1-core @7468820，Hark 已终签 verified（SV-03），等 OPC 建 GitHub 库 kyaring/sws-vault，再批准合并和部署。SV-03 遗留：卡面 errata 和 3 条建议，见 sws-vault 看板。
- T-20261015-02 和 T-03 已于 11:17 经 OPC 批准合入 main（6c4f83c），已推送；生产仍是 dab72aba222d，部署要等 OPC 另行批准。sws-vault 的 GitHub 库已建好（目前为空），节点 ls-remote 可以连通。
- T-20261015-01（workbuddy）：11:37 经 OPC 批准合入 main。T-20261015-04（遗留小项打包）ready，待转发。sws-vault 的 main 和 feat/v1-core 已推到 GitHub。
- sws-vault：v1 已于 11:53 经 OPC 批准合入 main（b8ba36a）并推送；S-6 测试部署卡 SV-20261015-04 ready（部署在 Hub 本机，127.0.0.1:9531，不接反代）。

## 一、当前状态
- 仓库：main = origin/main = `93e93c5`（接班时）；工作区干净；**worktree 与本地分支均只剩 main**。
- 生产：`39f3c5c` 的构建，sha256 `dab72aba222d…`，**17:05:48 启动**（T-33 部署）；active、NRestarts=0；内外 healthz 200。
- 备份：`backup-pre-dab72aba222d-20261014170547`（旧二进制 `610212a58fac` + db，integrity ok）。
- 通知：三群 wecom_bot 路由齐全；deliveries 89 条全 sent。
- 已生效：exec 4KB 截断与去重；`health/assertions` 9/9；`session/stats`；`memo_set`/`memo_get`（只存内存）。
- 审核岗凭据：`/etc/vps-commander/review-events.env`（600 root），推通知前先 source。
- 角色（OPC 23:11）：Hark 主审核；开发者 GPT（SWS-E002-gpt）与 RikkaHub-1（第二开发）；OPC=SWS-H000-opc。

## 二、进行中与遗留（Hark 22:29 接班；复核生产 dab72aba222d active NRestarts=0 healthz ok）
- 完成 T-17～T-34，全部 `verified`/`merged`（要点与明细见 `HANDOFF-ARCHIVE.md` 顶部归档节）。方案 C 四项量化目标全部达标。
- **遗留**：T-20261014-35（DevTask A1）**已出卡但暂缓、未转发**；三条小尾巴未恢复：`T-20261012-01` auth 单测（已改为 T-20261015-01 派给新开发 workbuddy SWS-E003 试用）、`T-20261012-02` executor 单测、「工具错误可恢复 / 失败输出保留尾部」。
- **待清理**：BOARD.md 里 T-20261014-35 有两行草稿（分支名 feat/devtask-a1-storage 和 feat/devtask-v1136-storage 不一致），转发前要统一成一行，和卡文件对齐。
## 三、下一批卡
1. **【主线 23:11】自有保险库**：VAULT-PLAN v1.1（加管理面板，首批平台为 CF、GitHub、Gitea）：OPC 23:21 定为独立项目 SWS Vault（独立仓库、二进制、库、端口、域名，Hub 只当普通 client），拆 S-0 到 S-8；OPC 03:17 批准：本地仓库 /root/work/sws-vault（main=74a1ff7，远端 kyaring/sws-vault 待 OPC 在 GitHub 建空私有库），大单 SV-20261015-01 已 ready（GPT，M1-M8，只开发不部署）。
2. **DevTask 恢复（暂停）**：A1（T-35，未转发）→A6，规格见 `docs/recovery/salvage/devtask/`、STATE-20261013 §6。
3. 三条小尾巴（见「二」）。
4. 更后：员工表 B → 面板卡 C → 令牌整理。

## 四、遗留（摘要）
- 明细见归档与 PLAYBOOK §7。重点：**sshd 允许密码登录、数据库无异地备份**；exec 通道偶发失效（建议后续做「MCP 会话跨 Hub 重启存活」）；T-10/T-12/T-09 若干非阻塞项；`127.0.0.1` 硬编码除 `deploy/upgrade-hub.sh`（已修）外均属有意为之。
## 五、坑（要点，完整见 PLAYBOOK §7）
- check-task 只能在任务分支跑；「改动范围」说明里不要出现「允许」。
（其余坑见 PLAYBOOK §7。）
- **开发卡 worktree 必须从「含本卡的 main」分出**，不要从基线 sha 分出；卡修后先 `git rebase main` 再跑 check-task。
- **每次 exec 都是新 shell**：`export PATH` 不跨调用保留；`go`/`gofmt` 在 `/usr/local/go/bin`（T-32 已软链，绝对路径最稳）。
- 推交付通知前必须 `cd` 到仓库根、再 source `notify.env`、再跑 `scripts/notify-agent-completion.sh`，否则 SKIP 且退出码 0。
- 部署必须后台执行写日志再轮询；`exit_code=-1`/进程消失通常是重启切断会话，**不要重试部署**，先分诊现场。

# 交接文档归档（明细历史）

本文件保存历次交接的**明细条目**。正本 `docs/recovery/HANDOFF-Hark-20261014-0535.md` 只保留 YAML 摘要头与 ≤4KB 正文（规则见正本）。
新增明细一律**追加到本文件顶部**（新的在上）。

## 归档：截至 2026-10-14 14:53 的原正本全文（L2-A 重构前，RikkaHub-1 原样搬迁）

# 交接文档（滚动更新，最后更新 2026-10-14 06:45 +08；当前持有人 RikkaHub-1，此前 Hark）

规则（OPC 05:35）：Hark 每次回复 OPC 前都要更新本文件并推送。

接手人：下一位审核、规划、运维岗（Hark 新会话，或 OPC 指定的同岗位员工）。
铁律：生产变更每一步都要 OPC 明确批准；合并要 OPC 批准，合并后推送 main；卡片、提交、日志、聊天里绝不出现令牌、webhook、密码；不读 hub.env 和 notify.env 的内容；外接 agent 不会自己领任务，每张卡都要附一段可直接转发给 GPT 的交接消息，由 OPC 转发。

规则（OPC 2026-10-14 07:12）：审核岗按主线直接下卡——新卡状态直接写 ready，不再先标 draft 等批准。卡里「分支：」的基线写起草时 main 的 sha（即卡提交的父提交）；开发卡的 worktree 从含本卡的 main 分出，check-task 会以分叉点为准，不必为基线额外开分支。

**接班必读：先看 docs/recovery/REVIEWER-PLAYBOOK.md（审核岗上手手册：角色铁律、环境、任务卡风格、转发模板、审核流程、交接规则、常见坑），再看本文和 docs/tasks/BOARD.md。**

## 一、当前状态
- 仓库 /root/work/vps-commander：main 等于 origin/main（以 git log 为准）。
- 生产 Hub 跑的是 80ac46c 的构建，sha256 为 e3e5ff3185326ada5f40158bf133e5acaa6e192ab80cae9862ddf952fc1e9145，05:32 部署（T-13）。已包含 T-08、T-09、T-10、T-12。备份目录 /opt/vps-commander/backup-pre-e3e5ff318532-20261014053223/，里面是旧版 eb1a6aab。
- T-13 已于 05:45 由 Hark 复核为 verified：独立构建的 sha256 一致；active，NRestarts=0；内网和公网 healthz 正常；retire 接口对无 admin 令牌返回 403（不存在的路由返回 404，说明 retire 已注册）；两个 legacy 通道仍启用；webhook_targets 有 2 行；没有 panic。
- 通知：仍走两个旧通道 legacy-webhook:wecom-dev 和 legacy-webhook:wecom-review。task-reported 还没配路由。
- OPC 工号的编号部分已定为 H000，写作 SWS-H000-<名字>，名字暂定 opc。
- 配置文件路径（易错点）：hub.env 在 /etc/vps-commander/hub.env；notify.env 在 /root/.config/vps-commander/notify.env（**不在** /etc/vps-commander/）。两者权限均 600 root。
- 令牌 scope（只读 api_tokens）：RikkaHub-1、hark-1、hark-2、gpt-2、muse-1、chatgpt-1 均为 [devices:read,files:read,files:write,exec,process,mcp:call,events:emit]；gpt-notify 仅 [events:emit]；legacy-env 为 [devices:read,files:read,files:write,exec,process,mcp:call,admin]，**不含 events:emit**。
- 现任审核岗署名：RikkaHub-1（OPC 06:26 指示用令牌名）。

## 二、进行中
- T-20261014-14 通道整理运维卡：ready（OPC 05:40「直接下卡」批准全部步骤），已交给 OPC 转发 GPT。
  - 前置条件：OPC 先在面板新建 wecom-system 通道（wecom_bot，webhook 由 OPC 录入）。
  - 顺序：备份 → 系统群 6 条路由 → retire 两个旧群（生成 wecom-dev-bot 和 wecom-review-bot）→ task-reported-dev 路由 → 只读核对 → notify.env 追加 EMPLOYEE 和 PROJECT → 探针。
  - admin 调用：在子 shell 里 source hub.env，取 VPS_COMMANDER_API_KEY，通过 curl -H @- 传入，只调本机 9521。
  - 复核：只读核对通道和路由；webhook_targets 为 0；推 review-completed，确认新审核群能收到；看探针投递是否为 sent。
  - 本卡完成后，GPT 交付改用 task-reported 模式。
  - 【受阻 05:46】OPC 在面板新建 wecom-system 时报错「channel config must be a JSON object」。原因：internal/web/static/app.js 第 734 行 saveNotificationChannel 把 config_json 写成了 JSON.stringify(config)，而 API 端字段是 json.RawMessage，收到的是 JSON 字符串而不是对象，所以面板新建任何类型的通道都会失败。处理方案：开热修卡 T-15（改成 config_json:config，补一条 web 测试），部署后 OPC 再建通道，然后执行 T-14。T-14 暂时不要转发给 GPT。OPC 05:48 批准。
  - T-20261014-15（GPT，热修，分支 fix/panel-channel-config-json，基线 3610a7b）：ready，卡提交 959a832。开发提交 2aa6bd7，Hark 05:57 审核 PASS（全部验收复跑通过，反向验证会失败），OPC 05:56 批准后合并成 bdc19a0，已推送。
  - T-20261014-16（GPT，运维，部署 bdc19a0）：ready，OPC 05:56 下卡。复核：独立构建核对 sha256；公网 app.js 里应是 config_json:config；OPC 在面板建 wecom-system 能保存成功。之后修订 T-14 的 sha。
  - 【T-16 verified 06:03】生产现为 bdc19a0 构建，sha256 057028b46079eead87f1bd1098b5ac63f7cce3b2dda52b321639fd564a1e6f46，05:59:41 部署。复核：独立重建 sha 一致；active，NRestarts=0；healthz 正常；公网 app.js 里已是 config_json:config；备份 backup-pre-057028b46079-20261014055940 内是 e3e5ff318532，integrity ok；legacy 通道 2 个、webhook_targets 2 行，均未变；无 panic。T-14 卡已改为基线 bdc19a0、sha 057028b46079。下一步：OPC 建 wecom-system，然后转发 T-14。
  - 【06:05】OPC 建 wecom-system 时选成了 webhook 类型：URL 明文存在 endpoint 里；企业微信回 HTTP 200 带 errcode，所以测试显示成功，实际没收到。已让 OPC 编辑：类型改为 wecom_bot，重新录入。之后 Hark 核对 type=wecom_bot、length(endpoint)=0、credentials_configured=true，通过后才能转发 T-14。【06:10 新阻塞】改成 wecom_bot 后保存报「notification secret encryption key is not configured」：生产 hub.env 里没有 VPS_COMMANDER_NOTIFICATION_KEY（32 字节，64 位 hex 或 base64，见 internal/storage/notification_secrets.go）。没有这个 key，wecom_bot 通道建不了，T-14 的 retire 也会失败。已向 OPC 提出方案并等待批准：由 Hark 执行，先备份 hub.env，在节点上用 openssl rand -hex 32 生成 key 追加进去（不打印），然后重启 Hub 并验证；回退方法是恢复 hub.env 后重启。【06:11 已完成，OPC 06:10 批准】Hark 用 systemd-run 执行：hub.env 已备份到 /opt/vps-commander/backup-pre-notifykey-20261014061100/（权限 600）；key 已追加，hub.env 仍是 600 root，key 值没有出现在任何输出里；Hub 于 06:11:00 重启，active，NRestarts=0，healthz 正常，无 panic/fatal。待办：OPC 自己把 key 拷进保险库（06:14 已把读取命令给了 OPC，由 OPC 本人在节点上执行，Hark 不读取；06:16 OPC 不同意存进 Hark 保险库，理由是不能过分依赖单一账号，表单已关闭；先由 OPC 自己离线保管，以后由 VCHub 自有保险库接管，规划见 docs/recovery/VAULT-PLAN.md）；OPC 重新保存 wecom-system（选 wecom_bot）并点测试。注意：今后任何部署都不能覆盖 hub.env 里的这个 key，否则已加密的通道会失效。另记一个非阻塞问题：普通 webhook 不检查企业微信的 errcode，也没有阻止把 qyapi URL 明文存入。
  - 【T-14 已执行，此前未记录；RikkaHub-1 06:30 复核】OPC 06:14 在面板建成 wecom-system（06:14:41 通道测试 ok）。GPT（gpt-2）06:16 起执行 T-14：步骤 1 备份 backup-pre-T14-20261014061618 与 …061628；步骤 2 六条系统群路由全部 ok；步骤 3 retire wecom-dev / wecom-review ok；步骤 4 task-reported-dev 路由 ok；步骤 6 notify.env 已追加 EMPLOYEE=SWS-E002-gpt 与 PROJECT；步骤 7a task-reported 探针 sent。06:17:02 推 development-completed 报「受阻」。
  - 受阻原因（GPT 原文）：首次 retire wecom-dev 响应 already_retired=true、moved_tasks=[]，与卡预期不符，按红线停止。**RikkaHub-1 判断：这是重复调用触发的正常幂等返回，不是生产缺陷**（internal/storage/notification_legacy_retire.go 在 webhook_targets 无该行、且新通道存在 / 旧通道 endpoint 为空 + enabled=0 + 名字带「（已退役）」时即返回 already_retired=true）。审计显示 wecom-dev 的 retire 被调了两次（06:16:43、06:16:45）。卡片第 3 步「already_retired 都是 false」只在首次调用成立，表述过严。
  - 待查（复核要求）：GPT 称「首次」即 already_retired=true，与 06:16:28 备份仍显示 webhook_targets=2、旧通道 enabled=1 存在时序矛盾；复核时要求 GPT 给出该次调用的原始响应与调用序号。
  - 生产终态（RikkaHub-1 06:30 只读核验，与 T-14 目标一致）：wecom-dev-bot / wecom-review-bot / wecom-system 均 wecom_bot 且 enabled=1；两个 legacy 通道 enabled=0、名字带「（已退役）」、endpoint 为空；endpoint<>'' 计 0；webhook_targets 计 0；路由逐条符合（dev-completed-wecom→wecom-dev-bot、review-completed-wecom→wecom-review-bot、task-reported-dev→wecom-dev-bot、6 条 system-*→wecom-system，legacy-task:* enabled=0）；notify.env 600 root；全库 42 条投递全部 sent。T-14 状态待 OPC 定调（等 GPT 结论）。
  - 【步骤 7b 未通过，RikkaHub-1 06:29 定位】系统群探针（builtin-default）返回 403 scope events:emit required。原因：执行时用的是 legacy-env（hub.env 内置 key），该 key 有 admin 但不含 events:emit；真正带 events:emit 的是 gpt-notify（卡片第 7 节本就要求用 notify.env 里的 gpt-notify）。**结论：系统群路由目前仍缺成功探针背书，需用 gpt-notify 补做一次。** RikkaHub-1 06:29 获 OPC 批准的 admin 途径补证也返回同一 403，未产生任何副作用。
  - 后续顺序：T-15 审核 → OPC 批准合并 → 部署卡 T-16 → 修订 T-14 卡 → OPC 建 wecom-system（06:14 已完成）→ 执行 T-14（06:16 已执行）→ 用 gpt-notify 补系统群探针。
  - 【RikkaHub-1 06:50 审核结论：CHANGES REQUESTED】生产终态达标、无需回退，但卡片验收项未全部满足，退回 GPT 补两项：① 第 7 步 b（系统群探针）须改用 notify.env 的 gpt-notify 重跑，确认 matched_tasks=1 且投递 sent；② 须补交两次 retire（wecom-dev / wecom-review）的原始响应，以澄清「首次即 already_retired=true」与 06:16:28 备份（webhook_targets=2）的时序矛盾。非阻塞建议：卡片第 3 步「already_retired 都是 false」表述过严，建议改为「首次为 false；重复调用为 true 且 moved_tasks 为空」。

  - 【06:46 GPT 整改 + RikkaHub-1 复核】7b 已按审核意见用 gpt-notify 重跑成功：evt_Rpy6P1ACpD3kRZeUldLC（custom.default，severity=P3，source=gpt，actor=gpt-notify）→ 通道 wecom-system（task system-default、template builtin-default），投递 del_1fd0162d85 sent、HTTP 200；task-reported 交付 evt_WkAzGwLjGDl9jThOm1Ua → wecom-dev-bot（task-reported-dev）sent。至此 T-14 的 7a/7b/7c/7d 四条探针全部 sent。
  - 【RikkaHub-1 终审：PASS】生产终态与目标一致；备份与 notify.env 权限正常；两次 retire 原始响应不可恢复（audit_logs 只存 result，无响应体），按不可恢复处理，不要求 GPT 编造。卡片第 3 步「already_retired 都是 false」措辞过严。
  - 【非阻塞① 叙述冲突，要求更正】GPT 两版报告对同一事实表述矛盾：06:17 版称「首次 retire wecom-dev 返回 already_retired=true」；06:46 版称「在第 0 步发现两通道已存在后就停止，没有调用过 retire」。审计证据：866/867 于 06:16:43、869 于 06:16:45，actor 均为 legacy-env、结果 ok；同一时刻的 exec 记录属 gpt-2，命令体正是 retire 包装脚本（retire_once()、以及 source hub.env 的 curl）。最一致的解释：首次调用（866）应为 already_retired=false、moved_tasks=["dev-completed-wecom"]，被 GPT 误述为「首次」的是 06:16:45 的重复调用（869）。要求 GPT 以审计为准更正记录。
  - 【非阻塞② 流程改进】卡片未要求把关键 API 原始响应落盘，审计也不保留响应体，导致事后无法复核。建议后续 ops 卡要求把关键 API 响应写入备份目录，或给审计加「响应摘要」字段。
  - 【RikkaHub-1 07:10 要求更正记录（非阻塞）】GPT 06:46 整改回报称「在第 0 步发现 wecom-dev-bot 与 wecom-review-bot 已存在后就停止，没有调用过 retire」，与其 06:17 报告（「首次 retire wecom-dev 返回 already_retired=true」）自相矛盾，且与审计 866/867（06:16:43）、869（06:16:45）冲突；同时刻 exec 记录属 gpt-2，命令体是 retire 包装脚本。已按手册 §4 给出转发文案，要求 GPT 以审计为准更正记录；不要求补造响应、不要求重跑 retire。
## 三、下一批卡（按顺序）
1. （已开卡，T-20261014-14）通道整理运维卡。每一步都要 OPC 批准：
   1) 备份数据库；
   2) retire wecom-dev 和 wecom-review。这一步要 admin，Hark 的令牌没有，需要用 hub.env 里的内置 key 调本机 9521，用之前先向 OPC 说明；
   3) OPC 在面板里新建系统消息 wecom_bot 通道（webhook 存在保险库，b611e945）；
   4) 配路由：task-reported 和 development-completed 发开发群，review-completed 发审核群，节点类 5 种事件（node_offline、node_recovery、resource_threshold、resource_recovery、scheduled_report）和 custom.default 发系统群；
   5) notify.env 切换到 task-reported，并设置 VPS_COMMANDER_EMPLOYEE=SWS-E002-gpt。
2. 兜底通道开发卡：没匹配到任务的事件发系统群，要改 outbox。
3. 员工表开发卡 B：employees 表，记录工号、显示名、类型、职位、状态；令牌绑定员工；停用员工后令牌和 OAuth 立即失效；审计里记员工快照。之后是面板卡 C，显示工号。设计见 plans/EMPLOYEE-DESIGN.md v0.3。
4. 令牌整理运维卡：只吊销 chatgpt-1；hark-1 保留登记，只有 OPC 能吊销它；改名或绑定员工要 OPC 批准。

- 【新】VCHub 自有保险库（OPC 06:16，排在后面做）：规划 docs/recovery/VAULT-PLAN.md，拆为 V-A 到 V-F 六张卡；T-14 之后开始，V-C 依赖员工表 B。

- 【新卡 T-20261014-17】运维（ready，OPC 07:12 指示直接下卡）：审核岗事件凭据在 /etc/vps-commander/ 落地，从 /root/.config/vps-commander/review-events.env 迁移（600 root）。基线 bdf6608，仓库不改。
- 【新卡 T-20261014-18】开发（ready，OPC 07:12 指示直接下卡）：手册补漏——第 2 节写明审核岗凭据路径、以及 legacy-env 有 admin 但无 events:emit；第 3/5 节增加「ops 卡须留存关键 API 原始响应」。改动范围仅 docs/recovery/REVIEWER-PLAYBOOK.md。基线 bdf6608。
- 【T-20261014-18 受阻 + 根因（RikkaHub-1 2026-10-14 07:03）】GPT 报「check-task 失败：未提交修改 + 相对 bdf6608 越界」。RikkaHub-1 在 worktree /root/work/vc-T-20261014-18 复现，三项失败同源：① ✗ 有未提交的修改；② ✗ 越界 docs/tasks/BOARD.md、docs/tasks/T-20261014-17.md、docs/tasks/T-20261014-18.md、docs/recovery/HANDOFF-*；③ ✗ acceptance.sh: gofmt: not found。根因：GPT 在**提交之前**跑 check-task，此时 HEAD==main==ca6459e，check-task 的分叉点修正条件（fork != HEAD）不成立，基线退回卡里的 bdf6608，于是把审核岗自己那几次提交算成本分支改动；同时该 shell 未 export PATH。提交一次后 fork=ca6459e，基线自动取分叉点，越界即消失。已修卡（明确「先提交再跑 check-task」、必须先 export PATH、失败用 commit --amend、不要在 worktree 检出 docs/tasks/）。GPT 本次按红线停下未提交未 push，符合要求。
- 【T-20261014-17 复核：PASS（RikkaHub-1 2026-10-14 07:10）】/etc/vps-commander/review-events.env 存在，600 root:root，121 字节，变量恰为 VPS_COMMANDER_HUB_URL 与 VPS_COMMANDER_EVENT_TOKEN，与 /root/.config/vps-commander/review-events.env 无差异；子 shell source 后 token 长度 45、healthz 200；备份 backup-pre-T17-20261014065918 的 commander.db 权限 600、integrity ok；服务未重启（NRestarts=0，启动仍 06:11:00）；06:55–07:02 审计窗口内除 GPT 的 task-reported 交付外无任何事件与通道/路由变更；通道终态未变（endpoint 非空 0、webhook_targets 0、启用路由 9）。看板改 verified。
- 【T-20261014-18 复核：PASS（RikkaHub-1 2026-10-14 07:10）】临时只读检出 /tmp/rv18 @ f101f10（复核后已删除）。改动仅 docs/recovery/REVIEWER-PLAYBOOK.md，与修订版卡片的 5 项要求逐条对应（§2 凭据路径、§2 legacy-env 无 events:emit、§3 ops 卡留存关键 API 原始响应、§5 缺响应文件即证据不足、§7 check-task 顺序与 PATH）。独立复跑：gofmt -l . 无输出；go vet、go build 退出码 0；go test -count=1 ./... 退出码 0（14 包 ok、0 FAIL）；定向 grep 路径=1、原始响应=2、commit --amend=1 均达标；check-task 退出码 0（基线自动取分叉点 ca6459e，改动都在允许范围内，规模 7 行/1 文件）；反向验证：删掉凭据路径行后 grep 计数 0，断言如期失败。看板改 in_review（PASS），待 OPC 批准后 merge。
- 【T-20261014-18 已合并（RikkaHub-1 2026-10-14 07:12）】OPC 批准后按手册 §5.7 执行：merge --no-ff docs/reviewer-credential-retention → 合并提交 ab3104c（审核岗身份），已推送 origin main；REVIEWER-PLAYBOOK.md 已含 5 处新增（§2 凭据路径、§2 legacy-env 无 events:emit、§3 ops 卡留存关键 API 原始响应、§5 缺响应文件即证据不足、§7 check-task 顺序与 PATH）。看板改 merged。
- 【待办】dev 的 worktree /root/work/vc-T-20261014-18 与分支 docs/reviewer-credential-retention 已合并但未清理；节点上已合并任务的旧 worktree 累计 11 个，清理需 OPC 同意后开卡。
- 【新卡 T-20261014-19】开发（ready，OPC 2026-10-14 07:12 批准）：验收脚本自兜底 go PATH。新增 docs/process/go-env.sh（探测 /usr/local/go/bin 等候选目录，找不到则报 `go toolchain not found` 并退出 1）、acceptance.sh 开头 source 它、新增 docs/process/test_go_env.sh 做正反两向断言。核心验收：`PATH=/usr/bin:/bin sh docs/process/acceptance.sh` 退出码 0。基线 691e967，分支 fix/acceptance-go-path。根因：acceptance.sh 依赖调用方 PATH，T-20261014-18 因此误报受阻（07:02）。
- 【T-20261014-19 复核：PASS（RikkaHub-1 2026-10-14 07:16）】临时只读检出 /tmp/rv19 @ b79bf13（复核后已删除）。改动 4 文件均在范围内（acceptance.sh、新增 go-env.sh、新增 test_go_env.sh、PROJECT-PROFILE.md）。独立复跑：gofmt -l . 无输出；go vet、go build 退出码 0；全量测试通过；**核心验收 `PATH=/usr/bin:/bin sh docs/process/acceptance.sh` 退出码 0**；`sh docs/process/test_go_env.sh` 输出 go-env tests: PASS；负向 `PATH=/usr/bin:/bin GO_ENV_CANDIDATES=/nonexistent` 退出 1 且 stderr 为 `go toolchain not found: searched GO_ENV_CANDIDATES`；source 后 cwd 不变（无 cd 副作用）；反向验证：删掉 acceptance.sh 的 source 行后最小 PATH 下复现 `gofmt: not found`（退出 127），证明修复确由本卡带来；check-task 退出码 0（基线自动取分叉点 fbaf40c，规模 25 行/3 文件）。看板改 in_review（PASS），待 OPC 批准后 merge。
- T-20261014-19 非阻塞建议：go-env.sh 用 `return`，因此只能被 source；若直接 `sh go-env.sh` 会报 shell 错误，可考虑加守卫给出清晰提示。另：成功路径不清理 GO_ENV_CANDIDATES（留在调用方环境），无实际影响。
- 【T-20261014-19 已合并（RikkaHub-1 2026-10-14 07:18）】OPC 批准后 merge --no-ff fix/acceptance-go-path → 合并提交 22fc111（审核岗身份），已推送 origin main。验收脚本自兜底 PATH 已生效：check-task 不再因调用方 PATH 缺失而误报。看板改 merged。
- 【新卡 T-20261014-20】运维（ready，OPC 2026-10-14 07:19 同意）：清理 11 个已合并任务的旧 worktree（vc-T-20261013-02 到 vc-T-20261014-19）与 13 个已并入 main 的本地分支（含不在 worktree 的 feat/legacy-webhook-retire、fix/panel-channel-config-json），保留 main 工作区与 main 分支。RikkaHub-1 已逐个核对：11 个 worktree 全部 merged=YES、dirty=0、untracked=0，合计约 19.4 MB（收益主要是整洁）。卡内要求：每个目录删前必须自检 4 条（工作区干净、无未跟踪、HEAD 已并入、非 main 工作区），只用 git worktree remove（不加 --force）、只用 git branch -d（不加 -D），任一不符就跳过并报告。基线 47ddf2a。
- 【上下文压缩策略：OPC 2026-10-14 07:26 选定方案 C，一步到位】设计定稿 docs/design/CONTEXT-BUDGET.md（RikkaHub-1 起草）。量化目标：单次工具回传 ≤4KB、生产状态核对 1 次调用、单卡复核 ≤8 次调用、交接文档加载 ≤4KB。
  - 【新卡 T-20261014-21】开发（ready）：exec 输出整形与去重。新增 `output_mode`(raw/tail/head/count/none)、`max_bytes`(默认 4096)、`dedup_window_seconds`(默认 0)；截断须带 truncated/total_bytes/total_lines/sha256 并在首行插标记；HTTP 与 MCP 字段名一致。基线 0ecaa5c。
  - 【新卡 T-20261014-22】开发（ready，**依赖 T-21 合并**）：新增只读 `GET /api/v1/health/assertions`，9 条断言（service/healthz/notification/db），授权用 devices:read 或 admin（不新增 scope），响应 ≤1.5KB 且严禁含 endpoint/webhook/密钥明文。基线 0ecaa5c。
  - 两卡都改 internal/api/api.go，故必须串行：T-21 合并后再开 T-22。
  - 本批不做 L2（会话预算统计、会话级 scratchpad、交接文档 front-matter 化），留待后续。
- 【T-20261014-21 卡修 1 次（RikkaHub-1 2026-10-14 07:29）】GPT 开工前停手上报：原卡的改动范围没有 `internal/mcp/mcp.go`，而 MCP 参数入口正在那里（`tools/list` 的 exec_command schema 在 mcp.go:807-809，参数解析 `case "exec_command"` 在 mcp.go:163 与 mcp.go:206）；`internal/api/mcp_adapter.go` 里 exec_command 只出现 1 次且是审计字符串，原卡把它当入口是误判。已把 internal/mcp/mcp.go 补入允许范围，并在卡里写明入口位置与 executor 上限落点（executor.go:19，默认 1024*1024）。GPT 未改任何文件、未建分支，处置正确。
- 【T-20261014-20 复核：PASS（RikkaHub-1 2026-10-14 07:40）】worktree 由 12 条降至 2 条、本地分支由 15 个降至 2 个（只剩 main 与当时在用的 feat/exec-output-shaping）；main 未变（63bb678）；服务 active、NRestarts=0；备份 backup-pre-T20-20261014072001 integrity ok。看板改 verified。
- 【T-20261014-21 复核：PASS（RikkaHub-1 2026-10-14 07:40）】临时只读检出 /tmp/rv21 @ 43c3f2a（复核后已删除）。8 个改动文件全部在允许范围内（+546/-21）：internal/executor/output.go（新增 ShapeOutput/NormalizeOutputOptions）、truncate_test.go、exec_output_integration_test.go、internal/api/api.go、internal/api/mcp_adapter.go、internal/mcp/mcp.go、docs/api.md、docs/client-skill/SKILL.md。独立复跑：gofmt 无输出；vet/build 退出码 0；go test -count=1 ./... EXIT=0（16 包 ok、0 FAIL）；go test -race ./internal/executor/... ./internal/api/... EXIT=0；7 个定向测试（TestShapeOutput*3、TestHTTPExecOutputModesAndDefaultTruncation、TestHTTPExecDedupHitAndExpiry、TestMCPExecOutputOptionsReachBackend、TestExecOutputOptionDefaults）全 PASS；check-task 退出码 0（基线自动取分叉点 63bb678，规模 340 行/6 文件）。反向验证：把 `if len(all) <= opts.MaxBytes` 改成 `if true` 后 TestShapeOutput 如期 FAIL（default truncation missing），已还原。看板改 in_review（PASS），待 OPC 批准后 merge。
- 【卡缺陷（RikkaHub-1 已修）】T-20261014-21 的交付命令漏了 `set -a; . /root/.config/vps-commander/notify.env; set +a`，导致 notify-agent-completion.sh 输出 SKIP、静默跳过（退出码仍 0）。已在 REVIEWER-PLAYBOOK.md §7「常见坑」与 §4 转发模板的交付行补上该步骤。GPT 停手上报、未重试，处置正确。
- T-20261014-21 非阻塞建议：HTTP/MCP 的集成测试放在 internal/executor 的外部测试包里（可运行、已通过），从归属看更贴近 internal/api；下次同类改动可考虑直接放 internal/api。
- 【T-20261014-21 已合并（RikkaHub-1 2026-10-14 07:43）】OPC 批准后 merge --no-ff feat/exec-output-shaping → 合并提交 1b4780f（审核岗身份），已推送 origin main。带入 internal/executor/output.go（ShapeOutput/NormalizeOutputOptions）、internal/api/api.go、internal/api/mcp_adapter.go、internal/mcp/mcp.go、docs/api.md、docs/client-skill/SKILL.md 与 2 个新测试文件。合并后轻量 sanity：gofmt 无输出、go build 退出码 0、executor 定向测试 ok、REVIEWER-PLAYBOOK 的 notify.env 修复未被覆盖。看板改 merged。
- 【下一步】T-20261014-22（状态断言接口）依赖 T-21，现已解锁，可转发给 GPT。
- 【遗留】T-21 合并后留下 worktree /root/work/vc-T-20261014-21 与分支 feat/exec-output-shaping 未清理（清理需 OPC 同意；可并入下次清理卡）。
- 【T-20261014-22 受阻 + 卡修（RikkaHub-1 2026-10-14 07:50）】GPT 报：提交 8e93ef7，gofmt/vet/全量测试/API race/反向验证通过，但 check-task 未执行——理由是他把约束里的「不构建发布」理解成禁止 `go build`，而 `docs/process/acceptance.sh:8` 恰好含 `go build ./...`。核实：T-22 卡 §四 验收第 1 条**明确要求** `go build ./...` 通过；PROJECT-PROFILE.md 的「不构建」指不产出发布物/不发 release；T-18/T-19/T-21 均跑过含 go build 的 check-task。已把卡片与手册 §3 模板的约束行改为「不产出发布物、不运行 release-build、不部署（go build 属验收必须执行）」，并在 §7 常见坑补上说明。8e93ef7 的 4 个文件全部在允许范围内，工作区干净。等 GPT 按澄清后重跑 check-task。
- 【T-20261014-22 复核：PASS（RikkaHub-1 2026-10-14 07:53）】临时只读检出 /tmp/rv22 @ 942997c（复核后已删除）。改动 4 文件全在范围内（+494/-22）：internal/api/health_assertions.go（新增 292 行）、health_assertions_test.go（新增 150 行）、internal/api/api.go、docs/api.md。独立复跑：gofmt 无输出；vet/build 退出码 0；go test -count=1 ./... EXIT=0（0 FAIL）；go test -race ./internal/api/... EXIT=0（无 DATA RACE）；5 个定向测试（AllPassAndBounded、NoPlaintextFailsWithoutLeakingValue、OneProbeFailureDoesNotAbortCollection、AuthorizationAndReadOnlyMethod、IntegrityCheckIsCached）全 PASS；9 条断言 id 齐全；detail 只输出计数与固定短语（如 "9 configured URLs; 0 legacy targets"），未泄露任何 endpoint/密钥值；check-task 退出码 0（基线自动取分叉点 2306488，规模 366 行/3 文件）。反向验证：把 no_plaintext 的 `OK: ok` 改成 `OK: !ok` 后 TestHealthAssertionsAllPassAndBounded 如期 FAIL，已还原。看板改 in_review（PASS），待 OPC 批准后 merge。
- 【T-20261014-22 已合并（RikkaHub-1 2026-10-14 07:56）】OPC 批准后 merge --no-ff feat/health-assertions → 合并提交 1152acb（审核岗身份），已推送 origin main。新路由已注册在 internal/api/api.go:865（`requireAnyScope(["devices:read","admin"])`）。合并后 sanity：gofmt 无输出、go build 退出码 0、api 定向测试 ok。看板改 merged。
- 【上下文瘦身 C 批 实测对比（RikkaHub-1 2026-10-14 07:56）】
  - 目标1「单次回传默认 ≤4KB」：代码就绪（internal/executor/output.go:12 `DefaultOutputBytes = 4096`；MCP schema 见 internal/mcp/mcp.go:817）——**未部署，生产未生效**。
  - 目标2「生产状态核对 1 次调用」：接口就绪（api.go:865）——**未部署，无法实测**；生产 `/api/v1/health/assertions` 返回 401（未注册）。
  - 目标3「单卡完整复核 ≤8 次工具调用」：**达标**。实测 T-22 复核周期 5 次 exec 调用（侦察/独立验收/check-task+反向验证/落盘推送/事件与投递确认）。
  - 目标4「交接文档加载 ≤4KB」：**未达标**。实测 HANDOFF 30,610 字节、BOARD 13,454、PLAYBOOK 9,701。属 L2（front-matter/摘要化），本批未做。
  - **关键结论：C 批在部署前对生产零效果。** 生产仍是 057028b46079（06:11:00 启动），要见效须先出一张部署卡把 1152acb 部署上去。
- 【新卡 T-20261014-23】运维（ready，OPC 2026-10-14 07:56 同意）：清理 T-21/T-22 的 worktree（vc-T-20261014-21、vc-T-20261014-22）与分支（feat/exec-output-shaping、feat/health-assertions）。基线 1152acb。
- 【T-20261014-23 卡修 1 次（RikkaHub-1 2026-10-14 07:58）】OPC 指示：把「清理 T-21/T-22 的 worktree 与分支」与「部署 1152acb 到生产」**合并进同一张卡**。已改写为 A 段（清理）+ B 段（部署）。B 段照 T-20261014-16 的成功模式：detached worktree @1152acb → `scripts/release-build.sh` 两次构建比对 sha256 → `deploy/deploy-hub-local.sh --dry-run` → 正式部署（只一次）→ 验证。新增关键核对：`hub.env` 的 sha256 部署前后必须一致（不得覆盖 06:11 追加的 VPS_COMMANDER_NOTIFICATION_KEY），以及用 `GET /api/v1/health/assertions` 验证加密通道未被破坏且 T-22 已生效。部署目标固定为 1152acb——它是最后一个改动代码的提交，其后 main 上的提交只改文档。
- 【L2 暂缓】上下文预算的目标④（交接文档 ≤4KB）与 L2（会话预算统计、会话级 scratchpad、交接文档 front-matter 化）**等本批（T-23）做完再安排**（OPC 2026-10-14 07:58）。
- 【T-20261014-23 复核：PASS（RikkaHub-1 2026-10-14 08:06）】A 段：worktree 剩 2 条（main + vc-deploy-1152acb）、本地分支只剩 main、MAIN 未变（4d0ec0e）。B 段：生产 sha256 = 3b215af2b13c…，**RikkaHub-1 在 /tmp/rv23 @1152acb 独立跑 `scripts/release-build.sh`，重建 sha256 与生产完全一致（可复现构建成立）**；active、NRestarts=0、08:00:05 启动、内外 healthz 200；`/etc/vps-commander/hub.env` mtime 仍 06:11:00、权限 600、NOTIFICATION_KEY 行数 1（**未被覆盖**）；备份 backup-pre-3b215af2b13c-20261014080004（旧二进制前12位 057028b46079 + commander.db）与 backup-pre-T23-clean-20261014075918 均在；近 200 行无 panic/fatal。看板改 verified。
- 【生产实测发现 T-20261014-22 的三条断言假阴性（RikkaHub-1 2026-10-14 08:06）】部署后实测 `GET /api/v1/health/assertions` 返回 6 pass / 3 fail，三条 detail 均为 "unavailable"（探针没跑起来）。根因：
  1) `service.active`/`service.restarts`：health_assertions.go:174/178 假设 `systemctl show --property=ActiveState --property=NRestarts --value` 的输出是「ActiveState 在前」，实测却是 **`0` 在前、`active` 在后** → `Atoi("active")` 失败 → 两条都记 unavailable（该命令实测仅 15–21ms，与 1200ms 超时无关）。
  2) `hub.healthz_internal`：health_assertions.go:67 写死 `http://127.0.0.1:9521/healthz`，而 Hub 只绑 192.168.20.132:9521 → 实测 127.0.0.1 返回 000、绑定地址返回 200。
  3) `hub.healthz_public`：`checkHealthz` 超时 900ms，走 Cloudflare 太短 → 同一次部署下 RikkaHub-1 实测 true、GPT 实测 false（抖动）。
  4) 测试全绿却生产失败的原因：现有测试全部注入假探针（healthAssertionProbes），**没有任何用例真正执行这三个真实探针**。
- 【审核岗自身教训（RikkaHub-1）】T-20261014-22 的复核只跑了单元/集成测试 + 反向验证，未能在生产验证真实探针——当时功能未部署，客观上做不到；**教训是：对依赖运行环境的探针，卡片验收必须要求「真实环境验证」或用真实夹具（本卡 T-24 已这样写）**。
- 【新卡 T-20261014-24】热修（ready）：修上述三条假阴性 + 补真实探针/夹具用例。基线 4d0ec0e，分支 fix/health-assertions-probes。
- 系统性遗留：`127.0.0.1` 硬编码还散落在 deploy/upgrade-hub.sh、deploy/install.sh、internal/storage/storage.go、internal/notify/notify.go、internal/mcp/oauth.go（本批只修了断言接口那一处）。建议后续开一张卡统一改为「按实际监听地址」。
- 【新卡 T-20261014-25】开发（ready，OPC 2026-10-14 08:09 批准）：升级脚本按实际监听地址做健康检查。RikkaHub-1 已逐个核实仓库内全部 `127.0.0.1` 出现处，**只有 `deploy/upgrade-hub.sh:84` 是缺陷**（升级后健康检查写死 loopback，而生产 unit 绑 192.168.20.132）；其余为安全默认（cmd/hub、cmd/mcp-stdio）、loopback 白名单守卫（health_assertions.go:208、mcp/oauth.go:25）、内部审计哨兵（storage.go:419 + notify.go:839）、测试夹具（两个测试脚本）——已在卡内列为「不要动」，避免误改。做法：新增 deploy/lib-hub-addr.sh 从 unit 的 `-addr` 推导健康检查 URL，upgrade-hub.sh 改用它，install.sh 的监听地址改为可覆盖（默认不变），新增 deploy/test_upgrade_health_url.sh 用 systemctl 桩做正反断言。基线 d3d9b3e。与 T-20261014-24 不冲突（不碰 internal/api/）。
- 【T-20261014-25 复核：PASS（RikkaHub-1 2026-10-14 08:14）】临时只读检出 /tmp/rv25 @ 70dc488（复核后已删除）。改动 4 文件全在范围内（+128/-3）：deploy/lib-hub-addr.sh（新增，暴露 hub_listen_addr / hub_health_check_url）、deploy/upgrade-hub.sh（source lib 并改用 `HUB_HEALTH_URL=$(hub_health_check_url)`）、deploy/install.sh（HUB_ADDR 可覆盖、默认 loopback）、deploy/test_upgrade_health_url.sh（新增，5 个用例）。独立复跑：gofmt 无输出；vet/build 退出码 0；`go test -count=1 ./...` EXIT=0（0 FAIL）；`sh deploy/test_upgrade_health_url.sh` 5 个用例全 PASS；**真实环境核对：在节点上 source lib 后 `hub_health_check_url` 返回 http://192.168.20.132:9521/healthz**（正是生产 unit 的绑定地址）；反向验证：把 `HUB_HEALTH_URL=$(hub_health_check_url)` 改回硬编码后，测试的 integration guard 如期报 `FAIL: case 1 ... hard-coded loopback URL`；`grep -n 127.0.0.1 deploy/upgrade-hub.sh` 无输出；「不要动」清单（internal/、cmd/、scripts/、deploy-hub-local.sh）未被碰；check-task 退出码 0（基线自动取分叉点 633071e，规模 62 行/3 文件）。看板改 in_review（PASS），待 OPC 批准后 merge。
- 【注意】T-20261014-24（热修三条假阴性）**尚未交付**——GPT 先做了 T-25。生产上 assertions 的 service.*/hub.healthz_internal 仍是假阴性，T-24 合并并部署后才转正。
- T-20261014-25 非阻塞建议：测试用 `grep` 检查 upgrade-hub.sh 的源码文本（integration guard）来防回退，属「文本耦合」，脚本重排格式时可能误报；可接受，若日后频繁改动该脚本再考虑改成行为级验证。
- 【T-20261014-24 复核：PASS（RikkaHub-1 2026-10-14 14:21）】临时只读检出 /tmp/rv24 @ e1c77d4（复核后已删除）。改动 2 文件：internal/api/health_assertions.go（+84/-9）+ 新增 internal/api/health_assertions_test.go_probes_test.go（+68）；现有 health_assertions_test.go **未被修改**。
  独立复跑：gofmt 无输出；vet/build 退出码 0；`go test -count=1 ./...` EXIT=0（0 FAIL）；`go test -race ./internal/api/...` EXIT=0；
  5 个新增用例（TestParseSystemdServiceStateProductionPropertyOrder、**TestSystemdServiceStateRealIntegration（实际执行未 SKIP）**、TestInternalHealthzURLUsesInjectedListenAddress、TestHealthzCheckTimeoutIsFiveSeconds、TestParseSystemdServiceStateRejectsInvalidValues）全 PASS；
  修法核对：`systemctl show … --property=ActiveState --property=NRestarts`（去掉 --value）后**按键名 Key=Value 解析**，与输出顺序无关；内部 healthz URL 改为从 `-addr` 解析构造（代码 217-228 行）；超时改 5s；`127.0.0.1` 只剩 274 行的 loopback 白名单守卫。
  check-task 退出码 0（基线自动取分叉点 815909e，规模 84 行/1 文件）。反向验证：先确认注入生效（对调两个 case 标签），夹具用例如期 FAIL，已还原。看板改 in_review（PASS），待 OPC 批准后 merge。
- 【重要·T-24 尚需部署才能定论】卡片要求 2「在本机部署形态下 hub.healthz_internal 必须为 ok:true」**只能在部署后验证**。因此 T-24 目前是「代码+测试 PASS」，**生产上的三条假阴性要等新二进制部署后才会转正**；合并后需一张部署卡（照 T-20261014-23 的 B 段模式），部署后复查 `GET /api/v1/health/assertions` 是否 9 条全 pass。
- T-20261014-24 非阻塞：新测试文件名 `internal/api/health_assertions_test.go_probes_test.go` 形如「xxx_test.go_probes_test.go」，疑似改名失误（应为 health_assertions_probes_test.go）。功能不受影响，但会误导后人，且它之所以通过 check-task 的允许范围检查，只是因为旧文件名恰好是它的前缀。建议合并前用 git mv + amend 改名（仍是单提交）。
- 【T-20261014-24 卡修 2 次（RikkaHub-1 2026-10-14 14:23）】OPC 指示「先解决异常文件名，再合并」。已改卡：改动范围加入 `internal/api/health_assertions_probes_test.go`（并保留旧畸形名以便 rename 的 diff 通过检查），新增要求 6（`git mv` 改名 + `git commit --amend` 保持单提交）与验收 5（`git diff --name-only main...HEAD` 不得残留 `..._test.go_probes_test.go`）。改名后 T-24 的提交 sha 会变，**RikkaHub-1 需对新 sha 重新复核**（至少核对文件清单/范围、测试、check-task），再批准合并。
- 【T-20261014-24 改名卡点 + 流程教训（RikkaHub-1 2026-10-14 14:29）】GPT 按卡修 2 次改名时，check-task 报越界：因为**修卡在主线（79c0eb9），而他 worktree 里的任务书还是旧版**，check-task 读的是工作区那份 `docs/tasks/T-20261014-24.md`。GPT 按红线停手、未 amend、未改其它文件，处置正确。解阻办法：先在开发 worktree 里 `git rebase main`（把含卡修的 main 合进来，任务书随之更新），再 `git mv` 改名、`git commit --amend --no-edit`，最后跑 check-task。**已把该教训写进 REVIEWER-PLAYBOOK §7 常见坑**：卡修后必须 rebase 到含卡修的主线，否则 check-task 按旧范围误判；也不要把 docs/tasks/ 检出到工作区。
- 【T-20261014-24 改名二次卡点（RikkaHub-1 2026-10-14 14:32）】我上一条解阻指令写了「git status 必须先干净，否则停下报告」，而 GPT 的分支上正躺着**上一轮尝试留下的已暂存改名**（`R internal/api/health_assertions_test.go_probes_test.go -> internal/api/health_assertions_probes_test.go`），于是被前置条件死锁。GPT 停手报告正确。解阻：**先用 `git commit --amend --no-edit` 把该改名并入原提交（树随即干净），再 `git rebase main`**，最后跑 check-task——这样既不丢改名，也满足 rebase 要求工作区干净。已把「rebase 前先 amend 把树弄干净、不要写死前置条件」补进 REVIEWER-PLAYBOOK §7。
- 【T-20261014-24 改名后重新复核：PASS（RikkaHub-1 2026-10-14 14:38）】新提交 **6002d8b**（parent 39a40a4，单提交，已 rebase 到最新 main）。**关键判定：与 e1c77d4 相比是纯改名**——`git diff --name-status e1c77d4 6002d8b` 显示测试文件为 `R100`（100% 相似度），且 `health_assertions.go` 与测试文件内容**逐字节相同**（`git diff --quiet` 均无差异），因此我此前在 e1c77d4 上做的测试与反向验证结论**继续有效**。文件清单只剩 internal/api/health_assertions.go 与 internal/api/health_assertions_probes_test.go，畸形名已消除。独立复跑：gofmt 无输出；vet/build 退出码 0；`go test -count=1 ./...` EXIT=0；`go test -race ./internal/api/...` EXIT=0；5 个新增用例全 PASS（含 TestSystemdServiceStateRealIntegration 实际执行）；check-task 退出码 0（基线自动取分叉点 39a40a4，规模 84 行/1 文件）。看板改 in_review（PASS，轮次 2）。**仍待 OPC 批准合并**；生产三条假阴性需部署后才转正。
- 【T-20261014-24 已合并（RikkaHub-1 2026-10-14 14:40）】OPC 批准后 merge --no-ff fix/health-assertions-probes → 合并提交 a6efa48（审核岗身份）。带入 internal/api/health_assertions.go（按键名解析 systemd 状态、内部 healthz URL 由 -addr 构造、超时 5s）与 internal/api/health_assertions_probes_test.go。合并后 sanity：gofmt 无输出、go build 退出码 0、internal 测试通过、assertions 5 个用例全 PASS。
- 【T-20261014-25 已合并（RikkaHub-1 2026-10-14 14:40）】merge --no-ff fix/upgrade-health-addr → 合并提交 **b3af938**（当前 main）。带入 deploy/lib-hub-addr.sh、deploy/upgrade-hub.sh、deploy/install.sh、deploy/test_upgrade_health_url.sh；合并后 `sh deploy/test_upgrade_health_url.sh` 通过、`deploy/upgrade-hub.sh` 已无 127.0.0.1。
- 【新卡 T-20261014-26】运维（ready，OPC 2026-10-14 14:40 批准这一次部署）：部署 b3af938 到生产，**并验证 `GET /api/v1/health/assertions` 返回 pass=9 fail=0**。流程照 T-20261014-23 的 B 段：detached worktree → release-build 两次比对 sha256 → dry-run → 正式部署 → 验证（含 hub.env 未被覆盖、备份 integrity、assertions 9 条）。**这一张同时收掉 T-20261014-24 卡片要求 2（部署形态下 hub.healthz_internal 必须 ok:true）——在此之前 T-24 只算「代码+测试 PASS」，不能说生产已修好。**
- 部署 worktree 遗留：/root/work/vc-deploy-1152acb（T-23 用过）与本次将产生的 /root/work/vc-deploy-b3af938，可在下次清理卡里一并处理。
- 【T-20261014-26 复核：PASS，生产已修好（RikkaHub-1 2026-10-14 14:44）】GPT 报「正式部署返回 exit_code=-1 且无输出」，按红线未重试。**根因是部署重启 Hub 切断了承载 exec 的会话**，不是部署失败。RikkaHub-1 分诊：服务 active running、NRestarts=0、14:43:13 启动；生产二进制 sha256 前12 = **fbcdf251decb**（= 本次新构建）；内外 healthz 200；备份 backup-pre-fbcdf251decb-20261014144312 含旧二进制（前12位 3b215af2b13c）与 commander.db（integrity ok）；hub.env mtime 仍 06:11:00（**未被覆盖**）；**`GET /api/v1/health/assertions` = pass:9 fail:0**，service.active/active、service.restarts/NRestarts=0 restarts、hub.healthz_internal/HTTP 200 均已 ok:true。
  独立重建：在 /tmp/rv26 @b3af938 跑 `scripts/release-build.sh`，sha256 = fbcdf251decb49e8eb2c6fb4… **与生产完全一致（可复现构建成立）**；近 200 行无 panic/fatal。
  **T-20261014-24 的要求 2（部署形态下 hub.healthz_internal 必须 ok:true）至此闭环**；生产三条假阴性已消除。
- 【流程教训（已写进 REVIEWER-PLAYBOOK §7）】部署卡的「正式部署」必须后台执行并写日志（`setsid nohup … > /tmp/deploy.log 2>&1 &`）再轮询，否则重启 Hub 会切断调用方会话，表现为 `exit_code=-1` 且无输出；遇到这种返回**不要重试部署**，先分诊现场（服务状态/生产 sha256/healthz/备份目录）再决定。
- 【遗留】worktree 又攒了 3 个：/root/work/vc-T-20261014-25（已合并分支）、/root/work/vc-deploy-1152acb、/root/work/vc-deploy-b3af938（两个 detached 部署工作区）。建议并入下次清理卡。
- 【新卡 T-20261014-27】运维（ready，OPC 2026-10-14 14:47 同意）：清理 T-24/T-25 的 4 个 worktree（vc-T-20261014-24、vc-T-20261014-25、vc-deploy-1152acb、vc-deploy-b3af938）与 2 个已并入 main 的分支（fix/health-assertions-probes、fix/upgrade-health-addr）。RikkaHub-1 已逐个核对全部 merged=YES/dirty=0/untracked=0；两个 detached 部署工作区无未提交内容，删掉不影响生产。基线 ae6cae7。卡内要求：每个目录删前自检四条、只用 `git worktree remove`（不加 --force）、只用 `git branch -d`（不加 -D）、**不得重启 Hub**。
- 【队列】OPC 2026-10-14 14:47 定：**T-20261014-27 完成后再进 L2**（上下文预算目标④：交接文档 ≤4KB，含 front-matter/摘要化、会话级 scratchpad、会话预算统计）。L2 的卡等本卡合并后再出。
- 【T-20261014-26 交付通知已更正（RikkaHub-1 2026-10-14 14:49 核实）】GPT 于 14:43:22 先发了「受阻」，经 OPC 授权后于 **14:45:51** 补发「**DEPLOY OK；HASH 前12位 fbcdf251decb；备份 backup-pre-fbcdf251decb-20261014144312；assertions…**」，投递 sent。两条都留在开发群里，最新的那条是正确状态。全库投递累计 71 条，全部 sent。
- 【T-20261014-27 复核：PASS（RikkaHub-1 2026-10-14 14:50）】worktree 由 5 条降至 1 条（仅 /root/work/vps-commander）、本地分支由 3 个降至 1 个（仅 main）；备份 backup-pre-T27-20261014144839 的 commander.db integrity ok；服务 active、NRestarts=0、启动时间仍 14:43:13（未重启）；生产二进制 sha256 仍 fbcdf251decb、mtime 仍 14:43:12（未重新部署）；hub.env mtime 仍 06:11:00；工作区干净；近 200 行无 panic/fatal。注：GPT 记录的 MAIN_SHA=ae156db 与当前 main=4969dfc 的差异来自审核岗同期的一条文档提交，非本卡所致。看板改 verified。
- 【队列：进入 L2（OPC 2026-10-14 14:50 定，T-27 完成后开始）】上下文预算目标④「交接文档加载 ≤4KB」尚未达成（本文件已 30KB+）。L2 拟拆三张卡：**L2-A 交接文档重构**（YAML 摘要头 + 正文 ≤4KB + 明细转 docs/recovery/HANDOFF-ARCHIVE.md，并定「每次交接只改摘要头、明细追加归档」的规则）；**L2-B 会话预算统计**（/session/stats：调用次数、回传字节、估算 token）；**L2-C 会话级 scratchpad**（memo_set/memo_get）。等 OPC 确认拆分与摘要头字段后再出卡。
## 四、非阻塞的遗留问题
- T-10 有 4 处：显式 P2 的权限规则；模板 severity 改成 P4 时缺省值怎么取；脚本的幂等键可能冲突；令牌名含【】或超过 32 rune 时缺省填充会失败。
- T-12：retry 接口能把已退役的投递重新置为 pending，只会导致发送失败，没有危害；maskLegacyWebhookURL 和 maskWebhookURL 重复。
- T-09：每次 check 都全表读一遍离线状态。
- deploy/upgrade-hub.sh 的健康检查写死了 127.0.0.1。
- 风险：sshd 允许密码登录；数据库没有异地备份。
- 节点上留着 9 个已合并任务的旧 worktree（vc-T-20261013-02 到 vc-T-20261014-10），可以开一张清理卡删掉，删之前要 OPC 同意。

- 面板体验问题：编辑通道时如果改了类型，「编辑时留空保留」的提示会误导人；普通 webhook 类型不检查企业微信的 errcode，也不阻止把 qyapi 的 URL 明文存进去。
- T-14：卡片第 3 步对 retire 幂等返回的预期表述过严（首次 false、重复 true 且 moved_tasks 为空），建议后续卡片模板统一措辞。
- 【已解决 06:44】审核岗事件凭据已落地：/root/.config/vps-commander/review-events.env（600 root，含 VPS_COMMANDER_HUB_URL 与 VPS_COMMANDER_EVENT_TOKEN = RikkaHub-1 自己的令牌；明文未进命令行、未打印）。RikkaHub-1 06:44:50 推送 review-completed 成功：event evt_GZd8MYzYBWZMs0b2eaKW，severity=P3，source=RikkaHub-1，Idempotency-Key review:T-20261014-14:bdc19a0:FAIL，投递 del_0def8b50fd0b 到 wecom-review-bot 状态 sent（HTTP 200）——**同时验证了 T-14 卡片第 7d 项（审核群路由）**。建议按旧节点惯例把该凭据移到 /etc/vps-commander/，并在手册 §2 写明路径。
- 注意：review-completed 的 result 只接受 PASS|FAIL（scripts/notify-agent-completion.sh 与历史事件均如此），没有 CHANGES REQUESTED 取值，故 T-14 的 CHANGES REQUESTED 落成 result=FAIL。

- 待办：把审核岗事件凭据从 /root/.config/vps-commander/review-events.env 迁到 /etc/vps-commander/，并在手册 §2 写明路径。
- 建议开流程改进卡：ops 卡把关键 API 原始响应落盘到备份目录（T-14 的 retire 响应已因此不可复核）。
## 五、坑
- check-task.sh 只能在任务分支上跑；「改动范围」那一行的说明文字里不要出现「允许」二字，它的 sed 是贪婪匹配。
- 卡里凡是用到 go 的步骤，都要写 export PATH=/usr/local/go/bin:$PATH。
- 运维卡的前置检查不要写死 HEAD=sha，要写成「基线是 HEAD 的祖先，且差异只在 docs/tasks/」。
- exec 跑长命令时，用 setsid nohup 放到后台、写日志再读，否则会超时；审计里的 command 字段会被截到 512 字符。
- Cloudflare 会拦 Python-urllib 的默认 UA（1010）。
- 内置 env key（legacy-env，来自 hub.env）scope 含 admin 但**不含 events:emit**；事件类探针（如 builtin-default）必须用 gpt-notify（notify.env）。admin 权限不等于 events:emit。
- notify.env 实际路径是 /root/.config/vps-commander/notify.env（不在 /etc/vps-commander/）。
- 写卡前必须核实接口入口文件：MCP 工具的 schema 与参数解析在 `internal/mcp/mcp.go`（`tools/list` 约 807 行、`callTool` 的 case 约 163/206 行），**不在** `internal/api/mcp_adapter.go`。T-20261014-21 因此卡修 1 次。


---

