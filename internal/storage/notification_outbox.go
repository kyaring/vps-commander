package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
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
		var legacyPolicy map[string]any
		if json.Unmarshal([]byte(c.eventTypesJSON), &eventTypes) != nil || json.Unmarshal([]byte(c.severitiesJSON), &severities) != nil || json.Unmarshal([]byte(c.policyJSON), &legacyPolicy) != nil {
			continue
		}
		legacyCompat, _ := legacyPolicy["legacy_compat"].(bool)
		if c.templateID != v.TemplateID && !(legacyCompat && c.legacyID != "") {
			continue
		}
		if !containsString(eventTypes, v.EventType) || !containsString(severities, v.Severity) {
			continue
		}
		if v.Shadow {
			deliveryID, err := newNotificationID("del_")
			if err != nil {
				return v, 0, false, err
			}
			now := v.CreatedAt
			if now == 0 {
				now = time.Now().Unix()
			}
			if _, err := tx.Exec(`INSERT INTO notification_deliveries(id,event_id,task_id,channel_id,template_id,status,attempt_count,next_attempt_at,last_error,last_http_status,created_at,updated_at,sent_at,rendered_message,merged_count) VALUES(?,?,?,?,?,'shadow',0,?,'shadow mode: not sent',0,?,?,0,?,1)`, deliveryID, v.ID, c.taskID, c.channelID, c.templateID, now, now, now, v.Message); err != nil {
				return v, 0, false, err
			}
			matched++
			continue
		}
		policyJSON := c.policyJSON
		if legacyCompat {
			policyJSON = "{}"
		}
		policy, err := ParseNotificationTaskPolicy(policyJSON)
		if err != nil { // Invalid persisted policy must fail closed for this task.
			continue
		}
		now := v.CreatedAt
		if now == 0 {
			now = time.Now().Unix()
		}
		stateKey := notificationTaskStateKey(v)
		isRecovery := containsString(policy.RecoveryEventTypes, v.EventType)
		if isRecovery {
			stateKey = notificationRecoveryStateKey(v)
		}
		var state NotificationTaskState
		stateErr := tx.QueryRow(`SELECT task_id,deduplication_key,active,first_seen_at,last_seen_at,notification_count,last_notified_at,next_due_at,merged_until,resolved_at,state_json FROM notification_task_state WHERE task_id=? AND deduplication_key=?`, c.taskID, stateKey).Scan(&state.TaskID, &state.DeduplicationKey, &state.Active, &state.FirstSeenAt, &state.LastSeenAt, &state.NotificationCount, &state.LastNotifiedAt, &state.NextDueAt, &state.MergedUntil, &state.ResolvedAt, &state.StateJSON)
		if stateErr != nil && stateErr != sql.ErrNoRows {
			return v, 0, false, stateErr
		}
		if stateErr == sql.ErrNoRows {
			state = NotificationTaskState{TaskID: c.taskID, DeduplicationKey: stateKey, FirstSeenAt: now}
		}
		state.LastSeenAt = now
		if isRecovery {
			identityPrefix := notificationTaskIdentityPrefix(v)
			var activeKeys []struct{ taskID, key string }
			rows, err := tx.Query(`SELECT st.task_id,st.deduplication_key FROM notification_task_state st JOIN notification_tasks t ON t.id=st.task_id WHERE t.channel_id=? AND st.active=1 AND substr(st.deduplication_key,1,length(?))=?`, c.channelID, identityPrefix, identityPrefix)
			if err != nil {
				return v, 0, false, err
			}
			for rows.Next() {
				var active struct{ taskID, key string }
				if err := rows.Scan(&active.taskID, &active.key); err != nil {
					rows.Close()
					return v, 0, false, err
				}
				alertType, decodeErr := url.QueryUnescape(strings.TrimPrefix(active.key, identityPrefix))
				if decodeErr == nil && containsString(policy.RecoveryForEventTypes, alertType) {
					activeKeys = append(activeKeys, active)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return v, 0, false, err
			}
			rows.Close()
			for _, active := range activeKeys {
				if _, err := tx.Exec(`UPDATE notification_task_state SET active=0,resolved_at=?,last_seen_at=? WHERE task_id=? AND deduplication_key=? AND active=1`, now, now, active.taskID, active.key); err != nil {
					return v, 0, false, err
				}
			}
			activeEpisodes := len(activeKeys)

			deliveryStatus, deliveryError, nextAttemptAt := "pending", "", now
			switch {
			case activeEpisodes == 0:
				deliveryStatus, deliveryError = "suppressed", "no active episode to recover"
			case policy.NotifyRecovery != nil && !*policy.NotifyRecovery:
				deliveryStatus, deliveryError = "suppressed", "recovery notifications disabled by task policy"
			case policy.RecoveryCooldownSeconds > 0 && state.LastNotifiedAt > 0 && now-state.LastNotifiedAt < policy.RecoveryCooldownSeconds:
				deliveryStatus, deliveryError = "suppressed", "recovery cooldown has not elapsed"
			default:
				quietNow := time.Unix(now, 0)
				if quietUntil := NotificationQuietUntil(quietNow, policy); !quietUntil.IsZero() && quietUntil.Unix() > nextAttemptAt {
					nextAttemptAt = quietUntil.Unix()
				}
				state.NotificationCount++
				state.LastNotifiedAt = now
				state.NextDueAt = now + policy.RecoveryCooldownSeconds
			}
			if state.FirstSeenAt == 0 {
				state.FirstSeenAt = now
			}
			if _, err := tx.Exec(`INSERT INTO notification_task_state(task_id,deduplication_key,active,first_seen_at,last_seen_at,notification_count,last_notified_at,next_due_at,merged_until,resolved_at,state_json) VALUES(?,?,0,?,?,?,?,?,?,?,?) ON CONFLICT(task_id,deduplication_key) DO UPDATE SET active=0,last_seen_at=excluded.last_seen_at,notification_count=excluded.notification_count,last_notified_at=excluded.last_notified_at,next_due_at=excluded.next_due_at,resolved_at=excluded.resolved_at,state_json=excluded.state_json`, state.TaskID, state.DeduplicationKey, state.FirstSeenAt, state.LastSeenAt, state.NotificationCount, state.LastNotifiedAt, state.NextDueAt, state.MergedUntil, now, state.StateJSON); err != nil {
				return v, 0, false, err
			}
			deliveryID, err := newNotificationID("del_")
			if err != nil {
				return v, 0, false, err
			}
			if _, err := tx.Exec(`INSERT INTO notification_deliveries(id,event_id,task_id,channel_id,template_id,status,attempt_count,next_attempt_at,last_error,last_http_status,created_at,updated_at,sent_at,rendered_message,merged_count) VALUES(?,?,?,?,?,?,0,?,?,0,?,?,0,'',1)`, deliveryID, v.ID, c.taskID, c.channelID, c.templateID, deliveryStatus, nextAttemptAt, deliveryError, now, now); err != nil {
				return v, 0, false, err
			}
			matched++
			continue
		}
		if policy.MergeWindowSeconds > 0 {
			var priorID, priorEventID, priorRendered, priorEventMessage string
			var priorMergedCount int
			err := tx.QueryRow(`SELECT d.id,d.event_id,d.merged_count,d.rendered_message,e.message FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE d.task_id=? AND d.status='pending' AND d.attempt_count=0 AND d.created_at>? AND e.event_type=? AND e.device=? AND e.source=? ORDER BY d.created_at,d.id LIMIT 1`, c.taskID, now-policy.MergeWindowSeconds, v.EventType, v.Device, v.Source).Scan(&priorID, &priorEventID, &priorMergedCount, &priorRendered, &priorEventMessage)
			if err == nil {
				if priorRendered == "" {
					priorRendered = priorEventMessage
				}
				merged := appendMergedNotificationMessage(priorRendered, v.Message, priorMergedCount+1)
				if _, err := tx.Exec(`UPDATE notification_deliveries SET rendered_message=?,merged_count=merged_count+1,updated_at=? WHERE id=? AND status='pending' AND attempt_count=0`, merged, now, priorID); err != nil {
					return v, 0, false, err
				}
				if _, err := tx.Exec(`INSERT INTO notification_task_state(task_id,deduplication_key,active,first_seen_at,last_seen_at,notification_count,last_notified_at,next_due_at,merged_until,resolved_at,state_json) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(task_id,deduplication_key) DO UPDATE SET last_seen_at=excluded.last_seen_at`, state.TaskID, state.DeduplicationKey, boolInt(state.Active), state.FirstSeenAt, state.LastSeenAt, state.NotificationCount, state.LastNotifiedAt, state.NextDueAt, state.MergedUntil, state.ResolvedAt, state.StateJSON); err != nil {
					return v, 0, false, err
				}
				deliveryID, err := newNotificationID("del_")
				if err != nil {
					return v, 0, false, err
				}
				if _, err := tx.Exec(`INSERT INTO notification_deliveries(id,event_id,task_id,channel_id,template_id,status,attempt_count,next_attempt_at,last_error,last_http_status,created_at,updated_at,sent_at,rendered_message,merged_count) VALUES(?,?,?,?,?,'suppressed',0,?,?,0,?,?,0,'',1)`, deliveryID, v.ID, c.taskID, c.channelID, c.templateID, now, "merged into delivery "+priorID, now, now); err != nil {
					return v, 0, false, err
				}
				matched++
				continue
			} else if err != sql.ErrNoRows {
				return v, 0, false, err
			}
		}
		deliveryStatus, deliveryError, nextAttemptAt := "pending", "", now
		suppressReason := ""
		if policy.MaxReminders > 0 && state.NotificationCount >= policy.MaxReminders {
			suppressReason = "maximum notifications reached"
		}
		if suppressReason == "" && policy.RepeatIntervalSeconds > 0 && state.LastNotifiedAt > 0 && now-state.LastNotifiedAt < policy.RepeatIntervalSeconds {
			suppressReason = "repeat interval has not elapsed"
		}
		if suppressReason != "" {
			deliveryStatus, deliveryError = "suppressed", suppressReason
		} else {
			quietNow := time.Unix(now, 0)
			if quietUntil := NotificationQuietUntil(quietNow, policy); !quietUntil.IsZero() && quietUntil.Unix() > nextAttemptAt {
				nextAttemptAt = quietUntil.Unix()
			}
			if policy.MergeWindowSeconds > 0 && now+policy.MergeWindowSeconds > nextAttemptAt {
				nextAttemptAt = now + policy.MergeWindowSeconds
			}
			state.Active = true
			state.NotificationCount++
			state.LastNotifiedAt = now
			state.NextDueAt = now + policy.RepeatIntervalSeconds
		}
		if state.FirstSeenAt == 0 {
			state.FirstSeenAt = now
		}
		if _, err := tx.Exec(`INSERT INTO notification_task_state(task_id,deduplication_key,active,first_seen_at,last_seen_at,notification_count,last_notified_at,next_due_at,merged_until,resolved_at,state_json) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(task_id,deduplication_key) DO UPDATE SET active=excluded.active,first_seen_at=excluded.first_seen_at,last_seen_at=excluded.last_seen_at,notification_count=excluded.notification_count,last_notified_at=excluded.last_notified_at,next_due_at=excluded.next_due_at,merged_until=excluded.merged_until,resolved_at=excluded.resolved_at,state_json=excluded.state_json`, state.TaskID, state.DeduplicationKey, boolInt(state.Active), state.FirstSeenAt, state.LastSeenAt, state.NotificationCount, state.LastNotifiedAt, state.NextDueAt, state.MergedUntil, state.ResolvedAt, state.StateJSON); err != nil {
			return v, 0, false, err
		}
		deliveryID, err := newNotificationID("del_")
		if err != nil {
			return v, 0, false, err
		}
		initialRenderedMessage := ""
		initialMergedCount := 1
		if policy.MergeWindowSeconds > 0 && deliveryStatus == "pending" {
			initialRenderedMessage = v.Message
		}
		if _, err := tx.Exec(`INSERT INTO notification_deliveries(id,event_id,task_id,channel_id,template_id,status,attempt_count,next_attempt_at,last_error,last_http_status,created_at,updated_at,sent_at,rendered_message,merged_count) VALUES(?,?,?,?,?,?,0,?,?,0,?,?,0,?,?)`, deliveryID, v.ID, c.taskID, c.channelID, c.templateID, deliveryStatus, nextAttemptAt, deliveryError, now, now, initialRenderedMessage, initialMergedCount); err != nil {
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
	if _, err := tx.Exec(`UPDATE notification_delivery_attempts SET outcome='interrupted',finished_at=?,error='worker interrupted' WHERE outcome='in_flight' AND delivery_id IN (SELECT id FROM notification_deliveries WHERE status='sending' AND updated_at<=?)`, now, now-60); err != nil {
		return nil, err
	}
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
		result, err := tx.Exec(`UPDATE notification_deliveries SET status='sending',attempt_count=attempt_count+1,retry_count=retry_count+1,updated_at=? WHERE id=? AND status='pending' AND next_attempt_at<=?`, now, id, now)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if count > 0 {
			var attemptNumber int
			if err := tx.QueryRow(`SELECT attempt_count FROM notification_deliveries WHERE id=?`, id).Scan(&attemptNumber); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`INSERT INTO notification_delivery_attempts(delivery_id,attempt_number,started_at,finished_at,outcome,http_status,error) VALUES(?,?,?,0,'in_flight',0,'')`, id, attemptNumber, now); err != nil {
				return nil, err
			}
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
	err := s.DB.QueryRow(`SELECT d.id,d.event_id,d.task_id,d.channel_id,d.template_id,d.status,d.attempt_count,d.retry_count,d.rendered_message,d.merged_count,d.next_attempt_at,d.last_error,d.last_http_status,d.created_at,d.updated_at,d.sent_at,
		e.id,e.template_id,e.template_format,e.request_hash,e.event_type,e.severity,e.source,e.device,e.message,e.data_json,COALESCE(e.idempotency_key,''),COALESCE(e.actor_token_id,''),e.created_at,
		t.id,t.name,t.channel_id,t.template_id,t.enabled,t.event_types_json,t.severities_json,t.policy_json,t.created_at,t.updated_at,
		c.id,c.name,c.type,c.endpoint,c.config_json,c.secrets_ciphertext,c.enabled,c.created_at,c.updated_at,c.last_attempt_at,c.last_success_at,c.last_status,c.last_http_status,c.consecutive_failures,c.last_error,COALESCE(c.legacy_webhook_id,''),c.legacy_type,
		p.id,p.name,p.event_type,p.severity,p.title_template,p.body_template,p.format,p.variables_json,p.enabled,p.created_at,p.updated_at
		FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id JOIN notification_tasks t ON t.id=d.task_id JOIN notification_channels c ON c.id=d.channel_id JOIN notification_templates p ON p.id=d.template_id WHERE d.id=?`, id).Scan(
		&j.Delivery.ID, &j.Delivery.EventID, &j.Delivery.TaskID, &j.Delivery.ChannelID, &j.Delivery.TemplateID, &j.Delivery.Status, &j.Delivery.AttemptCount, &j.Delivery.RetryCount, &j.Delivery.RenderedMessage, &j.Delivery.MergedCount, &j.Delivery.NextAttemptAt, &j.Delivery.LastError, &j.Delivery.LastHTTPStatus, &j.Delivery.CreatedAt, &j.Delivery.UpdatedAt, &j.Delivery.SentAt,
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
	_, err := s.completeNotificationDelivery(id, channelID, 0, status, httpStatus, failure, nextAttemptAt, sentAt)
	return err
}

// CompleteNotificationDeliveryAttempt only lets the worker that owns the
// current attempt mutate the delivery row. A late response from a stale worker
// is recorded separately and cannot overwrite a newer attempt's result.
func (s *Store) CompleteNotificationDeliveryAttempt(id, channelID string, attemptNumber int, status string, httpStatus int, failure string, nextAttemptAt, sentAt int64) (bool, error) {
	if attemptNumber < 1 {
		return false, errors.New("attempt number must be positive")
	}
	return s.completeNotificationDelivery(id, channelID, attemptNumber, status, httpStatus, failure, nextAttemptAt, sentAt)
}

func (s *Store) completeNotificationDelivery(id, channelID string, attemptNumber int, status string, httpStatus int, failure string, nextAttemptAt, sentAt int64) (bool, error) {
	now := time.Now().Unix()
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var result sql.Result
	if attemptNumber > 0 {
		result, err = tx.Exec(`UPDATE notification_deliveries SET status=?,last_http_status=?,last_error=?,next_attempt_at=?,sent_at=?,updated_at=? WHERE id=? AND status='sending' AND attempt_count=?`, status, httpStatus, failure, nextAttemptAt, sentAt, now, id, attemptNumber)
	} else {
		result, err = tx.Exec(`UPDATE notification_deliveries SET status=?,last_http_status=?,last_error=?,next_attempt_at=?,sent_at=?,updated_at=? WHERE id=?`, status, httpStatus, failure, nextAttemptAt, sentAt, now, id)
	}
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 && attemptNumber > 0 {
		lateOutcome := "late_" + status
		if status == "pending" {
			lateOutcome = "late_retry_scheduled"
		}
		if _, err := tx.Exec(`UPDATE notification_delivery_attempts SET finished_at=?,outcome=?,http_status=?,error=? WHERE delivery_id=? AND attempt_number=? AND outcome IN ('in_flight','interrupted')`, now, lateOutcome, httpStatus, failure, id, attemptNumber); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	attemptOutcome := status
	if status == "pending" {
		attemptOutcome = "retry_scheduled"
	}
	if attemptNumber > 0 {
		if _, err := tx.Exec(`UPDATE notification_delivery_attempts SET finished_at=?,outcome=?,http_status=?,error=? WHERE delivery_id=? AND attempt_number=? AND outcome='in_flight'`, now, attemptOutcome, httpStatus, failure, id, attemptNumber); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.Exec(`UPDATE notification_delivery_attempts SET finished_at=?,outcome=?,http_status=?,error=? WHERE id=(SELECT id FROM notification_delivery_attempts WHERE delivery_id=? AND outcome='in_flight' ORDER BY attempt_number DESC LIMIT 1)`, now, attemptOutcome, httpStatus, failure, id); err != nil {
			return false, err
		}
	}
	channelStatus := status
	if status == "sent" {
		channelStatus = "success"
	} else if status == "pending" || status == "failed" {
		channelStatus = "failed"
	}
	if status != "suppressed" {
		if _, err := tx.Exec(`UPDATE notification_channels SET last_attempt_at=?,last_success_at=CASE WHEN ?='success' THEN ? ELSE last_success_at END,last_status=?,last_http_status=?,consecutive_failures=CASE WHEN ?='success' THEN 0 WHEN ?='unsupported' THEN consecutive_failures ELSE consecutive_failures+1 END,last_error=?,updated_at=? WHERE id=? AND COALESCE(legacy_webhook_id,'')=''`, now, channelStatus, now, channelStatus, httpStatus, channelStatus, channelStatus, failure, now, channelID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
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

type NotificationDeliveryView struct {
	ID             string `json:"id"`
	EventID        string `json:"event_id"`
	EventType      string `json:"event_type"`
	Severity       string `json:"severity"`
	Source         string `json:"source"`
	Device         string `json:"device"`
	TaskID         string `json:"task_id"`
	TaskName       string `json:"task_name"`
	ChannelID      string `json:"channel_id"`
	ChannelName    string `json:"channel_name"`
	TemplateID     string `json:"template_id"`
	Status         string `json:"status"`
	AttemptCount   int    `json:"attempt_count"`
	RetryCount     int    `json:"retry_count"`
	MergedCount    int    `json:"merged_count"`
	NextAttemptAt  int64  `json:"next_attempt_at"`
	LastError      string `json:"last_error"`
	LastHTTPStatus int    `json:"last_http_status"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	SentAt         int64  `json:"sent_at"`
}

type NotificationDeliveryAttempt struct {
	ID            int64  `json:"id"`
	DeliveryID    string `json:"delivery_id"`
	AttemptNumber int    `json:"attempt_number"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at"`
	Outcome       string `json:"outcome"`
	HTTPStatus    int    `json:"http_status"`
	Error         string `json:"error"`
}

func (s *Store) ListNotificationDeliveries(limit int) ([]NotificationDeliveryView, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.DB.Query(`SELECT d.id,d.event_id,e.event_type,e.severity,e.source,e.device,t.id,t.name,c.id,c.name,d.template_id,d.status,d.attempt_count,d.retry_count,d.merged_count,d.next_attempt_at,d.last_error,d.last_http_status,d.created_at,d.updated_at,d.sent_at FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id JOIN notification_tasks t ON t.id=d.task_id JOIN notification_channels c ON c.id=d.channel_id ORDER BY d.updated_at DESC,d.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDeliveryView{}
	for rows.Next() {
		var v NotificationDeliveryView
		if err := rows.Scan(&v.ID, &v.EventID, &v.EventType, &v.Severity, &v.Source, &v.Device, &v.TaskID, &v.TaskName, &v.ChannelID, &v.ChannelName, &v.TemplateID, &v.Status, &v.AttemptCount, &v.RetryCount, &v.MergedCount, &v.NextAttemptAt, &v.LastError, &v.LastHTTPStatus, &v.CreatedAt, &v.UpdatedAt, &v.SentAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RetryNotificationDelivery(id string) error {
	result, err := s.DB.Exec(`UPDATE notification_deliveries SET status='pending',retry_count=0,next_attempt_at=?,last_error='',last_http_status=0,sent_at=0,updated_at=? WHERE id=? AND status='failed'`, time.Now().Unix(), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("only failed deliveries can be retried")
	}
	return nil
}

func (s *Store) ListNotificationDeliveryAttempts(deliveryID string) ([]NotificationDeliveryAttempt, error) {
	rows, err := s.DB.Query(`SELECT id,delivery_id,attempt_number,started_at,finished_at,outcome,http_status,error FROM notification_delivery_attempts WHERE delivery_id=? ORDER BY attempt_number`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDeliveryAttempt{}
	for rows.Next() {
		var v NotificationDeliveryAttempt
		if err := rows.Scan(&v.ID, &v.DeliveryID, &v.AttemptNumber, &v.StartedAt, &v.FinishedAt, &v.Outcome, &v.HTTPStatus, &v.Error); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func appendMergedNotificationMessage(existing, incoming string, count int) string {
	if strings.HasPrefix(existing, "[merged x") {
		if index := strings.Index(existing, "\n"); index >= 0 {
			existing = existing[index+1:]
		}
	}
	if existing == "" {
		existing = incoming
	}
	if incoming != "" && !strings.Contains(existing, incoming) {
		if len(existing)+len(incoming)+8 <= 8*1024 {
			existing += "\n• " + incoming
		} else {
			existing += "\n• additional events merged"
		}
	}
	if len(existing) > 8*1024 {
		existing = existing[:8*1024]
	}
	return fmt.Sprintf("[merged x%d]\n%s", count, existing)
}

// notificationTaskStateKey scopes repeat suppression to one task, event type,
// and originating device (or source when no device is supplied).
func notificationTaskIdentityPrefix(v NotificationEvent) string {
	identity := strings.TrimSpace(v.Device)
	if identity == "" {
		identity = "source:" + url.QueryEscape(strings.TrimSpace(v.Source))
	} else {
		identity = "device:" + url.QueryEscape(identity)
	}
	return identity + "|event:"
}

func notificationTaskStateKey(v NotificationEvent) string {
	return notificationTaskIdentityPrefix(v) + url.QueryEscape(v.EventType)
}
func notificationRecoveryStateKey(v NotificationEvent) string {
	identity := strings.TrimSpace(v.Device)
	if identity == "" {
		identity = "source:" + url.QueryEscape(strings.TrimSpace(v.Source))
	} else {
		identity = "device:" + url.QueryEscape(identity)
	}
	return "recovery|" + identity
}
