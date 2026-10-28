package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Task status values (v1.1.36 spec §3). Legal changes are listed in devTaskTransitions.
const (
	DevTaskQueued       = "queued"
	DevTaskRunning      = "running"
	DevTaskNeedsReview  = "needs_review"
	DevTaskFailed       = "failed"
	DevTaskBlockedModel = "blocked_model"
	DevTaskCancelled    = "cancelled"
	DevTaskDone         = "done"
)

// DevTaskAllowedModelsKey is the hub_settings key holding the model whitelist (JSON array of strings).
const DevTaskAllowedModelsKey = "devtask.allowed_models"

// DevTaskDefaultModel is the only model in the initial whitelist.
const DevTaskDefaultModel = "global:deepseek-v4.1-flash"

var (
	// ErrDevTaskIllegalTransition is returned for any status change not in devTaskTransitions.
	ErrDevTaskIllegalTransition = errors.New("illegal devtask status transition")
	// ErrDevTaskNotFound is returned when the task id does not exist.
	ErrDevTaskNotFound = errors.New("devtask not found")
)

// devTaskTransitions is the complete set of legal status changes. Any pair not listed is illegal.
//   - running → queued is the lease-expiry requeue.
//   - failed → queued is the revise path (dev_task_revise is valid for failed tasks).
//   - cancel is legal from every state that has not ended; done and cancelled are final.
var devTaskTransitions = map[string]map[string]bool{
	DevTaskQueued:       {DevTaskRunning: true, DevTaskCancelled: true},
	DevTaskRunning:      {DevTaskNeedsReview: true, DevTaskFailed: true, DevTaskBlockedModel: true, DevTaskQueued: true, DevTaskCancelled: true},
	DevTaskNeedsReview:  {DevTaskQueued: true, DevTaskDone: true, DevTaskCancelled: true},
	DevTaskFailed:       {DevTaskQueued: true, DevTaskCancelled: true},
	DevTaskBlockedModel: {DevTaskCancelled: true},
	DevTaskDone:         {},
	DevTaskCancelled:    {},
}

// ValidDevTaskStatus reports whether s is one of the seven status values.
func ValidDevTaskStatus(s string) bool {
	_, ok := devTaskTransitions[s]
	return ok
}

// CanTransitionDevTask reports whether a task may move from one status to another.
func CanTransitionDevTask(from, to string) bool {
	return devTaskTransitions[from][to]
}

// devTaskSchema creates the three devtask tables and their indexes. Every statement is
// IF NOT EXISTS, so running it again on an existing database changes nothing.
const devTaskSchema = `
CREATE TABLE IF NOT EXISTS dev_projects (
	name TEXT PRIMARY KEY,
	repo_path TEXT NOT NULL,
	base_ref TEXT NOT NULL DEFAULT 'main',
	default_model TEXT NOT NULL,
	allowed_cmds_json TEXT NOT NULL DEFAULT '[]',
	default_accept_cmds_json TEXT NOT NULL DEFAULT '[]',
	forbidden_paths_json TEXT NOT NULL DEFAULT '[]',
	max_rounds INTEGER NOT NULL DEFAULT 3 CHECK(max_rounds >= 1),
	max_steps INTEGER NOT NULL DEFAULT 50 CHECK(max_steps >= 1),
	timeout_s INTEGER NOT NULL DEFAULT 1800 CHECK(timeout_s >= 1),
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS dev_tasks (
	id TEXT PRIMARY KEY,
	project TEXT NOT NULL REFERENCES dev_projects(name),
	title TEXT NOT NULL,
	spec_md TEXT NOT NULL,
	accept_cmds_json TEXT NOT NULL DEFAULT '[]',
	allowed_paths_json TEXT NOT NULL DEFAULT '[]',
	forbidden_paths_json TEXT NOT NULL DEFAULT '[]',
	base_ref TEXT NOT NULL,
	base_sha TEXT NOT NULL DEFAULT '',
	branch TEXT NOT NULL,
	model TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('queued','running','needs_review','failed','blocked_model','cancelled','done')),
	round INTEGER NOT NULL DEFAULT 1 CHECK(round >= 1),
	max_rounds INTEGER NOT NULL CHECK(max_rounds >= 1),
	max_steps INTEGER NOT NULL CHECK(max_steps >= 1),
	timeout_s INTEGER NOT NULL CHECK(timeout_s >= 1),
	parent_task_id TEXT REFERENCES dev_tasks(id),
	commit_sha TEXT NOT NULL DEFAULT '',
	diff_stat TEXT NOT NULL DEFAULT '',
	test_summary TEXT NOT NULL DEFAULT '',
	result_md TEXT NOT NULL DEFAULT '',
	error_code TEXT NOT NULL DEFAULT '',
	error_detail TEXT NOT NULL DEFAULT '',
	actor_name TEXT NOT NULL DEFAULT '',
	oauth_client_id TEXT NOT NULL DEFAULT '',
	claimed_by TEXT NOT NULL DEFAULT '',
	lease_until INTEGER NOT NULL DEFAULT 0,
	heartbeat_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	started_at INTEGER NOT NULL DEFAULT 0,
	finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_dev_tasks_status ON dev_tasks(status, created_at);
CREATE INDEX IF NOT EXISTS idx_dev_tasks_project ON dev_tasks(project, created_at);
CREATE TABLE IF NOT EXISTS dev_task_events (
	task_id TEXT NOT NULL REFERENCES dev_tasks(id) ON DELETE CASCADE,
	seq INTEGER NOT NULL,
	ts INTEGER NOT NULL,
	round INTEGER NOT NULL DEFAULT 1,
	type TEXT NOT NULL CHECK(type IN ('model_call','tool_call','cmd','status','model_error','note')),
	payload_json TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY(task_id, seq)
);
`

// migrateDevTask creates the devtask tables and seeds the model whitelist. It is idempotent:
// the seed uses INSERT OR IGNORE, so a value an administrator has already changed is kept.
func (s *Store) migrateDevTask() error {
	if _, err := s.DB.Exec(devTaskSchema); err != nil {
		return err
	}
	seed, err := json.Marshal([]string{DevTaskDefaultModel})
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT OR IGNORE INTO hub_settings(key,value,updated_at) VALUES(?,?,?)`,
		DevTaskAllowedModelsKey, string(seed), time.Now().Unix())
	return err
}

// TransitionDevTask moves a task to status `to` if that change is legal. It reads the current
// status and writes the new one in a single transaction, with a compare-and-set on the old
// status, so two concurrent callers cannot both succeed from the same state. It returns the
// status the task was in before the call. An illegal change returns ErrDevTaskIllegalTransition
// and leaves the row untouched.
func (s *Store) TransitionDevTask(id, to string) (string, error) {
	if !ValidDevTaskStatus(to) {
		return "", fmt.Errorf("%w: unknown status %q", ErrDevTaskIllegalTransition, to)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var from string
	err = tx.QueryRow(`SELECT status FROM dev_tasks WHERE id=?`, id).Scan(&from)
	if err == sql.ErrNoRows {
		return "", ErrDevTaskNotFound
	}
	if err != nil {
		return "", err
	}
	if !CanTransitionDevTask(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", ErrDevTaskIllegalTransition, from, to)
	}
	res, err := tx.Exec(`UPDATE dev_tasks SET status=? WHERE id=? AND status=?`, to, id, from)
	if err != nil {
		return from, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return from, fmt.Errorf("%w: status changed concurrently", ErrDevTaskIllegalTransition)
	}
	if err := tx.Commit(); err != nil {
		return from, err
	}
	return from, nil
}

// GetDevTaskAllowedModels returns the model whitelist. A missing setting yields an empty list,
// which denies every model: the default is closed, not open.
func (s *Store) GetDevTaskAllowedModels() ([]string, error) {
	var raw string
	err := s.DB.QueryRow(`SELECT value FROM hub_settings WHERE key=?`, DevTaskAllowedModelsKey).Scan(&raw)
	if err == sql.ErrNoRows {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var models []string
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil, fmt.Errorf("%s: %w", DevTaskAllowedModelsKey, err)
	}
	if models == nil {
		models = []string{}
	}
	return models, nil
}

// SetDevTaskAllowedModels replaces the whitelist. Empty names and duplicates are rejected.
// The admin-only check and the audit record belong to the API layer, not to storage.
func (s *Store) SetDevTaskAllowedModels(models []string) error {
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		if m == "" {
			return errors.New("model name must not be empty")
		}
		if seen[m] {
			return fmt.Errorf("duplicate model %q", m)
		}
		seen[m] = true
	}
	if models == nil {
		models = []string{}
	}
	b, err := json.Marshal(models)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO hub_settings(key,value,updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		DevTaskAllowedModelsKey, string(b), time.Now().Unix())
	return err
}
