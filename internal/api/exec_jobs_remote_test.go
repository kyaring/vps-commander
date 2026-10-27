package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/cluster"
)

// fakeAgent stands in for a remote node. It implements the agent behaviour the Hub relies on:
// sessions owned by a token, output and exit state, and a force kill. Nothing here talks to a
// real node.
type fakeAgent struct {
	mu          sync.Mutex
	online      bool
	sessions    map[string]*fakeSession
	forceErr    error // ForceTerminate fails with this error
	ignoreForce bool  // ForceTerminate reports success but the process keeps running
	finishAfter int   // reads after which a session ends by itself (0 = never)
	exitCode    int
	stdout      string
	forced      int
}

type fakeSession struct {
	owner   string
	running bool
	reads   int
	code    int
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{online: true, sessions: map[string]*fakeSession{}}
}

func (f *fakeAgent) Online(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online
}

func (f *fakeAgent) StartProcess(ctx context.Context, name string, p cluster.StartProcessPayload) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.online {
		return nil, errors.New("device offline")
	}
	f.sessions[p.SessionID] = &fakeSession{owner: p.Owner, running: true}
	return map[string]any{"session_id": p.SessionID}, nil
}

func (f *fakeAgent) ReadProcessOutput(ctx context.Context, name, sessionID, owner string) (cluster.ProcessOutputPayload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.online {
		return cluster.ProcessOutputPayload{}, errors.New("device offline")
	}
	s, ok := f.sessions[sessionID]
	if !ok {
		return cluster.ProcessOutputPayload{}, errors.New("session not found")
	}
	if s.owner != owner {
		return cluster.ProcessOutputPayload{}, errors.New("session ownership denied")
	}
	s.reads++
	if s.running && f.finishAfter > 0 && s.reads >= f.finishAfter {
		s.running, s.code = false, f.exitCode
	}
	return cluster.ProcessOutputPayload{SessionID: sessionID, Stdout: f.stdout, ExitCode: s.code, Running: s.running}, nil
}

func (f *fakeAgent) ForceTerminate(ctx context.Context, name, sessionID, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceErr != nil {
		return f.forceErr
	}
	s, ok := f.sessions[sessionID]
	if !ok {
		return errors.New("session not found")
	}
	if s.owner != owner {
		return errors.New("session ownership denied")
	}
	f.forced++
	if f.ignoreForce {
		return nil
	}
	s.running, s.code = false, -9
	return nil
}

// endAll makes every session end, as a process that finally exits would.
func (f *fakeAgent) endAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.running {
			s.running, s.code = false, -1
		}
	}
}

func (f *fakeAgent) setOnline(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online = v
}

// newRemoteTestServer wires a fake node into a test server and speeds up the monitor.
func newRemoteTestServer(t *testing.T, f *fakeAgent) *Server {
	t.Helper()
	s, _ := newSpillTestServer(t)
	if err := s.Store.SetNodeSecurityMode("node-x", "high"); err != nil {
		t.Fatal(err)
	}
	s.remoteProc = f
	m := s.jobsFor()
	if m == nil {
		t.Fatal("no job manager")
	}
	m.tick = 10 * time.Millisecond
	m.confirmWait = 300 * time.Millisecond
	t.Cleanup(f.endAll)
	return s
}

// 1: a remote job goes running -> exited with its exit code and output.
func TestRemoteJobRunningThenExited(t *testing.T) {
	f := newFakeAgent()
	f.finishAfter, f.exitCode, f.stdout = 3, 0, "hi"
	s := newRemoteTestServer(t, f)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "node-x", "echo hi", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	if jobMap(t, v)["status"] != "running" || jobMap(t, v)["max_runtime_s"] != 1800 {
		t.Fatalf("start view = %v", jobMap(t, v))
	}
	m := waitJobStatus(t, b, ctx, id, "exited", 10*time.Second)
	if m["exit_code"] != 0 || m["stdout"] != "hi" {
		t.Fatalf("exited view = %v", m)
	}
	page, err := s.readSpillPage("tok-A", m["spill_path"].(string), 0, 64)
	if err != nil || page.Content != "hi" {
		t.Fatalf("spill = %q, %v", page.Content, err)
	}
}

// 2a: a refused kill is returned as an error and the job stays running.
func TestRemoteCancelKillFailureReturnsErrorAndStaysRunning(t *testing.T) {
	f := newFakeAgent()
	f.forceErr = errors.New("agent refused force_terminate")
	s := newRemoteTestServer(t, f)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "node-x", "sleep 600", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	if _, err := b.CancelExec(ctx, "127.0.0.1", id); err == nil || !strings.Contains(err.Error(), "kill failed") {
		t.Fatalf("cancel err = %v, want kill failed", err)
	}
	st, err := b.ExecStatus(ctx, id)
	if err != nil || jobMap(t, st)["status"] != "running" {
		t.Fatalf("after failed kill: %v %v", jobMap(t, st)["status"], err)
	}
	f.mu.Lock()
	f.forceErr = nil
	f.mu.Unlock()
	cv, err := b.CancelExec(ctx, "127.0.0.1", id)
	if err != nil || jobMap(t, cv)["status"] != "killed" || jobMap(t, cv)["reason"] != "cancel" {
		t.Fatalf("cancel after recovery: %v %v", jobMap(t, cv), err)
	}
}

// 2b: a confirmed kill ends the process and the job is killed with reason cancel.
func TestRemoteCancelConfirmedKills(t *testing.T) {
	f := newFakeAgent()
	s := newRemoteTestServer(t, f)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "node-x", "sleep 600", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	cv, err := b.CancelExec(ctx, "127.0.0.1", id)
	if err != nil {
		t.Fatal(err)
	}
	if jobMap(t, cv)["status"] != "killed" || jobMap(t, cv)["reason"] != "cancel" {
		t.Fatalf("cancel view = %v", jobMap(t, cv))
	}
	f.mu.Lock()
	forced := f.forced
	f.mu.Unlock()
	if forced != 1 {
		t.Fatalf("force_terminate sent %d times, want 1", forced)
	}
}

// 2c: a kill the agent accepted but the process did not confirm is not reported as killed.
func TestRemoteCancelUnconfirmedIsNotKilled(t *testing.T) {
	f := newFakeAgent()
	f.ignoreForce = true
	s := newRemoteTestServer(t, f)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "node-x", "sleep 600", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	cv, err := b.CancelExec(ctx, "127.0.0.1", id)
	if err == nil || !strings.Contains(err.Error(), "not been confirmed") {
		t.Fatalf("unconfirmed cancel err = %v", err)
	}
	if cv != nil {
		t.Fatalf("unconfirmed cancel returned a view: %v", cv)
	}
	st, err := b.ExecStatus(ctx, id)
	if err != nil || jobMap(t, st)["status"] != "running" {
		t.Fatalf("status after unconfirmed kill: %v %v", jobMap(t, st)["status"], err)
	}
}

// 3: the job holds its token's exec slot until the process is confirmed ended.
func TestRemoteJobHoldsSlotUntilProcessEnds(t *testing.T) {
	f := newFakeAgent()
	f.ignoreForce = true
	s := newRemoteTestServer(t, f)
	s.TokenLimiter = NewTokenLimiter(1)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "node-x", "sleep 600", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	if _, err := b.CancelExec(ctx, "127.0.0.1", id); err == nil {
		t.Fatal("unconfirmed cancel succeeded")
	}
	if _, err := b.StartExec(ctx, "127.0.0.1", "local", "true", ""); err == nil || !strings.Contains(err.Error(), "token concurrent exec limit reached") {
		t.Fatalf("slot released before the process ended: %v", err)
	}
	f.endAll()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := b.StartExec(ctx, "127.0.0.1", "local", "true", ""); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot not released after the process ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, err := b.ExecStatus(ctx, id)
	if err != nil || jobMap(t, st)["status"] != "killed" {
		t.Fatalf("final status = %v, %v", jobMap(t, st)["status"], err)
	}
}

// 4: a node that goes offline marks the job lost (node_offline) and releases the slot.
func TestRemoteJobLostWhenNodeOffline(t *testing.T) {
	f := newFakeAgent()
	s := newRemoteTestServer(t, f)
	s.TokenLimiter = NewTokenLimiter(1)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "node-x", "sleep 600", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	f.setOnline(false)
	m := waitJobStatus(t, b, ctx, id, "lost", 10*time.Second)
	if m["reason"] != "node_offline" {
		t.Fatalf("lost reason = %v, want node_offline", m["reason"])
	}
	if _, err := b.StartExec(ctx, "127.0.0.1", "local", "true", ""); err != nil {
		t.Fatalf("slot not released after the node went offline: %v", err)
	}
}

// 5: another token sees a remote job exactly as an unknown job.
func TestRemoteJobInvisibleToOtherTokens(t *testing.T) {
	f := newFakeAgent()
	s := newRemoteTestServer(t, f)
	b := s.NewMCPBackend()
	v, err := b.StartExec(jobCtx("tok-A"), "127.0.0.1", "node-x", "sleep 600", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	_, errStatus := b.ExecStatus(jobCtx("tok-B"), id)
	_, errCancel := b.CancelExec(jobCtx("tok-B"), "127.0.0.1", id)
	_, errUnknown := b.ExecStatus(jobCtx("tok-B"), "job-0000000000000000")
	if errStatus == nil || errCancel == nil || errStatus.Error() != errUnknown.Error() || errCancel.Error() != errUnknown.Error() {
		t.Fatalf("other token: status=%v cancel=%v unknown=%v", errStatus, errCancel, errUnknown)
	}
}
