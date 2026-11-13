package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"

	"github.com/wjyhk/vps-commander/internal/storage"
)

const testProviderKeyB64 = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=" // 32 bytes, test only

const canaryKey = "test-key-not-real"
const canaryURL = "https://gateway-canary.example.test/v1"

func setProviderKey(t *testing.T) {
	t.Helper()
	t.Setenv(providerKeyEnv, testProviderKeyB64)
	if _, err := base64.StdEncoding.DecodeString(testProviderKeyB64); err != nil {
		t.Fatal(err)
	}
}

func putProvider(t *testing.T, s *Server, body string) *httptestRecorder {
	t.Helper()
	admin := devtaskCtx(map[string]bool{"admin": true})
	return devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-providers/global", body)
}

type httptestRecorder = recorderAlias

func TestProviderKeyNotConfigured503(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	t.Setenv(providerKeyEnv, "")
	s, _ := newSpillTestServer(t)
	body := `{"display_name":"Global","base_url":"` + canaryURL + `","api_key":"` + canaryKey + `"}`
	if rec := putProvider(t, s, body); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("secret write without master key: status %d, want 503", rec.Code)
	}
	// Non-secret metadata still works without the master key.
	if rec := putProvider(t, s, `{"display_name":"Global","enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("metadata write without master key: status %d", rec.Code)
	}
}

func TestProviderResponsesNeverContainSecrets(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	setProviderKey(t)
	s, _ := newSpillTestServer(t)
	body := `{"display_name":"Global","base_url":"` + canaryURL + `","api_key":"` + canaryKey + `"}`
	rec := putProvider(t, s, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), canaryKey) || strings.Contains(rec.Body.String(), "gateway-canary") {
		t.Fatalf("PUT response leaks a secret: %s", rec.Body.String())
	}
	admin := devtaskCtx(map[string]bool{"admin": true})
	get := devtaskDo(t, s, admin, http.MethodGet, "/api/v1/devtask-providers", "")
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), canaryKey) || strings.Contains(get.Body.String(), "gateway-canary") || !strings.Contains(get.Body.String(), `"key_set":true`) {
		t.Fatalf("GET leaks a secret or misreports key_set: %s", get.Body.String())
	}
	if n := countAudit(t, s, "action = 'devtask_provider_update' AND error_msg LIKE '%api_key_changed=true%'"); n != 1 {
		t.Fatalf("provider audit rows = %d, want 1", n)
	}
	if n := countAudit(t, s, "error_msg LIKE ? OR error_msg LIKE ?", "%"+canaryKey+"%", "%gateway-canary%"); n != 0 {
		t.Fatalf("audit leaks a secret (%d rows)", n)
	}
}

func TestProviderPutKeepsKeyWhenOmitted(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	setProviderKey(t)
	s, _ := newSpillTestServer(t)
	if rec := putProvider(t, s, `{"display_name":"Global","base_url":"`+canaryURL+`","api_key":"`+canaryKey+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("first PUT: %d", rec.Code)
	}
	before, _ := s.Store.GetDevProvider("global")
	if rec := putProvider(t, s, `{"display_name":"Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("metadata-only PUT: %d", rec.Code)
	}
	after, _ := s.Store.GetDevProvider("global")
	if !after.KeySet || string(after.Sealed()) != string(before.Sealed()) || after.DisplayName != "Renamed" {
		t.Fatalf("metadata-only PUT changed the secret or key_set: %+v", after)
	}
	// Replacing only the key keeps the stored base URL.
	if rec := putProvider(t, s, `{"display_name":"Renamed","api_key":"test-key-not-real"}`); rec.Code != http.StatusOK {
		t.Fatalf("key-only PUT: %d", rec.Code)
	}
	if n := countAudit(t, s, "action = 'devtask_provider_update' AND error_msg LIKE '%base_url_changed=false%api_key_changed=true%'"); n != 1 {
		t.Fatalf("key-only audit rows = %d, want 1", n)
	}
}

func TestProviderClearKey(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	setProviderKey(t)
	s, _ := newSpillTestServer(t)
	putProvider(t, s, `{"display_name":"Global","base_url":"`+canaryURL+`","api_key":"`+canaryKey+`"}`)
	if rec := putProvider(t, s, `{"display_name":"Global","clear_key":true}`); rec.Code != http.StatusOK {
		t.Fatalf("clear_key: %d", rec.Code)
	}
	p, _ := s.Store.GetDevProvider("global")
	if p.KeySet || len(p.Sealed()) != 0 {
		t.Fatalf("clear_key did not clear: %+v", p)
	}
	if rec := putProvider(t, s, `{"display_name":"Global","clear_key":true,"api_key":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("clear_key combined with api_key: %d, want 400", rec.Code)
	}
}

func TestProviderBaseURLMustBeHTTPS(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	setProviderKey(t)
	s, _ := newSpillTestServer(t)
	for _, bad := range []string{"http://gateway.example.test/v1", "/relative/v1", "https://user:pw@gateway.example.test/v1"} {
		body := `{"display_name":"Global","base_url":"` + bad + `","api_key":"k"}`
		if rec := putProvider(t, s, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("base_url %q: status %d, want 400", bad, rec.Code)
		}
	}
	if _, err := s.Store.GetDevProvider("global"); err != storage.ErrDevProviderNotFound {
		t.Fatalf("rejected PUT still created the provider: %v", err)
	}
}

func TestProviderRequiresAdmin(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	setProviderKey(t)
	s, _ := newSpillTestServer(t)
	submitOnly := devtaskCtx(map[string]bool{"devtask:submit": true})
	if rec := devtaskDo(t, s, submitOnly, http.MethodPut, "/api/v1/devtask-providers/global", `{"display_name":"G"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin PUT: status %d, want 403", rec.Code)
	}
}

type recorderAlias = httptest.ResponseRecorder

func credentialRunnerContext(tokenID string, scopes map[string]bool) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: tokenID, Name: "runner", TokenID: tokenID, Scopes: scopes})
}

func credentialRequest(t *testing.T, s *Server, ctx context.Context, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devtasks/"+id+"/credentials", nil).WithContext(ctx)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func seedCredentialTask(t *testing.T, s *Server, claimedBy, model string, leaseUntil int64) {
	t.Helper()
	seedDevProject(t, s)
	if err := s.Store.CreateDevTask(storage.DevTask{
		ID: "cred-task", Project: "demo", Title: "credential test", SpecMD: "test",
		BaseRef: "main", Branch: "task/credential-test", Model: model, Status: storage.DevTaskRunning,
		Round: 1, MaxRounds: 1, MaxSteps: 5, TimeoutS: 60, ClaimedBy: claimedBy, LeaseUntil: leaseUntil,
	}); err != nil {
		t.Fatal("create test task failed")
	}
}

func setupCredentialProvider(t *testing.T, s *Server, enabled bool) {
	t.Helper()
	setProviderKey(t)
	body := `{"display_name":"Global","enabled":` + map[bool]string{true: "true", false: "false"}[enabled] + `,"base_url":"` + canaryURL + `","api_key":"` + canaryKey + `"}`
	if rec := putProvider(t, s, body); rec.Code != http.StatusOK {
		t.Fatalf("provider setup failed: status %d", rec.Code)
	}
}

func TestCredentialsEndpointRequiresLeaseOwner(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	setupCredentialProvider(t, s, true)
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:model"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	seedCredentialTask(t, s, "tok-adm", "global:model", time.Now().Unix()+60)
	other := credentialRunnerContext("tok-other", map[string]bool{"devtask:run": true})
	if rec := credentialRequest(t, s, other, "cred-task"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not_lease_owner") {
		t.Fatalf("wrong lease owner response: status %d cache=%q has_error=%t body=%q", rec.Code, rec.Header().Get("Cache-Control"), strings.Contains(rec.Body.String(), `"error"`), strings.ReplaceAll(strings.ReplaceAll(rec.Body.String(), canaryKey, "[redacted]"), canaryURL, "[url]"))
	}
	if _, err := s.Store.DB.Exec("UPDATE dev_tasks SET lease_until=? WHERE id=?", time.Now().Unix()-1, "cred-task"); err != nil {
		t.Fatal("expire lease setup failed")
	}
	runner := credentialRunnerContext("tok-adm", map[string]bool{"devtask:run": true})
	if rec := credentialRequest(t, s, runner, "cred-task"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "lease_expired") {
		t.Fatalf("expired lease response: status %d", rec.Code)
	}
}

func TestCredentialsEndpointRejectsModelNotInWhitelist(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	setupCredentialProvider(t, s, true)
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:other"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	seedCredentialTask(t, s, "tok-adm", "global:model", time.Now().Unix()+60)
	runner := credentialRunnerContext("tok-adm", map[string]bool{"devtask:run": true})
	if rec := credentialRequest(t, s, runner, "cred-task"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "model_not_allowed") {
		t.Fatalf("model whitelist response: status %d cache=%q has_error=%t", rec.Code, rec.Header().Get("Cache-Control"), strings.Contains(rec.Body.String(), `"error"`))
	}
}

func TestCredentialsEndpointRejectsDisabledProvider(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	for _, enabled := range []bool{false, true} {
		s, _ := newSpillTestServer(t)
		if enabled {
			setProviderKey(t)
			if rec := putProvider(t, s, `{"display_name":"Global","enabled":false}`); rec.Code != http.StatusOK {
				t.Fatal("provider setup failed")
			}
		} else {
			setProviderKey(t)
		}
		if err := s.Store.SetDevTaskAllowedModels([]string{"global:model"}); err != nil {
			t.Fatal("whitelist setup failed")
		}
		seedCredentialTask(t, s, "tok-adm", "global:model", time.Now().Unix()+60)
		runner := credentialRunnerContext("tok-adm", map[string]bool{"devtask:run": true})
		if rec := credentialRequest(t, s, runner, "cred-task"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "provider_unavailable") {
			t.Fatalf("unavailable provider response: status %d cache=%q has_error=%t", rec.Code, rec.Header().Get("Cache-Control"), strings.Contains(rec.Body.String(), `"error"`))
		}
	}
}

func TestCredentialsEndpointRejectsRegisteredButDisabledProvider(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	setProviderKey(t)
	s, _ := newSpillTestServer(t)
	if rec := putProvider(t, s, `{"display_name":"Global","base_url":"`+canaryURL+`","api_key":"`+canaryKey+`","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disabled provider setup failed: status %d", rec.Code)
	}
	provider, err := s.Store.GetDevProvider("global")
	if err != nil || !provider.KeySet || provider.Enabled {
		t.Fatal("provider fixture must be registered, keyed, and disabled")
	}
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:model"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	seedCredentialTask(t, s, "tok-adm", "global:model", time.Now().Unix()+60)
	runner := credentialRunnerContext("tok-adm", map[string]bool{"devtask:run": true})
	if rec := credentialRequest(t, s, runner, "cred-task"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "provider_unavailable") {
		t.Fatalf("registered disabled provider response: status %d has_error=%t", rec.Code, strings.Contains(rec.Body.String(), `"error"`))
	}
}

func TestCredentialsEndpointReturnsDecryptedSecrets(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	setupCredentialProvider(t, s, true)
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:model"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	seedCredentialTask(t, s, "tok-adm", "global:model", time.Now().Unix()+60)
	runner := credentialRunnerContext("tok-adm", map[string]bool{"devtask:run": true})
	rec := credentialRequest(t, s, runner, "cred-task")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential response status/header invalid: %d", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal("credential response JSON invalid")
	}
	if len(out) != 2 || out["base_url"] != canaryURL || out["api_key"] != canaryKey {
		t.Fatal("credential response fields/value mismatch")
	}
}

func TestCredentialsEndpointAuditHasNoSecret(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	setupCredentialProvider(t, s, true)
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:model"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	seedCredentialTask(t, s, "tok-adm", "global:model", time.Now().Unix()+60)
	runner := credentialRunnerContext("tok-adm", map[string]bool{"devtask:run": true})
	_ = credentialRequest(t, s, runner, "cred-task")
	if err := s.Store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Store.DB.Query(`SELECT command, COALESCE(error_msg,'') FROM audit_logs WHERE action=? AND command LIKE ?`, "devtask_credentials", "task=cred-task%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var command, errorMsg string
		if err := rows.Scan(&command, &errorMsg); err != nil {
			t.Fatal(err)
		}
		count++
		for _, field := range []struct{ name, value string }{{"command", command}, {"error_msg", errorMsg}} {
			if strings.Contains(field.value, canaryKey) {
				t.Fatalf("credential audit leak detected in %s", field.name)
			}
			for n := 4; n <= len(canaryKey); n++ {
				if strings.Contains(field.value, canaryKey[:n]) {
					t.Fatalf("credential audit leak detected in %s", field.name)
				}
			}
			if strings.Contains(strings.ToLower(field.value), "api_key=") {
				t.Fatalf("credential audit leak detected in %s: api_key assignment", field.name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("credential audit rows not found for task")
	}
}

func TestCredentialsEndpointRequiresRunScope(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	if rec := credentialRequest(t, s, devtaskCtx(map[string]bool{"devtask:read": true}), "missing"); rec.Code != http.StatusForbidden {
		t.Fatalf("scope response: %d", rec.Code)
	}
}

func TestWhitelistRejectsUnregisteredProvider(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:old"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	admin := devtaskCtx(map[string]bool{"admin": true})
	rec := devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-settings", `{"allowed_models":["global:new"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provider_not_registered") {
		t.Fatalf("unregistered provider response: %d", rec.Code)
	}
	got, _ := s.Store.GetDevTaskAllowedModels()
	if strings.Join(got, ",") != "global:old" {
		t.Fatal("whitelist changed after rejected update")
	}
}

func TestWhitelistRejectsDisabledProvider(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	setProviderKey(t)
	if rec := putProvider(t, s, `{"display_name":"Global","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatal("provider setup failed")
	}
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:old"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	admin := devtaskCtx(map[string]bool{"admin": true})
	rec := devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-settings", `{"allowed_models":["global:new"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provider_not_registered") {
		t.Fatalf("disabled provider response: %d", rec.Code)
	}
	got, _ := s.Store.GetDevTaskAllowedModels()
	if strings.Join(got, ",") != "global:old" {
		t.Fatal("whitelist changed after rejected update")
	}
}

func TestWhitelistAcceptsRegisteredEnabledProvider(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	setupCredentialProvider(t, s, true)
	admin := devtaskCtx(map[string]bool{"admin": true})
	rec := devtaskDo(t, s, admin, http.MethodPut, "/api/v1/devtask-settings", `{"allowed_models":["global:new"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled provider whitelist update status: %d", rec.Code)
	}
	got, _ := s.Store.GetDevTaskAllowedModels()
	if strings.Join(got, ",") != "global:new" {
		t.Fatal("enabled provider whitelist update was not persisted")
	}
}

func TestCredentialsEndpointRequiresNamedRunnerToken(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	setupCredentialProvider(t, s, true)
	if err := s.Store.SetDevTaskAllowedModels([]string{"global:model"}); err != nil {
		t.Fatal("whitelist setup failed")
	}
	seedCredentialTask(t, s, "tok-adm", "global:model", time.Now().Unix()+60)
	legacy := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "legacy-env", Name: "legacy-env", TokenID: "legacy-env", Scopes: map[string]bool{"devtask:run": true}})
	if rec := credentialRequest(t, s, legacy, "cred-task"); rec.Code != http.StatusForbidden {
		t.Fatalf("legacy token status: %d", rec.Code)
	}
	emptyID := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "runner", Name: "runner", Scopes: map[string]bool{"devtask:run": true}})
	if rec := credentialRequest(t, s, emptyID, "cred-task"); rec.Code != http.StatusForbidden {
		t.Fatalf("empty token ID status: %d", rec.Code)
	}
}

func providerProbeFixture(t *testing.T, handler http.Handler) (*Server, *httptest.Server) {
	t.Helper()
	t.Setenv("DEVTASK_ENABLED", "on")
	setProviderKey(t)
	upstream := httptest.NewTLSServer(handler)
	oldFactory := providerProbeClientFactory
	providerProbeClientFactory = func() *http.Client {
		client := upstream.Client()
		client.Timeout = 10 * time.Second
		return client
	}
	t.Cleanup(func() { providerProbeClientFactory = oldFactory })
	s, _ := newSpillTestServer(t)
	body := `{"display_name":"Probe Test","base_url":"` + upstream.URL + `/v1?discard=1","api_key":"` + canaryKey + `","enabled":true}`
	if rec := putProvider(t, s, body); rec.Code != http.StatusOK {
		t.Fatalf("provider fixture setup failed: status %d", rec.Code)
	}
	return s, upstream
}

func providerProbeRequest(t *testing.T, s *Server, scopes map[string]bool) *httptest.ResponseRecorder {
	t.Helper()
	return devtaskDo(t, s, devtaskCtx(scopes), http.MethodPost, "/api/v1/devtask-providers/global/test", "")
}

func TestProviderTestEndpointOK(t *testing.T) {
	var gotPath, gotAuth string
	s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	rec := providerProbeRequest(t, s, map[string]bool{"admin": true})
	var out struct {
		OK         bool  `json:"ok"`
		StatusCode int   `json:"status_code"`
		LatencyMS  int64 `json:"latency_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || !out.OK || out.StatusCode != http.StatusOK || out.LatencyMS < 0 {
		t.Fatalf("provider probe success response invalid: status=%d", rec.Code)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer "+canaryKey {
		t.Fatal("probe method/path/auth header mismatch")
	}
}

func TestProviderTestEndpointUnauthorized(t *testing.T) {
	s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	rec := providerProbeRequest(t, s, map[string]bool{"admin": true})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) || !strings.Contains(rec.Body.String(), `"status_code":401`) {
		t.Fatalf("unauthorized probe response invalid: status=%d", rec.Code)
	}
}

func TestProviderTestEndpointForbiddenAndOtherHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{http.StatusForbidden, "forbidden"}, {http.StatusServiceUnavailable, "http_503"}} {
		t.Run(tc.want, func(t *testing.T) {
			s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status) }))
			rec := providerProbeRequest(t, s, map[string]bool{"admin": true})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"error":"`+tc.want+`"`) || !strings.Contains(rec.Body.String(), `"status_code":`+fmt.Sprint(tc.status)) {
				t.Fatalf("upstream status mapping invalid: endpoint status=%d", rec.Code)
			}
		})
	}
}

type providerProbeRoundTripper func(*http.Request) (*http.Response, error)

func (f providerProbeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderTestEndpointNetworkError(t *testing.T) {
	s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	oldFactory := providerProbeClientFactory
	providerProbeClientFactory = func() *http.Client {
		return &http.Client{Timeout: 10 * time.Second, Transport: providerProbeRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	}
	t.Cleanup(func() { providerProbeClientFactory = oldFactory })
	rec := providerProbeRequest(t, s, map[string]bool{"admin": true})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"error":"network_error"`) || strings.Contains(rec.Body.String(), "offline") {
		t.Fatalf("network error mapping invalid: endpoint status=%d", rec.Code)
	}
}

func TestProviderTestEndpointTimeout(t *testing.T) {
	s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(11 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	started := time.Now()
	rec := providerProbeRequest(t, s, map[string]bool{"admin": true})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"error":"timeout"`) || time.Since(started) > 11*time.Second {
		t.Fatalf("timeout probe response invalid: status=%d", rec.Code)
	}
}

func TestProviderTestEndpointRequiresAdmin(t *testing.T) {
	s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := providerProbeRequest(t, s, map[string]bool{"devtask:submit": true})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin provider probe status=%d, want 403", rec.Code)
	}
}

func TestProviderTestEndpointAuditHasNoSecret(t *testing.T) {
	s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	if rec := providerProbeRequest(t, s, map[string]bool{"admin": true}); rec.Code != http.StatusOK {
		t.Fatalf("provider probe status=%d", rec.Code)
	}
	if err := s.Store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Store.DB.Query(`SELECT command, COALESCE(error_msg,''), target FROM audit_logs WHERE action=?`, "devtask_provider_test")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var command, errorMsg, target string
		if err := rows.Scan(&command, &errorMsg, &target); err != nil {
			t.Fatal(err)
		}
		count++
		if target != "global" || !strings.HasPrefix(command, "base_url=") || strings.Contains(command, "/") || strings.Contains(command, "?") {
			t.Fatal("provider test audit target/command is not host-only")
		}
		for _, field := range []string{command, errorMsg} {
			if strings.Contains(field, canaryKey) || strings.Contains(strings.ToLower(field), "api_key=") {
				t.Fatal("provider test audit leak detected")
			}
			for n := 4; n <= len(canaryKey); n++ {
				if strings.Contains(field, canaryKey[:n]) {
					t.Fatal("provider test audit leak detected")
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("provider test audit row count=%d, want 1", count)
	}
}

func TestProviderTestEndpointDoesNotLeakBody(t *testing.T) {
	upstreamBody := "sensitive-upstream-marker-" + canaryKey
	s, _ := providerProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	rec := providerProbeRequest(t, s, map[string]bool{"admin": true})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), upstreamBody) || strings.Contains(rec.Body.String(), canaryKey) || strings.Contains(rec.Body.String(), "sensitive-upstream-marker") {
		t.Fatal("provider probe response leaked upstream body")
	}
	for name, values := range rec.Header() {
		if strings.Contains(name, upstreamBody) || strings.Contains(name, canaryKey) || strings.Contains(name, "sensitive-upstream-marker") {
			t.Fatal("响应头泄露：检测到上游敏感标记")
		}
		for _, value := range values {
			if strings.Contains(value, upstreamBody) || strings.Contains(value, canaryKey) || strings.Contains(value, "sensitive-upstream-marker") {
				t.Fatal("响应头泄露：检测到上游敏感标记")
			}
		}
	}
	if n := countAudit(t, s, "action=? AND error_msg LIKE ?", "devtask_provider_test", "%sensitive-upstream-marker%"); n != 0 {
		t.Fatal("provider probe audit leaked upstream body")
	}
}

func TestProviderTestEndpointRespectsDevTaskGate(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "off")
	s, _ := newSpillTestServer(t)
	rec := providerProbeRequest(t, s, map[string]bool{"admin": true})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("provider test with DevTask gate off: status=%d, want 404", rec.Code)
	}
}
