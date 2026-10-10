package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestOAuthClientProvisioningIsAdminOnlyAndSecretIsOneTime(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "oauth-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &Server{Store: store}
	h := s.Routes()

	unauth := httptest.NewRecorder()
	unauthReq := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/clients", strings.NewReader(`{"name":"ChatGPT","redirect_uris":["https://client.example/callback"]}`))
	h.ServeHTTP(unauth, unauthReq)
	if unauth.Code != http.StatusForbidden {
		t.Fatalf("non-admin create status=%d body=%s", unauth.Code, unauth.Body.String())
	}

	create := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/clients", strings.NewReader(`{"name":"ChatGPT","redirect_uris":["https://client.example/callback"]}`))
	h.ServeHTTP(create, systemRequest(createReq))
	if create.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	if create.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential response cache-control=%q", create.Header().Get("Cache-Control"))
	}
	var result struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Name         string `json:"client_name"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ClientID == "" || result.ClientSecret == "" || result.Name != "ChatGPT" {
		t.Fatalf("incomplete client credentials: %+v", result)
	}
	if _, ok := store.AuthenticateOAuthClient(result.ClientID, result.ClientSecret); !ok {
		t.Fatal("new client credentials do not authenticate")
	}

	list := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/clients", nil)
	h.ServeHTTP(list, systemRequest(listReq))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), result.ClientSecret) || strings.Contains(list.Body.String(), "client_secret_hash") {
		t.Fatalf("client secret leaked from list: %s", list.Body.String())
	}

	disable := httptest.NewRecorder()
	disableReq := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/clients/"+result.ClientID+"/disable", nil)
	h.ServeHTTP(disable, systemRequest(disableReq))
	if disable.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	if _, ok := store.AuthenticateOAuthClient(result.ClientID, result.ClientSecret); ok {
		t.Fatal("disabled client still authenticates")
	}
}

func TestOAuthClientProvisioningRejectsUnsafeRedirectURI(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "oauth-redirect.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &Server{Store: store}
	for _, uri := range []string{"javascript:alert(1)", "https://evil.example/callback#fragment", "http://evil.example/callback", "https://user:pass@client.example/callback", "https://*.example/callback"} {
		t.Run(uri, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"name": "unsafe", "redirect_uris": []string{uri}})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/clients", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			s.OAuthClientsJSON(w, systemRequest(r))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("unsafe redirect URI %q returned %d: %s", uri, w.Code, w.Body.String())
			}
		})
	}
}
