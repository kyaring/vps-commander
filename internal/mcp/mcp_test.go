package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type mockBackend struct{}

func (m *mockBackend) ListDevices(ctx context.Context) (any, error) {
	return []map[string]any{{"name": "local", "status": "online"}}, nil
}
func (m *mockBackend) ExecCommand(ctx context.Context, clientIP, device, command, workdir string, timeout int) (any, error) {
	return map[string]any{"device": device, "exit_code": 0, "stdout": "ok"}, nil
}
func (m *mockBackend) ReadFile(ctx context.Context, clientIP, device, path string, offset, limit int64) (any, error) {
	return map[string]any{"device": device, "path": path, "content": "hello"}, nil
}
func (m *mockBackend) WriteFile(ctx context.Context, clientIP, device, path, content string) (any, error) {
	return map[string]any{"device": device, "path": path, "bytes": len(content)}, nil
}
func (m *mockBackend) EditBlock(ctx context.Context, clientIP, device, path, oldText, newText string) (any, error) {
	return map[string]any{"device": device, "path": path, "bytes_written": len(newText)}, nil
}

func TestMCPInitializeAndListTools(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)

	// Test Initialize
	initReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	}
	body, _ := json.Marshal(initReq)
	req := httptest.NewRequest("POST", "/mcp/message", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleMessage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	// Test tools/list
	listReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}
	body, _ = json.Marshal(listReq)
	req = httptest.NewRequest("POST", "/mcp/message", bytes.NewReader(body))
	w = httptest.NewRecorder()
	srv.HandleMessage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var listResp JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	resMap, ok := listResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result")
	}
	tools, ok := resMap["tools"].([]any)
	if !ok || len(tools) != 11 {
		t.Fatalf("expected 11 tools, got %d", len(tools))
	}
	foundWriteFile := false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is not an object: %#v", raw)
		}
		name, _ := tool["name"].(string)
		if name == "" {
			t.Fatalf("tool entry has empty name: %#v", tool)
		}
		if name == "write_file" {
			foundWriteFile = true
		}
	}
	if !foundWriteFile {
		t.Fatalf("write_file missing from tools/list")
	}
}

func TestMCPCallTool(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)

	callReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"exec_command","arguments":{"command":"echo test"}}`),
	}
	body, _ := json.Marshal(callReq)
	req := httptest.NewRequest("POST", "/mcp/message", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleMessage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	contentStr := string(w.Body.Bytes())
	if !strings.Contains(contentStr, "stdout") {
		t.Fatalf("expected stdout in result, got %s", contentStr)
	}
}

type gatedMockBackend struct {
	mockBackend
	deniedAction string
}

func (g *gatedMockBackend) AuthorizeMCP(device, action, clientIP string) error {
	if action == g.deniedAction {
		return fmt.Errorf("denied %s", action)
	}
	return nil
}

func (g *gatedMockBackend) ListAgentMCP(ctx context.Context, device string) (any, error) {
	return map[string]any{"device": device}, nil
}

func (g *gatedMockBackend) CallAgentMCP(ctx context.Context, clientIP, device, server, tool string, args any) (any, error) {
	return map[string]any{"device": device, "server": server, "tool": tool}, nil
}

func TestMCPAgentCallUsesSecurityGate(t *testing.T) {
	backend := &gatedMockBackend{deniedAction: "mcp"}
	srv := NewServer(backend, nil)
	params := json.RawMessage("{\"name\":\"call_agent_mcp\",\"arguments\":{\"device\":\"node1\",\"server\":\"s\",\"tool\":\"t\"}}")
	req := JSONRPCRequest{JSONRPC: "2.0", ID: 10, Method: "tools/call", Params: params}
	resp := srv.HandleDirectRequest(context.Background(), req)
	result, ok := resp.Result.(CallToolResult)
	if !ok || !result.IsError || !strings.Contains(result.Content[0].Text, "denied mcp") {
		t.Fatalf("expected MCP denial, got %#v", resp)
	}
}

func TestMCPListAgentUsesReadGate(t *testing.T) {
	backend := &gatedMockBackend{deniedAction: "read"}
	srv := NewServer(backend, nil)
	params := json.RawMessage("{\"name\":\"list_agent_mcp\",\"arguments\":{\"device\":\"node1\"}}")
	req := JSONRPCRequest{JSONRPC: "2.0", ID: 11, Method: "tools/call", Params: params}
	resp := srv.HandleDirectRequest(context.Background(), req)
	result, ok := resp.Result.(CallToolResult)
	if !ok || !result.IsError || !strings.Contains(result.Content[0].Text, "denied read") {
		t.Fatalf("expected MCP list denial, got %#v", resp)
	}
}
