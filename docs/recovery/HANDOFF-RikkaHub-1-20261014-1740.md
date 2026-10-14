---
updated: 2026-10-14 17:31
holder: RikkaHub-1
main: a251707
production_sha256: dab72aba222d
production_started: 2026-10-14 17:05:48
service: active
open_cards: [T-20261014-35]
next_batch: [DevTask 恢复 A2…A6 → B]
archive: docs/recovery/HANDOFF-ARCHIVE.md
---

# 交接文档（RikkaHub-1；摘要头 + ≤4KB 正文；明细见 HANDOFF-ARCHIVE.md）

**规则（L2-A）**：本文件只保留 YAML 摘要头与 ≤4KB 正文；明细一律追加到 `docs/recovery/HANDOFF-ARCHIVE.md` 顶部。
规则：回复 OPC 前先更新本文件并推送；审核岗按主线直接下卡（状态写 `ready`）。
铁律：生产变更与合并逐步经 OPC 批准；卡片/提交/日志/聊天绝不出现令牌、webhook、密码；不读 `hub.env`/`notify.env`；每张卡附可直接转发给 GPT 的交接消息，由 OPC 转发。
必读：`REVIEWER-PLAYBOOK.md` → 本文 → `docs/tasks/BOARD.md`。

## 一、当前状态
- 仓库：main = origin/main = `__MAIN__`；工作区干净；**worktree 与本地分支均只剩 main**。
- 生产：`39f3c5c` 的构建，sha256 `dab72aba222d…`，**17:05:48 启动**（T-33 部署）；active、NRestarts=0；内外 healthz 200。
- 备份：`backup-pre-dab72aba222d-20261014170547`（旧二进制 `610212a58fac` + db，integrity ok）。
- 通知：三群 wecom_bot 路由齐全；deliveries 89 条全 sent。
- 已生效：exec 4KB 截断与去重；`health/assertions` 9/9；`session/stats`；`memo_set`/`memo_get`（只存内存）。
- 审核岗凭据：`/etc/vps-commander/review-events.env`（600 root），推通知前先 source。
- 工号：OPC=SWS-H000-opc；GPT=SWS-E002-gpt。

## 二、本班（RikkaHub-1）与遗留
- 完成 T-17～T-34，全部 `verified`/`merged`（要点与明细见 `HANDOFF-ARCHIVE.md` 顶部归档节）。方案 C 四项量化目标全部达标。
- **遗留**：T-20261014-35（DevTask A1）**已出卡但暂缓、未转发**；三条小尾巴未恢复：`T-20261012-01` auth 单测、`T-20261012-02` executor 单测、「工具错误可恢复 / 失败输出保留尾部」。
## 三、下一批卡
1. **DevTask 恢复**（wjyhk 仍不可达，走 STATE-20261013.md §6 重写路线；a~d 已恢复，只剩 e）：A1 storage（T-35 待转发）→ A2 scope → A3 REST+runner lease → A4 7 个 MCP 工具 → A5 runner → A6 E2E+docs；B（面板+通知）需 A 验收并经 OPC 确认。规格见 `docs/recovery/salvage/devtask/`。
2. 三条小尾巴（见「二」）。
3. 更后：员工表 B → 面板卡 C → 令牌整理 → 保险库 V-A～V-F（见 `docs/recovery/VAULT-PLAN.md`）。

## 四、遗留（摘要）
- 明细见归档与 PLAYBOOK §7。重点：**sshd 允许密码登录、数据库无异地备份**；exec 通道偶发失效（建议后续做「MCP 会话跨 Hub 重启存活」）；T-10/T-12/T-09 若干非阻塞项；`127.0.0.1` 硬编码除 `deploy/upgrade-hub.sh`（已修）外均属有意为之。
## 五、坑（要点，完整见 PLAYBOOK §7）
- check-task 只能在任务分支跑；「改动范围」说明里不要出现「允许」。
（其余坑见 PLAYBOOK §7。）
- **开发卡 worktree 必须从「含本卡的 main」分出**，不要从基线 sha 分出；卡修后先 `git rebase main` 再跑 check-task。
- **每次 exec 都是新 shell**：`export PATH` 不跨调用保留；`go`/`gofmt` 在 `/usr/local/go/bin`（T-32 已软链，绝对路径最稳）。
- 推交付通知前必须 `cd` 到仓库根、再 source `notify.env`、再跑 `scripts/notify-agent-completion.sh`，否则 SKIP 且退出码 0。
- 部署必须后台执行写日志再轮询；`exit_code=-1`/进程消失通常是重启切断会话，**不要重试部署**，先分诊现场。
