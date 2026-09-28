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
- 📊 **实时节点资源画像 (Node Profiling & Role)**：
  - **原生零依赖深度采集**：直接读取 Linux 内核指标，实时监控物理 CPU 核心数与瞬时负载、真实可用内存（`MemAvailable`）与 Swap、根分区磁盘占用、IO 压力指数（内核原生 PSI / Loadavg）及 Docker 守护进程运行状态。
  - **动态角色标签**：支持节点角色分工（如 `CONTROL` 控制中枢、`APPLICATION` 应用节点、`DATABASE` 等），为 AI Agent 自动化调度部署决策提供全维度实时依据。
- 🛡️ **设备生命周期管理与防重连黑名单**：
  - 支持管理员在 Web 控制台一键注销并强制断开指定受控节点。
  - 持久化吊销拦截（Revocation List），彻底杜绝被下线 Agent 因 Systemd 自动重启而死灰复燃。
  - 本地主控节点（Local）强制安全锁，防误操作防护。
- 🖥️ **内嵌极简 Web 管理面板**：
  - 静态单页面（SPA）采用 Go `embed` 完整打包进二进制文件，单文件独立运行，无需安装 Node.js、Nginx 或外部静态资源 CDN。
  - **工程级双态主题**：纯手绘 SVG 极简几何微图标（太阳/弦月），支持深曜蓝黑与柔和浅灰白一键切换，完美适配系统偏好且零 FOUC 白屏闪烁。
  - **全端响应式**：彻底适配手机移动端（≤680px），宽表独立触控滑动，长节点名自动断行不穿透。

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

## 🚀 极速一键部署与平滑升级

### 1. 一键全自动安装 (推荐)
无论是在受控端 VPS 还是中心端，只需一行命令即可交互式安装与启动：

```bash
bash <(curl -fsSLk https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/install.sh)
```
- **安装 Agent (受控 VPS)**：输入 Hub 的 WSS 地址与通信密钥，全自动配置开机自启并秒级连入集群。
- **安装 Hub (中心服务端)**：自动生成安全 API Key / Web 面板密码并以 Systemd 托管常驻。

### 2. 受控端 Agent 一键平滑升级 (免重输配置)
当新版本发布后，无需重新配置通信密钥或重新安装，在任意受控机上直接运行升级脚本：

```bash
curl -fsSLk https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/upgrade-agent.sh | bash
```
- 自动检测并拉取 GitHub 最新版本架构包（AMD64 / ARM64）；
- 自动备份旧版本二进制（`.bak`）；
- 升级异常（校验失败或无法启动）时**自动秒级回滚**，保证节点永不失联。

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

## 🤖 接入 ChatGPT (新版插件 Apps & MCP 模式)

当前 ChatGPT 已升级为以 **插件 / 应用 (Apps & MCP)** 规范连接外部服务。VPS-Commander 原生内置完整的 **MCP SSE + OAuth2** 协议支持，无需部署额外适配层。

### 接入配置步骤：

1. **进入 ChatGPT 插件/应用配置页**：
   - 打开 ChatGPT -> **Settings (设置)** -> **Apps & Integrations (应用与集成) / Plugins** -> **Create / Add App (添加应用)**。
2. **连接方式选择 MCP**：
   - 连接协议选择 **MCP (Model Context Protocol)** 或 **Server-Sent Events (SSE)**。
   - **MCP Server URL**：填入 Hub 的 SSE 统一入口：
     ```text
     https://your-domain.com/mcp/sse
     ```
3. **身份认证 (Authentication)**：
   - **方式 A (OAuth 2.0 自动授权，推荐)**：
     - Hub 内置了标准 RFC 8414 OAuth Metadata (`/.well-known/oauth-authorization-server`)；
     - 授权端点与 Token 端点由 ChatGPT 自动识别，一键点击「Authorize」即可完成免密安全绑定。
   - **方式 B (Bearer API Key)**：
     - 若选择 API Key，填入 Hub 环境变量 `VPS_COMMANDER_API_KEY` 即可。
4. **自动发现 4 大运维工具**：
   - 握手成功后，ChatGPT 会自动加载并列出 4 个原生运维工具：
     - `list_devices`：列出受控节点、CPU/内存/磁盘剩余绝对值及 Docker 状态；
     - `exec_command`：在目标机器（如 `m4-live-agent`）执行 Shell 指令；
     - `read_file`：分页读取目标文件；
     - `write_file`：远程编辑或创建文件。

---

### 传统客户端支持 (Claude Desktop / Cursor / VS Code)

支持通过本地 stdio 二进制连接 Hub：
```json
{
  "mcpServers": {
    "vps-commander": {
      "command": "/path/to/vps-commander-mcp-stdio-linux-amd64",
      "args": [
        "-hub", "https://your-domain.com",
        "-key", "YOUR_API_KEY"
      ]
    }
  }
}
```

---

## 📊 接口与能力一览

| 接口路径 | 方法 | 功能描述 |
| :--- | :--- | :--- |
| `/api/v1/devices` | `GET` | 列出集群中所有受控设备状态与**实时资源画像 (CPU/Mem/Disk/IO PSI/Docker/Role)** |
| `/panel/api/devices` | `DELETE` | 【管理员】注销并踢出指定受控节点，持久化写入防重连黑名单 |
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
