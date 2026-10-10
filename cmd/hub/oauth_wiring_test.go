package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/api"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestMainPanelAssemblyAllowsLoggedInOAuthConsent(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "https://hub.example.test")
	store, err := storage.Open(filepath.Join(t.TempDir(), "hub-oauth-wiring.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureAPITokens(""); err != nil {
		t.Fatal(err)
	}
	redirect := "https://client.example/callback"
	uris, _ := json.Marshal([]string{redirect})
	if err := store.RegisterOAuthClient(storage.OAuthClient{ID: "client", Name: "Test Client", RedirectURIsJSON: string(uris), ClientSecretHash: storage.HashAPIToken("secret"), GrantTypesJSON: `["authorization_code","refresh_token"]`, ResponseTypesJSON: `["code"]`, CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	scopes, _ := json.Marshal([]string{"devices:read"})
	if err := store.CreateAPIToken(storage.APIToken{ID: "named-token", Name: "chatgpt-app", Prefix: "testprefix", TokenHash: storage.HashAPIToken("vpc_testtoken_abcdefghijklmnopqrstuvwxyz012345"), ScopesJSON: string(scopes), DevicesJSON: "[]", RateRPS: 10, MaxSessions: 2, CreatedAt: time.Now().Unix(), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}

	apiServer := &api.Server{Store: store}
	mcpServer := mcp.NewServer(nil, nil)
	mcpServer.OAuthStore = store
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/authorize", mcpServer.HandleAuthorize)
	wirePanel(mux, apiServer, "panel-password", mcpServer)

	login := httptest.NewRequest(http.MethodPost, "/panel/login", strings.NewReader(`{"password":"panel-password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResult := httptest.NewRecorder()
	mux.ServeHTTP(loginResult, login)
	if loginResult.Code != http.StatusNoContent {
		t.Fatalf("login status=%d body=%s", loginResult.Code, loginResult.Body.String())
	}
	cookies := loginResult.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "vpc_session" {
		t.Fatalf("login did not issue session cookie: %#v", cookies)
	}

	verifier := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"response_type": {"code"}, "client_id": {"client"}, "redirect_uri": {redirect}, "state": {"state-main-assembly"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {"https://hub.example.test/mcp"}, "scope": {"devices:read"}}
	authorize := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	authorize.AddCookie(cookies[0])
	consent := httptest.NewRecorder()
	mux.ServeHTTP(consent, authorize)
	if consent.Code != http.StatusOK {
		t.Fatalf("authorize after login status=%d location=%q body=%s", consent.Code, consent.Header().Get("Location"), consent.Body.String())
	}
	if !strings.Contains(consent.Body.String(), "授权请求") || !strings.Contains(consent.Body.String(), "chatgpt-app") {
		t.Fatalf("consent page missing expected content: %s", consent.Body.String())
	}
}
