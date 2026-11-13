package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
)

func callToolRaw(t *testing.T, args string) (JSONRPCResponse, string) {
	t.Helper()
	srv := NewServer(&mockBackend{}, nil)
	r := httptest.NewRequest("POST", "/mcp", nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "panel"})
	params := `{"name":"exec_command","arguments":` + args + `}`
	resp := srv.handleRequest(ctx, r, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: json.RawMessage(params)})
	b, _ := json.Marshal(resp)
	return resp, string(b)
}

// Malformed arguments are refused with invalid params and nothing runs.
func TestInvalidArgumentsJSONRejected(t *testing.T) {
	for _, args := range []string{`"not-an-object"`, `[1,2]`, `123`} {
		resp, _ := callToolRaw(t, args)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("arguments %s: want invalid params, got %+v", args, resp)
		}
	}
}

// A numeric device name must not fall back to the Hub's own node: it is refused.
func TestDeviceWrongTypeRejected(t *testing.T) {
	resp, _ := callToolRaw(t, `{"device":7,"command":"echo hi"}`)
	if resp.Error == nil || resp.Error.Code != -32602 || resp.Result != nil {
		t.Fatalf("numeric device was not refused: %+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "device must be string") {
		t.Fatalf("message should name the argument and type: %q", resp.Error.Message)
	}
}

// The error names the argument and the expected type, and never repeats the value sent.
func TestErrorDoesNotEchoArguments(t *testing.T) {
	_, b := callToolRaw(t, `{"device":7,"command":"echo SECRET-CANARY-42"}`)
	if strings.Contains(b, "SECRET-CANARY-42") || strings.Contains(b, "echo SECRET") {
		t.Fatalf("error repeats a caller value: %s", b)
	}
}

// Well-formed arguments are not affected.
func TestValidArgumentsUnchanged(t *testing.T) {
	for _, args := range []string{`{"device":"local","command":"echo hi"}`, `{"device":null,"command":"echo hi"}`, `{}`, `null`} {
		if err := validateToolArgs(json.RawMessage(args)); err != nil {
			t.Fatalf("valid arguments %s rejected: %v", args, err)
		}
	}
	if err := validateToolArgs(json.RawMessage(`{"timeout":2.5}`)); err == nil {
		t.Fatal("a fractional integer must be refused")
	}
}

// The validation table must agree with the schemas each tool publishes.
func TestArgKindsMatchSchemas(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	r := httptest.NewRequest("POST", "/mcp", nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "panel"})
	resp := srv.handleRequest(ctx, r, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	b, _ := json.Marshal(resp)
	var out struct {
		Result struct {
			Tools []struct {
				InputSchema struct {
					Properties map[string]struct {
						Type string `json:"type"`
					} `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, tool := range out.Result.Tools {
		for name, def := range tool.InputSchema.Properties {
			if prev, ok := seen[name]; ok && prev != def.Type {
				t.Fatalf("%s declared as both %s and %s", name, prev, def.Type)
			}
			seen[name] = def.Type
		}
	}
	for name, kind := range seen {
		if toolArgKinds[name] != kind {
			t.Errorf("%s: schema says %s, validation table says %q", name, kind, toolArgKinds[name])
		}
	}
	for name := range toolArgKinds {
		if _, ok := seen[name]; !ok {
			t.Errorf("%s is in the validation table but no schema declares it", name)
		}
	}
}

// Every argument published by a dev_task_* tool must have an identical validator kind.
func TestDevTaskToolArgKindsCovered(t *testing.T) {
	for _, tool := range devtaskToolDefs() {
		for name, def := range tool.InputSchema.Properties {
			kind, ok := toolArgKinds[name]
			if !ok {
				t.Errorf("%s.%s: missing from toolArgKinds", tool.Name, name)
				continue
			}
			if kind != def.Type {
				t.Errorf("%s.%s: schema says %s, toolArgKinds says %s", tool.Name, name, def.Type, kind)
			}
		}
	}
}
