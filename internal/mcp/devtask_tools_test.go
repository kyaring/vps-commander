package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func openDevTaskMCP(t *testing.T) (*Server, *storage.Store) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "devtask.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertDevProject(storage.DevProject{
		Name: "demo", RepoPath: "/tmp/demo", BaseRef: "main", DefaultModel: storage.DevTaskDefaultModel,
		AllowedCmdsJSON: `[]`, DefaultAcceptCmdsJSON: `["go test"]`, ForbiddenPathsJSON: `["deploy/"]`,
		MaxRounds: 2, MaxSteps: 20, TimeoutS: 600, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(&mockBackend{}, nil)
	srv.OAuthStore = st
	return srv, st
}

func devtaskPrincipal(scopes ...string) context.Context {
	set := map[string]bool{}
	for _, scope := range scopes {
		set[scope] = true
	}
	return auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "tok", Name: "gpt", TokenID: "tok", Scopes: set})
}

func callDevTask(t *testing.T, srv *Server, ctx context.Context, name, args string) (map[string]any, bool) {
	t.Helper()
	params, _ := json.Marshal(CallToolParams{Name: name, Arguments: json.RawMessage(args)})
	resp := srv.handleRequest(ctx, httptest.NewRequest("POST", "/mcp", nil), JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	body, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result CallToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) == 0 {
		t.Fatal("empty tool content")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	return payload, result.IsError
}

func toolNames(t *testing.T, srv *Server, ctx context.Context) []string {
	t.Helper()
	resp := srv.handleRequest(ctx, httptest.NewRequest("POST", "/mcp", nil), JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	body, _ := json.Marshal(resp.Result)
	var wrap struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(wrap.Tools))
	for _, tool := range wrap.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestDevTaskToolsHiddenWhenDisabled(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "")
	srv, _ := openDevTaskMCP(t)
	ctx := devtaskPrincipal("devtask:submit", "devtask:read", "devtask:cancel")
	for _, name := range toolNames(t, srv, ctx) {
		if strings.HasPrefix(name, "dev_task_") {
			t.Fatalf("disabled tools/list included %s", name)
		}
	}
	payload, isErr := callDevTask(t, srv, ctx, "dev_task_dispatch", `{"project":"demo","title":"t","spec_md":"goal"}`)
	if !isErr || !strings.Contains(payload["error"].(string), "disabled") {
		t.Fatalf("disabled call = %#v err=%v", payload, isErr)
	}
}

func TestDevTaskToolsFilteredByScope(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	srv, _ := openDevTaskMCP(t)
	names := toolNames(t, srv, devtaskPrincipal("devtask:submit"))
	got := map[string]bool{}
	for _, name := range names {
		if strings.HasPrefix(name, "dev_task_") {
			got[name] = true
		}
	}
	if !got["dev_task_dispatch"] || !got["dev_task_revise"] || got["dev_task_status"] || got["dev_task_cancel"] || len(got) != 2 {
		t.Fatalf("submit tools = %#v", got)
	}
}

func TestDevTaskDispatchStatusListDiffLogsReviseCancel(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	srv, st := openDevTaskMCP(t)
	ctx := devtaskPrincipal("devtask:submit", "devtask:read", "devtask:cancel")

	bad, isErr := callDevTask(t, srv, ctx, "dev_task_dispatch", `{"project":"demo","title":"t","spec_md":"goal","model":"other"}`)
	if !isErr || !strings.Contains(bad["error"].(string), "whitelist") {
		t.Fatalf("bad model = %#v", bad)
	}

	dispatched, isErr := callDevTask(t, srv, ctx, "dev_task_dispatch", `{"project":"demo","title":"fix","spec_md":"goal","forbidden_paths":[".env"]}`)
	if isErr {
		t.Fatalf("dispatch error: %#v", dispatched)
	}
	id := dispatched["task_id"].(string)
	if !strings.HasPrefix(dispatched["branch"].(string), "task/") || dispatched["model"] != storage.DevTaskDefaultModel {
		t.Fatalf("dispatch = %#v", dispatched)
	}
	stored, err := st.GetDevTask(id)
	if err != nil || !strings.Contains(stored.ForbiddenPathsJSON, "deploy/") || !strings.Contains(stored.ForbiddenPathsJSON, ".env") {
		t.Fatalf("forbidden = %s err=%v", stored.ForbiddenPathsJSON, err)
	}

	status, isErr := callDevTask(t, srv, ctx, "dev_task_status", `{"task_id":"`+id+`"}`)
	if isErr || status["status"] != storage.DevTaskQueued || status["round"].(float64) != 1 {
		t.Fatalf("status = %#v err=%v", status, isErr)
	}
	listed, isErr := callDevTask(t, srv, ctx, "dev_task_list", `{"project":"demo","status":"queued"}`)
	if isErr {
		t.Fatal(listed)
	}
	tasks, _ := listed["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("list = %#v", listed)
	}

	if _, err := st.ClaimDevTask("runner", 60); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteDevTask(id, "runner", storage.CompleteDevTaskResult{Status: storage.DevTaskNeedsReview, CommitSHA: "abc", DiffStat: "file.go | 2 +-", ResultMD: "done"}); err != nil {
		t.Fatal(err)
	}
	diff, isErr := callDevTask(t, srv, ctx, "dev_task_diff", `{"task_id":"`+id+`","offset":0,"limit":4}`)
	if isErr || diff["diff"] != "file" || diff["commit_sha"] != "abc" {
		t.Fatalf("diff = %#v", diff)
	}

	revised, isErr := callDevTask(t, srv, ctx, "dev_task_revise", `{"task_id":"`+id+`","note":"narrow the test"}`)
	if isErr || revised["status"] != storage.DevTaskQueued || revised["round"].(float64) != 2 {
		t.Fatalf("revise = %#v err=%v", revised, isErr)
	}
	logs, isErr := callDevTask(t, srv, ctx, "dev_task_logs", `{"task_id":"`+id+`"}`)
	if isErr {
		t.Fatal(logs)
	}
	events, _ := logs["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("logs = %#v", logs)
	}

	cancelled, isErr := callDevTask(t, srv, ctx, "dev_task_cancel", `{"task_id":"`+id+`"}`)
	if isErr || cancelled["status"] != storage.DevTaskCancelled {
		t.Fatalf("cancel = %#v", cancelled)
	}

	logsFound, err := st.QueryAudits("", "", "devtask_dispatch", "", "", 0, 0, 0, 10)
	if err != nil || len(logsFound) == 0 || logsFound[0].ActorName != "gpt" {
		t.Fatalf("audit = %+v err=%v", logsFound, err)
	}
}

func TestDevTaskDispatchDeniedWithoutSubmitScope(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	srv, _ := openDevTaskMCP(t)
	payload, isErr := callDevTask(t, srv, devtaskPrincipal("devtask:read"), "dev_task_dispatch", `{"project":"demo","title":"t","spec_md":"goal"}`)
	if !isErr || !strings.Contains(payload["error"].(string), "scope denied") {
		t.Fatalf("payload = %#v err=%v", payload, isErr)
	}
}

func TestDevTaskMCPToolsRequireScope(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	srv, _ := openDevTaskMCP(t)

	toolScopes := map[string]string{
		"dev_task_dispatch": "devtask:submit",
		"dev_task_revise":   "devtask:submit",
		"dev_task_status":   "devtask:read",
		"dev_task_list":     "devtask:read",
		"dev_task_diff":     "devtask:read",
		"dev_task_logs":     "devtask:read",
		"dev_task_cancel":   "devtask:cancel",
	}

	for tool, expectedScope := range toolScopes {
		actualScope := mcpToolScope(tool)
		if actualScope != expectedScope {
			t.Fatalf("mcpToolScope(%q) = %q, want %q", tool, actualScope, expectedScope)
		}

		ctx := devtaskPrincipal("other:scope")
		payload, isErr := callDevTask(t, srv, ctx, tool, `{}`)
		if !isErr {
			t.Fatalf("calling %s without scope should fail", tool)
		}
		errMsg, _ := payload["error"].(string)
		if !strings.Contains(errMsg, "scope denied: "+expectedScope) {
			t.Fatalf("calling %s without scope got %q, want 'scope denied: %s'", tool, errMsg, expectedScope)
		}
	}
}

func TestDevTaskMCPToolsListFiltered(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	srv, _ := openDevTaskMCP(t)

	namesNoScope := toolNames(t, srv, devtaskPrincipal())
	for _, n := range namesNoScope {
		if strings.HasPrefix(n, "dev_task_") {
			t.Fatalf("tools/list with no scope included %s", n)
		}
	}

	namesSubmit := toolNames(t, srv, devtaskPrincipal("devtask:submit"))
	submitMap := map[string]bool{}
	for _, n := range namesSubmit {
		if strings.HasPrefix(n, "dev_task_") {
			submitMap[n] = true
		}
	}
	if len(submitMap) != 2 || !submitMap["dev_task_dispatch"] || !submitMap["dev_task_revise"] {
		t.Fatalf("submit tools = %#v, want dispatch and revise", submitMap)
	}

	namesRead := toolNames(t, srv, devtaskPrincipal("devtask:read"))
	readMap := map[string]bool{}
	for _, n := range namesRead {
		if strings.HasPrefix(n, "dev_task_") {
			readMap[n] = true
		}
	}
	if len(readMap) != 4 || !readMap["dev_task_status"] || !readMap["dev_task_list"] || !readMap["dev_task_diff"] || !readMap["dev_task_logs"] {
		t.Fatalf("read tools = %#v, want status, list, diff, logs", readMap)
	}

	namesCancel := toolNames(t, srv, devtaskPrincipal("devtask:cancel"))
	cancelMap := map[string]bool{}
	for _, n := range namesCancel {
		if strings.HasPrefix(n, "dev_task_") {
			cancelMap[n] = true
		}
	}
	if len(cancelMap) != 1 || !cancelMap["dev_task_cancel"] {
		t.Fatalf("cancel tools = %#v, want cancel", cancelMap)
	}

	namesAll := toolNames(t, srv, devtaskPrincipal("devtask:submit", "devtask:read", "devtask:cancel"))
	allMap := map[string]bool{}
	for _, n := range namesAll {
		if strings.HasPrefix(n, "dev_task_") {
			allMap[n] = true
		}
	}
	if len(allMap) != 7 {
		t.Fatalf("all tools count = %d, want 7; got %#v", len(allMap), allMap)
	}
}
