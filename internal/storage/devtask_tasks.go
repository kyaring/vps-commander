package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultDevTaskLeaseSec is the claim lease length when the caller does not specify one.
const DefaultDevTaskLeaseSec = 120

// MaxDevTaskEventPayloadBytes is the per-event payload truncation limit (spec §3).
const MaxDevTaskEventPayloadBytes = 8 * 1024

// MaxDevTaskLeaseRequeues is how many times an expired lease may requeue before failing.
const MaxDevTaskLeaseRequeues = 2

// ErrDevTaskConflict is returned when a claim/heartbeat/complete loses a race or ownership check.
var ErrDevTaskConflict = errors.New("devtask conflict")

// DevTask is one row of dev_tasks.
type DevTask struct {
	ID                 string
	Project            string
	Title              string
	SpecMD             string
	AcceptCmdsJSON     string
	AllowedPathsJSON   string
	ForbiddenPathsJSON string
	BaseRef            string
	BaseSHA            string
	Branch             string
	Model              string
	Status             string
	Round              int
	MaxRounds          int
	MaxSteps           int
	TimeoutS           int
	ParentTaskID       string
	CommitSHA          string
	DiffStat           string
	TestSummary        string
	ResultMD           string
	ErrorCode          string
	ErrorDetail        string
	ActorName          string
	OAuthClientID      string
	ClaimedBy          string
	LeaseUntil         int64
	HeartbeatAt        int64
	LeaseRequeues      int
	CreatedAt          int64
	StartedAt          int64
	FinishedAt         int64
}

// DevTaskEvent is one step log row.
type DevTaskEvent struct {
	TaskID      string
	Seq         int
	TS          int64
	Round       int
	Type        string
	PayloadJSON string
}

const devTaskColumns = `id,project,title,spec_md,accept_cmds_json,allowed_paths_json,forbidden_paths_json,
	base_ref,base_sha,branch,model,status,round,max_rounds,max_steps,timeout_s,parent_task_id,
	commit_sha,diff_stat,test_summary,result_md,error_code,error_detail,actor_name,oauth_client_id,
	claimed_by,lease_until,heartbeat_at,lease_requeues,created_at,started_at,finished_at`

func scanDevTask(sc interface{ Scan(...any) error }) (DevTask, error) {
	var t DevTask
	var parent sql.NullString
	err := sc.Scan(
		&t.ID, &t.Project, &t.Title, &t.SpecMD, &t.AcceptCmdsJSON, &t.AllowedPathsJSON, &t.ForbiddenPathsJSON,
		&t.BaseRef, &t.BaseSHA, &t.Branch, &t.Model, &t.Status, &t.Round, &t.MaxRounds, &t.MaxSteps, &t.TimeoutS, &parent,
		&t.CommitSHA, &t.DiffStat, &t.TestSummary, &t.ResultMD, &t.ErrorCode, &t.ErrorDetail, &t.ActorName, &t.OAuthClientID,
		&t.ClaimedBy, &t.LeaseUntil, &t.HeartbeatAt, &t.LeaseRequeues, &t.CreatedAt, &t.StartedAt, &t.FinishedAt,
	)
	if parent.Valid {
		t.ParentTaskID = parent.String
	}
	return t, err
}

// ensureDevTaskLeaseColumn adds lease_requeues if missing (idempotent for D1 databases).
func (s *Store) ensureDevTaskLeaseColumn() error {
	rows, err := s.DB.Query(`PRAGMA table_info(dev_tasks)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	has := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "lease_requeues" {
			has = true
		}
	}
	if has {
		return rows.Err()
	}
	_, err = s.DB.Exec(`ALTER TABLE dev_tasks ADD COLUMN lease_requeues INTEGER NOT NULL DEFAULT 0`)
	return err
}

// CreateDevTask inserts a queued task. ID, Branch, Model and limits must already be set by the caller.
func (s *Store) CreateDevTask(t DevTask) error {
	if err := s.ensureDevTaskLeaseColumn(); err != nil {
		return err
	}
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Project) == "" || strings.TrimSpace(t.Title) == "" {
		return errors.New("id, project and title required")
	}
	if t.Status == "" {
		t.Status = DevTaskQueued
	}
	if t.Round < 1 {
		t.Round = 1
	}
	if t.AcceptCmdsJSON == "" {
		t.AcceptCmdsJSON = "[]"
	}
	if t.AllowedPathsJSON == "" {
		t.AllowedPathsJSON = "[]"
	}
	if t.ForbiddenPathsJSON == "" {
		t.ForbiddenPathsJSON = "[]"
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().Unix()
	}
	var parent any
	if t.ParentTaskID != "" {
		parent = t.ParentTaskID
	}
	_, err := s.DB.Exec(`INSERT INTO dev_tasks(`+devTaskColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Project, t.Title, t.SpecMD, t.AcceptCmdsJSON, t.AllowedPathsJSON, t.ForbiddenPathsJSON,
		t.BaseRef, t.BaseSHA, t.Branch, t.Model, t.Status, t.Round, t.MaxRounds, t.MaxSteps, t.TimeoutS, parent,
		t.CommitSHA, t.DiffStat, t.TestSummary, t.ResultMD, t.ErrorCode, t.ErrorDetail, t.ActorName, t.OAuthClientID,
		t.ClaimedBy, t.LeaseUntil, t.HeartbeatAt, t.LeaseRequeues, t.CreatedAt, t.StartedAt, t.FinishedAt)
	return err
}

// GetDevTask returns one task by id.
func (s *Store) GetDevTask(id string) (DevTask, error) {
	if err := s.ensureDevTaskLeaseColumn(); err != nil {
		return DevTask{}, err
	}
	t, err := scanDevTask(s.DB.QueryRow(`SELECT `+devTaskColumns+` FROM dev_tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DevTask{}, ErrDevTaskNotFound
	}
	return t, err
}

// ListDevTasks filters by optional project and status with limit/offset.
func (s *Store) ListDevTasks(project, status string, limit, offset int) ([]DevTask, error) {
	if err := s.ensureDevTaskLeaseColumn(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + devTaskColumns + ` FROM dev_tasks WHERE 1=1`
	var args []any
	if project != "" {
		q += ` AND project=?`
		args = append(args, project)
	}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DevTask
	for rows.Next() {
		t, err := scanDevTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClaimDevTask atomically takes one claimable task for claimedBy and extends the lease.
// Claimable = queued, or running with lease_until < now.
func (s *Store) ClaimDevTask(claimedBy string, leaseSec int) (DevTask, error) {
	if err := s.ensureDevTaskLeaseColumn(); err != nil {
		return DevTask{}, err
	}
	if strings.TrimSpace(claimedBy) == "" {
		return DevTask{}, errors.New("claimed_by required")
	}
	if leaseSec <= 0 {
		leaseSec = DefaultDevTaskLeaseSec
	}
	now := time.Now().Unix()
	leaseUntil := now + int64(leaseSec)

	tx, err := s.DB.Begin()
	if err != nil {
		return DevTask{}, err
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRow(`SELECT id FROM dev_tasks WHERE status=? OR (status=? AND lease_until>0 AND lease_until<?)
		ORDER BY created_at ASC LIMIT 1`, DevTaskQueued, DevTaskRunning, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return DevTask{}, ErrDevTaskNotFound
	}
	if err != nil {
		return DevTask{}, err
	}

	t, err := scanDevTask(tx.QueryRow(`SELECT `+devTaskColumns+` FROM dev_tasks WHERE id=?`, id))
	if err != nil {
		return DevTask{}, err
	}

	requeues := t.LeaseRequeues
	if t.Status == DevTaskRunning && t.LeaseUntil > 0 && t.LeaseUntil < now {
		requeues++
		if requeues > MaxDevTaskLeaseRequeues {
			_, err = tx.Exec(`UPDATE dev_tasks SET status=?, error_code=?, error_detail=?, finished_at=?, claimed_by='', lease_until=0, heartbeat_at=0, lease_requeues=? WHERE id=? AND status=?`,
				DevTaskFailed, "lease_expired", fmt.Sprintf("lease requeued %d times", requeues), now, requeues, id, DevTaskRunning)
			if err != nil {
				return DevTask{}, err
			}
			if err := tx.Commit(); err != nil {
				return DevTask{}, err
			}
			return DevTask{}, ErrDevTaskNotFound
		}
	}

	started := t.StartedAt
	if started == 0 {
		started = now
	}
	res, err := tx.Exec(`UPDATE dev_tasks SET status=?, claimed_by=?, lease_until=?, heartbeat_at=?, started_at=?, lease_requeues=?
		WHERE id=? AND (status=? OR (status=? AND lease_until>0 AND lease_until<?))`,
		DevTaskRunning, claimedBy, leaseUntil, now, started, requeues, id, DevTaskQueued, DevTaskRunning, now)
	if err != nil {
		return DevTask{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return DevTask{}, ErrDevTaskConflict
	}
	if err := tx.Commit(); err != nil {
		return DevTask{}, err
	}
	return s.GetDevTask(id)
}

// HeartbeatDevTask extends the lease when the caller still owns a running task.
func (s *Store) HeartbeatDevTask(id, claimedBy string, leaseSec int) error {
	if leaseSec <= 0 {
		leaseSec = DefaultDevTaskLeaseSec
	}
	now := time.Now().Unix()
	res, err := s.DB.Exec(`UPDATE dev_tasks SET lease_until=?, heartbeat_at=? WHERE id=? AND status=? AND claimed_by=?`,
		now+int64(leaseSec), now, id, DevTaskRunning, claimedBy)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDevTaskConflict
	}
	return nil
}

// CloseAuditForTest marks audit closed so AuditEntry synchronously executes writeAuditEntry.
func (s *Store) CloseAuditForTest() {
	s.auditMu.Lock()
	s.auditClosed = true
	stop, done := s.auditStop, s.auditDone
	if stop != nil {
		close(stop)
	}
	s.auditMu.Unlock()
	if done != nil {
		<-done
	}
}

// CompleteDevTaskResult is the runner completion payload.
type CompleteDevTaskResult struct {
	Status      string
	CommitSHA   string
	DiffStat    string
	TestSummary string
	ResultMD    string
	ErrorCode   string
	ErrorDetail string
}

// CompleteDevTask moves a running task owned by claimedBy to a terminal runner status.
func (s *Store) CompleteDevTask(id, claimedBy string, r CompleteDevTaskResult) error {
	switch r.Status {
	case DevTaskNeedsReview, DevTaskFailed, DevTaskBlockedModel:
	default:
		return fmt.Errorf("%w: complete status %q", ErrDevTaskIllegalTransition, r.Status)
	}
	now := time.Now().Unix()
	res, err := s.DB.Exec(`UPDATE dev_tasks SET status=?, commit_sha=?, diff_stat=?, test_summary=?, result_md=?,
		error_code=?, error_detail=?, finished_at=?, claimed_by='', lease_until=0, heartbeat_at=0
		WHERE id=? AND status=? AND claimed_by=?`,
		r.Status, r.CommitSHA, r.DiffStat, r.TestSummary, r.ResultMD, r.ErrorCode, r.ErrorDetail, now,
		id, DevTaskRunning, claimedBy)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDevTaskConflict
	}
	return nil
}

// CancelDevTask moves any non-terminal task to cancelled.
func (s *Store) CancelDevTask(id string) error {
	now := time.Now().Unix()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM dev_tasks WHERE id=?`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDevTaskNotFound
		}
		return err
	}
	if !CanTransitionDevTask(status, DevTaskCancelled) {
		return fmt.Errorf("%w: %s -> cancelled", ErrDevTaskIllegalTransition, status)
	}
	res, err := tx.Exec(`UPDATE dev_tasks SET status=?, finished_at=?, claimed_by='', lease_until=0, heartbeat_at=0 WHERE id=? AND status=?`,
		DevTaskCancelled, now, id, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDevTaskConflict
	}
	return tx.Commit()
}

// ReviseDevTask returns needs_review or failed tasks to queued and increments round.
func (s *Store) ReviseDevTask(id string) (DevTask, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return DevTask{}, err
	}
	defer tx.Rollback()
	t, err := scanDevTask(tx.QueryRow(`SELECT `+devTaskColumns+` FROM dev_tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DevTask{}, ErrDevTaskNotFound
	}
	if err != nil {
		return DevTask{}, err
	}
	if !CanTransitionDevTask(t.Status, DevTaskQueued) {
		return t, fmt.Errorf("%w: %s -> queued", ErrDevTaskIllegalTransition, t.Status)
	}
	if t.Round >= t.MaxRounds {
		return t, fmt.Errorf("%w: max_rounds %d reached", ErrDevTaskConflict, t.MaxRounds)
	}
	res, err := tx.Exec(`UPDATE dev_tasks SET status=?, round=round+1, commit_sha='', diff_stat='', test_summary='', result_md='',
		error_code='', error_detail='', finished_at=0, claimed_by='', lease_until=0, heartbeat_at=0, lease_requeues=0
		WHERE id=? AND status=?`, DevTaskQueued, id, t.Status)
	if err != nil {
		return DevTask{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return DevTask{}, ErrDevTaskConflict
	}
	if err := tx.Commit(); err != nil {
		return DevTask{}, err
	}
	return s.GetDevTask(id)
}

// MarkDoneDevTask moves needs_review → done.
func (s *Store) MarkDoneDevTask(id string) error {
	now := time.Now().Unix()
	res, err := s.DB.Exec(`UPDATE dev_tasks SET status=?, finished_at=? WHERE id=? AND status=?`,
		DevTaskDone, now, id, DevTaskNeedsReview)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// Distinguish not found vs illegal.
		if _, err := s.GetDevTask(id); errors.Is(err, ErrDevTaskNotFound) {
			return ErrDevTaskNotFound
		}
		return fmt.Errorf("%w: not needs_review", ErrDevTaskIllegalTransition)
	}
	return nil
}

var allowedEventTypes = map[string]bool{
	"model_call": true, "tool_call": true, "cmd": true, "status": true, "model_error": true, "note": true,
}

// AppendDevTaskEvents appends events with sequential seq values; payloads longer than 8KiB are truncated.
func (s *Store) AppendDevTaskEvents(taskID string, round int, events []DevTaskEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM dev_task_events WHERE task_id=?`, taskID).Scan(&maxSeq); err != nil {
		return err
	}
	seq := 0
	if maxSeq.Valid {
		seq = int(maxSeq.Int64)
	}
	now := time.Now().Unix()
	for _, e := range events {
		if !allowedEventTypes[e.Type] {
			return fmt.Errorf("invalid event type %q", e.Type)
		}
		payload := e.PayloadJSON
		if payload == "" {
			payload = "{}"
		}
		if len(payload) > MaxDevTaskEventPayloadBytes {
			payload = payload[:MaxDevTaskEventPayloadBytes]
		}
		seq++
		ts := e.TS
		if ts == 0 {
			ts = now
		}
		r := round
		if e.Round > 0 {
			r = e.Round
		}
		if _, err := tx.Exec(`INSERT INTO dev_task_events(task_id,seq,ts,round,type,payload_json) VALUES(?,?,?,?,?,?)`,
			taskID, seq, ts, r, e.Type, payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListDevTaskEvents returns events with seq > afterSeq, ordered by seq, limited.
func (s *Store) ListDevTaskEvents(taskID string, afterSeq, limit int) ([]DevTaskEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.DB.Query(`SELECT task_id,seq,ts,round,type,payload_json FROM dev_task_events
		WHERE task_id=? AND seq>? ORDER BY seq ASC LIMIT ?`, taskID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DevTaskEvent
	for rows.Next() {
		var e DevTaskEvent
		if err := rows.Scan(&e.TaskID, &e.Seq, &e.TS, &e.Round, &e.Type, &e.PayloadJSON); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
