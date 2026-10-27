# 审计：非测试代码中的 `_ =` 忽略错误（阶段一：分类报告）

- 对应卡：T-20261028-03（阶段一，只出报告，不改任何 .go 文件）
- 基线：main `ac25d16`
- 作者：Claude（开发），待 Hark 复核
- 本报告不做任何代码修改；阶段二（改行为）须经 OPC 决定。

## 1. 统计方法

```
git grep -n "_ = \|_, _ = " -- 'internal/*.go' 'cmd/*.go' | grep -v "_test.go:"
```
结果：**257 处**（每一处的文件、行号、所在函数、原文已列在附录，共 90 条 C 类；A、B 类的全量列表见 `/tmp/classified.txt` 的生成规则，即下述分类规则）。

说明：早先讨论中的「319 处」来自另一种更宽的筛选（未按文件名排除测试文件），不作为本次口径；以本节口径为准。其中包含 `var _ = fmt.Sprintf` 这类占位写法 2 处，它们不是错误被忽略，单独计为 C 类「占位」，见第 4 节。

## 2. 分类结果

| 类别 | 数量 | 含义 |
|---|---:|---|
| A 审计写入 | 113 | 写审计/通知记录的调用，返回值被丢弃 |
| B 清理 | 54 | Close、Remove、Unlock、Stop 等清理调用 |
| C 其他 | 90 | 需要判断影响的调用，阶段一只标注，阶段二处理 |
| 合计 | 257 | 与 grep 结果一致 |

分类依据：按调用名与所在函数名的关键字归类（audit/notify/deliver/record → A；close/remove/unlock/rollback/stop/kill/flush/release 等 → B；其余 → C）。归类后对 C 类逐条人工核对，并把占位、写 HTTP 响应等明显无害的单独标出。

C 类按包分布：internal/mcp 26, internal/api 24, internal/cluster 12, internal/storage 8, cmd/agent 4, cmd/hub 4, internal/executor 4, internal/web 4, cmd/loadtest 2, internal/auth 2。

## 3. A 类：审计写入（113 处）——结论：失败不无痕

抽样 12 处（完整抽样来源见下，均核对了调用链）：

- `internal/api/api.go:173` [requireDeviceAccess] `_ = s.audit(r, device, "auth_denied", "", http.StatusUnauthorized, 0, "missing principal")`
- `internal/api/api.go:179` [requireDeviceAccess] `_ = s.audit(r, device, "device_denied", "", 0, 0, "token device whitelist denied")`
- `internal/api/api.go:201` [requireSecurity] `_ = s.audit(r, device, "security_denied", action, http.StatusForbidden, 0, reason)`
- `internal/api/api.go:276` [UpdateNotificationSettingsJSON] `_ = s.Store.AuditEntry(storage.AuditEntry{Action: "notification_setting", ActorType: "pane`
- `internal/api/api.go:303` [legacyWebhookGone] `_ = s.audit(r, "", "notification_setting_legacy_gone", r.URL.Path, http.StatusGone, 0, "le`
- `internal/api/api.go:306` [legacyWebhookGone] `_ = json.NewEncoder(w).Encode(map[string]string{"error": "legacy webhook API removed; use `
- `internal/api/api.go:369` [UpdateLegacyKeyStatusJSON] `_ = s.Store.AuditEntry(storage.AuditEntry{Action: "legacy_key_setting", ActorType: "panel"`
- `internal/api/api.go:410` [RecordLegacyRejected] `_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_rejected", Command`
- `internal/api/api.go:493` [ProvisionDeviceCredentialJSON] `_ = s.audit(r, q.Device, "device_credential_provision", "device="+q.Device, 0, 0, "credent`
- `internal/api/api.go:538` [UpdateSecuritySettingsJSON] `_ = s.audit(r, "", "security_setting", "global="+q.Global, 0, 0, fmt.Sprintf("old=%s new=%`
- `internal/api/api.go:541` [UpdateSecuritySettingsJSON] `_ = s.audit(r, q.Node, "security_setting", "mode="+q.Mode, 0, 0, fmt.Sprintf("old=%s new=%`
- `internal/api/api.go:601` [RecordLegacyAuth] `_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_auth", Command: r.`

核对的调用链（全部一致）：

1. 调用方 `_ = s.audit(...)` / `_ = s.auditMCPContext(...)` 丢弃返回值；
2. `auditContext` / `auditContextTarget`（`internal/api/tokens.go`）调用 `Store.AuditEntry`，并把错误返回；
3. `Store.AuditEntry`（`internal/storage/storage.go`）在异步队列或同步写入失败时调用 `writeAuditFallback`：把整条审计以 JSON 行（`type=vps_commander_audit_fallback`，含 `persistence_error`）写到 stderr，并计数 `auditFallbackCount`；
4. 批量写入失败时打印 `ERROR audit batch write attempt ... failed` 日志。

结论：A 类调用丢弃返回值是可接受的，失败会在 stderr（journald）留下可检索的记录。前提是 journald 保留 stderr 且 Hub 以 systemd 运行（当前部署满足）。**建议**阶段二不改 A 类代码，只在运维文档中写明「审计失败的排查方式：搜索 `vps_commander_audit_fallback`」。

## 4. B 类：清理（54 处）——结论：可接受

主要为 `Close()`、`os.Remove()`、`Unlock()` 之前的清理，失败时没有补救动作，且清理失败不会改变用户可见结果。少数 `os.Remove` 在删除过期文件时失败，会导致磁盘上残留文件，但由后台清扫与下次启动清理兜底（T-20261016-01 的溢出存储设计）。**建议**：保持不变。

## 5. C 类：需要判断的 90 处

按影响分组，并给出优先级（P1 = 安全或误路由，P2 = 会掩盖真实故障，P3 = 无用户影响）。

### P1-1 MCP 参数解析失败被静默吞掉，可能误路由（`internal/mcp/mcp.go` 等约 18 处）
`callTool` 与 `handleRequest` 中大量 `_ = json.Unmarshal(params.Arguments, &args)`。参数 JSON 格式错误时，结构体保持零值，随后工具按默认值执行。例如 `device` 传成数字时，`device` 变为空字符串，`normalizeDevice` 会回落到 Hub 本机，**一条本该发往远端节点的 exec 可能在本机执行**。
**建议**（阶段二）：解析失败返回 JSON-RPC `invalid params` 错误；对 `device` 等关键字段做类型校验。需要 OPC 决定：这会改变对错误参数的响应（从“执行”变为“报错”）。

### P1-2 设备权限在数据解析失败时失败开放（`internal/storage/tokens.go:282`，`internal/api/tokens.go:338`）
`ValidateAPIToken` 中 `_ = json.Unmarshal(t.DevicesJSON, &devices)`。解析失败时 `devices` 为 nil，而 `Principal.AllowsDevice` 对 nil 返回 **true（允许所有设备）**。也就是说，数据库中设备列表损坏的令牌，会变成可以访问任意节点。触发条件是数据损坏或被手工改库，但失败方向是错误的。
**建议**（阶段二）：解析失败时拒绝该令牌（返回无效令牌）。需要 OPC 决定：损坏的令牌会立即失效。

### P1-3 OAuth 授权码在 PKCE 失败时的消费结果被丢弃（`internal/mcp/oauth_flow.go:559`）
PKCE 校验失败后调用 `_, _ = ConsumeOAuthCode(...)` 以“烧掉”授权码，防止反复猜测。如果这次写库失败（例如数据库忙），授权码不会被标记为已使用，攻击者在失败后仍可以尝试其他 verifier。
**建议**（阶段二）：至少记录日志；更稳妥的做法是 PKCE 失败时仍返回 `invalid_grant`，并在数据库写失败时记录 `ERROR`。需要 OPC 决定是否接受这个风险。

### P1-4 随机数失败被忽略（`internal/mcp/mcp.go:704` `newSessionID`，`internal/api/exec_jobs.go:154` `newJobID`）
`_, _ = rand.Read(b)`。`crypto/rand` 失败时 `b` 保持全零，会话 ID 或任务 ID 变为常量，可以被猜测或冲突。该失败在 Linux 上极少发生，但一旦发生没有任何迹象。
**建议**（阶段二）：检查返回值，失败时返回错误（`spill.go` 已这样做，可作为模板）。

### P2-1 吊销设备的结果在无 Cluster 路径被丢弃（`internal/api/api.go:1109` `DeleteDevice`）
Cluster 存在时错误已检查；无 Cluster 的分支 `_ = s.Store.RevokeDevice(...)` 丢弃错误，界面仍显示“已吊销”。
**建议**（阶段二）：与 Cluster 分支一致，返回 500。

### P2-2 远端设备的状态写库失败被忽略（`internal/cluster/manager.go:376`、`:382`、`:110`、`:327`）
`UpsertRemote`、`UpdateDeviceStatus` 的错误被丢弃。失败时管理面板显示的设备状态可能与实际不一致，但不影响权限。
**建议**：记录日志（`log.Printf`），不改行为。

### P2-3 通知时间、令牌元数据等解析与写入失败（`internal/storage/notification_policy.go:137`、`notification_store.go:244,268`、`oauth.go:651,652`、`tokens.go:287`）
- `parseNotificationClock` 解析失败时静默得到 0 点，静音时段会被错误设置。建议记录日志并返回错误。
- `TouchAPIToken`、`MarkOAuthGrantUsed` 只更新“最后使用时间”等元数据，失败无用户影响。建议记录日志即可。
- 通知凭据 JSON 解析失败时，凭据为空，通知会以无凭据方式发送并失败。建议返回错误。

### P2-4 其他解析失败（`internal/cluster/manager.go:794-796`、`internal/api/mcp_adapter.go:48`、`internal/api/token_limiter.go:239,240`、`internal/mcp/oauth_flow.go:660`）
存储的 MCP 服务配置、设备列表等 JSON 损坏时静默为空。建议记录日志，阶段二再决定是否返回错误。

### P3 无用户影响（约 40 处）
- 写 HTTP 响应体的 `w.Write(...)`、`json.NewEncoder(w).Encode(...)`（`internal/api`、`internal/mcp`、`internal/web`、`internal/auth`）：响应已经开始，错误无法再报告给客户端。建议保持不变。
- `cmd/loadtest/*` 的压测工具：忽略错误可接受，保持不变。
- `internal/executor/fs.go:294-295` 的 `var _ = fmt.Sprintf`、`var _ = exec.Command`：**占位写法**，只为避免未使用导入的编译错误，不是忽略错误。建议阶段二删除这两行和对应的导入。
- `internal/executor/executor.go:36` 写入 `bytes.Buffer` 永远不会失败。
- `cmd/hub/main.go:222-223` 读取 PRAGMA 的诊断值，失败时仅影响启动日志。

## 6. 阶段二建议（需要 OPC 决定，不在本卡内执行）

1. 先处理 P1-1、P1-2：它们可能导致误路由或权限失效，应优先。
2. P1-3、P1-4 的处理方式需要 OPC 决定（是否接受现有风险，或改为失败即拒绝）。
3. P2 类只记录日志即可，不改行为，可以单独成卡。
4. 占位写法和 A 类 `_ =` 保持不变。

## 7. 附录：C 类全量清单（90 条）

| # | 位置 | 所在函数 | 原文（截断） |
|---:|---|---|---|
| 1 | `cmd/agent/main.go:203` | run | `_ = writeJSON(cluster.Message{Event: "ping", Device: name, Arch: runtime.GOARCH, OS: runtime.GOOS, TS: time.Now().Unix()` |
| 2 | `cmd/agent/main.go:250` | run | `_ = c.SetReadDeadline(time.Now().Add(75 * time.Second))` |
| 3 | `cmd/agent/main.go:281` | run | `_ = sendError(writeJSON, msg.ID, err)` |
| 4 | `cmd/agent/main.go:381` | handle | `_ = writeJSON(cluster.Message{ID: msg.ID, Status: "error", Error: err.Error(), Payload: map[string]any{"server": p.Serve` |
| 5 | `cmd/hub/main.go:220` | main | `_ = store.DB.QueryRow("PRAGMA journal_mode").Scan(&journalModeString)` |
| 6 | `cmd/hub/main.go:221` | main | `_ = store.DB.QueryRow("PRAGMA synchronous").Scan(&synchronous)` |
| 7 | `cmd/hub/main.go:222` | main | `_ = store.DB.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)` |
| 8 | `cmd/hub/main.go:223` | main | `_ = store.DB.QueryRow("PRAGMA wal_autocheckpoint").Scan(&walAutoCheckpoint)` |
| 9 | `cmd/loadtest/main.go:494` | runAgentConnection | `_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))` |
| 10 | `cmd/loadtest/main.go:573` | runStreamableClient | `_ = waitContext(ctx, cfg.interval)` |
| 11 | `internal/api/api.go:175` | requireDeviceAccess | `_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))` |
| 12 | `internal/api/api.go:182` | requireDeviceAccess | `_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"device denied","device":%q}`, device)))` |
| 13 | `internal/api/api.go:204` | requireSecurity | `_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"forbidden","device":%q,"mode":%q,"reason":%q}`, device, mode, reason)))` |
| 14 | `internal/api/api.go:428` | LegacyUsageJSON | `_ = json.NewEncoder(w).Encode(summary)` |
| 15 | `internal/api/api.go:1032` | jsonOut | `_ = json.NewEncoder(w).Encode(v)` |
| 16 | `internal/api/api.go:1066` | devices | `_ = json.Unmarshal(b, &v)` |
| 17 | `internal/api/api.go:1109` | DeleteDevice | `_ = s.Store.RevokeDevice(name, "revoked by admin via web console")` |
| 18 | `internal/api/api.go:1721` | openapi | `_, _ = w.Write(data)` |
| 19 | `internal/api/exec_jobs.go:154` | newJobID | `_, _ = rand.Read(b[:])` |
| 20 | `internal/api/health_assertions.go:57` | requireAnyScope | `_, _ = w.Write([]byte(`{"error":"devices:read or admin scope required"}`))` |
| 21 | `internal/api/health_assertions.go:92` | healthAssertions | `body, _ = json.Marshal(result)` |
| 22 | `internal/api/health_assertions.go:97` | healthAssertions | `_, _ = w.Write(append(body, '\n'))` |
| 23 | `internal/api/mcp_adapter.go:48` | ListDevices | `_ = json.Unmarshal(data, &dev)` |
| 24 | `internal/api/session_memo.go:121` | writeSessionMemoError | `_ = json.NewEncoder(w).Encode(map[string]string{"error": message})` |
| 25 | `internal/api/session_stats.go:59` | sessionStats | `_, _ = w.Write(body)` |
| 26 | `internal/api/session_stats.go:65` | writeSessionStatsError | `_ = json.NewEncoder(w).Encode(map[string]string{"error": message})` |
| 27 | `internal/api/session_stats.go:127` | copySessionBudgetResponse | `_, _ = dst.Write(body)` |
| 28 | `internal/api/token_limiter.go:157` | TokenRateLimitMiddleware | `_, _ = w.Write([]byte(`{"error":"rate_limited"}`))` |
| 29 | `internal/api/token_limiter.go:239` | rateLimitIntent | `_ = json.Unmarshal(params.Arguments, &args)` |
| 30 | `internal/api/token_limiter.go:240` | rateLimitIntent | `device, _ = args["device"].(string)` |
| 31 | `internal/api/token_limiter.go:256` | rejectTokenExecLimit | `_, _ = w.Write([]byte(`{"error":"rate_limited","detail":"token concurrent exec limit reached"}`))` |
| 32 | `internal/api/tokens.go:222` | requireScope | `_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"scope %s required"}`, scope)))` |
| 33 | `internal/api/tokens.go:335` | TokenListJSON | `_ = json.Unmarshal([]byte(t.ScopesJSON), &scopes)` |
| 34 | `internal/api/tokens.go:338` | TokenListJSON | `_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)` |
| 35 | `internal/auth/auth.go:246` | rejectDisabledLegacy | `_, _ = w.Write([]byte(`{"error":"legacy API key is disabled; use a named API token"}`))` |
| 36 | `internal/auth/auth.go:297` | unauthorizedWithChallenge | `_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))` |
| 37 | `internal/cluster/manager.go:110` | NewManager | `_ = store.UpdateDeviceStatus(d.Name, "offline", d.LastHeartbeat)` |
| 38 | `internal/cluster/manager.go:206` | ServeWS | `_ = n.write(Message{Event: "registered", Device: name, TS: time.Now().Unix()})` |
| 39 | `internal/cluster/manager.go:327` | readLoop | `_ = m.Store.UpdateDeviceStatus(n.Name, "offline", n.LastHeartbeat.Load()/int64(time.Second))` |
| 40 | `internal/cluster/manager.go:335` | readLoop | `_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))` |
| 41 | `internal/cluster/manager.go:343` | readLoop | `_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))` |
| 42 | `internal/cluster/manager.go:376` | readLoop | `_ = m.Store.UpsertRemote(n.Name, "online", arch, osName, now/int64(time.Second), string(b))` |
| 43 | `internal/cluster/manager.go:382` | readLoop | `_ = m.Store.UpsertRemote(n.Name, "online", arch, osName, now/int64(time.Second), "")` |
| 44 | `internal/cluster/manager.go:510` | RevokeDevice | `_ = n.write(Message{Event: "revoked", Error: "revoked by admin"})` |
| 45 | `internal/cluster/manager.go:789` | syncMCP | `_ = n.write(Message{Event: "mcp_sync", Payload: services, TS: time.Now().Unix()})` |
| 46 | `internal/cluster/manager.go:794` | decodeStoredMCPService | `_ = json.Unmarshal([]byte(v.ArgsJSON), &out.Args)` |
| 47 | `internal/cluster/manager.go:795` | decodeStoredMCPService | `_ = json.Unmarshal([]byte(v.EnvJSON), &out.Env)` |
| 48 | `internal/cluster/manager.go:796` | decodeStoredMCPService | `_ = json.Unmarshal([]byte(v.HeadersJSON), &out.Headers)` |
| 49 | `internal/executor/atomic_write.go:52` | WriteFileAtomic | `owner, _ = ti.Sys().(*syscall.Stat_t)` |
| 50 | `internal/executor/executor.go:36` | Write | `_, _ = b.buf.Write(p[:n])` |
| 51 | `internal/executor/fs.go:294` | ListProcesses | `var _ = fmt.Sprintf` |
| 52 | `internal/executor/fs.go:295` | ListProcesses | `var _ = exec.Command` |
| 53 | `internal/mcp/mcp.go:704` | newSessionID | `_, _ = rand.Read(b)` |
| 54 | `internal/mcp/mcp.go:837` | HandleMessage | `_, _ = w.Write([]byte("rate_limited"))` |
| 55 | `internal/mcp/mcp.go:841` | HandleMessage | `_, _ = w.Write([]byte("accepted"))` |
| 56 | `internal/mcp/mcp.go:855` | HandleMessage | `_, _ = w.Write(respBytes)` |
| 57 | `internal/mcp/mcp.go:908` | writeJSONRPCError | `_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": JSONRPCError{Code: code, Message: mes` |
| 58 | `internal/mcp/mcp.go:1021` | HandleStreamableHTTP | `_, _ = w.Write(responseBody)` |
| 59 | `internal/mcp/mcp.go:1061` | HandleStreamableHTTP | `_, _ = w.Write(responseBody)` |
| 60 | `internal/mcp/mcp.go:1279` | handleRequest | `_ = json.Unmarshal(params.Arguments, &args)` |
| 61 | `internal/mcp/mcp.go:1357` | callTool | `_ = json.Unmarshal(params.Arguments, &rawDevice)` |
| 62 | `internal/mcp/mcp.go:1460` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 63 | `internal/mcp/mcp.go:1485` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 64 | `internal/mcp/mcp.go:1504` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 65 | `internal/mcp/mcp.go:1516` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 66 | `internal/mcp/mcp.go:1528` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 67 | `internal/mcp/mcp.go:1545` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 68 | `internal/mcp/mcp.go:1560` | callTool | `_ = json.Unmarshal(params.Arguments, &a)` |
| 69 | `internal/mcp/mcp.go:1572` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 70 | `internal/mcp/mcp.go:1588` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 71 | `internal/mcp/mcp.go:1602` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 72 | `internal/mcp/mcp.go:1628` | callTool | `_ = json.Unmarshal(params.Arguments, &args)` |
| 73 | `internal/mcp/mcp.go:1635` | callTool | `_ = json.Unmarshal(args.Arguments, &av)` |
| 74 | `internal/mcp/mcp.go:1905` | callAsyncExec | `_ = json.Unmarshal(params.Arguments, &args)` |
| 75 | `internal/mcp/oauth.go:65` | writeOAuthJSON | `_ = json.NewEncoder(w).Encode(value)` |
| 76 | `internal/mcp/oauth_flow.go:360` | beginOAuthAuthorization | `_ = authorizePage.Execute(w, data)` |
| 77 | `internal/mcp/oauth_flow.go:559` | HandleToken | `_, _ = s.OAuthStore.ConsumeOAuthCode(hash, client.ID, redirect, now)` |
| 78 | `internal/mcp/oauth_flow.go:660` | decodeScopes | `func decodeScopes(raw string) []string { var v []string; _ = json.Unmarshal([]byte(raw), &v); return v }` |
| 79 | `internal/storage/notification_policy.go:137` | parseNotificationClock | `_, _ = fmt.Sscanf(value, "%d:%d", &hour, &minute)` |
| 80 | `internal/storage/notification_store.go:244` | SaveNotificationChannel | `_ = json.Unmarshal(secrets["bot_token"], &token)` |
| 81 | `internal/storage/notification_store.go:268` | SaveNotificationChannel | `_ = json.Unmarshal(secrets["app_secret"], &secret)` |
| 82 | `internal/storage/oauth.go:651` | ValidateOAuthAccessToken | `_ = s.TouchAPIToken(out.APITokenID, ip)` |
| 83 | `internal/storage/oauth.go:652` | ValidateOAuthAccessToken | `_ = s.MarkOAuthGrantUsed(out.GrantID, now)` |
| 84 | `internal/storage/tokens.go:276` | ValidateAPIToken | `_ = json.Unmarshal([]byte(t.ScopesJSON), &scopes)` |
| 85 | `internal/storage/tokens.go:282` | ValidateAPIToken | `_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)` |
| 86 | `internal/storage/tokens.go:287` | ValidateAPIToken | `_ = s.TouchAPIToken(t.ID, ip)` |
| 87 | `internal/web/panel.go:429` | writeCachedResponse | `_, _ = w.Write(entry.body)` |
| 88 | `internal/web/panel.go:449` | index | `_, _ = w.Write(data)` |
| 89 | `internal/web/panel.go:456` | login | `_, _ = w.Write(data)` |
| 90 | `internal/web/panel.go:618` | static | `_, _ = w.Write(data)` |
