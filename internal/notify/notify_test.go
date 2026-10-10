package notify

import (
	"database/sql"
	"encoding/json"
	"github.com/wjyhk/vps-commander/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTestTargetAndPayload(t *testing.T) {
	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		got = string(b[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()
	db := t.TempDir() + "/test.db"
	st, err := storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	m := New(st)
	m.Client = srv.Client()
	if err := m.TestTarget(Target{ID: "t", Name: "test", Type: "generic", URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("empty webhook body")
	}
}

func TestWebhookStorage(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "x", Name: "X", Type: "generic", URL: "http://127.0.0.1", Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	xs, err := st.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(xs) != 1 || xs[0].ID != "x" {
		t.Fatalf("unexpected targets: %#v", xs)
	}
	if err := st.DeleteWebhookTarget("x"); err != nil {
		t.Fatal(err)
	}
}

func offlineFixture(t *testing.T) (*Manager, *storage.Store, *[]string, *time.Time) {
	t.Helper()
	var mu sync.Mutex
	msgs := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		_ = json.NewDecoder(r.Body).Decode(&e)
		mu.Lock()
		msgs = append(msgs, e.Type+"|"+e.Message)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	st, err := storage.Open(t.TempDir() + "/n.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB.Close() })
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "t", Name: "t", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	m := New(st)
	m.Client = srv.Client()
	clock := time.Date(2026, 10, 11, 3, 0, 0, 0, time.Local)
	m.clock = func() time.Time { return clock }
	return m, st, &msgs, &clock
}

func setDevice(t *testing.T, st *storage.Store, name, status string, offlineAt any) {
	t.Helper()
	if _, err := st.DB.Exec("INSERT INTO devices(name,status,is_local,last_seen,last_heartbeat,offline_at,created_at,updated_at) VALUES(?,?,0,0,0,?,0,0) ON CONFLICT(name) DO UPDATE SET status=excluded.status, offline_at=excluded.offline_at", name, status, offlineAt); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineAlertsAreLimitedPerEpisode(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	start := clock.Unix()
	setDevice(t, st, "node-a", "offline", start)
	expect := func(offset time.Duration, want int) {
		t.Helper()
		*clock = time.Unix(start, 0).Add(offset)
		m.check()
		if len(*msgs) != want {
			t.Fatalf("at +%s got %d notifications %v, want %d", offset, len(*msgs), *msgs, want)
		}
	}
	expect(30*time.Second, 0)
	expect(61*time.Second, 1)
	expect(5*time.Minute, 1)
	expect(10*time.Minute+time.Second, 2)
	expect(59*time.Minute, 2)
	expect(time.Hour+time.Second, 3)
	expect(3*time.Hour, 3)
	expect(30*time.Hour, 3)
	if !strings.Contains((*msgs)[0], "已离线 1 分钟") || !strings.Contains((*msgs)[2], "已离线 1 小时") || !strings.Contains((*msgs)[2], "不再重复提醒") {
		t.Fatalf("unexpected messages: %v", *msgs)
	}
	setDevice(t, st, "node-a", "online", nil)
	expect(31*time.Hour, 4)
	if !strings.Contains((*msgs)[3], "node_recovery") || !strings.Contains((*msgs)[3], "31 小时") {
		t.Fatalf("recovery message=%q", (*msgs)[3])
	}
	// A new disconnect is a new episode and alerts again.
	setDevice(t, st, "node-a", "offline", start+32*3600)
	expect(32*time.Hour+2*time.Minute, 5)
}

func TestOfflineFirstSeenLateIsQuiet(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	start := clock.Unix()
	setDevice(t, st, "node-b", "offline", start)
	*clock = time.Unix(start, 0).Add(5 * time.Hour)
	m.check()
	*clock = clock.Add(time.Hour)
	m.check()
	setDevice(t, st, "node-b", "online", nil)
	m.check()
	if len(*msgs) != 0 {
		t.Fatalf("late-observed offline node should be left to the daily report, got %v", *msgs)
	}
}

func TestDailyReportListsOfflineNodes(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	*clock = time.Date(2026, 10, 11, 8, 30, 0, 0, time.Local)
	since := clock.Add(-26 * time.Hour).Unix()
	setDevice(t, st, "node-c", "offline", since)
	setDevice(t, st, "node-d", "online", nil)
	m.check()
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "scheduled_report") || !strings.Contains((*msgs)[0], "离线节点：node-c") || !strings.Contains((*msgs)[0], "已 26 小时") {
		t.Fatalf("daily report=%v", *msgs)
	}
}

func TestUpdateDeviceStatusKeepsOfflineSince(t *testing.T) {
	_, st, _, _ := offlineFixture(t)
	setDevice(t, st, "node-e", "offline", int64(1000))
	if err := st.UpdateDeviceStatus("node-e", "offline", 900); err != nil {
		t.Fatal(err)
	}
	var at int64
	if err := st.DB.QueryRow("SELECT offline_at FROM devices WHERE name='node-e'").Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at != 1000 {
		t.Fatalf("offline_at=%d, want original 1000 kept across re-marking offline", at)
	}
	setDevice(t, st, "node-f", "online", nil)
	if err := st.UpdateDeviceStatus("node-f", "offline", 900); err != nil {
		t.Fatal(err)
	}
	var f sql.NullInt64
	_ = st.DB.QueryRow("SELECT offline_at FROM devices WHERE name='node-f'").Scan(&f)
	if !f.Valid || f.Int64 < 1000 {
		t.Fatalf("online->offline should set offline_at to now, got %v", f)
	}
}

func TestWebhookAuditHasSystemAttribution(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/audit.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "audit-target", Name: "audit", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	m := New(st)
	m.Client = srv.Client()
	m.emit(Event{Type: "test", Device: "node-a", Severity: "info", Message: "test", At: time.Now().Unix()})
	if err := st.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var actorType, actorID, actorName, transport, result string
	if err := st.DB.QueryRow(`SELECT actor_type,actor_id,actor_name,transport,result FROM audit_logs WHERE action='notification' AND command='webhook:audit-target' ORDER BY id DESC LIMIT 1`).Scan(&actorType, &actorID, &actorName, &transport, &result); err != nil {
		t.Fatal(err)
	}
	if actorType != "system" || actorID != "system:notify" || actorName != "system:notify" || transport != "internal" || result != "ok" {
		t.Fatalf("unexpected system audit: %q/%q/%q/%q/%q", actorType, actorID, actorName, transport, result)
	}
}
