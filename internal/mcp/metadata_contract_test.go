package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
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
	t.Setenv("DEVTASK_ENABLED", "on")
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

// knownShortDescriptions records tools with descriptions shorter than 20 characters.
// T-20261101-11 补齐了过短的工具描述，当前无豁免，所有工具描述均达到 20 字符要求。
var knownShortDescriptions = map[string]bool{}

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

// DevTask tools are classified by their effects and must remain open-world operations.
func TestDevTaskToolsHaveAnnotations(t *testing.T) {
	wantReadOnly := map[string]bool{
		"dev_task_list": true, "dev_task_status": true, "dev_task_diff": true, "dev_task_logs": true,
	}
	wantDestructive := map[string]bool{
		"dev_task_dispatch": true, "dev_task_revise": true, "dev_task_cancel": true,
	}
	seen := make(map[string]bool, len(wantReadOnly)+len(wantDestructive))
	for _, tool := range listedTools(t) {
		readOnly, isReadOnly := wantReadOnly[tool.Name]
		destructive, isDestructive := wantDestructive[tool.Name]
		if !isReadOnly && !isDestructive {
			continue
		}
		seen[tool.Name] = true
		if len(tool.Annotations) == 0 {
			t.Errorf("%s: annotations missing", tool.Name)
			continue
		}
		var gotReadOnly, gotDestructive, gotOpenWorld bool
		if err := json.Unmarshal(tool.Annotations["readOnlyHint"], &gotReadOnly); err != nil {
			t.Errorf("%s: invalid readOnlyHint: %v", tool.Name, err)
		}
		if err := json.Unmarshal(tool.Annotations["destructiveHint"], &gotDestructive); err != nil {
			t.Errorf("%s: invalid destructiveHint: %v", tool.Name, err)
		}
		if err := json.Unmarshal(tool.Annotations["openWorldHint"], &gotOpenWorld); err != nil {
			t.Errorf("%s: invalid openWorldHint: %v", tool.Name, err)
		}
		if gotReadOnly != readOnly {
			t.Errorf("%s: readOnlyHint=%t, want %t", tool.Name, gotReadOnly, readOnly)
		}
		if gotDestructive != destructive {
			t.Errorf("%s: destructiveHint=%t, want %t", tool.Name, gotDestructive, destructive)
		}
		if !gotOpenWorld {
			t.Errorf("%s: openWorldHint must be true", tool.Name)
		}
	}
	for name := range wantReadOnly {
		if !seen[name] {
			t.Errorf("%s: dev_task tool missing from tools/list", name)
		}
	}
	for name := range wantDestructive {
		if !seen[name] {
			t.Errorf("%s: dev_task tool missing from tools/list", name)
		}
	}
}

// The task's annotation matrix is intentionally asserted per tool, not inferred from naming.
func TestToolAnnotationsMatchTaskMatrix(t *testing.T) {
	wantReadOnly := map[string]bool{
		"memo_set": false, "memo_get": true, "list_devices": true, "exec_command": false,
		"read_file": true, "write_file": false, "edit_block": false, "read_multiple_files": true,
		"create_directory": false, "move_file": false, "list_processes": true, "diagnostic_command": true,
		"list_agent_mcp": true, "call_agent_mcp": false, "exec_start": false, "exec_status": true,
		"exec_cancel": false, "dev_task_dispatch": false, "dev_task_status": true, "dev_task_list": true,
		"dev_task_diff": true, "dev_task_logs": true, "dev_task_revise": false, "dev_task_cancel": false,
	}
	for _, tool := range listedTools(t) {
		want, ok := wantReadOnly[tool.Name]
		if !ok {
			t.Errorf("%s missing from annotation matrix", tool.Name)
			continue
		}
		var got bool
		if err := json.Unmarshal(tool.Annotations["readOnlyHint"], &got); err != nil {
			t.Errorf("%s readOnlyHint: %v", tool.Name, err)
			continue
		}
		if got != want {
			t.Errorf("%s readOnlyHint=%v want=%v", tool.Name, got, want)
		}
		var destructive bool
		if err := json.Unmarshal(tool.Annotations["destructiveHint"], &destructive); err != nil {
			t.Errorf("%s destructiveHint: %v", tool.Name, err)
			continue
		}
		wantDestructive := !want && tool.Name != "memo_set"
		if destructive != wantDestructive {
			t.Errorf("%s destructiveHint=%v want=%v", tool.Name, destructive, wantDestructive)
		}
		var idempotent bool
		if err := json.Unmarshal(tool.Annotations["idempotentHint"], &idempotent); err != nil {
			t.Errorf("%s idempotentHint: %v", tool.Name, err)
			continue
		}
		if idempotent != want {
			t.Errorf("%s idempotentHint=%v want=%v", tool.Name, idempotent, want)
		}
		var openWorld bool
		if err := json.Unmarshal(tool.Annotations["openWorldHint"], &openWorld); err != nil {
			t.Errorf("%s openWorldHint: %v", tool.Name, err)
			continue
		}
		wantOpenWorld := tool.Name != "memo_set" && tool.Name != "memo_get"
		if openWorld != wantOpenWorld {
			t.Errorf("%s openWorldHint=%v want=%v", tool.Name, openWorld, wantOpenWorld)
		}
	}
	if len(wantReadOnly) != len(listedTools(t)) {
		t.Fatalf("annotation matrix has %d tools but tools/list has %d", len(wantReadOnly), len(listedTools(t)))
	}
}

func TestMemoToolsRemainAvailableButDeprecated(t *testing.T) {
	tools := listedTools(t)
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "memo_set" || tool.Name == "memo_get" {
			seen[tool.Name] = true
			if !strings.HasPrefix(tool.Description, "已废弃，改用 scratch 文件 + read_file") {
				t.Errorf("%s description does not start with deprecation notice: %q", tool.Name, tool.Description)
			}
		}
	}
	if !seen["memo_set"] || !seen["memo_get"] {
		t.Fatalf("memo tools were removed: seen=%v", seen)
	}
}
