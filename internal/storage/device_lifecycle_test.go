package storage

import (
	"path/filepath"
	"testing"
)

func TestDeviceLifecyclePersistence(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	if err := s.UpsertRemote("node1", "online", "amd64", "linux", 100, "{\"role\":\"APPLICATION\"}"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListDevices()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].Status != "online" || rows[0].Arch != "amd64" || rows[0].OS != "linux" {
		t.Fatalf("unexpected row: %+v", rows[0])
	}

	if err := s.UpdateDeviceStatus("node1", "offline", 120); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListDevices()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].Status != "offline" || rows[0].OfflineAt == nil {
		t.Fatalf("offline state not persisted: %+v", rows[0])
	}

	if err := s.RevokeDevice("node1", "test"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("revoked device still present: %+v", rows)
	}
	revoked, err := s.IsRevoked("node1")
	if err != nil || !revoked {
		t.Fatalf("revoked=%v err=%v", revoked, err)
	}
}
