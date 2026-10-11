package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) NotificationDeliveriesV1JSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			http.Error(w, "limit must be between 1 and 200", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	items, err := s.Store.ListNotificationDeliveries(limit)
	if err != nil {
		http.Error(w, "delivery history unavailable", http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]any{"items": items})
}

func (s *Server) NotificationDeliveryAttemptsV1JSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.GetNotificationDeliveryJob(id); err != nil {
		http.Error(w, "delivery not found", http.StatusNotFound)
		return
	}
	items, err := s.Store.ListNotificationDeliveryAttempts(id)
	if err != nil {
		http.Error(w, "delivery attempts unavailable", http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]any{"items": items})
}

func (s *Server) NotificationDeliveryRetryV1JSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var q struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	q.ID = strings.TrimSpace(q.ID)
	if q.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.Store.RetryNotificationDelivery(q.ID); err != nil {
		_ = s.audit(r, "", "notification_delivery_retry", q.ID, http.StatusBadRequest, 0, "manual retry rejected")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = s.audit(r, "", "notification_delivery_retry", q.ID, http.StatusAccepted, 0, "manual retry queued")
	w.WriteHeader(http.StatusAccepted)
	jsonOut(w, map[string]any{"ok": true, "id": q.ID, "status": "pending"})
}
