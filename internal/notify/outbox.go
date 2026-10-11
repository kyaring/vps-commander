package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

const notificationOutboxPollInterval = time.Second
const notificationDeliveryMaxAttempts = 4

var notificationHeaderNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func (m *Manager) notificationOutboxLoop() {
	defer m.deliveryWG.Done()
	ticker := time.NewTicker(notificationOutboxPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.deliveryStop:
			return
		default:
		}
		if m.Store != nil {
			jobs, err := m.Store.ClaimNotificationDeliveries(m.clock().Unix(), 1)
			if err != nil {
				log.Printf("claim notification delivery: %v", err)
			} else if len(jobs) > 0 {
				m.deliverNotificationJob(jobs[0])
				continue
			}
		}
		select {
		case <-m.deliveryStop:
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) deliverNotificationJob(job storage.NotificationDeliveryJob) {
	if !job.Channel.Enabled || !job.Task.Enabled || !job.Template.Enabled {
		_ = m.Store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "suppressed", 0, "channel, task or template disabled", 0, 0)
		return
	}
	if job.Channel.Type == "qq_official_bot" {
		_ = m.Store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "unsupported", 0, "QQ official bot proactive push is not supported by the platform", 0, 0)
		return
	}
	status, failure, retryable := m.sendNotificationDelivery(job)
	now := m.clock().Unix()
	if job.Channel.LegacyWebhookID != "" {
		// Keep legacy delivery history in sync for the existing webhook screen.
		if err := m.Store.RecordWebhookDelivery(job.Channel.LegacyWebhookID, status, failure); err != nil {
			log.Printf("mirror legacy webhook delivery %s: %v", job.Channel.LegacyWebhookID, err)
		}
	}
	if failure == "" && status >= 200 && status < 300 {
		if err := m.Store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "sent", status, "", 0, now); err != nil {
			log.Printf("complete notification delivery %s: %v", job.Delivery.ID, err)
		}
		return
	}
	if m.deliveryCtx != nil && m.deliveryCtx.Err() != nil {
		// A shutdown cancellation is not a final delivery failure. Leave a durable
		// pending row so the next Hub process can retry it.
		if err := m.Store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "pending", status, failure, now+1, 0); err != nil {
			log.Printf("preserve interrupted delivery %s: %v", job.Delivery.ID, err)
		}
		return
	}
	if retryable && job.Delivery.AttemptCount < notificationDeliveryMaxAttempts && m.deliveryCtx != nil && m.deliveryCtx.Err() == nil {
		delays := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
		delay := delays[len(delays)-1]
		if job.Delivery.AttemptCount > 0 && job.Delivery.AttemptCount <= len(delays) {
			delay = delays[job.Delivery.AttemptCount-1]
		}
		if err := m.Store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "pending", status, failure, now+int64(delay.Seconds()), 0); err != nil {
			log.Printf("schedule notification retry %s: %v", job.Delivery.ID, err)
		}
		return
	}
	if err := m.Store.CompleteNotificationDelivery(job.Delivery.ID, job.Channel.ID, "failed", status, failure, 0, 0); err != nil {
		log.Printf("fail notification delivery %s: %v", job.Delivery.ID, err)
	}
}

func (m *Manager) sendNotificationDelivery(job storage.NotificationDeliveryJob) (int, string, bool) {
	ctx := m.deliveryCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return 0, "delivery canceled during shutdown", true
	}
	variables := map[string]string{}
	if err := json.Unmarshal([]byte(job.Event.DataJSON), &variables); err != nil {
		var values map[string]any
		if decodeErr := json.Unmarshal([]byte(job.Event.DataJSON), &values); decodeErr != nil {
			return 0, "event variables are invalid", false
		}
		for key, value := range values {
			variables[key] = fmt.Sprint(value)
		}
	}
	message := job.Event.Message // snapshot rendered at acceptance time; retries must not change if a template is edited
	format := job.Event.TemplateFormat
	if format == "" {
		format = job.Template.Format
	}
	payload := map[string]any{"id": job.Event.ID, "type": job.Event.EventType, "severity": job.Event.Severity, "source": job.Event.Source, "device": job.Event.Device, "message": message, "at": job.Event.CreatedAt, "data": variables}
	if job.Channel.LegacyWebhookID != "" {
		var legacyData map[string]any
		_ = json.Unmarshal([]byte(job.Event.DataJSON), &legacyData)
		legacyEvent := Event{ID: job.Event.ID, Type: job.Event.EventType, Device: job.Event.Device, Severity: job.Event.Severity, Message: job.Event.Message, At: job.Event.CreatedAt, Data: legacyData}
		if job.Channel.LegacyType == "telegram" {
			var cfg struct {
				ChatID string `json:"chat_id"`
			}
			_ = json.Unmarshal([]byte(job.Channel.ConfigJSON), &cfg)
			return m.postJSON(ctx, job.Channel.Endpoint, map[string]any{"chat_id": cfg.ChatID, "text": job.Event.Message}, nil)
		}
		if job.Channel.LegacyType == "wecom" {
			return m.postJSON(ctx, job.Channel.Endpoint, map[string]any{"msgtype": "text", "text": map[string]any{"content": job.Event.Message}}, nil)
		}
		return m.postJSON(ctx, job.Channel.Endpoint, legacyEvent, nil)
	}
	switch job.Channel.Type {
	case "webhook":
		var secrets struct {
			Headers map[string]string `json:"headers"`
		}
		if len(job.Channel.SecretsCiphertext) > 0 {
			plain, err := m.Store.DecryptNotificationChannelSecrets(job.Channel.ID, job.Channel.SecretsCiphertext)
			if err != nil {
				return 0, "webhook credentials could not be decrypted", false
			}
			if err := json.Unmarshal(plain, &secrets); err != nil {
				return 0, "webhook secret configuration is invalid", false
			}
		}
		if format == "raw_json" {
			return m.postJSON(ctx, job.Channel.Endpoint, Event{ID: job.Event.ID, Type: job.Event.EventType, Device: job.Event.Device, Severity: job.Event.Severity, Message: job.Event.Message, At: job.Event.CreatedAt, Data: mapStringAny(variables)}, secrets.Headers)
		}
		return m.postJSON(ctx, job.Channel.Endpoint, payload, secrets.Headers)
	case "telegram_bot":
		plain, err := m.Store.DecryptNotificationChannelSecrets(job.Channel.ID, job.Channel.SecretsCiphertext)
		if err != nil {
			return 0, "Telegram credentials could not be decrypted", false
		}
		var secrets struct {
			BotToken string `json:"bot_token"`
		}
		if err := json.Unmarshal(plain, &secrets); err != nil || strings.TrimSpace(secrets.BotToken) == "" {
			return 0, "Telegram bot token is missing", false
		}
		var cfg struct {
			ChatID          string `json:"chat_id"`
			MessageThreadID int64  `json:"message_thread_id"`
		}
		if err := json.Unmarshal([]byte(job.Channel.ConfigJSON), &cfg); err != nil || strings.TrimSpace(cfg.ChatID) == "" {
			return 0, "Telegram chat configuration is invalid", false
		}
		requestBody := map[string]any{"chat_id": cfg.ChatID, "text": message}
		if cfg.MessageThreadID != 0 {
			requestBody["message_thread_id"] = cfg.MessageThreadID
		}
		if format == "markdown" {
			requestBody["parse_mode"] = "MarkdownV2"
		}
		return m.postTelegram(ctx, secrets.BotToken, requestBody)
	default:
		return 0, "unsupported notification channel type", false
	}
}

func (m *Manager) doNotificationRequest(req *http.Request) (*http.Response, error) {
	client := *m.Client
	// Do not forward bot tokens or custom webhook headers to a redirect target.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}

func (m *Manager) postJSON(parent context.Context, endpoint string, payload any, headers map[string]string) (int, string, bool) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "notification payload encoding failed", false
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "notification request could not be created", false
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if !notificationHeaderNameRE.MatchString(name) || strings.ContainsAny(value, "\r\n") || len(value) > 4096 || normalized == "host" || normalized == "content-length" || normalized == "connection" || normalized == "transfer-encoding" || normalized == "upgrade" || normalized == "proxy-authorization" || normalized == "proxy-connection" {
			return 0, "invalid webhook header configuration", false
		}
		req.Header.Set(name, value)
	}
	resp, err := m.doNotificationRequest(req)
	if err != nil {
		return 0, "webhook network request failed", true
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, "", true
	}
	return resp.StatusCode, fmt.Sprintf("webhook http %d", resp.StatusCode), true
}

func (m *Manager) postTelegram(parent context.Context, token string, payload map[string]any) (int, string, bool) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "Telegram payload encoding failed", false
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	baseURL := strings.TrimRight(m.telegramAPIBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	url := baseURL + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "Telegram request could not be created", false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.doNotificationRequest(req)
	if err != nil {
		return 0, "Telegram network request failed", true
	}
	defer resp.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp.StatusCode, "Telegram response was invalid", true
		}
		return resp.StatusCode, fmt.Sprintf("Telegram http %d", resp.StatusCode), true
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && result.OK {
		return resp.StatusCode, "", true
	}
	return resp.StatusCode, "Telegram API rejected message", true
}

func mapStringAny(input map[string]string) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

// TestNotificationChannel performs one immediate delivery without creating a
// normal event or task delivery row. It updates the channel's last-test status.
func (m *Manager) TestNotificationChannel(channelID string) error {
	channel, err := m.Store.GetNotificationChannelForDelivery(channelID)
	if err != nil {
		return err
	}
	if channel.Type == "qq_official_bot" {
		failure := "QQ official bot proactive push is not supported by the platform"
		if channel.LegacyWebhookID == "" {
			_ = m.Store.RecordNotificationChannelTestResult(channel.ID, "unsupported", 0, failure)
		}
		return fmt.Errorf("%s", failure)
	}
	now := m.clock().Unix()
	job := storage.NotificationDeliveryJob{
		Delivery: storage.NotificationDelivery{ID: "test", ChannelID: channel.ID, TaskID: "test", TemplateID: "test"},
		Event:    storage.NotificationEvent{ID: fmt.Sprintf("test-%d", now), TemplateID: "test", TemplateFormat: "text", EventType: "notification.test", Severity: "P3", Source: "hub", Message: "VPS-Commander test notification", DataJSON: "{}", CreatedAt: now},
		Task:     storage.NotificationTask{ID: "test", ChannelID: channel.ID, TemplateID: "test", Enabled: true},
		Channel:  channel,
		Template: storage.NotificationTemplate{ID: "test", Name: "Test", EventType: "notification.test", Severity: "P3", Format: "text", VariablesJSON: "[]", Enabled: true},
	}
	status, failure, _ := m.sendNotificationDelivery(job)
	if failure == "" && status >= 200 && status < 300 {
		if channel.LegacyWebhookID != "" {
			err = m.Store.RecordWebhookDelivery(channel.LegacyWebhookID, status, "")
		} else {
			err = m.Store.RecordNotificationChannelTestResult(channel.ID, "success", status, "")
		}
		if err != nil {
			return fmt.Errorf("test sent but status could not be recorded")
		}
		return nil
	}
	if channel.LegacyWebhookID != "" {
		_ = m.Store.RecordWebhookDelivery(channel.LegacyWebhookID, status, failure)
	} else {
		_ = m.Store.RecordNotificationChannelTestResult(channel.ID, "failed", status, failure)
	}
	if failure == "" {
		failure = "notification test failed"
	}
	return fmt.Errorf("%s", failure)
}
