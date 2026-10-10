package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wjyhk/vps-commander/internal/storage"
)

type pendingOAuthRequest struct {
	ClientID, ClientName, RedirectURI, State, Challenge, Resource, SessionHash string
	Requested                                                                  []string
	CreatedAt                                                                  time.Time
}
type authorizeTokenOption struct {
	ID, Name string
	Scopes   []string
}

var authorizePage = template.Must(template.New("oauth-authorize").Funcs(template.FuncMap{"join": strings.Join}).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>授权 {{.ClientName}} · VPS-Commander</title><meta name="referrer" content="no-referrer">
<style>body{font:16px system-ui,sans-serif;max-width:760px;margin:32px auto;padding:0 18px;background:#111827;color:#e5e7eb}main{border:1px solid #374151;border-radius:14px;padding:24px}label{display:block;margin:12px 0}button{padding:10px 16px;margin-right:8px;cursor:pointer}select{padding:9px;max-width:100%;width:100%}.muted{color:#9ca3af}.danger{background:#7f1d1d;color:white;border:0}</style></head>
<body><main><h1>授权请求</h1><p><strong>{{.ClientName}}</strong> 请求访问 VPS-Commander。</p><p class="muted">只授予你选择的命名令牌权限。不要选择包含超出预期权限的令牌。</p>
<form method="post" action="/oauth/authorize"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label>使用哪个命名令牌？<select name="api_token_id" required>{{range .Tokens}}<option value="{{.ID}}">{{.Name}}（可授予：{{join .Scopes ", "}}）</option>{{end}}</select></label>
<fieldset><legend>本次授权范围</legend>{{range .Requested}}<label><input type="checkbox" name="scope" value="{{.}}"> <code>{{.}}</code></label>{{end}}</fieldset>
<p class="muted">访问令牌绑定到本次 OAuth 客户端和资源，并且不会获得所选命名令牌以外的权限。</p>
<button type="submit" name="decision" value="approve">批准授权</button><button class="danger" type="submit" name="decision" value="deny">拒绝</button></form></main></body></html>`))

var oauthScopes = map[string]bool{"devices:read": true, "files:read": true, "files:write": true, "exec": true, "process": true, "mcp:call": true}

func (s *Server) setOAuthBrowserHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func randomOAuthValue(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func randomOAuthID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}
func cookieSessionHash(r *http.Request) string {
	c, err := r.Cookie("vpc_session")
	if err != nil || c.Value == "" {
		return ""
	}
	b := sha256.Sum256([]byte(c.Value))
	return hex.EncodeToString(b[:])
}
func parseOAuthScopes(raw string) ([]string, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 || len(fields) > len(oauthScopes) {
		return nil, errors.New("scope required")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, v := range fields {
		if !oauthScopes[v] || seen[v] {
			return nil, errors.New("unsupported scope")
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}
func oauthClientAllows(c storage.OAuthClient, field, want string) bool {
	raw := c.GrantTypesJSON
	if field == "response_type" {
		raw = c.ResponseTypesJSON
	}
	var values []string
	if json.Unmarshal([]byte(raw), &values) != nil {
		return false
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Server) oauthClientAndRedirect(id, redirect string) (storage.OAuthClient, bool) {
	if s.OAuthStore == nil || id == "" || redirect == "" {
		return storage.OAuthClient{}, false
	}
	c, err := s.OAuthStore.GetOAuthClient(id)
	if err != nil || c.DisabledAt != nil || !oauthClientAllows(c, "grant_type", "authorization_code") || !oauthClientAllows(c, "response_type", "code") {
		return storage.OAuthClient{}, false
	}
	var list []string
	if json.Unmarshal([]byte(c.RedirectURIsJSON), &list) != nil {
		return storage.OAuthClient{}, false
	}
	for _, v := range list {
		if v == redirect {
			return c, true
		}
	}
	return storage.OAuthClient{}, false
}
func (s *Server) oauthRedirectError(w http.ResponseWriter, redirect, state, code string) {
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "invalid redirect URI", 400)
		return
	}
	q := u.Query()
	q.Set("error", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Location", u.String())
	w.WriteHeader(http.StatusFound)
}
func (s *Server) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	s.setOAuthBrowserHeaders(w)
	if _, enabled := oauthPublicURL(); !enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.OAuthStore == nil {
		http.Error(w, "OAuth storage unavailable", 503)
		return
	}
	if s.OAuthPanelSessionValid == nil || !s.OAuthPanelSessionValid(r) {
		next := "/oauth/authorize?" + r.URL.Query().Encode()
		http.Redirect(w, r, "/panel/login?next="+url.QueryEscape(next), 302)
		return
	}
	if r.Method == http.MethodGet {
		s.beginOAuthAuthorization(w, r)
		return
	}
	s.finishOAuthAuthorization(w, r)
}
func hasDuplicateParams(values url.Values, names ...string) bool {
	for _, name := range names {
		if len(values[name]) > 1 {
			return true
		}
	}
	return false
}

func (s *Server) beginOAuthAuthorization(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, valid := s.oauthClientAndRedirect(q.Get("client_id"), q.Get("redirect_uri"))
	if !valid {
		http.Error(w, "invalid OAuth client or redirect_uri", 400)
		return
	}
	state := q.Get("state")
	fail := func(code string) { s.oauthRedirectError(w, q.Get("redirect_uri"), state, code) }
	if hasDuplicateParams(q, "response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "resource", "scope") {
		fail("invalid_request")
		return
	}
	if state == "" || len(state) > 1024 || !utf8.ValidString(state) || q.Get("response_type") != "code" {
		fail("invalid_request")
		return
	}
	challenge := q.Get("code_challenge")
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	if q.Get("code_challenge_method") != "S256" || err != nil || len(decoded) != sha256.Size || len(challenge) != 43 {
		fail("invalid_request")
		return
	}
	resource, enabled := OAuthResourceURL()
	if !enabled || q.Get("resource") != resource {
		fail("invalid_target")
		return
	}
	requested, err := parseOAuthScopes(q.Get("scope"))
	if err != nil {
		fail("invalid_scope")
		return
	}
	tokens, err := s.OAuthStore.ListAPITokens()
	if err != nil {
		http.Error(w, "unable to list named tokens", 500)
		return
	}
	options := []authorizeTokenOption{}
	for _, token := range tokens {
		if token.IsLegacy || token.RevokedAt != nil || (token.ExpiresAt != nil && *token.ExpiresAt <= time.Now().Unix()) {
			continue
		}
		var scopes []string
		if json.Unmarshal([]byte(token.ScopesJSON), &scopes) != nil {
			continue
		}
		set := map[string]bool{}
		for _, v := range scopes {
			set[v] = true
		}
		// Admin-capable tokens must never appear in the OAuth consent picker.
		if set["admin"] {
			continue
		}
		allowed := []string{}
		for _, v := range requested {
			if set[v] {
				allowed = append(allowed, v)
			}
		}
		if len(allowed) > 0 {
			options = append(options, authorizeTokenOption{ID: token.ID, Name: token.Name, Scopes: allowed})
		}
	}
	if len(options) == 0 {
		http.Error(w, "no active named token grants any requested scope", 403)
		return
	}
	sort.Strings(requested)
	csrf, err := randomOAuthValue("", 32)
	if err != nil {
		http.Error(w, "authorization setup failed", 500)
		return
	}
	pending := pendingOAuthRequest{ClientID: client.ID, ClientName: client.Name, RedirectURI: q.Get("redirect_uri"), State: state, Challenge: challenge, Resource: resource, Requested: requested, SessionHash: cookieSessionHash(r), CreatedAt: time.Now()}
	if pending.SessionHash == "" {
		http.Error(w, "panel session required", 401)
		return
	}
	s.oauthMu.Lock()
	for k, old := range s.oauthPending {
		if time.Since(old.CreatedAt) > 10*time.Minute {
			delete(s.oauthPending, k)
		}
	}
	s.oauthPending[csrf] = pending
	s.oauthMu.Unlock()
	data := struct {
		ClientName, CSRF string
		Tokens           []authorizeTokenOption
		Requested        []string
	}{client.Name, csrf, options, requested}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = authorizePage.Execute(w, data)
}
func (s *Server) finishOAuthAuthorization(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	if hasDuplicateParams(r.PostForm, "csrf", "decision", "api_token_id") {
		http.Error(w, "duplicate form parameters are not allowed", http.StatusBadRequest)
		return
	}
	csrf := r.PostForm.Get("csrf")
	s.oauthMu.Lock()
	pending, ok := s.oauthPending[csrf]
	if ok {
		delete(s.oauthPending, csrf)
	}
	s.oauthMu.Unlock()
	if !ok || csrf == "" || time.Since(pending.CreatedAt) > 10*time.Minute || pending.SessionHash == "" || subtle.ConstantTimeCompare([]byte(pending.SessionHash), []byte(cookieSessionHash(r))) != 1 {
		http.Error(w, "authorization request expired or invalid; restart authorization", 400)
		return
	}
	if r.PostForm.Get("decision") == "deny" {
		s.oauthRedirectError(w, pending.RedirectURI, pending.State, "access_denied")
		return
	}
	if r.PostForm.Get("decision") != "approve" {
		s.oauthRedirectError(w, pending.RedirectURI, pending.State, "invalid_request")
		return
	}
	token, err := s.OAuthStore.GetAPITokenByID(r.PostForm.Get("api_token_id"))
	if err != nil || token.IsLegacy || token.RevokedAt != nil || (token.ExpiresAt != nil && *token.ExpiresAt <= time.Now().Unix()) {
		http.Error(w, "selected named token is unavailable", 400)
		return
	}
	var scopes []string
	if json.Unmarshal([]byte(token.ScopesJSON), &scopes) != nil {
		http.Error(w, "selected token policy is invalid", 400)
		return
	}
	tokenScopes := map[string]bool{}
	for _, v := range scopes {
		tokenScopes[v] = true
	}
	requested := map[string]bool{}
	for _, v := range pending.Requested {
		requested[v] = true
	}
	chosen := []string{}
	for _, v := range r.PostForm["scope"] {
		if !requested[v] || !tokenScopes[v] {
			http.Error(w, "selected scope exceeds policy", 400)
			return
		}
		chosen = append(chosen, v)
	}
	if len(chosen) == 0 {
		http.Error(w, "at least one scope must be approved", 400)
		return
	}
	sort.Strings(chosen)
	scopesJSON, _ := json.Marshal(chosen)
	code, err := randomOAuthValue("vpc_oac_", 32)
	if err != nil {
		http.Error(w, "authorization failed", 500)
		return
	}
	version, err := s.OAuthStore.GetAPITokenOAuthVersion(token.ID)
	if err != nil {
		http.Error(w, "selected named token unavailable", 400)
		return
	}
	now := time.Now().Unix()
	rec := storage.OAuthAuthorizationCode{CodeHash: storage.HashAPIToken(code), ClientID: pending.ClientID, APITokenID: token.ID, APITokenPrefix: token.Prefix, APITokenVersion: version, RedirectURI: pending.RedirectURI, ScopesJSON: string(scopesJSON), CodeChallenge: pending.Challenge, Resource: pending.Resource, CreatedAt: now, ExpiresAt: now + 300}
	if err := s.OAuthStore.CreateOAuthCode(rec); err != nil {
		http.Error(w, "authorization could not be recorded", 409)
		return
	}
	u, _ := url.Parse(pending.RedirectURI)
	params := u.Query()
	params.Set("code", code)
	params.Set("state", pending.State)
	u.RawQuery = params.Encode()
	w.Header().Set("Location", u.String())
	w.WriteHeader(http.StatusFound)
}
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	body := map[string]string{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	writeOAuthJSON(w, status, body)
}
func parseOAuthClientAuthentication(r *http.Request, form url.Values, store *storage.Store) (storage.OAuthClient, string, bool) {
	id, secret, basic := r.BasicAuth()
	formID, formSecret := form.Get("client_id"), form.Get("client_secret")
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader != "" && !basic {
		return storage.OAuthClient{}, "invalid_client", false
	}
	if basic && (formID != "" || formSecret != "") {
		return storage.OAuthClient{}, "invalid_request", false
	}
	if basic {
		var err error
		id, err = url.QueryUnescape(id)
		if err != nil {
			return storage.OAuthClient{}, "invalid_client", false
		}
		secret, err = url.QueryUnescape(secret)
		if err != nil {
			return storage.OAuthClient{}, "invalid_client", false
		}
	} else {
		id, secret = formID, formSecret
	}
	if id == "" || secret == "" {
		return storage.OAuthClient{}, "invalid_client", false
	}
	c, ok := store.AuthenticateOAuthClient(id, secret)
	if !ok {
		return storage.OAuthClient{}, "invalid_client", false
	}
	return c, "", true
}
func (s *Server) HandleToken(w http.ResponseWriter, r *http.Request) {
	if _, enabled := oauthPublicURL(); !enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		oauthError(w, 405, "invalid_request", "POST required")
		return
	}
	if s.OAuthStore == nil {
		oauthError(w, 503, "server_error", "OAuth storage unavailable")
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.URL.RawQuery != "" || mediaErr != nil || mediaType != "application/x-www-form-urlencoded" {
		oauthError(w, 400, "invalid_request", "form-encoded POST body required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024+1))
	if err != nil || len(body) > 32*1024 {
		oauthError(w, 400, "invalid_request", "request body too large")
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		oauthError(w, 400, "invalid_request", "malformed form")
		return
	}
	if hasDuplicateParams(form, "grant_type", "code", "redirect_uri", "code_verifier", "refresh_token", "client_id", "client_secret", "resource") {
		oauthError(w, 400, "invalid_request", "duplicate parameters are not allowed")
		return
	}
	client, authErr, ok := parseOAuthClientAuthentication(r, form, s.OAuthStore)
	if !ok {
		if authErr == "invalid_client" {
			w.Header().Set("WWW-Authenticate", `Basic realm="oauth/token"`)
			oauthError(w, 401, "invalid_client", "client authentication failed")
		} else {
			oauthError(w, 400, authErr, "do not send multiple client authentication methods")
		}
		return
	}
	grantType := form.Get("grant_type")
	if !oauthClientAllows(client, "grant_type", grantType) {
		oauthError(w, 400, "unsupported_grant_type", "client is not registered for this grant type")
		return
	}
	now := time.Now().Unix()
	switch grantType {
	case "authorization_code":
		code, redirect, verifier := form.Get("code"), form.Get("redirect_uri"), form.Get("code_verifier")
		if code == "" || redirect == "" || !validPKCEVerifier(verifier) {
			oauthError(w, 400, "invalid_request", "code, redirect_uri and valid code_verifier required")
			return
		}
		hash := storage.HashAPIToken(code)
		rec, err := s.OAuthStore.GetOAuthCodeForExchange(hash, client.ID, redirect, now)
		if err != nil {
			oauthError(w, 400, "invalid_grant", "authorization code invalid, expired, or used")
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(challenge), []byte(rec.CodeChallenge)) != 1 {
			// A code gets one verifier attempt. Consuming it on failure prevents
			// repeated guesses and makes the failure terminal for this code.
			_, _ = s.OAuthStore.ConsumeOAuthCode(hash, client.ID, redirect, now)
			oauthError(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		if resource := form.Get("resource"); resource != "" && resource != rec.Resource {
			oauthError(w, 400, "invalid_target", "resource does not match authorization")
			return
		}
		rec, err = s.OAuthStore.ConsumeOAuthCode(hash, client.ID, redirect, now)
		if err != nil {
			oauthError(w, 400, "invalid_grant", "authorization code invalid, expired, or used")
			return
		}
		access, err := randomOAuthValue("vpc_oat_", 32)
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		refresh, err := randomOAuthValue("vpc_ort_", 48)
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		gid, err := randomOAuthID("og_")
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		aid, err := randomOAuthID("ot_")
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		rid, err := randomOAuthID("ot_")
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		exp, refreshExp := now+3600, now+30*24*3600
		g := storage.OAuthGrant{ID: gid, ClientID: client.ID, APITokenID: rec.APITokenID, APITokenPrefix: rec.APITokenPrefix, APITokenVersion: rec.APITokenVersion, ScopesJSON: rec.ScopesJSON, Resource: rec.Resource, CodeHash: rec.CodeHash, CreatedAt: now, RefreshExpiresAt: refreshExp}
		if err := s.OAuthStore.CreateOAuthGrant(g, storage.OAuthToken{ID: aid, GrantID: gid, Kind: "access", TokenHash: storage.HashAPIToken(access), CreatedAt: now, ExpiresAt: exp}, storage.OAuthToken{ID: rid, GrantID: gid, Kind: "refresh", TokenHash: storage.HashAPIToken(refresh), CreatedAt: now, ExpiresAt: refreshExp}); err != nil {
			oauthError(w, 400, "invalid_grant", "authorization grant could not be created")
			return
		}
		writeOAuthJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "refresh_token": refresh, "scope": strings.Join(decodeScopes(rec.ScopesJSON), " "), "resource": rec.Resource})
	case "refresh_token":
		raw := form.Get("refresh_token")
		if raw == "" {
			oauthError(w, 400, "invalid_request", "refresh_token required")
			return
		}
		old, err := s.OAuthStore.GetOAuthGrantForRefresh(storage.HashAPIToken(raw), client.ID)
		if err != nil {
			oauthError(w, 400, "invalid_grant", "refresh token invalid")
			return
		}
		if resource := form.Get("resource"); resource != "" && resource != old.Resource {
			oauthError(w, 400, "invalid_target", "resource does not match authorization")
			return
		}
		access, err := randomOAuthValue("vpc_oat_", 32)
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		refresh, err := randomOAuthValue("vpc_ort_", 48)
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		aid, err := randomOAuthID("ot_")
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		rid, err := randomOAuthID("ot_")
		if err != nil {
			oauthError(w, 500, "server_error", "token generation failed")
			return
		}
		rotated, err := s.OAuthStore.RotateOAuthRefresh(storage.HashAPIToken(raw), client.ID, now, storage.OAuthToken{ID: aid, GrantID: old.ID, Kind: "access", TokenHash: storage.HashAPIToken(access), CreatedAt: now, ExpiresAt: now + 3600}, storage.OAuthToken{ID: rid, GrantID: old.ID, Kind: "refresh", TokenHash: storage.HashAPIToken(refresh), CreatedAt: now, ExpiresAt: old.RefreshExpiresAt})
		if err != nil {
			oauthError(w, 400, "invalid_grant", "refresh token invalid, expired, revoked, or replayed")
			return
		}
		writeOAuthJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "refresh_token": refresh, "scope": strings.Join(decodeScopes(rotated.ScopesJSON), " "), "resource": rotated.Resource})
	default:
		oauthError(w, 400, "unsupported_grant_type", "grant_type is not supported")
	}
}
func validPKCEVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}
func decodeScopes(raw string) []string { var v []string; _ = json.Unmarshal([]byte(raw), &v); return v }
