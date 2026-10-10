package mcp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// OAuth is disabled unless VPS_COMMANDER_PUBLIC_URL is explicitly configured.
// Never derive issuer/endpoints from r.Host: reverse proxies and Host headers
// are not a trusted source of public identity.
func oauthPublicURL() (string, bool) {
	raw := strings.TrimSpace(os.Getenv("VPS_COMMANDER_PUBLIC_URL"))
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	if u.Scheme != "https" {
		host := strings.ToLower(u.Hostname())
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return "", false
		}
	}
	base := strings.TrimRight(raw, "/")
	if base == "" {
		return "", false
	}
	return base, true
}

func writeOAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) HandleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base, enabled := oauthPublicURL()
	if !enabled {
		http.NotFound(w, r)
		return
	}
	var metadata map[string]any
	if strings.Contains(r.URL.Path, "oauth-protected-resource") {
		metadata = map[string]any{
			"resource":                 base + "/mcp",
			"authorization_servers":    []string{base},
			"bearer_methods_supported": []string{"header"},
			"scopes_supported":         []string{"devices:read", "files:read", "files:write", "exec", "process", "mcp:call"},
		}
	} else {
		metadata = map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/oauth/authorize",
			"token_endpoint":                        base + "/oauth/token",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		}
	}
	writeOAuthJSON(w, http.StatusOK, metadata)
}

func (s *Server) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	if _, enabled := oauthPublicURL(); !enabled {
		http.NotFound(w, r)
		return
	}
	http.Error(w, "OAuth authorization flow is not enabled in this build phase", http.StatusNotImplemented)
}

func (s *Server) HandleToken(w http.ResponseWriter, r *http.Request) {
	if _, enabled := oauthPublicURL(); !enabled {
		http.NotFound(w, r)
		return
	}
	http.Error(w, "OAuth token issuance is not enabled in this build phase", http.StatusNotImplemented)
}
