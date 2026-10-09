package api

import (
	"github.com/wjyhk/vps-commander/internal/storage"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditCSVExportAddsTruncationMarker(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 4; i++ {
		if err := store.AuditEntry(storage.AuditEntry{ActorName: "token", Action: "exec", Command: "echo test", Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store}
	req := httptest.NewRequest("GET", "/api/v1/audits/export.csv?max_rows=2", nil)
	rec := httptest.NewRecorder()
	s.AuditExportCSV(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "# truncated at 2 rows") {
		t.Fatalf("missing truncation marker: %s", rec.Body.String())
	}
	if strings.Count(rec.Body.String(), "echo test") != 2 {
		t.Fatalf("expected two exported records: %s", rec.Body.String())
	}
}

func TestAuditCSVExportFirstPageFailureReturns500(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store}
	req := httptest.NewRequest("GET", "/api/v1/audits/export.csv", nil)
	rec := httptest.NewRecorder()
	s.AuditExportCSV(rec, req)
	if rec.Code != 500 {
		t.Fatalf("status=%d body=%s, want 500", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "text/csv") || strings.Contains(rec.Body.String(), "id,timestamp") {
		t.Fatalf("first-page failure must not return CSV: %s", rec.Body.String())
	}
}
