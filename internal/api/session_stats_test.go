package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

type sessionStatsBackend struct{}

func (sessionStatsBackend) ListDevices(context.Context) (any, error) {
	return map[string]any{"devices": []string{"node-a"}}, nil
}
func (sessionStatsBackend) ExecCommand(context.Context, string, string, string, string, int) (any, error) {
	return map[string]any{"stdout": "ok"}, nil
}
func (sessionStatsBackend) ReadFile(context.Context, string, string, string, int64, int64) (any, error) {
	return map[string]any{"content": "ok"}, nil
}
func (sessionStatsBackend) WriteFile(context.Context, string, string, string, string) (any, error) {
	return map[string]any{"ok": true}, nil
}
func (sessionStatsBackend) EditBlock(context.Context, string, string, string, string, string) (any, error) {
	return map[string]any{"ok": true}, nil
}
func (sessionStatsBackend) DiagnosticCommand(context.Context, string, string, executor.DiagnosticRequest) (any, error) {
	return map[string]any{"ok": true}, nil
}

func newSessionStatsServer(t *testing.T, limit string) (*Server, *mcp.Server) {
	t.Helper()
	t.Setenv("VPS_COMMANDER_SESSION_BUDGET_BYTES", limit)
	mcpServer := mcp.NewServer(sessionStatsBackend{}, nil)
	return &Server{MCP: mcpServer}, mcpServer
}

func sessionStatsPrincipal(tokenID string) auth.Principal {
	return auth.Principal{Type: "api", ID: tokenID, TokenID: tokenID}
}

func getSessionStats(t *testing.T, s *Server, tokenID, sessionID, remoteAddr string) (int, []byte, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session/stats", nil)
	req.RemoteAddr = remoteAddr
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), sessionStatsPrincipal(tokenID)))
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	var body map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode stats response: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec.Code, rec.Body.Bytes(), body
}

func TestSessionStatsCountsAndTopTools(t *testing.T) {
	s, tracker := newSessionStatsServer(t, "100000")
	started := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	tracker.EnsureSessionBudget("token-A", "opaque-mcp-session", started)
	for _, call := range []struct {
		name  string
		bytes int
	}{{"exec_command", 500}, {"read_file", 300}, {"exec_command", 1000}, {"tool-c", 250}, {"tool-d", 200}, {"tool-e", 150}, {"tool-f", 100}, {"tool-g", 50}} {
		tracker.RecordSessionResponse("token-A", "opaque-mcp-session", "127.0.0.1:1", call.name, call.bytes)
	}
	status, body, decoded := getSessionStats(t, s, "token-A", "opaque-mcp-session", "127.0.0.1:1")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if len(body) > maxSessionStatsResponseBytes {
		t.Fatalf("stats response too large: %d bytes", len(body))
	}
	if got := decoded["session_id"]; got != "opaque-mcp-session" {
		t.Fatalf("session_id=%v", got)
	}
	if strings.Contains(string(body), "token-A") {
		t.Fatalf("response leaked token identifier: %s", body)
	}
	if got := int(decoded["tool_calls"].(float64)); got != 8 {
		t.Fatalf("tool_calls=%d, want 8", got)
	}
	if got := int(decoded["bytes_returned"].(float64)); got != 2550 {
		t.Fatalf("bytes_returned=%d, want 2550", got)
	}
	if got := int(decoded["est_tokens"].(float64)); got != 637 {
		t.Fatalf("est_tokens=%d, want 637", got)
	}
	top := decoded["top_tools"].([]any)
	if len(top) != 5 {
		t.Fatalf("top_tools length=%d, want 5", len(top))
	}
	first := top[0].(map[string]any)
	if first["name"] != "exec_command" || int(first["calls"].(float64)) != 2 || int(first["bytes"].(float64)) != 1500 {
		t.Fatalf("unexpected first top tool: %#v", first)
	}
	for i := 1; i < len(top); i++ {
		prev := int(top[i-1].(map[string]any)["bytes"].(float64))
		curr := int(top[i].(map[string]any)["bytes"].(float64))
		if prev < curr {
			t.Fatalf("top_tools not sorted by bytes descending: %#v", top)
		}
	}
	budget := decoded["budget"].(map[string]any)
	if int(budget["limit_bytes"].(float64)) != 100000 || int(budget["used_pct"].(float64)) != 2 || budget["warn"].(bool) {
		t.Fatalf("unexpected budget: %#v", budget)
	}
}

func TestSessionStatsTokenIsolationAndHTTPSessionIdentity(t *testing.T) {
	s, tracker := newSessionStatsServer(t, "100")
	tracker.RecordSessionResponse("token-A-secret", "", "127.0.0.1:4567", "exec", 50)
	tracker.RecordSessionResponse("token-B-secret", "", "127.0.0.1:4567", "read", 80)
	statusA, bodyA, a := getSessionStats(t, s, "token-A-secret", "", "127.0.0.1:4567")
	statusB, bodyB, b := getSessionStats(t, s, "token-B-secret", "", "127.0.0.1:4567")
	if statusA != http.StatusOK || statusB != http.StatusOK {
		t.Fatalf("statuses A=%d B=%d", statusA, statusB)
	}
	if int(a["bytes_returned"].(float64)) != 50 || int(a["tool_calls"].(float64)) != 1 {
		t.Fatalf("token A stats=%s", bodyA)
	}
	if int(b["bytes_returned"].(float64)) != 80 || int(b["tool_calls"].(float64)) != 1 {
		t.Fatalf("token B stats=%s", bodyB)
	}
	if a["session_id"] == b["session_id"] {
		t.Fatalf("tokens share session ID: %v", a["session_id"])
	}
	if strings.Contains(string(bodyA), "token-A-secret") || strings.Contains(string(bodyB), "token-B-secret") {
		t.Fatal("stats response leaked credential fragment")
	}
	tracker.EnsureSessionBudget("token-A-secret", "mcp-session-owned-by-A", time.Now())
	tracker.RecordSessionResponse("token-A-secret", "mcp-session-owned-by-A", "127.0.0.1:4567", "exec", 25)
	statusOther, bodyOther, _ := getSessionStats(t, s, "token-B-secret", "mcp-session-owned-by-A", "127.0.0.1:4567")
	if statusOther != http.StatusNotFound || strings.Contains(string(bodyOther), "25") {
		t.Fatalf("token B could inspect token A MCP session: status=%d body=%s", statusOther, bodyOther)
	}
	if int(a["est_tokens"].(float64)) != 12 {
		t.Fatalf("token A est_tokens=%v, want floor(50/4)=12", a["est_tokens"])
	}
	budget := a["budget"].(map[string]any)
	if int(budget["used_pct"].(float64)) != 50 || budget["warn"].(bool) {
		t.Fatalf("token A budget=%#v", budget)
	}
}

func TestSessionBudgetWarningOnlyOnSubsequentOverBudgetResponses(t *testing.T) {
	s, _ := newSessionStatsServer(t, "5")
	handler := s.sessionBudgetMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/example", nil)
		req.RemoteAddr = "127.0.0.1:9876"
		req = req.WithContext(auth.WithPrincipal(req.Context(), sessionStatsPrincipal("token-warning")))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	first := request()
	if strings.Contains(first.Body.String(), "budget_warning") {
		t.Fatalf("threshold-crossing response should not warn: %s", first.Body.String())
	}
	second := request()
	var decoded map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["budget_warning"]; !ok {
		t.Fatalf("subsequent over-budget response lacks warning: %s", second.Body.String())
	}
}

func TestSessionBudgetWarningAbsentBelowThreshold(t *testing.T) {
	s, _ := newSessionStatsServer(t, "1000")
	handler := s.sessionBudgetMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/example", nil)
		req.RemoteAddr = "127.0.0.1:9877"
		req = req.WithContext(auth.WithPrincipal(req.Context(), sessionStatsPrincipal("token-under")))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "budget_warning") {
			t.Fatalf("under-budget response unexpectedly warned: %s", rec.Body.String())
		}
	}
}

func TestMCPToolResponsesReceiveBudgetWarningAfterThreshold(t *testing.T) {
	s, tracker := newSessionStatsServer(t, "1")
	principal := sessionStatsPrincipal("token-mcp")
	call := func(sessionID, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
		rec := httptest.NewRecorder()
		tracker.HandleStreamableHTTP(rec, req)
		return rec
	}
	init := call("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if init.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", init.Code, init.Body.String())
	}
	sessionID := init.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize did not return MCP session ID")
	}
	toolCall := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_devices","arguments":{}}}`
	first := call(sessionID, toolCall)
	if strings.Contains(first.Body.String(), "budget_warning") {
		t.Fatalf("threshold-crossing MCP response should not warn: %s", first.Body.String())
	}
	second := call(sessionID, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_devices","arguments":{}}}`)
	var decoded map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	result := decoded["result"].(map[string]any)
	if _, ok := result["budget_warning"]; !ok {
		t.Fatalf("subsequent MCP tool response lacks warning: %s", second.Body.String())
	}
	status, body, stats := getSessionStats(t, s, "token-mcp", sessionID, "127.0.0.1:54321")
	if status != http.StatusOK {
		t.Fatalf("stats status=%d body=%s", status, body)
	}
	if int(stats["tool_calls"].(float64)) != 2 {
		t.Fatalf("tool_calls=%v, want 2", stats["tool_calls"])
	}
	if int(stats["bytes_returned"].(float64)) != first.Body.Len()+second.Body.Len() {
		t.Fatalf("bytes_returned=%v, expected response body sum %d", stats["bytes_returned"], first.Body.Len()+second.Body.Len())
	}
	budget := stats["budget"].(map[string]any)
	if !budget["warn"].(bool) {
		t.Fatalf("budget should be over threshold: %#v", budget)
	}
}

func TestSessionBudgetDefaultLimit(t *testing.T) {
	t.Setenv("VPS_COMMANDER_SESSION_BUDGET_BYTES", "")
	tracker := mcp.NewServer(sessionStatsBackend{}, nil)
	s := &Server{MCP: tracker}
	status, body, decoded := getSessionStats(t, s, "token-default", "", "127.0.0.1:2222")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	budget := decoded["budget"].(map[string]any)
	if int64(budget["limit_bytes"].(float64)) != 2097152 {
		t.Fatalf("default limit=%v, want 2097152", budget["limit_bytes"])
	}
}
