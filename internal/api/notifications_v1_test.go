package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func notificationAPIRequest(t *testing.T, h http.Handler, method, path, body string, principalType string, scopes map[string]bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if principalType != "" {
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: principalType, ID: "test-token", TokenID: "test-token", Name: "test-token", Scopes: scopes}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNotificationManagementAPIsProtectSecretsAndExposeTemplates(t *testing.T) {
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", hex.EncodeToString(bytes.Repeat([]byte{0x61}, 32)))
	store, err := storage.Open(filepath.Join(t.TempDir(), "notify-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{Store: store}
	h := server.Routes()

	unauthorized := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/channels", "", "api_token", map[string]bool{"events:emit": true})
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("channels endpoint status=%d, want 403", unauthorized.Code)
	}

	body := `{"id":"tg-ops","name":"Telegram ops","type":"telegram_bot","config_json":{"chat_id":"12345"},"secrets_json":"{\"bot_token\":\"do-not-echo-this-token\"}","enabled":true}`
	created := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/channels", body, "system", nil)
	if created.Code != http.StatusOK {
		t.Fatalf("create channel status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "do-not-echo-this-token") || strings.Contains(created.Body.String(), "secrets_json") || strings.Contains(created.Body.String(), "endpoint_hint") {
		t.Fatalf("channel response leaked secret or endpoint: %s", created.Body.String())
	}
	if !strings.Contains(created.Body.String(), `"credentials_configured":true`) {
		t.Fatalf("response should report configured credentials: %s", created.Body.String())
	}

	listed := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/channels", "", "system", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list channels status=%d body=%s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), "do-not-echo-this-token") || strings.Contains(listed.Body.String(), "config_json") || strings.Contains(listed.Body.String(), "endpoint_hint") {
		t.Fatalf("channel list leaked secret or raw config: %s", listed.Body.String())
	}

	templates := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/templates", "", "system", nil)
	if templates.Code != http.StatusOK || !strings.Contains(templates.Body.String(), "development-completed") || !strings.Contains(templates.Body.String(), "review-completed") {
		t.Fatalf("built-in templates unavailable: status=%d body=%s", templates.Code, templates.Body.String())
	}

	taskBody := `{"id":"tg-dev-task","name":"Development complete to Telegram","channel_id":"tg-ops","template_id":"development-completed","enabled":true,"event_types_json":"[\"development.completed\"]","severities_json":"[\"P3\"]","policy_json":"{}"}`
	task := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/tasks", taskBody, "system", nil)
	if task.Code != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", task.Code, task.Body.String())
	}
	listedTasks := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/tasks", "", "system", nil)
	if listedTasks.Code != http.StatusOK || !strings.Contains(listedTasks.Body.String(), "tg-dev-task") {
		t.Fatalf("list tasks status=%d body=%s", listedTasks.Code, listedTasks.Body.String())
	}

	var ciphertext []byte
	if err := store.DB.QueryRow(`SELECT secrets_ciphertext FROM notification_channels WHERE id='tg-ops'`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("do-not-echo-this-token")) {
		t.Fatal("stored ciphertext unexpectedly contains plaintext bot token")
	}
}

func TestNotificationManagementRejectsSecretsInPublicConfigAndQQProactiveTask(t *testing.T) {
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", hex.EncodeToString(bytes.Repeat([]byte{0x62}, 32)))
	store, err := storage.Open(filepath.Join(t.TempDir(), "notify-api-qq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{Store: store}
	h := server.Routes()
	badConfig := `{"id":"tg-bad","name":"Bad Telegram","type":"telegram_bot","config_json":{"chat_id":"123","bot_token":"must-not-be-public"},"secrets_json":"{\"bot_token\":\"actual-secret\"}","enabled":true}`
	bad := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/channels", badConfig, "system", nil)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("secret in config status=%d body=%s", bad.Code, bad.Body.String())
	}

	qq := `{"id":"qq-ops","name":"QQ official","type":"qq_official_bot","config_json":{"app_id":"app-123","target_type":"group","target_id":"group-openid"},"secrets_json":"{\"app_secret\":\"qq-app-secret\"}","enabled":true}`
	created := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/channels", qq, "system", nil)
	if created.Code != http.StatusOK {
		t.Fatalf("create QQ channel status=%d body=%s", created.Code, created.Body.String())
	}
	if !strings.Contains(created.Body.String(), `"enabled":false`) || !strings.Contains(created.Body.String(), `"last_status":"unsupported"`) {
		t.Fatalf("QQ channel should be explicitly unsupported for proactive delivery: %s", created.Body.String())
	}
	if strings.Contains(created.Body.String(), "qq-app-secret") {
		t.Fatalf("QQ secret leaked: %s", created.Body.String())
	}
	qqTask := `{"id":"qq-task","name":"QQ alert task","channel_id":"qq-ops","template_id":"review-completed","enabled":true,"event_types_json":"[\"review.completed\"]","severities_json":"[\"P3\"]","policy_json":"{}"}`
	rejected := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/tasks", qqTask, "system", nil)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "cannot be enabled") {
		t.Fatalf("QQ proactive task status=%d body=%s", rejected.Code, rejected.Body.String())
	}
}

func TestNotificationChannelViewDoesNotSerializeSecrets(t *testing.T) {
	v := notificationChannelView{ID: "x", Name: "x", Type: "telegram_bot", Enabled: true, CredentialsConfigured: true}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("secrets_json")) || bytes.Contains(b, []byte("bot_token")) {
		t.Fatalf("unexpected secret field in channel view: %s", b)
	}
}
