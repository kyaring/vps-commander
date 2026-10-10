package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

type Target struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	URL     string `json:"url"`
	Config  string `json:"config_json"`
	Enabled bool   `json:"enabled"`
}
type Event struct {
	Type     string `json:"type"`
	Device   string `json:"device,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	At       int64  `json:"timestamp"`
	Data     any    `json:"data,omitempty"`
}
type Manager struct {
	Store        *storage.Store
	Client       *http.Client
	stop         chan struct{}
	wg           sync.WaitGroup
	mu           sync.Mutex
	offline      map[string]*offlineState
	clock        func() time.Time
	sent         map[string]time.Time
	strikeCount  map[string]int
	lastSchedule string
}

func New(s *storage.Store) *Manager {
	m := &Manager{Store: s, Client: &http.Client{Timeout: 8 * time.Second}, stop: make(chan struct{}), offline: map[string]*offlineState{}, clock: time.Now, sent: map[string]time.Time{}, strikeCount: map[string]int{}}
	m.importOpenClawWebhook()
	return m
}

func (m *Manager) importOpenClawWebhook() {
	if xs, err := m.Store.ListWebhookTargets(); err == nil && len(xs) > 0 {
		return
	}
	b, err := os.ReadFile("/root/.openclaw/workspace/check_exchange_rate.py")
	if err != nil {
		return
	}
	re := regexp.MustCompile(`WEBHOOK_URL\s*=\s*[\"']([^\"']+)[\"']`)
	match := re.FindSubmatch(b)
	if len(match) != 2 || len(match[1]) == 0 {
		return
	}
	_ = m.Store.SaveWebhookTarget(storage.WebhookTarget{ID: "openclaw-wecom", Name: "OpenClaw 企业微信", Type: "wecom", URL: string(match[1]), Config: "{}", Enabled: true})
}
func (m *Manager) Start() { m.wg.Add(1); go m.loop() }
func (m *Manager) Stop()  { close(m.stop); m.wg.Wait() }
func (m *Manager) loop() {
	defer m.wg.Done()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	m.check()
	for {
		select {
		case <-t.C:
			m.check()
		case <-m.stop:
			return
		}
	}
}
func (m *Manager) check() {
	devices, _ := m.Store.ListDevices()
	now := m.now().Unix()
	for _, d := range devices {
		if d.IsLocal {
			continue
		}
		if d.Status == "offline" && d.OfflineAt != nil {
			m.handleOffline(d.Name, *d.OfflineAt, now)
		} else if d.Status == "online" {
			m.mu.Lock()
			st := m.offline[d.Name]
			delete(m.offline, d.Name)
			m.mu.Unlock()
			if st != nil && st.notified {
				m.emit(Event{Type: "node_recovery", Device: d.Name, Severity: "info", Message: fmt.Sprintf("节点 %s 已恢复在线（本次离线约 %s）", d.Name, humanDuration(now-st.since)), At: now, Data: map[string]any{"offline_since": st.since, "offline_seconds": now - st.since}})
			}
		}
	}
	m.checkSchedule(devices, now)
	for _, d := range devices {
		if d.ProfileJSON == "" {
			continue
		}
		var p map[string]any
		if json.Unmarshal([]byte(d.ProfileJSON), &p) != nil {
			continue
		}
		mem, _ := p["mem_percent"].(float64)
		disk, _ := p["disk_percent"].(float64)
		cpu, _ := p["cpu_usage_percent"].(float64)
		// 防抖逻辑：资源利用率需连续 3 次轮询（约 45 秒）超过阈值才触发告警
		checkStrike := func(metric string, val float64, threshold float64, msg string) {
			key := fmt.Sprintf("%s:%s", metric, d.Name)
			m.mu.Lock()
			if val >= threshold {
				m.strikeCount[key]++
			} else {
				m.strikeCount[key] = 0
			}
			count := m.strikeCount[key]
			m.mu.Unlock()

			if count >= 3 {
				m.emitOnce(key, Event{
					Type:     "resource_threshold",
					Device:   d.Name,
					Severity: "warning",
					Message:  msg,
					At:       now,
					Data:     map[string]any{"metric": metric, "value": val, "threshold": threshold, "strikes": count},
				})
			}
		}

		checkStrike("mem", mem, 90, fmt.Sprintf("节点 %s 连续 45s 内存使用率超限: %.0f%%", d.Name, mem))
		checkStrike("disk", disk, 90, fmt.Sprintf("节点 %s 磁盘使用率 %.0f%%", d.Name, disk))
		checkStrike("cpu", cpu, 95, fmt.Sprintf("节点 %s 连续 45s CPU 使用率超限: %.0f%%", d.Name, cpu))
	}
}

// offlineAlertSteps are the offline durations (seconds) at which a node_offline
// alert is sent for one offline episode: right after the disconnect, then two
// reminders. After the last one the node is only listed in the daily report.
var offlineAlertSteps = []int64{60, 10 * 60, 60 * 60}

// offlineAlertLateStart: an offline episode first observed after this long
// (e.g. right after a Hub restart) sends no alerts; the daily report covers it.
const offlineAlertLateStart int64 = 2 * 60 * 60

type offlineState struct {
	since    int64
	sent     int
	notified bool
}

func (m *Manager) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func (m *Manager) handleOffline(name string, since, now int64) {
	elapsed := now - since
	stage := 0
	for _, step := range offlineAlertSteps {
		if elapsed >= step {
			stage++
		}
	}
	m.mu.Lock()
	st := m.offline[name]
	if st == nil || st.since != since {
		st = &offlineState{since: since}
		m.offline[name] = st
		if elapsed >= offlineAlertLateStart {
			st.sent = len(offlineAlertSteps)
		}
	}
	if stage <= st.sent {
		m.mu.Unlock()
		return
	}
	st.sent = stage
	st.notified = true
	final := stage == len(offlineAlertSteps)
	m.mu.Unlock()
	msg := fmt.Sprintf("节点 %s 已离线 %s（自 %s）", name, humanDuration(elapsed), time.Unix(since, 0).Format("01-02 15:04"))
	if final {
		msg += "，后续不再重复提醒，离线状态会列在每日报告中"
	}
	m.emit(Event{Type: "node_offline", Device: name, Severity: "critical", Message: msg, At: now, Data: map[string]any{"offline_since": since, "offline_seconds": elapsed, "alert": stage, "final": final}})
}

func humanDuration(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	min := sec / 60
	if min < 60 {
		if min < 1 {
			min = 1
		}
		return fmt.Sprintf("%d 分钟", min)
	}
	h := min / 60
	min %= 60
	if h < 48 {
		if min == 0 {
			return fmt.Sprintf("%d 小时", h)
		}
		return fmt.Sprintf("%d 小时 %d 分钟", h, min)
	}
	d := h / 24
	h %= 24
	if h == 0 {
		return fmt.Sprintf("%d 天", d)
	}
	return fmt.Sprintf("%d 天 %d 小时", d, h)
}

func (m *Manager) emitOnce(key string, e Event) {
	m.mu.Lock()
	last := m.sent[key]
	if !last.IsZero() && time.Since(last) < 30*time.Minute {
		m.mu.Unlock()
		return
	}
	m.sent[key] = time.Now()
	m.mu.Unlock()
	m.emit(e)
}
func (m *Manager) emit(e Event) {
	targets, _ := m.Store.ListWebhookTargets()
	payload, _ := json.Marshal(e)
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		body := payload
		if t.Type == "telegram" {
			var c struct {
				ChatID string `json:"chat_id"`
			}
			_ = json.Unmarshal([]byte(t.Config), &c)
			body, _ = json.Marshal(map[string]any{"chat_id": c.ChatID, "text": e.Message})
		}
		if t.Type == "wecom" {
			body, _ = json.Marshal(map[string]any{"msgtype": "text", "text": map[string]any{"content": e.Message}})
		}
		req, _ := http.NewRequest(http.MethodPost, t.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		req = req.WithContext(ctx)
		resp, err := m.Client.Do(req)
		if err != nil {
			_ = m.Store.Audit("127.0.0.1", e.Device, "notification", "webhook:"+t.ID, 1, 0, err.Error())
		} else {
			resp.Body.Close()
			code := resp.StatusCode
			_ = m.Store.Audit("127.0.0.1", e.Device, "notification", "webhook:"+t.ID, func() int {
				if code >= 200 && code < 300 {
					return 0
				}
				return code
			}(), 0, "")
		}
		cancel()
	}
}
func (m *Manager) TestTarget(t Target) error {
	e := Event{Type: "test", Severity: "info", Message: "VPS-Commander Phase 3 Webhook 测试消息", At: time.Now().Unix()}
	b, _ := json.Marshal(e)
	req, _ := http.NewRequest(http.MethodPost, t.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook http %d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) checkSchedule(devices []storage.DeviceRecord, now int64) {
	t := m.now()
	key := t.Format("2006-01-02") + ":" + t.Format("15:04")
	minute := t.Hour()*60 + t.Minute()
	if minute != 510 && minute != 1320 {
		return
	}
	m.mu.Lock()
	if m.lastSchedule == key {
		m.mu.Unlock()
		return
	}
	m.lastSchedule = key
	m.mu.Unlock()
	online, offline := 0, 0
	offlineLines := []string{}
	offlineNames := []string{}
	for _, d := range devices {
		if d.IsLocal {
			continue
		}
		if d.Status == "online" {
			online++
		} else {
			offline++
			offlineNames = append(offlineNames, d.Name)
			if d.OfflineAt != nil {
				offlineLines = append(offlineLines, fmt.Sprintf("%s（自 %s，已 %s）", d.Name, time.Unix(*d.OfflineAt, 0).Format("01-02 15:04"), humanDuration(now-*d.OfflineAt)))
			} else {
				offlineLines = append(offlineLines, d.Name)
			}
		}
	}
	msg := fmt.Sprintf("VPS-Commander %s 报告：远程节点在线 %d，离线 %d", map[bool]string{true: "早间", false: "晚间"}[minute == 510], online, offline)
	if len(offlineLines) > 0 {
		msg += "\n离线节点：" + strings.Join(offlineLines, "、")
	}
	m.emit(Event{Type: "scheduled_report", Severity: "info", Message: msg, At: now, Data: map[string]any{"report": "daily", "period": map[bool]string{true: "morning", false: "evening"}[minute == 510], "online": online, "offline": offline, "offline_devices": offlineNames}})
}
