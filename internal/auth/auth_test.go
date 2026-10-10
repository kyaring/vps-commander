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

func TestMiddlewareOAuthTokenOnlyAuthenticatesCanonicalMCPResource(t *testing.T) {
	wantResource := "https://hub.example.test/mcp"
	calls := 0
	challenge := `Bearer realm="vps-commander", resource_metadata="https://hub.example.test/.well-known/oauth-protected-resource/mcp"`
	h := (Middleware{
		WWWAuthenticate: challenge,
		OAuthResource:   wantResource,
		OAuthTokenValidator: func(token, resource, ip string, now int64) (Principal, bool) {
			calls++
			if token != "vpc_oat_valid" || resource != wantResource || now <= 0 {
				t.Errorf("validator inputs token=%q resource=%q now=%d", token, resource, now)
			}
			return Principal{Type: "api_token", ID: "api-id", Name: "named-api-token", TokenID: "oauth:grant-id", Scopes: map[string]bool{"mcp:call": true}}, true
		},
	}).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok || p.Name != "named-api-token" || p.TokenID != "oauth:grant-id" {
			t.Errorf("OAuth principal not propagated: %+v, ok=%v", p, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/mcp/sse", "/mcp/message", "/api/v1/devices"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer vpc_oat_valid")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("path %s status=%d, want 401", path, rec.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("OAuth validator called %d times for non-/mcp paths", calls)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer vpc_oat_valid")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("/mcp status=%d validator calls=%d", rec.Code, calls)
	}
}

func TestMiddlewareInvalidBearerAddsInvalidTokenChallenge(t *testing.T) {
	challenge := `Bearer realm="vps-commander", resource_metadata="https://hub.example.test/.well-known/oauth-protected-resource/mcp"`
	h := (Middleware{WWWAuthenticate: challenge}).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name, authorization, want string
	}{
		{"missing token", "", challenge},
		{"invalid token", "Bearer bad-token", `Bearer realm="vps-commander", error="invalid_token", resource_metadata="https://hub.example.test/.well-known/oauth-protected-resource/mcp"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != tc.want {
				t.Fatalf("challenge=%q, want %q", got, tc.want)
			}
		})
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

func TestPrincipalQuotaIDAndAuditName(t *testing.T) {
	p := Principal{TokenID: "oauth:grant-1", QuotaTokenID: "api-1", Name: "chatgpt-app", AuthMethod: "oauth", OAuthClient: "chatgpt-client"}
	if got := p.QuotaID(); got != "api-1" {
		t.Fatalf("QuotaID=%q", got)
	}
	if got := p.AuditName(); got != "chatgpt-app" {
		t.Fatalf("AuditName=%q", got)
	}
	direct := Principal{TokenID: "api-1", Name: "chatgpt-app"}
	if got := direct.QuotaID(); got != "api-1" || direct.AuditName() != "chatgpt-app" {
		t.Fatalf("direct principal identity changed: quota=%q audit=%q", got, direct.AuditName())
	}
}
