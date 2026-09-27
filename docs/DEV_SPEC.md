# VPS-Commander 详细技术规格与开发实施方案 (v1.1.0)

## 一、 系统架构角色划分

整个系统划分为两个独立二进制产物（单一代码库仓库编译）：
1. **`vps-commander-hub` (中央网关 + 控制面板)**：
   - 部署在有公网域名或反代的主机上（当前主力机 `wjyhk`）。
   - 监听端口：`127.0.0.1:9521`（由 Lucky 负责 TLS 证书卸载与反代）。
   - 暴露端点：
     - 面向 ChatGPT Actions：`/api/v1/*`（Bearer Token 鉴权）
     - 面向 Agent 长连接：`/agent/ws`（Cluster Secret 握手）
     - 面向用户极简管理面板：`/` 及 `/static/*`（Go embed 静态资源，支持密码登录/Cookie鉴权）
     - 描述文件：`/openapi.json`
   - 本身内置 Local Executor（直接代表本地主力机，设备名：`local` 或自定义主机名）。
2. **`vps-commander-agent` (节点轻量客户端)**：
   - 部署在其他受管节点（如 `hwsg` 等）。
   - 主动向 Hub 发起安全 WebSocket 连接（支持断线指数退避重连）。
   - 纯受控执行器，只响应 Hub 下发指令，不开放任何公网入站端口（彻底规避防火墙/NAT问题）。

---

## 二、 协议设计 (RPC over WebSocket)

### 2.1 握手与注册 (Handshake)
Agent 连接 URL: `wss://hub.domain.com/agent/ws?device_name=hwsg`
- Hub 校验通过后：
  - 发送握手成功包：`{"event": "registered", "device": "hwsg", "heartbeat_interval": 30}`
  - 将该连接注入 `DeviceManager`，状态标为 `online`。

### 2.2 心跳 (Ping/Pong)
- 每 30 秒由 Hub 发送 `{"event": "ping", "ts": 1727049600}`。
- Agent 回复 `{"event": "pong", "ts": 1727049600}`。连续 2 次无响应标记为 `offline`。

### 2.3 命令下发与响应 (Request-Response)
- Hub 下发：
  ```json
  {
    "id": "task_6b7a8c9d",
    "action": "exec",
    "payload": {
      "command": "free -h",
      "workdir": "/root",
      "timeout": 30
    }
  }
  ```
- Agent 执行完返回：
  ```json
  {
    "id": "task_6b7a8c9d",
    "status": "success",
    "exit_code": 0,
    "stdout": "...",
    "stderr": "",
    "duration_ms": 45
  }
  ```

---

## 三、 ChatGPT Actions (OpenAPI 3.1) 接口定义

完整的 `openapi.json` 规范定义了以下 4 个原子能力：

1. **`list_devices`**:
   - `GET /api/v1/devices`
   - 返回受管设备名、IP、状态（online/offline）、最后心跳。
2. **`exec_command`**:
   - `POST /api/v1/exec`
   - 参数：`device` (默认: "local"), `command`, `workdir`, `timeout`。
3. **`read_file`**:
   - `POST /api/v1/file/read`
   - 参数：`device`, `path`, `offset`, `limit`。
4. **`write_file`**:
   - `POST /api/v1/file/write`
   - 参数：`device`, `path`, `content`。

---

## 四、 Hub Web 控制面板功能规范

### 4.1 技术实现
- 单二进制静态内嵌：基于 Go `embed.FS` 打包 HTML5 + TailwindCSS + Vanilla JS。
- 零外部 npm/node 依赖，开箱即用。

### 4.2 功能矩阵
1. **设备看板 (Devices Dashboard)**：
   - 节点列表展示（名称、IP、状态、架构/系统版本、最后活跃时间）。
   - 一键生成 Agent 安装注册命令（带 Token 参数）。
   - 手动注销/踢出无效节点。
2. **ChatGPT Actions 向导 (Actions Guide)**：
   - 动态渲染 `openapi.json` 并提供一键复制。
   - API Key 凭据生成、轮换与撤销。
   - 预设 System Prompt 最佳实践一键复制。
3. **安全审计追踪 (Audit Trails)**：
   - 实时日志流水：时间戳、来源 IP、目标节点、操作类型、执行命令、执行耗时、退出状态。
   - 支持日志按设备名、时间区间筛选。
4. **快速网页终端 (Quick Console)**：
   - 应急测试控制台，免切 ChatGPT 直接在 Web 端选择节点跑指令验证联通性。

---

## 五、 存储与安全审计 (SQLite)

- 数据库文件：`data/commander.db`
- 数据表结构：
  1. `devices`：记录设备名、备注、添加时间、密钥 Hash。
  2. `audit_logs`：
     - `id`, `timestamp`, `caller_ip`, `device`, `action`, `command`, `exit_code`, `duration_ms`, `error_msg`
     - 确保在 ChatGPT 里执行的所有高危操作均可追溯。

---

## 六、 开发实施里程碑 (Milestones)

- [ ] **M1: 基础骨架与单机执行**（完成本地 Local Executor + Gin API + Bearer 鉴权 + `/openapi.json`）
- [ ] **M2: Agent WebSocket 协议与多节点分发**（完成 Agent 注册、心跳保活、Hub 路由转发）
- [ ] **M3: 审计日志与嵌入式 Web 管理页**（完成前端内嵌、看板交互、命令历史审计与快速控制台）
- [ ] **M4: 本机 Lucky 证书反代与 ChatGPT 联调上线**
