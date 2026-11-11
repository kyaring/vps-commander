package storage

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func seedProject(t *testing.T, s *Store) {
	t.Helper()
	if err := s.UpsertDevProject(DevProject{
		Name: "demo", RepoPath: "/tmp/demo", BaseRef: "main", DefaultModel: DevTaskDefaultModel,
		AllowedCmdsJSON: `[]`, DefaultAcceptCmdsJSON: `["go test"]`, ForbiddenPathsJSON: `[]`,
		MaxRounds: 3, MaxSteps: 20, TimeoutS: 600, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func sampleTask(id string) DevTask {
	return DevTask{
		ID: id, Project: "demo", Title: "t", SpecMD: "# goal",
		AcceptCmdsJSON: `["go test"]`, AllowedPathsJSON: `[]`, ForbiddenPathsJSON: `[]`,
		BaseRef: "main", Branch: "task/" + id, Model: DevTaskDefaultModel,
		Status: DevTaskQueued, Round: 1, MaxRounds: 3, MaxSteps: 20, TimeoutS: 600,
	}
}

func TestDevTaskCreateAndGet(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	task := sampleTask("dt_a1")
	if err := s.CreateDevTask(task); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDevTask("dt_a1")
	if err != nil || got.Title != "t" || got.Status != DevTaskQueued || got.Branch != "task/dt_a1" {
		t.Fatalf("get = %+v, %v", got, err)
	}
}

func TestDevTaskListFilters(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	_ = s.CreateDevTask(sampleTask("dt_l1"))
	t2 := sampleTask("dt_l2")
	t2.Status = DevTaskRunning
	_ = s.CreateDevTask(t2)
	all, err := s.ListDevTasks("demo", "", 10, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%d err=%v", len(all), err)
	}
	running, err := s.ListDevTasks("demo", DevTaskRunning, 10, 0)
	if err != nil || len(running) != 1 || running[0].ID != "dt_l2" {
		t.Fatalf("running=%+v err=%v", running, err)
	}
}

func TestDevTaskClaimHeartbeatCompleteHappyPath(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	_ = s.CreateDevTask(sampleTask("dt_c1"))
	got, err := s.ClaimDevTask("runner-1", 60)
	if err != nil || got.Status != DevTaskRunning || got.ClaimedBy != "runner-1" || got.LeaseUntil == 0 {
		t.Fatalf("claim=%+v err=%v", got, err)
	}
	if err := s.HeartbeatDevTask("dt_c1", "runner-1", 90); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDevTask("dt_c1", "runner-1", CompleteDevTaskResult{Status: DevTaskNeedsReview, CommitSHA: "abc", ResultMD: "ok"}); err != nil {
		t.Fatal(err)
	}
	final, _ := s.GetDevTask("dt_c1")
	if final.Status != DevTaskNeedsReview || final.CommitSHA != "abc" || final.ClaimedBy != "" {
		t.Fatalf("final=%+v", final)
	}
}

func TestDevTaskClaimExpiredLeaseRequeue(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	task := sampleTask("dt_exp")
	task.Status = DevTaskRunning
	task.ClaimedBy = "old"
	task.LeaseUntil = time.Now().Unix() - 10
	task.LeaseRequeues = 0
	if err := s.CreateDevTask(task); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimDevTask("runner-2", 30)
	if err != nil || got.ClaimedBy != "runner-2" || got.LeaseRequeues != 1 {
		t.Fatalf("reclaim=%+v err=%v", got, err)
	}
}

func TestDevTaskCompleteIllegalStatusRejected(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	_ = s.CreateDevTask(sampleTask("dt_bad"))
	_, _ = s.ClaimDevTask("r", 30)
	if err := s.CompleteDevTask("dt_bad", "r", CompleteDevTaskResult{Status: DevTaskDone}); !errors.Is(err, ErrDevTaskIllegalTransition) {
		t.Fatalf("err=%v", err)
	}
}

func TestDevTaskCancelAndReviseAndDone(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	_ = s.CreateDevTask(sampleTask("dt_flow"))
	_, _ = s.ClaimDevTask("r", 30)
	_ = s.CompleteDevTask("dt_flow", "r", CompleteDevTaskResult{Status: DevTaskNeedsReview})
	rev, err := s.ReviseDevTask("dt_flow")
	if err != nil || rev.Status != DevTaskQueued || rev.Round != 2 {
		t.Fatalf("revise=%+v err=%v", rev, err)
	}
	_, _ = s.ClaimDevTask("r", 30)
	_ = s.CompleteDevTask("dt_flow", "r", CompleteDevTaskResult{Status: DevTaskNeedsReview})
	if err := s.MarkDoneDevTask("dt_flow"); err != nil {
		t.Fatal(err)
	}
	_ = s.CreateDevTask(sampleTask("dt_cancel"))
	if err := s.CancelDevTask("dt_cancel"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetDevTask("dt_cancel")
	if c.Status != DevTaskCancelled {
		t.Fatalf("cancel status=%s", c.Status)
	}
}

func TestDevTaskReviseMaxRounds(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	task := sampleTask("dt_max")
	task.MaxRounds = 1
	_ = s.CreateDevTask(task)
	_, _ = s.ClaimDevTask("r", 30)
	_ = s.CompleteDevTask("dt_max", "r", CompleteDevTaskResult{Status: DevTaskNeedsReview})
	if _, err := s.ReviseDevTask("dt_max"); !errors.Is(err, ErrDevTaskConflict) && err == nil {
		t.Fatalf("expected max_rounds conflict, got %v", err)
	}
}

func TestDevTaskEventsAppendAndList(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	_ = s.CreateDevTask(sampleTask("dt_ev"))
	big := strings.Repeat("x", MaxDevTaskEventPayloadBytes+50)
	if err := s.AppendDevTaskEvents("dt_ev", 1, []DevTaskEvent{
		{Type: "note", PayloadJSON: `{"a":1}`},
		{Type: "cmd", PayloadJSON: big},
	}); err != nil {
		t.Fatal(err)
	}
	ev, err := s.ListDevTaskEvents("dt_ev", 0, 10)
	if err != nil || len(ev) != 2 || ev[0].Seq != 1 || len(ev[1].PayloadJSON) != MaxDevTaskEventPayloadBytes {
		t.Fatalf("events=%+v err=%v", ev, err)
	}
	after, _ := s.ListDevTaskEvents("dt_ev", 1, 10)
	if len(after) != 1 || after[0].Seq != 2 {
		t.Fatalf("after=%+v", after)
	}
}

func TestDevTaskHeartbeatRequiresClaimOwner(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)
	_ = s.CreateDevTask(sampleTask("dt_hb_owner"))
	taskA, err := s.ClaimDevTask("runner-A", 60)
	if err != nil {
		t.Fatalf("claim runner-A failed: %v", err)
	}
	origLeaseUntil := taskA.LeaseUntil

	err = s.HeartbeatDevTask("dt_hb_owner", "runner-B", 120)
	if !errors.Is(err, ErrDevTaskConflict) {
		t.Fatalf("runner-B heartbeat expected ErrDevTaskConflict, got %v", err)
	}

	afterB, err := s.GetDevTask("dt_hb_owner")
	if err != nil {
		t.Fatalf("get dev task failed: %v", err)
	}
	if afterB.ClaimedBy != "runner-A" {
		t.Fatalf("claimed_by = %q, want runner-A", afterB.ClaimedBy)
	}
	if afterB.LeaseUntil != origLeaseUntil {
		t.Fatalf("lease_until modified by runner-B: got %d, want %d", afterB.LeaseUntil, origLeaseUntil)
	}

	time.Sleep(10 * time.Millisecond)
	if err := s.HeartbeatDevTask("dt_hb_owner", "runner-A", 120); err != nil {
		t.Fatalf("runner-A heartbeat failed: %v", err)
	}
	afterA, err := s.GetDevTask("dt_hb_owner")
	if err != nil {
		t.Fatalf("get dev task failed: %v", err)
	}
	if afterA.LeaseUntil <= origLeaseUntil {
		t.Fatalf("lease_until did not advance: got %d, orig %d", afterA.LeaseUntil, origLeaseUntil)
	}
}

func TestDevTaskHeartbeatRejectsNonRunning(t *testing.T) {
	s, _ := openDevTaskStore(t)
	seedProject(t, s)

	qTask := sampleTask("dt_hb_queued")
	qTask.ClaimedBy = DevTaskRunning
	qTask.Status = DevTaskQueued
	_ = s.CreateDevTask(qTask)
	if err := s.HeartbeatDevTask("dt_hb_queued", DevTaskRunning, 60); !errors.Is(err, ErrDevTaskConflict) {
		t.Fatalf("heartbeat on queued task expected ErrDevTaskConflict, got %v", err)
	}

	dTask := sampleTask("dt_hb_done")
	dTask.ClaimedBy = DevTaskRunning
	dTask.Status = DevTaskDone
	_ = s.CreateDevTask(dTask)
	if err := s.HeartbeatDevTask("dt_hb_done", DevTaskRunning, 60); !errors.Is(err, ErrDevTaskConflict) {
		t.Fatalf("heartbeat on done task expected ErrDevTaskConflict, got %v", err)
	}
}
