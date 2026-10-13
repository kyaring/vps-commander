package api

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
)

func TestAuditDeniedSSEExecScopeAndCredentialRedaction(t *testing.T) {
	principal := auth.Principal{
		Type: "api_token", ID: "tok-sse-denied", TokenID: "tok-sse-denied", Name: "scope-limited-sse",
		Scopes: map[string]bool{"files:read": true}, Devices: map[string]bool{"wjyhk": true},
	}
	store, mcpServer, _ := newMCPAuditFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/sse", func(w http.ResponseWriter, r *http.Request) {
		mcpServer.HandleSSE(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
	mux.HandleFunc("/mcp/message", func(w http.ResponseWriter, r *http.Request) {
		mcpServer.HandleMessage(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE handshake status=%d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	endpoint := ""
	for endpoint == "" {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read SSE endpoint: %v", readErr)
		}
		if strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	if !strings.HasPrefix(endpoint, "/mcp/message?sessionId=") {
		t.Fatalf("unexpected SSE message endpoint %q", endpoint)
	}

	postDenied := func(id int, command string) {
		t.Helper()
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"exec_command","arguments":{"device":"wjyhk","command":%q}}}`, id, command)
		postResp, postErr := http.Post(ts.URL+endpoint, "application/json", strings.NewReader(body))
		if postErr != nil {
			t.Fatal(postErr)
		}
		_ = postResp.Body.Close()
		if postResp.StatusCode != http.StatusAccepted {
			t.Fatalf("SSE message id=%d status=%d, want %d", id, postResp.StatusCode, http.StatusAccepted)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				t.Fatalf("read SSE response for id=%d: %v", id, readErr)
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, fmt.Sprintf(`"id":%d`, id)) {
				return
			}
		}
		t.Fatalf("timed out waiting for SSE JSON-RPC response id=%d", id)
	}

	originalCommand := "echo denied-sse-original-command"
	postDenied(1, originalCommand)
	credentialCommand := `curl -H 'Authorization: Basic hark-test-cred' https://example.invalid/denied-audit-test`
	postDenied(2, credentialCommand)

	rows := auditRows(t, store)
	foundOriginal, foundRedacted := false, false
	for _, row := range rows {
		if row.Result != "denied" {
			continue
		}
		if row.Transport != "mcp-sse" {
			t.Errorf("denied MCP message transport = %q, want mcp-sse: %+v", row.Transport, row)
		}
		if strings.Contains(row.Command, "denied-sse-original-command") {
			if !strings.Contains(row.Command, originalCommand) {
				t.Errorf("denied audit command %q does not preserve original command %q", row.Command, originalCommand)
			}
			foundOriginal = true
		}
		if strings.Contains(row.Command, "denied-audit-test") {
			if !strings.Contains(row.Command, "Authorization: Basic [REDACTED]") {
				t.Errorf("credential was not redacted in denied audit command: %q", row.Command)
			}
			if strings.Contains(row.Command, "hark-test-cred") {
				t.Errorf("credential leaked into denied audit command: %q", row.Command)
			}
			foundRedacted = true
		}
	}
	if !foundOriginal {
		t.Errorf("missing denied mcp-sse audit retaining original command %q; rows=%+v", originalCommand, rows)
	}
	if !foundRedacted {
		t.Errorf("missing denied mcp-sse audit for credential-bearing command; rows=%+v", rows)
	}
}
