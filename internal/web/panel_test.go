package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testRunner struct{}

func (testRunner) DevicesJSON(w http.ResponseWriter, r *http.Request)  {}
func (testRunner) AuditsJSON(w http.ResponseWriter, r *http.Request)   {}
func (testRunner) PanelExec(w http.ResponseWriter, r *http.Request)    {}
func (testRunner) RotateAPIKey(w http.ResponseWriter, r *http.Request) {}
func (testRunner) DeleteDevice(w http.ResponseWriter, r *http.Request) {}

func TestStaticCSSContentType(t *testing.T) {
	p := New("test-password", "admin", testRunner{})
	req := httptest.NewRequest("GET", "/static/app.css", nil)
	rec := httptest.NewRecorder()

	p.static(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("content-type=%q, want text/css; charset=utf-8", got)
	}
	if strings.TrimSpace(rec.Body.String()) == "" {
		t.Fatal("CSS body is empty")
	}
}

func TestCachedHandlerUsesETagAndTwoSecondCache(t *testing.T) {
	p := New("test-password", "admin", testRunner{})
	calls := 0
	h := p.cachedHandler("test-cache", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":1}`))
	})
	request := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/probe/summary", nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	first := request("")
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" {
		t.Fatalf("first response status=%d etag=%q", first.Code, first.Header().Get("ETag"))
	}
	second := request("")
	if second.Code != http.StatusOK || calls != 1 {
		t.Fatalf("cache hit status=%d calls=%d", second.Code, calls)
	}
	third := request(first.Header().Get("ETag"))
	if third.Code != http.StatusNotModified || third.Body.Len() != 0 {
		t.Fatalf("conditional response status=%d body=%q", third.Code, third.Body.String())
	}
	if calls != 1 {
		t.Fatalf("ETag request bypassed cache: calls=%d", calls)
	}

	p.cacheMu.Lock()
	entry := p.cache["test-cache"]
	entry.expiresAt = time.Now().Add(-time.Second)
	p.cache["test-cache"] = entry
	p.cacheMu.Unlock()
	fourth := request("")
	if fourth.Code != http.StatusOK || calls != 2 {
		t.Fatalf("expired cache status=%d calls=%d", fourth.Code, calls)
	}
}

type tokenPatchTestRunner struct {
	testRunner
	method string
	path   string
	body   string
}

func (r *tokenPatchTestRunner) TokenListJSON(http.ResponseWriter, *http.Request)   {}
func (r *tokenPatchTestRunner) TokenCreateJSON(http.ResponseWriter, *http.Request) {}
func (r *tokenPatchTestRunner) TokenPatchJSON(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.method = req.Method
	r.path = req.URL.Path
	r.body = string(b)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}
func (r *tokenPatchTestRunner) TokenRotateJSON(http.ResponseWriter, *http.Request) {}
func (r *tokenPatchTestRunner) TokenRevokeJSON(http.ResponseWriter, *http.Request) {}
func (r *tokenPatchTestRunner) TokenDeleteJSON(http.ResponseWriter, *http.Request) {}

func TestPanelTokenUpdateProxiesExistingPATCHRoute(t *testing.T) {
	runner := &tokenPatchTestRunner{}
	p := New("test-password", "admin", runner)
	p.sessions["valid-session"] = time.Now().Add(time.Hour)
	req := httptest.NewRequest(http.MethodPatch, "/panel/api/tokens/update?id=token-123", strings.NewReader(`{"scopes":["devices:read","exec"],"devices":["node-a"],"rate_rps":10}`))
	req.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-session"})
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if runner.method != http.MethodPatch || runner.path != "/api/v1/tokens/token-123" {
		t.Fatalf("proxy method/path=%s %s", runner.method, runner.path)
	}
	if !strings.Contains(runner.body, `"exec"`) || !strings.Contains(runner.body, `"node-a"`) {
		t.Fatalf("body was not preserved: %s", runner.body)
	}
}
