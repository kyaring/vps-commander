package storage

import (
	"errors"
	"fmt"
	"strings"
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

func TestFailedDeliveryManualRetryPreservesAttemptHistory(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-manual-retry.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "retry-hook", Name: "Retry", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "retry-task", Name: "Retry task", ChannelID: "retry-hook", TemplateID: "review-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	event := NotificationEvent{ID: "evt-manual-retry", TemplateID: "review-completed", RequestHash: "manual-retry", EventType: "review.completed", Severity: "P3", Source: "test", Message: "review failed", DataJSON: "{}", CreatedAt: now}
	if _, matched, _, err := store.AcceptNotificationEvent(event); err != nil || matched != 1 {
		t.Fatalf("accept matched=%d err=%v", matched, err)
	}
	jobs, err := store.ClaimNotificationDeliveries(now+1, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim jobs=%d err=%v", len(jobs), err)
	}
	job := jobs[0]
	if err := store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "failed", 500, "webhook http 500", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryNotificationDelivery(job.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	jobs, err = store.ClaimNotificationDeliveries(now+2, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim after retry jobs=%d err=%v", len(jobs), err)
	}
	if jobs[0].Delivery.AttemptCount != 2 || jobs[0].Delivery.RetryCount != 1 {
		t.Fatalf("retry attempt counts=%+v", jobs[0].Delivery)
	}
	if err := store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "sent", 204, "", 0, now+2); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListNotificationDeliveryAttempts(job.Delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Outcome != "failed" || attempts[1].Outcome != "sent" {
		t.Fatalf("attempt history=%+v", attempts)
	}
	if err := store.RetryNotificationDelivery(job.Delivery.ID); err == nil {
		t.Fatal("sent delivery was allowed to retry")
	}
}

func TestLateWorkerCannotOverwriteNewerDeliveryAttempt(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-late-worker.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "late-hook", Name: "Late", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "late-task", Name: "Late task", ChannelID: "late-hook", TemplateID: "review-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	event := NotificationEvent{ID: "evt-late-worker", TemplateID: "review-completed", RequestHash: "late-worker", EventType: "review.completed", Severity: "P3", Source: "test", Message: "review", DataJSON: "{}", CreatedAt: now}
	if _, matched, _, err := store.AcceptNotificationEvent(event); err != nil || matched != 1 {
		t.Fatalf("accept matched=%d err=%v", matched, err)
	}
	first, err := store.ClaimNotificationDeliveries(now+1, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim jobs=%d err=%v", len(first), err)
	}
	if _, err := store.DB.Exec(`UPDATE notification_deliveries SET updated_at=? WHERE id=?`, now-120, first[0].Delivery.ID); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimNotificationDeliveries(now+2, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("reclaimed jobs=%d err=%v", len(second), err)
	}
	if second[0].Delivery.AttemptCount != 2 {
		t.Fatalf("second attempt number=%d", second[0].Delivery.AttemptCount)
	}
	applied, err := store.CompleteNotificationDeliveryAttempt(first[0].Delivery.ID, first[0].Channel.ID, first[0].Delivery.AttemptCount, "failed", 500, "late failure", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("stale attempt was allowed to overwrite current delivery")
	}
	var status string
	var attemptsCount int
	if err := store.DB.QueryRow(`SELECT status,attempt_count FROM notification_deliveries WHERE id=?`, first[0].Delivery.ID).Scan(&status, &attemptsCount); err != nil {
		t.Fatal(err)
	}
	if status != "sending" || attemptsCount != 2 {
		t.Fatalf("stale attempt changed delivery state: status=%s attempts=%d", status, attemptsCount)
	}
	applied, err = store.CompleteNotificationDeliveryAttempt(second[0].Delivery.ID, second[0].Channel.ID, second[0].Delivery.AttemptCount, "sent", 204, "", 0, now+2)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("current attempt could not complete")
	}
	history, err := store.ListNotificationDeliveryAttempts(first[0].Delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Outcome != "late_failed" || history[1].Outcome != "sent" {
		t.Fatalf("late-worker attempt history=%+v", history)
	}
}

func TestNotificationTaskPolicyRepeatLimitAndQuietWindow(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-policy-runtime.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "policy-hook", Name: "Policy", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "policy-task", Name: "Policy task", ChannelID: "policy-hook", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["development.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: `{"repeat_interval_seconds":300,"max_reminders":1,"quiet_start":"22:00","quiet_end":"07:00","timezone":"UTC"}`}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 11, 22, 30, 0, 0, time.UTC).Unix()
	first := NotificationEvent{ID: "evt-policy-1", TemplateID: "development-completed", RequestHash: "p1", EventType: "development.completed", Severity: "P3", Source: "test", Device: "wjyhk", Message: "first", DataJSON: "{}", CreatedAt: base}
	if _, matched, _, err := store.AcceptNotificationEvent(first); err != nil || matched != 1 {
		t.Fatalf("first event matched=%d err=%v", matched, err)
	}
	var next int64
	var status string
	if err := store.DB.QueryRow(`SELECT status,next_attempt_at FROM notification_deliveries WHERE event_id=?`, first.ID).Scan(&status, &next); err != nil {
		t.Fatal(err)
	}
	wantQuietEnd := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC).Unix()
	if status != "pending" || next != wantQuietEnd {
		t.Fatalf("quiet-window delivery status=%s next=%d want pending/%d", status, next, wantQuietEnd)
	}
	second := first
	second.ID, second.RequestHash, second.Message, second.CreatedAt = "evt-policy-2", "p2", "repeat", base+60
	if _, matched, _, err := store.AcceptNotificationEvent(second); err != nil || matched != 1 {
		t.Fatalf("repeat event matched=%d err=%v", matched, err)
	}
	if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, second.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "suppressed" {
		t.Fatalf("repeat event status=%q, want suppressed", status)
	}
	var count int
	if err := store.DB.QueryRow(`SELECT notification_count FROM notification_task_state WHERE task_id=?`, "policy-task").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("notification_count=%d want 1", count)
	}
}

func TestNotificationTaskMergeWindowCoalescesPendingMessages(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-merge.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "merge-hook", Name: "Merge", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "merge-task", Name: "Merge task", ChannelID: "merge-hook", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["development.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: `{"merge_window_seconds":60}`}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Unix()
	for i, msg := range []string{"first event", "second event", "third event"} {
		event := NotificationEvent{ID: newIDForTest(i), TemplateID: "development-completed", RequestHash: msg, EventType: "development.completed", Severity: "P3", Source: "test", Device: "node-merge", Message: msg, DataJSON: "{}", CreatedAt: base + int64(i*10)}
		if _, matched, _, err := store.AcceptNotificationEvent(event); err != nil || matched != 1 {
			t.Fatalf("event %d matched=%d err=%v", i, matched, err)
		}
	}
	var status, rendered string
	var mergedCount int
	var next int64
	if err := store.DB.QueryRow(`SELECT status,rendered_message,merged_count,next_attempt_at FROM notification_deliveries WHERE event_id='evt-merge-0'`).Scan(&status, &rendered, &mergedCount, &next); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || mergedCount != 3 || !strings.Contains(rendered, "first event") || !strings.Contains(rendered, "second event") || !strings.Contains(rendered, "third event") {
		t.Fatalf("merged delivery status=%s count=%d message=%q", status, mergedCount, rendered)
	}
	if next != base+60 {
		t.Fatalf("merge window next_attempt_at=%d want %d", next, base+60)
	}
	var suppressed int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE status='suppressed' AND last_error LIKE 'merged into delivery %'`).Scan(&suppressed); err != nil {
		t.Fatal(err)
	}
	if suppressed != 2 {
		t.Fatalf("merged source delivery rows=%d want 2", suppressed)
	}
	var immutableMessage string
	if err := store.DB.QueryRow(`SELECT message FROM notification_events WHERE id='evt-merge-0'`).Scan(&immutableMessage); err != nil {
		t.Fatal(err)
	}
	if immutableMessage != "first event" {
		t.Fatalf("source event was mutated: %q", immutableMessage)
	}
	jobs, err := store.ClaimNotificationDeliveries(base+61, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Delivery.MergedCount != 3 || !strings.Contains(jobs[0].Delivery.RenderedMessage, "third event") {
		t.Fatalf("claimed merged job=%+v", jobs)
	}
}

func newIDForTest(i int) string { return fmt.Sprintf("evt-merge-%d", i) }

func TestNotificationRecoveryClosesActiveEpisodeAndHonorsCooldown(t *testing.T) {
	store, err := Open(t.TempDir() + "/notification-recovery.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(NotificationChannel{ID: "recovery-hook", Name: "Recovery", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTemplate(NotificationTemplate{ID: "test-alert-template", Name: "Alert", EventType: "test.alert", Severity: "P2", BodyTemplate: "Alert {{summary}}", Format: "text", VariablesJSON: `["summary"]`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTemplate(NotificationTemplate{ID: "test-recovery-template", Name: "Recovery", EventType: "test.recovered", Severity: "P3", BodyTemplate: "Recovered {{summary}}", Format: "text", VariablesJSON: `["summary"]`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "alert-task", Name: "Alert task", ChannelID: "recovery-hook", TemplateID: "test-alert-template", Enabled: true, EventTypesJSON: `["test.alert"]`, SeveritiesJSON: `["P2"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "recovery-task", Name: "Recovery task", ChannelID: "recovery-hook", TemplateID: "test-recovery-template", Enabled: true, EventTypesJSON: `["test.recovered"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: `{"recovery_event_types":["test.recovered"],"recovery_for_event_types":["test.alert"],"recovery_cooldown_seconds":3600}`}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Unix()
	recovery := NotificationEvent{ID: "evt-recovery-orphan", TemplateID: "test-recovery-template", RequestHash: "orphan", EventType: "test.recovered", Severity: "P3", Source: "test", Device: "node-r", Message: "recovered", DataJSON: `{"summary":"ok"}`, CreatedAt: base}
	if _, matched, _, err := store.AcceptNotificationEvent(recovery); err != nil || matched != 1 {
		t.Fatalf("orphan recovery matched=%d err=%v", matched, err)
	}
	var status string
	if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, recovery.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "suppressed" {
		t.Fatalf("orphan recovery status=%s", status)
	}
	alert := NotificationEvent{ID: "evt-alert", TemplateID: "test-alert-template", RequestHash: "alert", EventType: "test.alert", Severity: "P2", Source: "test", Device: "node-r", Message: "alert", DataJSON: `{"summary":"cpu high"}`, CreatedAt: base + 10}
	if _, matched, _, err := store.AcceptNotificationEvent(alert); err != nil || matched != 1 {
		t.Fatalf("alert matched=%d err=%v", matched, err)
	}
	if _, err := store.DB.Exec(`INSERT INTO notification_task_state(task_id,deduplication_key,active,first_seen_at,last_seen_at,notification_count,last_notified_at,next_due_at,merged_until,resolved_at,state_json) VALUES('alert-task','device:node-r|event:test.other-alert',1,?,?,1,?,0,0,0,'{}')`, base+11, base+11, base+11); err != nil {
		t.Fatal(err)
	}
	recovery.ID, recovery.RequestHash, recovery.CreatedAt = "evt-recovery-valid", "valid", base+20
	if _, matched, _, err := store.AcceptNotificationEvent(recovery); err != nil || matched != 1 {
		t.Fatalf("valid recovery matched=%d err=%v", matched, err)
	}
	if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, recovery.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("valid recovery status=%s want pending", status)
	}
	var active int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_task_state WHERE active=1 AND deduplication_key='device:node-r|event:test.alert'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("matching alert episode after recovery=%d", active)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_task_state WHERE active=1 AND deduplication_key='device:node-r|event:test.other-alert'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("unrelated alert episode should remain active, got %d", active)
	}
	// A second recovery with no new alert must be suppressed by episode state.
	recovery.ID, recovery.RequestHash, recovery.CreatedAt = "evt-recovery-duplicate", "duplicate", base+30
	if _, matched, _, err := store.AcceptNotificationEvent(recovery); err != nil || matched != 1 {
		t.Fatalf("duplicate recovery matched=%d err=%v", matched, err)
	}
	if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, recovery.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "suppressed" {
		t.Fatalf("duplicate recovery status=%s want suppressed", status)
	}
	// A fresh alert episode inside the recovery cooldown is resolved, but its
	// recovery notification is suppressed until the cooldown expires.
	alert.ID, alert.RequestHash, alert.CreatedAt = "evt-alert-again", "alert-again", base+30
	if _, matched, _, err := store.AcceptNotificationEvent(alert); err != nil || matched != 1 {
		t.Fatalf("second alert matched=%d err=%v", matched, err)
	}
	recovery.ID, recovery.RequestHash, recovery.CreatedAt = "evt-recovery-cooldown", "recovery-cooldown", base+40
	if _, matched, _, err := store.AcceptNotificationEvent(recovery); err != nil || matched != 1 {
		t.Fatalf("cooldown recovery matched=%d err=%v", matched, err)
	}
	if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, recovery.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "suppressed" {
		t.Fatalf("recovery inside cooldown status=%s want suppressed", status)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_task_state WHERE active=1 AND deduplication_key='device:node-r|event:test.alert'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("matching alert episode after cooldown recovery=%d", active)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_task_state WHERE active=1 AND deduplication_key='device:node-r|event:test.other-alert'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("unrelated episode after cooldown recovery=%d", active)
	}
}
