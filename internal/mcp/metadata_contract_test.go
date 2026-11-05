package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
)

// contractTool is the part of tools/list the contract checks. Raw maps keep missing fields visible.
type contractTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	} `json:"inputSchema"`
	Annotations map[string]json.RawMessage `json:"annotations"`
}

// listedTools returns tools/list as a panel principal sees it, so every registered tool is included.
func listedTools(t *testing.T) []contractTool {
	t.Helper()
	srv := NewServer(&mockBackend{}, nil)
	r := httptest.NewRequest("POST", "/mcp", nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "panel"})
	resp := srv.handleRequest(ctx, r, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			Tools []contractTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}
	return out.Result.Tools
}

// knownShortDescriptions records the existing gap: these tools have descriptions shorter than 20
// characters. Fixing them touches internal/mcp/mcp.go and is tracked as a follow-up card.
// The list may only shrink: a new tool can never be added to it, and an entry whose description
// reaches 20 characters must be removed (the test fails until it is).
var knownShortDescriptions = map[string]bool{
	"read_multiple_files": true,
	"create_directory":    true,
	"move_file":           true,
	"list_processes":      true,
	"write_file":          true,
}

// Adding a tool without a name, a usable description, annotations or parameter descriptions fails here.
func TestToolMetadataComplete(t *testing.T) {
	for _, tool := range listedTools(t) {
		if tool.Name == "" {
			t.Errorf("tool without a name")
			continue
		}
		n := len([]rune(tool.Description))
		switch {
		case n < 20 && !knownShortDescriptions[tool.Name]:
			t.Errorf("%s: description is %d characters, need at least 20", tool.Name, n)
		case n >= 20 && knownShortDescriptions[tool.Name]:
			t.Errorf("%s: description now has %d characters; remove it from knownShortDescriptions", tool.Name, n)
		}
		for param, def := range tool.InputSchema.Properties {
			if def.Description == "" {
				t.Errorf("%s: parameter %s has no description", tool.Name, param)
			}
		}
	}
}

// Every annotation field is set explicitly, so no client has to guess a default.
func TestEveryToolHasAnnotations(t *testing.T) {
	for _, tool := range listedTools(t) {
		if tool.Annotations == nil {
			t.Errorf("%s: no annotations", tool.Name)
			continue
		}
		for _, hint := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			if _, ok := tool.Annotations[hint]; !ok {
				t.Errorf("%s: annotation %s missing", tool.Name, hint)
			}
		}
	}
}

// Every tool is either covered by a scope or is one of the session memo tools that need no scope.
func TestToolNamesMatchScopeMap(t *testing.T) {
	for _, tool := range listedTools(t) {
		if mcpToolScope(tool.Name) == "" && tool.Name != "memo_set" && tool.Name != "memo_get" {
			t.Errorf("%s: no scope mapping and not a memo tool", tool.Name)
		}
	}
}
