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
		if a.Action == "device_denied" && a.Device == "node-b" && a.ExitCode == http.StatusForbidden {
			foundDenied = true
			if a.ActorName != "restricted-token" {
				t.Errorf("expected actor restricted-token, got %s", a.ActorName)
			}
			break
		}
	}
	if !foundDenied {
		t.Fatalf("expected audit entry for device_denied on node-b, but none found in audits: %+v", audits)
	}
}
