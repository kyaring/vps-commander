package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

func jobCtx(tok string) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: tok, Name: tok, TokenID: tok,
		Scopes: map[string]bool{"exec": true, "files:read": true}})
}

func jobMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("response is %T, want map", v)
	}
	return m
}

func waitJobStatus(t *testing.T, b *MCPBackend, ctx context.Context, id, want string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		v, err := b.ExecStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		m := jobMap(t, v)
		if m["status"] == want {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %v (reason %v), want %s", m["status"], m["reason"], want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// processAlive reports whether pid still runs. A zombie has been killed and counts as gone.
func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return true
	}
	state := b[i+2]
	return state != 'Z' && state != 'X'
}

func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pid file never written")
	return 0
}

func countAudit(t *testing.T, s *Server, where string, args ...any) int {
	t.Helper()
	if err := s.Store.FlushAudits(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 1: a command that finishes later is running first, then exited, with its output in the spill file.
func TestExecJobRunningThenExitedWithSpillOutput(t *testing.T) {
	s, _ := newSpillTestServer(t)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "local", "sleep 1; printf 'done-out'", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	if jobMap(t, v)["status"] != "running" {
		t.Fatalf("start status = %v, want running", jobMap(t, v)["status"])
	}
	m := waitJobStatus(t, b, ctx, id, "exited", 15*time.Second)
	if m["exit_code"] != 0 || m["stdout"] != "done-out" {
		t.Fatalf("exit=%v stdout=%q", m["exit_code"], m["stdout"])
	}
	spill, _ := m["spill_path"].(string)
	if spill == "" {
		t.Fatal("no spill_path for finished job")
	}
	page, err := s.readSpillPage("tok-A", spill, 0, 1024)
	if err != nil || page.Content != "done-out" {
		t.Fatalf("spill content = %q, %v", page.Content, err)
	}
}

// 2: another token sees a job as not found, exactly as for an unknown id; the owner still sees it.
func TestExecJobInvisibleToOtherTokens(t *testing.T) {
	s, _ := newSpillTestServer(t)
	b := s.NewMCPBackend()
	v, err := b.StartExec(jobCtx("tok-A"), "127.0.0.1", "local", "sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	defer func() { _, _ = b.CancelExec(jobCtx("tok-A"), "127.0.0.1", id) }()
	_, errStatus := b.ExecStatus(jobCtx("tok-B"), id)
	_, errCancel := b.CancelExec(jobCtx("tok-B"), "127.0.0.1", id)
	_, errUnknown := b.ExecStatus(jobCtx("tok-B"), "job-0000000000000000")
	for _, err := range []error{errStatus, errCancel} {
		if err == nil || err.Error() != errUnknown.Error() {
			t.Fatalf("other token got %v, unknown id gives %v", err, errUnknown)
		}
	}
	if _, err := b.ExecStatus(jobCtx("tok-A"), id); err != nil {
		t.Fatalf("owner cannot see its job: %v", err)
	}
}

// 3: a running job occupies one of its token's exec slots; a full limit rejects the start.
func TestExecJobHoldsTokenSlotAndLimitRejectsStart(t *testing.T) {
	s, _ := newSpillTestServer(t)
	s.TokenLimiter = NewTokenLimiter(1)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "local", "sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	if _, err := b.StartExec(ctx, "127.0.0.1", "local", "true", ""); err == nil || !strings.Contains(err.Error(), "token concurrent exec limit reached") {
		t.Fatalf("second start err = %v, want concurrent limit", err)
	}
	if _, err := b.CancelExec(ctx, "127.0.0.1", id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := b.StartExec(ctx, "127.0.0.1", "local", "true", ""); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("slot not released after the job ended")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 4: cancel marks the job killed and the process group is really gone.
func TestExecJobCancelKillsProcess(t *testing.T) {
	s, _ := newSpillTestServer(t)
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	pidFile := filepath.Join(t.TempDir(), "pid")
	v, err := b.StartExec(ctx, "127.0.0.1", "local", "sleep 300 & echo $! > "+pidFile+"; sleep 300", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	pid := waitPIDFile(t, pidFile)
	cv, err := b.CancelExec(ctx, "127.0.0.1", id)
	if err != nil {
		t.Fatal(err)
	}
	if jobMap(t, cv)["status"] != "killed" || jobMap(t, cv)["reason"] != "cancel" {
		t.Fatalf("cancel view = %v", jobMap(t, cv))
	}
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("background process %d still running after cancel", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 5: a job that outlives the Hub limit is killed automatically with reason timeout.
func TestExecJobMaxRuntimeKillsAtLimit(t *testing.T) {
	s, _ := newSpillTestServer(t)
	m := s.jobsFor()
	if m == nil {
		t.Fatal("no job manager")
	}
	m.localMax = time.Second
	b := s.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b.StartExec(ctx, "127.0.0.1", "local", "sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	got := waitJobStatus(t, b, ctx, id, "killed", 15*time.Second)
	if got["reason"] != "timeout" || got["timed_out"] != true {
		t.Fatalf("killed view = %v", got)
	}
}

// 6: the end audit is written even though the client that started the job is gone.
func TestExecJobEndAuditSurvivesClientDisconnect(t *testing.T) {
	s, _ := newSpillTestServer(t)
	b := s.NewMCPBackend()
	reqCtx, disconnect := context.WithCancel(jobCtx("tok-A"))
	v, err := b.StartExec(reqCtx, "127.0.0.1", "local", "sleep 1; echo end", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	disconnect()
	waitJobStatus(t, b, jobCtx("tok-A"), id, "exited", 15*time.Second)
	if n := countAudit(t, s, "action = 'exec' AND error_msg LIKE ?", "%job="+id+"%"); n != 1 {
		t.Fatalf("end audit rows = %d, want 1", n)
	}
	if n := countAudit(t, s, "action = 'exec_start' AND error_msg = ?", "job="+id); n != 1 {
		t.Fatalf("start audit rows = %d, want 1", n)
	}
}

// 7: a synchronous exec that hits its deadline says so in the response and in the audit.
func TestSyncExecTimeoutIsMarkedAndAudited(t *testing.T) {
	s, _ := newSpillTestServer(t)
	b := s.NewMCPBackend()
	v, err := b.ExecCommandWithOptions(jobCtx("tok-A"), "127.0.0.1", "local", "sleep 5", "", 1, "raw", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if jobMap(t, v)["timed_out"] != true {
		t.Fatalf("timed_out not set: %v", jobMap(t, v))
	}
	if n := countAudit(t, s, "action = 'exec' AND result = 'timeout' AND error_msg LIKE 'timeout;%'"); n != 1 {
		t.Fatalf("timeout audit rows = %d, want 1", n)
	}
}

// 8: after a restart, a job that was running is reported lost, never running.
func TestExecJobsAfterRestartAreNotRunning(t *testing.T) {
	s1, _ := newSpillTestServer(t)
	b1 := s1.NewMCPBackend()
	ctx := jobCtx("tok-A")
	v, err := b1.StartExec(ctx, "127.0.0.1", "local", "sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	id := jobMap(t, v)["job_id"].(string)
	defer func() { _, _ = s1.jobsFor().cancel("tok-A", id) }()
	s2 := &Server{Store: s1.Store, LocalName: "local"}
	got, err := s2.NewMCPBackend().ExecStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	m := jobMap(t, got)
	if m["status"] != "lost" || m["reason"] != "hub_restart" {
		t.Fatalf("after restart: status=%v reason=%v, want lost/hub_restart", m["status"], m["reason"])
	}
}

// Retention: a finished job reports expired after 24 hours and drops its output reference.
func TestExecJobExpiresAfterRetention(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, err := newExecJobs(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	m.jobs["job-x"] = &execJob{ID: "job-x", Owner: "tok-A", Status: "exited", Ended: now, SpillPath: "/x"}
	if j, err := m.get("tok-A", "job-x"); err != nil || j.Status != "exited" {
		t.Fatalf("before retention: %v %v", j.Status, err)
	}
	now = now.Add(25 * time.Hour)
	j, err := m.get("tok-A", "job-x")
	if err != nil || j.Status != "expired" || j.SpillPath != "" {
		t.Fatalf("after retention: status=%v spill=%q err=%v", j.Status, j.SpillPath, err)
	}
}

// Errata 2: spill ownership follows TokenID. Two tokens in one quota group cannot read each other's output.
func TestSpillOwnershipUsesTokenIDNotQuotaID(t *testing.T) {
	s, _ := newSpillTestServer(t)
	b := s.NewMCPBackend()
	mk := func(tok string) context.Context {
		return auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: tok, Name: tok, TokenID: tok, QuotaTokenID: "shared",
			Scopes: map[string]bool{"exec": true, "files:read": true}})
	}
	a, c := mk("tok-A"), mk("tok-B")
	v, err := b.ExecCommandWithOptions(a, "127.0.0.1", "local", `head -c 5000 /dev/zero | tr '\0' a`, "", 5, "raw", 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	spill := jobMap(t, v)["spill_path"].(string)
	if _, err := b.ReadFile(c, "127.0.0.1", mcp.SpillDeviceName, spill, 0, 64); err == nil || err.Error() != errSpillNotFound.Error() {
		t.Fatalf("same quota, other token: err = %v", err)
	}
	if _, err := b.ReadFile(a, "127.0.0.1", mcp.SpillDeviceName, spill, 0, 64); err != nil {
		t.Fatalf("owner cannot read its spill: %v", err)
	}
}

// Errata 3: a REST read of the spill device is audited like the MCP path.
func TestRESTSpillReadIsAudited(t *testing.T) {
	s, _ := newSpillTestServer(t)
	b := s.NewMCPBackend()
	v, err := b.ExecCommandWithOptions(jobCtx("tok-A"), "127.0.0.1", "local", `head -c 5000 /dev/zero | tr '\0' a`, "", 5, "raw", 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	spill := jobMap(t, v)["spill_path"].(string)
	body := `{"device":"` + mcp.SpillDeviceName + `","path":"` + spill + `","offset":0,"limit":16}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/file/read", strings.NewReader(body)).WithContext(jobCtx("tok-A"))
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("REST read: status %d body %s", rec.Code, rec.Body.String())
	}
	if n := countAudit(t, s, "action = 'file_read' AND device = ?", mcp.SpillDeviceName); n < 1 {
		t.Fatalf("REST spill read not audited (rows=%d)", n)
	}
}

// drainTestJobs stops every job the test started and waits until each has written its output
// and end audit, so the temporary directory and the database are not used after the test ends.
func drainTestJobs(s *Server) {
	m := s.jobs
	if m == nil {
		return
	}
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.Status == "running" && j.cancel != nil {
			j.cancel()
		}
	}
	m.mu.Unlock()
	deadline := time.Now().Add(20 * time.Second)
	for {
		m.mu.Lock()
		pending := 0
		for _, j := range m.jobs {
			if !j.finalized {
				pending++
			}
		}
		m.mu.Unlock()
		if pending == 0 || time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
