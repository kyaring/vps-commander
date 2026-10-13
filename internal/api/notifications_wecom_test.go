package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/notify"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestWecomChannelViewDoesNotExposeWebhookURLOrKey(t *testing.T) {
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", strings.Repeat("4a", 32))
	store, err := storage.Open(filepath.Join(t.TempDir(), "wecom-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{Store: store}
	h := server.Routes()
	const key = "hark-test-wecom-key"
	url := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + key
	body := `{"id":"wecom-api","name":"WeCom API","type":"wecom_bot","config_json":{"mentioned_list":["ops"]},"secrets_json":"{\"webhook_url\":\"` + url + `\"}","enabled":true}`
	created := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/channels", body, "system", nil)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), key) || strings.Contains(created.Body.String(), url) {
		t.Fatalf("create response leaked WeCom URL/key: %s", created.Body.String())
	}
	if !strings.Contains(created.Body.String(), `"credentials_configured":true`) || !strings.Contains(created.Body.String(), `"mentioned_list":["ops"]`) {
		t.Fatalf("safe channel fields missing: %s", created.Body.String())
	}
	listed := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/channels", "", "system", nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), key) || strings.Contains(listed.Body.String(), url) {
		t.Fatalf("list status=%d leaked=%v body=%s", listed.Code, strings.Contains(listed.Body.String(), key) || strings.Contains(listed.Body.String(), url), listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), `"webhook_url"`) {
		t.Fatalf("list returned secret field: %s", listed.Body.String())
	}
}

func TestLegacyWebhookGoneAndListRemainsAvailable(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "legacy-gone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveWebhookTarget(storage.WebhookTarget{ID: "legacy-wecom", Name: "Legacy WeCom", Type: "wecom", URL: "https://example.invalid/legacy", Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store}
	h := server.Routes()
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/api/v1/notifications/webhook/update"}, {http.MethodPost, "/api/v1/notifications/webhook/update"}, {http.MethodPost, "/api/v1/notifications/webhook/delete?id=legacy-wecom"}, {http.MethodDelete, "/api/v1/notifications/webhook/delete?id=legacy-wecom"}} {
		rec := notificationAPIRequest(t, h, tc.method, tc.path, `{"id":"legacy-wecom","name":"tampered"}`, "system", nil)
		if rec.Code != http.StatusGone {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "legacy webhook API removed; use /api/v1/notifications/channels" {
			t.Fatalf("gone response=%s err=%v", rec.Body.String(), err)
		}
	}
	items, err := store.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "legacy-wecom" || items[0].Name != "Legacy WeCom" {
		t.Fatalf("legacy target changed: %+v", items)
	}
	listed := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/webhooks", "", "system", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "legacy-wecom") {
		t.Fatalf("legacy list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var audited int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='notification_setting_legacy_gone'`).Scan(&audited); err != nil {
			t.Fatal(err)
		}
		if audited == 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if audited != 4 {
		t.Fatalf("legacy gone audit count=%d, want 4", audited)
	}
}

func TestDefaultTemplateEmitsP3WithoutExplicitSeverity(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "default-event.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{Store: store, Notify: notify.New(store)}
	h := server.Routes()
	body := `{"template_id":"builtin-default","variables":{"title":"Test title","message":"Test body"}}`
	rec := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/events", body, "api_token", map[string]bool{"events:emit": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("emit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["ok"] != true || response["event_type"] != "custom.default" || response["severity"] != "P3" {
		t.Fatalf("unexpected emit response: %#v", response)
	}
	var severity, eventType, message string
	if err := store.DB.QueryRow(`SELECT severity,event_type,message FROM notification_events WHERE template_id='builtin-default'`).Scan(&severity, &eventType, &message); err != nil {
		t.Fatal(err)
	}
	if severity != "P3" || eventType != "custom.default" || message != "Test title\nTest body" {
		t.Fatalf("stored event severity=%s type=%s message=%q", severity, eventType, message)
	}
}
