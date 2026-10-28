package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func devtaskCtx(scopes map[string]bool) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "tok-adm", Name: "adm", TokenID: "tok-adm", Scopes: scopes})
}

func devtaskDo(t *testing.T, s *Server, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

const validProject = `{"name":"wjy-web","repo_path":"/srv/repos/wjy-web","base_ref":"main","default_model":"global:deepseek-v4.1-flash","allowed_cmds":["go test"],"default_accept_cmds":["go vet ./..."],"forbidden_paths":[".git"],"max_rounds":3,"max_steps":50,"timeout_s":1800,"enabled":true}`

func TestDevTaskAdminRoutesDisabledByDefault(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	for _, path := range []string{"/api/v1/devtask-projects", "/api/v1/devtask-settings"} {
		if rec := devtaskDo(t, s, admin, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s with switch off: status %d, want 404", path, rec.Code)
		}
	}
}

func TestDevTaskAdminRequiresAdmin(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	submitOnly := devtaskCtx(map[string]bool{"devtask:submit": true})
	if rec := devtaskDo(t, s, submitOnly, http.MethodGet, "/api/v1/devtask-settings", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("devtask:submit token on admin settings: status %d, want 403", rec.Code)
	}
	if rec := devtaskDo(t, s, submitOnly, http.MethodPost, "/api/v1/devtask-projects", validProject); rec.Code != http.StatusForbidden {
		t.Fatalf("devtask:submit token on admin projects: status %d, want 403", rec.Code)
	}
}

func TestDevTaskProjectUpsertAndModelOK(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	if rec := devtaskDo(t, s, admin, http.MethodPost, "/api/v1/devtask-projects", validProject); rec.Code != http.StatusOK {
		t.Fatalf("valid project: status %d body %s", rec.Code, rec.Body.String())
	}
	bad := strings.Replace(validProject, "global:deepseek-v4.1-flash", "global:not-allowed", 1)
	if rec := devtaskDo(t, s, admin, http.MethodPost, "/api/v1/devtask-projects", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("model outside whitelist: status %d, want 400", rec.Code)
	}
	if rec := devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-settings", `{"allowed_models":["global:other-model"]}`); rec.Code != http.StatusOK {
		t.Fatalf("replace whitelist: status %d", rec.Code)
	}
	rec := devtaskDo(t, s, admin, http.MethodGet, "/api/v1/devtask-projects", "")
	var out struct {
		Projects []struct {
			Name    string `json:"name"`
			ModelOK *bool  `json:"model_ok"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Projects) != 1 || out.Projects[0].ModelOK == nil || *out.Projects[0].ModelOK {
		t.Fatalf("after whitelist removal: %s (%v)", rec.Body.String(), err)
	}
}

func TestDevTaskProjectValidation(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	relative := strings.Replace(validProject, `"/srv/repos/wjy-web"`, `"repos/wjy-web"`, 1)
	if rec := devtaskDo(t, s, admin, http.MethodPost, "/api/v1/devtask-projects", relative); rec.Code != http.StatusBadRequest {
		t.Fatalf("relative repo_path: status %d, want 400", rec.Code)
	}
	zero := strings.Replace(validProject, `"max_rounds":3`, `"max_rounds":0`, 1)
	if rec := devtaskDo(t, s, admin, http.MethodPost, "/api/v1/devtask-projects", zero); rec.Code != http.StatusBadRequest {
		t.Fatalf("max_rounds=0: status %d, want 400", rec.Code)
	}
}

func TestDevTaskSettingsReplaceAndAudit(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	body := `{"allowed_models":["global:deepseek-v4.1-flash","global:other-model"]}`
	if rec := devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-settings", body); rec.Code != http.StatusOK {
		t.Fatalf("PUT settings: status %d body %s", rec.Code, rec.Body.String())
	}
	if n := countAudit(t, s, "action = 'devtask_settings_update' AND error_msg LIKE '%old=%' AND error_msg LIKE '%global:other-model%'"); n != 1 {
		t.Fatalf("settings audit rows = %d, want 1 with old and new values", n)
	}
}

func TestDevTaskSettingsRejectsBadInput(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	before, _ := s.Store.GetDevTaskAllowedModels()
	for _, body := range []string{`{"allowed_models":[""]}`, `{"allowed_models":["global:a","global:a"]}`} {
		if rec := devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-settings", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", body, rec.Code)
		}
	}
	after, _ := s.Store.GetDevTaskAllowedModels()
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("whitelist changed on bad input: %v -> %v", before, after)
	}
}

func TestDevTaskProjectAudited(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	if rec := devtaskDo(t, s, admin, http.MethodPost, "/api/v1/devtask-projects", validProject); rec.Code != http.StatusOK {
		t.Fatalf("project: status %d", rec.Code)
	}
	if n := countAudit(t, s, "action = 'devtask_project_upsert' AND error_msg LIKE 'old=none%'"); n != 1 {
		t.Fatalf("project audit rows = %d, want 1", n)
	}
}

func TestTokenCreateAcceptsDevTaskScopes(t *testing.T) {
	s, _ := newSpillTestServer(t)
	if err := s.Store.EnsureAPITokens(""); err != nil {
		t.Fatal(err)
	}
	// Seed a named token, then PATCH scopes the same way the panel does.
	if err := s.Store.CreateAPIToken(storage.APIToken{
		ID: "tok-devtask-scopes", Name: "devtask-scopes", Prefix: "dtscope1",
		TokenHash:  storage.HashAPIToken("vpc_dtscope1_testsecret"),
		ScopesJSON: `["devices:read"]`, DevicesJSON: "[]", RateRPS: 10, MaxSessions: 10,
		CreatedAt: 1, CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	admin := devtaskCtx(map[string]bool{"admin": true})
	body := `{"scopes":["devtask:submit","devtask:read","devtask:cancel","devtask:run"]}`
	rec := devtaskDo(t, s, admin, http.MethodPatch, "/api/v1/tokens/tok-devtask-scopes", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch devtask scopes: status %d body %s", rec.Code, rec.Body.String())
	}
	tok, err := s.Store.GetAPITokenByPrefix("dtscope1")
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel", "devtask:run"} {
		if !strings.Contains(tok.ScopesJSON, sc) {
			t.Fatalf("saved scopes missing %s: %s", sc, tok.ScopesJSON)
		}
	}
	// Create path must also accept the four scopes.
	createBody := `{"name":"devtask-create-ok","scopes":["devtask:submit","devtask:read","devtask:cancel","devtask:run"],"devices":[]}`
	rec = devtaskDo(t, s, admin, http.MethodPost, "/api/v1/tokens", createBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("create with devtask scopes: status %d body %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel", "devtask:run"} {
		if !strings.Contains(raw, sc) {
			t.Fatalf("create response missing %s: %s", sc, raw)
		}
	}
	bad := `{"name":"bad-scope","scopes":["devtask:admin"],"devices":[]}`
	if rec := devtaskDo(t, s, admin, http.MethodPost, "/api/v1/tokens", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown scope: status %d, want 400", rec.Code)
	}
}

func TestDevTaskSettingsWriteDBErrorReturns500(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	// Keep reads working, force writes to fail (SQLite query_only).
	if _, err := s.Store.DB.Exec(`PRAGMA query_only = ON`); err != nil {
		t.Fatal(err)
	}
	rec := devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-settings", `{"allowed_models":["global:deepseek-v4.1-flash"]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("query_only PUT settings: status %d body=%s, want 500", rec.Code, rec.Body.String())
	}
}
