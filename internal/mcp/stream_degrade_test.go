package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
)

const listToolsBody = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`

func streamReq(method, body, token, sid string) *http.Request {
	req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	p := auth.Principal{Type: "api_token", ID: token, Name: token, TokenID: token,
		Scopes: map[string]bool{"devices:read": true, "mcp:call": true, "files:read": true, "exec": true}}
	return req.WithContext(auth.WithPrincipal(req.Context(), p))
}

// 未知会话 id：按新会话处理，请求照常执行，响应头返回新的会话 id。
func TestUnknownSessionIsDegradedToNewSession(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	rec := httptest.NewRecorder()
	srv.HandleStreamableHTTP(rec, streamReq(http.MethodPost, listToolsBody, "tok-A", "never-issued"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"tools"`) {
		t.Fatalf("request not executed: %q", rec.Body.String())
	}
	newID := rec.Header().Get("Mcp-Session-Id")
	if newID == "" || newID == "never-issued" {
		t.Fatalf("new session id = %q", newID)
	}
	srv.mu.Lock()
	owner := srv.streamSessions[newID].ownerTokenID
	srv.mu.Unlock()
	if owner != "tok-A" {
		t.Fatalf("new session owner = %q, want tok-A", owner)
	}
}

// 降级后的新会话不会把预算清零：同一令牌之前的字节仍计入。
func TestDegradedSessionDoesNotResetTokenBudget(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	srv.RecordSessionResponse("tok-A", "old-session", "", "read_file", 1000)
	rec := httptest.NewRecorder()
	srv.HandleStreamableHTTP(rec, streamReq(http.MethodPost, listToolsBody, "tok-A", "gone-away"))
	newID := rec.Header().Get("Mcp-Session-Id")
	stats, ok := srv.SessionBudgetStats("tok-A", newID, "")
	if !ok {
		t.Fatal("no stats for degraded session")
	}
	if stats.BytesReturned != 1000 {
		t.Fatalf("bytes after degrade = %d, want 1000 (budget must not reset)", stats.BytesReturned)
	}
}

// 会话存在但属于其他令牌：仍然 403，绝不降级、绝不复用，也不新建会话。
func TestSessionOwnedByOtherTokenIsRefusedNotDegraded(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	owned, status, _ := srv.openStreamSession(auth.Principal{Type: "api_token", ID: "owner", Name: "owner", TokenID: "owner"}, "2025-03-26", time.Now())
	if status != 0 {
		t.Fatalf("setup failed: %d", status)
	}
	before := len(srv.streamSessions)
	rec := httptest.NewRecorder()
	srv.HandleStreamableHTTP(rec, streamReq(http.MethodPost, listToolsBody, "intruder", owned))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(srv.streamSessions) != before {
		t.Fatal("a session was created for the intruder")
	}
	if rec.Header().Get("Mcp-Session-Id") != "" {
		t.Fatal("intruder was handed a session id")
	}
}

// 无效令牌仍然 401，即使带了未知会话 id。
func TestUnknownSessionWithInvalidTokenIs401(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	mw := auth.Middleware{Token: "right-token"}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listToolsBody))
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("Mcp-Session-Id", "never-issued")
	rec := httptest.NewRecorder()
	mw.Handler(http.HandlerFunc(srv.HandleStreamableHTTP)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(srv.streamSessions) != 0 {
		t.Fatal("a session was created for an unauthenticated request")
	}
}

// DELETE 一个未知或已结束的会话：没有可以结束的会话，幂等返回 204，不新建会话。
func TestDeleteUnknownSessionIsNoContent(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	rec := httptest.NewRecorder()
	srv.HandleStreamableHTTP(rec, streamReq(http.MethodDelete, "", "tok-A", "already-ended"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(srv.streamSessions) != 0 {
		t.Fatal("DELETE created a session")
	}
}
