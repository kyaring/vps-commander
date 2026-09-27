package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HandleOAuthMetadata serves /.well-known/oauth-authorization-server
func (s *Server) HandleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	scheme := "https"
	host := r.Host
	meta := map[string]any{
		"issuer":                                fmt.Sprintf("%s://%s", scheme, host),
		"authorization_endpoint":                fmt.Sprintf("%s://%s/oauth/authorize", scheme, host),
		"token_endpoint":                        fmt.Sprintf("%s://%s/oauth/token", scheme, host),
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic", "none"},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(meta)
}

// HandleAuthorize handles the user/client authorization step
func (s *Server) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")

	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}

	code := "vps-commander-auth-code"
	target := fmt.Sprintf("%s?code=%s&state=%s", redirectURI, code, state)
	http.Redirect(w, r, target, http.StatusFound)
}

// HandleToken exchanges code for bearer token
func (s *Server) HandleToken(w http.ResponseWriter, r *http.Request) {
	token := ""
	if s.Auth != nil {
		token = s.Auth.Token()
	}

	resp := map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   86400 * 365,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(resp)
}
