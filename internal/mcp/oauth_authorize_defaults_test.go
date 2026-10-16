package mcp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

func authorizeDefaultsRequest(t *testing.T, srv *Server, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+values.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-panel-session"})
	w := httptest.NewRecorder()
	srv.HandleAuthorize(w, req)
	return w
}

func authorizeDefaultsQuery(clientID, redirect, challenge string) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"state":                 {"state-defaults-test"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
}

func TestAuthorizeDefaultsMissingResource(t *testing.T) {
	srv, _, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	parts := strings.SplitN(verifierChallenge, "|", 2)
	q := authorizeDefaultsQuery(clientID, redirect, parts[1])
	q.Set("scope", "devices:read")
	w := authorizeDefaultsRequest(t, srv, q)
	if w.Code != http.StatusOK {
		t.Fatalf("missing resource should enter consent page: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAuthorizeDefaultsMissingScope(t *testing.T) {
	srv, _, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	parts := strings.SplitN(verifierChallenge, "|", 2)
	q := authorizeDefaultsQuery(clientID, redirect, parts[1])
	q.Set("resource", "https://hub.example.test/mcp")
	w := authorizeDefaultsRequest(t, srv, q)
	if w.Code != http.StatusOK {
		t.Fatalf("missing scope should enter consent page: status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "可授予：devices:read, exec") {
		t.Fatalf("candidate scopes must be default scope intersected with token scopes: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "可授予：devices:read, exec, files:") {
		t.Fatalf("candidate exposes scopes absent from token: %s", w.Body.String())
	}
	q.Set("scope", "   ")
	blankScope := authorizeDefaultsRequest(t, srv, q)
	if blankScope.Code != http.StatusOK {
		t.Fatalf("blank scope should use defaults: status=%d body=%s", blankScope.Code, blankScope.Body.String())
	}
}

func TestAuthorizeDefaultsBothMissingAndTokenExchangeResource(t *testing.T) {
	srv, _, clientID, clientSecret, redirect, verifierChallenge := oauthFlowFixture(t)
	parts := strings.SplitN(verifierChallenge, "|", 2)
	verifier, challenge := parts[0], parts[1]
	q := authorizeDefaultsQuery(clientID, redirect, challenge)
	page := authorizeDefaultsRequest(t, srv, q)
	if page.Code != http.StatusOK {
		t.Fatalf("both defaults should enter consent page: status=%d body=%s", page.Code, page.Body.String())
	}
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatal("authorization page missing CSRF nonce")
	}
	form := url.Values{"csrf": {match[1]}, "decision": {"approve"}, "api_token_id": {"api-token-1"}, "scope": {"devices:read", "exec"}}
	post := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-panel-session"})
	consent := httptest.NewRecorder()
	srv.HandleAuthorize(consent, post)
	if consent.Code != http.StatusFound {
		t.Fatalf("consent status=%d body=%s", consent.Code, consent.Body.String())
	}
	callback, err := url.Parse(consent.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := callback.Query().Get("code")
	if code == "" || callback.Query().Get("state") != "state-defaults-test" {
		t.Fatalf("bad callback response: status=%d", consent.Code)
	}
	tokenForm := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "client_id": {clientID}, "client_secret": {clientSecret}}
	tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResp := httptest.NewRecorder()
	srv.HandleToken(tokenResp, tokenReq)
	if tokenResp.Code != http.StatusOK {
		t.Fatalf("token exchange without resource failed: status=%d body=%s", tokenResp.Code, tokenResp.Body.String())
	}
	var got struct {
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Resource != "https://hub.example.test/mcp" {
		t.Fatalf("token resource=%q, want configured resource", got.Resource)
	}
}

func TestAuthorizeRejectsResourceMismatchAndDoesNotLogSecrets(t *testing.T) {
	srv, _, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	challenge := strings.SplitN(verifierChallenge, "|", 2)[1]
	q := authorizeDefaultsQuery(clientID, redirect, challenge)
	q.Set("resource", "https://wrong.example/mcp")
	q.Set("scope", "devices:read")
	q.Set("state", "STATE-MUST-NOT-APPEAR-938471")
	q.Set("code_challenge", challenge)
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	w := authorizeDefaultsRequest(t, srv, q)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "error=invalid_target") {
		t.Fatalf("mismatched resource response status=%d", w.Code)
	}
	out := logs.String()
	for _, want := range []string{"oauth_authorize_rejected", "code=invalid_target", "check=resource_mismatch", "client_id=" + clientID} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q: %s", want, out)
		}
	}
	for _, secret := range []string{"STATE-MUST-NOT-APPEAR-938471", challenge, "wrong.example"} {
		if strings.Contains(out, secret) {
			t.Errorf("sensitive value %q leaked in log", secret)
		}
	}
}

func TestAuthorizeScopeAdminRejectedAndAdminTokenNotListed(t *testing.T) {
	srv, store, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	adminScopes, _ := json.Marshal([]string{"admin"})
	if err := store.CreateAPIToken(storage.APIToken{ID: "admin-only-token", Name: "admin-only-token-name", Prefix: "admintest", TokenHash: storage.HashAPIToken("admin-only-token-secret"), ScopesJSON: string(adminScopes), DevicesJSON: "[]", RateRPS: 10, MaxSessions: 3, CreatedAt: time.Now().Unix(), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	challenge := strings.SplitN(verifierChallenge, "|", 2)[1]
	q := authorizeDefaultsQuery(clientID, redirect, challenge)
	q.Set("resource", "https://hub.example.test/mcp")
	q.Set("scope", "admin")
	bad := authorizeDefaultsRequest(t, srv, q)
	if bad.Code != http.StatusFound || !strings.Contains(bad.Header().Get("Location"), "error=invalid_scope") {
		t.Fatalf("admin scope should be rejected: status=%d", bad.Code)
	}
	q.Del("scope")
	page := authorizeDefaultsRequest(t, srv, q)
	if page.Code != http.StatusOK {
		t.Fatalf("default scope page status=%d body=%s", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), "admin-only-token-name") || strings.Contains(page.Body.String(), "admin-only-token\")") {
		t.Fatal("admin-only token appeared in default-scope consent candidates")
	}
}

func TestAuthorizeMetadataScopesSupportedMatch(t *testing.T) {
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "https://hub.example.test")
	fetch := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		(&Server{}).HandleOAuthMetadata(w, httptest.NewRequest(http.MethodGet, "https://hub.example.test"+path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("metadata %s status=%d", path, w.Code)
		}
		var value map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	as := fetch("/.well-known/oauth-authorization-server")
	pr := fetch("/.well-known/oauth-protected-resource/mcp")
	asScopes, okAS := as["scopes_supported"].([]any)
	prScopes, okPR := pr["scopes_supported"].([]any)
	if !okAS || !okPR || len(asScopes) != len(prScopes) {
		t.Fatalf("scope metadata mismatch: AS=%#v PR=%#v", as["scopes_supported"], pr["scopes_supported"])
	}
	for i := range asScopes {
		if asScopes[i] != prScopes[i] {
			t.Fatalf("scope metadata mismatch: AS=%#v PR=%#v", asScopes, prScopes)
		}
	}
	for _, scope := range asScopes {
		if scope == "admin" {
			t.Fatal("authorization server metadata advertises admin")
		}
	}
}

func TestAuthorizeDefaultsLogDoesNotExposeStateOrChallenge(t *testing.T) {
	srv, _, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	challenge := strings.SplitN(verifierChallenge, "|", 2)[1]
	q := authorizeDefaultsQuery(clientID, redirect, challenge)
	q.Set("resource", "https://wrong.example/mcp")
	q.Set("state", "STATE-UNIQUE-SECRET-111")
	q.Set("code_challenge", challenge)
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	_ = authorizeDefaultsRequest(t, srv, q)
	if strings.Contains(logs.String(), "STATE-UNIQUE-SECRET-111") || strings.Contains(logs.String(), challenge) {
		t.Fatalf("sensitive OAuth parameters leaked: %s", logs.String())
	}
}
