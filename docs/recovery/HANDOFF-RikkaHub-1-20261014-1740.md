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
- sws-vault v1：feat/v1-core @7468820，Hark 已终签 verified（SV-03），等 OPC 建 GitHub 库 kyaring/sws-vault，再批准合并和部署。SV-03 遗留：卡面 errata 和 3 条建议，见 sws-vault 看板。
- T-20261015-02 和 T-03 已于 11:17 经 OPC 批准合入 main（6c4f83c），已推送；生产仍是 dab72aba222d，部署要等 OPC 另行批准。sws-vault 的 GitHub 库已建好（目前为空），节点 ls-remote 可以连通。
- T-20261015-01（workbuddy）：11:37 经 OPC 批准合入 main。T-20261015-04（遗留小项打包）ready，待转发。sws-vault 的 main 和 feat/v1-core 已推到 GitHub。
- sws-vault：v1 已于 11:53 经 OPC 批准合入 main（b8ba36a）并推送；S-6 测试部署卡 SV-20261015-04 ready（部署在 Hub 本机，127.0.0.1:9531，不接反代）。
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
