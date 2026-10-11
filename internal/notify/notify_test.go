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

func TestOfflineFirstSeenLateUsesConfiguredReminderSchedule(t *testing.T) {
	newConfigured := func(t *testing.T) (*Manager, *storage.Store, *[]string, *time.Time, int64) {
		t.Helper()
		m, st, msgs, clock := offlineFixture(t)
		settings := storage.DefaultNotificationSettings()
		settings.OfflineReminderMinutes = []int{5, 20, 90}
		if err := st.SetNotificationSettings(settings); err != nil {
			t.Fatal(err)
		}
		start := clock.Unix()
		setDevice(t, st, "node-configured", "offline", start)
		return m, st, msgs, clock, start
	}

	t.Run("just before derived cutoff sends one catch-up alert", func(t *testing.T) {
		m, _, msgs, clock, start := newConfigured(t)
		*clock = time.Unix(start, 0).Add(179 * time.Minute)
		m.check()
		if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "后续不再重复提醒") || !strings.Contains((*msgs)[0], "2 小时 59 分钟") {
			t.Fatalf("expected one current-stage catch-up alert at 179m, got %v", *msgs)
		}
	})

	t.Run("at derived cutoff stays quiet", func(t *testing.T) {
		m, _, msgs, clock, start := newConfigured(t)
		*clock = time.Unix(start, 0).Add(180 * time.Minute)
		m.check()
		if len(*msgs) != 0 {
			t.Fatalf("expected quiet at configured 180m cutoff, got %v", *msgs)
		}
	})
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

func setResourceProfile(t *testing.T, st *storage.Store, name, profile string) {
	t.Helper()
	_, err := st.DB.Exec(`INSERT INTO devices(name,status,is_local,profile_json,last_heartbeat,created_at,updated_at) VALUES(?,'online',0,?,1,1,1) ON CONFLICT(name) DO UPDATE SET status='online',profile_json=excluded.profile_json`, name, profile)
	if err != nil {
		t.Fatal(err)
	}
}

func TestResourceAlertsLimitedToThreeThenRecoveryAndNewEpisode(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	setResourceProfile(t, st, "node-r", `{"mem_percent":95,"disk_percent":93,"cpu_usage_percent":98}`)
	for i := 0; i < 3; i++ {
		m.check()
	}
	if len(*msgs) != 3 {
		t.Fatalf("initial threshold alerts=%d messages=%v want 3", len(*msgs), *msgs)
	}
	if !strings.Contains((*msgs)[0], "resource_threshold") {
		t.Fatalf("initial event=%q", (*msgs)[0])
	}
	*clock = clock.Add(29 * time.Minute)
	m.check()
	if len(*msgs) != 3 {
		t.Fatalf("alert before 30m: %v", *msgs)
	}
	*clock = clock.Add(2 * time.Minute)
	m.check()
	if len(*msgs) != 6 {
		t.Fatalf("30m reminder count=%d messages=%v", len(*msgs), *msgs)
	}
	*clock = clock.Add(90 * time.Minute)
	m.check()
	if len(*msgs) != 9 {
		t.Fatalf("2h reminder count=%d messages=%v", len(*msgs), *msgs)
	}
	*clock = clock.Add(3 * time.Hour)
	m.check()
	if len(*msgs) != 9 {
		t.Fatalf("persistent alert repeated after third reminder: %v", *msgs)
	}
	setResourceProfile(t, st, "node-r", `{"mem_percent":30,"disk_percent":40,"cpu_usage_percent":15}`)
	for i := 0; i < 3; i++ {
		m.check()
	}
	if len(*msgs) != 12 {
		t.Fatalf("recovery events=%d messages=%v want 3", len(*msgs), *msgs)
	}
	for _, msg := range (*msgs)[9:] {
		if !strings.Contains(msg, "resource_recovery") {
			t.Errorf("expected recovery event: %q", msg)
		}
	}
	setResourceProfile(t, st, "node-r", `{"mem_percent":95,"disk_percent":93,"cpu_usage_percent":98}`)
	for i := 0; i < 3; i++ {
		m.check()
	}
	if len(*msgs) != 15 {
		t.Fatalf("new episode did not alert again: %v", *msgs)
	}
}

func TestResourceRecoveryRequiresThreeConsecutiveBelowThresholdSamples(t *testing.T) {
	m, st, msgs, _ := offlineFixture(t)
	setResourceProfile(t, st, "node-flap", `{"mem_percent":95,"disk_percent":95,"cpu_usage_percent":95}`)
	for i := 0; i < 3; i++ {
		m.check()
	}
	if len(*msgs) != 3 {
		t.Fatalf("initial alerts=%d, want 3: %v", len(*msgs), *msgs)
	}

	// Disk dips below threshold twice, but a high sample between them must reset recovery.
	setResourceProfile(t, st, "node-flap", `{"mem_percent":95,"disk_percent":89,"cpu_usage_percent":95}`)
	m.check()
	m.check()
	if len(*msgs) != 3 {
		t.Fatalf("recovered before three low samples: %v", *msgs)
	}
	setResourceProfile(t, st, "node-flap", `{"mem_percent":95,"disk_percent":91,"cpu_usage_percent":95}`)
	m.check()
	setResourceProfile(t, st, "node-flap", `{"mem_percent":95,"disk_percent":89,"cpu_usage_percent":95}`)
	m.check()
	m.check()
	if len(*msgs) != 3 {
		t.Fatalf("recovered after interrupted low sequence: %v", *msgs)
	}
	m.check()
	if len(*msgs) != 4 || !strings.Contains((*msgs)[3], "resource_recovery") || !strings.Contains((*msgs)[3], "磁盘使用率") {
		t.Fatalf("three consecutive low samples should recover only disk: %v", *msgs)
	}

	// After recovery, three consecutive high samples create a new episode.
	setResourceProfile(t, st, "node-flap", `{"mem_percent":95,"disk_percent":91,"cpu_usage_percent":95}`)
	m.check()
	m.check()
	if len(*msgs) != 4 {
		t.Fatalf("new episode triggered before three high samples: %v", *msgs)
	}
	m.check()
	if len(*msgs) != 5 || !strings.Contains((*msgs)[4], "resource_threshold") || !strings.Contains((*msgs)[4], "磁盘使用率") {
		t.Fatalf("new episode did not trigger after three high samples: %v", *msgs)
	}
}

func TestPersistentResourceAlertsAppearInScheduledReport(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	setResourceProfile(t, st, "node-r", `{"mem_percent":95,"disk_percent":40,"cpu_usage_percent":10}`)
	for i := 0; i < 3; i++ {
		m.check()
	}
	*clock = time.Date(2026, 10, 11, 8, 29, 0, 0, time.Local)
	m.check() // overdue 30-minute reminder
	m.check() // overdue 2-hour reminder, third and final
	*clock = time.Date(2026, 10, 11, 8, 30, 0, 0, time.Local)
	m.check()
	if len(*msgs) < 2 || !strings.Contains((*msgs)[len(*msgs)-1], "scheduled_report") || !strings.Contains((*msgs)[len(*msgs)-1], "持续资源告警") || !strings.Contains((*msgs)[len(*msgs)-1], "node-r") {
		t.Fatalf("scheduled report omitted persistent resource alert: %v", *msgs)
	}
}

func TestWebhookRetriesOnceAndTracksConsecutiveFailures(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/webhook-state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	attempts := 0
	mode := 500
	responses := []int{500, 204}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		status := mode
		if len(responses) > 0 {
			status = responses[0]
			responses = responses[1:]
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "retry", Name: "retry", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	m := New(st)
	m.Client = srv.Client()
	event := Event{Type: "test", Severity: "info", Message: "retry test", At: time.Now().Unix()}
	m.emit(event)
	if attempts != 2 {
		t.Fatalf("first failure then success attempts=%d want 2", attempts)
	}
	items, err := st.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].ConsecutiveFailures != 0 || items[0].LastError != "" || items[0].LastStatus != 204 {
		t.Fatalf("success state=%+v", items[0])
	}
	attempts = 0
	responses = nil
	mode = 500
	m.emit(event)
	if attempts != 2 {
		t.Fatalf("failed delivery attempts=%d want 2", attempts)
	}
	items, err = st.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].ConsecutiveFailures != 1 || items[0].LastError != "webhook http 500" || items[0].LastStatus != 500 {
		t.Fatalf("first failed delivery state=%+v", items[0])
	}
	attempts = 0
	m.emit(event)
	items, err = st.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].ConsecutiveFailures != 2 {
		t.Fatalf("consecutive failures=%d want 2", items[0].ConsecutiveFailures)
	}
	mode = 204
	m.emit(event)
	items, err = st.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].ConsecutiveFailures != 0 || items[0].LastError != "" {
		t.Fatalf("success did not clear abnormal state: %+v", items[0])
	}
}

func TestAsyncWebhookDeliveryIsNonBlockingAndStopDrainsQueue(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/async-webhook.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mu sync.Mutex
	var attempts int
	var requestTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		requestTimes = append(requestTimes, time.Now())
		current := attempts
		mu.Unlock()
		if current == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "async", Name: "async", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	m := New(st)
	m.Client = srv.Client()
	defer m.Stop()
	m.Start()

	started := time.Now()
	m.emit(Event{Type: "async-test", Severity: "info", Message: "first", At: started.Unix()})
	m.emit(Event{Type: "async-test", Severity: "info", Message: "second", At: started.Unix()})
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("enqueue blocked for %s", elapsed)
	}
	// Stop must wait for the in-flight retry and drain the queued second event.
	m.Stop()
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("webhook attempts=%d, want 3 (one retry plus queued event)", attempts)
	}
	if len(requestTimes) != 3 || requestTimes[1].Sub(requestTimes[0]) < 900*time.Millisecond {
		t.Fatalf("retry was not delayed: request times=%v", requestTimes)
	}
}

func TestNotificationSettingsChangeThresholdsAndReportTimes(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	settings := storage.DefaultNotificationSettings()
	settings.MemoryThreshold = 70
	settings.DiskThreshold = 80
	settings.CPUThreshold = 85
	settings.MorningReportTime = "09:15"
	settings.EveningReportTime = "21:45"
	settings.ResourceReminderMinutes = []int{0, 1, 2}
	if err := st.SetNotificationSettings(settings); err != nil {
		t.Fatal(err)
	}
	setResourceProfile(t, st, "node-config", `{"mem_percent":75,"disk_percent":30,"cpu_usage_percent":20}`)
	for i := 0; i < 3; i++ {
		m.check()
	}
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "resource_threshold") {
		t.Fatalf("configured threshold not applied: %v", *msgs)
	}
	*clock = time.Date(2026, 10, 11, 8, 30, 0, 0, time.Local)
	m.check()
	for _, msg := range *msgs {
		if strings.Contains(msg, "scheduled_report") {
			t.Fatalf("report sent at old default time: %v", *msgs)
		}
	}
	*clock = time.Date(2026, 10, 11, 9, 15, 0, 0, time.Local)
	m.check()
	found := false
	for _, msg := range *msgs {
		if strings.Contains(msg, "scheduled_report") {
			found = true
		}
	}
	if !found {
		t.Fatalf("custom report time was ignored: %v", *msgs)
	}
}

func TestResourceAlertStateResumesAcrossRestarts(t *testing.T) {
	dbPath := t.TempDir() + "/resource-restart.db"
	var mu sync.Mutex
	var events []Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("decode event: %v", err)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	openStore := func() *storage.Store {
		t.Helper()
		st, err := storage.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := openStore()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "resource-test", Name: "resource-test", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO devices(name,status,is_local,last_seen,last_heartbeat,profile_json,created_at,updated_at) VALUES('resource-node','online',0,0,0,'{"mem_percent":95}',0,0)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 3, 0, 0, 0, time.Local)
	newManager := func(st *storage.Store) *Manager {
		m := New(st)
		m.Client = srv.Client()
		m.clock = func() time.Time { return now }
		return m
	}

	m := newManager(st)
	m.check() // strike 1
	now = now.Add(15 * time.Second)
	m.check() // strike 2, persisted before restart
	if err := st.DB.Close(); err != nil {
		t.Fatal(err)
	}

	st = openStore()
	m = newManager(st)
	now = now.Add(15 * time.Second)
	m.check() // strike 3: first alert, not a reset to strike 1
	mu.Lock()
	if len(events) != 1 || events[0].Type != "resource_threshold" {
		mu.Unlock()
		t.Fatalf("after strike-state restart, events = %#v", events)
	}
	mu.Unlock()
	if err := st.DB.Close(); err != nil {
		t.Fatal(err)
	}

	st = openStore()
	m = newManager(st)
	now = time.Date(2026, 10, 11, 3, 30, 31, 0, time.Local)
	m.check() // persisted Sent=1 means this is alert stage 2
	mu.Lock()
	if len(events) != 2 || !strings.Contains(events[1].Message, "已持续 30 分钟") {
		mu.Unlock()
		t.Fatalf("after active-alert restart, events = %#v", events)
	}
	mu.Unlock()

	if _, err := st.DB.Exec(`UPDATE devices SET profile_json='{"mem_percent":80}' WHERE name='resource-node'`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(15 * time.Second)
	m.check() // recovery 1
	now = now.Add(15 * time.Second)
	m.check() // recovery 2, persisted
	if err := st.DB.Close(); err != nil {
		t.Fatal(err)
	}

	st = openStore()
	m = newManager(st)
	now = now.Add(15 * time.Second)
	m.check() // recovery 3; closes the existing episode
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 || events[2].Type != "resource_recovery" {
		t.Fatalf("after recovery-state restart, events = %#v", events)
	}
	var count int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM resource_alert_states`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted resource states after recovery = %d, want 0", count)
	}
	_ = st.DB.Close()
}

func TestBuiltInNotificationShadowRecordsWithoutSending(t *testing.T) {
	requests := 0
	var received Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	st, err := storage.Open(t.TempDir() + "/builtin-shadow.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "builtin-hook", Name: "Built-in legacy", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VPS_COMMANDER_NOTIFICATION_V1_BUILTIN_MODE", "shadow")
	m := New(st)
	m.Client = srv.Client()
	e := Event{Type: "node_offline", Device: "wjyhk", Severity: "critical", Message: "node is offline", At: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Unix(), Data: map[string]any{"alert": "first", "offline_seconds": 60}}
	m.emit(e)
	if requests != 1 {
		t.Fatalf("shadow mode legacy send count=%d want exactly 1", requests)
	}
	if received.Type != e.Type || received.Severity != e.Severity || received.Message != e.Message || received.Device != e.Device {
		t.Fatalf("shadow-mode legacy payload changed: %+v", received)
	}
	var status, rendered string
	if err := st.DB.QueryRow(`SELECT d.status,d.rendered_message FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.event_type='node_offline'`).Scan(&status, &rendered); err != nil {
		t.Fatal(err)
	}
	if status != "shadow" || rendered != e.Message {
		t.Fatalf("shadow delivery status=%s rendered=%q", status, rendered)
	}
	var attempts int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM notification_delivery_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("shadow mode created %d outbox send attempts", attempts)
	}
	var severity, message string
	if err := st.DB.QueryRow(`SELECT severity,message FROM notification_events WHERE event_type='node_offline'`).Scan(&severity, &message); err != nil {
		t.Fatal(err)
	}
	if severity != "P1" || message != e.Message {
		t.Fatalf("normalized event severity=%s message=%q", severity, message)
	}
}

func TestBuiltInNotificationV1ModeUsesDurableOutbox(t *testing.T) {
	requests := 0
	var received Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	st, err := storage.Open(t.TempDir() + "/builtin-v1.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "builtin-v1-hook", Name: "V1 legacy", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VPS_COMMANDER_NOTIFICATION_V1_BUILTIN_MODE", "v1")
	m := New(st)
	m.emit(Event{Type: "node_offline", Device: "node-v1", Severity: "critical", Message: "node offline through outbox", At: time.Now().Unix(), Data: map[string]any{"offline_seconds": 65, "alert": 1}})
	if requests != 0 {
		t.Fatalf("v1 mode called legacy sender synchronously %d times", requests)
	}
	jobs, err := st.ClaimNotificationDeliveries(time.Now().Unix()+1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Delivery.Status != "sending" || jobs[0].Channel.LegacyWebhookID != "builtin-v1-hook" {
		t.Fatalf("v1 outbox jobs=%+v", jobs)
	}
	m.Client = srv.Client()
	m.deliverNotificationJob(jobs[0])
	if requests != 1 {
		t.Fatalf("durable outbox send count=%d want 1", requests)
	}
	if received.ID != "" || received.Severity != "critical" || received.Type != "node_offline" || received.Device != "node-v1" || received.Message != "node offline through outbox" {
		t.Fatalf("legacy payload mismatch: %+v", received)
	}
	data, ok := received.Data.(map[string]any)
	if !ok || data["offline_seconds"] != float64(65) || data["alert"] != float64(1) {
		t.Fatalf("legacy event data mismatch: %#v", received.Data)
	}
	job, err := st.GetNotificationDeliveryJob(jobs[0].Delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Delivery.Status != "sent" {
		t.Fatalf("v1 delivery status=%s", job.Delivery.Status)
	}
}

func TestBuiltInTemplateRenderingPreservesLegacyPayload(t *testing.T) {
	var received Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	st, err := storage.Open(t.TempDir() + "/builtin-template-render.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "builtin-render-hook", Name: "Legacy", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	template, err := st.GetNotificationTemplate("builtin-node-offline")
	if err != nil {
		t.Fatal(err)
	}
	template.TitleTemplate = "OPS ALERT"
	template.BodyTemplate = "Alert detail: {{message}}"
	template.VariablesJSON = `["message"]`
	if err := st.SaveNotificationTemplate(template); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VPS_COMMANDER_NOTIFICATION_V1_BUILTIN_MODE", "v1")
	m := New(st)
	m.Client = srv.Client()
	original := Event{Type: "node_offline", Device: "node-render", Severity: "critical", Message: "original offline text", At: time.Now().Unix(), Data: map[string]any{"offline_seconds": 120}}
	m.emit(original)
	jobs, err := st.ClaimNotificationDeliveries(time.Now().Unix()+1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	m.deliverNotificationJob(jobs[0])
	if received.Message != original.Message || received.Severity != original.Severity {
		t.Fatalf("legacy payload changed after template customization: %+v", received)
	}
	var storedMessage string
	if err := st.DB.QueryRow(`SELECT message FROM notification_events WHERE event_type='node_offline'`).Scan(&storedMessage); err != nil {
		t.Fatal(err)
	}
	if storedMessage != "OPS ALERT\nAlert detail: original offline text" {
		t.Fatalf("rendered event message=%q", storedMessage)
	}
}
