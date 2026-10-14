package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

type sessionMemoAuditRecord struct {
	action, command, detail string
	code                    int
}

type sessionMemoTestBackend struct {
	sessionStatsBackend
	mu     sync.Mutex
	audits []sessionMemoAuditRecord
}

func (b *sessionMemoTestBackend) AuditMCPAction(_ context.Context, _, _, action, command string, code int, _ int64, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.audits = append(b.audits, sessionMemoAuditRecord{action: action, command: command, code: code, detail: detail})
}

func (b *sessionMemoTestBackend) auditText() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out strings.Builder
	for _, a := range b.audits {
		fmt.Fprintf(&out, "%s %s %d %s\n", a.action, a.command, a.code, a.detail)
	}
	return out.String()
}

func newSessionMemoServer() (*Server, *mcp.Server, *sessionMemoTestBackend) {
	backend := &sessionMemoTestBackend{}
	tracker := mcp.NewServer(backend, nil)
	return &Server{MCP: tracker}, tracker, backend
}

func sessionMemoRequest(t *testing.T, server *Server, method, path, body, tokenID, remoteAddr string) *httptest.ResponseRecorder {
	return sessionMemoRequestWithSession(t, server, method, path, body, tokenID, remoteAddr, "")
}

func sessionMemoRequestWithSession(t *testing.T, server *Server, method, path, body, tokenID, remoteAddr, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api", ID: tokenID, TokenID: tokenID}))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	return rec
}

func memoPostBody(key, value string, ttl *int64) string {
	body := map[string]any{"key": key, "value": value}
	if ttl != nil {
		body["ttl_seconds"] = *ttl
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func TestSessionMemoWriteReadOverwriteAndNotFound(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	remote := "192.0.2.10:1234"
	post := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("sha", "first", nil), "token-a", remote)
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), `"bytes":5`) {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
	get := sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=sha", "", "token-a", remote)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"value":"first"`) {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	post = sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("sha", "replacement", nil), "token-a", remote)
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), `"bytes":11`) {
		t.Fatalf("overwrite status=%d body=%s", post.Code, post.Body.String())
	}
	get = sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=sha", "", "token-a", remote)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"value":"replacement"`) {
		t.Fatalf("read overwritten value status=%d body=%s", get.Code, get.Body.String())
	}
	missing := sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=missing", "", "token-a", remote)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing key status=%d body=%s, want 404", missing.Code, missing.Body.String())
	}
}

func TestSessionMemoListReturnsMetadataOnly(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	secretValue := "MEMO_VALUE_MUST_NOT_APPEAR_IN_LIST_8d20"
	post := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("verified-sha", secretValue, nil), "token-list", "192.0.2.11:1234")
	if post.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
	list := sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo", "", "token-list", "192.0.2.11:1234")
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	if strings.Contains(body, secretValue) || strings.Contains(body, `"value"`) {
		t.Fatalf("list leaked memo value: %s", body)
	}
	for _, field := range []string{`"key":"verified-sha"`, `"bytes":`, `"updated_at":`} {
		if !strings.Contains(body, field) {
			t.Fatalf("list missing %s: %s", field, body)
		}
	}
}

func TestSessionMemoTokenIsolation(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	remote := "192.0.2.12:1234"
	post := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("private", "token-a-value", nil), "token-a", remote)
	if post.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
	get := sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=private", "", "token-b", remote)
	if get.Code != http.StatusNotFound || strings.Contains(get.Body.String(), "token-a-value") {
		t.Fatalf("cross-token read status=%d body=%s", get.Code, get.Body.String())
	}
	post = sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("private", "token-b-value", nil), "token-b", remote)
	if post.Code != http.StatusOK {
		t.Fatalf("token B POST status=%d body=%s", post.Code, post.Body.String())
	}
	get = sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=private", "", "token-a", remote)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "token-a-value") || strings.Contains(get.Body.String(), "token-b-value") {
		t.Fatalf("reverse cross-token read status=%d body=%s", get.Code, get.Body.String())
	}
}

func TestSessionMemoUsesMCPStatsSessionIdentity(t *testing.T) {
	server, tracker, _ := newSessionMemoServer()
	tracker.EnsureSessionBudget("token-mcp-session", "mcp-session-123", time.Now())
	remote := "192.0.2.18:1234"
	post := sessionMemoRequestWithSession(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("shared", "same-session", nil), "token-mcp-session", remote, "mcp-session-123")
	if post.Code != http.StatusOK {
		t.Fatalf("MCP-session POST status=%d body=%s", post.Code, post.Body.String())
	}
	get := sessionMemoRequestWithSession(t, server, http.MethodGet, "/api/v1/session/memo?key=shared", "", "token-mcp-session", remote, "mcp-session-123")
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"value":"same-session"`) {
		t.Fatalf("MCP-session GET status=%d body=%s", get.Code, get.Body.String())
	}
	otherToken := sessionMemoRequestWithSession(t, server, http.MethodGet, "/api/v1/session/memo?key=shared", "", "other-token", remote, "mcp-session-123")
	if otherToken.Code != http.StatusNotFound {
		t.Fatalf("foreign token accessed MCP session: status=%d body=%s", otherToken.Code, otherToken.Body.String())
	}
}

func TestSessionMemoQuotaValueLimit413(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	body := memoPostBody("large", strings.Repeat("v", mcp.SessionMemoMaxValueBytes+1), nil)
	resp := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", body, "token-value-limit", "192.0.2.13:1234")
	if resp.Code != http.StatusRequestEntityTooLarge || !strings.Contains(resp.Body.String(), "4096") || !strings.Contains(resp.Body.String(), "current usage") {
		t.Fatalf("value quota status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSessionMemoQuotaEntryLimit413(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	remote := "192.0.2.14:1234"
	for i := 0; i < mcp.SessionMemoMaxEntries; i++ {
		resp := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody(fmt.Sprintf("key-%02d", i), "x", nil), "token-entry-limit", remote)
		if resp.Code != http.StatusOK {
			t.Fatalf("write %d status=%d body=%s", i, resp.Code, resp.Body.String())
		}
	}
	resp := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("overflow", "x", nil), "token-entry-limit", remote)
	if resp.Code != http.StatusRequestEntityTooLarge || !strings.Contains(resp.Body.String(), "entries=32") || !strings.Contains(resp.Body.String(), "limit=32") {
		t.Fatalf("entry quota status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSessionMemoQuotaTotalBytes413(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	remote := "192.0.2.15:1234"
	for i := 0; i < mcp.SessionMemoMaxTotalBytes/mcp.SessionMemoMaxValueBytes; i++ {
		resp := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody(fmt.Sprintf("key-%d", i), strings.Repeat("x", mcp.SessionMemoMaxValueBytes), nil), "token-total-limit", remote)
		if resp.Code != http.StatusOK {
			t.Fatalf("write %d status=%d body=%s", i, resp.Code, resp.Body.String())
		}
	}
	resp := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("overflow", "x", nil), "token-total-limit", remote)
	if resp.Code != http.StatusRequestEntityTooLarge || !strings.Contains(resp.Body.String(), "32768") || !strings.Contains(resp.Body.String(), "current usage=32768") {
		t.Fatalf("total quota status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSessionMemoTTLExpires(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	remote := "192.0.2.16:1234"
	ttl := int64(1)
	resp := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("short-lived", "ephemeral", &ttl), "token-ttl", remote)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"expires_at"`) {
		t.Fatalf("TTL POST status=%d body=%s", resp.Code, resp.Body.String())
	}
	time.Sleep(1100 * time.Millisecond)
	resp = sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=short-lived", "", "token-ttl", remote)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expired memo status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSessionMemoAuditAndLogsNeverContainValue(t *testing.T) {
	server, _, backend := newSessionMemoServer()
	secretValue := "ULTRA_SECRET_MEMO_SENTINEL_94f4"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	resp := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("audit-key", secretValue, nil), "token-audit", "192.0.2.17:1234")
	if resp.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", resp.Code, resp.Body.String())
	}
	_ = sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=audit-key", "", "token-audit", "192.0.2.17:1234")
	_ = sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo", "", "token-audit", "192.0.2.17:1234")
	audit := backend.auditText()
	if strings.Contains(audit, secretValue) || strings.Contains(logs.String(), secretValue) {
		t.Fatalf("value leaked into audit or logs; audit=%q logs=%q", audit, logs.String())
	}
	if !strings.Contains(audit, "audit-key") || !strings.Contains(audit, "bytes=") {
		t.Fatalf("audit must contain key and byte count: %q", audit)
	}
}

func TestSessionMemoMCPToolsAndMetadataList(t *testing.T) {
	_, tracker, _ := newSessionMemoServer()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api", ID: "mcp-token", TokenID: "mcp-token"})
	list := tracker.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/list"})
	encoded, _ := json.Marshal(list.Result)
	if !strings.Contains(string(encoded), `"memo_set"`) || !strings.Contains(string(encoded), `"memo_get"`) {
		t.Fatalf("tools/list missing memo tools: %s", encoded)
	}
	setArgs, _ := json.Marshal(map[string]any{"key": "mcp-key", "value": "MCP_SECRET_VALUE"})
	set := tracker.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call", Params: json.RawMessage(`{"name":"memo_set","arguments":` + string(setArgs) + `}`)})
	setResult, ok := set.Result.(mcp.CallToolResult)
	if !ok || len(setResult.Content) == 0 || strings.Contains(setResult.Content[0].Text, "MCP_SECRET_VALUE") {
		t.Fatalf("memo_set result invalid: %#v", set.Result)
	}
	var setPayload map[string]any
	if err := json.Unmarshal([]byte(setResult.Content[0].Text), &setPayload); err != nil || setPayload["bytes"] != float64(16) {
		t.Fatalf("memo_set payload invalid: %s", setResult.Content[0].Text)
	}
	getValue := tracker.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("3"), Method: "tools/call", Params: json.RawMessage(`{"name":"memo_get","arguments":{"key":"mcp-key"}}`)})
	valueResult, ok := getValue.Result.(mcp.CallToolResult)
	if !ok || len(valueResult.Content) == 0 || !strings.Contains(valueResult.Content[0].Text, "MCP_SECRET_VALUE") {
		t.Fatalf("memo_get by key failed: %#v", getValue.Result)
	}
	getList := tracker.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("4"), Method: "tools/call", Params: json.RawMessage(`{"name":"memo_get","arguments":{}}`)})
	listResult, ok := getList.Result.(mcp.CallToolResult)
	if !ok || len(listResult.Content) == 0 {
		t.Fatalf("memo_get list result invalid: %#v", getList.Result)
	}
	listText := listResult.Content[0].Text
	if strings.Contains(listText, "MCP_SECRET_VALUE") || strings.Contains(listText, `"value"`) || !strings.Contains(listText, `"mcp-key"`) {
		t.Fatalf("memo_get list exposed value or omitted key: %s", listText)
	}
}
