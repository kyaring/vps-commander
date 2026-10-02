# VPS-Commander 部署与运维指南

本文档介绍 VPS-Commander 当前生产环境部署架构、凭据规范、Agent 接入、升级与回滚流程。

---

## 一、系统架构与端口规划

- **Hub 服务端**：监听 `127.0.0.1:9521`，不直接暴露公网。
- **反向代理**：公网 HTTPS/WSS 入口为 `https://vpstool.kory.kdns.fr`。
- **Agent 客户端**：通过 `wss://vpstool.kory.kdns.fr/agent/ws` 主动回连 Hub。

反代必须支持：
- WebSocket Upgrade；
- `/agent/ws`；
- `/mcp/*`；
- `/api/v1/*`；
- `/panel/*`；
- `/.well-known/*`；
- `/oauth/*`。

---

## 二、凭据安全规范（强制要求）

### 2.1 Hub 凭据
Hub 使用：
- `VPS_COMMANDER_API_KEY`；
- `VPS_COMMANDER_WEB_PASSWORD`。

生产环境不再依赖 `VPS_COMMANDER_CLUSTER_SECRET` 作为 Agent 身份 fallback。

### 2.2 Agent 凭据
每台 Agent 使用独立：
`VPS_COMMANDER_AGENT_TOKEN`。

Hub SQLite 仅保存 Credential Hash。

### 2.3 文件权限
```bash
chmod 0600 /etc/vps-commander/hub.env
chmod 0600 /etc/vps-commander/agent.env
```

### 2.4 v2.1 安全要求
当前 v2.0 代码仍兼容旧的 `-token` 参数；下一版本要求 Secret 不出现在 argv / `/proc/*/cmdline`，优先使用 systemd EnvironmentFile / LoadCredential。

---

## 三、一键安装与升级

### 3.1 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/install.sh | sudo bash
```

脚本自动识别 `amd64/arm64`，并进入 Hub / Agent 安装流程。

- Hub：自动生成 API Key 与 Web 面板密码。
- Agent：输入 Hub WSS 地址、节点名称和独立 Agent Token。
- 生产环境不再要求 Cluster Secret 作为长期凭据。

### 3.2 一键升级 Agent

```bash
curl -fsSL https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/upgrade-agent.sh | sudo bash
```

升级流程：

```text
下载 Release
   ↓
临时文件完整性检查
   ↓
备份当前 Agent
   ↓
原子替换
   ↓
systemd restart
   ↓
健康检查
   ├─ 成功 → 删除备份
   └─ 失败 → 自动恢复旧版本
```

### 3.3 手工安装

如果需要固定版本、离线部署或灰度验证，再使用下面的手工安装流程。

---

## 四、Hub 服务端部署步骤

### 4.1 编译
```bash
cd /root/.openclaw/workspace/vps-commander
go test ./...
go test -race ./...
git diff --check
make clean
make linux-amd64
```

发布前必须确认：
```text
vcs.modified=false
vcs.revision=<release commit>
```

### 4.2 安装
```bash
install -d -m 0750 /opt/vps-commander/data /etc/vps-commander
install -m 0755 bin/vps-commander-hub-linux-amd64 /opt/vps-commander/vps-commander-hub
```

### 4.3 配置
```bash
cat << 'ENV' > /etc/vps-commander/hub.env
VPS_COMMANDER_API_KEY=your_secure_api_key
VPS_COMMANDER_WEB_PASSWORD=your_web_password
ENV
chmod 0600 /etc/vps-commander/hub.env
```

### 4.4 Systemd
```ini
[Service]
EnvironmentFile=-/etc/vps-commander/hub.env
ExecStart=/opt/vps-commander/vps-commander-hub -addr 127.0.0.1:9521 -db /opt/vps-commander/data/commander.db
Restart=always
RestartSec=3s
```

### 4.5 启动
```bash
systemctl daemon-reload
systemctl enable --now vps-commander-hub
systemctl status vps-commander-hub
```

---

## 五、远程 Agent 节点部署步骤

### 5.1 安装二进制
```bash
install -d -m 0750 /etc/vps-commander /opt/vps-commander
install -m 0755 vps-commander-agent-linux-amd64 /opt/vps-commander/vps-commander-agent
```

### 5.2 配置
```bash
cat << 'ENV' > /etc/vps-commander/agent.env
VPS_COMMANDER_HUB_WS_URL=wss://vpstool.kory.kdns.fr/agent/ws
VPS_COMMANDER_AGENT_NAME=vps-node-01
VPS_COMMANDER_AGENT_TOKEN=<per-device-token>
ENV
chmod 0600 /etc/vps-commander/agent.env
```

### 5.3 Systemd
```ini
[Service]
EnvironmentFile=-/etc/vps-commander/agent.env
ExecStart=/opt/vps-commander/vps-commander-agent -hub ${VPS_COMMANDER_HUB_WS_URL} -name ${VPS_COMMANDER_AGENT_NAME}
Restart=always
RestartSec=3s
```

当前 v2.0 生产迁移阶段可兼容显式 token；v2.1 发布后必须改为不出现在 ExecStart 的方式。

### 5.4 启动
```bash
systemctl daemon-reload
systemctl enable --now vps-commander-agent
systemctl status vps-commander-agent
```

Agent 启动后主动连接 Hub，并在设备列表中显示为 `online`。

---

## 六、控制面板与 MCP 使用

### 6.1 管理面板
`https://vpstool.kory.kdns.fr/panel/login`

面板包含设备状态、安全策略、操作控制、审计和相关管理功能。

### 6.2 MCP
远程入口：
`https://vpstool.kory.kdns.fr/mcp/sse`

本地 stdio 使用 `vps-commander-mcp-stdio`。

当前共 11 个 MCP tools，详见 `docs/MCP_INTEGRATION.md`。

---

## 七、生产升级流程

```text
Backup
  ↓
Clean Build
  ↓
Single-node Gray Release
  ↓
Hub Restart Recovery
  ↓
Rolling Agent
  ↓
MCP / Security / Audit Smoke Test
  ↓
Full Acceptance
```

任一节点断联、权限绕过、Credential 泄露或协议不兼容，立即停止发布并回滚。

---

## 八、日常运维检查

```bash
systemctl status vps-commander-hub
journalctl -u vps-commander-hub -n 100
curl -fsS http://127.0.0.1:9521/healthz
```

集群可通过 Web Panel、`/api/v1/devices` 或 MCP `list_devices` 检查。

---

## 九、当前生产审计提示

2026-10-02 审计发现生产 Hub / Agent 曾由 `vcs.modified=true` 的工作树构建。
因此生产现场二进制不能直接视为当前 HEAD 的可复现产物。
下一次正式发布必须 clean rebuild，并重新部署与验证。
