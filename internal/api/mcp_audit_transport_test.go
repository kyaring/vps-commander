package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func newMCPAuditFixture(t *testing.T) (*storage.Store, *mcp.Server, *Server) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	apiServer := &Server{Store: store, LocalName: "wjyhk", Exec: executor.Local{MaxOutput: 1024 * 1024}}
	mcpServer := mcp.NewServer(apiServer.NewMCPBackend(), nil)
	mcpServer.SetLocalName("wjyhk")
	return store, mcpServer, apiServer
}

func auditRows(t *testing.T, store *storage.Store) []storage.AuditLog {
	t.Helper()
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryAudits("", "", "", "", "", 0, 0, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestNamedTokenAuditRecordsConcreteSSEAndStreamableHTTP(t *testing.T) {
	principal := auth.Principal{Type: "api_token", ID: "tok-1", TokenID: "tok-1", Name: "release-bot", Scopes: map[string]bool{"exec": true, "files:read": true}, Devices: map[string]bool{"wjyhk": true}}
	store, mcpServer, _ := newMCPAuditFixture(t)
	readPath := filepath.Join(t.TempDir(), "read-audit.txt")
	if err := os.WriteFile(readPath, []byte("auditable file payload"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/sse", func(w http.ResponseWriter, r *http.Request) {
		mcpServer.HandleSSE(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
	mux.HandleFunc("/mcp/message", func(w http.ResponseWriter, r *http.Request) {
		mcpServer.HandleMessage(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		mcpServer.HandleStreamableHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp/sse", nil)
	resp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	endpoint := ""
	for endpoint == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	if !strings.HasPrefix(endpoint, "/mcp/message?sessionId=") {
		t.Fatalf("unexpected SSE endpoint %q", endpoint)
	}
	bodySSE := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"exec_command","arguments":{"device":"wjyhk","command":"printf sse_audit_ok"}}}`
	postResp, err := http.Post(ts.URL+endpoint, "application/json", strings.NewReader(bodySSE))
	if err != nil {
		t.Fatal(err)
	}
	if postResp.StatusCode != http.StatusAccepted {
		_ = postResp.Body.Close()
		t.Fatalf("SSE message status=%d", postResp.StatusCode)
	}
	_ = postResp.Body.Close()
	// Consume the event to ensure the MCP tool call completed before inspecting audits.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for SSE JSON-RPC response")
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "sse_audit_ok") {
				goto sseDone
			}
		}
	}
sseDone:
	bodySSERead := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"device":"wjyhk","path":%q,"limit":128}}}`, readPath)
	postResp, err = http.Post(ts.URL+endpoint, "application/json", strings.NewReader(bodySSERead))
	if err != nil {
		t.Fatal(err)
	}
	if postResp.StatusCode != http.StatusAccepted {
		_ = postResp.Body.Close()
		t.Fatalf("SSE read status=%d", postResp.StatusCode)
	}
	_ = postResp.Body.Close()
	for {
		select {
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for SSE read response")
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "auditable file payload") {
				goto sseReadDone
			}
		}
	}
sseReadDone:
	bodyHTTP := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"exec_command","arguments":{"device":"wjyhk","command":"printf http_audit_ok"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(bodyHTTP))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var rpc map[string]any
	if err := json.NewDecoder(httpResp.Body).Decode(&rpc); err != nil {
		_ = httpResp.Body.Close()
		t.Fatal(err)
	}
	_ = httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("Streamable HTTP status=%d response=%v", httpResp.StatusCode, rpc)
	}
	bodyHTTPRead := fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_file","arguments":{"device":"wjyhk","path":%q,"limit":128}}}`, readPath)
	readReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(bodyHTTPRead))
	readReq.Header.Set("Content-Type", "application/json")
	readReq.Header.Set("Accept", "application/json")
	readResp, err := http.DefaultClient.Do(readReq)
	if err != nil {
		t.Fatal(err)
	}
	readRPC, readErr := io.ReadAll(readResp.Body)
	_ = readResp.Body.Close()
	if readErr != nil || readResp.StatusCode != http.StatusOK || !strings.Contains(string(readRPC), "auditable file payload") {
		t.Fatalf("Streamable HTTP read failed: status=%d err=%v body=%s", readResp.StatusCode, readErr, readRPC)
	}

	rows := auditRows(t, store)
	found := map[string]bool{}
	for _, row := range rows {
		if row.ActorName != "release-bot" {
			continue
		}
		if row.Action == "exec" && (row.TargetType != "device" || row.Target != "wjyhk" || row.Device != "wjyhk") {
			t.Errorf("incorrect device target: %+v", row)
		}
		if row.Action == "file_read" && row.Command == readPath {
			if row.TargetType != "file" || row.Target != readPath {
				t.Errorf("incorrect file read target: %+v", row)
			}
			if row.Transport == "mcp-sse" {
				found["sse-read"] = true
			}
			if row.Transport == "mcp-http" {
				found["http-read"] = true
			}
		}
		switch row.Command {
		case "printf sse_audit_ok":
			if row.Transport != "mcp-sse" || row.Result != "ok" {
				t.Errorf("incorrect SSE audit: %+v", row)
			}
			found["sse"] = true
		case "printf http_audit_ok":
			if row.Transport != "mcp-http" || row.Result != "ok" {
				t.Errorf("incorrect HTTP audit: %+v", row)
			}
			found["http"] = true
		}
	}
	if !found["sse"] || !found["http"] || !found["sse-read"] || !found["http-read"] {
		t.Fatalf("missing concrete transport audit records (found=%v rows=%+v)", found, rows)
	}
}

func TestMCPDeniedAuditRetainsIntentWithoutWriteBody(t *testing.T) {
	principal := auth.Principal{Type: "api_token", ID: "tok-denied", TokenID: "tok-denied", Name: "readonly-node-a", Scopes: map[string]bool{"files:read": true, "files:write": true}, Devices: map[string]bool{"wjyhk": true}}
	store, server, apiServer := newMCPAuditFixture(t)
	_ = apiServer
	call := func(ctx context.Context, id int, tool, args string) {
		t.Helper()
		resp := server.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: id, Method: "tools/call", Params: json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args))})
		result, ok := resp.Result.(mcp.CallToolResult)
		if !ok || !result.IsError {
			t.Fatalf("%s should be denied: %#v", tool, resp)
		}
	}
	ctx := auth.WithPrincipal(context.Background(), principal)
	call(ctx, 1, "exec_command", `{"device":"wjyhk","command":"echo forbidden-by-scope"}`)
	otherDevice := principal
	otherDevice.Scopes = map[string]bool{"files:read": true}
	otherDevice.Devices = map[string]bool{"node-b": true}
	call(auth.WithPrincipal(context.Background(), otherDevice), 2, "read_file", `{"device":"wjyhk","path":"/tmp/audit-read-path.txt"}`)

	long := strings.Repeat("x", 700)
	call(ctx, 3, "exec_command", fmt.Sprintf(`{"device":"wjyhk","command":%q}`, long))
	missingPath := filepath.Join(t.TempDir(), "missing-file.txt")
	call(ctx, 5, "read_file", fmt.Sprintf(`{"device":"wjyhk","path":%q}`, missingPath))

	// This succeeds, but only path and byte count may be retained; never file content.
	secret := "TOP_SECRET_FILE_BODY_7fa13"
	path := filepath.Join(t.TempDir(), "written.txt")
	writeArgs, _ := json.Marshal(map[string]any{"device": "wjyhk", "path": path, "content": secret})
	writeResp := server.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: 4, Method: "tools/call", Params: json.RawMessage(fmt.Sprintf(`{"name":"write_file","arguments":%s}`, writeArgs))})
	writeResult, ok := writeResp.Result.(mcp.CallToolResult)
	if !ok || writeResult.IsError {
		t.Fatalf("write_file failed: %#v", writeResp)
	}

	rows := auditRows(t, store)
	found := map[string]bool{}
	for _, row := range rows {
		switch {
		case row.Result == "denied" && strings.Contains(row.Command, "forbidden-by-scope"):
			if row.TargetType != "device" || row.Target != "wjyhk" {
				t.Errorf("scope denial target wrong: %+v", row)
			}
			found["scope"] = true
		case row.Result == "denied" && row.Command == "/tmp/audit-read-path.txt":
			if row.TargetType != "device" || row.Target != "wjyhk" {
				t.Errorf("device whitelist denial target wrong: %+v", row)
			}
			found["device"] = true
		case row.Result == "denied" && strings.HasPrefix(row.Command, strings.Repeat("x", 20)):
			if len([]rune(row.Command)) != 512 || !strings.HasSuffix(row.Command, "…") {
				t.Errorf("long command not truncated correctly: length=%d tail=%q", len([]rune(row.Command)), row.Command[len(row.Command)-8:])
			}
			found["truncated"] = true
		case row.Result == "error" && row.Action == "file_read" && row.Command == missingPath:
			found["read-error"] = true
		case row.Action == "file_write":
			if row.TargetType != "file" || row.Target != path {
				t.Errorf("write target mismatch: %+v", row)
			}
			if strings.Contains(row.Command, secret) || strings.Contains(row.ErrorMsg, secret) {
				t.Errorf("file body leaked into audit: %+v", row)
			}
			if !strings.Contains(row.Command, path) || !strings.Contains(row.Command, fmt.Sprintf("bytes=%d", len(secret))) {
				t.Errorf("write intent missing path/size: %+v", row)
			}
			found["write"] = true
		}
	}
	for _, key := range []string{"scope", "device", "truncated", "write", "read-error"} {
		if !found[key] {
			t.Errorf("missing %s audit; rows=%+v", key, rows)
		}
	}
}

func TestRESTScopeDenialCapturesAttemptedCommandAndDevice(t *testing.T) {
	store, _, server := newMCPAuditFixture(t)
	principal := auth.Principal{Type: "api_token", ID: "rest-ro", TokenID: "rest-ro", Name: "rest-readonly", Scopes: map[string]bool{"files:read": true}}
	body := `{"device":"wjyhk","command":"echo denied-rest-command"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(body)).WithContext(auth.WithPrincipal(context.Background(), principal))
	w := httptest.NewRecorder()
	server.requireScope("exec", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d", w.Code)
	}
	rows := auditRows(t, store)
	if len(rows) == 0 {
		t.Fatal("missing REST scope denial audit")
	}
	got := rows[0]
	if got.Transport != "rest" || got.Result != "denied" || got.Command != "echo denied-rest-command" || got.Device != "wjyhk" || got.TargetType != "device" || got.Target != "wjyhk" {
		t.Fatalf("incorrect REST denial intent: %+v", got)
	}
}

func TestAuditQueryMCPTransportPrefix(t *testing.T) {
	store, _, _ := newMCPAuditFixture(t)
	for _, transport := range []string{"mcp-sse", "mcp-http", "mcp"} {
		if err := store.AuditEntry(storage.AuditEntry{ActorName: "test", Transport: transport, Action: "transport-test", Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryAudits("", "", "", "", "mcp", 0, 0, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("transport=mcp returned %d rows, want mcp-sse, mcp-http and legacy mcp", len(rows))
	}
	if err := store.AuditEntry(storage.AuditEntry{ActorID: "legacy-env", ActorName: "legacy-env", ActorType: "api_token", Transport: "rest", Action: "legacy-test", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AuditEntry(storage.AuditEntry{ActorID: "panel", ActorName: "panel:admin", ActorType: "panel", Transport: "panel", Action: "panel-test", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.QueryAudits("legacy", "", "", "", "", 0, 0, 0, 500)
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy actor filter rows=%d err=%v", len(legacy), err)
	}
	panel, err := store.QueryAudits("panel", "", "", "", "", 0, 0, 0, 500)
	if err != nil || len(panel) != 1 {
		t.Fatalf("panel actor filter rows=%d err=%v", len(panel), err)
	}
}

func TestAuditCSVExportUsesCurrentFiltersAndActorLabels(t *testing.T) {
	store, _, server := newMCPAuditFixture(t)
	for _, entry := range []storage.AuditEntry{
		{ActorID: "token-a", ActorName: "named-a", ActorType: "api_token", Transport: "mcp-sse", Device: "node-a", Action: "exec", Command: "echo a", TargetType: "device", Target: "node-a", Result: "denied"},
		{ActorID: "token-b", ActorName: "named-b", ActorType: "api_token", Transport: "mcp-http", Device: "node-b", Action: "exec", Command: "echo b", TargetType: "device", Target: "node-b", Result: "ok"},
		{ActorID: "panel", ActorName: "panel:admin", ActorType: "panel", Transport: "panel", Action: "security_setting", Command: "mode=high", Result: "ok"},
	} {
		if err := store.AuditEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audits/export.csv?actor=named-a&transport=mcp&result=denied", nil)
	w := httptest.NewRecorder()
	server.AuditExportCSV(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("CSV response status=%d headers=%v", w.Code, w.Header())
	}
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("CSV rows=%d want header+1 match: %v", len(rows), rows)
	}
	if rows[1][2] != "named-a" || rows[1][4] != "mcp-sse" || rows[1][9] != "denied" || rows[1][10] != "echo a" {
		t.Fatalf("CSV row differs from filtered audit: %v", rows[1])
	}
	panelReq := httptest.NewRequest(http.MethodGet, "/api/v1/audits/export.csv?actor=panel", nil)
	panelW := httptest.NewRecorder()
	server.AuditExportCSV(panelW, panelReq)
	panelRows, err := csv.NewReader(strings.NewReader(panelW.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(panelRows) != 2 || panelRows[1][2] != "面板" {
		t.Fatalf("panel CSV label mismatch: %v", panelRows)
	}
}

func TestAuditActorFilterIncludesOAuthViaRows(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "oauth-actor-filter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{"chatgpt-app", "chatgpt-app via=oauth:client-123"} {
		if err := store.AuditEntry(storage.AuditEntry{ActorID: "token-id", ActorType: "api_token", ActorName: name, Transport: "mcp-http", Action: "mcp_call", Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.QueryAudits("chatgpt-app", "", "", "", "", 0, 0, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("named-token actor filter returned %d rows, want direct + OAuth", len(rows))
	}
}
