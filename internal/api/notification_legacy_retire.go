package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/wjyhk/vps-commander/internal/storage"
)

type legacyWebhookRetireRequest struct {
	LegacyWebhookID string `json:"legacy_webhook_id"`
	ChannelID       string `json:"channel_id"`
	Name            string `json:"name"`
}

func (s *Server) RetireLegacyWebhookJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var q legacyWebhookRetireRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	if err := dec.Decode(&q); err != nil {
		http.Error(w, "invalid retirement request", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid retirement request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(q.LegacyWebhookID) == "" || strings.TrimSpace(q.ChannelID) == "" || strings.TrimSpace(q.Name) == "" || strings.HasPrefix(strings.TrimSpace(q.ChannelID), "legacy-") {
		http.Error(w, "legacy webhook ID, channel ID and name are required; channel ID must not use the legacy- prefix", http.StatusBadRequest)
		return
	}
	result, err := s.Store.RetireLegacyWebhook(q.LegacyWebhookID, q.ChannelID, q.Name)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrLegacyRetireBadRequest):
			http.Error(w, "legacy webhook URL or retirement request is invalid", http.StatusBadRequest)
		case errors.Is(err, storage.ErrLegacyRetireNotFound):
			http.Error(w, "legacy webhook not found", http.StatusNotFound)
		case errors.Is(err, storage.ErrLegacyRetireConflict):
			http.Error(w, "notification channel ID is already in use", http.StatusConflict)
		default:
			http.Error(w, "legacy webhook retirement failed", http.StatusInternalServerError)
		}
		return
	}
	_ = s.audit(r, "", "notification_legacy_webhook_retire", q.LegacyWebhookID, http.StatusOK, 0, "")
	jsonOut(w, map[string]any{
		"ok": true, "channel_id": result.ChannelID, "moved_tasks": result.MovedTasks,
		"retired_task": result.RetiredTask, "already_retired": result.AlreadyRetired,
	})
}

func maskLegacyWebhookURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "***"
	}
	return u.Scheme + "://" + u.Host + "/…"
}
