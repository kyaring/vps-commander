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
