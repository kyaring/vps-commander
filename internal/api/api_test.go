package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func systemRequest(req *http.Request) *http.Request {
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		Type: "system", ID: "test-system", Name: "test-system",
		Scopes: map[string]bool{"exec": true, "files:read": true, "files:write": true, "process": true, "mcp:call": true, "admin": true},
	}))
}

func TestExecConcurrent100AuditsPersisted(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Exec: executor.Local{MaxOutput: 1024 * 1024}, Store: store, LocalName: "local"}

	const n = 100
	var wg sync.WaitGroup
	statuses := make(chan int, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/v1/exec", strings.NewReader("{\"device\":\"local\",\"command\":\"true\",\"timeout\":10}"))
			rec := httptest.NewRecorder()
			s.exec(rec, systemRequest(req))
			statuses <- rec.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Fatalf("unexpected HTTP status: %d", status)
		}
	}
	logs, err := store.RecentAudits(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != n {
		t.Fatalf("audit loss: got %d rows, want %d", len(logs), n)
	}
}

func TestSecurityEffectiveModeAndExecGate(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	s := &Server{Exec: executor.Local{MaxOutput: 1024 * 1024}, Store: store, LocalName: "local"}
	if got := s.GetEffectiveSecurityMode("node-a"); got != "unknown" {
		t.Fatalf("unknown mode=%s", got)
	}
	if err := store.SetNodeSecurityMode("node-a", "high"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetEffectiveSecurityMode("node-a"); got != "high" {
		t.Fatalf("override=%s", got)
	}
	if err := store.SetNodeSecurityMode("node-a", "inherit"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGlobalSecurityMode("low"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetEffectiveSecurityMode("node-a"); got != "low" {
		t.Fatalf("inherit=%s", got)
	}
	req := httptest.NewRequest("POST", "/api/v1/exec", strings.NewReader(`{"device":"node-a","command":"true"}`))
	rec := httptest.NewRecorder()
	s.exec(rec, systemRequest(req))
	if rec.Code != 403 {
		t.Fatalf("low exec status=%d", rec.Code)
	}
	logs, _ := store.RecentAudits(10)
	if len(logs) == 0 || logs[0].Action != "security_denied" {
		t.Fatalf("missing denial audit: %+v", logs)
	}
}

func TestProcessSessionLocalSnakeCaseAndLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Exec: executor.Local{MaxOutput: 1024 * 1024}, Store: store, LocalName: "local", LocalSessions: executor.NewSessionManager(0)}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/process/session?action=start", strings.NewReader(`{"device":"local","session_id":"session-test-001","command":"sleep 0.2; printf local-ok"}`))
	w := httptest.NewRecorder()
	s.processSession(w, systemRequest(req))
	if w.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/process/session?action=read", strings.NewReader(`{"device":"local","session_id":"session-test-001"}`))
		w = httptest.NewRecorder()
		s.processSession(w, systemRequest(req))
		if w.Code != http.StatusOK {
			t.Fatalf("read status=%d body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "local-ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for session output: %s", w.Body.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEditBlockSecurityAndReplacement(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("medium"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Exec: executor.Local{MaxOutput: 1024 * 1024}, Store: store, LocalName: "local"}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(path, []byte("alpha\nTARGET\nomega\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/devices/file/edit_block", strings.NewReader(fmt.Sprintf(`{"path":%q,"old_text":"TARGET","new_text":"CHANGED"}`, path)))
	w := httptest.NewRecorder()
	s.editBlock(w, systemRequest(r))
	if w.Code != 200 {
		t.Fatalf("edit status=%d body=%s", w.Code, w.Body.String())
	}
	b, _ := os.ReadFile(path)
	if string(b) != "alpha\nCHANGED\nomega\n" {
		t.Fatalf("unexpected content: %q", b)
	}

	if err := store.SetGlobalSecurityMode("low"); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/devices/file/edit_block", strings.NewReader(fmt.Sprintf(`{"path":%q,"old_text":"CHANGED","new_text":"NO"}`, path)))
	w = httptest.NewRecorder()
	s.editBlock(w, systemRequest(r))
	if w.Code != 403 {
		t.Fatalf("low mode status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestProvisionDeviceCredentialForNewOfflineDevice(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	s := &Server{
		Store:      store,
		LocalName:  "wjyhk",
		AdminToken: "admin-test-token",
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/security/device-credentials/provision", strings.NewReader("{\"device\":\"new-node-01\"}"))
	req.Header.Set("X-Admin-Token", "admin-test-token")
	w := httptest.NewRecorder()
	s.ProvisionDeviceCredentialJSON(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("provision status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "\"token\"") {
		t.Fatalf("missing token: %s", w.Body.String())
	}

	ok, err := store.HasDeviceCredential("new-node-01")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("credential was not persisted")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/security/device-credentials/provision", strings.NewReader("{\"device\":\"new-node-01\"}"))
	req.Header.Set("X-Admin-Token", "admin-test-token")
	w = httptest.NewRecorder()
	s.ProvisionDeviceCredentialJSON(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate provision status=%d body=%s", w.Code, w.Body.String())
	}
}
