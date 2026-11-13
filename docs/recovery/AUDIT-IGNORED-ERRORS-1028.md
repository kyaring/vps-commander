# 审计：非测试代码中的 `_ =` 忽略错误（阶段一：分类报告）

- 对应卡：T-20261028-03（阶段一，只出报告，不改任何 .go 文件）
- 基线：本分支分叉点 `c32701e`；统计口径下的实际代码基线 `ac25d16`（详见 1.2）
- 作者：Claude（开发），待 Hark 复核；本版为审核返工版（T-20261028-03: rework）
- 本报告不做任何代码修改；阶段二（改行为）须经 OPC 决定。

## 1. 统计方法与口径

### 1.1 grep 口径（卡面第一条）

```
git grep -n "_ = \|_, _ = " -- 'internal/*.go' 'cmd/*.go' | grep -v '_test\.go:'
```

结果：**257 行命中**（按丢弃点计 259 处：`internal/cluster/mcp.go:139`、`:282` 各在同一行写了 2 个 `_ =`）。本报告以**行**为单位统计，三类数量之和 = 257。

口径说明：
- 匹配 `_ = `（含 `var _ =`）与 `_, _ = `，不含 `for _ = range` 这类语法（本仓库当前 0 处）。
- 不含 `, _ :=` 形式的丢弃（例如 `body, _ := io.ReadAll(...)`）。这是另一类丢弃，本卡口径不覆盖，见 6.4 的阶段二扩展建议。

### 1.2 基线差异（与卡面不一致之处）

| 项 | 卡面写的 | 实际情况 |
|---|---|---|
| 基线 commit | 卡面「基线：main `1fc5dda`」，正文「以 `1fc5dda` 为准」 | 仓库中可解析的是 `1fc5dda`（`1fc5ddaff10c1860457cd25cb8aa49ed443fad56`）。审核意见里出现的 `1c5fda`（7 位）**不是合法 sha**：`git rev-parse 1c5fda` 报「有歧义的参数」，按 7 位前缀在仓库中找不到唯一 commit。本报告以 `1fc5dda` 为卡面基线。 |
| 本分支分叉点 | — | `c32701e`（`1fc5dda` 是它的祖先，`git merge-base --is-ancestor 1fc5dda c32701e` 为真）。`1fc5dda..c32701e` 之间有 6 个提交，主要是 T-20261027-06（远端异步 exec）合入，**含代码改动**。 |
| 实际统计基线 | — | `ac25d16`。`git diff --stat ac25d16 c32701e` 只有 `docs/tasks/T-20261028-03.md`（+4/-1），`git diff --stat ac25d16 c32701e -- '*.go'` 为空 —— **两者代码完全一致**。本报告所有行号按 `ac25d16`（= 本分支 HEAD 的代码）给出。 |
| 卡面「约 319 处」 | 319 | **无法复现**。在 `1fc5dda` 上按本节口径实测 254 行；`1fc5dda` 全 `*.go`（含测试）为 323 行；`ac25d16` 按本节口径 257 行、含测试 326 行。没有任何一种自然口径得到 319，推测卡面数字来自发卡时的一次性手数或不同仓库快照。**以本节 257 行为准。** |

> 因此本报告的行号与卡面基线 `1fc5dda` 存在偏差。逐条比对两版的 `_ =` 命中集合，`ac25d16` 相对 `1fc5dda` **只多 3 条**（全部来自 T-20261027-06 新增的远端异步 exec 代码，无一条被删除）：
> - `internal/api/exec_jobs.go:388` `_ = b.server.auditMCPContext(cancelCtx, clientIP, ref.Device, "exec_cancel", ...)`
> - `internal/api/exec_jobs.go:393` `_ = b.server.auditMCPContext(ctx, clientIP, view.Device, "exec_cancel", ...)`
> - `internal/api/exec_jobs_remote.go:193` `_ = s.auditMCPContext(endCtx, j.ClientIP, j.Device, "exec", ...)`
>
> 复现：`git grep -n "_ = \|_, _ = " 1fc5dda -- 'internal/*.go' 'cmd/*.go' | grep -v '_test\.go:' | wc -l` → 254；同一命令对 `ac25d16` → 257。这 3 条在本报告中都归 A1（审计写入），所以卡面基线下应为 A=88、B=60、C=106、合计 254。核对行号时请用 `ac25d16` 或本分支 HEAD。

### 1.3 复现命令（可直接执行，得到与本报告完全一致的三个数字）

在仓库根目录保存为 `classify.sh` 后 `sh classify.sh`：

```sh
#!/bin/sh
# 输出：类别 <TAB> 分组 <TAB> 位置 <TAB> 所在函数 <TAB> 原文
set -eu
git grep -n "_ = \|_, _ = " -- 'internal/*.go' 'cmd/*.go' | grep -v '_test\.go:' | awk -F: '
BEGIN{
  # C 类逐条指派（共 106 条；P1-1 参数解析 / P1-2 fail-open / P1-3 PKCE / P2 掩盖故障 / P3 无用户影响 / P4 随机数与占位）
  split("internal/mcp/mcp.go:1279 internal/mcp/mcp.go:1357 internal/mcp/mcp.go:1460 internal/mcp/mcp.go:1485 internal/mcp/mcp.go:1504 internal/mcp/mcp.go:1516 internal/mcp/mcp.go:1528 internal/mcp/mcp.go:1545 internal/mcp/mcp.go:1560 internal/mcp/mcp.go:1572 internal/mcp/mcp.go:1588 internal/mcp/mcp.go:1602 internal/mcp/mcp.go:1628 internal/mcp/mcp.go:1635 internal/mcp/mcp.go:1905", a, " "); for(i in a) G[a[i]]="P1-1"
  G["internal/storage/tokens.go:282"]="P1-2"
  G["internal/mcp/oauth_flow.go:559"]="P1-3"
  G["internal/mcp/mcp.go:704"]="P4"; G["internal/api/exec_jobs.go:154"]="P4"
  split("internal/api/mcp_adapter.go:48 internal/storage/oauth.go:651 internal/storage/oauth.go:652 internal/storage/tokens.go:287 internal/mcp/mcp.go:224 internal/api/token_limiter.go:239 internal/api/token_limiter.go:240 internal/cluster/manager.go:110 internal/cluster/manager.go:206 internal/cluster/manager.go:327 internal/cluster/manager.go:376 internal/cluster/manager.go:382 internal/cluster/manager.go:510 internal/cluster/manager.go:789 internal/cluster/manager.go:794 internal/cluster/manager.go:795 internal/cluster/manager.go:796 internal/api/api.go:1109 internal/api/tokens.go:166 internal/api/tokens.go:170 internal/storage/notification_policy.go:137 internal/storage/notification_store.go:244 internal/storage/notification_store.go:256 internal/storage/notification_store.go:268 internal/notify/notify.go:210 internal/notify/notify.go:781 internal/notify/outbox.go:142 internal/notify/outbox.go:163 internal/storage/tokens.go:276 internal/mcp/oauth_flow.go:660 cmd/agent/main.go:203 cmd/agent/main.go:281 cmd/agent/main.go:381", b, " "); for(i in b) G[b[i]]="P2"
  split("cmd/agent/main.go:250 cmd/hub/main.go:220 cmd/hub/main.go:221 cmd/hub/main.go:222 cmd/hub/main.go:223 cmd/loadtest/main.go:494 cmd/loadtest/main.go:573 internal/api/api.go:1032 internal/api/api.go:1066 internal/api/api.go:1721 internal/api/api.go:175 internal/api/api.go:182 internal/api/api.go:204 internal/api/api.go:306 internal/api/api.go:428 internal/api/health_assertions.go:57 internal/api/health_assertions.go:92 internal/api/health_assertions.go:97 internal/api/notification_deliveries.go:84 internal/api/session_memo.go:121 internal/api/session_stats.go:59 internal/api/session_stats.go:65 internal/api/session_stats.go:127 internal/api/token_limiter.go:157 internal/api/token_limiter.go:256 internal/api/tokens.go:222 internal/api/tokens.go:296 internal/api/tokens.go:301 internal/api/tokens.go:312 internal/api/tokens.go:335 internal/api/tokens.go:338 internal/auth/auth.go:246 internal/auth/auth.go:297 internal/cluster/manager.go:335 internal/cluster/manager.go:343 internal/executor/atomic_write.go:52 internal/executor/executor.go:36 internal/executor/fs.go:294 internal/executor/fs.go:295 internal/mcp/mcp.go:1021 internal/mcp/mcp.go:1061 internal/mcp/mcp.go:257 internal/mcp/mcp.go:837 internal/mcp/mcp.go:841 internal/mcp/mcp.go:855 internal/mcp/mcp.go:908 internal/mcp/oauth.go:65 internal/mcp/oauth_flow.go:360 internal/notify/notify.go:782 internal/notify/notify.go:785 internal/web/panel.go:429 internal/web/panel.go:449 internal/web/panel.go:456 internal/web/panel.go:618", c, " "); for(i in c) G[c[i]]="P3"
}
function load(p,   l,n,f,s){ delete FN; n=0; f="(包级)"
  while ((getline l < p) > 0){ n++
    if (l ~ /^func /){ s=l; sub(/^func \([^)]*\) /,"",s); sub(/^func /,"",s); sub(/[^A-Za-z0-9_].*$/,"",s); f=s }
    FN[n]=f }
  close(p) }
{ pos=index($0,":"); file=substr($0,1,pos-1); rest=substr($0,pos+1)
  pos=index(rest,":"); ln=substr(rest,1,pos-1); call=substr(rest,pos+1); gsub(/^[ \t]+/,"",call)
  if (file!=cur){ cur=file; load(file) }
  key=file":"ln
  if (call ~ /Record(WebhookDelivery|NotificationChannelTestResult)\(/) { cls="A"; grp="A2" }
  else if (call ~ /\.(audit[A-Za-z]*|Audit[A-Za-z]*)\(/) { cls="A"; grp="A1" }
  else if (call ~ /\.(Close|Remove|RemoveAll|Rollback|Unlock|Stop|Kill|Wait|Sync|Flush|Release)\(/ || call ~ /io\.Copy\(io\.Discard/) { cls="B"; grp="B" }
  else { cls="C"; grp=G[key]; if (grp=="") { grp="UNASSIGNED"; bad++ } }
  printf "%s\t%s\t%s:%s\t%s\t%s\n", cls, grp, file, ln, FN[ln], call
  n[cls]++ }
END{ printf "== 合计 %d（A=%d B=%d C=%d）\n", n["A"]+n["B"]+n["C"], n["A"], n["B"], n["C"] > "/dev/stderr"
     if (bad) printf "!! %d 条 C 未指派分组\n", bad > "/dev/stderr" }'
```

分类规则（脚本内即为准，正文叙述只是说明）：

1. **A（审计 / 投递记录写入）**：被丢弃调用的方法名匹配正则 `\.(audit[A-Za-z]*|Audit[A-Za-z]*)\(`（`s.audit`、`s.auditMCPContext`、`s.auditContextTarget`、`s.Store.AuditEntry`、`s.AuditMCP` 等）→ A1；匹配 `Record(WebhookDelivery|NotificationChannelTestResult)\(` → A2。
2. **B（清理）**：方法名匹配 `\.(Close|Remove|RemoveAll|Rollback|Unlock|Stop|Kill|Wait|Sync|Flush|Release)\(`，或调用是 `io.Copy(io.Discard, ...)`（读完响应体以便复用连接）。
3. **C（其他）**：其余全部。C 类**不再用关键词自动归类**，而是在脚本 BEGIN 段逐条指派到 P1-1/P1-2/P1-3/P2/P3/P4 六个分组（共 106 条，脚本末尾断言无 UNASSIGNED）。

> 为什么 A/B 可以用正则、C 必须逐条指派：审核实测「按关键词规则无法复现」的根因就是 C 类里混着 `json.Unmarshal`、`w.Write`、`json.NewEncoder(w).Encode`、`fmt.Sscanf` 等语义完全不同、只能人工判断的调用。本版把「机器可判定的部分」（A/B）与「必须人工判断的部分」（C 的分组）分开：前者正则、后者显式清单，两者都在同一个脚本里，跑一遍即可核对。

复现输出（`sh classify.sh | awk -F'\t' '{print $1" "$2}' | sort | uniq -c`）：

```
     87 A A1
      4 A A2
     60 B B
     15 C P1-1
      1 C P1-2
      1 C P1-3
     33 C P2
     54 C P3
      2 C P4
```

**A = 91、B = 60、C = 106、合计 = 257。**

### 1.4 与上一版的差异（审核意见阻塞 1）

| 项 | 上一版 | 本版 |
|---|---:|---:|
| A 审计写入 | 113 | **91**（拆 A1=87 写入审计库 / A2=4 通知投递记录） |
| B 清理 | 54 | **60** |
| C 其他 | 90 | **106** |
| 合计 | 257 | 257（不变，口径未动） |

被移出 A 的 22 条（审核逐条点名，全部核实）及新归属：

| 位置 | 所在函数 | 被丢弃的调用（截断） | 上一版 | 本版 | 理由 |
|---|---|---|---|---|---|
| `internal/notify/notify.go:781` | deliverEvent | `_ = json.Unmarshal([]byte(t.Config), &c)` | A | C/P2 | 解析 webhook 配置，与审计写入无关 |
| `internal/notify/notify.go:782` | deliverEvent | `body, _ = json.Marshal(...chat_id/text...)` | A | C/P3 | 序列化 map[string]any，实际不会失败 |
| `internal/notify/notify.go:785` | deliverEvent | `body, _ = json.Marshal(...msgtype/text...)` | A | C/P3 | 同上 |
| `internal/notify/notify.go:803` | deliverEvent | `_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))` | A | **B** | 读完响应体以便连接复用，是清理动作 |
| `internal/notify/outbox.go:142` | sendNotificationDelivery | `_ = json.Unmarshal([]byte(job.Event.DataJSON), &legacyData)` | A | C/P2 | 解析事件数据，非审计写入 |
| `internal/notify/outbox.go:163` | sendNotificationDelivery | `_ = json.Unmarshal([]byte(job.Channel.ConfigJSON), &cfg)` | A | C/P2 | 解析通道配置，非审计写入 |
| `internal/notify/outbox.go:281` | postJSON | `_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))` | A | **B** | 同 803，清理动作 |
| `internal/notify/notify.go:210` | importOpenClawWebhook | `_ = m.Store.SaveWebhookTarget(...)` | A | C/P2 | 是「写业务配置」不是「写审计」；失败则一次性导入静默不生效 |
| `internal/storage/notification_store.go:256` | SaveNotificationChannel | `_ = json.Unmarshal(secrets["webhook_url"], &webhookURL)` | A | C/P2 | 解析凭据字段，非审计写入 |
| `internal/mcp/mcp.go:224` | AuditIntent | `_ = json.Unmarshal(raw, &a)` | A | C/P2 | 函数名叫 AuditIntent，但动作是**解析参数**生成意图字符串，不写任何审计记录 |
| `internal/mcp/mcp.go:257` | AuditIntent | `_ = oldText // Never retain either file body...` | A | C/P3 | **不是忽略错误**，是消除未用变量（与 `var _ = fmt.Sprintf` 同类） |
| `internal/api/tokens.go:166` | requestAuditIntent | `body, _ = io.ReadAll(io.LimitReader(r.Body, 1024*1024))` | A | C/P2 | 读请求体（函数名含 Audit 但动作是读 body） |
| `internal/api/tokens.go:170` | requestAuditIntent | `_ = json.Unmarshal(body, &fields)` | A | C/P2 | 解析请求体字段 |
| `internal/api/tokens.go:296` | AuditExportCSV | `_ = c.Write([]string{"# error: export incomplete"})` | A | C/P3 | 写 CSV 响应体 |
| `internal/api/tokens.go:301` | AuditExportCSV | `_ = c.Write([]string{fmt.Sprintf("# truncated at %d rows", ...)})` | A | C/P3 | 写 CSV 响应体 |
| `internal/api/tokens.go:312` | AuditExportCSV | `_ = c.Write([]string{"# error: export incomplete"})` | A | C/P3 | 写 CSV 响应体 |
| `internal/api/api.go:306` | legacyWebhookGone | `_ = json.NewEncoder(w).Encode(map[string]string{"error": ...})` | A | C/P3 | 写 HTTP 响应体 |
| `internal/api/notification_deliveries.go:84` | NotificationDeliveryRetryV1JSON | `_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, ...})` | A | C/P3 | 写 HTTP 响应体 |
| `cmd/loadtest/main.go:663` | deleteStreamableSession | `_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))` | A | **B** | 清理动作 |
| `cmd/loadtest/main.go:698` | streamableRPC | `_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))` | A | **B** | 清理动作 |
| `cmd/loadtest/main.go:817` | openSSE | `_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))` | A | **B** | 清理动作 |
| `cmd/loadtest/main.go:911` | call | `_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))` | A | **B** | 清理动作 |

另外，B 类由 54 → 60 是因为 `io.Copy(io.Discard, ...)` 一类（6 条）统一并入「清理」，与 `Close` 同族。

## 2. 分类结果

| 类别 | 分组 | 数量 | 含义 |
|---|---|---:|---|
| A 审计写入 | A1 | 87 | 写审计库（`audit*` / `Audit*`） |
| A 审计写入 | A2 | 4 | 写通知投递记录（`RecordWebhookDelivery` / `RecordNotificationChannelTestResult`） |
| B 清理 | B | 60 | `Close`/`Remove`/`Rollback`/`Kill`/`io.Copy(io.Discard,...)` 等 |
| C 其他 | P1-1 | 15 | MCP 参数解析失败被吞，可能误路由 |
| C 其他 | P1-2 | 1 | 设备白名单解析失败 → fail-open |
| C 其他 | P1-3 | 1 | PKCE 失败时消费授权码的结果被丢 |
| C 其他 | P2 | 33 | 会掩盖真实故障（含审核补充的 cluster/agent 写指令丢弃） |
| C 其他 | P3 | 54 | 无用户可见影响（写响应体、占位、诊断日志等） |
| C 其他 | P4 | 2 | `crypto/rand.Read` 返回值被丢（本工具链下不可达，见 5.5） |
| 合计 | | **257** | 与 1.1 的 grep 行数一致 |

按包分布：

| 包 | A1 | A2 | B | C | 合计 |
|---|---:|---:|---:|---:|---:|
| internal/api | 86 | 0 | 9 | 31 | 126 |
| internal/mcp | 0 | 0 | 0 | 28 | 28 |
| cmd/loadtest | 0 | 0 | 18 | 2 | 20 |
| internal/cluster | 0 | 0 | 9 | 12 | 21 |
| internal/executor | 0 | 0 | 16 | 4 | 20 |
| internal/storage | 0 | 0 | 4 | 9 | 13 |
| internal/notify | 1 | 4 | 3 | 6 | 14 |
| internal/web | 0 | 0 | 0 | 4 | 4 |
| cmd/agent | 0 | 0 | 1 | 4 | 5 |
| cmd/hub | 0 | 0 | 0 | 4 | 4 |
| internal/auth | 0 | 0 | 0 | 2 | 2 |
| 合计 | 87 | 4 | 60 | 106 | 257 |

复现命令（接 1.3 的 `sh classify.sh`）：

```sh
sh classify.sh | awk -F'\t' '{split($3,p,":"); n=p[1]; sub(/\/[^\/]*$/,"",n); c[n" "$1]++} END{for(k in c) print k, c[k]}' | sort
```

## 3. A 类：审计写入（91 处）——结论：A1 失败不无痕，**A2 失败完全无痕**

### 3.1 抽样（14 处，覆盖 3 条互不相同的调用链）

上一版抽样 12 处**全部取自 `internal/api/api.go` 的 `s.audit` 同一条调用链**，不具代表性，正好漏掉投递记录这条反例。本版抽样覆盖三条链：

**链 1：`s.audit` / `s.auditMCPContext` → `auditContextTarget` → `Store.AuditEntry`（A1，87 处）**

- `internal/api/api.go:173` [requireDeviceAccess] `_ = s.audit(r, device, "auth_denied", "", http.StatusUnauthorized, 0, "missing principal")`
- `internal/api/api.go:179` [requireDeviceAccess] `_ = s.audit(r, device, "device_denied", "", 0, 0, "token device whitelist denied")`
- `internal/api/api.go:201` [requireSecurity] `_ = s.audit(r, device, "security_denied", action, http.StatusForbidden, 0, reason)`
- `internal/api/api.go:276` [UpdateNotificationSettingsJSON] `_ = s.Store.AuditEntry(storage.AuditEntry{Action: "notification_setting", ...})`
- `internal/api/api.go:303` [legacyWebhookGone] `_ = s.audit(r, "", "notification_setting_legacy_gone", r.URL.Path, http.StatusGone, 0, "legacy ...")`
- `internal/api/api.go:369` [UpdateLegacyKeyStatusJSON] `_ = s.Store.AuditEntry(storage.AuditEntry{Action: "legacy_key_setting", ...})`
- `internal/api/api.go:410` [RecordLegacyRejected] `_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_rejected", ...})`
- `internal/api/api.go:493` [ProvisionDeviceCredentialJSON] `_ = s.audit(r, q.Device, "device_credential_provision", "device="+q.Device, 0, 0, "credential ...")`
- `internal/api/api.go:538` [UpdateSecuritySettingsJSON] `_ = s.audit(r, "", "security_setting", "global="+q.Global, 0, 0, fmt.Sprintf("old=%s new=%s", ...))`
- `internal/api/api.go:541` [UpdateSecuritySettingsJSON] `_ = s.audit(r, q.Node, "security_setting", "mode="+q.Mode, 0, 0, fmt.Sprintf("old=%s new=%s", ...))`
- `internal/api/api.go:601` [RecordLegacyAuth] `_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_auth", ...})`
- `internal/api/mcp_adapter.go:127` [ExecCommandWithOptions] `_ = b.server.auditMCPContext(auditCtx, clientIP, device, "exec", command, res.ExitCode, ...)`
- `internal/api/exec_jobs.go:236` [finish] `_ = s.auditMCPContext(endCtx, j.ClientIP, j.Device, "exec", j.Summary, code, ...)`
- `internal/api/tokens.go:115` [AuditMCP] `_ = s.auditMCPContext(ctx, ip, device, "mcp_denied", command, 0, 0, result+": "+detail)`

**链 2：`deliverEvent` → `Store.RecordWebhookDelivery`（A2，1 处）**

- `internal/notify/notify.go:831` [deliverEvent] `_ = m.Store.RecordWebhookDelivery(t.ID, statusCode, failure)`

**链 3：`TestNotificationChannel` → `Store.RecordWebhookDelivery` / `Store.RecordNotificationChannelTestResult`（A2，3 处）**

- `internal/notify/outbox.go:390` [TestNotificationChannel] `_ = m.Store.RecordNotificationChannelTestResult(channel.ID, "unsupported", 0, failure)`
- `internal/notify/outbox.go:415` [TestNotificationChannel] `_ = m.Store.RecordWebhookDelivery(channel.LegacyWebhookID, status, failure)`
- `internal/notify/outbox.go:417` [TestNotificationChannel] `_ = m.Store.RecordNotificationChannelTestResult(channel.ID, "failed", status, failure)`

### 3.2 A1 结论：失败不无痕（与上一版一致）

`Store.AuditEntry`（`internal/storage/storage.go:503` 起）在三条路径上都留痕：

1. 同步/已关闭路径：`writeAuditEntry` 失败 → `writeAuditFallback(e, err)`，把整条审计以 JSON 行（`type=vps_commander_audit_fallback`，含 `persistence_error`）写到 stderr，并 `auditFallbackCount++`；
2. 异步队列满：先 `log.Printf("WARN audit queue full ...")`，再同步写，失败仍走 fallback；
3. 批量写：`auditWriter.flush()` 重试 `auditWriteAttempts` 次，每次失败 `log.Printf("ERROR audit batch write attempt %d/%d failed ...")`，最终失败逐条走 fallback。

**结论**：A1 的 87 处丢弃返回值可接受，失败在 stderr（journald）可检索。前提是 Hub 以 systemd 运行且 journald 保留 stderr（当前部署满足）。**建议**阶段二不改 A1 代码，只在运维文档写明「审计失败排查：搜 `vps_commander_audit_fallback`」。

### 3.3 A2 结论：失败**完全无痕**（审核意见阻塞 2，本节为重写）

上一版的「A 类失败不无痕」对投递记录**不成立**。核实结果：

- `Store.RecordWebhookDelivery` → `recordWebhookDelivery`（`internal/storage/storage.go:890`–`924`）内部**一行日志都没有**：`func (s *Store) RecordWebhookDelivery` 在 `:880`，`recordWebhookDelivery` 在 `:890`，整个 `890`–`924` 区间 `grep -c "log\."` 为 **0**。失败路径全是 `return err`。
- `Store.RecordNotificationChannelTestResult`（`internal/storage/notification_store.go:532` 起）同样：`notification_store.go` 全文没有任何 `log.` 调用（该文件连 `"log"` 都不 import）。
- 这两个函数的 `return err` 在 A2 的 4 个调用点被 `_ =` 直接吞掉，没有 `log.Printf`、没有 fallback、没有审计条目。

**后果**：`notification_channels`（`last_status`/`last_http_status`/`last_success_at`/`consecutive_failures`/`last_error`）与 `webhook_delivery_state` 两张投递状态表可能与真实投递结果静默不一致 —— 面板显示「上次成功」而实际失败，或反之；且事后无法从日志判断是哪一次、为什么没写进去。

**为什么上一版没发现**：抽样 12 处全在 `internal/api/api.go` 的同一条 `s.audit` 链上，而这条链恰好是**唯一**有 fallback 的链。抽样必须跨调用链才能发现反例。

**建议（阶段二，列入 OPC 决定清单）**：
- 最低要求：4 个 A2 调用点改为 `if err := ...; err != nil { log.Printf("record notification delivery status: %v", err) }`，或在这两个 Store 函数内部记录日志（不改返回契约，行为不变，纯日志）；
- 更强的做法：让 `TestNotificationChannel` 的失败分支也像成功分支那样返回错误（成功分支已经在 `err != nil` 时返回 `"test sent but status could not be recorded"`，失败分支却丢弃 —— 同一函数内不一致）。

## 4. B 类：清理（60 处）——结论：可接受，但 `_ = d.Sync()` 需单列

主要为 `Close()`、`os.Remove()`、`tx.Rollback()`、`Process.Kill()`/`Wait()`、`io.Copy(io.Discard, ...)` 等清理动作。失败时无补救动作，且清理失败不改变用户可见结果。少数 `os.Remove` 删除过期文件失败会残留文件，由后台清扫与下次启动兜底（T-20261016-01 溢出存储设计）。**建议**：保持不变。

**例外（审核非阻塞建议，本版单列）**：`_ = d.Sync()` 共 2 处 —— `internal/executor/atomic_write.go:97`、`internal/executor/edit.go:110`，都在 `os.Rename` 成功之后：

```go
if d, err := os.Open(dir); err == nil {
    _ = d.Sync()   // 目录 fsync
    _ = d.Close()
}
```

`tmp.Sync()`（文件数据）的错误是被检查的（`atomic_write.go:85`、`edit.go:99` 都是 `if err := tmp.Sync(); err != nil { return err }`），但**目录 fsync 的错误被丢弃**。目录 fsync 失败意味着 rename 这条目录项可能没落到磁盘：进程/机器崩溃后，文件内容在但名字可能丢（表现为「写成功但文件不存在」或旧文件仍在）。这不是「清理失败」，而是**持久化承诺的缺口**，建议阶段二至少 `log.Printf` 一行（不改返回行为），或在返回结果里加一个 `durability_warning` 字段。

## 5. C 类：需要判断的 106 处

按影响分组，优先级：P1 = 安全或误路由；P2 = 会掩盖真实故障；P3 = 无用户影响；P4 = 本工具链下不可达（保留现状）。

### 5.1 P1-1 MCP 参数解析失败被静默吞掉，可能误路由（`internal/mcp/mcp.go` 等 15 处）

`callTool` 与 `handleRequest` 中大量 `_ = json.Unmarshal(params.Arguments, &args)`（15 处：`mcp.go:1279`、`1357`、`1460`、`1485`、`1504`、`1516`、`1528`、`1545`、`1560`、`1572`、`1588`、`1602`、`1628`、`1635`、`1905`）。参数 JSON 格式错误时结构体保持零值，随后工具按默认值执行。例如 `device` 传成数字时 `device` 变空字符串，`normalizeDevice` 回落到 Hub 本机，**一条本该发往远端节点的 exec 可能在本机执行**。

**建议**（阶段二）：解析失败返回 JSON-RPC `invalid params`；对 `device` 等关键字段做类型校验。需要 OPC 决定：这会改变对错误参数的响应（从「执行」变为「报错」）。

### 5.2 P1-2 设备权限在数据解析失败时失败开放（真正的 fail-open 只有 1 处）

- **真正的 fail-open**：`internal/storage/tokens.go:282` [ValidateAPIToken] `_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)`。解析失败时 `devices` 为 nil，`Principal.AllowsDevice`（`internal/auth/auth.go:47`：`return len(p.Devices) == 0 || p.Devices[device]`）对空 map 返回 **true（允许所有设备）**。也就是说设备列表损坏的令牌会变成可访问任意节点。触发条件是数据损坏或手工改库，但失败方向是错误的。
- **无权限影响的展示用解析（与上一条拆开）**：`internal/api/tokens.go:338` [TokenListJSON] `_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)`。这里只影响令牌列表页面显示的白名单列，解析失败显示为空列表；**校验路径走的是 `storage.ValidateAPIToken`，不经过此处**，因此没有 fail-open。上一版把两者并列在 P1-2 标题里，容易误读为「两处都是权限漏洞」。

**建议**（阶段二）：`storage/tokens.go:282` 解析失败时拒绝该令牌（返回无效令牌）；需要 OPC 决定（损坏的令牌会立即失效）。`internal/api/tokens.go:338` 只需 `log.Printf` 一行。

### 5.3 P1-3 OAuth 授权码在 PKCE 失败时的消费结果被丢弃（`internal/mcp/oauth_flow.go:559`）

PKCE 校验失败后 `_, _ = s.OAuthStore.ConsumeOAuthCode(hash, client.ID, redirect, now)` 以「烧掉」授权码，防止反复猜测。如果写库失败（例如数据库忙），授权码不会被标记已使用，攻击者在失败后仍可尝试其他 verifier。
**建议**（阶段二）：至少记录日志；更稳妥的是 PKCE 失败时仍返回 `invalid_grant`，并在写失败时记 `ERROR`。需要 OPC 决定是否接受这个风险。

### 5.4 P2 会掩盖真实故障（33 处）——含审核补充的 cluster/agent 指令丢弃

#### 5.4.1 `internal/cluster/manager.go` 的 3 处下行指令丢弃（审核意见阻塞 4，本版新增）

| 位置 | 所在函数 | 原文 | 失败场景 | 影响用户可见结果？ | 掩盖真实故障？ |
|---|---|---|---|---|---|
| `internal/cluster/manager.go:206` | ServeWS | `_ = n.write(Message{Event: "registered", Device: name, TS: ...})` | 新连接建立后立即回写「已注册」通知，写失败（写队列已停、连接刚断、10s 写超时） | 否。`cmd/agent/main.go:251` 收到 `registered` 只是 `continue`，无副作用 | **是**。Hub 侧不留任何痕迹；agent 侧也不会有异常，双方都认为「注册流程正常结束」。若随后 `syncMCP` 也失败（见下），agent 会带着旧 MCP 配置长期在线 |
| `internal/cluster/manager.go:510` | RevokeDevice | `_ = n.write(Message{Event: "revoked", Error: "revoked by admin"})` | 吊销时向节点下发撤销指令失败 | **是**。`cmd/agent/main.go:264` 收到 `revoked` 会 `log.Printf` 并 `return errors.New("revoked by admin")` 退出进程；写失败则 agent **继续在线跑**，直到下一次 `IsRevoked` 检查（`manager.go:170`，仅在**重连握手**时执行）才被 403 拦下 | **是**。管理面板显示「已吊销」（`internal/api/api.go:1112` 返回 `{"ok":true,"revoked":name}`），DB 里也写了 `revoked_devices`，但节点实际未断；`RevokeDevice` 本身返回 `nil`（`m.Store.RevokeDevice` 成功即返回 nil），上层看到的是完全成功 |
| `internal/cluster/manager.go:789` | syncMCP | `_ = n.write(Message{Event: "mcp_sync", Payload: services, TS: ...})` | 保存/删除 MCP 服务后向节点同步配置失败 | **是**。`internal/api/api.go:858`、`:881` 保存成功后调 `SyncMCPServices()`，然后返回 200；agent 未收到 `mcp_sync` 则继续用旧配置，`call_agent_mcp` 会打到已删除的 server 或漏掉新 server | **是**。`syncMCP` 在 `ListMCPServicesForNode` 出错时也是 `return`（无日志），写失败再 `_ =` 吞掉，整条链 0 条日志；用户看到「保存成功」 |

**建议（阶段二）**：
- 三处至少 `log.Printf("cluster: %s -> device %s: %v", event, n.Name, err)`；
- `:510` 建议进一步：写失败时 `RevokeDevice` 返回 error（DB 已吊销但节点未断，属于部分成功，应让上层 500 或返回带警告的 200）。需要 OPC 决定（改变 API 返回）。
- `:789` 建议：`SyncMCPServices` 收集失败节点，API 在响应里带 `unsynced_devices` 字段。

#### 5.4.2 `cmd/agent/main.go` 同族 3 处（审核意见阻塞 4，本版新增）

| 位置 | 所在函数 | 原文 | 失败场景 | 影响 | 掩盖真实故障？ |
|---|---|---|---|---|---|
| `cmd/agent/main.go:203` | run（心跳 goroutine） | `_ = writeJSON(cluster.Message{Event: "ping", ...})` | 15s 心跳写失败 | 否（Hub 靠 75s 读超时判离线） | **是**。心跳写失败后 goroutine 继续每 15s 试一次，不退出、不记日志；若连接已半死，agent 会静默持续到 Hub 侧超时。写失败本应让 `run` 返回以触发重连（`main` 的 backoff 循环） |
| `cmd/agent/main.go:281` | run（请求分发） | `_ = sendError(writeJSON, msg.ID, err)` | `handle` 返回错误后回送错误帧失败 | 否（Hub 侧有 `Pending` 超时兜底） | **部分**。上一行 `log.Printf("request %s failed: %v")` 已记下原始错误，但「错误帧没送达」这件事无痕 |
| `cmd/agent/main.go:381` | handle（call_agent_mcp 失败分支） | `_ = writeJSON(cluster.Message{ID: msg.ID, Status: "error", ...})` | MCP 调用失败后回送错误结果失败 | 是（调用方拿不到错误，只能等超时） | **是**。这个分支**没有任何日志**（与 `:281` 不同），MCP 调用失败 + 回送失败 = Hub 侧只看到超时，不知道根因 |

**建议**：`:203` 写失败应 `log.Printf` 并 `return err`（让外层重连）；`:281`、`:381` 至少 `log.Printf`。均不改协议行为。

#### 5.4.3 其余 P2（27 处）

- **设备状态写库被丢**：`internal/cluster/manager.go:110`（NewManager 启动时把历史设备置 offline）、`:327`（readLoop 断连时置 offline）、`:376`/`:382`（UpsertRemote 上线）。失败时面板显示的设备状态与实际不一致，但不影响权限。建议 `log.Printf`，不改行为。
- **吊销结果在无 Cluster 路径被丢**：`internal/api/api.go:1109` [DeleteDevice] `_ = s.Store.RevokeDevice(...)`。有 Cluster 时错误已检查（`:1103`–`:1105`，失败返回 500），无 Cluster 分支丢弃，界面仍显示「已吊销」。建议与 Cluster 分支一致返回 500（改变行为，需 OPC 决定）。
- **解析失败静默为空**：`internal/cluster/manager.go:794/795/796`（`decodeStoredMCPService` 的 Args/Env/Headers）、`internal/api/mcp_adapter.go:48`（ListDevices 序列化回环）、`internal/mcp/oauth_flow.go:660`（`decodeScopes`）、`internal/storage/tokens.go:276`（scopes 解析，失败则该令牌**无任何 scope**，是失败关闭，方向正确但要记日志）、`internal/storage/notification_policy.go:137`（`fmt.Sscanf` 解析静音时段，失败得 0 点，静音时段被错误设置）、`internal/storage/notification_store.go:244/256/268`（凭据字段解析失败 → 凭据为空 → 后续校验返回明确错误，方向正确）。
- **审计意图/请求体解析**：`internal/mcp/mcp.go:224`（AuditIntent）、`internal/api/tokens.go:166/170`（requestAuditIntent）。解析失败时审计记录的 `command`/`target` 字段为空，审计条目仍在（不是丢条目），建议记日志。
- **一次性配置导入**：`internal/notify/notify.go:210`（`SaveWebhookTarget` 导入 openclaw webhook，失败则静默不生效）。
- **通知通道配置解析**：`internal/notify/notify.go:781`、`internal/notify/outbox.go:142/163`。解析失败 → 目标/载荷为空 → 投递失败，但**失败结果本身也走 A2 的静默路径**（3.3），叠加后整条链无痕。
- **元数据更新**：`internal/storage/oauth.go:651/652`、`internal/storage/tokens.go:287`（`TouchAPIToken`/`MarkOAuthGrantUsed`，只更新「最后使用时间」，失败无用户影响，记日志即可）。
- **MCP 参数解析（非路由关键路径）**：`internal/api/token_limiter.go:239/240`（`rateLimitIntent` 里解析 `device` 用于限流分组，失败退化为空设备名，限流仍然生效，不会绕过）。

### 5.5 P4 本工具链下不可达（2 处，审核意见阻塞 5 已降级）

- `internal/mcp/mcp.go:704` [newSessionID] `_, _ = rand.Read(b)`
- `internal/api/exec_jobs.go:154` [newJobID] `_, _ = rand.Read(b[:])`

上一版列为 P1-4，称「`crypto/rand.Read` 失败 → ID 全零，可被猜测或冲突」。**该风险在本仓库工具链下不可达**：

- `go.mod` 固定 `go 1.24.0`（`toolchain go1.24.4`，实测 `go version go1.24.4 linux/amd64`）。
- Go 1.24 起 `crypto/rand.Read` 的文档明确（`$GOROOT/src/crypto/rand/rand.go:60-65`）：「Read fills b with cryptographically secure random bytes. **It never returns an error, and always fills b entirely.** … calls io.ReadFull on Reader and **crashes the program irrecoverably** if an error is returned.」
- 因此 `_, _ = rand.Read(b)` 的第二个返回值恒为 nil，`b` 恒被填满；真出错时进程直接崩溃，不会走到「ID 全零」。

**结论**：**从 P1 优先级与 OPC 决策清单中移除**，降级为「可保留现状 / 一致性整理」：若要统一风格，可改为 `rand.Read(b)` 或 `_ = ...`，或按 `internal/api/spill.go:104` 的写法显式检查（那里已经 `if _, err := rand.Read(rnd[:]); err != nil { return "", err }`，在 Go 1.24 下该分支同样不可达，属防御性写法）。阶段二无必须动作。

### 5.6 P3 无用户影响（54 处）

- **写 HTTP 响应体**（`w.Write`、`json.NewEncoder(w).Encode`、CSV `c.Write`、`template.Execute`）：`internal/api`、`internal/mcp`、`internal/web`、`internal/auth` 共 33 处（可用 1.3 脚本 `awk -F'\t' '$2=="P3"' | grep -cE 'w\.Write|Encode\(|c\.Write|\.Execute\('` 复现）。响应头已发出，错误无法再报告给客户端。建议保持不变。
- **`cmd/loadtest/*`**（2 处 C：`:494` SetReadDeadline、`:573` waitContext）：压测工具，忽略可接受。
- **占位写法**（2 处）：`internal/executor/fs.go:294` `var _ = fmt.Sprintf`、`:295` `var _ = exec.Command`，只为避免未使用导入的编译错误，不是忽略错误。建议阶段二删除这两行与对应导入。同类：`internal/mcp/mcp.go:257` `_ = oldText`（消除未用变量），本版归 P3，与上一版把它算作 A 类审计写入的做法不同。
- **`internal/executor/executor.go:36`** `_, _ = b.buf.Write(p[:n])`：写 `bytes.Buffer` 永不失败。
- **`cmd/hub/main.go:220-223`**（4 处，审核指出上一版正文写 222-223 而附录是 220-223，**本版统一为 220-223**）：`PRAGMA journal_mode`/`synchronous`/`busy_timeout`/`wal_autocheckpoint` 诊断读取，失败仅影响启动日志。
- 其余 11 处：`SetReadDeadline`（`cmd/agent/main.go:250`、`internal/cluster/manager.go:335/343`）、`json.Marshal` map/结构体（`notify.go:782/785`）、`json.Unmarshal` 到本地 map 的序列化回环（`internal/api/api.go:1066`、`health_assertions.go:92`）、令牌列表展示解析（`internal/api/tokens.go:335/338`）、`internal/api/session_stats.go:127` `_, _ = dst.Write(body)`（写 `bytes.Buffer`）、`internal/executor/atomic_write.go:52` `owner, _ = ti.Sys().(*syscall.Stat_t)`（类型断言，失败时 `owner` 为 nil，仅用于「属主是否需要 chown」判断，不影响写入结果）。

计数核对：33（写响应体）+ 2（loadtest）+ 2（`fs.go` 占位）+ 1（`mcp.go:257` 消未用变量）+ 1（`executor.go:36`）+ 4（`cmd/hub` 诊断）+ 11（其余）= 54，与 2 节 P3=54 一致。逐条复现：`sh classify.sh | awk -F'\t' '$2=="P3"' | wc -l` → 54。

## 6. 阶段二建议（需要 OPC 决定，不在本卡内执行）

### 6.1 需要 OPC 决定的行为变更
1. **P1-1**（15 处）：参数解析失败改为返回 `invalid params`（错误参数从「执行」变「报错」）。
2. **P1-2**（`storage/tokens.go:282`）：设备列表损坏时拒绝令牌（损坏令牌立即失效）。
3. **P1-3**（`oauth_flow.go:559`）：PKCE 失败时消费授权码写失败是否接受现有风险。
4. **P2 吊销**：`internal/cluster/manager.go:510` 写失败、`internal/api/api.go:1109` 无 Cluster 分支——是否改为返回 500（部分成功不再报成功）。
5. **A2 通知投递记录**：是否只加日志（行为不变），还是让 `TestNotificationChannel` 失败分支也返回错误。

### 6.2 纯日志、不改行为（可单独成卡）
- A2 的 4 处、`cluster/manager.go:206/510/789`、`cmd/agent/main.go:203/281/381`、P2 其余 27 处。
- `_ = d.Sync()`（`atomic_write.go:97`、`edit.go:110`）的目录 fsync 失败日志。
- 运维文档补「审计失败排查：搜 `vps_commander_audit_fallback`」。

### 6.3 保持不变
- A1 的 87 处（已有 fallback）、B 类 60 处（除 `d.Sync()` 的日志建议）、P3 的 54 处、P4 的 2 处。

### 6.4 口径扩展（审核非阻塞建议）
本卡 grep 口径只覆盖 `_ = ` / `_, _ = `，**不含 `, _ :=` 形式**的丢弃。实测非测试代码中 `, _ := ` 有 **98 行**：

```
git grep -n ", _ := " -- '*.go' | grep -v '_test\.go:' | wc -l   # → 98
```

分布（前几）：`internal/cluster/manager.go` 14、`internal/api/api.go` 14、`internal/api/tokens.go` 12、`internal/mcp/mcp.go` 9、`internal/sysinfo/sysinfo.go` 4、`internal/storage/oauth.go` 4、`internal/api/exec_jobs.go` 4、`cmd/mcp-stdio/main.go` 4。这些同样是「忽略错误」，其中不乏 `p, _ := auth.PrincipalFromContext(ctx)` 这类影响权限判断的解析（`internal/api/tokens.go:58`、`:125`）。建议阶段二把口径扩展为 `_ = |_, _ = |, _ :=`，并在新卡里重新分类。

## 7. 附录 A：A 类全量清单（91 条）

生成命令见 1.3（`sh classify.sh | awk -F'\t' '$1=="A"'`）。位置 / 所在函数 / 原文：

| # | 位置 | 所在函数 | 原文（截断） |
|---:|---|---|---|
| 1 | `internal/api/api.go:1111` | DeleteDevice | `_ = s.audit(r, name, "revoke_device", "revoked by admin", 0, 0, "")` |
| 2 | `internal/api/api.go:1391` | listDirectory | `_ = s.audit(r, "", "file_list", "", 405, 0, "method not allowed")` |
| 3 | `internal/api/api.go:1401` | listDirectory | `_ = s.audit(r, "", "file_list", "", 400, 0, "bad json")` |
| 4 | `internal/api/api.go:1426` | listDirectory | `_ = s.audit(r, req.Device, "file_list", req.Path, 409, 0, err.Error())` |
| 5 | `internal/api/api.go:1430` | listDirectory | `_ = s.audit(r, req.Device, "file_list", req.Path, 0, 0, "")` |
| 6 | `internal/api/api.go:1435` | fileInfo | `_ = s.audit(r, "", "file_info", "", 405, 0, "method not allowed")` |
| 7 | `internal/api/api.go:1441` | fileInfo | `_ = s.audit(r, "", "file_info", "", 400, 0, "bad json")` |
| 8 | `internal/api/api.go:1466` | fileInfo | `_ = s.audit(r, req.Device, "file_info", req.Path, 404, 0, err.Error())` |
| 9 | `internal/api/api.go:1470` | fileInfo | `_ = s.audit(r, req.Device, "file_info", req.Path, 0, 0, "")` |
| 10 | `internal/api/api.go:1475` | searchFiles | `_ = s.audit(r, "", "file_search", "", 405, 0, "method not allowed")` |
| 11 | `internal/api/api.go:1484` | searchFiles | `_ = s.audit(r, "", "file_search", "", 400, 0, "bad json")` |
| 12 | `internal/api/api.go:1511` | searchFiles | `_ = s.audit(r, req.Device, "file_search", req.Root, 409, 0, err.Error())` |
| 13 | `internal/api/api.go:1515` | searchFiles | `_ = s.audit(r, req.Device, "file_search", req.Root, 0, 0, "")` |
| 14 | `internal/api/api.go:173` | requireDeviceAccess | `_ = s.audit(r, device, "auth_denied", "", http.StatusUnauthorized, 0, "missing principal")` |
| 15 | `internal/api/api.go:179` | requireDeviceAccess | `_ = s.audit(r, device, "device_denied", "", 0, 0, "token device whitelist denied")` |
| 16 | `internal/api/api.go:201` | requireSecurity | `_ = s.audit(r, device, "security_denied", action, http.StatusForbidden, 0, reason)` |
| 17 | `internal/api/api.go:276` | UpdateNotificationSettingsJSON | `_ = s.Store.AuditEntry(storage.AuditEntry{Action: "notification_setting", ActorType: "panel", ActorID: "panel", ActorNam…` |
| 18 | `internal/api/api.go:303` | legacyWebhookGone | `_ = s.audit(r, "", "notification_setting_legacy_gone", r.URL.Path, http.StatusGone, 0, "legacy webhook API removed; use …` |
| 19 | `internal/api/api.go:369` | UpdateLegacyKeyStatusJSON | `_ = s.Store.AuditEntry(storage.AuditEntry{Action: "legacy_key_setting", ActorType: "panel", ActorID: "panel", ActorName:…` |
| 20 | `internal/api/api.go:410` | RecordLegacyRejected | `_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_rejected", Command: r.UserAgent(), ErrorMsg: "le…` |
| 21 | `internal/api/api.go:493` | ProvisionDeviceCredentialJSON | `_ = s.audit(r, q.Device, "device_credential_provision", "device="+q.Device, 0, 0, "credential provisioned")` |
| 22 | `internal/api/api.go:538` | UpdateSecuritySettingsJSON | `_ = s.audit(r, "", "security_setting", "global="+q.Global, 0, 0, fmt.Sprintf("old=%s new=%s version=%d", oldGlobal, newG…` |
| 23 | `internal/api/api.go:541` | UpdateSecuritySettingsJSON | `_ = s.audit(r, q.Node, "security_setting", "mode="+q.Mode, 0, 0, fmt.Sprintf("old=%s new=%s version=%d", oldNode, s.GetE…` |
| 24 | `internal/api/api.go:601` | RecordLegacyAuth | `_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_auth", Command: r.UserAgent(), ActorType: "api_t…` |
| 25 | `internal/api/api.go:810` | TestMCPJSON | `_ = s.audit(r, q.Device, "mcp_test", service.Name, 1, ms, err.Error())` |
| 26 | `internal/api/api.go:814` | TestMCPJSON | `_ = s.audit(r, q.Device, "mcp_test", service.Name, 0, ms, "")` |
| 27 | `internal/api/api.go:860` | MCPServicesJSON | `_ = s.audit(r, s.LocalName, "mcp_service_save", q.ID, 0, 0, "")` |
| 28 | `internal/api/api.go:883` | DeleteMCPService | `_ = s.audit(r, s.LocalName, "mcp_service_delete", id, 0, 0, "")` |
| 29 | `internal/api/api.go:939` | CallAgentMCPJSON | `_ = s.audit(r, q.Device, "mcp_call", q.Server+"/"+q.Tool, 1, 0, "device offline")` |
| 30 | `internal/api/api.go:952` | CallAgentMCPJSON | `_ = s.audit(r, q.Device, "mcp_call", q.Server+"/"+q.Tool, 1, ms, err.Error())` |
| 31 | `internal/api/api.go:956` | CallAgentMCPJSON | `_ = s.audit(r, q.Device, "mcp_call", q.Server+"/"+q.Tool, 0, ms, "")` |
| 32 | `internal/api/exec_jobs.go:236` | finish | `_ = s.auditMCPContext(endCtx, j.ClientIP, j.Device, "exec", j.Summary, code, res.DurationMS, msg)` |
| 33 | `internal/api/exec_jobs.go:349` | StartExec | `_ = b.server.auditMCPContext(ctx, clientIP, device, "exec_start", j.Summary, 0, 0, "job="+j.ID)` |
| 34 | `internal/api/exec_jobs.go:388` | CancelExec | `_ = b.server.auditMCPContext(cancelCtx, clientIP, ref.Device, "exec_cancel", ref.Summary, 1, 0, "job="+ref.ID+" "+err.Er…` |
| 35 | `internal/api/exec_jobs.go:393` | CancelExec | `_ = b.server.auditMCPContext(ctx, clientIP, view.Device, "exec_cancel", view.Summary, 0, 0, "job="+view.ID)` |
| 36 | `internal/api/exec_jobs.go:402` | CancelExec | `_ = b.server.auditMCPContext(ctx, clientIP, j.Device, "exec_cancel", j.Summary, 0, 0, "job="+j.ID)` |
| 37 | `internal/api/exec_jobs_remote.go:193` | settleRemote | `_ = s.auditMCPContext(endCtx, j.ClientIP, j.Device, "exec", j.Summary, code, res.DurationMS, msg)` |
| 38 | `internal/api/mcp_adapter.go:127` | ExecCommandWithOptions | `_ = b.server.auditMCPContext(auditCtx, clientIP, device, "exec", command, res.ExitCode, res.DurationMS, msg)` |
| 39 | `internal/api/mcp_adapter.go:170` | DiagnosticCommand | `_ = b.server.auditMCPContext(ctx, clientIP, device, "diagnostic_command", "action="+req.Action+" target="+req.Target, ex…` |
| 40 | `internal/api/mcp_adapter.go:236` | ReadFile | `_ = b.server.auditMCPContext(ctx, clientIP, device, "file_read", path, 0, 0, "")` |
| 41 | `internal/api/mcp_adapter.go:273` | EditBlock | `_ = b.server.auditMCPContext(ctx, clientIP, device, "edit_block", path+fmt.Sprintf(" (bytes=%d)", len(newText)), 0, 0, f…` |
| 42 | `internal/api/mcp_adapter.go:280` | EditBlock | `_ = b.server.auditMCPContext(ctx, clientIP, device, "edit_block", path+fmt.Sprintf(" (bytes=%d)", len(newText)), 0, 0, f…` |
| 43 | `internal/api/mcp_adapter.go:317` | WriteFile | `_ = b.server.auditMCPContext(ctx, clientIP, device, "file_write", path+fmt.Sprintf(" (bytes=%d)", len(content)), 0, 0, "…` |
| 44 | `internal/api/mcp_adapter.go:35` | authorize | `_ = b.server.auditMCPContext(ctx, clientIP, device, "security_denied", action, 0, 0, "MCP security gate")` |
| 45 | `internal/api/mcp_adapter.go:354` | CallAgentMCP | `_ = b.server.auditMCPContext(ctx, clientIP, device, "mcp_call", server+"/"+tool, 1, ms, err.Error())` |
| 46 | `internal/api/mcp_adapter.go:356` | CallAgentMCP | `_ = b.server.auditMCPContext(ctx, clientIP, device, "mcp_call", server+"/"+tool, 0, ms, "")` |
| 47 | `internal/api/mcp_adapter.go:372` | AuditMCPAction | `_ = b.server.auditMCPContext(ctx, clientIP, device, action, command, code, durationMS, detail)` |
| 48 | `internal/api/mcp_adapter.go:405` | ReadMultipleFiles | `_ = b.server.auditMCPContext(ctx, clientIP, device, "file_read", strings.Join(paths, ", "), 0, 0, "")` |
| 49 | `internal/api/mcp_adapter.go:430` | CreateDirectory | `_ = b.server.auditMCPContext(ctx, clientIP, device, "create_directory", path, 0, 0, "")` |
| 50 | `internal/api/mcp_adapter.go:455` | MoveFile | `_ = b.server.auditMCPContext(ctx, clientIP, device, "move_file", source+" -> "+destination, 0, 0, "")` |
| 51 | `internal/api/mcp_adapter.go:478` | ListProcesses | `_ = b.server.auditMCPContext(ctx, clientIP, device, "list_processes", "list_processes", 0, 0, "")` |
| 52 | `internal/api/mcp_adapter.go:77` | ExecCommandWithOptions | `_ = b.server.auditMCPContext(ctx, clientIP, device, "exec", command, code, 0, "unchanged=true")` |
| 53 | `internal/api/notification_deliveries.go:77` | NotificationDeliveryRetryV1JSON | `_ = s.audit(r, "", "notification_delivery_retry", q.ID, http.StatusBadRequest, 0, "manual retry rejected")` |
| 54 | `internal/api/notification_deliveries.go:81` | NotificationDeliveryRetryV1JSON | `_ = s.audit(r, "", "notification_delivery_retry", q.ID, 0, 0, "manual retry queued")` |
| 55 | `internal/api/notification_events.go:197` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_idempotency_conflict", q.TemplateID, http.StatusConflict, 0, "idempotency key reu…` |
| 56 | `internal/api/notification_events.go:206` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_emit", prior.TemplateID, http.StatusOK, 0, "idempotent replay")` |
| 57 | `internal/api/notification_events.go:283` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_p1_denied", q.TemplateID, http.StatusForbidden, 0, "P1 emission requires admin sc…` |
| 58 | `internal/api/notification_events.go:288` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_escalation_denied", q.TemplateID, http.StatusForbidden, 0, "severity escalation r…` |
| 59 | `internal/api/notification_events.go:334` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_idempotency_conflict", q.TemplateID, http.StatusConflict, 0, "idempotency key reu…` |
| 60 | `internal/api/notification_events.go:355` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_emit_failed", q.TemplateID, http.StatusInternalServerError, 0, "event persistence…` |
| 61 | `internal/api/notification_events.go:360` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_emit_failed", q.TemplateID, http.StatusInternalServerError, 0, "event persistence…` |
| 62 | `internal/api/notification_events.go:369` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_emit", q.TemplateID, statusCode, 0, "severity="+severity+" matched_tasks="+strcon…` |
| 63 | `internal/api/notification_events.go:375` | rejectNotificationEvent | `_ = s.audit(r, "", "notification_event_emit_rejected", target, status, 0, message)` |
| 64 | `internal/api/notification_events.go:98` | EmitNotificationEventJSON | `_ = s.audit(r, "", "notification_event_rate_limited", actorID, http.StatusTooManyRequests, 0, "event emission rate limit…` |
| 65 | `internal/api/notification_legacy_retire.go:54` | RetireLegacyWebhookJSON | `_ = s.audit(r, "", "notification_legacy_webhook_retire", q.LegacyWebhookID, http.StatusOK, 0, "")` |
| 66 | `internal/api/notifications_v1.go:128` | NotificationChannelsV1JSON | `_ = s.audit(r, "", "notification_channel", stored.ID, 0, 0, "")` |
| 67 | `internal/api/notifications_v1.go:140` | NotificationChannelsV1JSON | `_ = s.audit(r, "", "notification_channel_disable", id, 0, 0, "")` |
| 68 | `internal/api/notifications_v1.go:168` | NotificationTemplatesV1JSON | `_ = s.audit(r, "", "notification_template", v.ID, 0, 0, "")` |
| 69 | `internal/api/notifications_v1.go:180` | NotificationTemplatesV1JSON | `_ = s.audit(r, "", "notification_template_disable", id, 0, 0, "")` |
| 70 | `internal/api/notifications_v1.go:208` | NotificationTasksV1JSON | `_ = s.audit(r, "", "notification_task", v.ID, 0, 0, "")` |
| 71 | `internal/api/notifications_v1.go:220` | NotificationTasksV1JSON | `_ = s.audit(r, "", "notification_task_disable", id, 0, 0, "")` |
| 72 | `internal/api/notifications_v1.go:251` | NotificationChannelTestJSON | `_ = s.audit(r, "", "notification_channel_test", q.ID, http.StatusBadGateway, 0, "test send failed")` |
| 73 | `internal/api/notifications_v1.go:255` | NotificationChannelTestJSON | `_ = s.audit(r, "", "notification_channel_test", q.ID, http.StatusOK, 0, "test send succeeded")` |
| 74 | `internal/api/oauth.go:124` | OAuthGrantRevokeJSON | `_ = s.audit(r, "", "oauth_grant_revoke", id, 0, 0, "")` |
| 75 | `internal/api/oauth.go:63` | OAuthClientsJSON | `_ = s.audit(r, "", "oauth_client_create", clientID, 0, 0, "")` |
| 76 | `internal/api/oauth.go:90` | OAuthClientDisableJSON | `_ = s.audit(r, "", "oauth_client_disable", id, 0, 0, "")` |
| 77 | `internal/api/spill.go:344` | readSpill | `_ = b.server.auditMCPContext(ctx, clientIP, mcp.SpillDeviceName, "file_read", path, 0, 0, "")` |
| 78 | `internal/api/spill.go:375` | serveSpillRead | `_ = s.auditMCPContext(r.Context(), auth.ClientIP(r), mcp.SpillDeviceName, "file_read", path, 0, 0, "")` |
| 79 | `internal/api/token_limiter.go:207` | auditRateLimitedEntry | `_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: clientIP, Device: device, Action: "rate_limited", Command: command, …` |
| 80 | `internal/api/tokens.go:115` | AuditMCP | `_ = s.auditMCPContext(ctx, ip, device, "mcp_denied", command, 0, 0, result+": "+detail)` |
| 81 | `internal/api/tokens.go:219` | requireScope | `_ = s.auditContextTarget(r.Context(), auth.ClientIP(r), device, "scope_denied", target, command, http.StatusForbidden, 0…` |
| 82 | `internal/api/tokens.go:440` | TokenCreateJSON | `_ = s.audit(r, "", "token_create", name, 0, 0, "")` |
| 83 | `internal/api/tokens.go:522` | TokenPatchJSON | `_ = s.audit(r, "", "token_update", name, 0, 0, "")` |
| 84 | `internal/api/tokens.go:557` | TokenRotateJSON | `_ = s.audit(r, "", "token_rotate", old.Name, 0, 0, "")` |
| 85 | `internal/api/tokens.go:574` | TokenRevokeJSON | `_ = s.audit(r, "", "token_revoke", id, 0, 0, "")` |
| 86 | `internal/api/tokens.go:588` | TokenDeleteJSON | `_ = s.audit(r, "", "token_delete", id, 0, 0, "")` |
| 87 | `internal/notify/notify.go:839` | deliverEvent | `_ = m.Store.AuditEntry(storage.AuditEntry{CallerIP: "127.0.0.1", Device: e.Device, Action: "notification", Command: "web…` |
| 88 | `internal/notify/notify.go:831` | deliverEvent | `_ = m.Store.RecordWebhookDelivery(t.ID, statusCode, failure)` |
| 89 | `internal/notify/outbox.go:390` | TestNotificationChannel | `_ = m.Store.RecordNotificationChannelTestResult(channel.ID, "unsupported", 0, failure)` |
| 90 | `internal/notify/outbox.go:415` | TestNotificationChannel | `_ = m.Store.RecordWebhookDelivery(channel.LegacyWebhookID, status, failure)` |
| 91 | `internal/notify/outbox.go:417` | TestNotificationChannel | `_ = m.Store.RecordNotificationChannelTestResult(channel.ID, "failed", status, failure)` |



## 8. 附录 B：B 类全量清单（60 条）

生成命令见 1.3（`sh classify.sh | awk -F'\t' '$1=="B"'`）。

| # | 位置 | 所在函数 | 原文（截断） |
|---:|---|---|---|
| 1 | `cmd/agent/main.go:165` | run | `_ = resp.Body.Close()` |
| 2 | `cmd/loadtest/main.go:1099` | devicesLatency | `_ = resp.Body.Close()` |
| 3 | `cmd/loadtest/main.go:422` | runAgent | `_ = response.Body.Close()` |
| 4 | `cmd/loadtest/main.go:440` | runAgent | `_ = conn.Close()` |
| 5 | `cmd/loadtest/main.go:469` | runAgentConnection | `_ = conn.Close()` |
| 6 | `cmd/loadtest/main.go:483` | runAgentConnection | `_ = conn.Close()` |
| 7 | `cmd/loadtest/main.go:663` | deleteStreamableSession | `_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))` |
| 8 | `cmd/loadtest/main.go:664` | deleteStreamableSession | `_ = resp.Body.Close()` |
| 9 | `cmd/loadtest/main.go:698` | streamableRPC | `_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))` |
| 10 | `cmd/loadtest/main.go:817` | openSSE | `_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))` |
| 11 | `cmd/loadtest/main.go:818` | openSSE | `_ = response.Body.Close()` |
| 12 | `cmd/loadtest/main.go:829` | openSSE | `_ = response.Body.Close()` |
| 13 | `cmd/loadtest/main.go:833` | openSSE | `_ = response.Body.Close()` |
| 14 | `cmd/loadtest/main.go:838` | openSSE | `_ = response.Body.Close()` |
| 15 | `cmd/loadtest/main.go:847` | openSSE | `_ = response.Body.Close()` |
| 16 | `cmd/loadtest/main.go:911` | call | `_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))` |
| 17 | `cmd/loadtest/main.go:912` | call | `_ = response.Body.Close()` |
| 18 | `cmd/loadtest/main.go:923` | call | `_ = response.Body.Close()` |
| 19 | `cmd/loadtest/main.go:941` | close | `c.closeOnce.Do(func() { c.cancel(); _ = c.body.Close() })` |
| 20 | `internal/api/exec_jobs.go:134` | pruneLocked | `_ = os.Remove(filepath.Join(m.dir, id+".json"))` |
| 21 | `internal/api/exec_jobs.go:99` | newExecJobs | `_ = os.Remove(path)` |
| 22 | `internal/api/spill.go:113` | writeAt | `_ = f.Close()` |
| 23 | `internal/api/spill.go:114` | writeAt | `_ = os.Remove(path)` |
| 24 | `internal/api/spill.go:118` | writeAt | `_ = f.Close()` |
| 25 | `internal/api/spill.go:119` | writeAt | `_ = os.Remove(path)` |
| 26 | `internal/api/spill.go:123` | writeAt | `_ = os.Remove(path)` |
| 27 | `internal/api/spill.go:203` | removeLocked | `_ = os.Remove(path)` |
| 28 | `internal/api/spill.go:79` | newSpillStore | `_ = os.Remove(filepath.Join(resolved, e.Name()))` |
| 29 | `internal/cluster/manager.go:204` | ServeWS | `_ = old.Conn.Close()` |
| 30 | `internal/cluster/manager.go:245` | writeLoop | `_ = n.Conn.Close()` |
| 31 | `internal/cluster/manager.go:252` | writeLoop | `_ = n.Conn.Close()` |
| 32 | `internal/cluster/manager.go:311` | readLoop | `_ = conn.Close()` |
| 33 | `internal/cluster/manager.go:442` | Close | `_ = n.Conn.Close()` |
| 34 | `internal/cluster/manager.go:511` | RevokeDevice | `_ = n.Conn.Close()` |
| 35 | `internal/cluster/manager.go:886` | heartbeat | `_ = n.Conn.Close()` |
| 36 | `internal/cluster/mcp.go:139` | probeStdio | `defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()` |
| 37 | `internal/cluster/mcp.go:282` | callStdio | `defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()` |
| 38 | `internal/executor/atomic_write.go:68` | WriteFileAtomic | `_ = os.Remove(tmpName)` |
| 39 | `internal/executor/atomic_write.go:72` | WriteFileAtomic | `_ = tmp.Close()` |
| 40 | `internal/executor/atomic_write.go:77` | WriteFileAtomic | `_ = tmp.Close()` |
| 41 | `internal/executor/atomic_write.go:82` | WriteFileAtomic | `_ = tmp.Close()` |
| 42 | `internal/executor/atomic_write.go:86` | WriteFileAtomic | `_ = tmp.Close()` |
| 43 | `internal/executor/atomic_write.go:97` | WriteFileAtomic | `_ = d.Sync()` |
| 44 | `internal/executor/atomic_write.go:98` | WriteFileAtomic | `_ = d.Close()` |
| 45 | `internal/executor/edit.go:100` | EditBlock | `_ = tmp.Close()` |
| 46 | `internal/executor/edit.go:110` | EditBlock | `_ = d.Sync()` |
| 47 | `internal/executor/edit.go:111` | EditBlock | `_ = d.Close()` |
| 48 | `internal/executor/edit.go:92` | EditBlock | `_ = tmp.Close()` |
| 49 | `internal/executor/edit.go:96` | EditBlock | `_ = tmp.Close()` |
| 50 | `internal/executor/executor.go:69` | Run | `_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)` |
| 51 | `internal/executor/session.go:124` | StartSession | `_ = stdin.Close()` |
| 52 | `internal/executor/session_manager.go:49` | Start | `_ = s.Kill()` |
| 53 | `internal/executor/session_manager.go:98` | Cleanup | `_ = s.Kill()` |
| 54 | `internal/notify/notify.go:803` | deliverEvent | `_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))` |
| 55 | `internal/notify/notify.go:804` | deliverEvent | `_ = resp.Body.Close()` |
| 56 | `internal/notify/outbox.go:281` | postJSON | `_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))` |
| 57 | `internal/storage/oauth.go:308` | GetOAuthCodeForExchange | `_ = tx.Rollback()` |
| 58 | `internal/storage/storage.go:563` | writeAuditBatch | `_ = tx.Rollback()` |
| 59 | `internal/storage/storage.go:572` | writeAuditBatch | `_ = stmt.Close()` |
| 60 | `internal/storage/storage.go:574` | writeAuditBatch | `_ = tx.Rollback()` |



## 9. 附录 C：C 类全量清单（106 条）

生成命令见 1.3（`sh classify.sh | awk -F'\t' '$1=="C"'`）。分组列即第 5 节的优先级分组。

| # | 分组 | 位置 | 所在函数 | 原文（截断） |
|---:|---|---|---|---|
| 1 | P1-1 | `internal/mcp/mcp.go:1279` | handleRequest | `_ = json.Unmarshal(params.Arguments, &args)` |
| 2 | P1-1 | `internal/mcp/mcp.go:1357` | callTool | `_ = json.Unmarshal(params.Arguments, &rawDevice)` |
| 3 | P1-1 | `internal/mcp/mcp.go:1460` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 4 | P1-1 | `internal/mcp/mcp.go:1485` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 5 | P1-1 | `internal/mcp/mcp.go:1504` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 6 | P1-1 | `internal/mcp/mcp.go:1516` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 7 | P1-1 | `internal/mcp/mcp.go:1528` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 8 | P1-1 | `internal/mcp/mcp.go:1545` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 9 | P1-1 | `internal/mcp/mcp.go:1560` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 10 | P1-1 | `internal/mcp/mcp.go:1572` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 11 | P1-1 | `internal/mcp/mcp.go:1588` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 12 | P1-1 | `internal/mcp/mcp.go:1602` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 13 | P1-1 | `internal/mcp/mcp.go:1628` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 14 | P1-1 | `internal/mcp/mcp.go:1635` | callTool | `_ = json.Unmarshal(args.Arguments, &av)` |
| 15 | P1-1 | `internal/mcp/mcp.go:1905` | callAsyncExec | `_ = json.Unmarshal(params.Arguments, &args)` |
| 16 | P1-2 | `internal/storage/tokens.go:282` | ValidateAPIToken | `_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)` |
| 17 | P1-3 | `internal/mcp/oauth_flow.go:559` | HandleToken | `_, _ = s.OAuthStore.ConsumeOAuthCode(hash, client.ID, redirect, now)` |
| 18 | P2 | `cmd/agent/main.go:203` | run | `_ = writeJSON(cluster.Message{Event: "ping", Device: name, Arch: runtime.GOARCH, OS: runtime.GOOS, TS: time.Now().Unix()…` |
| 19 | P2 | `cmd/agent/main.go:281` | run | `_ = sendError(writeJSON, msg.ID, err)` |
| 20 | P2 | `cmd/agent/main.go:381` | handle | `_ = writeJSON(cluster.Message{ID: msg.ID, Status: "error", Error: err.Error(), Payload: map[string]any{"server": p.Serve…` |
| 21 | P2 | `internal/api/api.go:1109` | DeleteDevice | `_ = s.Store.RevokeDevice(name, "revoked by admin via web console")` |
| 22 | P2 | `internal/api/mcp_adapter.go:48` | ListDevices | `_ = json.Unmarshal(data, &dev)` |
| 23 | P2 | `internal/api/token_limiter.go:239` | rateLimitIntent | `_ = json.Unmarshal(params.Arguments, &args)` |
| 24 | P2 | `internal/api/token_limiter.go:240` | rateLimitIntent | `device, _ = args["device"].(string)` |
| 25 | P2 | `internal/api/tokens.go:166` | requestAuditIntent | `body, _ = io.ReadAll(io.LimitReader(r.Body, 1024*1024))` |
| 26 | P2 | `internal/api/tokens.go:170` | requestAuditIntent | `_ = json.Unmarshal(body, &fields)` |
| 27 | P2 | `internal/cluster/manager.go:110` | NewManager | `_ = store.UpdateDeviceStatus(d.Name, "offline", d.LastHeartbeat)` |
| 28 | P2 | `internal/cluster/manager.go:206` | ServeWS | `_ = n.write(Message{Event: "registered", Device: name, TS: time.Now().Unix()})` |
| 29 | P2 | `internal/cluster/manager.go:327` | readLoop | `_ = m.Store.UpdateDeviceStatus(n.Name, "offline", n.LastHeartbeat.Load()/int64(time.Second))` |
| 30 | P2 | `internal/cluster/manager.go:376` | readLoop | `_ = m.Store.UpsertRemote(n.Name, "online", arch, osName, now/int64(time.Second), string(b))` |
| 31 | P2 | `internal/cluster/manager.go:382` | readLoop | `_ = m.Store.UpsertRemote(n.Name, "online", arch, osName, now/int64(time.Second), "")` |
| 32 | P2 | `internal/cluster/manager.go:510` | RevokeDevice | `_ = n.write(Message{Event: "revoked", Error: "revoked by admin"})` |
| 33 | P2 | `internal/cluster/manager.go:789` | syncMCP | `_ = n.write(Message{Event: "mcp_sync", Payload: services, TS: time.Now().Unix()})` |
| 34 | P2 | `internal/cluster/manager.go:794` | decodeStoredMCPService | `_ = json.Unmarshal([]byte(v.ArgsJSON), &out.Args)` |
| 35 | P2 | `internal/cluster/manager.go:795` | decodeStoredMCPService | `_ = json.Unmarshal([]byte(v.EnvJSON), &out.Env)` |
| 36 | P2 | `internal/cluster/manager.go:796` | decodeStoredMCPService | `_ = json.Unmarshal([]byte(v.HeadersJSON), &out.Headers)` |
| 37 | P2 | `internal/mcp/mcp.go:224` | AuditIntent | `_ = json.Unmarshal(raw, &a)` |
| 38 | P2 | `internal/mcp/oauth_flow.go:660` | decodeScopes | `func decodeScopes(raw string) []string { var v []string; _ = json.Unmarshal([]byte(raw), &v); return v }` |
| 39 | P2 | `internal/notify/notify.go:210` | importOpenClawWebhook | `_ = m.Store.SaveWebhookTarget(storage.WebhookTarget{ID: "openclaw-wecom", Name: "OpenClaw 企业微信", Type: "wecom", URL: str…` |
| 40 | P2 | `internal/notify/notify.go:781` | deliverEvent | `_ = json.Unmarshal([]byte(t.Config), &c)` |
| 41 | P2 | `internal/notify/outbox.go:142` | sendNotificationDelivery | `_ = json.Unmarshal([]byte(job.Event.DataJSON), &legacyData)` |
| 42 | P2 | `internal/notify/outbox.go:163` | sendNotificationDelivery | `_ = json.Unmarshal([]byte(job.Channel.ConfigJSON), &cfg)` |
| 43 | P2 | `internal/storage/notification_policy.go:137` | parseNotificationClock | `_, _ = fmt.Sscanf(value, "%d:%d", &hour, &minute)` |
| 44 | P2 | `internal/storage/notification_store.go:244` | SaveNotificationChannel | `_ = json.Unmarshal(secrets["bot_token"], &token)` |
| 45 | P2 | `internal/storage/notification_store.go:256` | SaveNotificationChannel | `_ = json.Unmarshal(secrets["webhook_url"], &webhookURL)` |
| 46 | P2 | `internal/storage/notification_store.go:268` | SaveNotificationChannel | `_ = json.Unmarshal(secrets["app_secret"], &secret)` |
| 47 | P2 | `internal/storage/oauth.go:651` | ValidateOAuthAccessToken | `_ = s.TouchAPIToken(out.APITokenID, ip)` |
| 48 | P2 | `internal/storage/oauth.go:652` | ValidateOAuthAccessToken | `_ = s.MarkOAuthGrantUsed(out.GrantID, now)` |
| 49 | P2 | `internal/storage/tokens.go:276` | ValidateAPIToken | `_ = json.Unmarshal([]byte(t.ScopesJSON), &scopes)` |
| 50 | P2 | `internal/storage/tokens.go:287` | ValidateAPIToken | `_ = s.TouchAPIToken(t.ID, ip)` |
| 51 | P4 | `internal/api/exec_jobs.go:154` | newJobID | `_, _ = rand.Read(b[:])` |
| 52 | P4 | `internal/mcp/mcp.go:704` | newSessionID | `_, _ = rand.Read(b)` |
| 53 | P3 | `cmd/agent/main.go:250` | run | `_ = c.SetReadDeadline(time.Now().Add(75 * time.Second))` |
| 54 | P3 | `cmd/hub/main.go:220` | main | `_ = store.DB.QueryRow("PRAGMA journal_mode").Scan(&journalModeString)` |
| 55 | P3 | `cmd/hub/main.go:221` | main | `_ = store.DB.QueryRow("PRAGMA synchronous").Scan(&synchronous)` |
| 56 | P3 | `cmd/hub/main.go:222` | main | `_ = store.DB.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)` |
| 57 | P3 | `cmd/hub/main.go:223` | main | `_ = store.DB.QueryRow("PRAGMA wal_autocheckpoint").Scan(&walAutoCheckpoint)` |
| 58 | P3 | `cmd/loadtest/main.go:494` | runAgentConnection | `_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))` |
| 59 | P3 | `cmd/loadtest/main.go:573` | runStreamableClient | `_ = waitContext(ctx, cfg.interval)` |
| 60 | P3 | `internal/api/api.go:1032` | jsonOut | `_ = json.NewEncoder(w).Encode(v)` |
| 61 | P3 | `internal/api/api.go:1066` | devices | `_ = json.Unmarshal(b, &v)` |
| 62 | P3 | `internal/api/api.go:1721` | openapi | `_, _ = w.Write(data)` |
| 63 | P3 | `internal/api/api.go:175` | requireDeviceAccess | `_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))` |
| 64 | P3 | `internal/api/api.go:182` | requireDeviceAccess | `_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"device denied","device":%q}`, device)))` |
| 65 | P3 | `internal/api/api.go:204` | requireSecurity | `_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"forbidden","device":%q,"mode":%q,"reason":%q}`, device, mode, reason)))` |
| 66 | P3 | `internal/api/api.go:306` | legacyWebhookGone | `_ = json.NewEncoder(w).Encode(map[string]string{"error": "legacy webhook API removed; use /api/v1/notifications/channels…` |
| 67 | P3 | `internal/api/api.go:428` | LegacyUsageJSON | `_ = json.NewEncoder(w).Encode(summary)` |
| 68 | P3 | `internal/api/health_assertions.go:57` | requireAnyScope | `_, _ = w.Write([]byte(`{"error":"devices:read or admin scope required"}`))` |
| 69 | P3 | `internal/api/health_assertions.go:92` | healthAssertions | `body, _ = json.Marshal(result)` |
| 70 | P3 | `internal/api/health_assertions.go:97` | healthAssertions | `_, _ = w.Write(append(body, '\n'))` |
| 71 | P3 | `internal/api/notification_deliveries.go:84` | NotificationDeliveryRetryV1JSON | `_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": q.ID, "status": "pending"})` |
| 72 | P3 | `internal/api/session_memo.go:121` | writeSessionMemoError | `_ = json.NewEncoder(w).Encode(map[string]string{"error": message})` |
| 73 | P3 | `internal/api/session_stats.go:127` | copySessionBudgetResponse | `_, _ = dst.Write(body)` |
| 74 | P3 | `internal/api/session_stats.go:59` | sessionStats | `_, _ = w.Write(body)` |
| 75 | P3 | `internal/api/session_stats.go:65` | writeSessionStatsError | `_ = json.NewEncoder(w).Encode(map[string]string{"error": message})` |
| 76 | P3 | `internal/api/token_limiter.go:157` | TokenRateLimitMiddleware | `_, _ = w.Write([]byte(`{"error":"rate_limited"}`))` |
| 77 | P3 | `internal/api/token_limiter.go:256` | rejectTokenExecLimit | `_, _ = w.Write([]byte(`{"error":"rate_limited","detail":"token concurrent exec limit reached"}`))` |
| 78 | P3 | `internal/api/tokens.go:222` | requireScope | `_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"scope %s required"}`, scope)))` |
| 79 | P3 | `internal/api/tokens.go:296` | AuditExportCSV | `_ = c.Write([]string{"# error: export incomplete"})` |
| 80 | P3 | `internal/api/tokens.go:301` | AuditExportCSV | `_ = c.Write([]string{fmt.Sprintf("# truncated at %d rows", rowsWritten)})` |
| 81 | P3 | `internal/api/tokens.go:312` | AuditExportCSV | `_ = c.Write([]string{"# error: export incomplete"})` |
| 82 | P3 | `internal/api/tokens.go:335` | TokenListJSON | `_ = json.Unmarshal([]byte(t.ScopesJSON), &scopes)` |
| 83 | P3 | `internal/api/tokens.go:338` | TokenListJSON | `_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)` |
| 84 | P3 | `internal/auth/auth.go:246` | rejectDisabledLegacy | `_, _ = w.Write([]byte(`{"error":"legacy API key is disabled; use a named API token"}`))` |
| 85 | P3 | `internal/auth/auth.go:297` | unauthorizedWithChallenge | `_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))` |
| 86 | P3 | `internal/cluster/manager.go:335` | readLoop | `_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))` |
| 87 | P3 | `internal/cluster/manager.go:343` | readLoop | `_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))` |
| 88 | P3 | `internal/executor/atomic_write.go:52` | WriteFileAtomic | `owner, _ = ti.Sys().(*syscall.Stat_t)` |
| 89 | P3 | `internal/executor/executor.go:36` | Write | `_, _ = b.buf.Write(p[:n])` |
| 90 | P3 | `internal/executor/fs.go:294` | ListProcesses | `var _ = fmt.Sprintf` |
| 91 | P3 | `internal/executor/fs.go:295` | ListProcesses | `var _ = exec.Command` |
| 92 | P3 | `internal/mcp/mcp.go:1021` | HandleStreamableHTTP | `_, _ = w.Write(responseBody)` |
| 93 | P3 | `internal/mcp/mcp.go:1061` | HandleStreamableHTTP | `_, _ = w.Write(responseBody)` |
| 94 | P3 | `internal/mcp/mcp.go:257` | AuditIntent | `_ = oldText // Never retain either file body in audit metadata.` |
| 95 | P3 | `internal/mcp/mcp.go:837` | HandleMessage | `_, _ = w.Write([]byte("rate_limited"))` |
| 96 | P3 | `internal/mcp/mcp.go:841` | HandleMessage | `_, _ = w.Write([]byte("accepted"))` |
| 97 | P3 | `internal/mcp/mcp.go:855` | HandleMessage | `_, _ = w.Write(respBytes)` |
| 98 | P3 | `internal/mcp/mcp.go:908` | writeJSONRPCError | `_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": JSONRPCError{Code: code, Message: mes…` |
| 99 | P3 | `internal/mcp/oauth.go:65` | writeOAuthJSON | `_ = json.NewEncoder(w).Encode(value)` |
| 100 | P3 | `internal/mcp/oauth_flow.go:360` | beginOAuthAuthorization | `_ = authorizePage.Execute(w, data)` |
| 101 | P3 | `internal/notify/notify.go:782` | deliverEvent | `body, _ = json.Marshal(map[string]any{"chat_id": c.ChatID, "text": e.Message})` |
| 102 | P3 | `internal/notify/notify.go:785` | deliverEvent | `body, _ = json.Marshal(map[string]any{"msgtype": "text", "text": map[string]any{"content": e.Message}})` |
| 103 | P3 | `internal/web/panel.go:429` | writeCachedResponse | `_, _ = w.Write(entry.body)` |
| 104 | P3 | `internal/web/panel.go:449` | index | `_, _ = w.Write(data)` |
| 105 | P3 | `internal/web/panel.go:456` | login | `_, _ = w.Write(data)` |
| 106 | P3 | `internal/web/panel.go:618` | static | `_, _ = w.Write(data)` |



## 10. 与上一版的差异汇总（返工记录）

| 审核意见 | 处理 | 位置 |
|---|---|---|
| 阻塞 1：A/B/C 归类不可复现，A 混入 22 条非审计调用 | 重做归类：A=91（A1 87 / A2 4）、B=60、C=106；给出脚本化规则（1.3）+ 22 条逐条去向表（1.4） | 1.1、1.3、1.4、2 |
| 阻塞 2：A 类结论对投递记录不成立 | 拆 A1/A2；重写 A 类结论；抽样覆盖三条调用链；4 条 A2 判为「失败完全无痕」，列入阶段二 | 3.1、3.2、3.3、6.1 |
| 阻塞 3：引用不存在的 `/tmp/classified.txt`；A/B 无全量清单 | 删除该引用；新增附录 A（A 类 91 条）与附录 B（B 类 60 条）全量清单，格式与 C 类一致 | 7、8 |
| 阻塞 4：C 类漏 cluster/agent 热点；`cmd/hub` 行号错 | 新增 5.4.1（cluster 3 处）、5.4.2（agent 3 处）逐条分析并全部列 P2；`cmd/hub/main.go` 统一为 220-223 | 5.4.1、5.4.2、5.6 |
| 阻塞 5：P1-4 `crypto/rand` 风险不可达 | 降级为 P4「本工具链下不可达」，从 P1 与 OPC 清单移除，引 `go.mod` go 1.24.0 与 `crypto/rand` 文档 | 5.5、6.1、6.3 |
| 非阻塞：基线差异 / 319 无法复现 | 新增 1.2 基线差异表 | 1.2 |
| 非阻塞：P1-2 拆开 fail-open 与展示用 | P1-2 只留 `storage/tokens.go:282`，`api/tokens.go:338` 移 P3 并注明无权限影响 | 5.2、附录 C #81 |
| 非阻塞：`_ = d.Sync()` 单列 | 第 4 节单列，说明目录 fsync 失败的持久化影响 | 4、附录 B #43/#48 |
| 非阻塞：`, _ :=` 不在口径内 | 6.4 给出 98 行的实测命令与分布，建议阶段二扩口径 | 6.4 |
