package storage

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"testing"
)

func TestNotificationChannelSecretCiphertextCannotMoveBetweenChannels(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{0x33}, 32)))
	store, err := Open(t.TempDir() + "/channel-aad.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"tg-one", "tg-two"} {
		v := NotificationChannel{ID: id, Name: id, Type: "telegram_bot", ConfigJSON: `{"chat_id":"12345"}`, Enabled: true}
		if err := store.SaveNotificationChannel(v, `{"bot_token":"token-`+id+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	var first []byte
	if err := store.DB.QueryRow(`SELECT secrets_ciphertext FROM notification_channels WHERE id='tg-one'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`UPDATE notification_channels SET secrets_ciphertext=? WHERE id='tg-two'`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNotificationChannelSecrets("tg-two"); err == nil {
		t.Fatal("ciphertext copied from another channel decrypted successfully")
	}
}

func TestNotificationTaskDisableFollowsChannelAndTemplate(t *testing.T) {
	store, err := Open(t.TempDir() + "/task-disable.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "hook-new", Name: "New webhook", Type: "webhook", Endpoint: "https://example.invalid/hook", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "task-dev", Name: "Development notifications", ChannelID: "hook-new", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["development.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableNotificationChannel("hook-new"); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListNotificationTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Enabled {
		t.Fatalf("channel disable should disable dependent task: %+v", tasks)
	}
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "hook-new", Name: "New webhook", Type: "webhook", Endpoint: "https://example.invalid/hook", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "task-dev", Name: "Development notifications", ChannelID: "hook-new", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["development.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableNotificationTemplate("development-completed"); err != nil {
		t.Fatal(err)
	}
	tasks, err = store.ListNotificationTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Enabled {
		t.Fatalf("template disable should disable dependent task: %+v", tasks)
	}
}

func TestSaveNotificationChannelRejectsMalformedProviderSecrets(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{0x41}, 32)))
	store, err := Open(t.TempDir() + "/bad-secrets.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	v := NotificationChannel{ID: "tg-bad", Name: "Bad Telegram", Type: "telegram_bot", ConfigJSON: `{"chat_id":"123"}`, Enabled: true}
	if err := store.SaveNotificationChannel(v, `{"other":"value"}`); err == nil {
		t.Fatal("Telegram channel accepted secrets without bot_token")
	}
	v = NotificationChannel{ID: "qq-bad", Name: "Bad QQ", Type: "qq_official_bot", ConfigJSON: `{"app_id":"123","target_type":"group","target_id":"x"}`, Enabled: false}
	if err := store.SaveNotificationChannel(v, `{"other":"value"}`); err == nil {
		t.Fatal("QQ channel accepted secrets without app_secret")
	}
}

func TestNotificationTemplateRejectsUndeclaredPlaceholderAndTaskFilterMismatch(t *testing.T) {
	store, err := Open(t.TempDir() + "/template-validation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationTemplate(NotificationTemplate{ID: "bad-template", Name: "Bad", EventType: "custom.event", Severity: "P3", BodyTemplate: "hello {{not_declared}}", Format: "text", VariablesJSON: `["declared"]`, Enabled: true}); err == nil {
		t.Fatal("undeclared template variable was accepted")
	}
	if err := store.SaveNotificationTemplate(NotificationTemplate{ID: "valid-template", Name: "Valid", EventType: "custom.valid", Severity: "P3", BodyTemplate: "hello {{task}}", Format: "text", VariablesJSON: `["task"]`, Enabled: true}); err != nil {
		t.Fatalf("declared placeholder was rejected: %v", err)
	}
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "template-hook", Name: "Template webhook", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "mismatch-task", Name: "Mismatch", ChannelID: "template-hook", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err == nil {
		t.Fatal("task with mismatched event type and template was accepted")
	}
}

func TestNotificationMigrationAddsSecretsColumnToExistingSchema(t *testing.T) {
	path := t.TempDir() + "/notification-old-schema.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE notification_channels (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL,
		endpoint TEXT NOT NULL DEFAULT '', config_json TEXT NOT NULL DEFAULT '{}',
		enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		last_attempt_at INTEGER NOT NULL DEFAULT 0, last_success_at INTEGER NOT NULL DEFAULT 0,
		last_status TEXT NOT NULL DEFAULT 'unknown', last_http_status INTEGER NOT NULL DEFAULT 0,
		consecutive_failures INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
		legacy_webhook_id TEXT UNIQUE, legacy_type TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE notification_deliveries (
		id TEXT PRIMARY KEY, event_id TEXT NOT NULL, task_id TEXT NOT NULL, channel_id TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending', attempt_count INTEGER NOT NULL DEFAULT 0,
		next_attempt_at INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
		last_http_status INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		sent_at INTEGER NOT NULL DEFAULT 0, UNIQUE(event_id,task_id)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('notification_channels') WHERE name='secrets_ciphertext'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("migration did not add secrets_ciphertext to existing channel table")
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('notification_deliveries') WHERE name='template_id'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("migration did not add template_id to existing notification deliveries")
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('notification_deliveries') WHERE name='retry_count'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("migration did not add retry_count to existing notification deliveries")
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='notification_delivery_attempts'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("migration did not create notification_delivery_attempts")
	}
}

func TestWebhookChannelBlankEndpointUpdatePreservesStoredURL(t *testing.T) {
	store, err := Open(t.TempDir() + "/endpoint-preserve.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channel := NotificationChannel{ID: "hook-preserve", Name: "Preserve URL", Type: "webhook", Endpoint: "https://example.invalid/secret-path", ConfigJSON: "{}", Enabled: true}
	if err := store.SaveNotificationChannel(channel, ""); err != nil {
		t.Fatal(err)
	}
	channel.Name = "Preserve URL renamed"
	channel.Endpoint = ""
	if err := store.SaveNotificationChannel(channel, ""); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetNotificationChannel(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "https://example.invalid/secret-path" || got.Name != channel.Name {
		t.Fatalf("blank endpoint update did not preserve URL: %+v", got)
	}
}

func TestWebhookSecretHeadersAreValidatedBeforeEncryption(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{0x51}, 32)))
	store, err := Open(t.TempDir() + "/webhook-headers.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	v := NotificationChannel{ID: "hook-headers", Name: "Headers", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}
	if err := store.SaveNotificationChannel(v, `{"headers":{"Authorization":"Bearer hidden","X-Request-ID":"123"}}`); err != nil {
		t.Fatalf("valid secret headers rejected: %v", err)
	}
	v.ID = "hook-host-header"
	if err := store.SaveNotificationChannel(v, `{"headers":{"Host":"attacker.invalid"}}`); err == nil {
		t.Fatal("reserved Host header was accepted")
	}
	v.ID = "hook-extra-secret"
	if err := store.SaveNotificationChannel(v, `{"unknown_secret":"hidden"}`); err == nil {
		t.Fatal("unknown webhook secret field was accepted")
	}
}
