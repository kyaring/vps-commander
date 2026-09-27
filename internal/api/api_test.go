package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestExecConcurrent100AuditsPersisted(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

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
			s.exec(rec, req)
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
