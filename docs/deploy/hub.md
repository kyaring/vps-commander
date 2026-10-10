# Hub 部署

适用版本：v1.1.30。示例域名统一使用 `hub.example.com`，请替换为你自己的域名；文中所有密钥均为占位符。

---

## 1. 系统要求与端口

- Linux `amd64` / `arm64` / `armv7`，systemd。
- Hub 默认监听 `127.0.0.1:9521`，不直接暴露公网；公网入口由反向代理终止 TLS。
- 数据库：SQLite，默认 `/opt/vps-commander/data/commander.db`。
- 配置：`/etc/vps-commander/hub.env`，权限 `0600`。

## 2. 一键安装

```bash
curl -fsSL https://github.com/kyaring/vps-commander/releases/latest/download/install.sh | sudo bash
```

选择 Hub 后，脚本会：

1. 识别架构并从最新 Release 下载 `vps-commander-hub-linux-<arch>`，按 `checksums.txt` 做 SHA256 校验；
2. 生成 `hub.env`（随机 `VPS_COMMANDER_API_KEY` 与面板密码），权限 `0600`；
3. 写入并启动 `vps-commander-hub.service`。

安装结束时会显示一次面板密码和 API Key，请立即保存。该 API Key 是 legacy 全权限密钥，仅用于初始化；日常客户端请使用面板创建的命名令牌（见 [../security.md](../security.md)）。

## 3. 手工安装

适合固定版本、离线部署或灰度验证。

```bash
install -d -m 0750 /opt/vps-commander/data /etc/vps-commander
install -m 0755 vps-commander-hub-linux-amd64 /opt/vps-commander/vps-commander-hub
cat > /etc/vps-commander/hub.env <<'ENV'
VPS_COMMANDER_WEB_PASSWORD=<面板密码>
VPS_COMMANDER_API_KEY=<初始化用 legacy 密钥>
VPS_COMMANDER_TRUST_PROXY=on
ENV
chmod 0600 /etc/vps-commander/hub.env
```

systemd 单元（与 `deploy/vps-commander-hub.service` 一致）：

```ini
[Service]
Type=simple
User=root
WorkingDirectory=/opt/vps-commander
EnvironmentFile=-/etc/vps-commander/hub.env
ExecStart=/opt/vps-commander/vps-commander-hub -addr 127.0.0.1:9521 -db /opt/vps-commander/data/commander.db
Restart=on-failure
RestartSec=3s
KillSignal=SIGTERM
TimeoutStopSec=20s
LimitNOFILE=65536
Environment=GOGC=50
```

```bash
systemctl daemon-reload
systemctl enable --now vps-commander-hub
curl -fsS http://127.0.0.1:9521/healthz
```

命令行参数只有两个：`-addr`（默认 `127.0.0.1:9521`）和 `-db`（默认 `data/commander.db`）。所有凭据只通过环境变量注入，不要放进 `ExecStart`。

## 4. 环境变量全表

以下变量与 `cmd/hub`、`internal/*` 中的 `os.Getenv` 一一对应。

| 变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `VPS_COMMANDER_WEB_PASSWORD` | 无（必填） | 管理面板登录密码。 |
| `VPS_COMMANDER_API_KEY` | 无 | legacy 全权限密钥（含 `admin`）。未设置 `LEGACY_KEY=off` 时必填。 |
| `VPS_COMMANDER_LEGACY_KEY` | 开启 | 设为 `off` 后 legacy 密钥立即失效，此时可不设 `API_KEY`。所有客户端迁移到命名令牌后建议关闭。 |
| `VPS_COMMANDER_ADMIN_TOKEN` | 等于面板密码 | 面板到内部管理接口的桥接令牌（`X-Admin-Token`），只在服务端使用，不发送给浏览器。 |
| `VPS_COMMANDER_TRUST_PROXY` | 关闭 | 设为 `on` 时从 `X-Forwarded-For` 最右侧有效 IP 取客户端地址（不信任 `X-Real-IP`）。位于反代之后必须开启。 |
| `VPS_COMMANDER_CORS_ORIGINS` | 空 | 逗号分隔的精确 Origin 白名单；不接受 `*`。为空表示不允许跨域浏览器访问。 |
| `VPS_COMMANDER_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`。 |
| `VPS_COMMANDER_AUDIT_RETENTION_DAYS` | `90` | 启动时清理早于该天数的审计记录。 |
| `VPS_COMMANDER_AUDIT_READ_LIST` | 关闭 | 设为 `on` 时，只读列表类成功操作（`list_devices`、`list_agent_mcp`、`list_processes`、REST 文件列表/信息/搜索）也写审计。失败与拒绝始终记录。 |
| `VPS_COMMANDER_PROFILE_INTERVAL_SECONDS` | `300` | Agent 资源画像最长上报间隔，经心跳下发给 Agent。 |
| `VPS_COMMANDER_EXEC_CONCURRENCY` | `32` | 全局 exec 并发上限。 |
| `VPS_COMMANDER_SEARCH_CONCURRENCY` | `8` | 全局文件搜索并发上限。 |
| `VPS_COMMANDER_TOKEN_EXEC_CONCURRENCY` | `2` | 单个令牌的 exec 并发上限，超出返回 `429`。 |
| `VPS_COMMANDER_INSTALL_COMMAND` | 空 | 签发设备凭据时随结果返回的安装命令模板，便于在面板复制。 |
| `VPS_COMMANDER_CLUSTER_SECRET` | 空 | 旧版共享密钥。仅用于测试 / 压测环境；生产不要设置，Agent 一律使用每设备独立凭据。 |

## 5. 反向代理

反代需要转发以下路径到 `127.0.0.1:9521`：`/agent/ws`（WebSocket Upgrade）、`/mcp`、`/mcp/sse`、`/mcp/message`、`/api/`、`/panel/`、`/static/`、`/openapi.json`、`/healthz`、`/.well-known/`、`/oauth/`。

要点：

- `/agent/ws` 必须支持 WebSocket Upgrade，读超时不低于数分钟。
- `/mcp/sse` 是长连接 SSE，需关闭响应缓冲（Nginx：`proxy_buffering off;`）。
- 代理必须**覆盖或正确追加** `X-Forwarded-For`，保证最右侧是真实客户端 IP；同时保证 Hub 端口不能绕过代理被直接访问，否则 XFF 可被伪造。
- 上线前用带伪造 `X-Forwarded-For` / `X-Real-IP` 的请求验证：审计和限流记录的 IP 应是真实客户端 IP。

Nginx 示例：

```nginx
location / {
    proxy_pass http://127.0.0.1:9521;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;   # 覆盖，不信任客户端传入值
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_buffering off;
    proxy_read_timeout 600s;
}
```

Caddy 示例：

```caddy
hub.example.com {
    reverse_proxy 127.0.0.1:9521 {
        flush_interval -1
    }
}
```

## 6. 部署后检查

```bash
systemctl status vps-commander-hub
journalctl -u vps-commander-hub -n 100
curl -fsS http://127.0.0.1:9521/healthz
```

然后登录 `https://hub.example.com/panel/`，在“接入与令牌”中创建第一个命名令牌，并在“安全与通知”中确认全局安全等级。

下一步：[接入 Agent](agent.md)。
