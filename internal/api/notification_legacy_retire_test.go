package api

import (
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/storage"
)

const legacyRetireAPIFakeURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake-test-key"

func TestLegacyRetireAdminAndWebhookURLMasking(t *testing.T) {
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", hex.EncodeToString(make([]byte, 32)))
	store, err := storage.Open(filepath.Join(t.TempDir(), "legacy-retire-api.db"))
	if err != nil {
		t.Fatal("open store")
	}
	defer store.Close()
	if err := store.SaveWebhookTarget(storage.WebhookTarget{ID: "api-legacy", Name: "Legacy", Type: "wecom", URL: legacyRetireAPIFakeURL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal("seed target")
	}
	server := &Server{Store: store}
	h := server.Routes()
	body := `{"legacy_webhook_id":"api-legacy","channel_id":"api-wecom-bot","name":"API migrated"}`
	denied := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/legacy/retire", body, "api_token", map[string]bool{"events:emit": true})
	if denied.Code != http.StatusForbidden {
		t.Fatal("non-admin request was not forbidden")
	}
	list := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/webhooks", "", "system", nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "key=") || !strings.Contains(list.Body.String(), "https://qyapi.weixin.qq.com/…") {
		t.Fatal("webhook URL was not masked")
	}
	channels := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/channels", "", "system", nil)
	if channels.Code != http.StatusOK || strings.Contains(channels.Body.String(), "key=") || strings.Contains(channels.Body.String(), legacyRetireAPIFakeURL) {
		t.Fatal("channels response leaked legacy endpoint")
	}
	retired := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/legacy/retire", body, "system", nil)
	if retired.Code != http.StatusOK || strings.Contains(retired.Body.String(), "fake-test-key") || !strings.Contains(retired.Body.String(), `"already_retired":false`) {
		t.Fatal("retire API failed or leaked secret")
	}
	again := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/legacy/retire", body, "system", nil)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"already_retired":true`) {
		t.Fatal("retire API was not idempotent")
	}
	badID := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/legacy/retire", `{"legacy_webhook_id":"x","channel_id":"legacy-bad","name":"x"}`, "system", nil)
	if badID.Code != http.StatusBadRequest || strings.Contains(badID.Body.String(), "fake-test-key") {
		t.Fatal("invalid channel ID was not safely rejected")
	}
	missing := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/legacy/retire", `{"legacy_webhook_id":"missing","channel_id":"new-channel","name":"x"}`, "system", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatal("missing webhook did not return 404")
	}
}

func TestLegacyRetireAPIConflictRollsBack(t *testing.T) {
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", hex.EncodeToString(make([]byte, 32)))
	store, err := storage.Open(filepath.Join(t.TempDir(), "legacy-retire-conflict.db"))
	if err != nil {
		t.Fatal("open store")
	}
	defer store.Close()
	if err := store.SaveWebhookTarget(storage.WebhookTarget{ID: "conflict-legacy", Name: "Legacy", Type: "wecom", URL: legacyRetireAPIFakeURL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal("seed target")
	}
	if _, err := store.DB.Exec(`UPDATE webhook_targets SET url='https://example.invalid/not-wecom' WHERE id='conflict-legacy'`); err != nil {
		t.Fatal("seed invalid URL")
	}
	server := &Server{Store: store}
	invalid := notificationAPIRequest(t, server.Routes(), http.MethodPost, "/api/v1/notifications/legacy/retire", `{"legacy_webhook_id":"conflict-legacy","channel_id":"unused-channel","name":"New"}`, "system", nil)
	if invalid.Code != http.StatusBadRequest || strings.Contains(invalid.Body.String(), "example.invalid") {
		t.Fatal("invalid URL response was not safely rejected")
	}
	if _, err := store.DB.Exec(`UPDATE webhook_targets SET url=? WHERE id='conflict-legacy'`, legacyRetireAPIFakeURL); err != nil {
		t.Fatal("restore fake URL")
	}
	if err := store.SaveNotificationChannel(storage.NotificationChannel{ID: "taken-channel", Name: "Taken", Type: "wecom_bot", ConfigJSON: `{"mentioned_list":[]}`, Enabled: true}, `{"webhook_url":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake-test-key"}`); err != nil {
		t.Fatal("seed conflict channel")
	}
	resp := notificationAPIRequest(t, server.Routes(), http.MethodPost, "/api/v1/notifications/legacy/retire", `{"legacy_webhook_id":"conflict-legacy","channel_id":"taken-channel","name":"New"}`, "system", nil)
	if resp.Code != http.StatusConflict || strings.Contains(resp.Body.String(), "fake-test-key") {
		t.Fatal("channel conflict was not safely rejected")
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM webhook_targets WHERE id='conflict-legacy'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("conflict mutated legacy target")
	}
}
