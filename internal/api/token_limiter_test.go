package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	if actor != "client-one" || result != "denied" || message != "rate_limited" || transport != "mcp" {
		t.Fatalf("audit = actor:%q result:%q message:%q transport:%q", actor, result, message, transport)
	}
}
