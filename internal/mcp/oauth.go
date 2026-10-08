package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// OAuth discovery is retained for MCP clients that probe OAuth metadata.
// Token issuance is intentionally disabled: VPS-Commander uses its configured
// static Bearer API key and must never expose it through an unauthenticated
// OAuth endpoint.
func (s *Server) HandleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	scheme := "https"
	host := r.Host
	meta := map[string]any{
		"issuer":                                fmt.Sprintf("%s://%s", scheme, host),
		"authorization_endpoint":                fmt.Sprintf("%s://%s/oauth/authorize", scheme, host),
		"token_endpoint":                        fmt.Sprintf("%s://%s/oauth/token", scheme, host),
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(meta)
}

func (s *Server) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "OAuth authorization is not supported; use the configured Bearer API key", http.StatusNotImplemented)
}

func (s *Server) HandleToken(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "OAuth token issuance is disabled", http.StatusNotImplemented)
}
