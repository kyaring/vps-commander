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
