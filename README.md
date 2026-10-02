<div align="center">

# VPS-Commander

**面向 AI Agent、MCP 与个人运维的高性能、轻量、多节点私有基础设施调度中枢**

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Architecture](https://img.shields.io/badge/Arch-Linux%20AMD64%20%7C%20ARM64-orange)](#)

</div>

---

## 🌟 核心特性

- ⚡ **轻量单体部署**：纯 Go 原生编译，Hub、Agent、MCP stdio 均可独立部署。
- 🤖 **原生 AI / MCP 接入**：提供 HTTP API、MCP SSE、stdio 以及 Agent WebSocket，多种客户端可以共用同一套安全模型。
- 🌐 **Hub & Agent 双通道架构**：Hub 负责控制与授权，Agent 通过 WSS 主动回连，不要求远端开放入站管理端口。
- 🛡️ **认证与授权分离**：HTTP/MCP 使用 Bearer API Key；Agent 使用每设备独立 Credential；Tool Registry + Security Snapshot 实现 Low / Medium / High 风险门禁。
- 📋 **完整审计链**：SQLite 持久化设备、凭据哈希、策略版本、操作审计与 Webhook 配置。
- 🧩 **多节点能力聚合**：文件、Shell、进程、Session、Agent MCP 等能力由 Hub 统一调度。
- 🖥️ **内嵌 Web 管理面板**：Go embed 打包，无需 Node.js 运行时。

---

## 🏁 研发最初目标

VPS-Commander 最初并不是一个简单的远程 Shell 工具，而是为 AI Agent 建立一个**用户自己控制的私有化 Linux 运维控制面**。

### 1.1 私有化
- 运维请求通过用户自己的 Hub 和 VPS 节点完成。
- 不依赖第三方运维 Relay。
- 凭据、策略和审计数据由用户自己的基础设施控制。

### 1.2 Host-centric 运维
- 全局 Shell。
- 全局文件读写。
- 进程与长任务 Session。
- 远程 Agent MCP。

### 1.3 多节点统一调度
- 一个 Hub 管理多个 VPS。
- 使用节点名称选择目标。
- 节点离线后保留设备身份与持久化状态。

### 1.4 AI 原生接口
- HTTP API。
- MCP。
- OpenAPI 契约。
- Web 管理面板。

---

## 🏗️ 当前总体架构

```text
                  +--------------------------------------+
                  |      AI / MCP Client / Web Panel    |
                  +------------------+-------------------+
                                     | HTTPS / MCP / WSS
                                     v
                  +--------------------------------------+
                  |       Reverse Proxy / TLS            |
                  +------------------+-------------------+
                                     | 127.0.0.1:9521
                                     v
        +-----------------------------------------------------------+
        |                    VPS-Commander Hub                     |
        |-----------------------------------------------------------|
        | Auth / API / MCP / Web Panel                             |
        | Security Snapshot / Tool Registry / Audit                |
        | Cluster Router / Session / Rate Limiter                  |
        +----------------------+-------------------+---------------+
                               |                   |
                         Local Executor       Agent WSS
                               |                   |
                               v                   v
                         Hub 本机             Remote VPS
                                                   |
                                            Host Executor
                                            File / Shell / Process
```

---

## 🔐 当前安全模型

### 1. Client Authentication
`Authorization: Bearer <API_KEY>` 是 HTTP / MCP 的主要客户端认证方式。

### 2. Agent Authentication
每个 Agent 使用独立的 `VPS_COMMANDER_AGENT_TOKEN`。
Hub SQLite 只保存 Credential Hash，不保存明文 Token。

### 3. Authorization
Tool Registry 首先确定操作风险：

| 风险 | 典型能力 |
| :--- | :--- |
| Low | 读取、搜索、设备/进程观察 |
| Medium | 文件修改、目录创建/移动、Agent MCP |
| High | Shell、进程启动、交互、终止 |

未知设备、未知身份或策略无法解析时不得获得 High 权限。

### 4. Shared Secret
生产环境已经关闭 Cluster Secret / Shared Secret fallback。

---

## 🤖 当前 MCP 能力

目前共 **12 个 MCP Tools**：

| Tool | Risk | 功能 |
| :--- | :--- | :--- |
| `list_devices` | Low | 节点列表与状态 |
| `exec_command` | High | Shell 执行 |
| `read_file` | Low | 分页读取文件 |
| `read_multiple_files` | Low | 批量读取文件 |
| `create_directory` | Medium | 创建目录 |
| `move_file` | Medium | 移动/重命名 |
| `list_processes` | Low | 进程观察 |
| `edit_block` | Medium | 原子文本修改 |
| `write_file` | Medium | 创建/覆写文件 |
| `list_agent_mcp` | Low | 查看 Agent MCP |
| `call_agent_mcp` | Medium | 调用 Agent MCP |

远程 MCP 当前使用 **legacy HTTP + SSE**；本地客户端使用 stdio。
Streamable HTTP 作为下一阶段 transport 演进方向。

---

## 📊 HTTP / Web 能力

| 接口 | 方法 | 功能 |
| :--- | :--- | :--- |
| `/api/v1/devices` | GET | 设备状态与资源画像 |
| `/api/v1/exec` | POST | 远程 Shell |
| `/api/v1/file/read` | POST | 文件读取 |
| `/api/v1/file/list` | POST | 目录列表 |
| `/api/v1/file/info` | POST | 文件信息 |
| `/api/v1/file/search` | POST | 文件搜索 |
| `/api/v1/file/write` | POST | 文件写入 |
| `/api/v1/devices/file/edit_block` | POST | 安全块编辑 |
| `/api/v1/process/session` | POST | Session 操作 |
| `/api/v1/mcp/*` | GET/POST/DELETE | MCP 服务管理与调用 |
| `/api/v1/security/*` | GET/POST | 安全策略 |
| `/api/v1/notifications/webhook*` | GET/POST/DELETE | Webhook |
| `/agent/ws` | WebSocket | Agent 长连接 |
| `/panel/*` | GET/POST | Web 管理面板 |

---

## 🚀 生产部署

Hub：
```text
/opt/vps-commander/vps-commander-hub
/etc/vps-commander/hub.env
/opt/vps-commander/data/commander.db
127.0.0.1:9521
```

Agent：
```text
/opt/vps-commander/vps-commander-agent
/etc/vps-commander/agent.env
wss://vpstool.kory.kdns.fr/agent/ws
```

Agent 只主动连接 Hub，不需要公网入站端口。

生产发布顺序：
`backup → clean build → 单节点灰度 → Hub restart recovery → rolling Agent → MCP/security/audit 验收`。

---

## 🚀 一键安装与升级

### 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/install.sh | sudo bash
```

安装脚本自动识别 `amd64/arm64` 并进入 Hub / Agent 安装流程。

### 一键升级 Agent

```bash
curl -fsSL https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/upgrade-agent.sh | sudo bash
```

升级脚本下载目标 Release、校验文件大小、备份旧版本并在启动失败时自动回滚。

生产环境的 Agent 凭据以独立设备凭据为准；不要将 Cluster Secret 作为长期生产凭据重新启用。

---

## 📄 文档索引

- [文档基线与全局索引](docs/DOCUMENTATION_BASELINE_20261002.md)
- [系统架构与开发设计规范](docs/ARCHITECTURE.md)
- [研发与协议规范](docs/DEV_SPEC.md)
- [MCP 接入指南](docs/MCP_INTEGRATION.md)
- [生产部署与运维指南](deploy/README.md)
- [发布验收标准](docs/ACCEPTANCE_CRITERIA.md)
- [项目进度与路线图](docs/PROGRESS_TRACKING.md)
- [v2.1 安全加固规范](docs/VPS_COMMANDER_V2_1_SECURITY_HARDENING_SPEC.md)

---

## 📜 开源协议

本项目基于 [MIT](LICENSE) 协议开源。
