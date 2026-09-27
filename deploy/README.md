# VPS-Commander 部署与运维指南

本文档介绍 VPS-Commander 的生产环境部署架构、配置规范与运维操作。

---

## 一、 系统架构与端口规划

- **Hub 服务端**：统一监听本地 `127.0.0.1:9521`，不对公网直接暴露端口。
- **反向代理 (Lucky)**：
  - 域名：`https://vpstool.kory.kdns.fr`
  - 转发目标：`http://127.0.0.1:9521`
  - 核心要求：开启 **WebSocket Upgrade** 支持，转发透传所有子路径（`/.well-known/*`、`/oauth/*`、`/mcp/*`、`/api/v1/*`、`/agent/ws`、`/panel/*`）。
  - SSL 终止于反代层，反代自动携带 `X-Forwarded-Proto: https` 头以保障 Cookie 的 `Secure` 属性。
- **Agent 客户端**：
  - 运行于受控的远程 VPS 机器上。
  - 通过公网 WSS 长连接主动回连 Hub：`wss://vpstool.kory.kdns.fr/agent/ws`。

---

## 二、 凭据安全规范（强制要求）

1. **绝对禁止命令行参数传密**：
   - 二进制不再支持 `--api-key`、`--cluster-secret`、`--web-password` 命令行参数。
   - 彻底杜绝在 `/proc/*/cmdline` 和 `ps aux` 中泄露明文密码。
2. **凭据存储位置与权限**：
   - 凭据文件保存在 `/etc/vps-commander/hub.env` 与 `/etc/vps-commander/agent.env`。
   - 权限必须严格设置为 `0600`（仅 root 可读写）。

---

## 三、 Hub 服务端部署步骤

### 1. 编译并安装二进制
```bash
cd /root/.openclaw/workspace/vps-commander
make linux-amd64
install -d -m 0750 /opt/vps-commander/data /etc/vps-commander
install -m 0755 bin/vps-commander-hub-linux-amd64 /opt/vps-commander/vps-commander-hub
```

### 2. 配置环境变量文件 (`/etc/vps-commander/hub.env`)
```bash
cat << 'ENV' > /etc/vps-commander/hub.env
VPS_COMMANDER_API_KEY=your_generated_api_key_here
VPS_COMMANDER_CLUSTER_SECRET=your_generated_cluster_secret_here
VPS_COMMANDER_WEB_PASSWORD=your_generated_web_password_here
ENV
chmod 0600 /etc/vps-commander/hub.env
```

### 3. 配置 Systemd 托管 (`/etc/systemd/system/vps-commander-hub.service`)
```ini
[Unit]
Description=VPS-Commander Hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/vps-commander
EnvironmentFile=-/etc/vps-commander/hub.env
ExecStart=/opt/vps-commander/vps-commander-hub -addr 127.0.0.1:9521 -db /opt/vps-commander/data/commander.db
Restart=always
RestartSec=3s
KillSignal=SIGTERM
TimeoutStopSec=10s
LimitNOFILE=65536
Environment=GOGC=50

[Install]
WantedBy=multi-user.target
```

### 4. 启动服务
```bash
systemctl daemon-reload
systemctl enable --now vps-commander-hub
systemctl status vps-commander-hub
```

---

## 四、 远程 Agent 节点部署步骤

若要将其他 VPS 纳入受控列表，在该远程 VPS 上执行：

### 1. 安装 Agent 二进制
将编译生成的 `bin/vps-commander-agent-linux-amd64`（或 ARM64 版本）上传至目标 VPS 的 `/opt/vps-commander/vps-commander-agent` 并赋予执行权限 `chmod +x`。

### 2. 配置 Agent 环境变量 (`/etc/vps-commander/agent.env`)
```bash
install -d -m 0750 /etc/vps-commander
cat << 'ENV' > /etc/vps-commander/agent.env
VPS_COMMANDER_HUB_WS_URL=wss://vpstool.kory.kdns.fr/agent/ws
VPS_COMMANDER_AGENT_NAME=vps-node-01
VPS_COMMANDER_CLUSTER_SECRET=与Hub相同的CLUSTER_SECRET
ENV
chmod 0600 /etc/vps-commander/agent.env
```

### 3. 配置 Systemd 托管 (`/etc/systemd/system/vps-commander-agent.service`)
```ini
[Unit]
Description=VPS-Commander Agent
After=network-online.target
Wants=network-online.target

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

[Install]
WantedBy=multi-user.target
```

### 4. 启动并连入集群
```bash
systemctl daemon-reload
systemctl enable --now vps-commander-agent
```
启动后，Hub 控制面板与 `/api/v1/devices` 接口将立即展示该节点并标记为 `online`。

---

## 五、 控制面板与 Actions 使用

1. **管理后台地址**：`https://vpstool.kory.kdns.fr/panel/login`
2. **后台功能**：
   - **设备看板**：查看所有在线/离线节点、系统架构与延迟。
   - **快速控制台**：直接在网页端向指定节点执行 Shell 指令。
   - **审计日志**：实时监控 GPT 与用户的全部执行调用、命令详情与耗时。
   - **Actions 向导**：一键复制 Schema 地址、System Prompt 与 API 密钥。
