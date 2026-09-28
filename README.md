<div align="center">

# VPS-Commander

**面向 AI Agent (ChatGPT / Claude / MCP) 与个人运维的高性能、极轻量、多节点私有基础设施调度中枢**

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Architecture](https://img.shields.io/badge/Arch-Linux%20AMD64%20%7C%20ARM64-orange)](#)
[![Memory Footprint](https://img.shields.io/badge/Memory-Hub%20%E2%89%A4%2025MB%20%7C%20Agent%20%E2%89%A4%2012MB-brightgreen)](#)

</div>

---

## 🌟 核心特性

- ⚡ **极致轻量，极低常驻**：纯 Go 原生编写，静态无依赖编译。Hub 核心管理进程物理常驻内存（RSS）稳定在 **15MB** 左右；受控端 Agent 内存占用仅 **7~11MB**，小内存 VPS 零负担。
- 🤖 **原生 AI 友好 (ChatGPT Actions & MCP 双协议)**：
  - **OpenAPI 3.1.0 标准**：开箱即用支持 ChatGPT Custom GPTs / Claude Actions。
  - **MCP (Model Context Protocol)**：内置兼容 SSE 与 JSON-RPC 标准，支持 Cursor、Windsurf、Claude Desktop 等客户端。
  - **OAuth 2.0 / Bearer 认证**：支持公开客户端授权与 Bearer API Key 访问控制。
- 🌐 **多节点集群架构 (Hub & Agent)**：
  - 单中心 Hub 统一管理多台受控 VPS。
  - 远程 Agent 节点通过标准 WebSocket (WSS) 主动建立安全长连接回连，天然穿透 NAT 与内网，无需受控端暴露端口。
- 🛡️ **严格安全防护与权限闭环**：
  - **零参数泄露**：全面封禁 CLI 敏感传参，凭据由 `0600` 环境变量接管，避免 `/proc/*/cmdline` 泄密。
  - **完整审计追溯**：内置本地 SQLite 存储，全量记录调用者 IP、下发节点、指令内容、退出码及精确执行耗时。
  - **执行隔离与超时回收**：内置上下文超时控制，强制清理孤儿进程，彻底杜绝僵尸进程。
- 🖥️ **内嵌极简 Web 管理面板**：
  - 静态单页面（SPA）采用 Go `embed` 完整打包进二进制文件，单文件独立运行，无需安装 Node.js、Nginx 或外部静态资源 CDN。
  - 包含设备状态感知、Actions 向导、在线调试控制台及操作审计日志。

---

## 🏗️ 架构概览

```text
                  +-----------------------------------+
                  |     OpenAI ChatGPT / AI Agents   |
                  +-----------------+-----------------+
                                    | HTTPS / MCP SSE / WSS
                                    v
                  +-----------------------------------+
                  |    反向代理 / SSL 终止 (Lucky/Nginx)|
                  +-----------------+-----------------+
                                    | 127.0.0.1:9521
                                    v
     +-------------------------------------------------------------+
     |                    VPS-Commander Hub                        |
     |  - OpenAPI 3.1 & MCP 路由引擎                               |
     |  - OAuth / Bearer 鉴权与 Session 会话                       |
     |  - 本地 SQLite 审计数据库 (commander.db)                    |
     |  - 内嵌 Web 管理面板 (SPA)                                   |
     +--------------+-------------------------------+--------------+
                    |                               |
        (本地执行通道)                  (WebSocket RPC 长连接通道)
                    |                               |
                    v                               v
         +--------------------+          +--------------------+
         |   宿主机 (Local)    |          |  受控 VPS (Agent)  |
         |   uname / sh / 文件 |          |  vps-commander-a   |
         +--------------------+          +--------------------+
```

---

## 🚀 极速一键部署 (推荐)

无论是在受控端 VPS 还是中心端，只需一行命令即可交互式安装与启动：

```bash
bash <(curl -sSL https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/install.sh)
```

- **安装 Agent (受控 VPS)**：输入 Hub 的 WSS 地址与通信密钥，全自动配置开机自启并秒级连入集群。
- **安装 Hub (中心服务端)**：自动生成安全 API Key / Web 面板密码并以 Systemd 托管常驻。

---

## 🛠️ 手动编译部署

### 1. Hub 服务端部署

在中心管理机上部署 Hub：

```bash
# 1. 编译二进制
make linux-amd64

# 2. 安装二进制文件
install -d -m 0750 /opt/vps-commander/data /etc/vps-commander
install -m 0755 bin/vps-commander-hub-linux-amd64 /opt/vps-commander/vps-commander-hub

# 3. 创建环境变量配置 (注意保护权限)
cat << 'ENV' > /etc/vps-commander/hub.env
VPS_COMMANDER_API_KEY=your_secure_api_key_here
VPS_COMMANDER_CLUSTER_SECRET=your_cluster_sync_secret_here
VPS_COMMANDER_WEB_PASSWORD=your_web_panel_password_here
ENV
chmod 0600 /etc/vps-commander/hub.env

# 4. 配置并启动 Systemd 服务
cp deploy/vps-commander-hub.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now vps-commander-hub
```

### 2. 远程 VPS Agent 受控端部署

在需要受控的远程 VPS 机器上执行：

```bash
# 1. 下载或拷贝 agent 二进制至远程机器
install -d -m 0750 /etc/vps-commander /opt/vps-commander
install -m 0755 vps-commander-agent-linux-amd64 /opt/vps-commander/vps-commander-agent

# 2. 配置 Agent 环境变量
cat << 'ENV' > /etc/vps-commander/agent.env
VPS_COMMANDER_HUB_WS_URL=wss://your-domain.com/agent/ws
VPS_COMMANDER_AGENT_NAME=vps-us-node01
VPS_COMMANDER_CLUSTER_SECRET=your_cluster_sync_secret_here
ENV
chmod 0600 /etc/vps-commander/agent.env

# 3. 配置并启动 Systemd 服务
cp deploy/vps-commander-agent.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now vps-commander-agent
```
Agent 启动后会通过 WSS 自动连入 Hub，在 Hub 管理面板与 `/api/v1/devices` 接口中实时显示为 `online`。

---

## 🤖 接入 ChatGPT / AI 客户端

### 方式 1：ChatGPT Actions (推荐)
1. 在 ChatGPT 创建自定义 GPT（GPT Builder）-> **Actions** -> **Create new action**。
2. **Schema 导入**：在 `Import from URL` 填入 `https://your-domain.com/openapi.json`。
3. **Authentication (身份验证)**：
   - Authentication Type 选择 **API Key**。
   - Auth Type 选择 **Bearer**。
   - 填入你配置的 `VPS_COMMANDER_API_KEY`。

### 方式 2：MCP (Model Context Protocol)
- **SSE 端点**：`https://your-domain.com/mcp/sse`
- **消息交互**：`https://your-domain.com/mcp/message`
- **认证**：支持 URL 参数 `?token=...` 或标准 Header `Authorization: Bearer <TOKEN>`。

---

## 📊 接口与能力一览

| 接口路径 | 方法 | 功能描述 |
| :--- | :--- | :--- |
| `/api/v1/devices` | `GET` | 列出集群中所有受控设备及其实时状态（在线/离线、架构、系统等） |
| `/api/v1/exec` | `POST` | 在指定受控节点执行任意 Shell 指令（支持超时截断） |
| `/api/v1/file/read` | `POST` | 安全分页读取目标机器上的文件内容（防内存溢出） |
| `/api/v1/file/write` | `POST` | 向目标机器指定路径覆写或创建文件 |
| `/mcp/sse` | `GET` | MCP 协议长连接事件流通道 |
| `/panel/login` | `GET/POST`| 内嵌 Web 运维管理控制台 |

---

## 📄 文档索引

- [生产部署指南与运维手册](deploy/README.md)
- [系统架构与安全设计说明](docs/ARCHITECTURE.md)
- [研发设计规范与内存基线](docs/DEV_SPEC.md)
- [阶段验收标准与门禁用例矩阵](docs/ACCEPTANCE_CRITERIA.md)
- [MCP 客户端接入协议文档](docs/MCP_INTEGRATION.md)

---

## 📜 开源协议

本项目基于 [MIT](LICENSE) 协议开源。
