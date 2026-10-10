# Legacy Key 退役与审计来源清单

基线：`main=db83ea9`（v1.1.32）；目标版本：v1.1.33。本文记录在 wjyhk 上进行的只读来源排查。未升级生产 Hub，未撤销任何现有令牌，也未修改 ToNav 配置。

| 来源 | 已确认事实 | 处理决定 / 状态 |
|---|---|---|
| `legacy-env` / `/mcp/sse` / `9.129.x` | 旧 ChatGPT 插件；按运维任务输入，用户已停用；最后一次记录为 2026-10-11 06:04。 | 不在当前生产 Hub 上改动。v1.1.33 上线后观察 `legacy_auth`，连续 48 小时为 0 才满足退役验收。 |
| `unknown(legacy)` / `127.0.0.1` / `notification` | Hub 自己投递 Webhook 时的旧审计归属。 | v1.1.33 将新事件标为 `system:notify` / `internal`；生产库只读副本上的迁移已连续执行两次，旧通知 legacy 归属归零。 |
| `66.235.106.12` / `GET /panel`（约每 5 分钟） | `m4-live-agent` 与 `wjyhk` 是同一主机：主机名相同，Hub systemd unit/drop-in 和运行进程相同。已找到请求源：ToNav 的 `handlers/health.go` 每 5 分钟执行健康检查；ToNav SQLite 中服务 ID 20，名称“VPS探针监控”，URL 为 `https://vpstool.kory.kdns.fr/panel`，`health_check_enabled=1`，未单独设置健康检查 URL。该检查将 200–399 都视为在线，因此 `/panel` 的 302 也会被认为正常。未发现对应的每 5 分钟 OpenClaw cron 或 web_tasks 常驻进程。 | 这是有明确用途的监控，不停用。尚未修改 ToNav 配置；建议把该服务的健康检查 URL 单独设为 `/healthz`（或公开探针 `/`），保留服务跳转 URL `/panel`，避免无认证访问管理页并减少请求日志噪声。 |
| `127.0.0.1` / `/api/v1/devices` / `deploy-probe` | 生产库中存在名为 `deploy-probe` 的**命名令牌**，scope 为 `devices:read`，最近使用 IP 为 `127.0.0.1`，最近使用时间为 2026-10-11 06:29:31（本地时间）。现有审计 actor 已显示 `deploy-probe`；旧审计中它对 `/metrics` / `admin` 的越权请求被拒绝。全盘未找到同名脚本文件。 | 保留令牌，不归类为 legacy。actor 归属已确认；其调用脚本的文件路径仍未定位，后续若要撤销应先检查部署流水线是否仍引用该令牌。 |
| `138.2.112.99` / 面板会话 | 按运维任务输入，已确认是用户浏览器。 | 保留为 `panel` 来源，不属于 legacy。 |
| ChatGPT 请求 `/.well-known/openid-configuration` | 连接时重复 404；不涉及 legacy key 鉴权。 | 可选处理，不阻塞退役。 |

## 上线后的退出门槛

1. 启用 v1.1.33 后，检查 `/mcp/sse`、`/mcp/message`、`/mcp` 与 REST 的 `legacy_auth` / `legacy_rejected`。
2. 旧插件来源连续 48 小时无成功鉴权；审计来源收敛为命名令牌、OAuth、面板和系统事件。
3. 若仍有 legacy 请求，先按 IP、传输、路径和 UA 定位调用方，再决定是否打开软停用开关；不要仅凭 `unknown(legacy)` 的历史数据撤销命名令牌。
