package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

func TestExecNonzeroExitIsAuditedSeparatelyFromToolErrors(t *testing.T) {
	principal := auth.Principal{Type: "api_token", ID: "tok-exit", TokenID: "tok-exit", Name: "dev-agent", Scopes: map[string]bool{"exec": true, "files:read": true}, Devices: map[string]bool{"wjyhk": true}}
	store, server, apiServer := newMCPAuditFixture(t)
	ctx := auth.WithPrincipal(mcp.WithTransport(context.Background(), "mcp-http"), principal)
	call := func(id int, tool, args string) mcp.CallToolResult {
		t.Helper()
		resp := server.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: id, Method: "tools/call", Params: json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args))})
		result, ok := resp.Result.(mcp.CallToolResult)
		if !ok {
			t.Fatalf("%s: unexpected response %#v", tool, resp)
		}
		return result
	}
	// Command ran but exited non-zero, failure text only on stdout (like go test).
	if r := call(1, "exec_command", `{"device":"wjyhk","command":"printf 'FAIL TestX\\nFAIL pkg'; exit 3"}`); r.IsError {
		t.Fatalf("non-zero exit must stay a normal tool result: %#v", r)
	}
	// grep with no match: exit 1, empty output.
	call(2, "exec_command", `{"device":"wjyhk","command":"grep -q no-such-text-xyz /dev/null"}`)
	// stderr wins over stdout.
	call(3, "exec_command", `{"device":"wjyhk","command":"echo out; echo boom >&2; exit 1"}`)
	// Successful command stays ok.
	call(4, "exec_command", `{"device":"wjyhk","command":"true"}`)
	// Tool-level failure (missing file) stays "error".
	missing := filepath.Join(t.TempDir(), "missing.txt")
	if r := call(5, "read_file", fmt.Sprintf(`{"device":"wjyhk","path":%q}`, missing)); !r.IsError {
		t.Fatalf("missing file should be a tool error: %#v", r)
	}

	// REST exec path.
	body := `{"device":"wjyhk","command":"printf rest-stdout-only; exit 2","timeout":10}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(body)).WithContext(auth.WithPrincipal(context.Background(), principal))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	apiServer.exec(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rest exec status=%d body=%s", w.Code, w.Body.String())
	}

	rows := auditRows(t, store)
	find := func(substr string) (result, msg string) {
		t.Helper()
		for _, row := range rows {
			if strings.Contains(row.Command, substr) {
				return row.Result, row.ErrorMsg
			}
		}
		t.Fatalf("no audit row for %q in %#v", substr, rows)
		return "", ""
	}
	if res, msg := find("FAIL TestX"); res != AuditResultExitNonzero || !strings.HasPrefix(msg, "stdout: ") || !strings.Contains(msg, "FAIL pkg") {
		t.Fatalf("stdout-only failure: result=%q msg=%q", res, msg)
	}
	if res, msg := find("no-such-text-xyz"); res != AuditResultExitNonzero || msg != "" {
		t.Fatalf("grep no match: result=%q msg=%q", res, msg)
	}
	if res, msg := find("echo boom"); res != AuditResultExitNonzero || msg != "boom" {
		t.Fatalf("stderr precedence: result=%q msg=%q", res, msg)
	}
	if res, _ := find("true"); res != "ok" {
		t.Fatalf("success: result=%q", res)
	}
	if res, _ := find(missing); res != "error" {
		t.Fatalf("tool failure must stay error: result=%q", res)
	}
	if res, msg := find("rest-stdout-only"); res != AuditResultExitNonzero || msg != "stdout: rest-stdout-only" {
		t.Fatalf("rest: result=%q msg=%q", res, msg)
	}
	// Filtering by the new result works.
	filtered, err := store.QueryAudits("", "", "", AuditResultExitNonzero, "", 0, 0, 0, 100)
	if err != nil || len(filtered) != 4 {
		t.Fatalf("filter exit_nonzero: n=%d err=%v", len(filtered), err)
	}
}

func TestExecAuditMessageBoundsAndFallback(t *testing.T) {
	long := strings.Repeat("a", 5000) + "TAIL"
	msg := execAuditMessage(executor.Result{ExitCode: 1, Stderr: long})
	if len([]rune(msg)) != maxExecAuditMessageRunes || !strings.HasSuffix(msg, "TAIL") || !strings.HasPrefix(msg, "…") {
		t.Fatalf("stderr tail not bounded: len=%d", len([]rune(msg)))
	}
	if got := execAuditMessage(executor.Result{ExitCode: 0, Stdout: "fine"}); got != "" {
		t.Fatalf("success with stdout should keep no message, got %q", got)
	}
	cjk := strings.Repeat("错", 3000)
	if got := execAuditMessage(executor.Result{ExitCode: 1, Stdout: cjk}); !strings.HasPrefix(got, "stdout: …") || len([]rune(got)) != len("stdout: ")+maxExecAuditMessageRunes {
		t.Fatalf("utf-8 tail broken: %d", len([]rune(got)))
	}
}
