package cluster

import (
	"path/filepath"
	"testing"

	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestProfileQueueCoalescesLatestSnapshot(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.UpsertRemote("node-1", "online", "amd64", "linux", 100, `{}`); err != nil {
		t.Fatal(err)
	}
	m := NewManager("", store)
	m.queueProfile(storage.RemoteProfileUpdate{Name: "node-1", LastHeartbeat: 101, Arch: "amd64", OS: "linux", ProfileJSON: `{"version":1}`})
	m.queueProfile(storage.RemoteProfileUpdate{Name: "node-1", LastHeartbeat: 102, Arch: "amd64", OS: "linux", ProfileJSON: `{"version":2}`})
	m.flushProfiles()
	var profile string
	if err := store.DB.QueryRow("SELECT profile_json FROM devices WHERE name=?", "node-1").Scan(&profile); err != nil {
		t.Fatal(err)
	}
	if profile != `{"version":2}` {
		t.Fatalf("profile=%s, want newest queued snapshot", profile)
	}
	m.profileMu.Lock()
	pending := len(m.pendingProfiles)
	m.profileMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending profiles=%d after successful flush, want 0", pending)
	}
}

func TestProfileFlushFailureRestoresQueue(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager("", store)
	m.queueProfile(storage.RemoteProfileUpdate{Name: "node-1", LastHeartbeat: 101, Arch: "amd64", OS: "linux", ProfileJSON: `{"version":1}`})
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	m.flushProfiles()
	m.profileMu.Lock()
	defer m.profileMu.Unlock()
	if len(m.pendingProfiles) != 1 {
		t.Fatalf("failed flush should preserve retry, pending=%d", len(m.pendingProfiles))
	}
	if got := m.pendingProfiles["node-1"].ProfileJSON; got != `{"version":1}` {
		t.Fatalf("retry profile=%s", got)
	}
}

func TestCloseFlushesQueuedProfiles(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.UpsertRemote("node-1", "online", "amd64", "linux", 100, `{}`); err != nil {
		t.Fatal(err)
	}
	m := NewManager("", store)
	m.queueProfile(storage.RemoteProfileUpdate{Name: "node-1", LastHeartbeat: 101, Arch: "amd64", OS: "linux", ProfileJSON: `{"version":1}`})
	m.Close()
	var profile string
	if err := store.DB.QueryRow("SELECT profile_json FROM devices WHERE name=?", "node-1").Scan(&profile); err != nil {
		t.Fatal(err)
	}
	if profile != `{"version":1}` {
		t.Fatalf("shutdown flush profile=%s", profile)
	}
}

func TestHelloDropsQueuedProfile(t *testing.T) {
	m := NewManager("", nil)
	m.queueProfile(storage.RemoteProfileUpdate{Name: "node-1", LastHeartbeat: 101, ProfileJSON: `{"old":true}`})
	m.dropPendingProfile("node-1")
	m.profileMu.Lock()
	defer m.profileMu.Unlock()
	if _, ok := m.pendingProfiles["node-1"]; ok {
		t.Fatal("hello/disconnect cleanup should drop stale queued profile")
	}
}
