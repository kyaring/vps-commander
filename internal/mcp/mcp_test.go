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
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
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

func TestMCPToolsListFiltersByScope(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "ro", Name: "readonly", Scopes: map[string]bool{"files:read": true}})
	resp := srv.HandleDirectRequest(ctx, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result: %#v", resp.Result)
	}
	tools, ok := result["tools"].([]Tool)
	if !ok {
		t.Fatalf("unexpected tools result: %#v", result["tools"])
	}
	foundReadFile := false
	for _, tool := range tools {
		if scope := mcpToolScope(tool.Name); scope != "" && scope != "files:read" {
			t.Fatalf("scope-filtered tool leaked: %s", tool.Name)
		}
		if tool.Name == "read_file" {
			foundReadFile = true
		}
	}
	if !foundReadFile {
		t.Fatal("read_file missing from scope-filtered tools/list")
	}
}

func TestMCPUnknownToolErrorIsExplicit(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "ro", Name: "readonly", Scopes: map[string]bool{"files:read": true}})
	resp := srv.HandleDirectRequest(ctx, JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: []byte(`{"name":"does_not_exist","arguments":{}}`)})
	result, ok := resp.Result.(CallToolResult)
	if !ok || !result.IsError || !strings.Contains(result.Content[0].Text, "unknown tool: does_not_exist") {
		t.Fatalf("unexpected unknown-tool response: %#v", resp)
	}
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
	if !ok || len(tools) != 12 {
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
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", TokenID: "test", Scopes: map[string]bool{"exec": true}, Devices: map[string]bool{"local": true}, MaxSessions: 1}))
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

func (g *gatedMockBackend) AuthorizeMCP(ctx context.Context, device, action, clientIP string) error {
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

func TestMCPToolScopeAndDeviceMatrix(t *testing.T) {
	cases := []struct{ name, scope string }{
		{"exec_command", "exec"},
		{"read_file", "files:read"},
		{"read_multiple_files", "files:read"},
		{"create_directory", "files:write"},
		{"move_file", "files:write"},
		{"list_processes", "process"},
		{"diagnostic_command", "files:read"},
		{"edit_block", "files:write"},
		{"write_file", "files:write"},
		{"list_agent_mcp", "files:read"},
		{"call_agent_mcp", "mcp:call"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := CallToolParams{Name: tc.name, Arguments: json.RawMessage(`{"device":"local"}`)}
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", TokenID: "t", Scopes: map[string]bool{tc.scope: true}, Devices: map[string]bool{"local": true}})
			res, failed := NewServer(&mockBackend{}, nil).callTool(ctx, "127.0.0.1", params)
			if failed && strings.Contains(fmt.Sprint(res), "scope denied") {
				t.Fatalf("valid scope denied: %v", res)
			}

			wrong := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", TokenID: "t", Scopes: map[string]bool{"devices:read": true}, Devices: map[string]bool{"local": true}})
			res, failed = NewServer(&mockBackend{}, nil).callTool(wrong, "127.0.0.1", params)
			if !failed || !strings.Contains(fmt.Sprint(res), "scope denied") {
				t.Fatalf("wrong scope allowed: %v", res)
			}

			denied := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", TokenID: "t", Scopes: map[string]bool{tc.scope: true}, Devices: map[string]bool{"N3450": true}})
			res, failed = NewServer(&mockBackend{}, nil).callTool(denied, "127.0.0.1", params)
			if !failed || !strings.Contains(fmt.Sprint(res), "device denied") {
				t.Fatalf("device whitelist bypassed: %v", res)
			}
		})
	}
}

func TestMCPListDevicesRequiresScope(t *testing.T) {
	params := CallToolParams{Name: "list_devices", Arguments: json.RawMessage("{}")}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", TokenID: "t", Scopes: map[string]bool{"devices:read": true}})
	if _, failed := NewServer(&mockBackend{}, nil).callTool(ctx, "127.0.0.1", params); failed {
		t.Fatal("devices:read should allow list_devices")
	}
	wrong := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", TokenID: "t", Scopes: map[string]bool{"files:read": true}})
	res, failed := NewServer(&mockBackend{}, nil).callTool(wrong, "127.0.0.1", params)
	if !failed || !strings.Contains(fmt.Sprint(res), "scope denied") {
		t.Fatalf("list_devices scope bypassed: %v", res)
	}
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

func TestStreamableHTTPInitializeAndNotification(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	principal := auth.Principal{Type: "api_token", TokenID: "streamable-test", Scopes: map[string]bool{"files:read": true}}
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
	w := httptest.NewRecorder()
	srv.HandleStreamableHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var resp JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok || result["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize protocol version = %#v, want 2025-03-26", resp.Result)
	}

	notify := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	notify.Header.Set("Content-Type", "application/json")
	notify.Header.Set("Accept", "application/json, text/event-stream")
	notify = notify.WithContext(auth.WithPrincipal(notify.Context(), principal))
	nw := httptest.NewRecorder()
	srv.HandleStreamableHTTP(nw, notify)
	if nw.Code != http.StatusAccepted || nw.Body.Len() != 0 {
		t.Fatalf("notification response = status %d body %q, want 202 with empty body", nw.Code, nw.Body.String())
	}
}

func TestStreamableHTTPRejectsWrongContentType(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "text/plain")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", TokenID: "t"}))
	w := httptest.NewRecorder()
	srv.HandleStreamableHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", w.Code)
	}
}

func TestSSESessionIdleDeadlineAndQueueFullClosesSession(t *testing.T) {
	sess := newSession("token-a")
	sess.lastActivity.Store(time.Now().Add(-31 * time.Minute).UnixNano())
	if sess.idleFor() < sseSessionIdleTimeout {
		t.Fatalf("session idle for %s, want at least %s", sess.idleFor(), sseSessionIdleTimeout)
	}

	srv := NewServer(&mockBackend{}, nil)
	sessionID := "full-queue-session"
	sess = newSession("token-a")
	for i := 0; i < cap(sess.ch); i++ {
		sess.ch <- []byte(`queued`)
	}
	srv.mu.Lock()
	srv.sessions[sessionID] = sess
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/mcp/message?sessionId="+sessionID, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", TokenID: "token-a"}))
	w := httptest.NewRecorder()
	srv.HandleMessage(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("full queue status = %d, want 503: %s", w.Code, w.Body.String())
	}
	select {
	case <-sess.done:
	default:
		t.Fatal("session was not closed after outbound queue timeout")
	}
	srv.mu.RLock()
	_, exists := srv.sessions[sessionID]
	srv.mu.RUnlock()
	if exists {
		t.Fatal("timed-out session remained registered")
	}
}

func TestStreamableHTTPSessionOptionalAndTokenBound(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	principal := auth.Principal{Type: "api_token", TokenID: "streamable-owner", Scopes: map[string]bool{"devices:read": true}}
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`
	initReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initBody))
	initReq.Header.Set("Content-Type", "application/json")
	initReq = initReq.WithContext(auth.WithPrincipal(initReq.Context(), principal))
	initW := httptest.NewRecorder()
	srv.HandleStreamableHTTP(initW, initReq)
	if initW.Code != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", initW.Code, initW.Body.String())
	}
	sessionID := initW.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize did not return optional Mcp-Session-Id")
	}

	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	listReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listBody))
	listReq.Header.Set("Content-Type", "application/json")
	listReq.Header.Set("Mcp-Session-Id", sessionID)
	listReq = listReq.WithContext(auth.WithPrincipal(listReq.Context(), principal))
	listW := httptest.NewRecorder()
	srv.HandleStreamableHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("same-token session request status = %d: %s", listW.Code, listW.Body.String())
	}

	other := auth.Principal{Type: "api_token", TokenID: "different-token", Scopes: map[string]bool{"devices:read": true}}
	crossReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listBody))
	crossReq.Header.Set("Content-Type", "application/json")
	crossReq.Header.Set("Mcp-Session-Id", sessionID)
	crossReq = crossReq.WithContext(auth.WithPrincipal(crossReq.Context(), other))
	crossW := httptest.NewRecorder()
	srv.HandleStreamableHTTP(crossW, crossReq)
	if crossW.Code != http.StatusForbidden {
		t.Fatalf("cross-token session status = %d, want 403", crossW.Code)
	}

	statelessReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listBody))
	statelessReq.Header.Set("Content-Type", "application/json")
	statelessReq = statelessReq.WithContext(auth.WithPrincipal(statelessReq.Context(), principal))
	statelessW := httptest.NewRecorder()
	srv.HandleStreamableHTTP(statelessW, statelessReq)
	if statelessW.Code != http.StatusOK {
		t.Fatalf("stateless request status = %d: %s", statelessW.Code, statelessW.Body.String())
	}
}

func TestStreamableHTTPRequiresPrincipal(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.HandleStreamableHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
