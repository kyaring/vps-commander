package storage

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNotificationMigrationBackfillsLegacyWebhookIdempotently(t *testing.T) {
	path := t.TempDir() + "/legacy-notify.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE webhook_targets (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, url TEXT NOT NULL, config_json TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		CREATE TABLE webhook_delivery_state (target_id TEXT PRIMARY KEY, consecutive_failures INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', last_status INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0);
		INSERT INTO webhook_targets(id,name,type,url,config_json,enabled,created_at,updated_at) VALUES('old-alert','Legacy alert','generic','https://example.invalid/hook','{"headers":{"X-Test":"yes"}}',1,100,101);
		INSERT INTO webhook_delivery_state(target_id,consecutive_failures,last_error,last_status,updated_at) VALUES('old-alert',2,'upstream timeout',503,120);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	channels, err := store.ListNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("channels after migration = %d, want 1", len(channels))
	}
	c := channels[0]
	if c.ID != legacyChannelID("old-alert") || c.LegacyWebhookID != "old-alert" || c.Type != "webhook" || c.LegacyType != "generic" || c.Endpoint != "https://example.invalid/hook" || c.ConfigJSON != `{"headers":{"X-Test":"yes"}}` || !c.Enabled || c.LastStatus != "failed" || c.LastHTTPStatus != 503 || c.ConsecutiveFailures != 2 || c.LastError != "upstream timeout" || c.LastAttemptAt != 120 {
		t.Fatalf("migrated channel = %+v", c)
	}
	tasks, err := store.ListNotificationTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != legacyTaskID("old-alert") || tasks[0].ChannelID != c.ID || tasks[0].TemplateID != legacyEventTemplateID || !tasks[0].Enabled || tasks[0].PolicyJSON != `{"legacy_compat":true}` {
		t.Fatalf("migrated tasks = %+v", tasks)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channels, err = store.ListNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	tasks, err = store.ListNotificationTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || len(tasks) != 1 {
		t.Fatalf("migration duplicated rows on reopen: channels=%d tasks=%d", len(channels), len(tasks))
	}
}

func TestLegacyWebhookWritesShadowChannelAndDeletePreservesHistory(t *testing.T) {
	store, err := Open(t.TempDir() + "/shadow-notify.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	v := WebhookTarget{ID: "hook-a", Name: "Hook A", Type: "generic", URL: "https://example.invalid/one", Config: `{"token":"secret"}`, Enabled: true}
	if err := store.SaveWebhookTarget(v); err != nil {
		t.Fatal(err)
	}
	v.Name = "Hook A updated"
	v.URL = "https://example.invalid/two"
	v.Enabled = false
	if err := store.SaveWebhookTarget(v); err != nil {
		t.Fatal(err)
	}
	channels, err := store.ListNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListNotificationTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].Name != v.Name || channels[0].Endpoint != v.URL || channels[0].Enabled {
		t.Fatalf("shadow channels after update = %+v", channels)
	}
	encoded, err := json.Marshal(channels[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), v.URL) || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "config_json") || strings.Contains(string(encoded), "endpoint") {
		t.Fatalf("channel JSON leaked endpoint or configuration: %s", encoded)
	}
	if len(tasks) != 1 || tasks[0].Enabled {
		t.Fatalf("shadow tasks after update = %+v", tasks)
	}
	if err := store.DeleteWebhookTarget(v.ID); err != nil {
		t.Fatal(err)
	}
	channels, err = store.ListNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	tasks, err = store.ListNotificationTasks()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 0 || len(channels) != 1 || channels[0].Enabled || len(tasks) != 1 || tasks[0].Enabled {
		t.Fatalf("legacy deletion should remove old row but preserve disabled shadow history: old=%+v channels=%+v tasks=%+v", legacy, channels, tasks)
	}
}

func TestLegacyWebhookDeliveryStatusMirrorsToShadowChannel(t *testing.T) {
	store, err := Open(t.TempDir() + "/shadow-delivery.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveWebhookTarget(WebhookTarget{ID: "hook-status", Name: "Hook status", Type: "generic", URL: "https://example.invalid", Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWebhookDelivery("hook-status", 503, "remote failed"); err != nil {
		t.Fatal(err)
	}
	channels, err := store.ListNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].LastStatus != "failed" || channels[0].LastHTTPStatus != 503 || channels[0].ConsecutiveFailures != 1 || channels[0].LastError != "remote failed" || channels[0].LastAttemptAt == 0 {
		t.Fatalf("channel after failed delivery = %+v", channels)
	}
	if err := store.RecordWebhookDelivery("hook-status", 204, ""); err != nil {
		t.Fatal(err)
	}
	channels, err = store.ListNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].LastStatus != "success" || channels[0].LastHTTPStatus != 204 || channels[0].ConsecutiveFailures != 0 || channels[0].LastError != "" || channels[0].LastSuccessAt == 0 {
		t.Fatalf("channel after successful delivery = %+v", channels)
	}
	if err := store.RecordWebhookDelivery("hook-status", 503, ""); err != nil {
		t.Fatal(err)
	}
	channels, err = store.ListNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].LastStatus != "failed" || channels[0].ConsecutiveFailures != 1 || channels[0].LastError == "" {
		t.Fatalf("non-2xx without supplied error should be normalized as failure: %+v", channels)
	}
}

func TestNotificationEventIdempotencyAndSeverityValidation(t *testing.T) {
	store, err := Open(t.TempDir() + "/event-notify.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	v := NotificationEvent{ID: "event-1", EventType: "development.completed", Severity: "P3", Source: "test", Message: "done", IdempotencyKey: "same-key", ActorTokenID: "token-1"}
	if err := store.SaveNotificationEvent(v); err != nil {
		t.Fatal(err)
	}
	got, err := store.NotificationEventByIdempotency("token-1", "same-key")
	if err != nil || got.ID != v.ID || got.Severity != v.Severity {
		t.Fatalf("idempotent event lookup = %+v, err=%v", got, err)
	}
	v.ID = "event-2"
	if err := store.SaveNotificationEvent(v); err == nil {
		t.Fatal("duplicate token-scoped idempotency key was accepted")
	}
	v.ID, v.IdempotencyKey, v.Severity = "event-3", "", "P0"
	if err := store.SaveNotificationEvent(v); err == nil {
		t.Fatal("invalid severity was accepted")
	}
}
