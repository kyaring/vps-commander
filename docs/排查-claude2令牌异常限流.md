# claude-2 令牌异常限流排查报告

> **日期**：2026-11-07
> **状态**：🔴 **未结案**（根因未确定）
> **代码位置**：`/home/orca/projects/vps-commander`
> **生产实例**：`/opt/vps-commander`（服务 `vps-commander-hub`）

---

## 一、问题描述

**现象**：claude-2 令牌频繁被限流（429），即使把 `rate_rps` 从 10 调到 20 仍会触发。**其他 14 个令牌均未出现此问题。**

---

## 二、已确认的事实

### 2.1 限流记录统计

| 项 | 数据 |
|---|---|
| claude-2 近 7 天 429 次数 | **22 次** |
| 11-01 | 16 次（06:06-06:27 集中） |
| 11-07 | 6 次（08:05-08:06 集中） |
| **其他令牌 429 次数** | **0 次** |
| claude-2 总请求（10-18~11-07） | 979 次（审计）/ 322 次（日志） |

### 2.2 关键时间线（11-07）

```
08:05:05  claude-2  200  duration=10ms
08:05:23  claude-2  200  duration=14ms
08:05:51  claude-2  429  bytes=32   ← 距上次 28 秒
08:06:29  claude-2  429  bytes=32   ← 距上次 38 秒
```

**11-01 类似：**

```
06:05:38  200  (间隔 —)
06:05:43  200  (5s)
06:05:48  200  (5s)
06:05:52  200  (4s)
06:05:59  200  (7s)
06:06:04  200  (5s)
06:06:12  200  (8s)
06:06:18  200  (6s)
06:06:25  429  ← 距上次 7 秒
06:06:27  429  (2s)
06:06:30  429  (3s)
```

### 2.3 实际请求速率

**约 0.2 RPS（每 5 秒 1 个请求）。**

**rate_rps = 20，桶容量 = 20。理论上不该被限流。**

### 2.4 令牌配置

| 字段 | claude-2 | claude-1（对比） |
|---|---|---|
| id | `Br51PEdY7TbD4CWE` | `6buFOdrZhqCmNh0N` |
| prefix | `FS4ZXq6a` | `h8JL3pOV` |
| **rate_rps** | **20** | 10 |
| max_sessions | 20 | 10 |
| scopes | 相同 | 相同 |
| devices_json | `[]`（空） | `[]`（空） |
| **oauth_version** | **2** | 1 |
| is_legacy | 0 | 0 |
| revoked_at | NULL | NULL |
| last_used_ip | **192.168.20.1**（路由器） | 192.168.20.1 |

### 2.5 429 响应特征

**`bytes=32`，对应 `TokenRateLimitMiddleware` 的 RPS 限流响应：**

```json
{"error":"rate_limited"}
```

**不是并发 exec 限制**（那个响应是 65 字节，含 `detail`）：

```json
{"error":"rate_limited","detail":"token concurrent exec limit reached"}
```

---

## 三、代码分析

### 3.1 令牌桶实现（`internal/api/token_limiter.go`）

```go
const (
    defaultTokenRateRPS       = 10
    tokenLimiterStateTTL      = time.Hour
    tokenLimiterCleanupPeriod = 10 * time.Minute
)

type tokenLimitState struct {
    tokens         float64
    lastRefill     time.Time
    lastSeen       time.Time
    execSlots      chan struct{}
    rateAuditCount int
    rateAuditLast  time.Time
}

func (l *TokenLimiter) Allow(tokenID string, rateRPS int, now time.Time) (bool, time.Duration) {
    if l == nil || tokenID == "" {
        return true, 0
    }
    if rateRPS <= 0 {
        rateRPS = defaultTokenRateRPS   // 回落到 10
    }
    l.mu.Lock()
    defer l.mu.Unlock()
    state := l.stateLocked(tokenID, rateRPS, now)
    elapsed := now.Sub(state.lastRefill).Seconds()
    if elapsed > 0 {
        state.tokens += elapsed * float64(rateRPS)
        state.lastRefill = now
    }
    // 桶容量 = rateRPS
    state.tokens = math.Min(float64(rateRPS), state.tokens)
    if state.tokens >= 1 {
        state.tokens--
        return true, 0
    }
    wait := time.Duration(math.Ceil((1 - state.tokens) / float64(rateRPS) * float64(time.Second)))
    if wait < time.Second {
        wait = time.Second
    }
    return false, wait
}
```

**按此逻辑，28 秒间隔 × 20 RPS = 560 个令牌补充，桶容量 20，必然满桶，必然放行。**

**但它返回了 429。**

### 3.2 RateRPS 传递链（已确认正确）

```go
// cmd/hub/main.go:231
authManager := auth.NewManagerWithValidator(apiKey, auth.TokenValidatorFunc(func(raw, ip string) (auth.Principal, bool) {
    id, name, scopes, devices, ok := store.ValidateAPIToken(raw, ip)
    if !ok {
        return auth.Principal{}, false
    }
    t, err := store.GetAPITokenByID(id)
    if err != nil || (legacyKeyOff && t.IsLegacy) {
        return auth.Principal{}, false
    }
    return auth.Principal{
        Type: "api_token", ID: id, Name: name, TokenID: id,
        Scopes: scopes, Devices: devices,
        MaxSessions: t.MaxSessions,
        RateRPS:     t.RateRPS,     // ← 正确从数据库读取
    }, true
}))

// internal/api/token_limiter.go:147
allowed, retryAfter := s.TokenLimiter.Allow(p.QuotaID(), p.RateRPS, time.Now())
```

**`t.RateRPS` 来自数据库，claude-2 是 20，传递链正确。**

### 3.3 中间件注册（`cmd/hub/main.go`）

```go
// 第 286 行
protected := auth.Middleware{...}.Handler(s.TokenRateLimitMiddleware(s.Routes()))

// 第 312-314 行
mux.Handle("/mcp", mcpAuth.Handler(s.TokenRateLimitMiddleware(http.HandlerFunc(mcpServer.HandleStreamableHTTP))))
mux.Handle("/mcp/sse", mcpAuth.Handler(s.TokenRateLimitMiddleware(http.HandlerFunc(mcpServer.HandleSSE))))
mux.Handle("/mcp/message", mcpAuth.Handler(s.TokenRateLimitMiddleware(http.HandlerFunc(mcpServer.HandleMessage))))
```

**每个 HTTP 请求都会扣 1 个令牌。**

### 3.4 并发 exec 限制（独立的，不共用令牌桶）

```go
func (l *TokenLimiter) AcquireExec(tokenID string, now time.Time) (func(), bool) {
    l.mu.Lock()
    state := l.stateLocked(tokenID, defaultTokenRateRPS, now)   // ← 传 10
    slots := state.execSlots
    l.mu.Unlock()
    select {
    case slots <- struct{}{}:
        return func() { <-slots }, true
    default:
        return func() {}, false
    }
}
```

**`AcquireExec` 也调用 `stateLocked`，但不修改 `tokens` 和 `lastRefill`，只更新 `lastSeen`。**

**理论上不污染令牌桶。**

---

## 四、已排除的可能

| # | 假设 | 排除依据 |
|---|---|---|
| 1 | `rate_rps` 配置错误 | ✅ 数据库确认 claude-2 是 20 |
| 2 | `RateRPS` 未传递到限流器 | ✅ 代码确认 `t.RateRPS` → `Principal.RateRPS` → `Allow` |
| 3 | 429 来自并发 exec 限制 | ✅ 响应体 32 字节 = RPS 限流；并发限制是 65 字节 |
| 4 | SSE 长连接占用配额 | ✅ claude-2 只走 `/mcp`（322 次），无 SSE 记录 |
| 5 | 系统时间跳变导致 `elapsed` 为负 | ✅ `timedatectl` 显示 clock synchronized，无跳变记录 |
| 6 | `oauth_version` 影响限速 | ✅ 只影响 OAuth 流程（`GetAPITokenOAuthVersion`） |
| 7 | 令牌桶算法本身错误 | ✅ 代码逻辑正确（28 秒 × 20 RPS 必然满桶） |
| 8 | `QuotaTokenID` 与 `TokenID` 不一致 | ✅ `QuotaTokenID` 仅用于 MCP 会话关闭，不参与限流 |

---

## 五、剩余可疑点（未验证）

### 5.1 有未记录的请求消耗令牌

**日志只记录 `http_request` 级别（`msg=http_request`）。**

**如果 MCP 客户端有重试机制，或者底层有额外的 HTTP 请求，可能不体现在日志里。**

**验证方法**：加调试日志，记录每次 `Allow` 调用。

### 5.2 `AcquireExec` 与 `Allow` 的状态竞争

**两者共用同一个 `state` 对象：**

```go
// Allow 会修改 tokens 和 lastRefill
state.tokens += elapsed * float64(rateRPS)
state.lastRefill = now

// AcquireExec 只更新 lastSeen
state.lastSeen = now
```

**理论上不冲突（都在 `l.mu` 保护下）。**

**但如果 `stateLocked` 里的清理逻辑删除了 state：**

```go
if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= tokenLimiterCleanupPeriod {
    for id, state := range l.states {
        if len(state.execSlots) == 0 && now.Sub(state.lastSeen) >= tokenLimiterStateTTL {
            delete(l.states, id)   // ← 删除后下次重建，tokens = capacity
        }
    }
    l.lastCleanup = now
}
```

**删除后重建是满桶，不会导致 429。**

### 5.3 路由器到本机的链路问题

**claude-2 的 `client_ip = 192.168.20.1`（路由器），说明流量经路由器转发。**

**如果路由器侧有重传、重复投递，可能导致实际请求数多于日志记录。**

**但这个假设无法从服务端验证。**

### 5.4 多实例 / 集群模式

**如果 vchub 运行在多节点集群，各节点的 `TokenLimiter` 是独立的（内存态），但令牌共享。**

**不同节点各自限流，可能导致「看起来没超限但被拒」。**

**需确认**：当前是否单节点运行？

### 5.5 审计记录丢失（独立 Bug）

**日志有 22 次 429，但 `audit_logs` 表里 claude-2 的 `rate_limited` 是 0 条。**

**代码：**

```go
func (s *Server) auditRateLimitedEntry(...) {
    count := 1
    if s.TokenLimiter != nil {
        shouldLog, n := s.TokenLimiter.rateLimitAuditDecision(p.QuotaID(), time.Now())
        if !shouldLog {
            return          // ← 10 秒节流
        }
        count = n
    }
    ...
    _ = s.Store.AuditEntry(...)   // ← 错误被忽略
}
```

**两种可能**：
1. `rateLimitAuditDecision` 返回 false（被节流）—— 但 22 次分布在多天，不该全被节流
2. `Store.AuditEntry` 失败但错误被 `_ =` 丢弃

**这是一个独立的 bug，需要单独修。**

---

## 六、下一步计划

### 6.1 方案 A：加调试日志（推荐）

**在 `Allow` 返回 false 前加：**

```go
log.Printf("RATE_LIMIT_DEBUG token=%s rateRPS=%d tokens=%.4f elapsed=%.2f lastRefill=%v stateAge=%v",
    tokenID, rateRPS, state.tokens, elapsed, state.lastRefill, now.Sub(state.lastSeen))
```

**成本**：
- 改代码（3 行）
- 重新编译
- **重启 hub（会短暂中断所有 Agent）**

**收益**：下次 429 时能看到桶的真实状态。

### 6.2 方案 B：修审计丢失 bug（独立）

**把 `_ =` 改为记录错误：**

```go
if err := s.Store.AuditEntry(...); err != nil {
    log.Printf("audit rate_limited failed: %v", err)
}
```

**这样至少能确认审计为什么没写进去。**

### 6.3 方案 C：临时缓解（不改代码）

**把 claude-2 的 `rate_rps` 调到 100：**

```sql
UPDATE api_tokens SET rate_rps = 100 WHERE name = 'claude-2';
```

**如果调到 100 还限流，说明根本不是 RPS 问题，而是其他 bug。**

**这是最快的验证方法，且无需重启。**

---

## 七、建议

**按此顺序：**

1. **先做方案 C**（改 SQL，零风险）—— 验证是否真的是 RPS 限流
2. 如果调到 100 还限流 → 说明有 bug，做方案 A（加日志）
3. 同时做方案 B（修审计）—— 独立问题，随时可做

**方案 C 的具体做法：**

```bash
sqlite3 /opt/vps-commander/data/commander.db \
  "UPDATE api_tokens SET rate_rps = 100 WHERE name = 'claude-2';"
```

**注意**：改完是否需要重启 hub 才生效？

**从代码看**：

```go
t, err := store.GetAPITokenByID(id)   // ← 每次认证都查库
return auth.Principal{..., RateRPS: t.RateRPS}
```

**每次请求都查库，所以改 SQL 立即生效，无需重启。**

---

## 八、附：关键代码位置

| 内容 | 路径:行号 |
|---|---|
| 令牌桶核心 | `internal/api/token_limiter.go:78-104` |
| 限速中间件 | `internal/api/token_limiter.go:140-166` |
| 并发 exec 限制 | `internal/api/token_limiter.go:106-121` |
| 审计节流 | `internal/api/token_limiter.go:123-138` |
| 审计写入 | `internal/api/token_limiter.go:187-209` |
| Principal 构造 | `cmd/hub/main.go:231-243` |
| 中间件注册 | `cmd/hub/main.go:286, 312-314` |
| 令牌验证 | `internal/storage/tokens.go:255-288` |
| QuotaID 实现 | `internal/auth/auth.go:31-36` |

---

## 九、附录：原始数据

### 9.1 所有令牌配置

```
name         oauth_version  rate_rps
Dave-1       1              10
RikkaHub-1   1              10
chatgpt-1    1              10
claude-1     1              10
grok-1       1              10
hark-2       1              10
legacy-env   1              10
muse-1       1              10
orca-gemini  1              10
workbuddy-1  1              10
All-notify   2              2
claude-2     2              20      ← 唯一被限流的
gpt-2        2              10
hark-1       2              10
orca-gpt     2              10
```

**注意：`All-notify` 也是 oauth_version=2，rate_rps=2（最低），但没有 429。**

### 9.2 11-01 的 16 次 429 时间点

```
06:06:25
06:06:27
06:06:30
06:13:32
06:15:03
06:15:13
06:15:15
06:16:53
06:17:00
06:17:02
06:19:23
06:22:16
06:22:23
06:25:17
06:26:28
06:26:55
```

**特征：成簇出现，每簇 2-3 次连续。**

### 9.3 11-07 的 6 次 429

```
08:05:51
08:06:29
（另有 4 次未在本次查询窗口内）
```

---

## 十、结案条件

**以下任一条件满足即可结案：**

1. 通过调试日志确认了 `state.tokens` 的真实值，找到了根因
2. 把 `rate_rps` 调到 100 后不再限流，确认为参数问题
3. 定位到其他代码路径（非 `TokenRateLimiter`）导致的 429

---

*报告结束。状态：未结案，等待下一步验证。*
