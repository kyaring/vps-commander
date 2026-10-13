# 上下文预算与输出瘦身（v0.1，RikkaHub-1 2026-10-14 起草）

背景：审核/运维 agent 通过 MCP 调 `exec_command` 作业时，Hub 把 stdout **原样**回传（executor 上限 1MB），且没有状态聚合接口，导致 agent 每轮重复推导同一批事实、上下文迅速膨胀。实测：一次 `journalctl -n 20` 回传约 100 行近似重复日志（≈7KB）；同一批通道/路由/投递状态在一个会话里被重复查询 6 轮。

## 一、量化指标（本批的验收目标）

| 指标 | 目标 |
|---|---|
| 单次工具回传默认上限 | **≤ 4KB**（超出自动截断并附元数据） |
| 生产状态核对 | **1 次调用**完成 |
| 单卡完整复核 | **≤ 8 次工具调用** |
| 交接文档加载 | **≤ 4KB**（摘要段） |

## 二、原则
1. **默认窄、按需宽**：默认返回最小必要摘要；细节由调用方显式放大。
2. **截断必须可见**：任何截断都要带 `truncated/total_bytes/total_lines/sha256`，不得静默丢数据。
3. **聚合在服务端**：能由 Hub 推导的结论，不要让 agent 自己写 SQL。
4. **向后兼容**：新参数默认值不得破坏现有调用方。

## 三、契约 A：`exec_command` 输出整形与去重（对应卡 T-20261014-21）

新增可选参数：

| 参数 | 取值 | 默认 |
|---|---|---|
| `output_mode` | `raw` \| `tail` \| `head` \| `count` \| `none` | `raw` |
| `max_bytes` | 整数，上限 1048576 | **4096** |
| `dedup_window_seconds` | 整数 | **0（关闭）** |

- **截断语义**：超出 `max_bytes` 时保留尾部（`output_mode=head` 时保留头部），并在输出首行插入
  `[truncated: kept <X> of <Y> bytes / <L> lines; sha256=<hex12>]`；返回体同时带 `truncated:true, total_bytes, total_lines, sha256`。
- **去重语义**：`dedup_window_seconds>0` 时，同 `(device, workdir, command, output_mode, max_bytes)` 在窗口内重复调用，返回
  `{"unchanged":true,"hash":"<hex12>","first_seen":"<RFC3339>","cached_bytes":N,"exit_code":N}`，不重发正文。
- `output_mode=count` 只返回 `total_bytes`/`total_lines`/`exit_code`；`none` 只返回退出码与元数据。
- HTTP `/api/v1/exec` 与 MCP `exec_command` 字段名必须一致。

## 四、契约 B：状态断言接口（对应卡 T-20261014-22）

`GET /api/v1/health/assertions`

- 授权：`devices:read` 或 `admin`（任一即可；**不新增 scope**，避免令牌变更）。
- 响应体 ≤1.5KB，`assertions` 上限 32 条：

```json
{"generated_at":"<RFC3339>","summary":{"pass":9,"fail":0},
 "assertions":[{"id":"service.active","ok":true,"detail":"active; NRestarts=0"}]}
```

- v0.1 断言集合：

| id | 判定 |
|---|---|
| `service.active` | 服务为 active |
| `service.restarts` | `NRestarts=0` |
| `hub.healthz_internal` | 本机 `GET /healthz` 200 |
| `hub.healthz_public` | 公网 `/healthz` 200（失败只记 fail，不阻塞其它断言） |
| `notification.legacy_retired` | 旧通道 `enabled=0` 且 `endpoint` 为空 |
| `notification.no_plaintext` | `endpoint<>''` 计数=0 且 `webhook_targets` 计数=0 |
| `notification.routes` | 启用路由条数（`detail` 只报数量） |
| `notification.deliveries` | `pending`/`failed` 条数（`detail` 只报数量） |
| `db.integrity` | `PRAGMA integrity_check` 为 ok（慢查询需缓存，缓存期 ≥60s） |

- **严禁**返回任何 endpoint、webhook、密钥、令牌明文；只允许计数、布尔、枚举与已脱敏短串。
- 单条断言内部出错记 `ok:false` 并在 `detail` 给出简短原因，不得让整个接口 5xx。

## 五、本批不做
- 不改审计的 512 字符截断逻辑、不新增 scope、不改面板。
- 不做 L2（会话预算统计、会话级 scratchpad、交接文档 front-matter 化）——留待后续批次。

## 六、预期收益
- `journalctl -n 20` 这类调用回传从 ≈7KB 降到 ≤4KB（且默认只留尾部）。
- 「生产状态核对」由 6 轮 SQL 降为 1 次 `GET /api/v1/health/assertions`。
- 单卡复核的工具调用次数与回传字节可被度量、可回归。
