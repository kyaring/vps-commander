package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestTokenDeviceWhitelistEnforcementAndAudit(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Exec:      executor.Local{MaxOutput: 1024 * 1024},
		Store:     store,
		LocalName: "control-node",
	}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		t.Fatal(err)
	}

	// Principal only allows "control-node"
	principalRestricted := auth.Principal{
		Type:    "api_token",
		ID:      "tok-1",
		Name:    "restricted-token",
		Scopes:  map[string]bool{"exec": true, "files:read": true},
		Devices: map[string]bool{"control-node": true},
	}

	// 1. Request to authorized device ("control-node") -> 200 OK
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(`{"device":"control-node","command":"echo ok"}`))
		req = req.WithContext(auth.WithPrincipal(req.Context(), principalRestricted))
		rec := httptest.NewRecorder()
		s.exec(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for allowed device, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	// 2. Request with "" or "local" maps to LocalName ("control-node") -> 200 OK
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(`{"device":"local","command":"echo ok"}`))
		req = req.WithContext(auth.WithPrincipal(req.Context(), principalRestricted))
		rec := httptest.NewRecorder()
		s.exec(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for normalized local device, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	// 3. Request to unauthorized device ("node-b") -> 403 Forbidden with device_denied
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(`{"device":"node-b","command":"echo ok"}`))
		req = req.WithContext(auth.WithPrincipal(req.Context(), principalRestricted))
		rec := httptest.NewRecorder()
		s.exec(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for unauthorized device, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "device denied") {
			t.Fatalf("expected 'device denied' in response body, got: %s", rec.Body.String())
		}
	}

	// 4. Verify audit log recorded "device_denied"
	audits, err := store.RecentAudits(10)
	if err != nil {
		t.Fatal(err)
	}
	foundDenied := false
	for _, a := range audits {
		if a.Action == "device_denied" && a.Device == "node-b" {
			foundDenied = true
			if a.ExitCode != 0 {
				t.Errorf("expected exit_code 0 for device denial, got %d", a.ExitCode)
			}
			if a.ActorName != "restricted-token" {
				t.Errorf("expected actor restricted-token, got %s", a.ActorName)
			}
			if a.TargetType != "device" || a.Target != "node-b" || a.Result != "denied" {
				t.Errorf("unexpected denial audit semantics: target_type=%s target=%s result=%s", a.TargetType, a.Target, a.Result)
			}
			break
		}
	}
	if !foundDenied {
		t.Fatalf("expected audit entry for device_denied on node-b, but none found in audits: %+v", audits)
	}
}

func TestRESTDeviceWhitelistMatrix(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "matrix.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, LocalName: "wjyhk", Exec: executor.Local{MaxOutput: 1024 * 1024}}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{
		Type: "api_token", ID: "matrix", Name: "matrix-token",
		Scopes: map[string]bool{
			"exec": true, "files:read": true, "files:write": true,
			"process": true, "mcp:call": true,
		},
		Devices: map[string]bool{"N3450": true},
	}
	tests := []struct {
		name string
		run  func(http.ResponseWriter, *http.Request)
		req  *http.Request
	}{
		{"exec", s.exec, httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(`{"device":"wjyhk","command":"id"}`))},
		{"file_read", s.fileRead, httptest.NewRequest(http.MethodPost, "/api/v1/file/read", strings.NewReader(`{"device":"wjyhk","path":"/etc/hosts"}`))},
		{"file_list", s.listDirectory, httptest.NewRequest(http.MethodPost, "/api/v1/file/list", strings.NewReader(`{"device":"wjyhk","path":"/"}`))},
		{"file_info", s.fileInfo, httptest.NewRequest(http.MethodPost, "/api/v1/file/info", strings.NewReader(`{"device":"wjyhk","path":"/etc/hosts"}`))},
		{"file_search", s.searchFiles, httptest.NewRequest(http.MethodPost, "/api/v1/file/search", strings.NewReader(`{"device":"wjyhk","root":"/tmp","query":"x"}`))},
		{"process", s.processSession, httptest.NewRequest(http.MethodPost, "/api/v1/process/session", strings.NewReader(`{"device":"wjyhk","session_id":"x","command":"id"}`))},
		{"file_write", s.fileWrite, httptest.NewRequest(http.MethodPost, "/api/v1/file/write", strings.NewReader(`{"device":"wjyhk","path":"/tmp/x","content":"x"}`))},
		{"edit_block", s.editBlock, httptest.NewRequest(http.MethodPost, "/api/v1/devices/file/edit_block", strings.NewReader(`{"device":"wjyhk","path":"/tmp/x","old_text":"x","new_text":"y"}`))},
		{"mcp_call", s.CallAgentMCPJSON, httptest.NewRequest(http.MethodPost, "/api/v1/mcp/call", strings.NewReader(`{"device":"wjyhk","server":"s","tool":"t"}`))},
		{"mcp_test", s.TestMCPJSON, httptest.NewRequest(http.MethodPost, "/api/v1/mcp/test", strings.NewReader(`{"device":"wjyhk","service":{"transport":"stdio","command":"id"}}`))},
		{"mcp_list", s.ListAgentMCPJSON, httptest.NewRequest(http.MethodGet, "/api/v1/mcp/list?device=wjyhk", nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req.WithContext(auth.WithPrincipal(tc.req.Context(), p))
			rec := httptest.NewRecorder()
			tc.run(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for unauthorized device, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeviceAccessRequiresPrincipal(t *testing.T) {
	s := &Server{LocalName: "wjyhk"}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(`{"device":"wjyhk","command":"id"}`))
	rec := httptest.NewRecorder()
	if s.requireDeviceAccess(rec, req, "wjyhk") {
		t.Fatal("expected device access to fail without principal")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without principal, got %d", rec.Code)
	}
}

func TestTokenDeviceNormalization(t *testing.T) {
	s := &Server{LocalName: "wjyhk"}
	got, err := s.normalizeTokenDevices([]string{"local", " wjyhk "})
	if err != nil {
		t.Fatalf("unexpected normalization error: %v", err)
	}
	if len(got) != 1 || got[0] != "wjyhk" {
		t.Fatalf("unexpected normalized devices: %#v", got)
	}
	if _, err := s.normalizeTokenDevices([]string{"LOCAL"}); err == nil {
		t.Fatal("expected unknown device error for LOCAL")
	}
	if _, err := s.normalizeTokenDevices([]string{"missing-node"}); err == nil {
		t.Fatal("expected unknown device error for missing-node")
	}
}
