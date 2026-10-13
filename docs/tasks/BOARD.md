# 任务看板

状态：draft → ready → in_dev → in_review → changes_requested → approved → merged → deployed → verified / rolled_back / cancelled
编号规则：T-<YYYYMMDD>-<序号>；flash 任务另填 DevTask ID。
维护：Hark（规划/审核）。每次状态变化当场更新本表。
恢复规划：docs/recovery/（规划书 RECOVERY-PLAN 随阶段 0 提交）。

| ID | 标题 | 开发者 | 改动范围 | 状态 | 分支 / commit | 轮次 | 审核结论 | 发布版本 | DevTask ID | 更新时间 |
|----|------|--------|----------|------|---------------|------|----------|----------|------------|----------|
| T-20261013-01 | 恢复开发环境（工具、代理、仓库） | GPT | 主机环境（非仓库） | in_review | - | 1 | 待复核：缺交付报告 | - | - | 2026-10-13 21:50 |
| T-20261013-02 | 恢复开发流程文档（docs/process、AGENTS 规则块、客户端 skill） | GPT | docs/process/ docs/client-skill/ AGENTS.md | merged | main @ c1f18cf（已推送） | 2 | PASS | - | - | 2026-10-13 23:32 |
| T-20261013-03 | 新机构建与本机部署脚本 | GPT | scripts/release-build.sh scripts/test_release_build.sh deploy/deploy-hub-local.sh deploy/test_deploy_hub_local.sh deploy/README.md | done | main @ 5ae5126 | 1 | PASS（可复现构建、生产 dry-run 已核） | - | - | 2026-10-14 00:00 |
| T-20261013-04 | 通知脚本修复（User-Agent、可诊断错误） | GPT | scripts/emit-notification-event.py scripts/notify-agent-completion.sh scripts/test_emit_notification_event.py scripts/test_notify_agent_completion.sh | merged | main @ 3aa87f7（已推送） | 1 | PASS（公网 400 已验证） | - | - | 2026-10-13 23:12 |
| T-20261014-01 | 首次本机部署演练（8cbd273 可复现构建替换生产 Hub） | GPT | 运维：/opt/vps-commander 二进制（仓库无改动） | ready | 基线 8cbd273 | 0 | - | - | - | 2026-10-14 00:08 |
