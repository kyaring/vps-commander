package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthMetadataDisabledByDefault(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "")
	h := (&Server{}).HandleOAuthMetadata
	r := httptest.NewRequest("GET", "https://attacker.example/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 404 {
		t.Fatalf("status=%d body=%s; want OAuth metadata disabled by default", w.Code, w.Body.String())
	}
}

func TestOAuthMetadataUsesConfiguredPublicURLNotHost(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "https://hub.example.test")
	h := (&Server{}).HandleOAuthMetadata
	r := httptest.NewRequest("GET", "https://attacker.example/.well-known/oauth-authorization-server", nil)
	r.Host = "attacker.example"
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var meta map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["issuer"] != "https://hub.example.test" || meta["authorization_endpoint"] != "https://hub.example.test/oauth/authorize" {
		t.Fatalf("metadata trusted request Host or has wrong endpoint: %#v", meta)
	}
	methods, ok := meta["token_endpoint_auth_methods_supported"].([]any)
	if !ok || len(methods) != 2 || methods[0] != "client_secret_basic" || methods[1] != "client_secret_post" {
		t.Fatalf("client authentication metadata=%#v", meta["token_endpoint_auth_methods_supported"])
	}
}

func TestOAuthProtectedResourceMetadata(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "https://hub.example.test/")
	h := (&Server{}).HandleOAuthMetadata
	r := httptest.NewRequest("GET", "https://hub.example.test/.well-known/oauth-protected-resource/mcp", nil)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var meta map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["resource"] != "https://hub.example.test/mcp" {
		t.Fatalf("resource=%v", meta["resource"])
	}
	issuers, ok := meta["authorization_servers"].([]any)
	if !ok || len(issuers) != 1 || issuers[0] != "https://hub.example.test" {
		t.Fatalf("authorization_servers=%#v", meta["authorization_servers"])
	}
	if scopes, ok := meta["scopes_supported"].([]any); !ok {
		t.Fatalf("scopes_supported=%#v", meta["scopes_supported"])
	} else {
		for _, scope := range scopes {
			if scope == "admin" {
				t.Fatal("resource metadata advertises admin scope")
			}
		}
	}
}

func TestOAuthMetadataRejectsInvalidPublicURL(t *testing.T) {
	for _, raw := range []string{"https://user:pass@hub.example", "https://hub.example/path", "https://hub.example/path?query=1", "https://hub.example/#frag", "http://hub.example"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("VPS_COMMANDER_PUBLIC_URL", raw)
			w := httptest.NewRecorder()
			(&Server{}).HandleOAuthMetadata(w, httptest.NewRequest("GET", "https://hub.example/.well-known/oauth-authorization-server", nil))
			if w.Code != 404 {
				t.Fatalf("invalid public URL %q returned %d", raw, w.Code)
			}
		})
	}
}

func TestOAuthResourceURLMatchesConfiguredPublicOrigin(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "https://hub.example.test/")
	got, ok := OAuthResourceURL()
	if !ok || got != "https://hub.example.test/mcp" {
		t.Fatalf("OAuthResourceURL()=(%q,%v), want configured origin + /mcp", got, ok)
	}
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "")
	if got, ok := OAuthResourceURL(); ok || got != "" {
		t.Fatalf("OAuthResourceURL() while disabled=(%q,%v)", got, ok)
	}
}

func TestOAuthWWWAuthenticateAdvertisesProtectedResourceMetadata(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "https://hub.example.test")
	want := `Bearer realm="vps-commander", resource_metadata="https://hub.example.test/.well-known/oauth-protected-resource/mcp"`
	if got := OAuthWWWAuthenticate(); got != want {
		t.Fatalf("OAuthWWWAuthenticate()=%q, want %q", got, want)
	}
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "")
	if got := OAuthWWWAuthenticate(); got != `Bearer realm="vps-commander"` {
		t.Fatalf("OAuthWWWAuthenticate() while disabled=%q", got)
	}
}

func TestOAuthAuthorizeAndTokenDisabledByDefault(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "")
	s := &Server{}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		method  string
	}{
		{name: "authorize", handler: s.HandleAuthorize, method: "GET"},
		{name: "token", handler: s.HandleToken, method: "POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(tc.method, "https://hub.example.test/oauth/"+tc.name, nil))
			if w.Code != 404 {
				t.Fatalf("OAuth endpoint status=%d, want 404 when public URL is unset", w.Code)
			}
		})
	}
}
