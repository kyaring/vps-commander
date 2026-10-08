package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
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
	if got.Device != "wjyhk" || got.TargetType != "mcp_tool" || got.Target != "exec_command" {
		t.Fatalf("unexpected target/device: %+v", got)
	}
	if got.Transport != "mcp" {
		t.Fatalf("unexpected transport: %q", got.Transport)
	}
}
