package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditCommandRedactedBeforeTruncation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Repeat("x", 495) + "token = SECRET_BOUNDARY_ABC123" + strings.Repeat("z", 600)
	if err := store.AuditEntry(AuditEntry{Action: "exec", Command: command, Result: "denied", ErrorMsg: "authorization: Bearer ERROR_SECRET_456"}); err != nil {
		t.Fatal(err)
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var savedCommand, savedError string
	if err := store.DB.QueryRow("SELECT command,error_msg FROM audit_logs ORDER BY id DESC LIMIT 1").Scan(&savedCommand, &savedError); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET_BOUNDARY_ABC123", "SECRET_BOUNDARY", "ABC123", "ERROR_SECRET_456", "ERROR_SECRET"} {
		if strings.Contains(savedCommand, secret) || strings.Contains(savedError, secret) {
			t.Fatalf("audit leaked secret fragment %q: command=%q error=%q", secret, savedCommand, savedError)
		}
	}
	if len([]rune(savedCommand)) > 512 {
		t.Fatalf("stored command has %d runes", len([]rune(savedCommand)))
	}
	if !strings.Contains(savedError, "[REDACTED]") {
		t.Fatalf("error message was not redacted: %q", savedError)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
