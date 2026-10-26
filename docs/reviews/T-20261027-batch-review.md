# 审核报告：T-20261027-01 / 02 / 04 / 03（executor 与 Agent 文件操作加固）

审核人：Hark（第三方）　时间：2026-10-27 06:50 +08
对象：docs/hardening-notes @ af71d66（含 89bf78a、a7debc0、7f32e6b），规划分支 docs/plan-T-20261027 @ ebbabed；基线 main 3940cfc

## 结论
| 卡 | 结论 |
|---|---|
| 01 | 有条件通过：需小返工 1 项（R1） |
| 02 | 通过（附建议） |
| 04 | 通过（附建议） |
| 03 | 不通过：需返工 1 项（R2） |

## 复现
- acceptance.sh 全绿（af71d66）；go test -race ./internal/executor 通过。
- 四张卡 check-task.sh 都在各自 worktree 用材料里给的基线复跑，全部通过。
- 风险 #1 已补测：用临时非 root 用户跑 Search 测试，TestSearchSkipsUnreadableFile 实际执行并通过（临时用户已删）。
- 另做的对抗测试（仅在临时 worktree 里跑，没有提交）：两层链式符号链接 → 真实目标被改、链接保留、0640 不变；符号链接指向目录 → WriteFileAtomic 拒绝；`sleep 20 & exit 5` → 2.0 秒返回，退出码 5，非零退出码没有被吞掉。

## 必须返工
- R1（卡 01，edit.go）：目标是目录时（直接给目录，或者经符号链接指向目录），EditBlock 返回 `hardlinked file refused`。原因是目录的 nlink 恒 ≥2，而代码在判断 nlink 之前没有检查 IsRegular，这个报错是误导性的。修法：在判断 nlink 之前先检查 `!info.Mode().IsRegular()`，返回 not a regular file，与 WriteFileAtomic 一致；同时补一个用例。
- R2（卡 03，文档与代码不符）：api.md 的 /api/v1/file/write、mcp-clients.md 的 write_file、architecture.md 都写着“原子写入、保留属主、硬链接拒绝”，但 Hub 本机设备分支仍然是 os.WriteFile（internal/api/api.go:1679、internal/api/mcp_adapter.go:302），只有远程 Agent 是原子写入。卡 03 要求 3 的“Agent 与 Hub”本身就写错了。二选一：(a) 文档改为“仅远程 Agent 节点”；(b) 另开卡 05，让 Hub 本机分支也走 WriteFileAtomic（推荐，保持一致），之后文档维持现状。

## 建议（不阻塞）
- 02：只有当属主与当前进程不同时才 chown；目前会丢失 xattr、ACL、setuid 位（Perm 只保留 9 位），建议在文档里写一句。
- 04：Info 和 ReadFile 之间文件可能继续增长，建议读取时套上 io.LimitReader(MaxSearchFileBytes+1)。另外遍历时没有跳过 FIFO 和设备文件（这是旧问题，os.ReadFile 读 FIFO 会挂住），建议只读 d.Type().IsRegular() 的文件。
- 流程：卡 01、02 状态仍是 draft，“待确认（OPC）”里的硬链接策略、悬空链接、0644 都没有回复，开发按草案默认值实现。请 OPC 补确认；同时卡 01、02 写的开发者是 GPT，看板却记为 Claude，需要统一。

## 未做
未合并、未推送、未部署。合并顺序：01 → 02 → 04 → 03，合并前须完成 R1、R2，并经 OPC 批准。
