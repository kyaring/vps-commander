package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/model"
	"github.com/wjyhk/vps-commander/internal/storage"
)

type outputSchemaBackend struct{ mockBackend }

func (b *outputSchemaBackend) ListDevices(context.Context) (any, error) {
	return []model.Device{{Name: "local", Status: "online", Local: true}}, nil
}

func (b *outputSchemaBackend) ExecCommand(context.Context, string, string, string, string, int) (any, error) {
	return map[string]any{"device": "local", "exit_code": 0, "stdout": "ok", "stderr": "", "duration_ms": 1, "truncated": false, "total_bytes": 2, "total_lines": 1, "sha256": "abc", "spill_path": "", "spill_device": "", "timed_out": false}, nil
}
func (b *outputSchemaBackend) ReadFile(_ context.Context, _, device, path string, offset, _ int64) (any, error) {
	return map[string]any{"device": device, "path": path, "offset": offset, "content": "ok", "bytes": 2, "total_size": 2, "has_more": false}, nil
}
func (b *outputSchemaBackend) CreateDirectory(context.Context, string, string, string) (any, error) {
	return map[string]any{"ok": true}, nil
}
func (b *outputSchemaBackend) MoveFile(context.Context, string, string, string, string) (any, error) {
	return map[string]any{"ok": true}, nil
}
func (b *outputSchemaBackend) ReadMultipleFiles(context.Context, string, string, []string, int64) (any, error) {
	return []executor.FileReadItem{{Path: "/tmp/a", Content: "ok"}}, nil
}
func (b *outputSchemaBackend) ListProcesses(context.Context, string, string) (any, error) {
	return []executor.ProcessInfo{{PID: 1, Command: "init", CPU: "0%", MemoryKB: 1}}, nil
}
func (b *outputSchemaBackend) DiagnosticCommand(_ context.Context, _, _ string, req executor.DiagnosticRequest) (any, error) {
	return executor.DiagnosticResult{Action: req.Action, ExitCode: 0, Stdout: "ok", Stderr: "", DurationMS: 1}, nil
}
func (b *outputSchemaBackend) ListAgentMCP(context.Context, string) (any, error) {
	return map[string]any{"device": "local", "services": []any{}}, nil
}
func (b *outputSchemaBackend) CallAgentMCP(context.Context, string, string, string, string, any) (any, error) {
	return cluster.MCPCallResult{Server: "s", Tool: "t", Result: map[string]any{"ok": true}, DurationMS: 1}, nil
}
func (b *outputSchemaBackend) StartExec(context.Context, string, string, string, string) (any, error) {
	return testJobView(), nil
}
func (b *outputSchemaBackend) ExecStatus(context.Context, string) (any, error) {
	return testJobView(), nil
}
func (b *outputSchemaBackend) CancelExec(context.Context, string, string) (any, error) {
	return testJobView(), nil
}

func testJobView() map[string]any {
	return map[string]any{"device": "local", "exit_code": 0, "stdout": "ok", "stderr": "", "duration_ms": 1,
		"truncated": false, "total_bytes": 2, "total_lines": 1, "sha256": "abc", "spill_path": "", "spill_device": "",
		"timed_out": false, "job_id": "job-test", "status": "exited", "reason": "", "started_at": "2026-01-01T00:00:00Z", "max_runtime_s": 60}
}

func schemaTypeMatches(want string, value any) bool {
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		switch n := value.(type) {
		case float64:
			return n == float64(int64(n))
		case int, int32, int64, uint, uint32, uint64:
			return true
		}
	}
	return true
}

func validateOutputSchema(schema map[string]any, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
		return err
	}
	return validateSchemaValue(schema, normalized, "$")
}

func validateSchemaValue(schema map[string]any, value any, path string) error {
	if branches, ok := schema["anyOf"].([]any); ok {
		for _, branch := range branches {
			if child, ok := branch.(map[string]any); ok && validateSchemaValue(child, value, path) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s: value matches no anyOf branch", path)
	}
	if typ, _ := schema["type"].(string); typ != "" && !schemaTypeMatches(typ, value) {
		return fmt.Errorf("%s: expected %s, got %T", path, typ, value)
	}
	if required, ok := schema["required"].([]string); ok {
		m, _ := value.(map[string]any)
		for _, key := range required {
			if _, exists := m[key]; !exists {
				return fmt.Errorf("%s: required property %q missing", path, key)
			}
		}
	}
	if required, ok := schema["required"].([]any); ok {
		m, _ := value.(map[string]any)
		for _, item := range required {
			key, _ := item.(string)
			if _, exists := m[key]; !exists {
				return fmt.Errorf("%s: required property %q missing", path, key)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		m, _ := value.(map[string]any)
		for key, childSchema := range props {
			if child, exists := m[key]; exists {
				if err := validateSchemaValue(childSchema.(map[string]any), child, path+"."+key); err != nil {
					return err
				}
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if list, yes := value.([]any); yes {
			for i, child := range list {
				if err := validateSchemaValue(items, child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func TestEveryStructuredToolCallMatchesOutputSchema(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "true")
	store, err := storage.Open(t.TempDir() + "/schema.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertDevProject(storage.DevProject{Name: "schema-project", RepoPath: t.TempDir(), BaseRef: "main", DefaultModel: storage.DevTaskDefaultModel, AllowedCmdsJSON: `[]`, DefaultAcceptCmdsJSON: `[]`, ForbiddenPathsJSON: `[]`, MaxRounds: 3, MaxSteps: 10, TimeoutS: 60, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDevTaskAllowedModels([]string{storage.DevTaskDefaultModel}); err != nil {
		t.Fatal(err)
	}

	backend := &outputSchemaBackend{}
	srv := NewServer(backend, nil)
	srv.OAuthStore = store
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "panel", ID: "schema-test", Name: "schema-test"})
	req := httptest.NewRequest("POST", "/mcp", nil)
	listed := srv.handleRequest(ctx, req, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	listResult, ok := listed.Result.(map[string]any)
	if !ok {
		t.Fatalf("tools/list result: %T", listed.Result)
	}
	tools, ok := listResult["tools"].([]Tool)
	if !ok {
		t.Fatalf("tools list: %T", listResult["tools"])
	}
	schemas := make(map[string]map[string]any, len(tools))
	for _, tool := range tools {
		if tool.OutputSchema != nil {
			schemas[tool.Name] = tool.OutputSchema
		}
	}

	call := func(name, args string) any {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(args)})
		resp := srv.handleRequest(ctx, req, JSONRPCRequest{JSONRPC: "2.0", ID: name, Method: "tools/call", Params: params})
		result, ok := resp.Result.(CallToolResult)
		if !ok || result.IsError {
			t.Fatalf("%s call failed: %#v", name, resp)
		}
		if result.StructuredContent == nil {
			t.Fatalf("%s did not return structuredContent", name)
		}
		if err := validateOutputSchema(schemas[name], result.StructuredContent); err != nil {
			t.Fatalf("%s structuredContent violates outputSchema: %v; value=%#v", name, err, result.StructuredContent)
		}
		return result.StructuredContent
	}

	call("list_devices", `{}`)
	call("exec_command", `{"command":"true"}`)
	call("read_file", `{"path":"/tmp/schema"}`)
	call("read_multiple_files", `{"paths":["/tmp/schema"]}`)
	call("list_processes", `{}`)
	call("diagnostic_command", `{"action":"uptime"}`)
	call("exec_start", `{"command":"true"}`)
	call("exec_status", `{"job_id":"job-test"}`)
	call("exec_cancel", `{"job_id":"job-test"}`)
	call("list_agent_mcp", `{"device":"local"}`)
	call("call_agent_mcp", `{"device":"remote-test","server":"s","tool":"t"}`)

	dispatch := call("dev_task_dispatch", `{"project":"schema-project","title":"schema test","spec_md":"test"}`)
	dispatchMap := dispatch.(map[string]any)
	taskID, _ := dispatchMap["task_id"].(string)
	if taskID == "" {
		t.Fatal("dev_task_dispatch returned no task_id")
	}
	args := fmt.Sprintf(`{"task_id":%q}`, taskID)
	call("dev_task_status", args)
	call("dev_task_list", `{}`)
	call("dev_task_diff", args)
	call("dev_task_logs", args)
	if _, err := store.DB.Exec(`UPDATE dev_tasks SET status='failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	call("dev_task_revise", args)
	call("dev_task_cancel", args)

	if err := validateOutputSchema(schemas["list_agent_mcp"], []cluster.MCPService{{ID: "s", Name: "service", Transport: "stdio"}}); err != nil {
		t.Fatalf("remote list_agent_mcp shape: %v", err)
	}
	if len(schemas) != len(outputSchemas()) {
		t.Fatalf("schemas listed=%d, declared=%d", len(schemas), len(outputSchemas()))
	}
}

func TestOutputSchemaValidatorRejectsMismatchedType(t *testing.T) {
	schema := outputSchemas()["exec_command"]
	properties := schema["properties"].(map[string]any)
	properties["exit_code"] = map[string]any{"type": "string"}
	sample := map[string]any{"device": "local", "exit_code": 0, "stdout": "", "stderr": "", "duration_ms": 0,
		"truncated": false, "total_bytes": 0, "total_lines": 0, "sha256": "", "spill_path": "", "spill_device": "", "timed_out": false}
	if err := validateOutputSchema(schema, sample); err == nil {
		t.Fatal("schema with mismatched exit_code type unexpectedly accepted")
	}
}

func TestOutputSchemaListMatchesDeclaredToolSet(t *testing.T) {
	got := outputSchemas()
	want := []string{"exec_command", "read_file", "read_multiple_files", "list_devices", "list_processes", "diagnostic_command", "exec_start", "exec_status", "exec_cancel", "list_agent_mcp", "call_agent_mcp", "dev_task_dispatch", "dev_task_status", "dev_task_list", "dev_task_diff", "dev_task_logs", "dev_task_revise", "dev_task_cancel"}
	if len(got) != len(want) {
		t.Fatalf("schema count=%d want=%d", len(got), len(want))
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("missing outputSchema for %s", name)
		}
	}
	if !reflect.DeepEqual(got["dev_task_status"], got["dev_task_revise"]) {
		t.Fatal("dev task status and revise schemas should describe the same status view")
	}
}

func TestDeprecatedMemoAndSimpleWriteToolsRemainOutputSchemaExempt(t *testing.T) {
	for _, name := range []string{"memo_set", "memo_get", "create_directory", "move_file", "edit_block", "write_file"} {
		if _, ok := outputSchemas()[name]; ok {
			t.Errorf("%s should remain exempt from outputSchema", name)
		}
		if got := structuredContentFor(name, map[string]any{"ok": true}, false); got != nil {
			t.Errorf("%s unexpectedly returned structuredContent: %#v", name, got)
		}
	}
}
