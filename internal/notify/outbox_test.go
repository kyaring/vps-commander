package notify

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestNotificationOutboxDeliversWebhookAndRetriesDurably(t *testing.T) {
	var attempts atomic.Int32
	delivered := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		delivered <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	store, err := storage.Open(t.TempDir() + "/notify-outbox.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(storage.NotificationChannel{ID: "outbox-http", Name: "Outbox HTTP", Type: "webhook", Endpoint: srv.URL, ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(storage.NotificationTask{ID: "outbox-http-task", Name: "Review complete", ChannelID: "outbox-http", TemplateID: "review-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	event := storage.NotificationEvent{ID: "evt-outbox-http", TemplateID: "review-completed", RequestHash: "hash", EventType: "review.completed", Severity: "P3", Source: "test", Device: "wjyhk", Message: "审核完成：PASS", DataJSON: `{"result":"PASS","commit":"abc123","summary":"all tests passed"}`, CreatedAt: time.Now().Unix()}
	_, matched, _, err := store.AcceptNotificationEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if matched != 1 {
		t.Fatalf("matched tasks=%d, want 1", matched)
	}
	m := New(store)
	m.Client = srv.Client()
	m.Start()
	defer m.Stop()
	select {
	case payload := <-delivered:
		if payload["id"] != event.ID || payload["type"] != "review.completed" || payload["severity"] != "P3" || payload["device"] != "wjyhk" {
			t.Fatalf("unexpected delivered payload: %#v", payload)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("notification outbox did not retry and deliver within 6 seconds")
	}
	var status string
	var attemptCount int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := store.DB.QueryRow(`SELECT status,attempt_count FROM notification_deliveries WHERE event_id=?`, event.ID).Scan(&status, &attemptCount); err != nil {
			t.Fatal(err)
		}
		if status == "sent" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "sent" || attemptCount != 2 {
		t.Fatalf("delivery status=%s attempts=%d, want sent after 2 attempts", status, attemptCount)
	}
	var channelStatus string
	if err := store.DB.QueryRow(`SELECT last_status FROM notification_channels WHERE id='outbox-http'`).Scan(&channelStatus); err != nil {
		t.Fatal(err)
	}
	if channelStatus != "success" {
		t.Fatalf("channel status=%s, want success", channelStatus)
	}
}

func TestNotificationOutboxSendsTelegramBotMessageWithoutExposingToken(t *testing.T) {
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", hex.EncodeToString(bytes.Repeat([]byte{0x3a}, 32)))
	var requestBody map[string]any
	var requestPath string
	var requestMu sync.Mutex
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Errorf("decode Telegram request: %v", err)
		}
		requestMu.Lock()
		requestPath = r.URL.Path
		requestBody = decoded
		requestMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer telegram.Close()
	store, err := storage.Open(t.TempDir() + "/notify-telegram.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(storage.NotificationChannel{ID: "telegram-test", Name: "Telegram test", Type: "telegram_bot", ConfigJSON: `{"chat_id":"12345"}`, Enabled: true}, `{"bot_token":"secret-bot-token"}`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(storage.NotificationTask{ID: "telegram-task", Name: "Review to Telegram", ChannelID: "telegram-test", TemplateID: "review-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	event := storage.NotificationEvent{ID: "evt-telegram", TemplateID: "review-completed", RequestHash: "tg-hash", EventType: "review.completed", Severity: "P3", Source: "test", Message: "审核完成：PASS", DataJSON: `{"result":"PASS","commit":"cafe","summary":"ok"}`, CreatedAt: time.Now().Unix()}
	if _, matched, _, err := store.AcceptNotificationEvent(event); err != nil || matched != 1 {
		t.Fatalf("accept event matched=%d err=%v", matched, err)
	}
	// Editing a template after acceptance must not rewrite an already accepted event.
	updatedTemplate, err := store.GetNotificationTemplate("review-completed")
	if err != nil {
		t.Fatal(err)
	}
	updatedTemplate.Format = "markdown"
	updatedTemplate.BodyTemplate = "new body {{result}}"
	if err := store.SaveNotificationTemplate(updatedTemplate); err != nil {
		t.Fatal(err)
	}
	m := New(store)
	m.Client = telegram.Client()
	m.telegramAPIBaseURL = telegram.URL
	m.Start()
	defer m.Stop()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, event.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "sent" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status string
	if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, event.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "sent" {
		t.Fatalf("Telegram delivery status=%s", status)
	}
	requestMu.Lock()
	pathCopy := requestPath
	bodyCopy := requestBody
	requestMu.Unlock()
	if pathCopy != "/botsecret-bot-token/sendMessage" {
		t.Fatalf("unexpected Telegram API path %q", pathCopy)
	}
	if bodyCopy["chat_id"] != "12345" || !strings.Contains(bodyCopy["text"].(string), "审核完成：PASS") || strings.Contains(bodyCopy["text"].(string), "new body") {
		t.Fatalf("unexpected Telegram payload %#v", bodyCopy)
	}
	if _, ok := bodyCopy["parse_mode"]; ok {
		t.Fatalf("template edit changed the format of an accepted event: %#v", bodyCopy)
	}
	encoded, _ := json.Marshal(bodyCopy)
	if strings.Contains(string(encoded), "secret-bot-token") {
		t.Fatalf("Telegram token leaked into message payload: %s", encoded)
	}
}

func TestNotificationWebhookDoesNotForwardSecretsAcrossRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	m := &Manager{Client: redirect.Client()}
	status, failure, retryable := m.postJSON(context.Background(), redirect.URL, map[string]any{"message": "test"}, map[string]string{"Authorization": "Bearer sensitive-value"})
	if status != http.StatusFound || failure == "" || !retryable {
		t.Fatalf("redirect result status=%d failure=%q retryable=%v", status, failure, retryable)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received a request %d times", targetHits.Load())
	}
}
