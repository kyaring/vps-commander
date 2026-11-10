package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wjyhk/vps-commander/internal/auditutil"
	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/storage"
)

var jsonRPCMethodPattern = regexp.MustCompile(`"method"\s*:\s*"([^"]+)"`)

const (
	defaultTokenRateRPS       = 10
	tokenLimiterStateTTL      = time.Hour
	tokenLimiterCleanupPeriod = 10 * time.Minute
)

type tokenLimitState struct {
	tokens                float64
	lastRefill            time.Time
	lastSeen              time.Time
	inFlight              int
	customExecLimit       int
	hasCustomLimit        bool
	rateAuditCount        int
	rateAuditLast         time.Time
	concurrencyAuditCount int
	concurrencyAuditLast  time.Time
}

func (s *tokenLimitState) effectiveExecLimit(globalDefault int) int {
	if s.hasCustomLimit && s.customExecLimit > 0 {
		return s.customExecLimit
	}
	if globalDefault > 0 {
		return globalDefault
	}
	return 2
}

// TokenLimiter applies independent token buckets and exec semaphores per API token.
type TokenLimiter struct {
	mu                sync.Mutex
	states            map[string]*tokenLimitState
	maxConcurrentExec int
	lastCleanup       time.Time
}

func NewTokenLimiter(maxConcurrentExec int) *TokenLimiter {
	if maxConcurrentExec <= 0 {
		maxConcurrentExec = 2
	}
	return &TokenLimiter{states: make(map[string]*tokenLimitState), maxConcurrentExec: maxConcurrentExec}
}

func (l *TokenLimiter) stateLocked(tokenID string, rateRPS int, now time.Time) *tokenLimitState {
	if rateRPS <= 0 {
		rateRPS = defaultTokenRateRPS
	}
	if l.states == nil {
		l.states = make(map[string]*tokenLimitState)
	}
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= tokenLimiterCleanupPeriod {
		for id, state := range l.states {
			if state.inFlight == 0 && now.Sub(state.lastSeen) >= tokenLimiterStateTTL {
				delete(l.states, id)
			}
		}
		l.lastCleanup = now
	}
	state := l.states[tokenID]
	if state == nil {
		capacity := float64(rateRPS)
		state = &tokenLimitState{tokens: capacity, lastRefill: now, lastSeen: now}
		l.states[tokenID] = state
	}
	state.lastSeen = now
	return state
}

func (l *TokenLimiter) Allow(tokenID string, rateRPS int, now time.Time) (bool, time.Duration) {
	if l == nil || tokenID == "" {
		return true, 0
	}
	if rateRPS <= 0 {
		rateRPS = defaultTokenRateRPS
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateLocked(tokenID, rateRPS, now)
	elapsed := now.Sub(state.lastRefill).Seconds()
	if elapsed > 0 {
		state.tokens += elapsed * float64(rateRPS)
		state.lastRefill = now
	}
	// Clamp even when rate_rps was reduced while a token bucket was full.
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

func (l *TokenLimiter) AcquireExec(tokenID string, now time.Time) (func(), bool) {
	if l == nil || tokenID == "" {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateLocked(tokenID, defaultTokenRateRPS, now)
	limit := state.effectiveExecLimit(l.maxConcurrentExec)
	if state.inFlight >= limit {
		return func() {}, false
	}
	state.inFlight++
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if !released {
			released = true
			state.inFlight--
			if state.inFlight < 0 {
				state.inFlight = 0
			}
		}
	}, true
}

func (l *TokenLimiter) SetTokenLimit(tokenID string, limit int) {
	if l == nil || tokenID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateLocked(tokenID, defaultTokenRateRPS, time.Now())
	state.customExecLimit = limit
	state.hasCustomLimit = true
}

func (l *TokenLimiter) HasConfiguredLimit(tokenID string) bool {
	if l == nil || tokenID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.states[tokenID]
	return state != nil && state.hasCustomLimit
}

func (l *TokenLimiter) MaxConcurrentExec() int {
	if l == nil {
		return 2
	}
	return l.maxConcurrentExec
}

func (l *TokenLimiter) MaxConcurrentExecForToken(tokenID string) int {
	if l == nil {
		return 2
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if tokenID != "" {
		if state := l.states[tokenID]; state != nil {
			return state.effectiveExecLimit(l.maxConcurrentExec)
		}
	}
	return l.maxConcurrentExec
}

func (l *TokenLimiter) ExecInUse(tokenID string) int {
	if l == nil || tokenID == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.states[tokenID]
	if state == nil {
		return 0
	}
	return state.inFlight
}

func (l *TokenLimiter) concurrencyAuditDecision(tokenID string, now time.Time) (bool, int, int, int) {
	if l == nil || tokenID == "" {
		return true, 1, 2, 2
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateLocked(tokenID, defaultTokenRateRPS, now)
	state.concurrencyAuditCount++
	limit := state.effectiveExecLimit(l.maxConcurrentExec)
	inUse := state.inFlight
	if state.concurrencyAuditLast.IsZero() || now.Sub(state.concurrencyAuditLast) >= 10*time.Second {
		count := state.concurrencyAuditCount
		state.concurrencyAuditCount = 0
		state.concurrencyAuditLast = now
		return true, count, limit, inUse
	}
	return false, state.concurrencyAuditCount, limit, inUse
}

// rateLimitAuditDecision throttles audit writes to one per token per ten seconds.
// The returned count includes all denials since the previous emitted audit row.
func (l *TokenLimiter) rateLimitAuditDecision(tokenID string, now time.Time) (bool, int) {
	if l == nil || tokenID == "" {
		return true, 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateLocked(tokenID, defaultTokenRateRPS, now)
	state.rateAuditCount++
	if state.rateAuditLast.IsZero() || now.Sub(state.rateAuditLast) >= 10*time.Second {
		count := state.rateAuditCount
		state.rateAuditCount = 0
		state.rateAuditLast = now
		return true, count
	}
	return false, state.rateAuditCount
}

func (s *Server) TokenRateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFromContext(r.Context())
		if !ok || p.Type != "api_token" || p.QuotaID() == "" || s.TokenLimiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		allowed, retryAfter := s.TokenLimiter.Allow(p.QuotaID(), p.RateRPS, time.Now())
		if !allowed {
			s.auditRateLimited(r, p)
			seconds := int(math.Ceil(retryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", fmt.Sprint(seconds))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			limit := p.RateRPS
			if limit <= 0 {
				limit = defaultTokenRateRPS
			}
			retryAfterMS := retryAfter.Milliseconds()
			if retryAfterMS < 0 {
				retryAfterMS = 0
			}
			resp := map[string]any{
				"error":          "rate_limited",
				"limiter":        "rate",
				"limit":          limit,
				"retry_after_ms": retryAfterMS,
			}
			body, _ := json.Marshal(resp)
			_, _ = w.Write(body)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ensureTokenConcurrencyLimit(tokenID string) {
	if s == nil || s.TokenLimiter == nil || s.Store == nil || tokenID == "" {
		return
	}
	if s.TokenLimiter.HasConfiguredLimit(tokenID) {
		return
	}
	tok, err := s.Store.GetAPITokenByID(tokenID)
	if err == nil {
		s.TokenLimiter.SetTokenLimit(tokenID, tok.ConcurrentExecLimit)
	}
}

func (s *Server) acquireTokenExec(p auth.Principal) (func(), bool) {
	if s.TokenLimiter == nil || p.Type != "api_token" || p.QuotaID() == "" {
		return func() {}, true
	}
	s.ensureTokenConcurrencyLimit(p.QuotaID())
	return s.TokenLimiter.AcquireExec(p.QuotaID(), time.Now())
}

func (s *Server) auditRateLimited(r *http.Request, p auth.Principal) {
	if s.Store == nil {
		return
	}
	transport := "rest"
	switch r.URL.Path {
	case "/mcp":
		transport = "mcp-http"
	case "/mcp/sse", "/mcp/message":
		transport = "mcp-sse"
	}
	device, _, command, method := rateLimitIntent(r)
	s.auditRateLimitedEntry(auth.ClientIP(r), device, command, transport, method, p)
}

func (s *Server) auditRateLimitedEntry(clientIP, device, command, transport, method string, p auth.Principal) {
	if s.Store == nil {
		return
	}
	count := 1
	if s.TokenLimiter != nil {
		shouldLog, n := s.TokenLimiter.rateLimitAuditDecision(p.QuotaID(), time.Now())
		if !shouldLog {
			return
		}
		count = n
	}
	command = auditutil.Truncate(auditutil.Redact(command))
	message := fmt.Sprintf("rate_limited (%d rejected since previous audit; 10s throttle)", count)
	if method != "" {
		message += "; jsonrpc_method=" + method
	}
	targetType, target := "token", p.Name
	if device != "" {
		targetType, target = "device", device
	}
	if err := s.Store.AuditEntry(storage.AuditEntry{CallerIP: clientIP, Device: device, Action: "rate_limited", Command: command, ErrorMsg: message, ActorType: p.Type, ActorID: p.ID, ActorName: p.AuditName(), OAuthClientID: p.OAuthClient, Transport: transport, TargetType: targetType, Target: target, Result: "denied"}); err != nil {
		slog.Error("rate limit audit write failed", "error", err, "actor", p.AuditName(), "device", device)
	}
}

func rateLimitIntent(r *http.Request) (device, action, command, method string) {
	if r.Body == nil || (r.URL.Path != "/mcp" && r.URL.Path != "/mcp/message") {
		return "", "", "", ""
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(body, &req) != nil {
		match := jsonRPCMethodPattern.FindSubmatch(body)
		if len(match) == 2 {
			return "", "", "", string(match[1])
		}
		return "", "", "", ""
	}
	method = req.Method
	if method != "tools/call" {
		return "", "", "", method
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(req.Params, &params) != nil {
		return "", "", "", method
	}
	var args map[string]any
	_ = json.Unmarshal(params.Arguments, &args)
	device, _ = args["device"].(string)
	command = mcp.AuditIntent(params.Name, params.Arguments)
	if command == "" {
		command = "tools/call"
	} else {
		command = params.Name + ": " + command
	}
	command = auditutil.Truncate(auditutil.Redact(command))
	return device, params.Name, command, method
}

func (s *Server) auditConcurrencyLimited(r *http.Request, p auth.Principal) {
	if s.Store == nil {
		return
	}
	transport := "rest"
	switch r.URL.Path {
	case "/mcp":
		transport = "mcp-http"
	case "/mcp/sse", "/mcp/message":
		transport = "mcp-sse"
	}
	device, _, command, method := rateLimitIntent(r)
	if command == "" {
		command = "exec"
	}
	s.auditConcurrencyLimitedEntry(auth.ClientIP(r), device, command, transport, method, p)
}

func (s *Server) auditConcurrencyLimitedEntry(clientIP, device, command, transport, method string, p auth.Principal) {
	if s.Store == nil {
		return
	}
	s.ensureTokenConcurrencyLimit(p.QuotaID())
	count := 1
	limit := 2
	inUse := 2
	if s.TokenLimiter != nil {
		shouldLog, n, lim, inu := s.TokenLimiter.concurrencyAuditDecision(p.QuotaID(), time.Now())
		if !shouldLog {
			return
		}
		count = n
		limit = lim
		inUse = inu
	}
	command = auditutil.Truncate(auditutil.Redact(command))
	message := fmt.Sprintf("concurrency_limit (%d rejected since previous audit; 10s throttle; limit=%d in_use=%d)", count, limit, inUse)
	if method != "" {
		message += "; jsonrpc_method=" + method
	}
	targetType, target := "token", p.Name
	if device != "" {
		targetType, target = "device", device
	}
	if err := s.Store.AuditEntry(storage.AuditEntry{CallerIP: clientIP, Device: device, Action: "concurrency_limited", Command: command, ErrorMsg: message, ActorType: p.Type, ActorID: p.ID, ActorName: p.AuditName(), OAuthClientID: p.OAuthClient, Transport: transport, TargetType: targetType, Target: target, Result: "denied"}); err != nil {
		slog.Error("concurrency limit audit write failed", "error", err, "actor", p.AuditName(), "device", device)
	}
}

func (s *Server) rejectTokenExecLimit(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	s.ensureTokenConcurrencyLimit(p.QuotaID())
	s.auditConcurrencyLimited(r, p)
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	limit := 2
	inUse := 2
	if s.TokenLimiter != nil {
		limit = s.TokenLimiter.MaxConcurrentExecForToken(p.QuotaID())
		inUse = s.TokenLimiter.ExecInUse(p.QuotaID())
	}
	resp := map[string]any{
		"error":          "rate_limited",
		"limiter":        "concurrency",
		"limit":          limit,
		"in_use":         inUse,
		"retry_after_ms": 1000,
	}
	body, _ := json.Marshal(resp)
	_, _ = w.Write(body)
}
