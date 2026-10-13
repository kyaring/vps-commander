package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrLegacyRetireBadRequest = errors.New("invalid legacy webhook retirement request")
	ErrLegacyRetireNotFound   = errors.New("legacy webhook not found")
	ErrLegacyRetireConflict   = errors.New("notification channel ID is already in use")
)

type LegacyWebhookRetireResult struct {
	ChannelID      string   `json:"channel_id"`
	MovedTasks     []string `json:"moved_tasks"`
	RetiredTask    string   `json:"retired_task"`
	AlreadyRetired bool     `json:"already_retired"`
}

// RetireLegacyWebhook moves a legacy WeCom webhook to encrypted channel secrets
// and removes the plaintext compatibility records atomically.
func (s *Store) RetireLegacyWebhook(legacyID, channelID, name string) (LegacyWebhookRetireResult, error) {
	legacyID, channelID, name = strings.TrimSpace(legacyID), strings.TrimSpace(channelID), strings.TrimSpace(name)
	if legacyID == "" || channelID == "" || name == "" || strings.HasPrefix(channelID, "legacy-") {
		return LegacyWebhookRetireResult{}, ErrLegacyRetireBadRequest
	}
	result := LegacyWebhookRetireResult{ChannelID: channelID, MovedTasks: []string{}, RetiredTask: legacyTaskID(legacyID)}
	tx, err := s.DB.Begin()
	if err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	defer tx.Rollback()

	var oldType, rawURL string
	var enabled int
	var createdAt int64
	err = tx.QueryRow(`SELECT type,url,enabled,created_at FROM webhook_targets WHERE id=?`, legacyID).Scan(&oldType, &rawURL, &enabled, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		var newType, newChannelName, oldEndpoint, oldChannelName string
		var oldEnabled, oldTaskEnabled int
		checkErr := tx.QueryRow(`SELECT type,name FROM notification_channels WHERE id=?`, channelID).Scan(&newType, &newChannelName)
		if checkErr == nil && newType == "wecom_bot" {
			oldCheck := tx.QueryRow(`SELECT type,endpoint,name,enabled FROM notification_channels WHERE id=? AND COALESCE(legacy_webhook_id,'')=?`, legacyChannelID(legacyID), legacyID).Scan(&oldType, &oldEndpoint, &oldChannelName, &oldEnabled)
			taskCheck := tx.QueryRow(`SELECT enabled FROM notification_tasks WHERE id=?`, legacyTaskID(legacyID)).Scan(&oldTaskEnabled)
			if oldCheck == nil && taskCheck == nil && newChannelName == name && oldEndpoint == "" && oldEnabled == 0 && oldTaskEnabled == 0 && strings.HasSuffix(oldChannelName, "（已退役）") {
				result.AlreadyRetired = true
				return result, nil
			}
		}
		return LegacyWebhookRetireResult{}, ErrLegacyRetireNotFound
	}
	if err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	if !validWeComWebhookURL(rawURL) {
		return LegacyWebhookRetireResult{}, ErrLegacyRetireBadRequest
	}
	var occupied string
	err = tx.QueryRow(`SELECT type FROM notification_channels WHERE id=?`, channelID).Scan(&occupied)
	if err == nil {
		return LegacyWebhookRetireResult{}, ErrLegacyRetireConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}

	newSecrets, err := json.Marshal(map[string]string{"webhook_url": rawURL})
	if err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	sealed, err := s.EncryptNotificationChannelSecrets(channelID, newSecrets)
	if err != nil {
		return LegacyWebhookRetireResult{}, errors.New("notification secret encryption unavailable")
	}
	now := time.Now().Unix()
	_, err = tx.Exec(`INSERT INTO notification_channels(id,name,type,endpoint,config_json,secrets_ciphertext,enabled,created_at,updated_at,last_status,legacy_webhook_id,legacy_type) VALUES(?,?,'wecom_bot','',?, ?,?,?,?,'unknown',NULL,'')`, channelID, name, `{"mentioned_list":[]}`, sealed, enabled, createdAt, now)
	if err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}

	rows, err := tx.Query(`SELECT id FROM notification_tasks WHERE channel_id=? AND id NOT LIKE 'legacy-task:%' ORDER BY id`, legacyChannelID(legacyID))
	if err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
		}
		result.MovedTasks = append(result.MovedTasks, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	rows.Close()
	if _, err := tx.Exec(`UPDATE notification_tasks SET channel_id=?,updated_at=? WHERE channel_id=? AND id NOT LIKE 'legacy-task:%'`, channelID, now, legacyChannelID(legacyID)); err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	if _, err := tx.Exec(`UPDATE notification_tasks SET enabled=0,updated_at=? WHERE id=?`, now, legacyTaskID(legacyID)); err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	if _, err := tx.Exec(`UPDATE notification_channels SET enabled=0,endpoint='',name=CASE WHEN substr(name,-5)='（已退役）' THEN name ELSE name || '（已退役）' END,config_json='{}',updated_at=? WHERE id=? AND COALESCE(legacy_webhook_id,'')=?`, now, legacyChannelID(legacyID), legacyID); err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	if _, err := tx.Exec(`UPDATE notification_deliveries SET status='failed',last_error='legacy webhook retired',next_attempt_at=0,updated_at=? WHERE task_id=? AND status IN ('pending','sending')`, now, legacyTaskID(legacyID)); err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	if _, err := tx.Exec(`DELETE FROM webhook_targets WHERE id=?`, legacyID); err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	if _, err := tx.Exec(`DELETE FROM webhook_delivery_state WHERE target_id=?`, legacyID); err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	if err := tx.Commit(); err != nil {
		return LegacyWebhookRetireResult{}, errors.New("legacy webhook retirement failed")
	}
	return result, nil
}
