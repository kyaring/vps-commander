package executor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/api"
	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestHTTPExecOutputModesAndDefaultTruncation(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Exec: executor.Local{MaxOutput: executor.MaxOutputBytes}, Store: store, LocalName: "local"}
	call := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(body))
		p := auth.Principal{Type: "system", ID: "test", Name: "test", Scopes: map[string]bool{"exec": true, "admin": true}}
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := call(`{"command":"printf '%05000d' 1"}`)
	if got["truncated"] != true || got["total_bytes"] != float64(5000) || got["total_lines"] != float64(1) || got["sha256"] == nil || !strings.Contains(got["stdout"].(string), "truncated: kept") {
		t.Fatalf("default truncation metadata: %#v", got)
	}
	count := call(`{"command":"printf 'abc\\ndef'","output_mode":"count"}`)
	if len(count) != 3 || count["total_bytes"] != float64(7) || count["total_lines"] != float64(2) {
		t.Fatalf("count response: %#v", count)
	}
	none := call(`{"command":"printf 'abc'","output_mode":"none"}`)
	if none["stdout"] != nil || none["exit_code"] != float64(0) || none["sha256"] == nil {
		t.Fatalf("none response: %#v", none)
	}
}

func TestHTTPExecDedupHitAndExpiry(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Exec: executor.Local{MaxOutput: executor.MaxOutputBytes}, Store: store, LocalName: "local"}
	file := filepath.Join(t.TempDir(), "runs")
	body := `{"command":"printf x >> ` + file + `; printf done","dedup_window_seconds":1}`
	call := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", strings.NewReader(body))
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "system", ID: "test", Name: "test", Scopes: map[string]bool{"exec": true, "admin": true}}))
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := call(); got["stdout"] != "done" {
		t.Fatalf("first response: %#v", got)
	}
	if got := call(); got["unchanged"] != true || got["hash"] == nil || got["first_seen"] == nil || got["cached_bytes"] != float64(4) || got["exit_code"] != float64(0) || got["stdout"] != nil {
		t.Fatalf("dedup response: %#v", got)
	}
	time.Sleep(1100 * time.Millisecond)
	if got := call(); got["stdout"] != "done" || got["unchanged"] != nil {
		t.Fatalf("expired response: %#v", got)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "xx" {
		t.Fatalf("dedup/expiry execution count: %q", data)
	}
}

type outputOptionsBackend struct {
	mode                  string
	maxBytes, dedupWindow int
}

func (*outputOptionsBackend) ListDevices(context.Context) (any, error) { return []string{}, nil }
func (*outputOptionsBackend) ExecCommand(context.Context, string, string, string, string, int) (any, error) {
	return map[string]any{"legacy": true}, nil
}
func (b *outputOptionsBackend) ExecCommandWithOptions(_ context.Context, _ string, _ string, _ string, _ string, _ int, mode string, maxBytes, dedupWindow int) (any, error) {
	b.mode, b.maxBytes, b.dedupWindow = mode, maxBytes, dedupWindow
	return map[string]any{"output_mode": mode, "max_bytes": maxBytes, "dedup_window_seconds": dedupWindow}, nil
}
func (*outputOptionsBackend) ReadFile(context.Context, string, string, string, int64, int64) (any, error) {
	return nil, nil
}
func (*outputOptionsBackend) WriteFile(context.Context, string, string, string, string) (any, error) {
	return nil, nil
}
func (*outputOptionsBackend) EditBlock(context.Context, string, string, string, string, string) (any, error) {
	return nil, nil
}

func TestMCPExecOutputOptionsReachBackend(t *testing.T) {
	backend := &outputOptionsBackend{}
	s := mcp.NewServer(backend, nil)
	s.SetLocalName("local")
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "system", ID: "test", Name: "test", Scopes: map[string]bool{"exec": true}})
	args, _ := json.Marshal(map[string]any{"command": "printf ok", "output_mode": "count", "max_bytes": 128, "dedup_window_seconds": 9})
	params, _ := json.Marshal(map[string]any{"name": "exec_command", "arguments": json.RawMessage(args)})
	resp := s.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	result, ok := resp.Result.(mcp.CallToolResult)
	if !ok || result.IsError {
		t.Fatalf("unexpected MCP response: %#v", resp)
	}
	if backend.mode != "count" || backend.maxBytes != 128 || backend.dedupWindow != 9 {
		t.Fatalf("options not forwarded: %+v", backend)
	}
}

func TestExecOutputOptionDefaults(t *testing.T) {
	opts, err := executor.NormalizeOutputOptions("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != "raw" || opts.MaxBytes != 4096 {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
}
