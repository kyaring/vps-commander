package storage

import (
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// specLegal is the legal-transition set written out by hand from v1.1.36 spec §3
// (plus the revise path for failed tasks in §4). It is deliberately NOT derived from
// devTaskTransitions: if the implementation gains or loses an edge, the tests below
// disagree with it instead of quietly adopting it.
var specLegal = map[[2]string]bool{
	{DevTaskQueued, DevTaskRunning}:         true,
	{DevTaskQueued, DevTaskCancelled}:       true,
	{DevTaskRunning, DevTaskNeedsReview}:    true,
	{DevTaskRunning, DevTaskFailed}:         true,
	{DevTaskRunning, DevTaskBlockedModel}:   true,
	{DevTaskRunning, DevTaskQueued}:         true,
	{DevTaskRunning, DevTaskCancelled}:      true,
	{DevTaskNeedsReview, DevTaskQueued}:     true,
	{DevTaskNeedsReview, DevTaskDone}:       true,
	{DevTaskNeedsReview, DevTaskCancelled}:  true,
	{DevTaskFailed, DevTaskQueued}:          true,
	{DevTaskFailed, DevTaskCancelled}:       true,
	{DevTaskBlockedModel, DevTaskCancelled}: true,
}

var allDevTaskStatuses = []string{DevTaskQueued, DevTaskRunning, DevTaskNeedsReview, DevTaskFailed, DevTaskBlockedModel, DevTaskCancelled, DevTaskDone}

func openDevTaskStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s, path
}

func seedDevProject(t *testing.T, s *Store, name string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := s.DB.Exec(`INSERT INTO dev_projects(name,repo_path,base_ref,default_model,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		name, "/srv/repos/"+name, "main", DevTaskDefaultModel, now, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func seedDevTask(t *testing.T, s *Store, id, status string) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT INTO dev_tasks(id,project,title,spec_md,base_ref,branch,model,status,max_rounds,max_steps,timeout_s,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, "proj", "t", "spec", "main", "task/"+id, DevTaskDefaultModel, status, 3, 50, 1800, time.Now().Unix()); err != nil {
		t.Fatalf("seed task %s/%s: %v", id, status, err)
	}
}

func devTaskStatus(t *testing.T, s *Store, id string) string {
	t.Helper()
	var st string
	if err := s.DB.QueryRow(`SELECT status FROM dev_tasks WHERE id=?`, id).Scan(&st); err != nil {
		t.Fatalf("read status %s: %v", id, err)
	}
	return st
}

func tableNames(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// 要求 5a: the three tables exist, and running the migration again (and reopening) is harmless.
func TestDevTaskTablesCreatedAndMigrationIdempotent(t *testing.T) {
	s, path := openDevTaskStore(t)
	for _, want := range []string{"dev_projects", "dev_tasks", "dev_task_events"} {
		found := false
		for _, n := range tableNames(t, s) {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("table %s missing", want)
		}
	}
	if err := s.migrateDevTask(); err != nil {
		t.Fatalf("second migrateDevTask: %v", err)
	}
	if err := s.migrateDevTask(); err != nil {
		t.Fatalf("third migrateDevTask: %v", err)
	}
	s.DB.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.DB.Close()
	if err := s2.migrateDevTask(); err != nil {
		t.Fatalf("migrate after reopen: %v", err)
	}
}

// 要求 5b: rows already in the database survive the devtask migration.
func TestDevTaskMigrationKeepsExistingData(t *testing.T) {
	s, path := openDevTaskStore(t)
	if err := s.SetLegacyKeyDisabled(true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO resource_alert_states(device,metric,updated_at) VALUES('node-a','cpu',123)`); err != nil {
		t.Fatal(err)
	}
	dump := func(st *Store) []string {
		var out []string
		for _, q := range []string{
			`SELECT key||'='||value FROM hub_settings WHERE key<>'devtask.allowed_models' ORDER BY key`,
			`SELECT device||'/'||metric||'/'||updated_at FROM resource_alert_states ORDER BY device,metric`,
		} {
			rows, err := st.DB.Query(q)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					t.Fatal(err)
				}
				out = append(out, v)
			}
			rows.Close()
		}
		sort.Strings(out)
		return out
	}
	before := dump(s)
	s.DB.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.DB.Close()
	after := dump(s2)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("existing rows changed:\nbefore=%v\nafter =%v", before, after)
	}
	if len(before) == 0 {
		t.Fatal("test seeded no rows; the check would be vacuous")
	}
}

// 要求 2a: every legal transition (from the spec set) succeeds and the stored status follows it.
func TestDevTaskLegalTransitionsAccepted(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedDevProject(t, s, "proj")
	n := 0
	for tr := range specLegal {
		n++
		id := "legal-" + tr[0] + "-" + tr[1]
		seedDevTask(t, s, id, tr[0])
		from, err := s.TransitionDevTask(id, tr[1])
		if err != nil {
			t.Fatalf("%s -> %s: unexpected error: %v", tr[0], tr[1], err)
		}
		if from != tr[0] {
			t.Fatalf("returned from = %q, want %q", from, tr[0])
		}
		if got := devTaskStatus(t, s, id); got != tr[1] {
			t.Fatalf("%s -> %s: stored status = %q", tr[0], tr[1], got)
		}
	}
	if n != len(specLegal) {
		t.Fatalf("checked %d of %d legal pairs", n, len(specLegal))
	}
}

// 要求 2a': the implementation's transition table matches the spec set exactly, in both directions.
func TestDevTaskTransitionMatrixMatchesSpec(t *testing.T) {
	for _, from := range allDevTaskStatuses {
		for _, to := range allDevTaskStatuses {
			want := specLegal[[2]string{from, to}]
			if got := CanTransitionDevTask(from, to); got != want {
				t.Fatalf("%s -> %s: implementation=%v spec=%v", from, to, got, want)
			}
		}
	}
}

// 要求 2b: every illegal transition (full 7x7 matrix minus the legal set) is rejected and the row is untouched.
func TestDevTaskIllegalTransitionsRejected(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedDevProject(t, s, "proj")
	checked := 0
	for _, from := range allDevTaskStatuses {
		for _, to := range allDevTaskStatuses {
			if specLegal[[2]string{from, to}] {
				continue
			}
			checked++
			id := "illegal-" + from + "-" + to
			seedDevTask(t, s, id, from)
			_, err := s.TransitionDevTask(id, to)
			if !errors.Is(err, ErrDevTaskIllegalTransition) {
				t.Fatalf("%s -> %s: err = %v, want ErrDevTaskIllegalTransition", from, to, err)
			}
			if got := devTaskStatus(t, s, id); got != from {
				t.Fatalf("%s -> %s: row changed to %q", from, to, got)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no illegal pairs were checked")
	}
}

// 要求 2c: unknown target, unknown task and a self-loop are rejected.
func TestDevTaskTransitionEdgeCases(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedDevProject(t, s, "proj")
	if _, err := s.TransitionDevTask("missing", DevTaskRunning); !errors.Is(err, ErrDevTaskNotFound) {
		t.Fatalf("unknown task: err = %v, want ErrDevTaskNotFound", err)
	}
	seedDevTask(t, s, "t-edge", DevTaskQueued)
	if _, err := s.TransitionDevTask("t-edge", "bogus"); !errors.Is(err, ErrDevTaskIllegalTransition) {
		t.Fatalf("unknown status: err = %v, want ErrDevTaskIllegalTransition", err)
	}
	if _, err := s.TransitionDevTask("t-edge", DevTaskQueued); !errors.Is(err, ErrDevTaskIllegalTransition) {
		t.Fatalf("self loop: err = %v, want ErrDevTaskIllegalTransition", err)
	}
	if got := devTaskStatus(t, s, "t-edge"); got != DevTaskQueued {
		t.Fatalf("status changed to %q", got)
	}
}

// 要求 2d: the database itself refuses a status outside the seven values.
func TestDevTaskStatusCheckConstraint(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedDevProject(t, s, "proj")
	_, err := s.DB.Exec(`INSERT INTO dev_tasks(id,project,title,spec_md,base_ref,branch,model,status,max_rounds,max_steps,timeout_s,created_at)
		VALUES('bad','proj','t','s','main','task/bad','m','weird',3,50,1800,1)`)
	if err == nil {
		t.Fatal("insert with unknown status succeeded")
	}
}

// 要求 3: the initial whitelist is exactly the default model.
func TestDevTaskAllowedModelsInitialValue(t *testing.T) {
	s, _ := openDevTaskStore(t)
	got, err := s.GetDevTaskAllowedModels()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"global:deepseek-v4.1-flash"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed_models = %v, want %v", got, want)
	}
}

// 要求 3b: an administrator's change survives a later migration run (the seed never overwrites).
func TestDevTaskAllowedModelsSurvivesRerun(t *testing.T) {
	s, _ := openDevTaskStore(t)
	if err := s.SetDevTaskAllowedModels([]string{"global:deepseek-v4.1-flash", "global:other-model"}); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateDevTask(); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDevTaskAllowedModels()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"global:deepseek-v4.1-flash", "global:other-model"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after rerun allowed_models = %v, want %v", got, want)
	}
}

// 要求 3c: writes validate their input.
func TestDevTaskAllowedModelsRejectsBadInput(t *testing.T) {
	s, _ := openDevTaskStore(t)
	if err := s.SetDevTaskAllowedModels([]string{""}); err == nil {
		t.Fatal("empty model name accepted")
	}
	if err := s.SetDevTaskAllowedModels([]string{"a", "a"}); err == nil {
		t.Fatal("duplicate model accepted")
	}
}
