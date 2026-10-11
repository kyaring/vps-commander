package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotificationIdempotencyConflict = errors.New("idempotency key was already used for a different event")
	ErrNotificationTemplateMismatch    = errors.New("event does not match the selected template")
)

func newNotificationID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// AcceptNotificationEvent stores the event and all matching delivery rows in one
// SQLite transaction. A token-scoped idempotency key cannot create duplicate jobs.
func (s *Store) AcceptNotificationEvent(v NotificationEvent) (NotificationEvent, int, bool, error) {
	if v.ID == "" || v.TemplateID == "" || v.EventType == "" || v.Source == "" {
		return v, 0, false, errors.New("id, template_id, event_type and source are required")
	}
	if v.Severity != "P1" && v.Severity != "P2" && v.Severity != "P3" && v.Severity != "P4" {
		return v, 0, false, errors.New("invalid severity")
	}
	if v.DataJSON == "" {
		v.DataJSON = "{}"
	}
	if !json.Valid([]byte(v.DataJSON)) {
		return v, 0, false, errors.New("event variables must be valid JSON")
	}
	if v.CreatedAt == 0 {
		v.CreatedAt = time.Now().Unix()
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return v, 0, false, err
	}
	defer tx.Rollback()

	if v.ActorTokenID != "" && v.IdempotencyKey != "" {
		var existing NotificationEvent
		err := tx.QueryRow(`SELECT id,template_id,template_format,request_hash,event_type,severity,source,device,message,data_json,COALESCE(idempotency_key,''),COALESCE(actor_token_id,''),created_at FROM notification_events WHERE actor_token_id=? AND idempotency_key=?`, v.ActorTokenID, v.IdempotencyKey).Scan(&existing.ID, &existing.TemplateID, &existing.TemplateFormat, &existing.RequestHash, &existing.EventType, &existing.Severity, &existing.Source, &existing.Device, &existing.Message, &existing.DataJSON, &existing.IdempotencyKey, &existing.ActorTokenID, &existing.CreatedAt)
		if err == nil {
			if existing.RequestHash != v.RequestHash {
				return v, 0, false, ErrNotificationIdempotencyConflict
			}
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE event_id=?`, existing.ID).Scan(&count); err != nil {
				return v, 0, false, err
			}
			if err := tx.Commit(); err != nil {
				return v, 0, false, err
			}
			return existing, count, true, nil
		}
		if err != sql.ErrNoRows {
			return v, 0, false, err
		}
	}

	var templateEventType, templateSeverity, templateFormat string
	var templateEnabled int
	if err := tx.QueryRow(`SELECT event_type,severity,format,enabled FROM notification_templates WHERE id=?`, v.TemplateID).Scan(&templateEventType, &templateSeverity, &templateFormat, &templateEnabled); err != nil {
		return v, 0, false, err
	}
	if v.TemplateFormat == "" {
		v.TemplateFormat = templateFormat
	}
	if templateEnabled == 0 || (templateEventType != "*" && templateEventType != v.EventType) || templateEventType == "*" || templateSeverity == "*" {
		return v, 0, false, ErrNotificationTemplateMismatch
	}

	var idempotency any
	if v.IdempotencyKey != "" {
		idempotency = v.IdempotencyKey
	}
	var actor any
	if v.ActorTokenID != "" {
		actor = v.ActorTokenID
	}
	if _, err := tx.Exec(`INSERT INTO notification_events(id,template_id,template_format,request_hash,event_type,severity,source,device,message,data_json,idempotency_key,actor_token_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.TemplateID, v.TemplateFormat, v.RequestHash, v.EventType, v.Severity, v.Source, v.Device, v.Message, v.DataJSON, idempotency, actor, v.CreatedAt); err != nil {
		return v, 0, false, err
	}

	rows, err := tx.Query(`SELECT t.id,t.channel_id,t.template_id,t.event_types_json,t.severities_json,t.policy_json,COALESCE(c.legacy_webhook_id,'')
		FROM notification_tasks t JOIN notification_channels c ON c.id=t.channel_id JOIN notification_templates p ON p.id=t.template_id
		WHERE t.enabled=1 AND c.enabled=1 AND p.enabled=1 ORDER BY t.id`)
	if err != nil {
		return v, 0, false, err
	}
	type candidate struct{ taskID, channelID, templateID, eventTypesJSON, severitiesJSON, policyJSON, legacyID string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.taskID, &c.channelID, &c.templateID, &c.eventTypesJSON, &c.severitiesJSON, &c.policyJSON, &c.legacyID); err != nil {
			rows.Close()
			return v, 0, false, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return v, 0, false, err
	}
	rows.Close()
	matched := 0
	for _, c := range candidates {
		var eventTypes, severities []string
		var policy map[string]any
		if json.Unmarshal([]byte(c.eventTypesJSON), &eventTypes) != nil || json.Unmarshal([]byte(c.severitiesJSON), &severities) != nil || json.Unmarshal([]byte(c.policyJSON), &policy) != nil {
			continue
		}
		legacyCompat, _ := policy["legacy_compat"].(bool)
		if c.templateID != v.TemplateID && !(legacyCompat && c.legacyID != "") {
			continue
		}
		if !containsString(eventTypes, v.EventType) || !containsString(severities, v.Severity) {
			continue
		}
		deliveryID, err := newNotificationID("del_")
		if err != nil {
			return v, 0, false, err
		}
		now := time.Now().Unix()
		if _, err := tx.Exec(`INSERT INTO notification_deliveries(id,event_id,task_id,channel_id,template_id,status,attempt_count,next_attempt_at,last_error,last_http_status,created_at,updated_at,sent_at) VALUES(?,?,?,?,?, 'pending',0,?,'',0,?,?,0)`, deliveryID, v.ID, c.taskID, c.channelID, c.templateID, now, now, now); err != nil {
			return v, 0, false, err
		}
		matched++
	}
	if err := tx.Commit(); err != nil {
		return v, 0, false, err
	}
	return v, matched, false, nil
}

// ClaimNotificationDeliveries marks due rows as sending before returning them. A
// stale sending row is recoverable after a process crash or hard shutdown.
func (s *Store) ClaimNotificationDeliveries(now int64, limit int) ([]NotificationDeliveryJob, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE notification_deliveries SET status='pending',updated_at=? WHERE status='sending' AND updated_at<=?`, now, now-60); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT id FROM notification_deliveries WHERE status='pending' AND next_attempt_at<=? ORDER BY created_at,id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	claimed := make([]string, 0, len(ids))
	for _, id := range ids {
		result, err := tx.Exec(`UPDATE notification_deliveries SET status='sending',attempt_count=attempt_count+1,updated_at=? WHERE id=? AND status='pending' AND next_attempt_at<=?`, now, id, now)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if count > 0 {
			claimed = append(claimed, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	jobs := make([]NotificationDeliveryJob, 0, len(claimed))
	for _, id := range claimed {
		job, err := s.GetNotificationDeliveryJob(id)
		if err != nil {
			return jobs, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) GetNotificationDeliveryJob(id string) (NotificationDeliveryJob, error) {
	var j NotificationDeliveryJob
	var tEnabled, cEnabled, pEnabled int
	err := s.DB.QueryRow(`SELECT d.id,d.event_id,d.task_id,d.channel_id,d.template_id,d.status,d.attempt_count,d.next_attempt_at,d.last_error,d.last_http_status,d.created_at,d.updated_at,d.sent_at,
		e.id,e.template_id,e.template_format,e.request_hash,e.event_type,e.severity,e.source,e.device,e.message,e.data_json,COALESCE(e.idempotency_key,''),COALESCE(e.actor_token_id,''),e.created_at,
		t.id,t.name,t.channel_id,t.template_id,t.enabled,t.event_types_json,t.severities_json,t.policy_json,t.created_at,t.updated_at,
		c.id,c.name,c.type,c.endpoint,c.config_json,c.secrets_ciphertext,c.enabled,c.created_at,c.updated_at,c.last_attempt_at,c.last_success_at,c.last_status,c.last_http_status,c.consecutive_failures,c.last_error,COALESCE(c.legacy_webhook_id,''),c.legacy_type,
		p.id,p.name,p.event_type,p.severity,p.title_template,p.body_template,p.format,p.variables_json,p.enabled,p.created_at,p.updated_at
		FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id JOIN notification_tasks t ON t.id=d.task_id JOIN notification_channels c ON c.id=d.channel_id JOIN notification_templates p ON p.id=d.template_id WHERE d.id=?`, id).Scan(
		&j.Delivery.ID, &j.Delivery.EventID, &j.Delivery.TaskID, &j.Delivery.ChannelID, &j.Delivery.TemplateID, &j.Delivery.Status, &j.Delivery.AttemptCount, &j.Delivery.NextAttemptAt, &j.Delivery.LastError, &j.Delivery.LastHTTPStatus, &j.Delivery.CreatedAt, &j.Delivery.UpdatedAt, &j.Delivery.SentAt,
		&j.Event.ID, &j.Event.TemplateID, &j.Event.TemplateFormat, &j.Event.RequestHash, &j.Event.EventType, &j.Event.Severity, &j.Event.Source, &j.Event.Device, &j.Event.Message, &j.Event.DataJSON, &j.Event.IdempotencyKey, &j.Event.ActorTokenID, &j.Event.CreatedAt,
		&j.Task.ID, &j.Task.Name, &j.Task.ChannelID, &j.Task.TemplateID, &tEnabled, &j.Task.EventTypesJSON, &j.Task.SeveritiesJSON, &j.Task.PolicyJSON, &j.Task.CreatedAt, &j.Task.UpdatedAt,
		&j.Channel.ID, &j.Channel.Name, &j.Channel.Type, &j.Channel.Endpoint, &j.Channel.ConfigJSON, &j.Channel.SecretsCiphertext, &cEnabled, &j.Channel.CreatedAt, &j.Channel.UpdatedAt, &j.Channel.LastAttemptAt, &j.Channel.LastSuccessAt, &j.Channel.LastStatus, &j.Channel.LastHTTPStatus, &j.Channel.ConsecutiveFailures, &j.Channel.LastError, &j.Channel.LegacyWebhookID, &j.Channel.LegacyType,
		&j.Template.ID, &j.Template.Name, &j.Template.EventType, &j.Template.Severity, &j.Template.TitleTemplate, &j.Template.BodyTemplate, &j.Template.Format, &j.Template.VariablesJSON, &pEnabled, &j.Template.CreatedAt, &j.Template.UpdatedAt)
	if err != nil {
		return j, err
	}
	j.Task.Enabled = tEnabled != 0
	j.Channel.Enabled = cEnabled != 0
	j.Template.Enabled = pEnabled != 0
	return j, nil
}

func (s *Store) CompleteNotificationDelivery(id, channelID, status string, httpStatus int, failure string, nextAttemptAt, sentAt int64) error {
	now := time.Now().Unix()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE notification_deliveries SET status=?,last_http_status=?,last_error=?,next_attempt_at=?,sent_at=?,updated_at=? WHERE id=?`, status, httpStatus, failure, nextAttemptAt, sentAt, now, id); err != nil {
		return err
	}
	channelStatus := status
	if status == "sent" {
		channelStatus = "success"
	} else if status == "pending" || status == "failed" {
		channelStatus = "failed"
	}
	if status != "suppressed" {
		if _, err := tx.Exec(`UPDATE notification_channels SET last_attempt_at=?,last_success_at=CASE WHEN ?='success' THEN ? ELSE last_success_at END,last_status=?,last_http_status=?,consecutive_failures=CASE WHEN ?='success' THEN 0 WHEN ?='unsupported' THEN consecutive_failures ELSE consecutive_failures+1 END,last_error=?,updated_at=? WHERE id=? AND COALESCE(legacy_webhook_id,'')=''`, now, channelStatus, now, channelStatus, httpStatus, channelStatus, channelStatus, failure, now, channelID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) NotificationDeliveryCount(eventID string) (int, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE event_id=?`, eventID).Scan(&count)
	return count, err
}

func (s *Store) NotificationEventByID(id string) (NotificationEvent, error) {
	var v NotificationEvent
	err := s.DB.QueryRow(`SELECT id,template_id,template_format,request_hash,event_type,severity,source,device,message,data_json,COALESCE(idempotency_key,''),COALESCE(actor_token_id,''),created_at FROM notification_events WHERE id=?`, id).Scan(&v.ID, &v.TemplateID, &v.TemplateFormat, &v.RequestHash, &v.EventType, &v.Severity, &v.Source, &v.Device, &v.Message, &v.DataJSON, &v.IdempotencyKey, &v.ActorTokenID, &v.CreatedAt)
	return v, err
}
