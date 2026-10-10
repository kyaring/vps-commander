package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

type oauthClientCreateRequest struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
}

// OAuthClientsJSON is an admin-only static client provisioner. This is not
// OAuth Dynamic Client Registration: callers must already have admin access,
// and the generated secret is returned exactly once.
func (s *Server) OAuthClientsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		clients, err := s.Store.ListOAuthClients()
		if err != nil {
			http.Error(w, "OAuth client query failed", http.StatusInternalServerError)
			return
		}
		jsonOut(w, clients)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var q oauthClientCreateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(q.Name) == "" || len(q.Name) > 200 || len(q.RedirectURIs) == 0 || len(q.RedirectURIs) > 10 {
		http.Error(w, "name and 1-10 redirect_uris required", http.StatusBadRequest)
		return
	}
	uris, err := json.Marshal(q.RedirectURIs)
	if err != nil {
		http.Error(w, "invalid redirect URIs", http.StatusBadRequest)
		return
	}
	clientID := "vpc_client_" + randomTokenPart(16)
	clientSecret := "vpcs_" + randomTokenPart(48)
	client := storage.OAuthClient{
		ID: clientID, Name: strings.TrimSpace(q.Name), RedirectURIsJSON: string(uris),
		ClientSecretHash:  storage.HashAPIToken(clientSecret),
		GrantTypesJSON:    `["authorization_code","refresh_token"]`,
		ResponseTypesJSON: `["code"]`, CreatedAt: time.Now().Unix(),
	}
	if err := s.Store.RegisterOAuthClient(client); err != nil {
		http.Error(w, "OAuth client registration rejected", http.StatusBadRequest)
		return
	}
	_ = s.audit(r, "", "oauth_client_create", clientID, 0, 0, "")
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]any{
		"client_id": clientID, "client_secret": clientSecret,
		"client_name": client.Name, "redirect_uris": q.RedirectURIs,
		"grant_types":    []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": "client_secret_basic",
	})
}

func (s *Server) OAuthClientDisableJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/oauth/clients/")
	id := strings.TrimSuffix(path, "/disable")
	id = strings.Trim(id, "/")
	if id == "" || id == path || strings.Contains(id, "/") {
		http.Error(w, "client ID required", http.StatusBadRequest)
		return
	}
	if err := s.Store.DisableOAuthClient(id, time.Now().Unix()); err != nil {
		http.Error(w, "OAuth client not found", http.StatusNotFound)
		return
	}
	_ = s.audit(r, "", "oauth_client_disable", id, 0, 0, "")
	jsonOut(w, map[string]any{"client_id": id, "disabled": true})
}

func (s *Server) OAuthGrantsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	grants, err := s.Store.ListOAuthGrants()
	if err != nil {
		http.Error(w, "OAuth grant query failed", http.StatusInternalServerError)
		return
	}
	jsonOut(w, grants)
}

func (s *Server) OAuthGrantRevokeJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/oauth/grants/")
	id := strings.TrimSuffix(path, "/revoke")
	if id == "" || id == path || strings.Contains(id, "/") {
		http.Error(w, "grant ID required", http.StatusBadRequest)
		return
	}
	if err := s.Store.RevokeOAuthGrant(id, time.Now().Unix()); err != nil {
		http.Error(w, "OAuth grant not found", http.StatusNotFound)
		return
	}
	_ = s.audit(r, "", "oauth_grant_revoke", id, 0, 0, "")
	jsonOut(w, map[string]any{"grant_id": id, "revoked": true})
}
