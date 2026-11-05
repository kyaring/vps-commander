package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
)

func devicesOnly() auth.Principal {
	return auth.Principal{Type: "api_token", Name: "reader", Scopes: map[string]bool{"devices:read": true}}
}

func allScopes() auth.Principal {
	return auth.Principal{Type: "api_token", Name: "full", Scopes: map[string]bool{
		"devices:read": true, "exec": true, "files:read": true, "files:write": true, "process": true, "mcp:call": true,
	}}
}

// A token that cannot run commands must not be told about command tools.
func TestInstructionsMatchVisibleTools(t *testing.T) {
	low := serverInstructions(devicesOnly(), true)
	for _, banned := range []string{"exec_", "【执行】", "【输出】", "call_agent_mcp", "【文件】", "【写入】"} {
		if strings.Contains(low, banned) {
			t.Fatalf("devices-only instructions mention %q", banned)
		}
	}
	full := serverInstructions(allScopes(), true)
	for _, want := range []string{"exec_start", "exec_status", "@hub-spill", "edit_block", "call_agent_mcp"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full-scope instructions missing %q", want)
		}
	}
}

func TestInstructionsLength(t *testing.T) {
	for _, p := range []auth.Principal{devicesOnly(), allScopes()} {
		if n := len([]rune(serverInstructions(p, true))); n > maxInstructionsLen {
			t.Fatalf("instructions are %d characters, limit %d", n, maxInstructionsLen)
		}
	}
	if n := len([]rune(serverInstructions(auth.Principal{}, false))); n == 0 || n > maxInstructionsLen {
		t.Fatalf("unauthenticated instructions length %d", n)
	}
}

func TestAnnotationsReadOnlyToolsAreReadOnly(t *testing.T) {
	for name := range readOnlyTools {
		a := annotationsFor(name)
		if a.ReadOnlyHint == nil || !*a.ReadOnlyHint {
			t.Fatalf("%s: readOnlyHint must be true", name)
		}
		if a.DestructiveHint == nil || *a.DestructiveHint {
			t.Fatalf("%s: a read-only tool must not be destructive", name)
		}
	}
}

func TestAnnotationsDestructiveToolsNotReadOnly(t *testing.T) {
	for name := range destructiveTools {
		a := annotationsFor(name)
		if a.ReadOnlyHint == nil || *a.ReadOnlyHint {
			t.Fatalf("%s: a state-changing tool must not be readOnly", name)
		}
		if a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Fatalf("%s: destructiveHint must be true", name)
		}
	}
}

// Unknown tools fall back to the conservative values.
func TestAnnotationsUnknownToolConservative(t *testing.T) {
	a := annotationsFor("some_future_tool")
	if *a.ReadOnlyHint || !*a.DestructiveHint || !*a.OpenWorldHint {
		t.Fatalf("unknown tool not conservative: %+v", a)
	}
}

// End to end: tools/list carries the annotations, and every listed tool has all four hints set.
func TestToolsListCarriesAnnotations(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	r := httptest.NewRequest("POST", "/mcp", nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "panel"})
	resp := srv.handleRequest(ctx, r, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	b, _ := json.Marshal(resp)
	var out struct {
		Result struct {
			Tools []struct {
				Name        string           `json:"name"`
				Annotations *ToolAnnotations `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range out.Result.Tools {
		a := tool.Annotations
		if a == nil || a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Fatalf("%s: annotations incomplete: %+v", tool.Name, a)
		}
	}
}
