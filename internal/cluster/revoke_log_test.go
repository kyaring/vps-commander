package cluster

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// clusterLogBuffer is a concurrency-safe log sink for asserting on log lines.
type clusterLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *clusterLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *clusterLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestClusterRevokeWriteFailureIsLogged pins the audit finding that a dropped
// `revoked` downlink left the node running with no trace anywhere. The revoke
// path must keep its previous behavior (same return value, connection still
// closed), but the dropped downlink now has to be visible in the log.
func TestClusterRevokeWriteFailureIsLogged(t *testing.T) {
	var logs clusterLogBuffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Keep the server side open until the client closes so the client write
		// fails on a closed local connection rather than on a peer reset.
		_ = conn.ReadJSON(&Message{})
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
	if err != nil {
		t.Fatal(err)
	}
	// Break the connection so n.write fails deterministically.
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	m := NewManager("", nil)
	m.nodes["node-revoke"] = &Node{Name: "node-revoke", Conn: conn}
	if err := m.RevokeDevice("node-revoke", "audit log probe"); err != nil {
		t.Fatalf("RevokeDevice must not fail because the downlink failed: %v", err)
	}
	if _, still := m.nodes["node-revoke"]; still {
		t.Fatal("revoked node must still be removed from the manager")
	}

	const want = "cluster: revoked -> device node-revoke"
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("log does not contain %q; got:\n%s", want, logs.String())
	}
}
