# 任务看板

状态：draft → ready → in_dev → in_review → changes_requested → approved → merged → deployed → verified / rolled_back / cancelled
编号规则：T-<YYYYMMDD>-<序号>；flash 任务另填 DevTask ID。
维护：Hark（规划/审核）。每次状态变化当场更新本表。
恢复规划：docs/recovery/（规划书 RECOVERY-PLAN 随阶段 0 提交）。

| ID | 标题 | 开发者 | 改动范围 | 状态 | 分支 / commit | 轮次 | 审核结论 | 发布版本 | DevTask ID | 更新时间 |
|----|------|--------|----------|------|---------------|------|----------|----------|------------|----------|
| T-20261013-01 | 恢复开发环境（工具、代理、仓库） | GPT | 主机环境（非仓库） | in_review | - | 1 | 待复核：缺交付报告 | - | - | 2026-10-13 21:50 |
| T-20261013-02 | 恢复开发流程文档（docs/process、AGENTS 规则块、客户端 skill） | GPT | docs/process/ docs/client-skill/ AGENTS.md | in_review | docs/restore-process @ 18cb972 | 1 | 内容通过、交付报告已补；阻塞：GPT 执行环境无 events:emit 令牌，开发完成未推送 | - | - | 2026-10-13 22:36 |
