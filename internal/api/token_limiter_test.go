package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestTokenLimiterIndependentBucketsAndRetryAfter(t *testing.T) {
	limiter := NewTokenLimiter(1)
	now := time.Unix(1000, 0)
	if ok, _ := limiter.Allow("token-a", 1, now); !ok {
		t.Fatal("first token-a request should pass")
	}
	if ok, retry := limiter.Allow("token-a", 1, now); ok || retry < time.Second {
		t.Fatalf("second token-a request = (%v, %s), want limited with >=1s retry", ok, retry)
	}
	if ok, _ := limiter.Allow("token-b", 1, now); !ok {
		t.Fatal("token-b bucket should be independent")
	}
	if ok, _ := limiter.Allow("token-a", 1, now.Add(time.Second)); !ok {
		t.Fatal("token-a should refill after one second")
	}
}

func TestTokenLimiterConcurrentExecIsPerToken(t *testing.T) {
	limiter := NewTokenLimiter(1)
	now := time.Unix(1000, 0)
	releaseA, ok := limiter.AcquireExec("token-a", now)
	if !ok {
		t.Fatal("first token-a exec should acquire slot")
	}
	defer releaseA()
	if _, ok := limiter.AcquireExec("token-a", now); ok {
		t.Fatal("second concurrent token-a exec should be denied")
	}
	releaseB, ok := limiter.AcquireExec("token-b", now)
	if !ok {
		t.Fatal("token-b should have an independent exec slot")
	}
	releaseB()
}

func TestTokenRateLimitMiddlewareReturns429AndRetryAfter(t *testing.T) {
	server := &Server{TokenLimiter: NewTokenLimiter(1)}
	h := server.TokenRateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	principal := auth.Principal{Type: "api_token", ID: "a", Name: "token-a", TokenID: "token-a", RateRPS: 1}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
		r = r.WithContext(auth.WithPrincipal(context.Background(), principal))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", w.Code)
	}
	w := request()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", w.Header().Get("Retry-After"))
	}
}

func TestRateLimitedRequestIsAuditedAsDenied(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{Store: store}
	principal := auth.Principal{Type: "api_token", ID: "token-1", Name: "client-one", TokenID: "token-1"}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
	server.auditRateLimited(req, principal)
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var actor, result, message, transport string
	if err := store.DB.QueryRow("SELECT actor_name,result,error_msg,transport FROM audit_logs ORDER BY id DESC LIMIT 1").Scan(&actor, &result, &message, &transport); err != nil {
		t.Fatal(err)
	}
	if actor != "client-one" || result != "denied" || !strings.HasPrefix(message, "rate_limited (1 rejected") || transport != "mcp-http" {
		t.Fatalf("audit = actor:%q result:%q message:%q transport:%q", actor, result, message, transport)
	}
}

func TestRateLimitAuditCapturesMCPHTTPAndSSEIntent(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	limiter := NewTokenLimiter(1)
	server := &Server{Store: store, TokenLimiter: limiter}
	bodyFor := func(command string) []byte {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "exec_command", "arguments": map[string]any{"device": "node-a", "command": command}}})
		return b
	}
	for _, tc := range []struct{ path, token, transport, command string }{{"/mcp", "http-token", "mcp-http", "exec_command: uname -a"}, {"/mcp/message", "sse-token", "mcp-sse", "exec_command: cat /etc/hostname"}} {
		p := auth.Principal{Type: "api_token", ID: tc.token, Name: tc.token, TokenID: tc.token, RateRPS: 1}
		h := server.TokenRateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
		call := func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(string(bodyFor(strings.TrimPrefix(tc.command, "exec_command: ")))))
			req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}
		if got := call().Code; got != http.StatusOK {
			t.Fatalf("%s first status=%d", tc.path, got)
		}
		if got := call().Code; got != http.StatusTooManyRequests {
			t.Fatalf("%s limited status=%d", tc.path, got)
		}
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ token, transport, command string }{{"http-token", "mcp-http", "exec_command: uname -a"}, {"sse-token", "mcp-sse", "exec_command: cat /etc/hostname"}} {
		var transport, device, command string
		if err := store.DB.QueryRow("SELECT transport,device,command FROM audit_logs WHERE actor_name=? AND action='rate_limited' ORDER BY id DESC LIMIT 1", tc.token).Scan(&transport, &device, &command); err != nil {
			t.Fatal(err)
		}
		if transport != tc.transport || device != "node-a" || command != tc.command {
			t.Fatalf("%s audit transport=%q device=%q command=%q", tc.token, transport, device, command)
		}
	}
	rows, err := store.QueryAudits("", "", "rate_limited", "denied", "mcp-sse", 0, 0, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ActorName != "sse-token" {
		t.Fatalf("transport=mcp-sse filter returned %#v", rows)
	}
}

func TestRateLimitAuditThrottlesPerTokenAndCountsDenials(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	limiter := NewTokenLimiter(1)
	server := &Server{Store: store, TokenLimiter: limiter}
	p := auth.Principal{Type: "api_token", ID: "tok", Name: "tok", TokenID: "tok"}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	server.auditRateLimited(req, p)
	for i := 0; i < 4; i++ {
		server.auditRateLimited(req, p)
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB.QueryRow("SELECT count(*) FROM audit_logs WHERE action='rate_limited'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("wrote %d rows in one ten-second window", count)
	}
	limiter.mu.Lock()
	limiter.states["tok"].rateAuditLast = time.Now().Add(-11 * time.Second)
	limiter.mu.Unlock()
	server.auditRateLimited(req, p)
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var msg string
	if err := store.DB.QueryRow("SELECT error_msg FROM audit_logs WHERE action='rate_limited' ORDER BY id DESC LIMIT 1").Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "5 rejected") {
		t.Fatalf("expected suppressed denial count, got %q", msg)
	}
}

func TestOAuthAndDirectTokenShareRateAndExecQuotas(t *testing.T) {
	server := &Server{TokenLimiter: NewTokenLimiter(1)}
	h := server.TokenRateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	direct := auth.Principal{Type: "api_token", ID: "api-1", Name: "shared-token", TokenID: "api-1", RateRPS: 1}
	oauth := auth.Principal{Type: "api_token", ID: "api-1", Name: "shared-token", TokenID: "oauth:grant-1", QuotaTokenID: "api-1", AuthMethod: "oauth", OAuthClient: "chatgpt-app", RateRPS: 1}
	call := func(p auth.Principal) int {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(auth.WithPrincipal(context.Background(), p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if got := call(direct); got != http.StatusOK {
		t.Fatalf("direct status=%d", got)
	}
	if got := call(oauth); got != http.StatusTooManyRequests {
		t.Fatalf("OAuth bypassed shared rate bucket: status=%d", got)
	}

	release, ok := server.acquireTokenExec(direct)
	if !ok {
		t.Fatal("direct token failed to acquire exec slot")
	}
	defer release()
	if _, ok := server.acquireTokenExec(oauth); ok {
		t.Fatal("OAuth bypassed shared exec concurrency limit")
	}
}

func TestOAuthRateLimitAuditIncludesViaClient(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "oauth-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{Store: store, TokenLimiter: NewTokenLimiter(1)}
	p := auth.Principal{Type: "api_token", ID: "api-1", Name: "chatgpt-app", TokenID: "oauth:grant-1", QuotaTokenID: "api-1", AuthMethod: "oauth", OAuthClient: "chatgpt-client"}
	server.auditRateLimited(httptest.NewRequest(http.MethodPost, "/mcp", nil), p)
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := store.DB.QueryRow("SELECT actor_name FROM audit_logs ORDER BY id DESC LIMIT 1").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != "chatgpt-app via=oauth:chatgpt-client" {
		t.Fatalf("actor=%q", actor)
	}
}
