package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestRequireScopeWritesDeniedAudit(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	s := &Server{Store: store, LocalName: "wjyhk"}
	p := auth.Principal{Type: "api_token", ID: "token-ro", Name: "readonly", Scopes: map[string]bool{"files:read": true}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", nil).WithContext(auth.WithPrincipal(context.Background(), p))
	w := httptest.NewRecorder()
	s.requireScope("exec", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d", w.Code)
	}
	logs, err := store.RecentAudits(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("expected scope denial audit")
	}
	got := logs[0]
	if got.Action != "scope_denied" || got.Result != "denied" || got.ActorID != "token-ro" {
		t.Fatalf("unexpected audit: %+v", got)
	}
}
