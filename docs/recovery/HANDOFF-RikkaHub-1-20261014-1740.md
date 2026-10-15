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
