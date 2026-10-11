package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var notificationSecretHeaderNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

var notificationTemplatePlaceholderRE = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)

func (s *Store) GetNotificationChannel(id string) (NotificationChannel, error) {
	var v NotificationChannel
	var enabled int
	err := s.DB.QueryRow(`SELECT id,name,type,endpoint,config_json,enabled,created_at,updated_at,last_attempt_at,last_success_at,last_status,last_http_status,consecutive_failures,last_error,COALESCE(legacy_webhook_id,''),legacy_type FROM notification_channels WHERE id=?`, id).Scan(&v.ID, &v.Name, &v.Type, &v.Endpoint, &v.ConfigJSON, &enabled, &v.CreatedAt, &v.UpdatedAt, &v.LastAttemptAt, &v.LastSuccessAt, &v.LastStatus, &v.LastHTTPStatus, &v.ConsecutiveFailures, &v.LastError, &v.LegacyWebhookID, &v.LegacyType)
	v.Enabled = enabled != 0
	return v, err
}

func (s *Store) GetNotificationChannelSecrets(id string) ([]byte, error) {
	var ciphertext []byte
	if err := s.DB.QueryRow(`SELECT secrets_ciphertext FROM notification_channels WHERE id=?`, id).Scan(&ciphertext); err != nil {
		return nil, err
	}
	return s.DecryptNotificationChannelSecrets(id, ciphertext)
}

func (s *Store) NotificationChannelHasSecrets(id string) (bool, error) {
	var size int
	err := s.DB.QueryRow(`SELECT length(secrets_ciphertext) FROM notification_channels WHERE id=?`, id).Scan(&size)
	return size > 0, err
}

func containsNotificationSecretKey(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
			if normalized == "token" || normalized == "bottoken" || normalized == "appsecret" || normalized == "secret" || normalized == "password" || normalized == "apikey" || normalized == "authorization" || normalized == "cookie" || normalized == "privatekey" || containsNotificationSecretKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsNotificationSecretKey(child) {
				return true
			}
		}
	}
	return false
}

func (s *Store) SaveNotificationChannel(v NotificationChannel, secretsJSON string) error {
	v.ID = strings.TrimSpace(v.ID)
	v.Name = strings.TrimSpace(v.Name)
	if v.ID == "" || v.Name == "" {
		return errors.New("channel id and name are required")
	}
	if v.Type != "webhook" && v.Type != "telegram_bot" && v.Type != "qq_official_bot" {
		return errors.New("unsupported notification channel type")
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
	if !json.Valid([]byte(v.ConfigJSON)) {
		return errors.New("channel config must be valid JSON")
	}
	var publicConfig any
	if err := json.Unmarshal([]byte(v.ConfigJSON), &publicConfig); err != nil {
		return errors.New("channel config must be valid JSON")
	}
	if containsNotificationSecretKey(publicConfig) {
		return errors.New("secret values must be placed in secrets_json, not config_json")
	}
	configObject, ok := publicConfig.(map[string]any)
	if !ok {
		return errors.New("channel config must be a JSON object")
	}
	allowedConfigKeys := map[string]bool{}
	switch v.Type {
	case "webhook":
		allowedConfigKeys = map[string]bool{"method": true}
	case "telegram_bot":
		allowedConfigKeys = map[string]bool{"chat_id": true, "message_thread_id": true}
	case "qq_official_bot":
		allowedConfigKeys = map[string]bool{"app_id": true, "target_type": true, "target_id": true}
	}
	for key := range configObject {
		if !allowedConfigKeys[key] {
			return fmt.Errorf("unsupported public config field %q; credentials and custom headers belong in secrets_json", key)
		}
	}
	if method, ok := configObject["method"]; ok && v.Type == "webhook" && method != "POST" {
		return errors.New("webhook method must be POST")
	}
	if secretsJSON != "" && !json.Valid([]byte(secretsJSON)) {
		return errors.New("channel secrets must be valid JSON")
	}
	if v.Type == "webhook" && strings.TrimSpace(v.Endpoint) == "" {
		if existing, err := s.GetNotificationChannel(v.ID); err == nil && existing.Type == "webhook" && existing.LegacyWebhookID == "" {
			v.Endpoint = existing.Endpoint // blank endpoint on update means retain the stored URL
		}
	}
	if v.Type == "webhook" {
		u, err := url.Parse(strings.TrimSpace(v.Endpoint))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return errors.New("webhook endpoint must be an http(s) URL without userinfo")
		}
	} else if strings.TrimSpace(v.Endpoint) != "" {
		return errors.New("endpoint is only valid for webhook channels")
	}
	if v.Type == "telegram_bot" {
		var cfg struct {
			ChatID string `json:"chat_id"`
		}
		if err := json.Unmarshal([]byte(v.ConfigJSON), &cfg); err != nil || strings.TrimSpace(cfg.ChatID) == "" {
			return errors.New("telegram channel requires config.chat_id")
		}
	}
	if v.Type == "qq_official_bot" {
		var cfg struct {
			AppID      string `json:"app_id"`
			TargetType string `json:"target_type"`
			TargetID   string `json:"target_id"`
		}
		if err := json.Unmarshal([]byte(v.ConfigJSON), &cfg); err != nil || strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.TargetType) == "" || strings.TrimSpace(cfg.TargetID) == "" {
			return errors.New("QQ official bot requires config.app_id, target_type and target_id")
		}
	}
	now := time.Now().Unix()
	if v.CreatedAt == 0 {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var legacyID, oldType string
	var oldSecrets []byte
	err = tx.QueryRow(`SELECT COALESCE(legacy_webhook_id,''),type,secrets_ciphertext FROM notification_channels WHERE id=?`, v.ID).Scan(&legacyID, &oldType, &oldSecrets)
	exists := err == nil
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if exists && legacyID != "" {
		return errors.New("legacy webhook channels must be edited through the compatibility API")
	}
	var sealed []byte
	if secretsJSON != "" {
		var secrets map[string]json.RawMessage
		if err := json.Unmarshal([]byte(secretsJSON), &secrets); err != nil || secrets == nil {
			return errors.New("channel secrets must be a JSON object")
		}
		if v.Type == "webhook" {
			for key := range secrets {
				if key != "headers" {
					return fmt.Errorf("unsupported webhook secret field %q", key)
				}
			}
			if raw, ok := secrets["headers"]; ok {
				var headers map[string]string
				if err := json.Unmarshal(raw, &headers); err != nil || len(headers) > 32 {
					return errors.New("webhook secrets.headers must be a string map with at most 32 entries")
				}
				for name, value := range headers {
					normalized := strings.ToLower(strings.TrimSpace(name))
					if !notificationSecretHeaderNameRE.MatchString(name) || strings.ContainsAny(value, "\r\n") || len(value) > 4096 || normalized == "host" || normalized == "content-length" || normalized == "connection" || normalized == "transfer-encoding" || normalized == "upgrade" || normalized == "proxy-authorization" || normalized == "proxy-connection" {
						return fmt.Errorf("invalid webhook secret header %q", name)
					}
				}
			}
		}
		if v.Type == "telegram_bot" {
			for key := range secrets {
				if key != "bot_token" {
					return fmt.Errorf("unsupported Telegram secret field %q", key)
				}
			}
			var token string
			_ = json.Unmarshal(secrets["bot_token"], &token)
			if strings.TrimSpace(token) == "" {
				return errors.New("telegram channel requires secrets.bot_token")
			}
		}
		if v.Type == "qq_official_bot" {
			for key := range secrets {
				if key != "app_secret" {
					return fmt.Errorf("unsupported QQ secret field %q", key)
				}
			}
			var secret string
			_ = json.Unmarshal(secrets["app_secret"], &secret)
			if strings.TrimSpace(secret) == "" {
				return errors.New("QQ official bot requires secrets.app_secret")
			}
		}
		sealed, err = s.EncryptNotificationChannelSecrets(v.ID, []byte(secretsJSON))
		if err != nil {
			return err
		}
	} else if exists && oldType == v.Type {
		sealed = oldSecrets // blank secret field on update means retain current credentials
	}
	if (v.Type == "telegram_bot" || v.Type == "qq_official_bot") && len(sealed) == 0 {
		return errors.New("provider credentials are required")
	}
	if sealed == nil {
		sealed = []byte{}
	}
	if v.Type == "qq_official_bot" {
		v.Enabled = false
		v.LastStatus = "unsupported"
		v.LastError = "QQ official bot proactive push is not supported by the platform"
	} else if v.LastStatus == "" || v.LastStatus == "unsupported" {
		v.LastStatus = "unknown"
		v.LastError = ""
	}
	_, err = tx.Exec(`INSERT INTO notification_channels(id,name,type,endpoint,config_json,secrets_ciphertext,enabled,created_at,updated_at,last_attempt_at,last_success_at,last_status,last_http_status,consecutive_failures,last_error,legacy_webhook_id,legacy_type)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,'') ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,endpoint=excluded.endpoint,config_json=excluded.config_json,secrets_ciphertext=excluded.secrets_ciphertext,enabled=excluded.enabled,updated_at=excluded.updated_at,last_status=CASE WHEN excluded.type='qq_official_bot' THEN 'unsupported' ELSE CASE WHEN notification_channels.last_status='unsupported' THEN 'unknown' ELSE notification_channels.last_status END END,last_error=CASE WHEN excluded.type='qq_official_bot' THEN excluded.last_error ELSE CASE WHEN notification_channels.last_status='unsupported' THEN '' ELSE notification_channels.last_error END END`, v.ID, v.Name, v.Type, v.Endpoint, v.ConfigJSON, sealed, boolInt(v.Enabled), v.CreatedAt, v.UpdatedAt, v.LastAttemptAt, v.LastSuccessAt, v.LastStatus, v.LastHTTPStatus, v.ConsecutiveFailures, v.LastError)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DisableNotificationChannel(id string) error {
	existing, err := s.GetNotificationChannel(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if existing.LegacyWebhookID != "" {
		return errors.New("legacy webhook channels must be disabled through the compatibility API")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE notification_channels SET enabled=0,updated_at=? WHERE id=? AND COALESCE(legacy_webhook_id,'')=''`, now, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE notification_tasks SET enabled=0,updated_at=? WHERE channel_id=?`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveNotificationTemplate(v NotificationTemplate) error {
	v.ID, v.Name, v.EventType = strings.TrimSpace(v.ID), strings.TrimSpace(v.Name), strings.TrimSpace(v.EventType)
	if v.ID == legacyEventTemplateID {
		return errors.New("legacy compatibility template is read-only")
	}
	if v.ID == "" || v.Name == "" || v.EventType == "" {
		return errors.New("template id, name and event_type are required")
	}
	if v.Severity != "P1" && v.Severity != "P2" && v.Severity != "P3" && v.Severity != "P4" && v.Severity != "*" {
		return errors.New("template severity must be P1-P4 or *")
	}
	if v.Format != "text" && v.Format != "markdown" && v.Format != "raw_json" {
		return errors.New("template format must be text, markdown or raw_json")
	}
	if v.VariablesJSON == "" {
		v.VariablesJSON = "[]"
	}
	var variableNames []string
	if err := json.Unmarshal([]byte(v.VariablesJSON), &variableNames); err != nil || len(variableNames) > 64 {
		return errors.New("template variables must be a JSON string array with at most 64 entries")
	}
	seenVariables := map[string]bool{}
	for _, name := range variableNames {
		if name == "" || len(name) > 64 || seenVariables[name] || name[0] < 'a' || name[0] > 'z' {
			return errors.New("template variable names must start with a lowercase letter, be unique and at most 64 characters")
		}
		for _, ch := range name {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_') {
				return fmt.Errorf("invalid template variable name %q", name)
			}
		}
		seenVariables[name] = true
	}
	if v.Format != "raw_json" && v.BodyTemplate == "" {
		return errors.New("template body is required")
	}
	if v.Format != "raw_json" {
		for _, source := range []string{v.TitleTemplate, v.BodyTemplate} {
			for _, match := range notificationTemplatePlaceholderRE.FindAllStringSubmatch(source, -1) {
				if !seenVariables[match[1]] {
					return fmt.Errorf("template placeholder %q is not declared in variables_json", match[1])
				}
			}
			clean := notificationTemplatePlaceholderRE.ReplaceAllString(source, "")
			if strings.Contains(clean, "{{") || strings.Contains(clean, "}}") {
				return errors.New("template contains unsupported placeholder syntax")
			}
		}
	}
	now := time.Now().Unix()
	if v.CreatedAt == 0 {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	_, err := s.DB.Exec(`INSERT INTO notification_templates(id,name,event_type,severity,title_template,body_template,format,variables_json,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,event_type=excluded.event_type,severity=excluded.severity,title_template=excluded.title_template,body_template=excluded.body_template,format=excluded.format,variables_json=excluded.variables_json,enabled=excluded.enabled,updated_at=excluded.updated_at`, v.ID, v.Name, v.EventType, v.Severity, v.TitleTemplate, v.BodyTemplate, v.Format, v.VariablesJSON, boolInt(v.Enabled), v.CreatedAt, v.UpdatedAt)
	return err
}

func (s *Store) GetNotificationTemplate(id string) (NotificationTemplate, error) {
	var v NotificationTemplate
	var enabled int
	err := s.DB.QueryRow(`SELECT id,name,event_type,severity,title_template,body_template,format,variables_json,enabled,created_at,updated_at FROM notification_templates WHERE id=?`, id).Scan(&v.ID, &v.Name, &v.EventType, &v.Severity, &v.TitleTemplate, &v.BodyTemplate, &v.Format, &v.VariablesJSON, &enabled, &v.CreatedAt, &v.UpdatedAt)
	v.Enabled = enabled != 0
	return v, err
}

func (s *Store) DisableNotificationTemplate(id string) error {
	if id == legacyEventTemplateID {
		return errors.New("legacy compatibility template cannot be disabled")
	}
	if _, err := s.GetNotificationTemplate(strings.TrimSpace(id)); err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE notification_templates SET enabled=0,updated_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE notification_tasks SET enabled=0,updated_at=? WHERE template_id=?`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target || value == "*" {
			return true
		}
	}
	return false
}

func (s *Store) SaveNotificationTask(v NotificationTask) error {
	v.ID, v.Name, v.ChannelID, v.TemplateID = strings.TrimSpace(v.ID), strings.TrimSpace(v.Name), strings.TrimSpace(v.ChannelID), strings.TrimSpace(v.TemplateID)
	if strings.HasPrefix(v.ID, "legacy-task:") {
		return errors.New("legacy compatibility tasks are managed by webhook compatibility APIs")
	}
	if v.ID == "" || v.Name == "" || v.ChannelID == "" || v.TemplateID == "" {
		return errors.New("task id, name, channel_id and template_id are required")
	}
	var eventTypes []string
	var severities []string
	if v.EventTypesJSON == "" {
		v.EventTypesJSON = `["*"]`
	}
	if v.SeveritiesJSON == "" {
		v.SeveritiesJSON = `["P1","P2","P3","P4"]`
	}
	if v.PolicyJSON == "" {
		v.PolicyJSON = "{}"
	}
	if err := json.Unmarshal([]byte(v.EventTypesJSON), &eventTypes); err != nil || len(eventTypes) == 0 {
		return errors.New("task event_types_json must be a non-empty JSON string array")
	}
	if err := json.Unmarshal([]byte(v.SeveritiesJSON), &severities); err != nil || len(severities) == 0 {
		return errors.New("task severities_json must be a non-empty JSON string array")
	}
	for _, severity := range severities {
		if severity != "P1" && severity != "P2" && severity != "P3" && severity != "P4" && severity != "*" {
			return fmt.Errorf("invalid task severity filter %q", severity)
		}
	}
	for _, eventType := range eventTypes {
		if strings.TrimSpace(eventType) == "" {
			return errors.New("task event types cannot contain empty values")
		}
	}
	if _, err := ParseNotificationTaskPolicy(v.PolicyJSON); err != nil {
		return err
	}
	channel, err := s.GetNotificationChannel(v.ChannelID)
	if err != nil {
		return fmt.Errorf("task channel unavailable: %w", err)
	}
	if v.Enabled && !channel.Enabled {
		return errors.New("task cannot be enabled while its channel is disabled")
	}
	if channel.Type == "qq_official_bot" && v.Enabled {
		return errors.New("QQ official bot cannot be enabled as a proactive alert destination")
	}
	template, err := s.GetNotificationTemplate(v.TemplateID)
	if err != nil {
		return fmt.Errorf("task template unavailable: %w", err)
	}
	policy, err := ParseNotificationTaskPolicy(v.PolicyJSON)
	if err != nil {
		return err
	}
	if len(policy.RecoveryEventTypes) > 0 && !containsString(policy.RecoveryEventTypes, template.EventType) {
		return errors.New("recovery_event_types must include the task template event_type")
	}
	if !template.Enabled && v.Enabled {
		return errors.New("task cannot be enabled with a disabled template")
	}
	if template.EventType != "*" && !containsString(eventTypes, template.EventType) {
		return errors.New("task event filter must include its template event_type")
	}
	if template.Severity != "*" && !containsString(severities, template.Severity) {
		return errors.New("task severity filter must include its template severity")
	}
	now := time.Now().Unix()
	if v.CreatedAt == 0 {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	_, err = s.DB.Exec(`INSERT INTO notification_tasks(id,name,channel_id,template_id,enabled,event_types_json,severities_json,policy_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,channel_id=excluded.channel_id,template_id=excluded.template_id,enabled=excluded.enabled,event_types_json=excluded.event_types_json,severities_json=excluded.severities_json,policy_json=excluded.policy_json,updated_at=excluded.updated_at`, v.ID, v.Name, v.ChannelID, v.TemplateID, boolInt(v.Enabled), v.EventTypesJSON, v.SeveritiesJSON, v.PolicyJSON, v.CreatedAt, v.UpdatedAt)
	return err
}

func (s *Store) DisableNotificationTask(id string) error {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "legacy-task:") {
		return errors.New("legacy compatibility tasks are managed by webhook compatibility APIs")
	}
	result, err := s.DB.Exec(`UPDATE notification_tasks SET enabled=0,updated_at=? WHERE id=?`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetNotificationChannelForDelivery includes encrypted credentials for the
// sender path; ordinary list/read methods never return ciphertext.
func (s *Store) GetNotificationChannelForDelivery(id string) (NotificationChannel, error) {
	v, err := s.GetNotificationChannel(id)
	if err != nil {
		return v, err
	}
	if v.LegacyWebhookID == "" {
		if err := s.DB.QueryRow(`SELECT secrets_ciphertext FROM notification_channels WHERE id=?`, id).Scan(&v.SecretsCiphertext); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (s *Store) RecordNotificationChannelTestResult(id, status string, httpStatus int, failure string) error {
	now := time.Now().Unix()
	if len(failure) > 512 {
		failure = failure[:512]
	}
	_, err := s.DB.Exec(`UPDATE notification_channels SET last_attempt_at=?,last_success_at=CASE WHEN ?='success' THEN ? ELSE last_success_at END,last_status=?,last_http_status=?,consecutive_failures=CASE WHEN ?='success' THEN 0 WHEN ?='unsupported' THEN consecutive_failures ELSE consecutive_failures+1 END,last_error=?,updated_at=? WHERE id=? AND COALESCE(legacy_webhook_id,'')=''`, now, status, now, status, httpStatus, status, status, failure, now, id)
	return err
}
