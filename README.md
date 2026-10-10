<div align="center">

# VPS-Commander

**面向 AI Agent、MCP 与个人运维的轻量级多节点私有运维控制中枢**

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](#开源协议)
[![Architecture](https://img.shields.io/badge/Arch-Linux%20amd64%20%7C%20arm64%20%7C%20armv7-orange)](#)

</div>

> 当前版本：**v1.1.30**（2026-10-10）。发布 tag 采用 `v1.1.x`；文档中出现的 “v2.0 / v2.1 / v2.2” 是内部设计阶段名称，不是发布版本号。

---

## 核心特性

- **轻量单体部署**：Go 原生编译，Hub、Agent、MCP stdio 均为单个二进制；Web 面板通过 Go embed 内嵌，无需 Node.js。
- **三种客户端接入**：REST（`/api/v1/*`）、MCP Streamable HTTP（`POST /mcp`）、旧版 MCP SSE（`/mcp/sse`），以及本地 stdio。三者共用同一套令牌、scope、设备白名单、限流和审计。
- **Hub & Agent 双通道**：Agent 通过 WSS 主动回连 Hub，远端节点不需要开放入站管理端口。
- **命名令牌**：每个客户端一个独立令牌，可配置 scope、设备白名单、限速（rps）和最大会话数；Hub 只保存哈希，完整密钥仅在创建时显示一次。
- **双层授权**：令牌 scope + 设备白名单决定“谁能调用什么”，Low / Medium / High 安全等级决定“这台节点允许什么风险”。未注册或离线的远程节点一律拒绝。
- **完整审计**：记录操作人、传输方式（rest / mcp-http / mcp-sse / panel / stdio）、目标设备、命令或路径（脱敏、截断）与结果；面板支持筛选、游标分页与 CSV 导出。
- **可观测与限流**：管理员可读 `/metrics`（Prometheus）；按令牌限速与 exec 并发限制，Agent 握手令牌桶限流。
- **内嵌管理面板**：设备、控制台、接入与令牌（含 AI 接入向导）、安全与通知、日志五个分区，适配手机屏幕。

---

## 架构概览

```text
   AI 客户端 / MCP 客户端 / 脚本 / Web 面板
                    │  HTTPS（REST、MCP、Panel）
                    ▼
            反向代理（TLS 终止）
                    │  127.0.0.1:9521
                    ▼
  ┌──────────────────────────────────────────┐
  │              VPS-Commander Hub           │
  │  认证（命名令牌）/ REST / MCP / Panel     │
  │  Scope + 设备白名单 / 安全策略快照        │
  │  集群路由 / Session / 限流 / 审计 / 通知  │
  │  SQLite                                  │
  └───────────────┬──────────────┬───────────┘
                  │              │ Agent WSS（/agent/ws）
           本机执行器            ▼
                           远程 VPS 上的 Agent
                           Shell / 文件 / 进程 / Agent MCP
```

详见 [docs/architecture.md](docs/architecture.md)。

---

## 5 分钟快速开始

以下示例中的 `hub.example.com` 请替换为你自己的域名。

### 1. 安装 Hub

```bash
curl -fsSL https://github.com/kyaring/vps-commander/releases/latest/download/install.sh | sudo bash
```

选择 Hub。脚本自动识别 `amd64 / arm64 / armv7`，从 GitHub Release 下载并做 SHA256 校验，生成 `/etc/vps-commander/hub.env`（权限 0600）并注册 systemd 服务。Hub 默认只监听 `127.0.0.1:9521`。

### 2. 配置反向代理

把 `https://hub.example.com` 反代到 `127.0.0.1:9521`，需支持 WebSocket Upgrade，并对 `/mcp/sse` 关闭响应缓冲。若使用反代，在 `hub.env` 中设置 `VPS_COMMANDER_TRUST_PROXY=on`（见 [docs/deploy/hub.md](docs/deploy/hub.md)）。

### 3. 接入 Agent

在面板“设备”分区的“设备接入”中点击“添加设备”→“生成接入凭证”，为该节点签发独立凭据；然后在目标节点运行同一个安装脚本并选择 Agent，填入：

- Hub 地址：`wss://hub.example.com/agent/ws`
- 节点名称
- 该设备的独立凭据

### 4. 创建命名令牌并接入客户端

打开 `https://hub.example.com/panel/`，进入“接入与令牌”：

1. 创建命名令牌（默认只读；需要执行命令或写文件时选“运维”），完整密钥只显示一次，请立即复制保存。
2. 用“AI 接入向导”生成 GPT Actions、MCP 或 REST 配置，并一键测试连通。

```bash
curl -s https://hub.example.com/api/v1/devices \
  -H "Authorization: Bearer $VPS_COMMANDER_TOKEN"
```

---

## 客户端接入方式

| 方式 | 地址 | 适用 |
| :--- | :--- | :--- |
| MCP Streamable HTTP | `POST https://hub.example.com/mcp` | 原生 MCP 客户端（推荐） |
| MCP SSE（旧版） | `GET /mcp/sse` + `POST /mcp/message` | 只支持 SSE 的旧客户端 |
| REST | `https://hub.example.com/api/v1/*` | Skills、脚本、GPT Actions；一次调用一个请求 |
| stdio | `vps-commander-mcp-stdio -hub … -key …` | 本地桌面 MCP 客户端 |

常见坑：Streamable HTTP 客户端的地址必须是 `/mcp`，不要填成 `/mcp/sse`（会返回 405），结尾也不要加 `/`（会返回 404）。完整说明见 [docs/mcp-clients.md](docs/mcp-clients.md)。

### MCP 工具（12 个）

| 工具 | 所需 scope | 风险等级 |
| :--- | :--- | :--- |
| `list_devices` | `devices:read` | Low |
| `read_file` / `read_multiple_files` | `files:read` | Low |
| `diagnostic_command`（白名单只读诊断） | `files:read` | Low |
| `list_agent_mcp` | `files:read` | Low |
| `list_processes` | `process` | Low |
| `create_directory` / `move_file` / `edit_block` / `write_file` | `files:write` | Medium |
| `call_agent_mcp` | `mcp:call` | Medium |
| `exec_command` | `exec` | High |

`tools/list` 只返回当前令牌 scope 允许的工具。

---

## 文档

| 文档 | 内容 |
| :--- | :--- |
| [docs/README.md](docs/README.md) | 文档索引与维护规则 |
| [docs/architecture.md](docs/architecture.md) | 架构、Agent 协议、执行模型 |
| [docs/security.md](docs/security.md) | 令牌、scope、白名单、安全等级、审计 |
| [docs/mcp-clients.md](docs/mcp-clients.md) | MCP / REST 客户端接入与工具参数 |
| [docs/api.md](docs/api.md) | REST 接口与 scope 对照 |
| [docs/deploy/hub.md](docs/deploy/hub.md) | Hub 部署、环境变量全表、反向代理 |
| [docs/deploy/agent.md](docs/deploy/agent.md) | Agent 安装、升级、排查 |
| [docs/operations.md](docs/operations.md) | 升级回滚、备份、日志、指标、FAQ |
| [docs/development.md](docs/development.md) | 构建、测试、发布与验收门禁 |
| [docs/loadtest.md](docs/loadtest.md) | 500 Agent / 50 MCP 压测工具 |
| [docs/roadmap.md](docs/roadmap.md) | 当前进度与后续计划 |
| [CHANGELOG.md](CHANGELOG.md) | 版本变更记录 |

历史评审、事故与交接记录已移入 [docs/archive/](docs/archive/)，仅供追溯，不代表当前状态。

---

## 开源协议

本项目声明采用 MIT 协议开源。仓库目前尚未包含 `LICENSE` 文件，待补充。
