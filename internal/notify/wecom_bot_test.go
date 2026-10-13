package notify

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

type wecomRoundTripper func(*http.Request) (*http.Response, error)

func (f wecomRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func wecomFixture(t *testing.T, format, mentioned string, response func(*http.Request, int32) (int, string)) (*Manager, storage.NotificationDeliveryJob, *atomic.Int32) {
	t.Helper()
	t.Setenv("VPS_COMMANDER_NOTIFICATION_KEY", hex.EncodeToString(bytes.Repeat([]byte{0x48}, 32)))
	store, err := storage.Open(t.TempDir() + "/wecom-outbox.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const key = "hark-test-wecom-key"
	url := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + key
	config := `{"mentioned_list":["ops"]}`
	if mentioned == "" {
		config = `{}`
	}
	channel := storage.NotificationChannel{ID: "wecom-channel", Name: "WeCom", Type: "wecom_bot", ConfigJSON: config, Enabled: true}
	if err := store.SaveNotificationChannel(channel, `{"webhook_url":"`+url+`"}`); err != nil {
		t.Fatal(err)
	}
	task := storage.NotificationTask{ID: "wecom-task", Name: "WeCom task", ChannelID: channel.ID, TemplateID: "review-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: `{}`}
	if err := store.SaveNotificationTask(task); err != nil {
		t.Fatal(err)
	}
	event := storage.NotificationEvent{ID: "evt-wecom", TemplateID: "review-completed", TemplateFormat: format, RequestHash: "wecom-hash", EventType: "review.completed", Severity: "P3", Source: "test", Message: "Title\nBody", DataJSON: `{"result":"PASS","commit":"abc123","summary":"ok"}`, CreatedAt: time.Now().Unix()}
	if _, matched, _, err := store.AcceptNotificationEvent(event); err != nil || matched != 1 {
		t.Fatalf("accept event matched=%d err=%v", matched, err)
	}
	jobs, err := store.ClaimNotificationDeliveries(time.Now().Unix()+1, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim jobs=%d err=%v", len(jobs), err)
	}
	var attempts atomic.Int32
	m := New(store)
	m.Client = &http.Client{Transport: wecomRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "qyapi.weixin.qq.com" || r.URL.Query().Get("key") != key {
			return nil, fmt.Errorf("unexpected WeCom URL")
		}
		n := attempts.Add(1)
		status, body := response(r, n)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: ioNopCloserString(body), Request: r}, nil
	})}
	return m, jobs[0], &attempts
}

type stringReadCloser struct{ *strings.Reader }

func (s stringReadCloser) Close() error           { return nil }
func ioNopCloserString(s string) stringReadCloser { return stringReadCloser{strings.NewReader(s)} }

func TestWecomTextAndMarkdownPayloads(t *testing.T) {
	for _, tc := range []struct{ name, format, wantType string }{{"text", "text", "text"}, {"markdown", "markdown", "markdown"}} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			m, job, attempts := wecomFixture(t, tc.format, "yes", func(r *http.Request, _ int32) (int, string) {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
				}
				return 200, `{"errcode":0,"errmsg":"ok"}`
			})
			status, failure, _ := m.sendNotificationDelivery(job)
			if status != 200 || failure != "" || attempts.Load() != 1 {
				t.Fatalf("send status=%d failure=%q attempts=%d", status, failure, attempts.Load())
			}
			if payload["msgtype"] != tc.wantType {
				t.Fatalf("msgtype=%v", payload["msgtype"])
			}
			if tc.wantType == "text" {
				textBody, ok := payload["text"].(map[string]any)
				if !ok || textBody["content"] != "Title\nBody" {
					t.Fatalf("text payload=%#v", payload)
				}
				mentions, ok := textBody["mentioned_list"].([]any)
				if !ok || len(mentions) != 1 || mentions[0] != "ops" {
					t.Fatalf("mentioned_list missing: %#v", payload)
				}
			} else {
				markdown, ok := payload["markdown"].(map[string]any)
				if !ok || markdown["content"] != "Title\nBody" {
					t.Fatalf("markdown payload=%#v", payload)
				}
			}
		})
	}
}

func TestWecomErrcodeFailsWithoutRetryOrSecretLeak(t *testing.T) {
	m, job, attempts := wecomFixture(t, "text", "", func(_ *http.Request, _ int32) (int, string) {
		return 200, `{"errcode":93000,"errmsg":"sensitive server details"}`
	})
	m.deliveryCtx = context.Background()
	m.deliverNotificationJob(job)
	if attempts.Load() != 1 {
		t.Fatalf("attempts=%d, want 1 (no retry)", attempts.Load())
	}
	var status, lastError string
	var attemptCount int
	if err := m.Store.DB.QueryRow(`SELECT status,last_error,attempt_count FROM notification_deliveries WHERE event_id=?`, job.Event.ID).Scan(&status, &lastError, &attemptCount); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attemptCount != 1 || lastError != "wecom errcode=93000" {
		t.Fatalf("delivery status=%s attempts=%d last_error=%q", status, attemptCount, lastError)
	}
	if strings.Contains(lastError, "hark-test-wecom-key") || strings.Contains(lastError, "https://qyapi.weixin.qq.com") || strings.Contains(lastError, "sensitive server details") {
		t.Fatalf("secret or provider errmsg leaked in last_error: %q", lastError)
	}
	attemptsLog, err := m.Store.ListNotificationDeliveryAttempts(job.Delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptsLog) != 1 || strings.Contains(attemptsLog[0].Error, "hark-test-wecom-key") || strings.Contains(attemptsLog[0].Error, "https://qyapi.weixin.qq.com") {
		t.Fatalf("secret leaked in delivery attempts: %+v", attemptsLog)
	}
	var raw string
	if err := m.Store.DB.QueryRow(`SELECT last_error FROM notification_channels WHERE id=?`, job.Channel.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "hark-test-wecom-key") || strings.Contains(raw, "https://qyapi.weixin.qq.com") {
		t.Fatalf("secret leaked in channel status: %q", raw)
	}
}

func TestWecomHTTP5xxRetriesAndThenSucceeds(t *testing.T) {
	m, job, attempts := wecomFixture(t, "text", "", func(_ *http.Request, n int32) (int, string) {
		if n == 1 {
			return 503, `{"errcode":-1}`
		}
		return 200, `{"errcode":0}`
	})
	m.deliveryCtx = context.Background()
	m.deliverNotificationJob(job)
	if attempts.Load() != 1 {
		t.Fatalf("first attempts=%d", attempts.Load())
	}
	var status string
	if err := m.Store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, job.Event.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("after 503 status=%q, want pending", status)
	}
	jobs, err := m.Store.ClaimNotificationDeliveries(time.Now().Unix()+5, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("retry claim jobs=%d err=%v", len(jobs), err)
	}
	m.deliverNotificationJob(jobs[0])
	if attempts.Load() != 2 {
		t.Fatalf("attempts=%d, want retry", attempts.Load())
	}
	if err := m.Store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE event_id=?`, job.Event.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "sent" {
		t.Fatalf("after retry status=%q, want sent", status)
	}
	attemptsLog, err := m.Store.ListNotificationDeliveryAttempts(job.Delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptsLog) != 2 {
		t.Fatalf("attempt history=%+v", attemptsLog)
	}
	for _, a := range attemptsLog {
		if strings.Contains(a.Error, "hark-test-wecom-key") || strings.Contains(a.Error, "https://qyapi.weixin.qq.com") {
			t.Fatalf("secret leaked in attempt history: %+v", a)
		}
	}
}
