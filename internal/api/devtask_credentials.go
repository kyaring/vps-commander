package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"net/url"
	"os"
	"regexp"

	"github.com/wjyhk/vps-commander/internal/storage"
)

const providerKeyEnv = "VPS_COMMANDER_PROVIDER_KEY"

var providerIDRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// providerKey reads the master key for sealing provider secrets. It must decode to 32 bytes.
// It is never logged and never stored.
func providerKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(providerKeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s is not set", providerKeyEnv)
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("%s must decode to 32 bytes", providerKeyEnv)
}

// providerSecret is the plaintext payload sealed in the database. It never leaves this package
// except through the runner credential endpoint, which is added with the lease work.
type providerSecret struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// providerView is what the admin API returns: metadata only, never URL or key.
type providerView struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	KeySet      bool   `json:"key_set"`
}

func validHTTPSBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("base_url must be an absolute https URL without credentials")
	}
	return nil
}

// devtaskProviders serves GET (list) and PUT /api/v1/devtask-providers/{id} for admins.
func (s *Server) devtaskProviders(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/v1/devtask-providers" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		list, err := s.Store.ListDevProviders()
		if err != nil {
			http.Error(w, "list providers failed", http.StatusInternalServerError)
			return
		}
		out := make([]providerView, 0, len(list))
		for _, p := range list {
			out = append(out, providerView{ID: p.ID, DisplayName: p.DisplayName, Enabled: p.Enabled, KeySet: p.KeySet})
		}
		jsonOut(w, map[string]any{"providers": out})
		return
	}
	id := strings.TrimPrefix(path, "/api/v1/devtask-providers/")
	if r.Method != http.MethodPut || !providerIDRE.MatchString(id) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var in struct {
		DisplayName string `json:"display_name"`
		Enabled     *bool  `json:"enabled"`
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
		ClearKey    bool   `json:"clear_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.DisplayName) == "" || len(in.DisplayName) > 64 {
		http.Error(w, "display_name must be 1-64 characters", http.StatusBadRequest)
		return
	}
	if in.BaseURL != "" {
		if err := validHTTPSBaseURL(in.BaseURL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if in.ClearKey && (in.BaseURL != "" || in.APIKey != "") {
		http.Error(w, "clear_key cannot be combined with base_url or api_key", http.StatusBadRequest)
		return
	}
	existing, err := s.Store.GetDevProvider(id)
	isNew := err != nil
	if err != nil && err != storage.ErrDevProviderNotFound {
		http.Error(w, "read provider failed", http.StatusInternalServerError)
		return
	}
	enabled := true
	if !isNew {
		enabled = existing.Enabled
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	// Work out the new sealed secret before writing anything.
	var newSealed []byte
	secretChanged := in.ClearKey || in.BaseURL != "" || in.APIKey != ""
	if secretChanged && !in.ClearKey {
		key, kerr := providerKey()
		if kerr != nil {
			http.Error(w, "provider key not configured", http.StatusServiceUnavailable)
			return
		}
		var cur providerSecret
		if !isNew && existing.KeySet {
			plain, oerr := storage.OpenProviderSecret(key, id, existing.Sealed())
			if oerr != nil || json.Unmarshal(plain, &cur) != nil {
				http.Error(w, "stored secret cannot be opened", http.StatusInternalServerError)
				return
			}
		}
		if in.BaseURL != "" {
			cur.BaseURL = in.BaseURL
		}
		if in.APIKey != "" {
			cur.APIKey = in.APIKey
		}
		if cur.BaseURL == "" || cur.APIKey == "" {
			http.Error(w, "base_url and api_key are both required when setting the secret", http.StatusBadRequest)
			return
		}
		payload, _ := json.Marshal(cur)
		newSealed, err = storage.SealProviderSecret(key, id, payload)
		if err != nil {
			http.Error(w, "seal failed", http.StatusInternalServerError)
			return
		}
	}

	if err := s.Store.UpsertDevProviderMeta(id, in.DisplayName, enabled); err != nil {
		http.Error(w, "save provider failed", http.StatusInternalServerError)
		return
	}
	if secretChanged {
		if err := s.Store.SetDevProviderSecret(id, newSealed); err != nil {
			http.Error(w, "save secret failed", http.StatusInternalServerError)
			return
		}
	}
	keySetAfter := existing.KeySet
	if secretChanged {
		keySetAfter = len(newSealed) > 0
	}
	// Audit records which fields changed, never their values.
	msg := fmt.Sprintf("id=%s enabled=%t key_set_before=%t key_set_after=%t base_url_changed=%t api_key_changed=%t clear_key=%t",
		id, enabled, existing.KeySet, keySetAfter, in.BaseURL != "", in.APIKey != "", in.ClearKey)
	_ = s.audit(r, id, "devtask_provider_update", "", 0, 0, msg)
	jsonOut(w, providerView{ID: id, DisplayName: in.DisplayName, Enabled: enabled, KeySet: keySetAfter})
}

// devtaskCredentials returns the provider URL and API key only to the named runner
// that currently owns the task lease. Secret values are never included in audit data.
func (s *Server) devtaskCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := devtaskIDFromActionPath(r.URL.Path, "credentials")
	p, _ := auth.PrincipalFromContext(r.Context())
	if p.Type != "api_token" || p.TokenID == "" || p.TokenID == "legacy-env" || p.ID == "legacy-env" || p.Name == "legacy-env" {
		s.auditCredentials(r, "", "", http.StatusForbidden, "runner_token_required")
		credentialsError(w, http.StatusForbidden, "runner_token_required")
		return
	}
	if !credentialsRequestIsHTTPS(r) {
		s.auditCredentials(r, "", "", http.StatusForbidden, "https_required")
		credentialsError(w, http.StatusForbidden, "https_required")
		return
	}
	task, err := s.Store.GetDevTask(id)
	if err != nil {
		s.auditCredentials(r, "", "", http.StatusNotFound, "task_not_found")
		credentialsError(w, http.StatusNotFound, "task_not_found")
		return
	}
	command := "task=" + task.ID + " model=" + task.Model
	if task.Status != storage.DevTaskRunning || task.LeaseUntil <= time.Now().Unix() {
		s.auditCredentials(r, providerIDFromModel(task.Model), command, http.StatusConflict, "lease_expired")
		credentialsError(w, http.StatusConflict, "lease_expired")
		return
	}
	if task.ClaimedBy != p.TokenID {
		s.auditCredentials(r, providerIDFromModel(task.Model), command, http.StatusForbidden, "not_lease_owner")
		credentialsError(w, http.StatusForbidden, "not_lease_owner")
		return
	}
	allowed, err := s.Store.GetDevTaskAllowedModels()
	if err != nil {
		s.auditCredentials(r, providerIDFromModel(task.Model), command, http.StatusInternalServerError, "whitelist_read_failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !containsString(allowed, task.Model) {
		s.auditCredentials(r, providerIDFromModel(task.Model), command, http.StatusConflict, "model_not_allowed")
		credentialsError(w, http.StatusConflict, "model_not_allowed")
		return
	}
	providerID := providerIDFromModel(task.Model)
	if providerID == "" {
		s.auditCredentials(r, "", command, http.StatusConflict, "provider_unavailable")
		credentialsError(w, http.StatusConflict, "provider_unavailable")
		return
	}
	provider, err := s.Store.GetDevProvider(providerID)
	if err != nil || !provider.Enabled || !provider.KeySet {
		s.auditCredentials(r, providerID, command, http.StatusConflict, "provider_unavailable")
		credentialsError(w, http.StatusConflict, "provider_unavailable")
		return
	}
	masterKey, err := providerKey()
	if err != nil {
		s.auditCredentials(r, providerID, command, http.StatusInternalServerError, "provider_secret_unavailable")
		http.Error(w, "provider secret unavailable", http.StatusInternalServerError)
		return
	}
	plain, err := storage.OpenProviderSecret(masterKey, providerID, provider.Sealed())
	if err != nil {
		s.auditCredentials(r, providerID, command, http.StatusInternalServerError, "provider_secret_unavailable")
		http.Error(w, "provider secret unavailable", http.StatusInternalServerError)
		return
	}
	var secret providerSecret
	if err := json.Unmarshal(plain, &secret); err != nil || secret.BaseURL == "" || secret.APIKey == "" {
		s.auditCredentials(r, providerID, command, http.StatusInternalServerError, "provider_secret_unavailable")
		http.Error(w, "provider secret unavailable", http.StatusInternalServerError)
		return
	}
	if err := s.auditCredentials(r, providerID, command, 0, ""); err != nil {
		http.Error(w, "credential audit unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]string{"base_url": secret.BaseURL, "api_key": secret.APIKey})
}

func providerIDFromModel(model string) string {
	providerID, modelName, ok := strings.Cut(model, ":")
	if !ok || !providerIDRE.MatchString(providerID) || strings.TrimSpace(modelName) == "" {
		return ""
	}
	return providerID
}

func credentialsRequestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.Trim(r.RemoteAddr, "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) auditCredentials(r *http.Request, providerID, command string, code int, errorCode string) error {
	p, _ := auth.PrincipalFromContext(r.Context())
	result := "ok"
	if code == http.StatusForbidden || code == http.StatusConflict {
		result = "denied"
	} else if code >= 400 {
		result = "error"
	}
	return s.Store.AuditEntry(storage.AuditEntry{
		CallerIP: auth.ClientIP(r), Action: "devtask_credentials", Command: command,
		ExitCode: code, ErrorMsg: errorCode, ActorType: p.Type, ActorID: p.ID,
		ActorName: p.AuditName(), OAuthClientID: p.OAuthClient, Transport: "rest",
		TargetType: "provider", Target: providerID, Result: result,
	})
}

func credentialsError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	jsonOut(w, map[string]string{"error": code})
}
