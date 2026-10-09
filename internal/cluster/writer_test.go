package cluster

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNodeSingleWriterSerializesConcurrentMessages(t *testing.T) {
	const count = 64
	received := make(chan string, count)
	serverDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		for i := 0; i < count; i++ {
			var msg Message
			if err := conn.ReadJSON(&msg); err != nil {
				serverDone <- err
				return
			}
			received <- msg.ID
		}
		serverDone <- nil
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	n := &Node{Conn: conn}
	n.startWriter()
	defer n.stopWriter()
	var wg sync.WaitGroup
	errs := make(chan error, count)
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(i int) {
			defer wg.Done()
			errs <- n.write(Message{ID: fmt.Sprintf("msg-%02d", i)})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("write failed: %v", err)
		}
	}
	seen := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		select {
		case id := <-received:
			if seen[id] {
				t.Errorf("duplicate message %q", id)
			}
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for serialized messages")
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server read failed: %v", err)
	}
	if len(seen) != count {
		t.Fatalf("received %d unique messages, want %d", len(seen), count)
	}
}
