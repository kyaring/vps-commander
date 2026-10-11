package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wjyhk/vps-commander/internal/storage"
)

type notificationChannelInput struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Endpoint    string          `json:"endpoint,omitempty"`
	ConfigJSON  json.RawMessage `json:"config_json,omitempty"`
	SecretsJSON string          `json:"secrets_json,omitempty"`
	Enabled     bool            `json:"enabled"`
}

type notificationChannelView struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Type                  string          `json:"type"`
	Enabled               bool            `json:"enabled"`
	Legacy                bool            `json:"legacy"`
	EndpointConfigured    bool            `json:"endpoint_configured"`
	Config                json.RawMessage `json:"config,omitempty"`
	CredentialsConfigured bool            `json:"credentials_configured"`
	LastAttemptAt         int64           `json:"last_attempt_at"`
	LastSuccessAt         int64           `json:"last_success_at"`
	LastStatus            string          `json:"last_status"`
	LastHTTPStatus        int             `json:"last_http_status"`
	ConsecutiveFailures   int             `json:"consecutive_failures"`
	LastError             string          `json:"last_error,omitempty"`
}

func notificationChannelResponse(s *Server, channel storage.NotificationChannel) notificationChannelView {
	out := notificationChannelView{
		ID: channel.ID, Name: channel.Name, Type: channel.Type, Enabled: channel.Enabled,
		Legacy: channel.LegacyWebhookID != "", LastAttemptAt: channel.LastAttemptAt,
		LastSuccessAt: channel.LastSuccessAt, LastStatus: channel.LastStatus,
		LastHTTPStatus: channel.LastHTTPStatus, ConsecutiveFailures: channel.ConsecutiveFailures,
		LastError: channel.LastError,
	}
	if channel.Type == "webhook" && channel.Endpoint != "" {
		out.EndpointConfigured = true
	}
	if !out.Legacy {
		if safeConfig := safeNotificationChannelConfig(channel); len(safeConfig) > 0 {
			out.Config = safeConfig
		}
	}
	if !out.Legacy {
		if configured, err := s.Store.NotificationChannelHasSecrets(channel.ID); err == nil {
			out.CredentialsConfigured = configured
		}
	}
	return out
}

func safeNotificationChannelConfig(channel storage.NotificationChannel) json.RawMessage {
	if channel.Type == "webhook" || !json.Valid([]byte(channel.ConfigJSON)) {
		return nil
	}
	var input map[string]json.RawMessage
	if json.Unmarshal([]byte(channel.ConfigJSON), &input) != nil {
		return nil
	}
	allowed := []string{}
	switch channel.Type {
	case "telegram_bot":
		allowed = []string{"chat_id", "message_thread_id"}
	case "qq_official_bot":
		allowed = []string{"app_id", "target_type", "target_id"}
	default:
		return nil
	}
	output := map[string]json.RawMessage{}
	for _, key := range allowed {
		if value, ok := input[key]; ok {
			output[key] = value
		}
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil
	}
	return encoded
}

func (s *Server) NotificationChannelsV1JSON(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.Store.ListNotificationChannels()
		if err != nil {
			http.Error(w, "notification channels unavailable", http.StatusInternalServerError)
			return
		}
		out := make([]notificationChannelView, 0, len(items))
		for _, item := range items {
			out = append(out, notificationChannelResponse(s, item))
		}
		jsonOut(w, out)
	case http.MethodPost:
		var q notificationChannelInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&q); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		q.ID = strings.TrimSpace(q.ID)
		config := strings.TrimSpace(string(q.ConfigJSON))
		if config == "" {
			config = "{}"
		}
		v := storage.NotificationChannel{ID: q.ID, Name: q.Name, Type: q.Type, Endpoint: q.Endpoint, ConfigJSON: config, Enabled: q.Enabled}
		if err := s.Store.SaveNotificationChannel(v, q.SecretsJSON); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stored, err := s.Store.GetNotificationChannel(q.ID)
		if err != nil {
			http.Error(w, "channel saved but could not be read", http.StatusInternalServerError)
			return
		}
		_ = s.audit(r, "", "notification_channel", stored.ID, 0, 0, "")
		jsonOut(w, map[string]any{"ok": true, "channel": notificationChannelResponse(s, stored)})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := s.Store.DisableNotificationChannel(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = s.audit(r, "", "notification_channel_disable", id, 0, 0, "")
		jsonOut(w, map[string]any{"ok": true, "id": id, "enabled": false})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) NotificationTemplatesV1JSON(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.Store.ListNotificationTemplates()
		if err != nil {
			http.Error(w, "notification templates unavailable", http.StatusInternalServerError)
			return
		}
		jsonOut(w, items)
	case http.MethodPost:
		var v storage.NotificationTemplate
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&v); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		v.ID = strings.TrimSpace(v.ID)
		if err := s.Store.SaveNotificationTemplate(v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = s.audit(r, "", "notification_template", v.ID, 0, 0, "")
		jsonOut(w, map[string]any{"ok": true, "id": v.ID})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := s.Store.DisableNotificationTemplate(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = s.audit(r, "", "notification_template_disable", id, 0, 0, "")
		jsonOut(w, map[string]any{"ok": true, "id": id, "enabled": false})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) NotificationTasksV1JSON(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.Store.ListNotificationTasks()
		if err != nil {
			http.Error(w, "notification tasks unavailable", http.StatusInternalServerError)
			return
		}
		jsonOut(w, items)
	case http.MethodPost:
		var v storage.NotificationTask
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&v); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		v.ID = strings.TrimSpace(v.ID)
		if err := s.Store.SaveNotificationTask(v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = s.audit(r, "", "notification_task", v.ID, 0, 0, "")
		jsonOut(w, map[string]any{"ok": true, "id": v.ID})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := s.Store.DisableNotificationTask(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = s.audit(r, "", "notification_task_disable", id, 0, 0, "")
		jsonOut(w, map[string]any{"ok": true, "id": id, "enabled": false})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) NotificationChannelTestJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var q struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&q); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	q.ID = strings.TrimSpace(q.ID)
	if q.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if s.Notify == nil {
		http.Error(w, "notification sender unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.Notify.TestNotificationChannel(q.ID); err != nil {
		_ = s.audit(r, "", "notification_channel_test", q.ID, http.StatusBadGateway, 0, "test send failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = s.audit(r, "", "notification_channel_test", q.ID, http.StatusOK, 0, "test send succeeded")
	jsonOut(w, map[string]any{"ok": true, "id": q.ID, "status": "success"})
}
