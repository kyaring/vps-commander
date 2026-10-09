package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wjyhk/vps-commander/internal/cluster"
)

func TestBuildConfigRequiresExplicitHubAndTokens(t *testing.T) {
	if _, err := buildConfig("", "agent", "mcp", "", "", 1, 1, time.Minute, time.Second, time.Second, time.Second, time.Second, "loadtest", "exec_command", false); err == nil {
		t.Fatal("expected empty Hub URL to fail")
	}
	if _, err := buildConfig("http://127.0.0.1:9521", "", "mcp", "", "", 1, 1, time.Minute, time.Second, time.Second, time.Second, time.Second, "loadtest", "exec_command", false); err == nil {
		t.Fatal("expected missing Agent token to fail")
	}
}

func TestAgentWSURLUsesHubSchemeAndUniqueDeviceName(t *testing.T) {
	base, err := url.Parse("https://hub.example:9443")
	if err != nil {
		t.Fatal(err)
	}
	got, err := url.Parse(agentWSURL(base, "loadtest-001"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Scheme != "wss" || got.Path != "/agent/ws" || got.Query().Get("device_name") != "loadtest-001" {
		t.Fatalf("unexpected WebSocket URL: %s", got)
	}
}

func TestStaggerSpreadsConnections(t *testing.T) {
	if got := stagger(0, 10, 10*time.Second); got != 0 {
		t.Fatalf("first stagger=%s", got)
	}
	if got := stagger(5, 10, 10*time.Second); got != 5*time.Second {
		t.Fatalf("middle stagger=%s", got)
	}
	if got := stagger(9, 10, 10*time.Second); got >= 10*time.Second {
		t.Fatalf("last stagger outside ramp: %s", got)
	}
}

func TestReadSSEEventAndToolResultError(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(": keepalive\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"isError\":true}}\n\n"))
	event, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if event.name != "message" || !strings.Contains(event.data, `"id":1`) {
		t.Fatalf("event=%+v", event)
	}
	if !toolResultIsError([]byte(`{"isError":true}`)) {
		t.Fatal("expected isError=true to be detected")
	}
	if toolResultIsError([]byte(`{"isError":false}`)) {
		t.Fatal("isError=false should not be detected")
	}
}

func TestStreamableRPCNegotiatesSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Protocol-Version", "2025-03-26")
		if request.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "session-test")
		}
		result := json.RawMessage(`{}`)
		if request.Method == "tools/list" {
			result = json.RawMessage(`{"tools":[{"name":"list_devices"}]}`)
		}
		if request.Method == "tools/call" {
			result = json.RawMessage(`{"isError":false,"content":[]}`)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	stats := &counters{}
	client := &http.Client{Timeout: time.Second}
	resp, status, sessionID, version, err := streamableRPC(context.Background(), base, "test-token", "", "", client, rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: initializeParams()}, stats)
	if err != nil || status != http.StatusOK || resp == nil {
		t.Fatalf("initialize response=%#v status=%d err=%v", resp, status, err)
	}
	if sessionID != "session-test" || version != "2025-03-26" {
		t.Fatalf("session=%q version=%q", sessionID, version)
	}
	resp, status, _, _, err = streamableRPC(context.Background(), base, "test-token", sessionID, version, client, rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"}, stats)
	if err != nil || status != http.StatusOK || resp == nil {
		t.Fatalf("tools/list response=%#v status=%d err=%v", resp, status, err)
	}
}

func TestSSEClientPostsAndReceivesJSONRPCResponse(t *testing.T) {
	responses := make(chan []byte, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, "event: endpoint\ndata: /mcp/message?sessionId=session-test\n\n")
		flusher.Flush()
		for {
			select {
			case payload := <-responses:
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/mcp/message", func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payload, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"ok":true}`)})
		responses <- payload
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	base, _ := url.Parse(server.URL)
	stats := &counters{}
	client := &http.Client{Transport: newHTTPTransport()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openSSE(ctx, base, "test-token", client, stats)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()
	response, err := stream.call(ctx, rpcRequest{JSONRPC: "2.0", ID: 7, Method: "tools/list"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(response.ID) != "7" || string(response.Result) != `{"ok":true}` {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestFakeAgentRespondsToHubPingAndExec(t *testing.T) {
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		var hello cluster.Message
		if err := conn.ReadJSON(&hello); err != nil {
			serverErrors <- err
			return
		}
		if hello.Event != "hello" || hello.Device != "loadtest-agent" {
			serverErrors <- fmt.Errorf("unexpected hello: %#v", hello)
			return
		}
		if err := conn.WriteJSON(cluster.Message{Event: "ping", TS: 1}); err != nil {
			serverErrors <- err
			return
		}
		var pong cluster.Message
		if err := conn.ReadJSON(&pong); err != nil {
			serverErrors <- err
			return
		}
		if pong.Event != "pong" {
			serverErrors <- fmt.Errorf("expected pong, got %#v", pong)
			return
		}
		if err := conn.WriteJSON(cluster.Message{ID: "exec-1", Action: "exec"}); err != nil {
			serverErrors <- err
			return
		}
		var reply cluster.Message
		if err := conn.ReadJSON(&reply); err != nil {
			serverErrors <- err
			return
		}
		if reply.ID != "exec-1" || reply.Status != "ok" || reply.Stdout != "loadtest-ok" {
			serverErrors <- fmt.Errorf("unexpected exec reply: %#v", reply)
			return
		}
		serverErrors <- nil
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stats := &counters{}
	agentDone := make(chan error, 1)
	go func() { agentDone <- runAgentConnection(ctx, conn, "loadtest-agent", stats) }()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		_ = conn.Close()
		t.Fatal("fake Agent did not answer ping/exec in time")
	}
	cancel()
	_ = conn.Close()
	select {
	case <-agentDone:
	case <-time.After(3 * time.Second):
		t.Fatal("fake Agent loop did not stop")
	}
	if got := stats.agentExecResponses.Load(); got != 1 {
		t.Fatalf("exec responses=%d want 1", got)
	}
}
