---
name: dev-process
description: 多 agent 协作开发流程与角色规范。在任何接入本流程的项目里接任务、写任务书、开发、审核或发布时使用。
---

# 开发流程与角色规范 v1.1

正本：dev-process 仓库。各项目内的 DEV-PROCESS.md、SKILL.md、check-task.sh、AGENTS.md 规则段都是副本，只能由 sync.sh 更新，不得在项目里直接改。项目细节（命令、路径、发布方式、开发者、指定模型）写在该项目的 docs/process/PROJECT-PROFILE.md。

## 开工前必读（按顺序）
1. 仓库根目录 AGENTS.md
2. docs/process/DEV-PROCESS.md
3. docs/process/PROJECT-PROFILE.md
4. docs/tasks/<任务编号>.md（任务书）。没有任务书，不改代码。

角色由派活方指定；未指定按「开发者」处理。

## 流程与状态
需求(OPC) → 任务书(规划) → OPC 确认 → 开发 → 提交审核 → 审核 → OPC 决定并入 → 构建切入生产（备份 + 健康检查 + 自动回滚）→ OPC 实测 → 关闭

状态：draft → ready → in_dev → in_review → (changes_requested ↺ in_dev) → approved → merged → deployed → verified / rolled_back / cancelled

## 铁律
1. 一个任务只有一个开发者；同时进行的任务不得重叠同一文件或模块。
2. 任何生产变更（部署、改配置、改主机）必须 OPC 逐步批准。
3. AI 模型只用 OPC 指定的（见项目档案），不得自行切换；受限或失败如实报告。
4. 快速迭代期只提交本地主线，不 push、不打 tag、不发版；何时结束、版本节奏由 OPC 决定。
5. 密钥、令牌、密码不得出现在任务书、提交、日志、审核报告里（需要时用文字描述格式）。
6. 通知优先走事件，不靠轮询。

## 角色
### OPC（产品 / 实测 / 决策）
提需求（问题、可验收的期望、优先级、是否涉及生产、约束）；确认任务书后才开工；审核通过后决定并入 / 部署 / 暂缓 / 打回；部署后实测。

### 规划
- 先核实现状（读代码、查生产），不凭记忆写任务书。
- 拆成可独立审核的任务。非测试代码上限：flash 类 ≤300 行、≤6 文件；GPT 类 ≤600 行、≤12 文件。超出须拆分，或写明理由并经 OPC 同意。
- 指定开发者，确认不与进行中任务重叠；登记到看板。
- 交给 DevTask runner 的任务：路径只写真实前缀（不支持通配）；验收命令必须在允许命令列表内并原样照写；规则全部写进任务卡，不依赖它读到 AGENTS.md。

### 开发者
- 只做任务书写的事，只改「改动范围」允许的路径；越界先问，不自己扩大范围。
- 从任务书指定基线新建分支。不 push、不打 tag、不部署、不登录或修改生产主机和生产数据。
- 每个行为变化有测试；修 bug 先写复现测试。
- 不读取、不打印、不提交任何密钥。
- 不修改 docs/process/、docs/tasks/BOARD.md、AGENTS.md 规则段及项目档案列出的受保护路径。
- 交付前运行 sh docs/process/check-task.sh <任务编号>，全部通过后提交审核。

### 审核
- 独立复跑 check-task.sh 和全部验收命令，加密钥扫描。
- 对照任务书逐条验收；查越界、回归、安全（鉴权、脱敏、注入、路径穿越）；能实测的实测。
- 结论三选一：PASS / PASS+建议 / CHANGES REQUESTED。任务书不符直接打回。
- 发审核事件，更新看板。不替开发者改代码，不自行合并或部署。审核不设 SLA。

### 发布（OPC 明确批准后）
主线 fast-forward 到审核通过的 commit → 按项目档案构建、预演 → 部署（先备份、健康检查、失败自动回滚，看到成功标志才算上线）→ 通知 OPC 实测 → 更新看板。

## 任务书模板（前几行格式固定，check-task.sh 按它解析）
```
任务：<一句话标题>
编号：T-<YYYYMMDD>-<序号>
开发者：<见项目档案；含 flash 按 flash 上限>
分支：基于 <分支>（<sha>），新建 <fix|feat>/<主题>
约束：不 push、不打 tag、不部署
改动范围：允许 <路径前缀…>；禁止 <路径前缀…>

一、问题：现象 + 影响 + 证据
二、要求：可判定的行为，状态码/字段/阈值写死；不做：明确排除项
三、测试：与要求一一对应，输入 → 期望结果
四、验收：项目档案中的命令，原样照写
五、交付：提交信息 <type>(<模块>): <英文一句话>；需更新的文档；完成后发完成通知，把 SHA 交审核
```
写法：数字写死，不留“适当”“合理”。

## check-task.sh（交付前自检）
`sh docs/process/check-task.sh <任务编号> [基线commit]`
检查：任务书存在；改动全在「允许」前缀内且未碰「禁止」前缀；非测试代码规模不超限；无未提交/未跟踪文件；git diff --check 干净；调用项目的 docs/process/acceptance.sh。未显式传基线时，从本分支与 main 的分叉点开始算。

## 看板
项目仓库 docs/tasks/BOARD.md 总表（ID、标题、开发者、改动范围、状态、分支/commit、轮次、审核结论、发布版本、DevTask ID、更新时间）+ 每任务一份 docs/tasks/<编号>.md。规划/审核方每次状态变化当场更新。

## 新项目接入
1. sync.sh <项目仓库>：放入 DEV-PROCESS.md、SKILL.md、check-task.sh 副本，AGENTS.md 规则段，CLAUDE.md 指针。
2. 填 PROJECT-PROFILE.md，写 docs/process/acceptance.sh（失败非零退出），再跑一次 sync.sh 把档案并进项目 SKILL.md。
3. 建空的 docs/tasks/BOARD.md。
4. 用 DevTask 的：在 Hub 注册项目并起 runner 实例。
5. OPC 确认项目档案后开始接任务。

## 新 agent 接入
先派一张小任务卡（如补一个测试），审核通过再派正式任务。

## 修改本规范
由 OPC 提出或批准。在 dev-process 仓库里一起更新 DEV-PROCESS.md、SKILL.md、templates/（AGENTS-BLOCK.md、check-task.sh、模板），提交后用 sync.sh 同步到每个项目并在各项目提交。项目档案改动后也要重跑 sync.sh，让项目 SKILL.md 跟上。
