package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestMCPBackendHonorsSecuritySnapshot(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("low"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, LocalName: "local", Exec: executor.Local{MaxOutput: 1024 * 1024}}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		t.Fatal(err)
	}
	b := s.NewMCPBackend()
	if _, err := b.ExecCommand(context.Background(), "test", "local", "true", "", 5); err == nil {
		t.Fatal("MCP exec must be denied in low mode")
	}
	if _, err := b.ReadFile(context.Background(), "test", "local", "/etc/hosts", 0, 128); err != nil {
		t.Fatalf("MCP read should remain available in low mode: %v", err)
	}
}

func TestUnknownDeviceFailsClosed(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, LocalName: "local", Exec: executor.Local{MaxOutput: 1024 * 1024}}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		t.Fatal(err)
	}
	if got := s.GetEffectiveSecurityMode("not-registered"); got != "unknown" {
		t.Fatalf("unknown device mode=%q", got)
	}
	b := s.NewMCPBackend()
	if _, err := b.ExecCommand(context.Background(), "test", "not-registered", "true", "", 5); err == nil {
		t.Fatal("unknown device must not inherit high risk")
	}
}
