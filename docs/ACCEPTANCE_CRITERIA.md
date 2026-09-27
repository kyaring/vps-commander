# VPS-Commander 验收审核标准与收尾门禁规范 (Quality Gate & Acceptance Criteria)

> 本规范由架构与质量负责人（OpenClaw）制定并执行最终裁决。
> 任何交付代码必须满足以下全量准入与准出指标方可判定为验收通过（Release Ready）。

---

## 一、 核心门禁四不原则（强制一票否决项）

1. **绝对无外部公网中转**：任何网络交互（除与 OpenAI 官方通信外）严禁经过第三方 Relay 服务器，必须 100% 走私有域名直连。
2. **绝对零残留孤儿进程**：执行任何命令、超时中断或连接断开后，后台不得产生僵尸进程或孤儿子进程。
3. **绝对无未授权越权**：所有非公共接口必须强制 Bearer Token / Cluster Secret 校验，缺失或错误凭证必须严厉返回 HTTP 401，严禁静默放行。
4. **内存超标即挂**：Hub 静态常驻物理内存不得突破 **25 MB**，Agent 客户端常驻物理内存不得突破 **12 MB**；10MB 为优化目标。

---

## 二、 详细验收测试用例矩阵 (12 项全绿门禁)

| 用例 ID | 验证模块 | 测试动作与输入 | 预期结果与合格标准 | 验证方式 |
| :--- | :--- | :--- | :--- | :--- |
| **TC-01** | **OpenAPI 规范合法性** | `GET /openapi.json` | 格式符合 OpenAPI 3.1.0 标准，包含 servers URL、4 个标准 path、安全定义为 `bearerAuth`（HTTP Bearer），可通过 Swagger / OpenAI Actions 校验无报错。 | 机器静态校验 |
| **TC-02** | **未授权拦截** | 无 Token 请求 `/api/v1/exec` | 立即返回 HTTP 401 Unauthorized，响应体包含 `{"error": "Unauthorized"}`，不触发任何底层 bash。 | `curl -i` 验证 |
| **TC-03** | **本地基础命令执行** | `POST /api/v1/exec` (`{"device":"local","command":"uname -a"}`) | 返回 exit_code: 0，stdout 正确包含 Linux 内核信息，duration_ms > 0 且合理。 | 实测响应校验 |
| **TC-04** | **命令超时与进程回收** | 执行 `sleep 10`，设置 `timeout: 2` | 命令在 2 秒后强制终止，返回超时错误，且检查系统进程树确认 `sleep` 进程彻底消失，无僵尸残留。 | `ps -ef` 核查 |
| **TC-05** | **文件分页读取防爆** | 读取 10MB 文本文件，设置 `limit: 1024` | 仅返回前 1024 字节内容，返回 `total_size` 与 `has_more: true`，严禁一次性将全文件读入内存。 | 接口响应验证 |
| **TC-06** | **文件安全覆写** | `POST /api/v1/file/write` 写入 `/tmp/vps_cmd_test.txt` | 文件内容精确写入，权限正常，支持覆盖写入。 | 本地文件比对 |
| **TC-07** | **Agent 握手与注册** | 启动 Agent 连入 Hub WSS | Hub 面板与 `GET /api/v1/devices` 瞬间显示该设备为 `online`，展示正确 IP 与内核信息。 | 状态机校验 |
| **TC-08** | **心跳保活与离线感知** | 模拟杀死 Agent 进程 | 经过 2 个心跳周期（~60s）后，Hub 自动将该节点状态变更为 `offline`，不发生 panic。 | 节点状态监听 |
| **TC-09** | **跨节点路由下发** | `POST /api/v1/exec` (`{"device":"hwsg","command":"uptime"}`) | Hub 正确通过 WebSocket 寻址到远程 Agent，Agent 执行并原路返回，耗时增加在 RTT 合理范围内。 | 跨机实测 |
| **TC-10** | **Web 管理面板嵌入** | 浏览器直接访问 `GET /` | 页面秒开，设备列表、ChatGPT 向导、审计日志正常渲染，无任何外部 CDN 404，无 node 构建产物遗留。 | 浏览器/Headless 验证 |
| **TC-11** | **审计日志完备性** | 执行一次正常命令与一次失败命令 | 查询 SQLite `audit_logs` 表，两条记录均完整存在（含 IP、设备、指令、退出码、耗时），字段无空缺。 | SQLite 查询比对 |
| **TC-12** | **内存基线压测** | 连续并发执行 100 次 `ps aux` | 压测结束后，Hub RSS 必须 ≤ 25MB；20MB 为优化目标。压测结束后应回落并稳定，无持续增长趋势。 | `ps -eo rss` 采样 |

---

## 三、 严格收尾与交付规范 (Sign-off Checklist)

GPT 完成开发提交代码后，由我执行以下严苛收尾工作：
1. **代码审计 (Code Review)**：
   - 检查并发安全性（互斥锁 `sync.RWMutex` 是否合理、Channel 是否有阻塞泄漏风险）。
   - 检查外部输入 Shell 命令的执行上下文，是否存在注入或不可控行为。
2. **构建与打包 (Clean Build)**：
   - 生成标准的 `Makefile`，支持编译 Linux AMD64/ARM64 静态无依赖产物。
   - 清理所有中间编译缓存与临时测试文件。
3. **部署托管与 Systemd 固化**：
   - 为 Hub 编写规范的 `vps-commander-hub.service`。
   - Lucky 反代添加规则及 SSL 校验。
4. **终验报告与归档**：
   - 输出完整的《VPS-Commander 最终验收合格报告》。
   - 将完整配置参数与使用说明同步至长期记忆 `MEMORY.md`。
