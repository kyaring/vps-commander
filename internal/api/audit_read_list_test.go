package api

import (
	"context"
	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
	"path/filepath"
	"testing"
)

func TestAuditReadListsOptInAndDeniedAlwaysRecorded(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &Server{Store: store}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "token-a", Name: "named-a"})
	t.Setenv("VPS_COMMANDER_AUDIT_READ_LIST", "off")
	actions := []string{"list_devices", "list_agent_mcp", "list_processes", "file_list", "file_info", "file_search"}
	for _, action := range actions {
		if err := s.auditContext(ctx, "127.0.0.1", "node-a", action, "/tmp", 0, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB.QueryRow("SELECT count(*) FROM audit_logs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("successful list with switch off wrote %d rows", count)
	}
	if err := s.auditContext(ctx, "127.0.0.1", "node-a", "scope_denied", "list_devices", 403, 0, "denied"); err != nil {
		t.Fatal(err)
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow("SELECT count(*) FROM audit_logs WHERE action='scope_denied'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("denied list with switch off has %d rows", count)
	}
	if err := s.auditContext(ctx, "127.0.0.1", "node-a", "file_list", "/tmp", 409, 0, "list failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow("SELECT count(*) FROM audit_logs WHERE action='file_list'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed list with switch off has %d rows", count)
	}
	t.Setenv("VPS_COMMANDER_AUDIT_READ_LIST", "on")
	for _, action := range actions {
		if err := s.auditContext(ctx, "127.0.0.1", "node-a", action, "/tmp", 0, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow("SELECT count(*) FROM audit_logs WHERE result='ok' AND action IN ('list_devices','list_agent_mcp','list_processes','file_list','file_info','file_search')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(actions) {
		t.Fatalf("successful lists with switch on wrote %d rows, want %d", count, len(actions))
	}
}
