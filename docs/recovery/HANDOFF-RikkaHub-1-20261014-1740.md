---
updated: 2026-10-15 11:20
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
- sws-vault v1：feat/v1-core @7468820，Hark 已终签 verified（SV-03），等 OPC 建 GitHub 库 kyaring/sws-vault，再批准合并和部署。SV-03 遗留：卡面 errata 和 3 条建议，见 sws-vault 看板。
- T-20261015-02：Hark 07:55 终签 verified @bcbda0d，待 OPC 批准合并、部署。另开 T-20261015-03（修测试偶发竞争，ready）。
- T-20261015-03：Hark 11:20 终签 verified @0512bc4（只改测试），可以和 T-02 一起合并。
- T-20261015-01（workbuddy 试用）：GPT 复审 PASS（b860eb6），等 Hark 终签。
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
