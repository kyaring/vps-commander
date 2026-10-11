package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

const (
	eventEmitWindowDuration = time.Minute
	eventEmitMaxPerWindow   = 30
	maxEventVariableBytes   = 4096
	maxEventVariablesBytes  = 16 * 1024
)

type eventEmitWindow struct {
	StartedAt time.Time
	Count     int
}

type notificationEventRequest struct {
	TemplateID string            `json:"template_id"`
	Severity   string            `json:"severity,omitempty"`
	Variables  map[string]string `json:"variables"`
	Source     string            `json:"source,omitempty"`
	Device     string            `json:"device,omitempty"`
}

var eventTemplatePlaceholderRE = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)

func (s *Server) allowNotificationEvent(tokenID string, now time.Time) (bool, int) {
	s.eventEmitMu.Lock()
	defer s.eventEmitMu.Unlock()
	if s.eventEmitWindows == nil {
		s.eventEmitWindows = make(map[string]eventEmitWindow)
	}
	window, ok := s.eventEmitWindows[tokenID]
	if !ok || now.Sub(window.StartedAt) >= eventEmitWindowDuration || now.Before(window.StartedAt) {
		window = eventEmitWindow{StartedAt: now}
	}
	if window.Count >= eventEmitMaxPerWindow {
		retry := int(eventEmitWindowDuration.Seconds() - now.Sub(window.StartedAt).Seconds())
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	window.Count++
	s.eventEmitWindows[tokenID] = window
	if len(s.eventEmitWindows) > 2048 {
		for key, candidate := range s.eventEmitWindows {
			if now.Sub(candidate.StartedAt) > 2*eventEmitWindowDuration {
				delete(s.eventEmitWindows, key)
			}
		}
	}
	return true, 0
}

func notificationActorTokenID(p auth.Principal) string {
	if id := p.QuotaID(); id != "" {
		return id
	}
	if p.TokenID != "" {
		return p.TokenID
	}
	return p.ID
}

func (s *Server) EmitNotificationEventJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := principal(r)
	actorID := notificationActorTokenID(p)
	if actorID == "" {
		http.Error(w, "authenticated token identity required", http.StatusUnauthorized)
		return
	}
	allowed, retryAfter := s.allowNotificationEvent(actorID, time.Now())
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		_ = s.audit(r, "", "notification_event_rate_limited", actorID, http.StatusTooManyRequests, 0, "event emission rate limit exceeded")
		http.Error(w, "event rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var q notificationEventRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "invalid event JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "request must contain one JSON object")
		return
	}
	q.TemplateID = strings.TrimSpace(q.TemplateID)
	if q.TemplateID == "" || q.TemplateID == "legacy-event-json" {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "enabled non-legacy template_id required")
		return
	}
	q.Severity = strings.TrimSpace(q.Severity)
	if q.Variables == nil {
		q.Variables = map[string]string{}
	}
	if len(q.Variables) > 64 {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "too many event variables")
		return
	}
	totalBytes := 0
	for key, value := range q.Variables {
		if len(key) > 64 || len(value) > maxEventVariableBytes {
			s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "event variable exceeds length limit")
			return
		}
		totalBytes += len(key) + len(value)
	}
	if totalBytes > maxEventVariablesBytes {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "event variables exceed total size limit")
		return
	}
	q.Source = strings.TrimSpace(q.Source)
	if q.Source == "" {
		q.Source = "api"
	}
	if len(q.Source) > 64 || !safeEventLabel(q.Source) {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "invalid source label")
		return
	}
	q.Device = strings.TrimSpace(q.Device)
	for _, ch := range q.Device {
		if unicode.IsControl(ch) {
			s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "device label contains control characters")
			return
		}
	}
	if len(q.Device) > 128 {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "device label exceeds length limit")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) > 128 || (idempotencyKey != "" && !safeIdempotencyKey(idempotencyKey)) {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "invalid Idempotency-Key")
		return
	}
	canonical, err := json.Marshal(struct {
		TemplateID        string            `json:"template_id"`
		RequestedSeverity string            `json:"severity"`
		Variables         map[string]string `json:"variables"`
		Source            string            `json:"source"`
		Device            string            `json:"device"`
	}{q.TemplateID, q.Severity, q.Variables, q.Source, q.Device})
	if err != nil {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "could not normalize event")
		return
	}
	requestHashBytes := sha256.Sum256(canonical)
	requestHash := hex.EncodeToString(requestHashBytes[:])
	if idempotencyKey != "" {
		prior, lookupErr := s.Store.NotificationEventByIdempotency(actorID, idempotencyKey)
		if lookupErr == nil {
			if prior.RequestHash != requestHash {
				_ = s.audit(r, "", "notification_event_idempotency_conflict", q.TemplateID, http.StatusConflict, 0, "idempotency key reused with different payload")
				http.Error(w, "Idempotency-Key already belongs to a different event", http.StatusConflict)
				return
			}
			count, countErr := s.Store.NotificationDeliveryCount(prior.ID)
			if countErr != nil {
				http.Error(w, "event delivery state unavailable", http.StatusInternalServerError)
				return
			}
			_ = s.audit(r, "", "notification_event_emit", prior.TemplateID, http.StatusOK, 0, "idempotent replay")
			w.WriteHeader(http.StatusOK)
			jsonOut(w, map[string]any{"ok": true, "event_id": prior.ID, "event_type": prior.EventType, "severity": prior.Severity, "matched_tasks": count, "deliveries_created": count, "replayed": true, "warning": eventNoTaskWarning(count)})
			return
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			http.Error(w, "event idempotency lookup failed", http.StatusInternalServerError)
			return
		}
	}
	template, err := s.Store.GetNotificationTemplate(q.TemplateID)
	if err != nil || !template.Enabled || template.EventType == "*" || template.Severity == "*" || template.Format == "raw_json" {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "unknown or non-emittable notification template")
		return
	}
	severity := template.Severity
	if q.Severity != "" {
		severity = strings.TrimSpace(q.Severity)
	}
	if severity != "P1" && severity != "P2" && severity != "P3" && severity != "P4" {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "severity must be P1-P4")
		return
	}
	if severity == "P1" && !p.HasScope("admin") {
		_ = s.audit(r, "", "notification_event_p1_denied", q.TemplateID, http.StatusForbidden, 0, "P1 emission requires admin scope")
		http.Error(w, "P1 emission requires admin scope", http.StatusForbidden)
		return
	}
	if severityRank(severity) < severityRank(template.Severity) && !p.HasScope("admin") {
		_ = s.audit(r, "", "notification_event_escalation_denied", q.TemplateID, http.StatusForbidden, 0, "severity escalation requires admin scope")
		http.Error(w, "severity escalation requires admin scope", http.StatusForbidden)
		return
	}
	var declared []string
	if err := json.Unmarshal([]byte(template.VariablesJSON), &declared); err != nil {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusInternalServerError, "template variable schema is invalid")
		return
	}
	allowedVars := make(map[string]bool, len(declared))
	for _, name := range declared {
		allowedVars[name] = true
	}
	if len(q.Variables) > len(allowedVars) {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "too many event variables")
		return
	}
	for key := range q.Variables {
		if !allowedVars[key] {
			s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "undeclared event variable")
			return
		}
	}
	title, body, err := renderExternalNotificationTemplate(template, q.Variables)
	if err != nil {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "template rendering failed")
		return
	}
	message := body
	if title != "" {
		message = title + "\n" + body
	}
	if len(message) > 8*1024 {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "rendered event message exceeds length limit")
		return
	}
	variablesJSON, err := json.Marshal(q.Variables)
	if err != nil {
		s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "invalid event variables")
		return
	}

	event := storage.NotificationEvent{ID: "evt_" + randomTokenPart(20), TemplateID: template.ID, TemplateFormat: template.Format, RequestHash: requestHash, EventType: template.EventType, Severity: severity, Source: q.Source, Device: q.Device, Message: message, DataJSON: string(variablesJSON), IdempotencyKey: idempotencyKey, ActorTokenID: actorID, CreatedAt: time.Now().Unix()}
	stored, matched, replayed, err := s.Store.AcceptNotificationEvent(event)
	if err != nil {
		if errors.Is(err, storage.ErrNotificationIdempotencyConflict) {
			_ = s.audit(r, "", "notification_event_idempotency_conflict", q.TemplateID, http.StatusConflict, 0, "idempotency key reused with different payload")
			http.Error(w, "Idempotency-Key already belongs to a different event", http.StatusConflict)
			return
		}
		if errors.Is(err, storage.ErrNotificationTemplateMismatch) {
			s.rejectNotificationEvent(w, r, q.TemplateID, http.StatusBadRequest, "event does not match selected template")
			return
		}
		if idempotencyKey != "" {
			if prior, lookupErr := s.Store.NotificationEventByIdempotency(actorID, idempotencyKey); lookupErr == nil {
				if prior.RequestHash != event.RequestHash {
					http.Error(w, "Idempotency-Key already belongs to a different event", http.StatusConflict)
					return
				}
				if count, countErr := s.Store.NotificationDeliveryCount(prior.ID); countErr == nil {
					stored, matched, replayed = prior, count, true
				} else {
					http.Error(w, "event delivery state unavailable", http.StatusInternalServerError)
					return
				}
			} else {
				_ = s.audit(r, "", "notification_event_emit_failed", q.TemplateID, http.StatusInternalServerError, 0, "event persistence failed")
				http.Error(w, "event persistence failed", http.StatusInternalServerError)
				return
			}
		} else {
			_ = s.audit(r, "", "notification_event_emit_failed", q.TemplateID, http.StatusInternalServerError, 0, "event persistence failed")
			http.Error(w, "event persistence failed", http.StatusInternalServerError)
			return
		}
	}
	statusCode := http.StatusAccepted
	if replayed {
		statusCode = http.StatusOK
	}
	_ = s.audit(r, "", "notification_event_emit", q.TemplateID, statusCode, 0, "severity="+severity+" matched_tasks="+strconv.Itoa(matched))
	w.WriteHeader(statusCode)
	jsonOut(w, map[string]any{"ok": true, "event_id": stored.ID, "event_type": stored.EventType, "severity": stored.Severity, "matched_tasks": matched, "deliveries_created": matched, "replayed": replayed, "warning": eventNoTaskWarning(matched)})
}

func (s *Server) rejectNotificationEvent(w http.ResponseWriter, r *http.Request, target string, status int, message string) {
	_ = s.audit(r, "", "notification_event_emit_rejected", target, status, 0, message)
	http.Error(w, message, status)
}

func eventNoTaskWarning(matched int) string {
	if matched == 0 {
		return "no enabled task matches this template and severity"
	}
	return ""
}

func severityRank(value string) int {
	switch value {
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	case "P4":
		return 4
	default:
		return 99
	}
}

func safeEventLabel(value string) bool {
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return value != ""
}

func safeIdempotencyKey(value string) bool {
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return value != ""
}

func renderExternalNotificationTemplate(template storage.NotificationTemplate, variables map[string]string) (string, string, error) {
	render := func(source string) (string, error) {
		clean := eventTemplatePlaceholderRE.ReplaceAllStringFunc(source, func(token string) string {
			match := eventTemplatePlaceholderRE.FindStringSubmatch(token)
			if len(match) != 2 {
				return token
			}
			value := variables[match[1]]
			if template.Format == "markdown" {
				value = escapeTelegramMarkdownV2(value)
			}
			return value
		})
		if strings.Contains(clean, "{{") || strings.Contains(clean, "}}") {
			return "", errors.New("unsupported template placeholder")
		}
		return clean, nil
	}
	title, err := render(template.TitleTemplate)
	if err != nil {
		return "", "", err
	}
	body, err := render(template.BodyTemplate)
	if err != nil {
		return "", "", err
	}
	return title, body, nil
}

func escapeTelegramMarkdownV2(value string) string {
	var b strings.Builder
	for _, r := range value {
		if strings.ContainsRune("_ * [ ] ( ) ~ ` > # + - = | { } . ! \\", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
