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

## 复审（2026-10-27 07:15 +08，第二轮）
对象：01 485f134、02 abffb7e、04 d978ea0、05 d53d920、03 8e72dbf；规划分支 eab6573。
- range-diff 显示变基后的原提交与上一轮逐字一致（=），本轮只审增量。
- R1 已修复：先检查 IsRegular，再判断 nlink；补了目录用例和指向目录的符号链接用例。通过。
- R2 已按 (b) 解决：新卡 05 把 Hub 本机的 REST file/write 和 MCP write_file 都改为 WriteFileAtomic，补了 6 个用例；文档与代码一致。通过。
- 建议已全部落实：02 只在属主或属组不同时才 chown；04 用 LimitReader 封顶、只读普通文件、补了 FIFO 测试；文档注明不保留的属性。
- 复现：5 张卡的 check-task 都按链上前一提交作基线，全部通过，工作区干净；go test -race 覆盖 executor 与 api，通过；以 nobody 运行 Search、WriteFileAtomic、EditBlock 的测试，通过。
- 结论：01、02、03、04、05 全部 verified（Hark 终签）。
- 遗留（不阻塞）：(1) 父目录带 setgid 位、且原文件属组等于进程 egid 时，会跳过 chown，新文件的属组会变成目录的属组，属于罕见边角；(2) 卡 01、02 文件里状态仍是 draft，待确认项没有回写到卡里，OPC 的决定只记录在提交信息中，建议回写。
- 未合并、未推送、未部署；合并顺序 01→02→04→05→03（docs/hardening-notes 头 8e72dbf 已含全部），规划分支 eab6573 一并合并；每一步都需 OPC 批准。
