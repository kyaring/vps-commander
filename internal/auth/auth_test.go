package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareUsesConfiguredWWWAuthenticateChallenge(t *testing.T) {
	want := `Bearer realm="vps-commander", resource_metadata="https://hub.example.test/.well-known/oauth-protected-resource/mcp"`
	h := (Middleware{WWWAuthenticate: want}).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != want {
		t.Fatalf("WWW-Authenticate=%q, want %q", got, want)
	}
}

func TestClientIPTrustProxy(t *testing.T) {
	t.Setenv("VPS_COMMANDER_TRUST_PROXY", "on")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.1")
	if got := ClientIP(r); got != "10.0.0.1" {
		t.Fatalf("ClientIP=%q, want 10.0.0.1", got)
	}
}

func TestClientIPIgnoresSpoofableXRealIPAndUsesRightmostXFF(t *testing.T) {
	t.Setenv("VPS_COMMANDER_TRUST_PROXY", "on")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Real-IP", "198.51.100.99") // untrusted client-supplied value
	r.Header.Set("X-Forwarded-For", "203.0.113.10, 192.0.2.25")
	if got := ClientIP(r); got != "192.0.2.25" {
		t.Fatalf("ClientIP=%q, want rightmost XFF 192.0.2.25", got)
	}
}

func TestClientIPIgnoresProxyHeadersWhenDisabled(t *testing.T) {
	t.Setenv("VPS_COMMANDER_TRUST_PROXY", "off")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	if got := ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP=%q, want 127.0.0.1", got)
	}
}
