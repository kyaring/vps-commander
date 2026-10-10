package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

func oauthFlowFixture(t *testing.T) (*Server, *storage.Store, string, string, string, string) {
	t.Helper()
	t.Setenv("VPS_COMMANDER_PUBLIC_URL", "https://hub.example.test")
	store, err := storage.Open(filepath.Join(t.TempDir(), "oauth-flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureAPITokens(""); err != nil {
		t.Fatal(err)
	}
	redirect := "https://client.example/callback"
	clientID, clientSecret := "client-flow", "secret-flow"
	uris, _ := json.Marshal([]string{redirect})
	if err := store.RegisterOAuthClient(storage.OAuthClient{ID: clientID, Name: "ChatGPT Test", RedirectURIsJSON: string(uris), ClientSecretHash: storage.HashAPIToken(clientSecret), GrantTypesJSON: `["authorization_code","refresh_token"]`, ResponseTypesJSON: `["code"]`, CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	apiRaw := "vpc_tokenflow_abcdefghijklmnopqrstuvwxyz012345"
	scopes, _ := json.Marshal([]string{"devices:read", "exec"})
	if err := store.CreateAPIToken(storage.APIToken{ID: "api-token-1", Name: "chatgpt-app", Prefix: "tokenflow", TokenHash: storage.HashAPIToken(apiRaw), ScopesJSON: string(scopes), DevicesJSON: "[]", RateRPS: 10, MaxSessions: 3, CreatedAt: time.Now().Unix(), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(&mockBackend{}, nil)
	srv.OAuthStore = store
	srv.OAuthPanelSessionValid = func(r *http.Request) bool {
		c, err := r.Cookie("vpc_session")
		return err == nil && c.Value == "valid-panel-session"
	}
	verifier := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return srv, store, clientID, clientSecret, redirect, verifier + "|" + challenge
}

func TestOAuthAuthorizationCodePKCERefreshAndReplay(t *testing.T) {
	srv, store, clientID, clientSecret, redirect, verifierChallenge := oauthFlowFixture(t)
	parts := strings.SplitN(verifierChallenge, "|", 2)
	verifier, challenge := parts[0], parts[1]
	resource := "https://hub.example.test/mcp"
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"state-123"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {resource}, "scope": {"devices:read exec"}}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-panel-session"})
	w := httptest.NewRecorder()
	srv.HandleAuthorize(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authorize status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("authorization page missing anti-framing headers")
	}
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal("authorization page missing CSRF nonce")
	}
	form := url.Values{"csrf": {match[1]}, "decision": {"approve"}, "api_token_id": {"api-token-1"}, "scope": {"devices:read", "exec"}}
	post := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-panel-session"})
	pw := httptest.NewRecorder()
	srv.HandleAuthorize(pw, post)
	if pw.Code != http.StatusFound {
		t.Fatalf("consent status=%d body=%s", pw.Code, pw.Body.String())
	}
	callback, err := url.Parse(pw.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := callback.Query().Get("code")
	if code == "" || callback.Query().Get("state") != "state-123" || callback.Scheme+"://"+callback.Host+callback.Path != redirect {
		t.Fatalf("bad callback URL: %s", callback)
	}

	tokenCall := func(values url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth(clientID, clientSecret)
		out := httptest.NewRecorder()
		srv.HandleToken(out, r)
		return out
	}
	badResource := tokenCall(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {"https://other.example.test/mcp"}})
	if badResource.Code != http.StatusBadRequest || !strings.Contains(badResource.Body.String(), "invalid_target") || badResource.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("wrong resource status=%d body=%s cache=%q", badResource.Code, badResource.Body.String(), badResource.Header().Get("Cache-Control"))
	}
	bad := tokenCall(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {"wrong-verifier-value-that-is-long-enough-for-pkce-123"}})
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "invalid_grant") {
		t.Fatalf("bad PKCE status=%d body=%s", bad.Code, bad.Body.String())
	}
	exchange := tokenCall(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if exchange.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", exchange.Code, exchange.Body.String())
	}
	var first struct {
		Access   string `json:"access_token"`
		Refresh  string `json:"refresh_token"`
		Scope    string `json:"scope"`
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(exchange.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.Access, "vpc_oat_") || !strings.HasPrefix(first.Refresh, "vpc_ort_") || first.Scope != "devices:read exec" || first.Resource != resource {
		t.Fatalf("unexpected token response: %+v", first)
	}
	if _, ok := store.ValidateOAuthAccessToken(first.Access, resource, "127.0.0.1", time.Now().Unix()); !ok {
		t.Fatal("issued access token failed resource validation")
	}

	rotated := tokenCall(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.Refresh}})
	if rotated.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rotated.Code, rotated.Body.String())
	}
	var second struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.Access == first.Access || second.Refresh == first.Refresh {
		t.Fatal("refresh rotation did not replace both tokens")
	}
	replay := tokenCall(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.Refresh}})
	if replay.Code != http.StatusBadRequest || !strings.Contains(replay.Body.String(), "invalid_grant") {
		t.Fatalf("refresh replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if _, ok := store.ValidateOAuthAccessToken(second.Access, resource, "127.0.0.1", time.Now().Unix()); ok {
		t.Fatal("refresh replay did not revoke grant family")
	}
}

func TestOAuthAuthorizeRequiresPanelSessionAndRejectsBadRedirect(t *testing.T) {
	srv, _, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	parts := strings.SplitN(verifierChallenge, "|", 2)
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"s"}, "code_challenge": {parts[1]}, "code_challenge_method": {"S256"}, "resource": {"https://hub.example.test/mcp"}, "scope": {"devices:read"}}
	unauth := httptest.NewRecorder()
	srv.HandleAuthorize(unauth, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	if unauth.Code != http.StatusFound || !strings.Contains(unauth.Header().Get("Location"), "/panel/login?next=") {
		t.Fatalf("unauth authorize response=%d location=%q", unauth.Code, unauth.Header().Get("Location"))
	}
	q.Set("redirect_uri", "https://attacker.example/steal")
	bad := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-panel-session"})
	srv.HandleAuthorize(bad, req)
	if bad.Code != http.StatusBadRequest || bad.Header().Get("Location") != "" {
		t.Fatalf("bad redirect was not rejected locally: %d %q", bad.Code, bad.Header().Get("Location"))
	}
}

func TestOAuthTokenEndpointRejectsMixedClientAuthentication(t *testing.T) {
	srv, _, clientID, clientSecret, _, _ := oauthFlowFixture(t)
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"not-a-token"}, "client_id": {clientID}, "client_secret": {clientSecret}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	w := httptest.NewRecorder()
	srv.HandleToken(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_request") {
		t.Fatalf("mixed auth status=%d body=%s", w.Code, w.Body.String())
	}
	postOnly := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"not-a-token"}, "client_id": {clientID}, "client_secret": {clientSecret}}
	req = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(postOnly.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	w = httptest.NewRecorder()
	srv.HandleToken(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("client_secret_post was not accepted: status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, "wrong-secret")
	w = httptest.NewRecorder()
	srv.HandleToken(w, req)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "invalid_client") || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("invalid client auth status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestOAuthConsentDenialAndCSRFReplay(t *testing.T) {
	srv, store, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	parts := strings.SplitN(verifierChallenge, "|", 2)
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"deny-state"}, "code_challenge": {parts[1]}, "code_challenge_method": {"S256"}, "resource": {"https://hub.example.test/mcp"}, "scope": {"devices:read"}}
	cookie := &http.Cookie{Name: "vpc_session", Value: "valid-panel-session"}
	get := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	get.AddCookie(cookie)
	gw := httptest.NewRecorder()
	srv.HandleAuthorize(gw, get)
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(gw.Body.String())
	if len(csrf) != 2 {
		t.Fatalf("missing CSRF field: %s", gw.Body.String())
	}
	form := url.Values{"csrf": {csrf[1]}, "decision": {"deny"}}
	post := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	pw := httptest.NewRecorder()
	srv.HandleAuthorize(pw, post)
	if pw.Code != http.StatusFound {
		t.Fatalf("deny status=%d body=%s", pw.Code, pw.Body.String())
	}
	cb, _ := url.Parse(pw.Header().Get("Location"))
	if cb.Query().Get("error") != "access_denied" || cb.Query().Get("state") != "deny-state" {
		t.Fatalf("deny callback=%s", cb)
	}
	replayReq := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	replayReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replayReq.AddCookie(cookie)
	replay := httptest.NewRecorder()
	srv.HandleAuthorize(replay, replayReq)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("CSRF replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	grants, err := store.ListOAuthGrants()
	if err != nil || len(grants) != 0 {
		t.Fatalf("denial created grant(s): %+v err=%v", grants, err)
	}
}
