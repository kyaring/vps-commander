# Agent 部署

适用版本：v1.1.30。示例域名为 `hub.example.com`，凭据均为占位符。

Agent 只主动向 Hub 发起 WSS 连接（`wss://hub.example.com/agent/ws`），节点无需开放任何入站管理端口。

---

## 1. 签发设备凭据

每台 Agent 使用独立凭据，Hub 只保存其哈希。

1. 登录面板 → “设备”分区 → “设备接入” → “添加设备”；
2. 输入节点名称，点击“生成接入凭证”；
3. 立即复制返回的凭据（只显示一次）。若 Hub 配置了 `VPS_COMMANDER_INSTALL_COMMAND`，还会同时显示安装命令模板。

也可以用带 `admin` scope 的令牌调用 `POST /api/v1/security/device-credentials/provision`。

## 2. 一键安装

```bash
curl -fsSL https://github.com/kyaring/vps-commander/releases/latest/download/install.sh | sudo bash
```

选择 `1) 安装受控端 Agent`，依次输入：

- Hub WSS 地址：`wss://hub.example.com/agent/ws`
- 本节点名称（须与签发凭据时一致）
- Agent 独立凭据

脚本会校验 SHA256、写入 `/etc/vps-commander/agent.env`（`0600`）、注册 systemd 服务并确认 Agent 已连上 Hub。

> 注意：当前 `install.sh` 的 Hub 地址提示带有默认值，请务必输入你自己的地址，不要直接回车。

## 3. 手工安装

```bash
install -d -m 0750 /etc/vps-commander /opt/vps-commander
install -m 0755 vps-commander-agent-linux-amd64 /opt/vps-commander/vps-commander-agent
cat > /etc/vps-commander/agent.env <<'ENV'
VPS_COMMANDER_HUB_WS_URL=wss://hub.example.com/agent/ws
VPS_COMMANDER_AGENT_NAME=node-01
VPS_COMMANDER_AGENT_TOKEN=<该设备的独立凭据>
ENV
chmod 0600 /etc/vps-commander/agent.env
```

systemd 单元（与 `deploy/vps-commander-agent.service` 一致）：

```ini
[Service]
Type=simple
User=root
WorkingDirectory=/opt/vps-commander
EnvironmentFile=-/etc/vps-commander/agent.env
ExecStart=/opt/vps-commander/vps-commander-agent -hub ${VPS_COMMANDER_HUB_WS_URL} -name ${VPS_COMMANDER_AGENT_NAME}
Restart=always
RestartSec=3s
KillSignal=SIGTERM
TimeoutStopSec=10s
LimitNOFILE=65536
Environment=GOGC=50
```

凭据通过 `VPS_COMMANDER_AGENT_TOKEN` 环境变量读取，不出现在 `ExecStart` 和 `/proc/*/cmdline` 中。不要再使用 `-token` 参数传递凭据。

```bash
systemctl daemon-reload
systemctl enable --now vps-commander-agent
journalctl -u vps-commander-agent -n 50 --no-pager
```

连上后日志会出现 `MCP services synced:`，面板设备列表显示为在线。

### 参数与环境变量

| 名称 | 说明 |
| :--- | :--- |
| `-hub` / `VPS_COMMANDER_HUB_WS_URL` | Hub WebSocket 地址（必填）。 |
| `-name` / `VPS_COMMANDER_AGENT_NAME` | 节点名称（必填），须与签发凭据时一致。 |
| `VPS_COMMANDER_AGENT_TOKEN` | 该设备的独立凭据（必填）。 |
| `VPS_COMMANDER_ROLE` | 可选，资源画像中的角色标签，默认 `APPLICATION`。 |
| `-insecure` | 跳过 TLS 校验，仅用于测试环境。 |
| `VPS_COMMANDER_CLUSTER_SECRET` | 旧版共享密钥，仅在未设置 `AGENT_TOKEN` 时作为回退；生产不要使用。 |

## 4. 连接行为

- 断线重连采用 full jitter 指数退避：`rand(0, min(30s, 1s·2^n))`；进程首次重连额外随机等待 0–10s，并遵守握手响应的 `Retry-After`。连接稳定 2 分钟后退避重置。
- Hub 对握手限流：全局 `50/s`（突发 200）、单 IP `5/s`（突发 20），超限返回 `429` + `Retry-After: 1`。同一 NAT 后的大量 Agent 集体重连时可能短暂触发 429，属预期。
- 入站 WebSocket 帧上限 8 MiB。
- 资源画像（CPU、内存、磁盘、网络、负载、Docker 等）按 Hub 下发的间隔上报，默认最长 300 秒。

## 5. 升级

```bash
curl -fsSL https://github.com/kyaring/vps-commander/releases/latest/download/upgrade-agent.sh | sudo bash
```

流程：下载 → SHA256 校验 → 备份旧二进制 → 原子替换 → 重启 → 15 秒内确认重新连上 Hub；任一步失败自动回滚旧版本。Agent 与 Hub 的协议向后兼容，可在 Hub 升级后再逐台升级 Agent。

## 6. 卸载

```bash
systemctl disable --now vps-commander-agent
rm -f /etc/systemd/system/vps-commander-agent.service /opt/vps-commander/vps-commander-agent /etc/vps-commander/agent.env
systemctl daemon-reload
```

然后在面板中吊销该设备，使其凭据失效。

## 7. 离线排查

| 现象 | 排查 |
| :--- | :--- |
| 面板显示离线 | `systemctl status vps-commander-agent`；`journalctl -u vps-commander-agent -n 100` |
| 日志反复 401 / 403 | 凭据错误、已被吊销，或节点名称与签发时不一致；重新签发凭据。 |
| 日志出现 429 | 握手被限流，Agent 会按 `Retry-After` 自动退避，通常无需处理。 |
| TLS 错误 | 检查反代证书与 `VPS_COMMANDER_HUB_WS_URL` 是否为 `wss://` 正确域名。 |
| 连上但命令被拒绝 | 不是连接问题：检查该节点的安全等级与调用令牌的 scope / 设备白名单（见 [../security.md](../security.md)）。 |
