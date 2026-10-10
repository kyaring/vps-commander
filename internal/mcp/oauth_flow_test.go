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

func issueOAuthCode(t *testing.T, srv *Server, clientID, redirect, challenge, state string) string {
	t.Helper()
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {"https://hub.example.test/mcp"}, "scope": {"devices:read exec"}}
	get := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	get.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-panel-session"})
	page := httptest.NewRecorder()
	srv.HandleAuthorize(page, get)
	if page.Code != http.StatusOK {
		t.Fatalf("authorize status=%d body=%s", page.Code, page.Body.String())
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
	if code == "" || callback.Query().Get("state") != state {
		t.Fatalf("bad callback: %s", callback)
	}
	return code
}

func TestOAuthAuthorizationCodePKCERefreshAndReplay(t *testing.T) {
	srv, store, clientID, clientSecret, redirect, verifierChallenge := oauthFlowFixture(t)
	parts := strings.SplitN(verifierChallenge, "|", 2)
	verifier, challenge := parts[0], parts[1]
	resource := "https://hub.example.test/mcp"
	code := issueOAuthCode(t, srv, clientID, redirect, challenge, "state-123")

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
	if _, ok := store.ValidateOAuthAccessToken(first.Access, resource, "127.0.0.1", time.Now().Unix()); ok {
		t.Fatal("old access token remained valid after refresh rotation")
	}
	if _, ok := store.ValidateOAuthAccessToken(second.Access, resource, "127.0.0.1", time.Now().Unix()); !ok {
		t.Fatal("replacement access token is invalid after refresh rotation")
	}
	replay := tokenCall(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.Refresh}})
	if replay.Code != http.StatusBadRequest || !strings.Contains(replay.Body.String(), "invalid_grant") {
		t.Fatalf("refresh replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if _, ok := store.ValidateOAuthAccessToken(second.Access, resource, "127.0.0.1", time.Now().Unix()); ok {
		t.Fatal("refresh replay did not revoke grant family")
	}

	// A failed PKCE attempt consumes the code; a later correct verifier cannot redeem it.
	badCode := issueOAuthCode(t, srv, clientID, redirect, challenge, "state-bad-pkce")
	bad := tokenCall(url.Values{"grant_type": {"authorization_code"}, "code": {badCode}, "redirect_uri": {redirect}, "code_verifier": {"wrong-verifier-value-that-is-long-enough-for-pkce-123"}})
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "PKCE verification failed") {
		t.Fatalf("bad PKCE status=%d body=%s", bad.Code, bad.Body.String())
	}
	usedAfterFailure := tokenCall(url.Values{"grant_type": {"authorization_code"}, "code": {badCode}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if usedAfterFailure.Code != http.StatusBadRequest || !strings.Contains(usedAfterFailure.Body.String(), "invalid_grant") {
		t.Fatalf("PKCE-failed code remained redeemable: status=%d body=%s", usedAfterFailure.Code, usedAfterFailure.Body.String())
	}

	// Redeeming a code and then replaying it revokes the grant family created by it.
	replayCode := issueOAuthCode(t, srv, clientID, redirect, challenge, "state-code-replay")
	replayExchange := tokenCall(url.Values{"grant_type": {"authorization_code"}, "code": {replayCode}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if replayExchange.Code != http.StatusOK {
		t.Fatalf("replay fixture exchange status=%d body=%s", replayExchange.Code, replayExchange.Body.String())
	}
	var replayTokens struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(replayExchange.Body.Bytes(), &replayTokens); err != nil {
		t.Fatal(err)
	}
	replayedCode := tokenCall(url.Values{"grant_type": {"authorization_code"}, "code": {replayCode}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if replayedCode.Code != http.StatusBadRequest || !strings.Contains(replayedCode.Body.String(), "invalid_grant") {
		t.Fatalf("authorization code replay status=%d body=%s", replayedCode.Code, replayedCode.Body.String())
	}
	if _, ok := store.ValidateOAuthAccessToken(replayTokens.Access, resource, "127.0.0.1", time.Now().Unix()); ok {
		t.Fatal("authorization-code replay did not revoke grant family")
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

func TestOAuthConsentOmitsAdminCapableTokens(t *testing.T) {
	srv, store, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	adminScopes, _ := json.Marshal([]string{"admin", "devices:read"})
	if err := store.CreateAPIToken(storage.APIToken{ID: "admin-token", Name: "admin-capable-token", Prefix: "admintest", TokenHash: storage.HashAPIToken("vpc_admin_test_token_abcdefghijklmnopqrstuvwxyz"), ScopesJSON: string(adminScopes), DevicesJSON: "[]", RateRPS: 10, MaxSessions: 2, CreatedAt: time.Now().Unix() + 1, CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	challenge := strings.SplitN(verifierChallenge, "|", 2)[1]
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"admin-filter"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {"https://hub.example.test/mcp"}, "scope": {"devices:read"}}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "vpc_session", Value: "valid-panel-session"})
	w := httptest.NewRecorder()
	srv.HandleAuthorize(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authorize status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "admin-capable-token") {
		t.Fatal("consent picker exposed a token with admin scope")
	}
	if !strings.Contains(w.Body.String(), "chatgpt-app") {
		t.Fatal("consent picker omitted eligible non-admin named token")
	}
}

func TestOAuthConsentRejectsForgedAdminTokenSelection(t *testing.T) {
	srv, store, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	adminScopes, _ := json.Marshal([]string{"admin", "devices:read"})
	if err := store.CreateAPIToken(storage.APIToken{ID: "admin-token", Name: "admin-capable-token", Prefix: "admintest", TokenHash: storage.HashAPIToken("vpc_admin_test_token_abcdefghijklmnopqrstuvwxyz"), ScopesJSON: string(adminScopes), DevicesJSON: "[]", RateRPS: 10, MaxSessions: 2, CreatedAt: time.Now().Unix() + 1, CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	challenge := strings.SplitN(verifierChallenge, "|", 2)[1]
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"forged-admin-selection"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {"https://hub.example.test/mcp"}, "scope": {"devices:read"}}
	get := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	cookie := &http.Cookie{Name: "vpc_session", Value: "valid-panel-session"}
	get.AddCookie(cookie)
	page := httptest.NewRecorder()
	srv.HandleAuthorize(page, get)
	if page.Code != http.StatusOK {
		t.Fatalf("authorize page status=%d body=%s", page.Code, page.Body.String())
	}
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatal("authorization page missing CSRF nonce")
	}

	// Deliberately forge the POST to select an admin-capable token that the
	// consent picker does not render. The server must reject it independently.
	form := url.Values{"csrf": {match[1]}, "decision": {"approve"}, "api_token_id": {"admin-token"}, "scope": {"devices:read"}}
	post := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	result := httptest.NewRecorder()
	srv.HandleAuthorize(result, post)
	if result.Code != http.StatusForbidden {
		t.Fatalf("forged admin-token approval status=%d body=%s", result.Code, result.Body.String())
	}
	grants, err := store.ListOAuthGrants()
	if err != nil || len(grants) != 0 {
		t.Fatalf("forged admin-token approval created grant(s): %+v err=%v", grants, err)
	}
}
