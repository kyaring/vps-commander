package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wjyhk/vps-commander/internal/storage"
	"github.com/wjyhk/vps-commander/internal/sysinfo"
)

func TestLifecycleAcceptance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "acceptance.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open failed: %v", err)
	}
	defer store.DB.Close()

	secret := "acc-secret"
	mgr := NewManager(secret, store)

	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", mgr.ServeWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + ts.URL[4:] + "/agent/ws?device_name=node-test"
	header := http.Header{"Authorization": []string{"Bearer " + secret}}

	// [DoD 1] 初次注册持久化
	t.Run("DoD 1: Register and Persist", func(t *testing.T) {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			t.Fatalf("dial failed: %v", err)
		}
		defer c.Close()

		prof := sysinfo.Collect("APPLICATION")
		bProf, _ := json.Marshal(prof)
		_ = bProf

		msg := Message{
			Event:   "hello",
			Device:  "node-test",
			Arch:    "amd64",
			OS:      "linux",
			Profile: &prof,
			TS:      time.Now().Unix(),
		}
		if err := c.WriteJSON(msg); err != nil {
			t.Fatalf("writeJSON failed: %v", err)
		}
		time.Sleep(150 * time.Millisecond)

		rows, err := store.ListDevices()
		if err != nil || len(rows) != 1 {
			t.Fatalf("expected 1 row in DB, got: %d, err: %v", len(rows), err)
		}
		if rows[0].Status != "online" || rows[0].Arch != "amd64" || rows[0].OS != "linux" {
			t.Fatalf("DB device mismatch: %+v", rows[0])
		}
	})

	// [DoD 2] 离线状态保留
	t.Run("DoD 2: Offline Persistence on Disconnect", func(t *testing.T) {
		// 上个连接已 defer c.Close()，休眠等待触发 readLoop defer
		time.Sleep(150 * time.Millisecond)

		if mgr.Online("node-test") {
			t.Fatalf("Online('node-test') should be false after disconnect")
		}

		devs := mgr.Devices()
		var target map[string]any
		for _, d := range devs {
			if d["name"] == "node-test" {
				target = d
				break
			}
		}
		if target == nil {
			t.Fatalf("device vanished from mgr.Devices() on disconnect!")
		}
		if target["status"] != "offline" {
			t.Fatalf("device status expected 'offline', got: %v", target["status"])
		}

		rows, _ := store.ListDevices()
		if len(rows) != 1 || rows[0].Status != "offline" {
			t.Fatalf("store device status expected 'offline', got: %+v", rows)
		}
	})

	// [DoD 3] 离线执行防护
	t.Run("DoD 3: Offline Exec Guard", func(t *testing.T) {
		_, err := mgr.Exec(context.Background(), "node-test", ExecPayload{Command: "uname -a"})
		if err == nil || err.Error() != "device offline" {
			t.Fatalf("expected 'device offline' error, got: %v", err)
		}
	})

	// [DoD 4] 自动重连恢复在线
	t.Run("DoD 4: Reconnect Restore Online", func(t *testing.T) {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			t.Fatalf("dial failed: %v", err)
		}
		defer c.Close()

		prof := sysinfo.Collect("APPLICATION")
		_ = c.WriteJSON(Message{
			Event:   "ping",
			Device:  "node-test",
			Profile: &prof,
			TS:      time.Now().Unix(),
		})
		time.Sleep(150 * time.Millisecond)

		if !mgr.Online("node-test") {
			t.Fatalf("expected Online('node-test') to be true after reconnect")
		}
		devs := mgr.Devices()
		for _, d := range devs {
			if d["name"] == "node-test" && d["status"] != "online" {
				t.Fatalf("expected status 'online', got: %v", d["status"])
			}
		}
	})

	// [DoD 5] Hub 重启数据不丢 (从 DB 恢复)
	t.Run("DoD 5: Hub Restart Restore from DB", func(t *testing.T) {
		mgrNew := NewManager(secret, store)
		devs := mgrNew.Devices()
		var found map[string]any
		for _, d := range devs {
			if d["name"] == "node-test" {
				found = d
				break
			}
		}
		if found == nil {
			t.Fatalf("restarted Hub failed to restore device from DB")
		}
		if found["status"] != "offline" {
			t.Fatalf("restarted Hub initial device status should be offline, got: %v", found["status"])
		}
		if found["profile"] == nil {
			t.Fatalf("restarted Hub should restore profile snapshot")
		}
	})

	// [DoD 6] 主动注销清理与黑名单
	t.Run("DoD 6: Revoke Cleanup and Blacklist", func(t *testing.T) {
		if err := mgr.RevokeDevice("node-test", "admin action"); err != nil {
			t.Fatalf("RevokeDevice failed: %v", err)
		}
		if len(mgr.Devices()) != 0 {
			t.Fatalf("expected 0 devices in mgr, got: %d", len(mgr.Devices()))
		}
		rows, _ := store.ListDevices()
		if len(rows) != 0 {
			t.Fatalf("expected 0 devices in DB, got: %d", len(rows))
		}
		revoked, _ := store.IsRevoked("node-test")
		if !revoked {
			t.Fatalf("expected device to be recorded in revoked_devices")
		}
	})
}
