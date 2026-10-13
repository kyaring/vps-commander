package storage

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

const legacyRetireFakeURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake-test-key"

func newLegacyRetireStore(t *testing.T) (*Store, string) {
	t.Helper()
	key := hex.EncodeToString(make([]byte, 32))
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", key)
	path := filepath.Join(t.TempDir(), "legacy-retire.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal("open test store")
	}
	if err := store.SaveWebhookTarget(WebhookTarget{ID: "legacy-retire-test", Name: "Legacy test", Type: "wecom", URL: legacyRetireFakeURL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal("seed legacy webhook")
	}
	if _, err := store.DB.Exec(`INSERT INTO webhook_delivery_state(target_id,consecutive_failures,last_error,last_status,updated_at) VALUES('legacy-retire-test',2,'test failure',503,1)`); err != nil {
		t.Fatal("seed legacy delivery state")
	}
	return store, path
}

func TestLegacyRetireMovesTasksAndPreservesDeliveryHistory(t *testing.T) {
	store, path := newLegacyRetireStore(t)
	if _, err := store.DB.Exec(`INSERT INTO notification_tasks(id,name,channel_id,template_id,enabled,event_types_json,severities_json,policy_json,created_at,updated_at) VALUES('ordinary-retire-task','ordinary',?,?,1,'["development.completed"]','["P3"]','{}',1,1)`, legacyChannelID("legacy-retire-test"), "development-completed"); err != nil {
		t.Fatal("seed ordinary task")
	}
	now := time.Now().Unix()
	for _, id := range []string{"retire-pending", "retire-sent"} {
		if _, err := store.DB.Exec(`INSERT INTO notification_events(id,template_id,template_format,request_hash,event_type,severity,source,device,message,data_json,created_at) VALUES(?, 'development-completed','text','', 'development.completed','P3','test','','test','{}',?)`, id, now); err != nil {
			t.Fatal("seed event")
		}
	}
	if _, err := store.DB.Exec(`INSERT INTO notification_deliveries(id,event_id,task_id,channel_id,template_id,status,attempt_count,retry_count,next_attempt_at,last_error,last_http_status,created_at,updated_at,sent_at,rendered_message,merged_count) VALUES('retire-pending-delivery','retire-pending',? ,?,'development-completed','pending',0,0,?, '',0,?,?,0,'',1),('retire-sent-delivery','retire-sent',?,?,'development-completed','sent',1,1,0,'',204,?,?,?, 'sent body',1)`, legacyTaskID("legacy-retire-test"), legacyChannelID("legacy-retire-test"), now+500, now, now, legacyTaskID("legacy-retire-test"), legacyChannelID("legacy-retire-test"), now, now, now); err != nil {
		t.Fatal("seed deliveries")
	}

	got, err := store.RetireLegacyWebhook("legacy-retire-test", "retired-wecom-bot", "Retired destination")
	if err != nil {
		t.Fatalf("retire failed: %v", err)
	}
	if got.AlreadyRetired || got.ChannelID != "retired-wecom-bot" || len(got.MovedTasks) != 1 || got.MovedTasks[0] != "ordinary-retire-task" {
		t.Fatal("unexpected retirement result")
	}
	var typ, endpoint, config string
	var enabled int
	if err := store.DB.QueryRow(`SELECT type,endpoint,config_json,enabled FROM notification_channels WHERE id='retired-wecom-bot'`).Scan(&typ, &endpoint, &config, &enabled); err != nil {
		t.Fatal("read new channel")
	}
	if typ != "wecom_bot" || endpoint != "" || config != `{"mentioned_list":[]}` || enabled != 1 {
		t.Fatal("new channel fields incorrect")
	}
	secretBytes, err := store.GetNotificationChannelSecrets("retired-wecom-bot")
	if err != nil {
		t.Fatal("decrypt new channel secret")
	}
	var secrets map[string]string
	if json.Unmarshal(secretBytes, &secrets) != nil || secrets["webhook_url"] != legacyRetireFakeURL {
		t.Fatal("encrypted secret did not round-trip")
	}
	var channelID string
	if err := store.DB.QueryRow(`SELECT channel_id FROM notification_tasks WHERE id='ordinary-retire-task'`).Scan(&channelID); err != nil || channelID != "retired-wecom-bot" {
		t.Fatal("ordinary task was not moved")
	}
	if err := store.DB.QueryRow(`SELECT enabled FROM notification_tasks WHERE id=?`, legacyTaskID("legacy-retire-test")).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("legacy task was not disabled")
	}
	if err := store.DB.QueryRow(`SELECT enabled,endpoint,config_json FROM notification_channels WHERE id=?`, legacyChannelID("legacy-retire-test")).Scan(&enabled, &endpoint, &config); err != nil || enabled != 0 || endpoint != "" || config != "{}" {
		t.Fatal("legacy channel was not disabled and cleared")
	}
	var pendingStatus, pendingError, sentStatus, sentMessage string
	if err := store.DB.QueryRow(`SELECT status,last_error FROM notification_deliveries WHERE id='retire-pending-delivery'`).Scan(&pendingStatus, &pendingError); err != nil {
		t.Fatal("read pending delivery")
	}
	if err := store.DB.QueryRow(`SELECT status,rendered_message FROM notification_deliveries WHERE id='retire-sent-delivery'`).Scan(&sentStatus, &sentMessage); err != nil {
		t.Fatal("read sent delivery")
	}
	if pendingStatus != "failed" || pendingError != "legacy webhook retired" || sentStatus != "sent" || sentMessage != "sent body" {
		t.Fatal("delivery state transition incorrect")
	}
	for _, table := range []string{"webhook_targets", "webhook_delivery_state"} {
		var count int
		column := "id"
		if table == "webhook_delivery_state" {
			column = "target_id"
		}
		if err := store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?", "legacy-retire-test").Scan(&count); err != nil || count != 0 {
			t.Fatalf("legacy row remains in %s", table)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal("close store")
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal("reopen store")
	}
	defer store.Close()
	if err := store.DB.QueryRow(`SELECT enabled,endpoint FROM notification_channels WHERE id=?`, legacyChannelID("legacy-retire-test")).Scan(&enabled, &endpoint); err != nil || enabled != 0 || endpoint != "" {
		t.Fatal("legacy channel reappeared after reopen")
	}
	if err := store.DB.QueryRow(`SELECT enabled FROM notification_tasks WHERE id=?`, legacyTaskID("legacy-retire-test")).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("legacy task reappeared enabled after reopen")
	}
	got, err = store.RetireLegacyWebhook("legacy-retire-test", "retired-wecom-bot", "Retired destination")
	if err != nil || !got.AlreadyRetired {
		t.Fatal("second retirement was not idempotent")
	}
	var channels int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_channels WHERE id='retired-wecom-bot'`).Scan(&channels); err != nil || channels != 1 {
		t.Fatal("idempotent call duplicated channel")
	}
}

func TestLegacyRetireRejectsInvalidURLAndConflictWithoutMutation(t *testing.T) {
	store, _ := newLegacyRetireStore(t)
	defer store.Close()
	if _, err := store.DB.Exec(`UPDATE webhook_targets SET url='https://example.invalid/not-wecom' WHERE id='legacy-retire-test'`); err != nil {
		t.Fatal("seed invalid URL")
	}
	if _, err := store.RetireLegacyWebhook("legacy-retire-test", "new-channel", "New"); !errors.Is(err, ErrLegacyRetireBadRequest) {
		t.Fatal("invalid URL was not rejected")
	}
	if _, err := store.DB.Exec(`UPDATE webhook_targets SET url=? WHERE id='legacy-retire-test'`, legacyRetireFakeURL); err != nil {
		t.Fatal("restore test URL")
	}
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "occupied-channel", Name: "Occupied", Type: "wecom_bot", ConfigJSON: `{"mentioned_list":[]}`, Enabled: true}, `{"webhook_url":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake-test-key"}`); err != nil {
		t.Fatal("seed occupied channel")
	}
	if _, err := store.RetireLegacyWebhook("legacy-retire-test", "occupied-channel", "New"); !errors.Is(err, ErrLegacyRetireConflict) {
		t.Fatal("channel conflict was not rejected")
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM webhook_targets WHERE id='legacy-retire-test'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("conflict did not roll back target")
	}
	if _, err := store.RetireLegacyWebhook("missing-id", "new-channel", "New"); !errors.Is(err, ErrLegacyRetireNotFound) {
		t.Fatal("missing target was not reported")
	}
}
