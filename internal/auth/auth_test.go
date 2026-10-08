package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustProxy(t *testing.T) {
	t.Setenv("VPS_COMMANDER_TRUST_PROXY", "on")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.10" {
		t.Fatalf("clientIP=%q, want 203.0.113.10", got)
	}
}

func TestClientIPIgnoresProxyHeadersWhenDisabled(t *testing.T) {
	t.Setenv("VPS_COMMANDER_TRUST_PROXY", "off")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	if got := clientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP=%q, want 127.0.0.1", got)
	}
}
