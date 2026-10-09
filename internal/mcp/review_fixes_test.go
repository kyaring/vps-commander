package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
)

type rateLimitedBackend struct{ mockBackend }

func (rateLimitedBackend) ExecCommand(ctx context.Context, clientIP, device, command, workdir string, timeout int) (any, error) {
	return nil, fmt.Errorf("%w: token concurrent exec limit reached", ErrRateLimited)
}

// textRateLimitedBackend returns ordinary output that merely contains the word
// rate_limited; it must not be treated as a limiter rejection.
type textRateLimitedBackend struct{ mockBackend }

func (textRateLimitedBackend) ExecCommand(ctx context.Context, clientIP, device, command, workdir string, timeout int) (any, error) {
	return nil, fmt.Errorf("command failed: grep rate_limited /var/log/app.log exited 1")
}

func execPrincipalRequest(method, target, body, tokenID string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", TokenID: tokenID, Scopes: map[string]bool{"exec": true, "mcp:call": true}, Devices: map[string]bool{"local": true}}))
}

const execCall = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"exec_command","arguments":{"command":"echo hi"}}}`

func TestRateLimitUsesSentinelNotErrorText(t *testing.T) {
	w := httptest.NewRecorder()
	NewServer(&rateLimitedBackend{}, nil).HandleStreamableHTTP(w, execPrincipalRequest(http.MethodPost, "/mcp", execCall, "t1"))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("sentinel rate limit: status=%d retry-after=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	w = httptest.NewRecorder()
	NewServer(&textRateLimitedBackend{}, nil).HandleStreamableHTTP(w, execPrincipalRequest(http.MethodPost, "/mcp", execCall, "t1"))
	if w.Code != http.StatusOK {
		t.Fatalf("plain error mentioning rate_limited must stay 200 tool error, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLegacySSERateLimitResponseIsDeliveredOnStream(t *testing.T) {
	srv := NewServer(&rateLimitedBackend{}, nil)
	sess := newSession("t1")
	srv.mu.Lock()
	srv.sessions["sse-rl"] = sess
	srv.mu.Unlock()
	w := httptest.NewRecorder()
	srv.HandleMessage(w, execPrincipalRequest(http.MethodPost, "/mcp/message?sessionId=sse-rl", execCall, "t1"))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d retry-after=%q", w.Code, w.Header().Get("Retry-After"))
	}
	select {
	case msg := <-sess.ch:
		var resp struct {
			ID     int `json:"id"`
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil || resp.ID != 7 || !resp.Result.IsError {
			t.Fatalf("stream message = %s (err %v)", msg, err)
		}
	default:
		t.Fatal("rate-limited JSON-RPC response was not delivered on the SSE stream")
	}
}

func TestJSONRPCParseAndInvalidRequestErrors(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	cases := []struct {
		body string
		code int
	}{
		{`{not json`, -32700},
		{`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, -32600},
		{`[{"jsonrpc":`, -32700},
		{`{"jsonrpc":"1.0","id":1,"method":"ping"}`, -32600},
		{`{"jsonrpc":"2.0","id":1,"method":42}`, -32600},
	}
	for _, tc := range cases {
		for _, target := range []string{"/mcp", "/mcp/message"} {
			w := httptest.NewRecorder()
			req := execPrincipalRequest(http.MethodPost, target, tc.body, "t1")
			if target == "/mcp" {
				srv.HandleStreamableHTTP(w, req)
			} else {
				srv.HandleMessage(w, req)
			}
			var resp struct {
				JSONRPC string        `json:"jsonrpc"`
				Error   *JSONRPCError `json:"error"`
			}
			if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &resp) != nil || resp.Error == nil || resp.Error.Code != tc.code || resp.JSONRPC != "2.0" {
				t.Fatalf("%s %q: status=%d body=%s, want 400 with code %d", target, tc.body, w.Code, w.Body.String(), tc.code)
			}
		}
	}
}

func TestStreamableHTTPDeleteEndsSession(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	w := httptest.NewRecorder()
	srv.HandleStreamableHTTP(w, execPrincipalRequest(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, "owner"))
	sid := w.Header().Get("Mcp-Session-Id")
	if w.Code != http.StatusOK || sid == "" {
		t.Fatalf("initialize status=%d sid=%q", w.Code, sid)
	}
	del := func(token, id string) int {
		req := execPrincipalRequest(http.MethodDelete, "/mcp", "", token)
		if id != "" {
			req.Header.Set("Mcp-Session-Id", id)
		}
		rec := httptest.NewRecorder()
		srv.HandleStreamableHTTP(rec, req)
		return rec.Code
	}
	if code := del("owner", ""); code != http.StatusBadRequest {
		t.Fatalf("delete without id = %d, want 400", code)
	}
	if code := del("intruder", sid); code != http.StatusForbidden {
		t.Fatalf("delete by other token = %d, want 403", code)
	}
	if code := del("owner", sid); code != http.StatusNoContent {
		t.Fatalf("delete by owner = %d, want 204", code)
	}
	if code := del("owner", sid); code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", code)
	}
	w = httptest.NewRecorder()
	req := execPrincipalRequest(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "owner")
	req.Header.Set("Mcp-Session-Id", sid)
	srv.HandleStreamableHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("request on deleted session = %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	srv.HandleStreamableHTTP(w, execPrincipalRequest(http.MethodGet, "/mcp", "", "owner"))
	if w.Code != http.StatusMethodNotAllowed || !strings.Contains(w.Header().Get("Allow"), "DELETE") {
		t.Fatalf("GET status=%d allow=%q", w.Code, w.Header().Get("Allow"))
	}
}

func TestShortSessionID(t *testing.T) {
	if got := shortSessionID("0123456789abcdef"); got != "01234567…" {
		t.Fatalf("short id = %q", got)
	}
	if got := shortSessionID("abc"); got != "abc" {
		t.Fatalf("short id = %q", got)
	}
}
