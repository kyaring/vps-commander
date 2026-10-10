package storage

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAuditConcurrent100NoLoss(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

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

	if err := s.FlushAudits(); err != nil {
		t.Fatalf("flush audits: %v", err)
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

func TestAuditCloseFlushesPendingEntries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "close-flush.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Audit("127.0.0.1", "node-a", "close_test", "queued", 0, 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.DB.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE action='close_test'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("close lost queued audit: got %d rows, want 1", count)
	}
}

func TestAuditFlushBarrierPersistsQueuedEntries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "flush-barrier.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 17; i++ {
		if err := s.Audit("127.0.0.1", "node-a", "barrier_test", fmt.Sprint(i), 0, 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE action='barrier_test'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 17 {
		t.Fatalf("flush barrier persisted %d entries, want 17", count)
	}
}

func TestAuditQueueFullFallsBackToSynchronousWrite(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "queue-full.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.auditWriteMu.Lock() // Hold the writer after its first batch starts, then fill all 1024 queue slots.
	for i := 0; i < auditBatchSize; i++ {
		if err := s.Audit("127.0.0.1", "node-a", "queue_full_test", fmt.Sprint(i), 0, 1, ""); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	time.Sleep(20 * time.Millisecond) // Let the writer take the first batch and block on auditWriteMu.
	for i := auditBatchSize; i < auditBatchSize+auditQueueCapacity; i++ {
		if err := s.Audit("127.0.0.1", "node-a", "queue_full_test", fmt.Sprint(i), 0, 1, ""); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.Audit("127.0.0.1", "node-a", "queue_full_test", "sync-fallback", 0, 1, "") }()
	time.Sleep(20 * time.Millisecond)
	s.auditWriteMu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("synchronous fallback failed: %v", err)
	}
	if err := s.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE action='queue_full_test'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != auditQueueCapacity+auditBatchSize+1 {
		t.Fatalf("queue-full path lost audits: got %d rows, want %d", count, auditQueueCapacity+auditBatchSize+1)
	}
}

func TestAuditTimestampIsCapturedAtEnqueue(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "event-timestamp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const eventTimestamp int64 = 1700000123
	if err := s.AuditEntry(AuditEntry{Timestamp: eventTimestamp, Device: "node-a", Action: "timestamp_test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := s.DB.QueryRow("SELECT timestamp FROM audit_logs WHERE action='timestamp_test'").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != eventTimestamp {
		t.Fatalf("timestamp changed during async write: got %d, want %d", got, eventTimestamp)
	}
}

func TestAuditWriteFailureFallsBackWithoutInfiniteRetry(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "audit-fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DROP TABLE audit_logs"); err != nil {
		t.Fatal(err)
	}
	if err := s.AuditEntry(AuditEntry{Device: "node-a", Action: "fallback_test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushAudits(); err == nil {
		t.Fatal("flush should report persistence failure")
	}
	if got := s.AuditFallbackCount(); got != 1 {
		t.Fatalf("fallback count=%d, want 1", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateAuditExtractsOAuthClientID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-oauth-migration.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO audit_logs(timestamp,actor_name,exit_code) VALUES(?,?,?)", time.Now().Unix(), "chatgpt-app via=oauth:client-123", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var actorName, oauthClientID string
	if err := s.DB.QueryRow("SELECT actor_name,oauth_client_id FROM audit_logs ORDER BY id DESC LIMIT 1").Scan(&actorName, &oauthClientID); err != nil {
		t.Fatal(err)
	}
	if actorName != "chatgpt-app" || oauthClientID != "client-123" {
		t.Fatalf("migrated actor_name=%q oauth_client_id=%q", actorName, oauthClientID)
	}
}

func TestNotificationAuditMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-notify.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO audit_logs(timestamp,caller_ip,device,action,command,exit_code,actor_name,transport) VALUES(1,'127.0.0.1','node-a','notification','webhook:old-target',0,'unknown(legacy)','legacy')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		s, err = Open(path)
		if err != nil {
			t.Fatalf("migration run %d: %v", run, err)
		}
		var actorType, actorID, actorName, transport string
		if err := s.DB.QueryRow(`SELECT actor_type,actor_id,actor_name,transport FROM audit_logs WHERE command='webhook:old-target'`).Scan(&actorType, &actorID, &actorName, &transport); err != nil {
			t.Fatal(err)
		}
		if actorType != "system" || actorID != "system:notify" || actorName != "system:notify" || transport != "internal" {
			t.Fatalf("run %d migrated attribution = %q/%q/%q/%q", run, actorType, actorID, actorName, transport)
		}
		var count int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE command='webhook:old-target'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("run %d duplicated rows: %d", run, count)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
