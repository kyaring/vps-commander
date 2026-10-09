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
	var synchronous, checkpoint int
	if err := s.DB.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous=%d, want NORMAL (1)", synchronous)
	}
	if err := s.DB.QueryRow("PRAGMA wal_autocheckpoint").Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint != 1000 {
		t.Fatalf("wal_autocheckpoint=%d, want 1000", checkpoint)
	}
}

func TestUpdateRemoteProfilesBatchPreservesDeviceStatus(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err := s.UpsertRemote("node-1", "offline", "amd64", "linux", 100, `{"mem_percent":10}`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRemoteProfiles([]RemoteProfileUpdate{{
		Name: "node-1", LastHeartbeat: 200, Arch: "amd64", OS: "linux", ProfileJSON: `{"mem_percent":20}`,
	}}); err != nil {
		t.Fatal(err)
	}
	var status, profile string
	if err := s.DB.QueryRow("SELECT status, profile_json FROM devices WHERE name=?", "node-1").Scan(&status, &profile); err != nil {
		t.Fatal(err)
	}
	if status != "offline" {
		t.Fatalf("profile flush changed status to %q; want offline", status)
	}
	if profile != `{"mem_percent":20}` {
		t.Fatalf("profile=%s, want latest snapshot", profile)
	}
	// A queued profile from an older connection must not overwrite a newer hello.
	if err := s.UpsertRemote("node-1", "online", "amd64", "linux", 300, `{"mem_percent":30}`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRemoteProfiles([]RemoteProfileUpdate{{Name: "node-1", LastHeartbeat: 250, Arch: "amd64", OS: "linux", ProfileJSON: `{"mem_percent":25}`}}); err != nil {
		t.Fatal(err)
	}
	var heartbeat int64
	if err := s.DB.QueryRow("SELECT last_heartbeat, profile_json FROM devices WHERE name=?", "node-1").Scan(&heartbeat, &profile); err != nil {
		t.Fatal(err)
	}
	if heartbeat != 300 || profile != `{"mem_percent":30}` {
		t.Fatalf("stale batch overwrote newer snapshot: heartbeat=%d profile=%s", heartbeat, profile)
	}
}

func TestDeviceCredentialHashAndRotation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	if err := s.SetDeviceCredential("node-1", "secret-one"); err != nil {
		t.Fatal(err)
	}
	configured, err := s.HasDeviceCredential("node-1")
	if err != nil || !configured {
		t.Fatalf("credential should be configured: %v", err)
	}
	valid, err := s.VerifyDeviceCredential("node-1", "secret-one")
	if err != nil || !valid {
		t.Fatalf("valid credential rejected: %v", err)
	}
	valid, err = s.VerifyDeviceCredential("node-1", "wrong")
	if err != nil || valid {
		t.Fatal("wrong credential accepted")
	}
	if err := s.SetDeviceCredential("node-1", "secret-two"); err != nil {
		t.Fatal(err)
	}
	valid, err = s.VerifyDeviceCredential("node-1", "secret-one")
	if err != nil || valid {
		t.Fatal("old credential accepted after rotation")
	}
	valid, err = s.VerifyDeviceCredential("node-1", "secret-two")
	if err != nil || !valid {
		t.Fatal("rotated credential rejected")
	}
}
