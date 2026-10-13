package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// NotificationChannel is a configured delivery endpoint. ConfigJSON is never
// intended to be returned verbatim by a public API because it may contain secrets.
type NotificationChannel struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	Endpoint            string `json:"-"`
	ConfigJSON          string `json:"-"`
	SecretsCiphertext   []byte `json:"-"`
	Enabled             bool   `json:"enabled"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
	LastAttemptAt       int64  `json:"last_attempt_at"`
	LastSuccessAt       int64  `json:"last_success_at"`
	LastStatus          string `json:"last_status"`
	LastHTTPStatus      int    `json:"last_http_status"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastError           string `json:"last_error,omitempty"`
	LegacyWebhookID     string `json:"legacy_webhook_id,omitempty"`
	LegacyType          string `json:"legacy_type,omitempty"`
}

// NotificationTemplate describes how a normalized event is rendered.
type NotificationTemplate struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	EventType     string `json:"event_type"`
	Severity      string `json:"severity"`
	TitleTemplate string `json:"title_template"`
	BodyTemplate  string `json:"body_template"`
	Format        string `json:"format"`
	VariablesJSON string `json:"variables_json"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// NotificationTask binds one event template to one channel and owns routing policy.
type NotificationTask struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ChannelID      string `json:"channel_id"`
	TemplateID     string `json:"template_id"`
	Enabled        bool   `json:"enabled"`
	EventTypesJSON string `json:"event_types_json"`
	SeveritiesJSON string `json:"severities_json"`
	PolicyJSON     string `json:"policy_json"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// NotificationEvent is the durable, normalized input to the notification router.
type NotificationEvent struct {
	ID             string `json:"id"`
	TemplateID     string `json:"template_id"`
	TemplateFormat string `json:"template_format,omitempty"`
	RequestHash    string `json:"-"`
	EventType      string `json:"event_type"`
	Severity       string `json:"severity"`
	Source         string `json:"source"`
	Device         string `json:"device,omitempty"`
	Message        string `json:"message"`
	DataJSON       string `json:"data_json,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	ActorTokenID   string `json:"actor_token_id,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	Shadow         bool   `json:"-"` // transient mode: persist matching decisions without sending
}

// NotificationDelivery tracks one event's delivery through one task/channel.
type NotificationDelivery struct {
	ID              string `json:"id"`
	EventID         string `json:"event_id"`
	TaskID          string `json:"task_id"`
	ChannelID       string `json:"channel_id"`
	TemplateID      string `json:"template_id"`
	Status          string `json:"status"`
	AttemptCount    int    `json:"attempt_count"`
	RetryCount      int    `json:"retry_count"`
	RenderedMessage string `json:"rendered_message,omitempty"`
	MergedCount     int    `json:"merged_count"`
	NextAttemptAt   int64  `json:"next_attempt_at"`
	LastError       string `json:"last_error,omitempty"`
	LastHTTPStatus  int    `json:"last_http_status"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	SentAt          int64  `json:"sent_at"`
}

type NotificationDeliveryJob struct {
	Delivery NotificationDelivery
	Event    NotificationEvent
	Task     NotificationTask
	Channel  NotificationChannel
	Template NotificationTemplate
}

// NotificationTaskState stores per-task episode counters used for throttling,
// quiet windows, recovery notifications and aggregation.
type NotificationTaskState struct {
	TaskID            string `json:"task_id"`
	DeduplicationKey  string `json:"deduplication_key"`
	Active            bool   `json:"active"`
	FirstSeenAt       int64  `json:"first_seen_at"`
	LastSeenAt        int64  `json:"last_seen_at"`
	NotificationCount int    `json:"notification_count"`
	LastNotifiedAt    int64  `json:"last_notified_at"`
	NextDueAt         int64  `json:"next_due_at"`
	MergedUntil       int64  `json:"merged_until"`
	ResolvedAt        int64  `json:"resolved_at"`
	StateJSON         string `json:"state_json,omitempty"`
}

const legacyEventTemplateID = "legacy-event-json"

func (s *Store) migrateNotificationV1() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS notification_channels (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL,
			endpoint TEXT NOT NULL DEFAULT '', config_json TEXT NOT NULL DEFAULT '{}',
			enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			secrets_ciphertext BLOB NOT NULL DEFAULT X'',
			last_attempt_at INTEGER NOT NULL DEFAULT 0, last_success_at INTEGER NOT NULL DEFAULT 0,
			last_status TEXT NOT NULL DEFAULT 'unknown', last_http_status INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
			legacy_webhook_id TEXT UNIQUE, legacy_type TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_channels_enabled ON notification_channels(enabled,type)`,
		`CREATE TABLE IF NOT EXISTS notification_templates (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, event_type TEXT NOT NULL, severity TEXT NOT NULL DEFAULT '*',
			title_template TEXT NOT NULL DEFAULT '', body_template TEXT NOT NULL DEFAULT '',
			format TEXT NOT NULL DEFAULT 'text', variables_json TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			CHECK(severity IN ('P1','P2','P3','P4','*'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_templates_lookup ON notification_templates(event_type,severity,enabled)`,
		`CREATE TABLE IF NOT EXISTS notification_tasks (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, channel_id TEXT NOT NULL, template_id TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1, event_types_json TEXT NOT NULL DEFAULT '["*"]',
			severities_json TEXT NOT NULL DEFAULT '["P1","P2","P3","P4"]',
			policy_json TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			FOREIGN KEY(channel_id) REFERENCES notification_channels(id) ON DELETE RESTRICT,
			FOREIGN KEY(template_id) REFERENCES notification_templates(id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_tasks_enabled ON notification_tasks(enabled,channel_id)`,
		`CREATE TABLE IF NOT EXISTS notification_events (
			id TEXT PRIMARY KEY, template_id TEXT NOT NULL DEFAULT '', template_format TEXT NOT NULL DEFAULT '', request_hash TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL, severity TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '', device TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '',
			data_json TEXT NOT NULL DEFAULT '{}', idempotency_key TEXT, actor_token_id TEXT,
			created_at INTEGER NOT NULL, CHECK(severity IN ('P1','P2','P3','P4')),
			UNIQUE(actor_token_id,idempotency_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_events_created ON notification_events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_events_type_severity ON notification_events(event_type,severity,created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS notification_deliveries (
			id TEXT PRIMARY KEY, event_id TEXT NOT NULL, task_id TEXT NOT NULL, channel_id TEXT NOT NULL, template_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending', attempt_count INTEGER NOT NULL DEFAULT 0, retry_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
			last_http_status INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		sent_at INTEGER NOT NULL DEFAULT 0, rendered_message TEXT NOT NULL DEFAULT '', merged_count INTEGER NOT NULL DEFAULT 1, UNIQUE(event_id,task_id),
			FOREIGN KEY(event_id) REFERENCES notification_events(id) ON DELETE RESTRICT,
			FOREIGN KEY(task_id) REFERENCES notification_tasks(id) ON DELETE RESTRICT,
			FOREIGN KEY(channel_id) REFERENCES notification_channels(id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_deliveries_due ON notification_deliveries(status,next_attempt_at)`,
		`CREATE TABLE IF NOT EXISTS notification_task_state (
			task_id TEXT NOT NULL, deduplication_key TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1,
			first_seen_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, notification_count INTEGER NOT NULL DEFAULT 0,
			last_notified_at INTEGER NOT NULL DEFAULT 0, next_due_at INTEGER NOT NULL DEFAULT 0,
			merged_until INTEGER NOT NULL DEFAULT 0, resolved_at INTEGER NOT NULL DEFAULT 0,
			state_json TEXT NOT NULL DEFAULT '{}', PRIMARY KEY(task_id,deduplication_key),
			FOREIGN KEY(task_id) REFERENCES notification_tasks(id) ON DELETE RESTRICT
		)`,
	}
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("create notification schema: %w", err)
		}
	}
	if err := ensureNotificationColumn(tx, "notification_channels", "last_http_status", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_channels", "consecutive_failures", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_channels", "secrets_ciphertext", "BLOB NOT NULL DEFAULT X''"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_events", "template_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_events", "template_format", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_events", "request_hash", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_deliveries", "template_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_deliveries", "retry_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_deliveries", "rendered_message", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureNotificationColumn(tx, "notification_deliveries", "merged_count", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS notification_delivery_attempts (id INTEGER PRIMARY KEY AUTOINCREMENT, delivery_id TEXT NOT NULL, attempt_number INTEGER NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0, outcome TEXT NOT NULL DEFAULT 'in_flight', http_status INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', UNIQUE(delivery_id,attempt_number))`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_notification_delivery_attempts_delivery ON notification_delivery_attempts(delivery_id,attempt_number)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE notification_deliveries SET template_id=COALESCE((SELECT template_id FROM notification_tasks WHERE notification_tasks.id=notification_deliveries.task_id),'') WHERE template_id=''`); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO notification_templates
		(id,name,event_type,severity,title_template,body_template,format,variables_json,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, legacyEventTemplateID, "Legacy webhook event JSON", "*", "*", "", "", "raw_json", `[]`, 1, now, now); err != nil {
		return fmt.Errorf("seed legacy event template: %w", err)
	}
	builtins := []NotificationTemplate{
		{ID: "development-completed", Name: "开发完成", EventType: "development.completed", Severity: "P3", TitleTemplate: "开发完成：{{commit}}", BodyTemplate: "任务：{{task}}\n提交：{{commit}}\n摘要：{{summary}}", Format: "text", VariablesJSON: `["task","commit","summary"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "review-completed", Name: "审核完成", EventType: "review.completed", Severity: "P3", TitleTemplate: "审核完成：{{result}}", BodyTemplate: "结果：{{result}}\n提交：{{commit}}\n摘要：{{summary}}", Format: "text", VariablesJSON: `["result","commit","summary"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "builtin-node-offline", Name: "节点离线", EventType: "node_offline", Severity: "P3", BodyTemplate: "{{message}}", Format: "text", VariablesJSON: `["message"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "builtin-node-recovery", Name: "节点恢复", EventType: "node_recovery", Severity: "P3", BodyTemplate: "{{message}}", Format: "text", VariablesJSON: `["message"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "builtin-resource-threshold", Name: "资源阈值告警", EventType: "resource_threshold", Severity: "P3", BodyTemplate: "{{message}}", Format: "text", VariablesJSON: `["message"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "builtin-resource-recovery", Name: "资源恢复", EventType: "resource_recovery", Severity: "P3", BodyTemplate: "{{message}}", Format: "text", VariablesJSON: `["message"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "builtin-scheduled-report", Name: "定时报告", EventType: "scheduled_report", Severity: "P3", BodyTemplate: "{{message}}", Format: "text", VariablesJSON: `["message"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "builtin-default", Name: "默认事件", EventType: "custom.default", Severity: "P3", TitleTemplate: "{{title}}", BodyTemplate: "{{message}}", Format: "text", VariablesJSON: `["title","message"]`, Enabled: true, CreatedAt: now, UpdatedAt: now},
	}
	for _, template := range builtins {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO notification_templates(id,name,event_type,severity,title_template,body_template,format,variables_json,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, template.ID, template.Name, template.EventType, template.Severity, template.TitleTemplate, template.BodyTemplate, template.Format, template.VariablesJSON, 1, template.CreatedAt, template.UpdatedAt); err != nil {
			return fmt.Errorf("seed built-in notification template %q: %w", template.ID, err)
		}
	}
	rows, err := tx.Query(`SELECT w.id,w.name,w.type,w.url,COALESCE(w.config_json,''),w.enabled,w.created_at,w.updated_at,
		COALESCE(d.consecutive_failures,0),COALESCE(d.last_error,''),COALESCE(d.last_status,0),COALESCE(d.updated_at,0)
		FROM webhook_targets w LEFT JOIN webhook_delivery_state d ON d.target_id=w.id ORDER BY w.id`)
	if err != nil {
		return fmt.Errorf("read legacy webhook targets: %w", err)
	}
	type legacyTarget struct {
		id, name, kind, endpoint, config string
		enabled                          int
		createdAt, updatedAt             int64
		consecutiveFailures, httpStatus  int
		lastError                        string
		lastAttemptAt                    int64
	}
	var targets []legacyTarget
	for rows.Next() {
		var v legacyTarget
		if err := rows.Scan(&v.id, &v.name, &v.kind, &v.endpoint, &v.config, &v.enabled, &v.createdAt, &v.updatedAt, &v.consecutiveFailures, &v.lastError, &v.httpStatus, &v.lastAttemptAt); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, v := range targets {
		channelID := legacyChannelID(v.id)
		taskID := legacyTaskID(v.id)
		if v.config == "" {
			v.config = "{}"
		}
		status := "unknown"
		lastSuccessAt := int64(0)
		if v.lastAttemptAt > 0 || v.httpStatus > 0 || v.lastError != "" {
			status = "failed"
			if v.lastError == "" && v.httpStatus >= 200 && v.httpStatus < 300 {
				status = "success"
				lastSuccessAt = v.lastAttemptAt
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO notification_channels
			(id,name,type,endpoint,config_json,enabled,created_at,updated_at,last_attempt_at,last_success_at,last_status,last_http_status,consecutive_failures,last_error,legacy_webhook_id,legacy_type)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, channelID, v.name, "webhook", v.endpoint, v.config, v.enabled, v.createdAt, v.updatedAt, v.lastAttemptAt, lastSuccessAt, status, v.httpStatus, v.consecutiveFailures, v.lastError, v.id, v.kind); err != nil {
			return fmt.Errorf("migrate legacy webhook %q to channel: %w", v.id, err)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO notification_tasks
			(id,name,channel_id,template_id,enabled,event_types_json,severities_json,policy_json,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, taskID, "Legacy webhook: "+v.name, channelID, legacyEventTemplateID, v.enabled, `["node_offline","node_recovery","resource_threshold","resource_recovery","scheduled_report"]`, `["P1","P2","P3","P4"]`, `{"legacy_compat":true}`, v.createdAt, v.updatedAt); err != nil {
			return fmt.Errorf("migrate legacy webhook %q to task: %w", v.id, err)
		}
	}
	// Normalize rows created by earlier v1.1.35 builds as well; INSERT OR IGNORE
	// alone would leave their old wildcard filter in place after an upgrade.
	if _, err := tx.Exec(`UPDATE notification_tasks SET event_types_json=? WHERE id LIKE 'legacy-task:%' AND policy_json LIKE '%"legacy_compat":true%'`, `["node_offline","node_recovery","resource_threshold","resource_recovery","scheduled_report"]`); err != nil {
		return fmt.Errorf("restrict legacy compatibility event filters: %w", err)
	}
	return tx.Commit()
}

func ensureNotificationColumn(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition); err != nil {
		return fmt.Errorf("add notification column %s.%s: %w", table, column, err)
	}
	return nil
}

func legacyChannelID(webhookID string) string { return "legacy-webhook:" + webhookID }
func legacyTaskID(webhookID string) string    { return "legacy-task:" + webhookID }

func (s *Store) ListNotificationChannels() ([]NotificationChannel, error) {
	rows, err := s.DB.Query(`SELECT id,name,type,endpoint,config_json,enabled,created_at,updated_at,last_attempt_at,last_success_at,last_status,last_http_status,consecutive_failures,last_error,COALESCE(legacy_webhook_id,''),legacy_type FROM notification_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationChannel{}
	for rows.Next() {
		var v NotificationChannel
		var enabled int
		if err := rows.Scan(&v.ID, &v.Name, &v.Type, &v.Endpoint, &v.ConfigJSON, &enabled, &v.CreatedAt, &v.UpdatedAt, &v.LastAttemptAt, &v.LastSuccessAt, &v.LastStatus, &v.LastHTTPStatus, &v.ConsecutiveFailures, &v.LastError, &v.LegacyWebhookID, &v.LegacyType); err != nil {
			return nil, err
		}
		v.Enabled = enabled != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ListNotificationTemplates() ([]NotificationTemplate, error) {
	rows, err := s.DB.Query(`SELECT id,name,event_type,severity,title_template,body_template,format,variables_json,enabled,created_at,updated_at FROM notification_templates ORDER BY event_type,severity,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationTemplate{}
	for rows.Next() {
		var v NotificationTemplate
		var enabled int
		if err := rows.Scan(&v.ID, &v.Name, &v.EventType, &v.Severity, &v.TitleTemplate, &v.BodyTemplate, &v.Format, &v.VariablesJSON, &enabled, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Enabled = enabled != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ListNotificationTasks() ([]NotificationTask, error) {
	rows, err := s.DB.Query(`SELECT id,name,channel_id,template_id,enabled,event_types_json,severities_json,policy_json,created_at,updated_at FROM notification_tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationTask{}
	for rows.Next() {
		var v NotificationTask
		var enabled int
		if err := rows.Scan(&v.ID, &v.Name, &v.ChannelID, &v.TemplateID, &enabled, &v.EventTypesJSON, &v.SeveritiesJSON, &v.PolicyJSON, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Enabled = enabled != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DisableLegacyNotificationTarget(webhookID string) error {
	_, err := s.DB.Exec(`UPDATE notification_channels SET enabled=0,updated_at=? WHERE legacy_webhook_id=?`, time.Now().Unix(), webhookID)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE notification_tasks SET enabled=0,updated_at=? WHERE id=?`, time.Now().Unix(), legacyTaskID(webhookID))
	return err
}

func (s *Store) SaveNotificationEvent(v NotificationEvent) error {
	if v.ID == "" || v.EventType == "" || v.Source == "" {
		return fmt.Errorf("id, event type and source required")
	}
	if v.Severity != "P1" && v.Severity != "P2" && v.Severity != "P3" && v.Severity != "P4" {
		return fmt.Errorf("invalid severity")
	}
	if v.DataJSON == "" {
		v.DataJSON = "{}"
	}
	if v.CreatedAt == 0 {
		v.CreatedAt = time.Now().Unix()
	}
	var idempotency any
	if v.IdempotencyKey != "" {
		idempotency = v.IdempotencyKey
	}
	var actor any
	if v.ActorTokenID != "" {
		actor = v.ActorTokenID
	}
	_, err := s.DB.Exec(`INSERT INTO notification_events(id,template_id,template_format,request_hash,event_type,severity,source,device,message,data_json,idempotency_key,actor_token_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.TemplateID, v.TemplateFormat, v.RequestHash, v.EventType, v.Severity, v.Source, v.Device, v.Message, v.DataJSON, idempotency, actor, v.CreatedAt)
	return err
}

func (s *Store) NotificationEventByIdempotency(actorTokenID, key string) (NotificationEvent, error) {
	var v NotificationEvent
	err := s.DB.QueryRow(`SELECT id,template_id,template_format,request_hash,event_type,severity,source,device,message,data_json,COALESCE(idempotency_key,''),COALESCE(actor_token_id,''),created_at FROM notification_events WHERE actor_token_id=? AND idempotency_key=?`, actorTokenID, key).Scan(&v.ID, &v.TemplateID, &v.TemplateFormat, &v.RequestHash, &v.EventType, &v.Severity, &v.Source, &v.Device, &v.Message, &v.DataJSON, &v.IdempotencyKey, &v.ActorTokenID, &v.CreatedAt)
	return v, err
}

func (s *Store) CreateNotificationDelivery(v NotificationDelivery) error {
	if v.ID == "" || v.EventID == "" || v.TaskID == "" || v.ChannelID == "" {
		return fmt.Errorf("delivery id, event, task and channel required")
	}
	if v.Status == "" {
		v.Status = "pending"
	}
	now := time.Now().Unix()
	if v.CreatedAt == 0 {
		v.CreatedAt = now
	}
	if v.UpdatedAt == 0 {
		v.UpdatedAt = now
	}
	_, err := s.DB.Exec(`INSERT INTO notification_deliveries(id,event_id,task_id,channel_id,status,attempt_count,next_attempt_at,last_error,last_http_status,created_at,updated_at,sent_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.EventID, v.TaskID, v.ChannelID, v.Status, v.AttemptCount, v.NextAttemptAt, v.LastError, v.LastHTTPStatus, v.CreatedAt, v.UpdatedAt, v.SentAt)
	return err
}
