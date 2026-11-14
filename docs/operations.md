# 运维手册

适用版本：v1.1.30。

## 1. 日常检查

```bash
systemctl status vps-commander-hub vps-commander-agent
journalctl -u vps-commander-hub -n 100 --no-pager
curl -fsS http://127.0.0.1:9521/healthz
```

集群状态可在面板“设备”分区、`GET /api/v1/devices` 或 MCP `list_devices` 查看。

## 2. 升级 Hub

一键升级（不修改 `hub.env`）：

```bash
curl -fsSL https://github.com/kyaring/vps-commander/releases/latest/download/upgrade-hub.sh | sudo bash
```

流程：下载 → 按 `checksums.txt` 校验 SHA256 → 备份旧二进制 → 原子替换 → 重启 → `/healthz` 检查，失败自动回滚。

生产环境建议采用带探测的发布：

1. 备份：旧二进制、`sqlite3 commander.db ".backup …"`、`hub.env`；
2. 从干净工作树构建或使用 Release 产物，核对 SHA256；
3. 记录升级前在线 Agent 数；
4. 用 `install` + `mv` 原子替换二进制（直接 `cp` 覆盖运行中的文件会报 `Text file busy`）后重启；
5. 检查 `/healthz`，并用只读（`devices:read`）探测令牌确认在线 Agent 数恢复到升级前水平；
6. 不满足则立即用备份二进制回滚。

建议在 systemd 临时单元中执行（`systemd-run --on-active=3 …`），避免通过 Hub 自身执行升级时随 Hub 重启而中断。

## 3. 升级 Agent

```bash
curl -fsSL https://github.com/kyaring/vps-commander/releases/latest/download/upgrade-agent.sh | sudo bash
```

Hub 与 Agent 协议向后兼容，先升级 Hub，再逐台升级 Agent。

## 4. 回滚

```bash
install -m 0755 /opt/vps-commander/backup-<时间>/vps-commander-hub /opt/vps-commander/vps-commander-hub.rb
mv -f /opt/vps-commander/vps-commander-hub.rb /opt/vps-commander/vps-commander-hub
systemctl restart vps-commander-hub
```

数据库结构变更为增量迁移；如需恢复数据，先停 Hub，再用备份的 `commander.db` 覆盖。

## 5. 数据库备份

```bash
sqlite3 /opt/vps-commander/data/commander.db ".backup /var/backups/commander-$(date +%F).db"
chmod 0600 /var/backups/commander-*.db
```

数据库包含令牌和设备凭据的哈希、审计记录，备份文件按密钥同等级别保管。

## 6. 日志

- Hub 请求日志使用结构化 `slog` 输出到 journald，级别由 `VPS_COMMANDER_LOG_LEVEL` 控制；`/healthz`、`/static/` 和 `304` 不记录。
- 操作审计在面板“日志”或 `/api/v1/audits` 查看，不在 journald 中。

### 6.1 审计失败排查

审计写入走异步批量队列，正常情况下失败会重试并把条目落回 stderr；用以下标记确认是否发生过降级：

| 标记 | 含义 |
| :--- | :--- |
| `vps_commander_audit_fallback` | 审计条目重试仍失败，已改为写入 stderr 的 JSON 行（含原始 `entry` 与 `persistence_error`） |
| `ERROR audit batch write attempt` | 批量写库失败，行内含 `attempt/attempts` 与条目数，连续出现说明数据库持续不可写 |
| `CRITICAL audit fallback` | fallback 自身也失败（JSON 编码或 stderr 写失败），属最高优先级 |

这些日志都进 journald，检索方式：

```bash
journalctl -u vps-commander-hub --since "24 hours ago" | grep vps_commander_audit_fallback
journalctl -u vps-commander-hub --since "24 hours ago" | grep "ERROR audit batch write attempt"
journalctl -u vps-commander-hub --since "24 hours ago" | grep "CRITICAL audit fallback"
```

`vps_commander_audit_queue_depth` 持续增长 + 上述标记同时出现，基本可判定为数据库写入受阻（磁盘满、`SQLITE_BUSY`、文件锁）。此时不要重启 Hub（内存队列里的审计会丢），先解决磁盘/锁问题；必要时用 fallback 的 stderr JSON 行补录审计。

投递与下行的失败记录（`internal/notify`、`internal/cluster`、`internal/executor`）同样是 `log.Printf` 一行一条，可直接按前缀检索：

```bash
journalctl -u vps-commander-hub | grep "notify: record"
journalctl -u vps-commander-hub | grep "cluster: "
journalctl -u vps-commander-agent | grep "heartbeat ping write failed"
journalctl -u vps-commander-hub | grep "executor: fsync directory"
```

## 7. 指标

用带 `admin` scope 的令牌抓取 `/metrics`（Prometheus 文本格式），关注：

| 指标 | 关注点 |
| :--- | :--- |
| `vps_commander_online_agents` | 低于预期即有节点掉线 |
| `vps_commander_audit_queue_depth` | 持续增长说明审计写入积压 |
| `vps_commander_in_flight_exec` | 接近并发上限会出现 429 |
| HTTP 429 计数 | 令牌限速或握手限流触发频率 |
| `vps_commander_process_resident_memory_bytes`、`vps_commander_goroutines` | 资源泄漏迹象 |

## 8. 令牌轮换与吊销

- 单个客户端令牌泄露：面板“接入与令牌”中吊销或轮换，再给客户端换新密钥。
- legacy 密钥：确认审计中已无 `legacy` 调用后，设置 `VPS_COMMANDER_LEGACY_KEY=off` 或在面板轮换。
- 设备凭据泄露：在面板吊销该设备，重新签发凭据并更新 `agent.env`。

## 9. 常见问题

| 问题 | 处理 |
| :--- | :--- |
| MCP 客户端报 405 method not allowed | Streamable HTTP 地址应为 `/mcp`，不是 `/mcp/sse` |
| exec 一直等到超时（exit 124） | 命令里有未重定向输出的后台进程；用 `nohup … > log 2>&1 < /dev/null &` 或改用 Session |
| 被拒绝：`security mode … denies exec` | 节点安全等级低于 High；按需在面板调整，或改用 `diagnostic_command` |
| 403 scope denied | 令牌缺少 scope；在面板编辑令牌 |
| 审计 IP 全是 127.0.0.1 | 位于反代后但未设置 `VPS_COMMANDER_TRUST_PROXY=on` |
| Agent 集体重连时出现 429 | 握手限流的预期行为，Agent 会自动退避 |
| 面板新令牌密钥找不到了 | 完整密钥只显示一次，只能轮换或重建 |
