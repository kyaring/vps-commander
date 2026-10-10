# 安全模型

适用版本：v1.1.30。

一次调用要成功，必须依次通过：**认证 → 限流 → scope → 设备白名单 → 节点安全等级**。任何一层失败都会被拒绝并写审计。

---

## 1. 认证

| 调用方 | 方式 |
| :--- | :--- |
| REST / MCP / stdio 客户端 | `Authorization: Bearer <命名令牌>` |
| Web 面板 | 面板密码登录后的服务端 Session；内部桥接令牌只在服务端使用 |
| Agent | 每设备独立凭据（WSS `Authorization` 头），Hub 只存哈希 |

- 令牌不得放在 URL 中：`?token=`、`?key=`、`?api_key=` 都不作为认证方式。
- 认证失败返回 `401`。OAuth 默认关闭；只有配置合法的 `VPS_COMMANDER_PUBLIC_URL` 后，才启用 Authorization Code + PKCE S256。授权页要求面板登录、一次性 CSRF nonce、精确注册的回调 URI，并设置防点击劫持响应头。

## 2. OAuth 客户端与授权

- OAuth 仅接受配置的公开 Origin，资源标识固定为 `VPS_COMMANDER_PUBLIC_URL/mcp`；不从请求 Host 推导。
- 客户端由管理员在面板“接入与令牌”中创建，Client Secret 只显示一次；不支持动态客户端注册。
- 授权请求必须使用 `response_type=code`、精确注册的 `redirect_uri`、`state`、PKCE `S256` 和匹配的 `resource`。
- 用户登录面板后，选择一个有效的命名令牌和本次 scope 子集。OAuth 永远不能申请 `admin`，最终权限受命名令牌权限与授权 scope 的交集约束。
- 授权码短时有效且一次性使用。Token 端点支持 `client_secret_basic` 和 `client_secret_post`，兑换时重新验证 PKCE 与 redirect URI。
- Access Token 有效期 1 小时；Refresh Token 采用滑动有效期：每次刷新续期 30 天（30 天未使用即失效），自首次授权起绝对上限 360 天，到期需重新授权；并采用轮换与重放检测。停用客户端、吊销命名令牌或撤销授权都会使相关 OAuth Token 失效。
- OAuth 和直接命名令牌共用限流、exec 并发和 MCP 会话额度；审计 Actor 会追加 `via=oauth:<client_id>`。

## 3. 命名令牌

每个客户端一个独立令牌，在面板“接入与令牌”中管理，或用 `admin` 令牌调用 `/api/v1/tokens`。

| 属性 | 说明 |
| :--- | :--- |
| 名称 / 备注 | 审计中的“操作人”，名称唯一 |
| 前缀 | `vpc_` 开头的前若干位，用于辨认；完整密钥只在创建时显示一次 |
| scope | 见下表 |
| 设备白名单 | 为空表示全部设备；勾选后只允许所选设备 |
| 限速 | 每秒请求数（默认 10），超出返回 `429` + `Retry-After` |
| 最大会话数 | MCP 会话上限（默认 10） |
| 状态 | 有效 / 已吊销；记录最近使用时间与 IP |

面板提供三种模板：**只读**（`devices:read`、`files:read`）、**运维**（除 `admin` 外全部 6 项）、**全部**（含 `admin`，创建时需二次确认）。

面板展示新密钥时：只出现在一次性密钥框中，可复制；点击“刷新”、“我已保存，隐藏”、编辑其他令牌或再建新令牌都会清除它，未复制时会先确认。密钥不会写入 URL、localStorage 或浏览器控制台。

### Scope

| scope | 允许 |
| :--- | :--- |
| `devices:read` | 设备列表与状态 |
| `files:read` | 读取 / 列出 / 搜索文件，白名单诊断，查看 Agent MCP |
| `exec` | 执行 Shell 命令 |
| `files:write` | 写入、编辑、创建目录、移动文件 |
| `process` | 命令会话与进程管理 |
| `mcp:call` | 调用 Agent 上挂载的 MCP 工具 |
| `admin` | 令牌、安全策略、Agent MCP 服务、通知、审计查询与导出、`/metrics` |

缺少 scope 返回 `403 scope denied`；设备不在白名单返回 `403`。MCP `tools/list` 只返回当前令牌可用的工具。

### legacy 密钥

`VPS_COMMANDER_API_KEY` 是安装时生成的全权限密钥，仅用于初始化。迁移步骤：

1. 为每个客户端创建命名令牌并切换；
2. 在“日志”中按传输方式 `legacy` 筛选，确认一段时间内没有 legacy 调用；
3. 在 `hub.env` 设置 `VPS_COMMANDER_LEGACY_KEY=off` 并重启 Hub，或在面板“安全与通知”中轮换 legacy 密钥。

## 4. 节点安全等级

每个工具在 Tool Registry 中有固定风险等级，节点的安全等级决定允许的最高风险：

| 等级 | 允许 | 典型工具 |
| :--- | :--- | :--- |
| Low | 只读 | `read_file`、`read_multiple_files`、`list_processes`、`diagnostic_command`、文件列表 / 搜索 |
| Medium | Low + 文件修改、Agent MCP | `edit_block`、`write_file`、`create_directory`、`move_file`、`call_agent_mcp` |
| High | 全部 | `exec_command`、进程启动 / 交互 / 终止 |

- 全局等级 + 单节点覆盖，在面板“安全与通知”中设置；策略持久化在 SQLite，带单调递增版本号，不允许回退。
- 未注册或离线的远程节点解析为 `unknown`，一律拒绝；不会继承全局等级。
- 风险等级由 Hub 强制执行，不是给客户端的提示。

## 4. 审计

每次调用（含被拒绝的）写入 `audit_logs`：时间、操作人（令牌名称 / 面板 / stdio / legacy）、传输方式（`rest`、`mcp-http`、`mcp-sse`、`panel`、`stdio`、`legacy`）、客户端 IP、目标设备、动作、命令或路径、结果（`ok` / `error` / `denied`）、耗时。

- 命令与路径先脱敏再截断到 512 字符；`write_file` 不记录文件内容。
- 只读列表类成功操作默认不写审计（`VPS_COMMANDER_AUDIT_READ_LIST=on` 可开启）。
- 默认保留 90 天（`VPS_COMMANDER_AUDIT_RETENTION_DAYS`）。
- 面板“日志”支持按操作人 / 设备 / 动作 / 结果 / 传输方式 / 时间筛选、游标分页与 CSV 导出；也可用 `admin` 令牌调用 `/api/v1/audits` 和 `/api/v1/audits/export.csv`。

## 5. 客户端 IP 与反向代理

开启 `VPS_COMMANDER_TRUST_PROXY=on` 后，Hub 只信任 `X-Forwarded-For` 最右侧的有效 IP，不信任 `X-Real-IP`。前提是反代覆盖或正确追加 XFF，并且 Hub 端口无法绕过反代直接访问。详见 [deploy/hub.md](deploy/hub.md#5-反向代理)。

## 6. 已知边界

- Hub 所在主机上的本机 Agent / 本机执行器以 root 运行。对 Hub 主机授予 `files:read`、`exec` 或 `files:write`，实际可读取 Hub 的配置与数据库，等同于管理员权限。给非管理员客户端发令牌时，建议在设备白名单中排除 Hub 主机。长期方案是 Agent 非 root 化并隔离 Hub 的密钥与数据目录（见 [roadmap.md](roadmap.md)）。
- `exec` 是任意 Shell，令牌一旦泄露即可在允许的节点上执行命令；请为每个客户端单独发令牌、按需最小授权，并定期查看审计。
