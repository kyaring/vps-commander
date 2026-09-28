package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestMCPRuntimeStdioOnDemand(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	script := dir + "/mcp.py"
	code := `import json,sys
for line in sys.stdin:
 r=json.loads(line)
 if r.get("method")=="initialize":
  print(json.dumps({"jsonrpc":"2.0","id":r["id"],"result":{"protocolVersion":"2024-11-05"}}),flush=True)
 elif r.get("method")=="tools/call":
  a=r.get("params",{}).get("arguments",{})
  print(json.dumps({"jsonrpc":"2.0","id":r["id"],"result":{"content":[{"type":"text","text":"hello "+a.get("name","")}]}}),flush=True)
`
	if err := os.WriteFile(script, []byte(code), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewMCPRuntime()
	r.Sync([]MCPService{{ID: "test", Transport: "stdio", Command: "python3", Args: []string{script}}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := r.Call(ctx, "test", "hello", map[string]any{"name": "vpc"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if string(b) == "" {
		t.Fatal("empty result")
	}
	if !contains(string(b), "hello vpc") {
		t.Fatalf("unexpected %s", b)
	}
}

func TestMCPRuntimeHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q mcpRPC
		_ = json.Unmarshal(b, &q)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`, jsonID(q.ID))
	}))
	defer ts.Close()
	r := NewMCPRuntime()
	r.Sync([]MCPService{{ID: "http", Transport: "http", URL: ts.URL}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := r.Call(ctx, "http", "ping", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(fmt.Sprint(v), "true") {
		t.Fatalf("unexpected %v", v)
	}
}
func jsonID(v any) string { b, _ := json.Marshal(v); return string(b) }
func contains(s, x string) bool {
	for i := 0; i+len(x) <= len(s); i++ {
		if s[i:i+len(x)] == x {
			return true
		}
	}
	return false
}
