package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPSessionIdentityIgnoresSourcePortAndSharesMemo(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	const token = "same-token"
	const ip = "192.0.2.51"

	post := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("port-check", "shared-value", nil), token, ip+":45873")
	if post.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
	statusA, bodyA, statsA := getSessionStats(t, server, token, "", ip+":45873")
	statusB, bodyB, statsB := getSessionStats(t, server, token, "", ip+":45874")
	if statusA != http.StatusOK || statusB != http.StatusOK {
		t.Fatalf("stats statuses A=%d B=%d; A=%s B=%s", statusA, statusB, bodyA, bodyB)
	}
	if statsA["session_id"] != statsB["session_id"] {
		t.Fatalf("source port changed session: A=%v B=%v", statsA["session_id"], statsB["session_id"])
	}
	get := sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=port-check", "", token, ip+":45874")
	if get.Code != http.StatusOK || !containsJSONString(get.Body.String(), `"value":"shared-value"`) {
		t.Fatalf("memo read using different source port status=%d body=%s", get.Code, get.Body.String())
	}
}

func TestHTTPSessionIdentitySeparatesTokens(t *testing.T) {
	server, _, _ := newSessionMemoServer()
	statusA, bodyA, statsA := getSessionStats(t, server, "token-A", "", "192.0.2.52:45873")
	statusB, bodyB, statsB := getSessionStats(t, server, "token-B", "", "192.0.2.52:45873")
	if statusA != http.StatusOK || statusB != http.StatusOK {
		t.Fatalf("stats statuses A=%d B=%d; A=%s B=%s", statusA, statusB, bodyA, bodyB)
	}
	if statsA["session_id"] == statsB["session_id"] {
		t.Fatalf("different tokens shared session ID %v", statsA["session_id"])
	}
}

func TestHTTPSessionExpiresAfterIdleAndClearsMemo(t *testing.T) {
	t.Setenv("VPS_COMMANDER_HTTP_SESSION_IDLE_MINUTES", "0.001")
	server, _, _ := newSessionMemoServer()
	const token = "idle-token"
	const remoteA = "192.0.2.53:45873"
	const remoteB = "192.0.2.53:45874"

	post := sessionMemoRequest(t, server, http.MethodPost, "/api/v1/session/memo", memoPostBody("idle-check", "must-expire", nil), token, remoteA)
	if post.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
	statusBefore, bodyBefore, before := getSessionStats(t, server, token, "", remoteA)
	if statusBefore != http.StatusOK {
		t.Fatalf("initial stats status=%d body=%s", statusBefore, bodyBefore)
	}
	time.Sleep(150 * time.Millisecond)
	statusAfter, bodyAfter, after := getSessionStats(t, server, token, "", remoteB)
	if statusAfter != http.StatusOK {
		t.Fatalf("post-expiry stats status=%d body=%s", statusAfter, bodyAfter)
	}
	if before["session_id"] == after["session_id"] {
		t.Fatalf("idle expiry did not create a new session: before=%v after=%v", before["session_id"], after["session_id"])
	}
	if after["tool_calls"].(float64) != 0 || after["bytes_returned"].(float64) != 0 {
		t.Fatalf("new session retained prior stats: %#v", after)
	}
	get := sessionMemoRequest(t, server, http.MethodGet, "/api/v1/session/memo?key=idle-check", "", token, remoteB)
	if get.Code != http.StatusNotFound {
		t.Fatalf("memo survived idle expiry: status=%d body=%s", get.Code, get.Body.String())
	}
}

func containsJSONString(body, fragment string) bool {
	return strings.Contains(body, fragment)
}
