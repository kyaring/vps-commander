package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestMCPAdapterAuditDeniedUsesRealHubBackend(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	s := &Server{Store: store, LocalName: "wjyhk"}
	backend := s.NewMCPBackend()
	mcpServer := mcp.NewServer(backend, nil)
	mcpServer.SetLocalName("wjyhk")

	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "token-1", Name: "readonly", TokenID: "token-1", Scopes: map[string]bool{"files:read": true}, Devices: map[string]bool{"wjyhk": true}})
	req := mcp.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: []byte("{\"name\":\"exec_command\",\"arguments\":{\"device\":\"local\",\"command\":\"id\"}}")}
	resp := mcpServer.HandleDirectRequest(ctx, req)
	result, ok := resp.Result.(mcp.CallToolResult)
	if !ok || !result.IsError {
		t.Fatalf("expected exec authorization denial: %#v", resp)
	}

	logs, err := store.RecentAudits(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("expected MCP denial audit")
	}
	got := logs[0]
	if got.Action != "mcp_denied" || got.Result != "denied" {
		t.Fatalf("unexpected audit: %+v", got)
	}
	if got.Device != "wjyhk" || got.TargetType != "device" || got.Target != "wjyhk" || got.Command != "id" {
		t.Fatalf("unexpected target/device: %+v", got)
	}
	if got.Transport != "mcp" {
		t.Fatalf("unexpected transport: %q", got.Transport)
	}
}

func TestMCPAdapterAuditSuccessUsesMCPContext(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}

	s := &Server{Store: store, LocalName: "wjyhk", Exec: executor.Local{MaxOutput: 1024 * 1024}}
	backend := s.NewMCPBackend()
	mcpServer := mcp.NewServer(backend, nil)
	mcpServer.SetLocalName("wjyhk")
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "token-2", Name: "exec-user", TokenID: "token-2", Scopes: map[string]bool{"exec": true}, Devices: map[string]bool{"wjyhk": true}})
	req := mcp.JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: []byte("{\"name\":\"exec_command\",\"arguments\":{\"device\":\" wjyhk \",\"command\":\"hostname\"}}")}
	resp := mcpServer.HandleDirectRequest(ctx, req)
	result, ok := resp.Result.(mcp.CallToolResult)
	if !ok || result.IsError {
		t.Fatalf("expected successful exec: %#v", resp)
	}
	logs, err := store.RecentAudits(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("expected MCP success audit")
	}
	got := logs[0]
	if got.Action != "exec" || got.Result != "ok" || got.Transport != "mcp" {
		t.Fatalf("unexpected audit: %+v", got)
	}
	if got.Device != "wjyhk" || got.TargetType != "device" || got.Target != "wjyhk" || got.Command != "hostname" {
		t.Fatalf("unexpected target/device: %+v", got)
	}
}

func TestMCPAdapterSecurityDenialNormalizesDeviceAndAudits(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("low"); err != nil {
		t.Fatal(err)
	}

	s := &Server{Store: store, LocalName: "wjyhk", Exec: executor.Local{MaxOutput: 1024 * 1024}}
	backend := s.NewMCPBackend()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "token-3", Name: "exec-user", TokenID: "token-3", Scopes: map[string]bool{"exec": true}, Devices: map[string]bool{"wjyhk": true}})
	_, err = backend.ExecCommand(ctx, "198.51.100.90", " wjyhk ", "hostname", "", 5)
	if err == nil {
		t.Fatal("expected security denial")
	}
	logs, err := store.RecentAudits(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("expected security denial audit")
	}
	got := logs[0]
	if got.Action != "security_denied" || got.Result != "denied" || got.Transport != "mcp" {
		t.Fatalf("unexpected audit: %+v", got)
	}
	if got.Device != "wjyhk" || got.TargetType != "device" || got.Target != "wjyhk" || got.ActorID != "token-3" {
		t.Fatalf("unexpected security audit: %+v", got)
	}
}
