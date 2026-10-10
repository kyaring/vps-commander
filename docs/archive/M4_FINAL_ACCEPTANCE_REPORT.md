# VPS-Commander M4 最终验收报告（待人工签收）

> **归档说明（2026-10-10）**：本文为历史快照，反映写作当时的状态，可能与现行 v1.1.30 不一致，不作为当前行为依据。已脱敏：域名、节点名与本地路径替换为占位符。现行文档见 [docs/README.md](../README.md)。

> 文档属性：历史验收快照（2026-09-28）。当前状态以 README、DOCUMENTATION_BASELINE_20261002 和当前验收门禁为准。

日期：2026-09-28
状态：🟡 技术收尾完成；管理面板 UI 已人工验收并上线，M4 总体验收仍等待 Custom GPT Actions 人工签收

## 已完成

- Hub/Agent systemd 固化，Hub 监听 `127.0.0.1:9521`。
- Lucky 正式 HTTPS：`https://hub.example.com`。
- OpenAPI 3.1.0，4 个 Actions path，统一 `bearerAuth`。
- Hub 敏感凭据仅允许通过环境变量注入，禁止 CLI 参数。
- Cluster Secret 仅通过 WebSocket Authorization Header，不进入 URL。
- 单一 `node-d` 正式实例通过 WSS 注册在线。
- 本机执行、远程执行、文件读取、文件写入、超时回收、401 鉴权均完成真实回归。
- SQLite 审计链路已确认记录请求。
- 临时凭据文件已清理，生产环境凭据文件权限为 0600。
- Hub RSS 当前约 14.5MB；Agent 经 `GOGC=50` 优化后约 10.5MB，均低于 25MB/12MB 硬上限；20MB/10MB 仍为优化目标。

## 尚未签署

用户仍需在 Custom GPT 中：
1. 导入 `https://hub.example.com/openapi.json`。
2. 配置 Bearer API Key。
3. 实测设备列表、本机 exec、远程 exec、文件读写和错误/超时。
4. 完成后由用户人工确认 M4 通过。

在此之前，本报告不得视为 Release Ready / M4 Passed。

- 管理面板 UI 已完成手机端多 Agent 适配与深/浅双态主题；用户人工验收通过后已重新构建 Hub 并重启 systemd，线上 embed 静态资源已立即生效。
