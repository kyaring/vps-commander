package storage

import (
	"errors"
	"testing"
	"time"
)

func TestAcceptNotificationEventIsAtomicIdempotentAndClaimable(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-outbox.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "outbox-hook", Name: "Outbox webhook", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "outbox-task", Name: "Development events", ChannelID: "outbox-hook", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["development.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	event := NotificationEvent{ID: "evt_first", TemplateID: "development-completed", RequestHash: "hash-same", EventType: "development.completed", Severity: "P3", Source: "api", Device: "wjyhk", Message: "done", DataJSON: `{"task":"task-a","commit":"abc","summary":"done"}`, IdempotencyKey: "job-123", ActorTokenID: "token-a", CreatedAt: now}
	stored, matched, replayed, err := store.AcceptNotificationEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != event.ID || matched != 1 || replayed {
		t.Fatalf("first accept = event:%+v matched:%d replayed:%v", stored, matched, replayed)
	}
	duplicate := event
	duplicate.ID = "evt_duplicate"
	stored, matched, replayed, err = store.AcceptNotificationEvent(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != event.ID || matched != 1 || !replayed {
		t.Fatalf("replay = event:%+v matched:%d replayed:%v", stored, matched, replayed)
	}
	conflict := duplicate
	conflict.RequestHash = "different"
	if _, _, _, err := store.AcceptNotificationEvent(conflict); !errors.Is(err, ErrNotificationIdempotencyConflict) {
		t.Fatalf("idempotency conflict err=%v", err)
	}
	var eventCount, deliveryCount int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || deliveryCount != 1 {
		t.Fatalf("duplicate request created rows: events=%d deliveries=%d", eventCount, deliveryCount)
	}

	jobs, err := store.ClaimNotificationDeliveries(now+1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Delivery.Status != "sending" || jobs[0].Delivery.AttemptCount != 1 || jobs[0].Event.ID != event.ID || jobs[0].Task.ID != "outbox-task" || jobs[0].Channel.ID != "outbox-hook" || jobs[0].Template.ID != "development-completed" {
		t.Fatalf("claimed jobs = %+v", jobs)
	}
	if err := store.CompleteNotificationDelivery(jobs[0].Delivery.ID, jobs[0].Channel.ID, "sent", 204, "", 0, now+1); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetNotificationDeliveryJob(jobs[0].Delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Delivery.Status != "sent" || job.Delivery.SentAt != now+1 || job.Channel.LastStatus != "success" {
		t.Fatalf("completed delivery = %+v channel=%+v", job.Delivery, job.Channel)
	}
}

func TestAcceptNotificationEventRoutesLegacyWebhookTaskAndRejectsMismatch(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-outbox-legacy.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveWebhookTarget(WebhookTarget{ID: "legacy-hook", Name: "Legacy", Type: "generic", URL: "https://example.invalid", Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	event := NotificationEvent{ID: "evt_legacy", TemplateID: "development-completed", RequestHash: "h1", EventType: "development.completed", Severity: "P3", Source: "api", Message: "done", DataJSON: "{}", ActorTokenID: "token", CreatedAt: time.Now().Unix()}
	_, matched, replayed, err := store.AcceptNotificationEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if matched != 1 || replayed {
		t.Fatalf("legacy wildcard task did not match: matched=%d replay=%v", matched, replayed)
	}
	bad := event
	bad.ID = "evt_mismatch"
	bad.TemplateID = "review-completed"
	bad.EventType = "development.completed"
	bad.IdempotencyKey = ""
	if _, _, _, err := store.AcceptNotificationEvent(bad); !errors.Is(err, ErrNotificationTemplateMismatch) {
		t.Fatalf("template mismatch err=%v", err)
	}
}

func TestClaimNotificationDeliveriesRecoversStaleSendingRow(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-stale.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "stale-hook", Name: "Stale", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "stale-task", Name: "Stale task", ChannelID: "stale-hook", TemplateID: "review-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	event := NotificationEvent{ID: "evt_stale", TemplateID: "review-completed", RequestHash: "h", EventType: "review.completed", Severity: "P3", Source: "api", Message: "reviewed", DataJSON: "{}", CreatedAt: now}
	if _, _, _, err := store.AcceptNotificationEvent(event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`UPDATE notification_deliveries SET status='sending',attempt_count=1,updated_at=? WHERE event_id=?`, now-120, event.ID); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimNotificationDeliveries(now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Delivery.Status != "sending" || jobs[0].Delivery.AttemptCount != 2 {
		t.Fatalf("stale delivery wasn't reclaimed: %+v", jobs)
	}
}
