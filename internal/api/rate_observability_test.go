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

func TestConcurrencyDenialAudited(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "concurrency-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	limiter := NewTokenLimiter(1)
	server := &Server{Store: store, TokenLimiter: limiter}
	p := auth.Principal{Type: "api_token", ID: "tok-concur", Name: "tok-concur", TokenID: "tok-concur"}

	release, ok := server.acquireTokenExec(p)
	if !ok {
		t.Fatal("first acquireTokenExec failed")
	}
	defer release()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(`{"command":"echo test"}`))
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	server.rejectTokenExecLimit(rec, req, p)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}

	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}

	var result, errorMsg string
	if err := store.DB.QueryRow("SELECT result, error_msg FROM audit_logs WHERE actor_id=? AND action='rate_limited' ORDER BY id DESC LIMIT 1", "tok-concur").Scan(&result, &errorMsg); err != nil {
		t.Fatalf("audit log query failed: %v", err)
	}
	if result != "denied" {
		t.Fatalf("result = %q, want denied", result)
	}
	if !strings.Contains(errorMsg, "concurrency_limit") {
		t.Fatalf("error_msg %q does not contain concurrency_limit", errorMsg)
	}
	if !strings.Contains(errorMsg, "limit=") || !strings.Contains(errorMsg, "in_use=") {
		t.Fatalf("error_msg %q does not contain limit or in_use", errorMsg)
	}
}

func TestConcurrencyDenialAuditThrottled(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "concurrency-throttle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	limiter := NewTokenLimiter(1)
	server := &Server{Store: store, TokenLimiter: limiter}
	p := auth.Principal{Type: "api_token", ID: "tok-throttle", Name: "tok-throttle", TokenID: "tok-throttle"}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", nil)
	req = req.WithContext(auth.WithPrincipal(context.Background(), p))

	for i := 0; i < 5; i++ {
		server.auditConcurrencyLimited(req, p)
	}

	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := store.DB.QueryRow("SELECT count(*) FROM audit_logs WHERE actor_id='tok-throttle' AND error_msg LIKE '%concurrency_limit%'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrency audit rows in 10s window = %d, want 1", count)
	}

	limiter.mu.Lock()
	limiter.states["tok-throttle"].concurrencyAuditLast = time.Now().Add(-11 * time.Second)
	limiter.mu.Unlock()

	server.auditConcurrencyLimited(req, p)

	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}

	if err := store.DB.QueryRow("SELECT count(*) FROM audit_logs WHERE actor_id='tok-throttle' AND error_msg LIKE '%concurrency_limit%'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("concurrency audit rows after throttle reset = %d, want 2", count)
	}
}

func TestRateLimitBodyStructured(t *testing.T) {
	server := &Server{TokenLimiter: NewTokenLimiter(1)}
	h := server.TokenRateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	p := auth.Principal{Type: "api_token", ID: "tok-rate", Name: "tok-rate", TokenID: "tok-rate", RateRPS: 1}

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("first request code = %d, want 200", rec.Code)
	}

	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request code = %d, want 429", rec.Code)
	}

	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("missing Retry-After header on rate limit 429")
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal rate 429 body failed: %v", err)
	}

	if body["error"] != "rate_limited" {
		t.Fatalf("body.error = %v, want rate_limited", body["error"])
	}
	if body["limiter"] != "rate" {
		t.Fatalf("body.limiter = %v, want rate", body["limiter"])
	}
	if body["limit"] == nil || int(body["limit"].(float64)) != 1 {
		t.Fatalf("body.limit = %v, want 1", body["limit"])
	}
	if body["retry_after_ms"] == nil {
		t.Fatal("body.retry_after_ms is missing")
	}
}

func TestConcurrencyLimitBodyStructured(t *testing.T) {
	server := &Server{TokenLimiter: NewTokenLimiter(1)}
	p := auth.Principal{Type: "api_token", ID: "tok-concur-body", Name: "tok-concur-body", TokenID: "tok-concur-body"}

	release, ok := server.acquireTokenExec(p)
	if !ok {
		t.Fatal("first acquireTokenExec failed")
	}
	defer release()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	server.rejectTokenExecLimit(rec, req, p)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rec.Code)
	}

	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("missing Retry-After header on concurrency 429")
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal concurrency 429 body failed: %v", err)
	}

	if body["error"] != "rate_limited" {
		t.Fatalf("body.error = %v, want rate_limited", body["error"])
	}
	if body["limiter"] != "concurrency" {
		t.Fatalf("body.limiter = %v, want concurrency", body["limiter"])
	}
	if body["limit"] == nil || int(body["limit"].(float64)) != 1 {
		t.Fatalf("body.limit = %v, want 1", body["limit"])
	}
	if body["in_use"] == nil || int(body["in_use"].(float64)) != 1 {
		t.Fatalf("body.in_use = %v, want 1", body["in_use"])
	}
}

func TestExecInflightListsRunningJobs(t *testing.T) {
	server := &Server{}
	jobsDir := filepath.Join(t.TempDir(), "jobs")
	jobs, err := newExecJobs(jobsDir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server.jobs = jobs

	p := auth.Principal{Type: "api_token", ID: "tok-inflight", Name: "tok-inflight", TokenID: "tok-inflight"}

	jobID := "job-long-1"
	jobs.mu.Lock()
	jobs.jobs[jobID] = &execJob{
		ID:      jobID,
		Owner:   "tok-inflight",
		Status:  "running",
		Started: time.Now().Add(-5 * time.Second),
		command: "sleep 100 --arg1=foo --arg2=bar",
		Summary: "sleep 100",
	}
	jobs.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tokens/tok-inflight/exec-inflight", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var items []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal inflight jobs failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0]["job_id"] != jobID {
		t.Fatalf("job_id = %v, want %s", items[0]["job_id"], jobID)
	}
	if items[0]["started_at"] == "" {
		t.Fatal("started_at is empty")
	}
	summary, _ := items[0]["command_summary"].(string)
	if !strings.HasPrefix(summary, "sleep 100") {
		t.Fatalf("command_summary = %q, want prefix 'sleep 100'", summary)
	}
	if len([]rune(summary)) > 80 {
		t.Fatalf("command_summary runes = %d, must be <= 80", len([]rune(summary)))
	}

	// Mark job ended
	jobs.mu.Lock()
	jobs.jobs[jobID].Status = "exited"
	jobs.mu.Unlock()

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/tokens/tok-inflight/exec-inflight", nil)
	req2 = req2.WithContext(auth.WithPrincipal(req2.Context(), p))
	rec2 := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status after job end = %d, want 200", rec2.Code)
	}

	var items2 []map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &items2); err != nil {
		t.Fatalf("unmarshal inflight jobs after end failed: %v", err)
	}
	if len(items2) != 0 {
		t.Fatalf("len(items2) = %d, want 0 after job completion", len(items2))
	}
}

func TestExecInflightAuthorization(t *testing.T) {
	server := &Server{}
	jobsDir := filepath.Join(t.TempDir(), "jobs")
	jobs, err := newExecJobs(jobsDir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server.jobs = jobs

	pSelf := auth.Principal{Type: "api_token", ID: "tok-self", Name: "tok-self", TokenID: "tok-self"}
	pOther := auth.Principal{Type: "api_token", ID: "tok-other", Name: "tok-other", TokenID: "tok-other"}
	pAdmin := auth.Principal{Type: "api_token", ID: "tok-admin", Name: "tok-admin", TokenID: "tok-admin", Scopes: map[string]bool{"admin": true}}

	call := func(p auth.Principal) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tokens/tok-self/exec-inflight", nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call(pOther); code != http.StatusForbidden {
		t.Fatalf("other token code = %d, want 403", code)
	}
	if code := call(pSelf); code != http.StatusOK {
		t.Fatalf("self token code = %d, want 200", code)
	}
	if code := call(pAdmin); code != http.StatusOK {
		t.Fatalf("admin token code = %d, want 200", code)
	}
}

func TestDecisionsUnchanged(t *testing.T) {
	now := time.Now()

	limiter := NewTokenLimiter(2)
	release1, ok1 := limiter.AcquireExec("tok-test", now)
	if !ok1 {
		t.Fatal("first acquire should succeed")
	}
	defer release1()

	release2, ok2 := limiter.AcquireExec("tok-test", now)
	if !ok2 {
		t.Fatal("second acquire should succeed")
	}

	_, ok3 := limiter.AcquireExec("tok-test", now)
	if ok3 {
		t.Fatal("third acquire should fail when maxConcurrentExec is 2")
	}

	release2()
	release4, ok4 := limiter.AcquireExec("tok-test", now)
	if !ok4 {
		t.Fatal("acquire after release should succeed")
	}
	defer release4()

	// Rate limiter decisions unchanged
	rateLimiter := NewTokenLimiter(1)
	allow1, _ := rateLimiter.Allow("tok-rate", 1, now)
	if !allow1 {
		t.Fatal("first allow should succeed")
	}
	allow2, wait2 := rateLimiter.Allow("tok-rate", 1, now)
	if allow2 {
		t.Fatal("second allow in same second should be rate limited")
	}
	if wait2 <= 0 {
		t.Fatalf("wait duration = %v, want > 0", wait2)
	}
}
