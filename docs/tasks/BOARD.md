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
| T-20261014-01 | 首次本机部署演练（8cbd273 可复现构建替换生产 Hub） | GPT | 运维：/opt/vps-commander 二进制（仓库无改动） | verified | 生产 sha256 fde576ef07f8（8cbd273） | 3 | PASS（独立重建一致，备份 integrity ok） | - | - | 2026-10-14 00:30 |
| T-20261014-02 | 审计 HTTP 2xx 记为 ok（恢复 T-20261012-03） | GPT | internal/api/audit_exec_result.go internal/api/audit_exec_result_test.go internal/api/notification_events_test.go docs/client-skill/SKILL.md | deployed | main @ 84fbd47（已推送，已部署 T-20261014-03） | 1 | PASS | - | - | 2026-10-14 00:48 |
| T-20261014-03 | 部署 84fbd47（审计 2xx 修复）到生产 Hub | GPT | 运维：/opt/vps-commander 二进制（仓库无改动） | verified | 生产 sha256 00461fe5d20d（84fbd47） | 1 | PASS（独立重建一致，审计 202|ok 已实测） | - | - | 2026-10-14 00:58 |
| T-20261014-04 | auditutil 单独 Bearer 脱敏 + UTF-8 安全截断（恢复 44cd806+35d7f04） | GPT | internal/auditutil/auditutil.go internal/auditutil/redact_fix_test.go | deployed | fix/auditutil-bearer-utf8（基线 abb5dd3） | 0 | 6a1c3a3（原 3da5410，变基后内容一致） | PASS（Hark 01:10，gofmt/vet/build/全量/race/check-task 通过，独立探针通过；OPC 01:11：等 T-20261014-05 修完再一起合并）；合并 540866c | evt_0tTH6iLvfTZIvwCToK1u | 2026-10-14 01:38 |
| T-20261014-05 | auditutil Authorization: Basic/Negotiate/NTLM/Token 凭据脱敏 | GPT | internal/auditutil/auditutil.go internal/auditutil/redact_basic_test.go | deployed | fix/auditutil-bearer-utf8（先变基到 main，基线 3da5410） | 0 | 0a3a958 | PASS（Hark 01:25，gofmt/vet/build/全量/race/check-task 通过，独立探针通过）；合并 540866c | evt_jstHW74Mbjayiy9tdunt | 2026-10-14 01:38 |
| T-20261014-06 | 部署 540866c（auditutil 脱敏修复）到生产 Hub | GPT（运维） | （仓库无改动） | verified | 基线 540866c，detached worktree 构建 | 0 | 540866c → 119d510cadbe | PASS（Hark 01:38：独立重建 sha 一致；active，NRestarts=0；内网和公网 healthz 正常；备份 integrity ok；生产探针 Basic/Bearer 都是 [REDACTED]） | evt_b1SoD7QB0hBfETGN4woY | 2026-10-14 01:38 |
| T-20261014-07 | T-07 核对结论落地：审计面板列 / 拒绝行 / 传输区分的回归测试（功能在 891904a 里已有） | GPT | internal/web/audit_ui_test.go internal/api/mcp_audit_denied_regression_test.go | merged | test/audit-panel-regression（基线 593e4a2） | 0 | 6fa1f9c | PASS（Hark 02:05，gofmt/vet/build/全量/race/check-task 通过；node SKIP；反向验证删「传输」列后失败）；合并 bad2f3a（OPC 02:04 批准，只加测试，不部署） | evt_7KbtBTUPEyXF90zpdsY0 | 2026-10-14 02:06 |
| T-20261014-08 | T-08 企业微信 wecom_bot 渠道 + 内置 default 模板 + 旧 webhook 写接口 410（重做 ffad0c1） | GPT | internal/storage/notification_store.go internal/storage/notification_v1.go internal/notify/outbox.go internal/api/notifications_v1.go internal/api/api.go internal/web/static/index.html internal/web/static/app.js + 3 个新测试 | approved | feat/wecom-bot-channel（基线 96938a8，卡修 2 次） | 0 | c7b9210 | PASS（Hark 02:40，check-task/race/定向测试复跑通过；errcode 不重试、5xx 重试、key 不泄露；410 只覆盖 update/delete）；待 OPC 决定合并 | evt_fkEjocSCen3zkN2l1IjT | 2026-10-14 02:40 |
