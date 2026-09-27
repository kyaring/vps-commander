package storage

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestAuditConcurrent100NoLoss(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs <- s.Audit("127.0.0.1", "local", "exec", fmt.Sprintf("echo %d", i), 0, 1, "")
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("audit write failed: %v", err)
		}
	}

	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit_logs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("audit loss: got %d rows, want %d", count, n)
	}

	var journal string
	if err := s.DB.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode=%q, want wal", journal)
	}

	var busy int
	if err := s.DB.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy < 5000 {
		t.Fatalf("busy_timeout=%d, want >=5000", busy)
	}
}
