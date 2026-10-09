package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestMetricsPrometheusOutputAnd429Counter(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AuditEntry(storage.AuditEntry{Action: "metrics_test", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	if store.LastSQLiteWriteLatency() <= 0 {
		t.Fatal("SQLite audit write latency was not recorded")
	}
	server := &Server{Store: store, TokenLimiter: NewTokenLimiter(1)}
	server.RecordHTTPStatus(http.StatusTooManyRequests)
	server.RecordHTTPStatus(http.StatusOK)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", ID: "admin", Name: "admin", TokenID: "admin", Scopes: map[string]bool{"admin": true}}))
	w := httptest.NewRecorder()
	server.Metrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Fatalf("content-type=%q", got)
	}
	for _, metric := range []string{
		"vps_commander_online_agents 0",
		`vps_commander_mcp_sessions{transport="legacy_sse"} 0`,
		`vps_commander_mcp_sessions{transport="streamable_http"} 0`,
		"vps_commander_in_flight_exec 0",
		"vps_commander_audit_queue_depth 0",
		"vps_commander_sqlite_write_latency_seconds ",
		"vps_commander_http_429_total 1",
		"vps_commander_goroutines ",
		"vps_commander_process_resident_memory_bytes ",
	} {
		if !strings.Contains(w.Body.String(), metric) {
			t.Errorf("metrics missing %q", metric)
		}
	}
}

func TestMetricsRequiresAdminScope(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{Store: store}
	h := server.requireScope("admin", http.HandlerFunc(server.Metrics))
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", TokenID: "readonly", Scopes: map[string]bool{"devices:read": true}}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", w.Code)
	}
}
