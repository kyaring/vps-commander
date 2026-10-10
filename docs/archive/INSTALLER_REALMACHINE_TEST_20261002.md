# vps-commander 一键安装脚本真机等效测试报告（2026-10-02）

> **归档说明（2026-10-10）**：本文为历史快照，反映写作当时的状态，可能与现行 v1.1.30 不一致，不作为当前行为依据。已脱敏：域名、节点名与本地路径替换为占位符。现行文档见 [docs/README.md](../README.md)。

- **测试人**：测试工程师（独立真机测试）
- **测试时间**：2026-10-02 17:55–18:15（+08，UTC 09:55–10:15）
- **测试对象**：`deploy/install.sh`、`deploy/upgrade-hub.sh`、`deploy/upgrade-agent.sh`（commit 888efd1）
- **测试方式**：Docker 隔离容器，零生产接触
- **结论**：6 个用例全部通过；发现 1 个新问题（中低）

---

## 环境说明

| 项目 | 说明 |
|---|---|
| 宿主机 | hub-host（仅作为容器宿主，**未在宿主机运行任何安装脚本**） |
| 网络 | 专用 docker network `vpsc-test` |
| c-hub | `jrei/systemd-debian:13`，privileged + `--cgroupns=host` + cgroup rw，systemd 正常运行 |
| c-agent | 同上 |
| Hub 版本 | v1.1.25（安装时最新），后升级至 v1.1.26 |
| Agent 版本 | v1.1.26（安装时最新） |

**测试环境适配**（非脚本缺陷，特此说明）：
1. Hub 硬编码监听 127.0.0.1：测试中改为 0.0.0.0 以允许跨容器连接。
2. Agent 要求 wss://：在 c-hub 上搭建 Python TLS 转发器（0.0.0.0:9443 → 127.0.0.1:9521），自签证书（带 SAN）并加入 c-agent 信任库。
3. install.sh 不负责注册 Agent 凭据：手动在测试 Hub 的 DB 插入 `device_credentials`（SHA256 hash）。
4. 容器内预装：curl、ca-certificates、procps、python3、sqlite3、openssl。

---

## T1：全新安装 Hub 模式

- **步骤**：c-hub 上 `bash /root/install.sh`，输入 `2`（Hub）、端口/凭据全部默认。
- **预期**：退出 0、服务 active、9521 监听、`hub.env` 权限 600。
- **实际**：退出 0；`systemctl is-active` = active；`curl http://127.0.0.1:9521/` = HTTP 200；`hub.env` 权限 600，API Key 已生成保存。
- **结果**：✅ 通过
- **备注**：全新安装时打印了“现有凭据已保留，不会在重复安装时重新生成”——文案在全新安装场景下有误导性（并无“现有”凭据），属 косметик 问题。

---

## T2：全新安装 Agent 模式

- **步骤**：c-agent 上 `bash /root/install.sh`，输入 `1`（Agent）、Hub 地址 `wss://c-hub:9443/agent/ws`、节点名默认、Token `test-agent-token-001`（已在测试 Hub 注册）。
- **预期**：退出 0、服务 active、15 秒内日志出现 Hub 连接成功标志。
- **实际**：退出 0，`[SUCCESS] VPS-Commander Agent 安装、启动并完成 Hub 连接校验！`；日志出现 `MCP services synced: 0`；测试 Hub 的 DB 显示 `c-agent|online`。
- **结果**：✅ 通过

---

## T3：重跑 Hub 安装（审计高#1 回归）

- **步骤**：在 c-hub 上再次运行 install.sh（Hub 模式，全默认），对比重跑前后 `hub.env` 的 API Key。
- **预期**：API Key 被保留，有明确提示。
- **实际**：脚本打印 `[INFO] 检测到已有 Hub 配置，将保留现有 API Key / Web 密码，不覆盖凭据。`；重跑前后 API Key 完全一致（`f8fff878fae3799943b9…`）；服务保持 active。
- **结果**：✅ 通过 —— 审计高#1（静默覆盖 hub.env）已修复。

---

## T4：注入 payload 测试（审计高#3 回归）

- **步骤**：
  - T4a：节点名输入 `test$(touch /tmp/pwned)`
  - T4b：Hub 地址输入 ``wss://c-hub:9443/agent/ws`touch /tmp/pwned2` ``
- **预期**：均被拒绝，`/tmp/pwned` 和 `/tmp/pwned2` 未被创建。
- **实际**：
  - T4a：`[ERROR] 节点名称仅允许字母、数字、点、下划线、冒号和连字符`，退出 1，`/tmp/pwned` 不存在。
  - T4b：`[ERROR] Hub WSS 地址格式无效或包含 shell 元字符`，退出 1，`/tmp/pwned2` 不存在。
- **结果**：✅ 通过 —— 审计高#3（命令注入）已修复。另确认：新版 unit 已改用 systemd 原生变量展开，无 `/bin/sh -c` 包装。

---

## T5：Token 输入回显测试（审计高#2 回归）

- **步骤**：用 Python pty 模拟人工逐字输入（等待 prompt 出现后再发送），调用 `read_tty "Enter secret: " VAR 1`，检查 pty 输出中是否出现 token。
- **预期**：token 不出现在终端输出中，且值被正确读取。
- **实际**：`TOKEN_IN_PTY_OUTPUT: False`，`GOT_LEN:13`（值正确读取）。
- **结果**：✅ 通过 —— 审计高#2（明文回显）已修复（`read -s` 生效）。
- **备注**：直接用管道 `printf | script` 测试时 token 会出现在输出中，系 pty 行规程在 `read -s` 生效前回显管道输入，属测试手法产物，非脚本缺陷。

---

## T6：升级脚本测试

### upgrade-hub.sh
- **步骤**：在 c-hub 上运行 `bash /root/upgrade-hub.sh`。
- **实际**：`[SUCCESS] Hub 成功升级至 v1.1.26，SHA256、systemd、/healthz 均验证通过；hub.env 未修改。`；API Key 与升级前一致；服务 active。
- **结果**：✅ 通过

### upgrade-agent.sh（含回滚路径）
- **步骤**：在 c-agent 上运行（当时 agent 配置了未注册的 token，模拟 Hub 不可达）。
- **实际**：下载 v1.1.26 并通过 SHA256 校验；重启后 15 秒未确认 Hub 同步 → `[WARN] 新版本服务运行但未在 15 秒内确认 Hub 同步，正在回滚...` → `[ERROR] 升级失败，已恢复旧版本并重新启动 Agent`；验证旧二进制已恢复、服务 active。
- **结果**：✅ 通过 —— 回滚路径真实生效。

---

## 发现的新问题

### 【中低】install.sh 假设 `ps` 存在，精简系统上误报安装失败
- **位置**：`deploy/install.sh` Agent 安装后的进程参数校验（`ps -eo args=`）。
- **复现**：在未安装 `procps` 的 Debian 精简容器中运行 Agent 安装，agent 实际安装并启动成功（service active），但脚本报错 `[ERROR] Agent 进程参数校验失败: `（空）并以 exit 1 退出。
- **影响**：在最小化 VPS（如 Debian netinst 精简安装）上，安装会被误判为失败，用户可能重复安装或误以为 agent 没跑起来。
- **建议**：安装前检查 `ps` 是否存在，不存在则 `apt-get install -y procps`（脚本已有 apt 安装 curl 的先例），或改用 `/proc` 直接读取。

---

## 总体结论

6 个用例全部通过。888efd1 对审计报告的 3 个高优先级问题（hub.env 静默覆盖、token 明文回显、命令注入）**修复均经真机验证有效**；新增的 `upgrade-hub.sh` 工作正常且不碰凭据；`upgrade-agent.sh` 的回滚路径真实可用。

唯一新发现是 `ps` 依赖缺失导致的误报失败（中低），建议修复后再公开发布。

*测试工程师，2026-10-02*
