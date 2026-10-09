package cluster

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wjyhk/vps-commander/internal/storage"
	"github.com/wjyhk/vps-commander/internal/sysinfo"
)

func TestReadLoopHelloPersistsImmediatelyAndPingDoesNotWriteDB(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	mgr := NewManager("test-secret", store)
	defer mgr.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", mgr.ServeWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	url := "ws" + ts.URL[4:] + "/agent/ws?device_name=profile-test"
	header := http.Header{"Authorization": []string{"Bearer test-secret"}}
	c, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	profile := sysinfo.Profile{CPUCores: 4, MemPercent: 30, UpdatedAt: time.Now().Unix()}
	if err := c.WriteJSON(Message{Event: "hello", Device: "profile-test", Arch: "amd64", OS: "linux", Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	waitFor := func(check func() bool) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return check()
	}
	if !waitFor(func() bool {
		var count int
		_ = store.DB.QueryRow("SELECT COUNT(*) FROM devices WHERE name=?", "profile-test").Scan(&count)
		return count == 1
	}) {
		t.Fatal("hello profile was not persisted immediately")
	}
	var beforeUpdated, beforeHeartbeat int64
	if err := store.DB.QueryRow("SELECT updated_at,last_heartbeat FROM devices WHERE name=?", "profile-test").Scan(&beforeUpdated, &beforeHeartbeat); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteJSON(Message{Event: "ping", Device: "profile-test", Arch: "amd64", OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	var afterUpdated, afterHeartbeat int64
	if err := store.DB.QueryRow("SELECT updated_at,last_heartbeat FROM devices WHERE name=?", "profile-test").Scan(&afterUpdated, &afterHeartbeat); err != nil {
		t.Fatal(err)
	}
	if afterUpdated != beforeUpdated || afterHeartbeat != beforeHeartbeat {
		t.Fatalf("profile-free ping unexpectedly wrote database: before=(%d,%d) after=(%d,%d)", beforeUpdated, beforeHeartbeat, afterUpdated, afterHeartbeat)
	}
}

func TestServeWSClosesConnectionWhenFrameExceedsReadLimit(t *testing.T) {
	mgr := NewManager("test-secret", nil)
	defer mgr.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", mgr.ServeWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	url := "ws" + ts.URL[4:] + "/agent/ws?device_name=oversized-frame-test"
	header := http.Header{"Authorization": []string{"Bearer test-secret"}}
	c, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	// Keep the payload valid JSON: without SetReadLimit, the server would parse
	// the message and continue instead of disconnecting this client.
	payload := []byte(`{"event":"ping","padding":"` + strings.Repeat("x", 8<<20) + `"}`)
	// The server may close the TCP connection while the client is still writing.
	_ = c.WriteMessage(websocket.TextMessage, payload)
	for {
		_, _, err = c.ReadMessage()
		if err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("server accepted a WebSocket frame larger than 8 MiB")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.RLock()
		n := mgr.nodes["oversized-frame-test"]
		mgr.mu.RUnlock()
		if n != nil && !n.Connected.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mgr.mu.RLock()
	n := mgr.nodes["oversized-frame-test"]
	mgr.mu.RUnlock()
	if n == nil || n.Connected.Load() {
		t.Fatal("server did not disconnect the oversized-frame client")
	}
}
