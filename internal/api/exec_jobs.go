package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

const (
	jobMaxRuntime  = 60 * time.Minute
	jobRetention   = spillTTL // status and output kept 24 h, like spill files
	jobForgetAfter = 7 * 24 * time.Hour
	jobsDirName    = "jobs"
)

var (
	// errJobNotFound is returned for unknown jobs and for jobs owned by another token alike.
	errJobNotFound = errors.New("job not found")
)

// execJob is one asynchronous exec on the local Hub. The owner is the TokenID of the token that started it.
type execJob struct {
	ID          string                `json:"job_id"`
	Owner       string                `json:"owner"`
	Device      string                `json:"device"`
	Summary     string                `json:"summary"`
	Status      string                `json:"status"` // running, exited, killed, lost (expired is derived)
	Reason      string                `json:"reason,omitempty"`
	ExitCode    int                   `json:"exit_code"`
	TimedOut    bool                  `json:"timed_out"`
	Remote      bool                  `json:"remote"`
	MaxRuntimeS int                   `json:"max_runtime_s"`
	ClientIP    string                `json:"client_ip"`
	Started     time.Time             `json:"started_at"`
	Ended       time.Time             `json:"ended_at"`
	DurationMS  int64                 `json:"duration_ms"`
	SpillPath   string                `json:"spill_path"`
	Shaped      executor.ShapedOutput `json:"shaped"`

	command    string             // full command; never persisted
	cancel     context.CancelFunc // stops the process group; never persisted
	finalized  bool               // set once finish has written output and audit; never persisted
	killReason string             // why a kill was requested (cancel or timeout); never persisted
	killSent   bool               // the kill was accepted by the agent; never persisted
}

// execJobs owns every job of this Hub. Records are also written under the spill directory so
// that a restart can report jobs that were running as lost instead of running.
type execJobs struct {
	startMu         sync.Mutex // serializes remote starts so the per-node capacity check holds
	mu              sync.Mutex
	dir             string
	jobs            map[string]*execJob
	now             func() time.Time
	max             time.Duration
	tick            time.Duration // remote monitor interval
	confirmWait     time.Duration // how long cancel waits for the end of a remote process
	remoteTTL       time.Duration // the agent's absolute session lifetime
	remoteKillAfter time.Duration // Hub kills a remote job this long after start, before the agent expires the session
	remoteIdle      time.Duration // how long a finished remote session keeps an agent slot
	remoteCapacity  int           // remote sessions per node
}

func newExecJobs(dir string, now func() time.Time) (*execJobs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	m := &execJobs{dir: dir, jobs: make(map[string]*execJob), now: now, max: jobMaxRuntime, tick: time.Second, confirmWait: 20 * time.Second,
		remoteTTL: remoteSessionTTL, remoteKillAfter: remoteSessionTTL - remoteKillMargin, remoteIdle: remoteSessionIdle, remoteCapacity: remoteNodeCapacity}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	at := now()
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		var j execJob
		if err != nil || json.Unmarshal(raw, &j) != nil || j.ID+".json" != e.Name() {
			_ = os.Remove(path)
			continue
		}
		if j.Status == "running" {
			// The process belonged to the previous Hub. Report it honestly; never as running.
			j.Status, j.Reason, j.Ended = "lost", "hub_restart", at
			m.persist(&j)
		}
		j.finalized = true
		m.jobs[j.ID] = &j
	}
	m.pruneLocked(at)
	return m, nil
}

func (m *execJobs) persist(j *execJob) {
	b, err := json.Marshal(j)
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(m.dir, j.ID+".json"), b, 0o600); err != nil {
		slog.Warn("exec job record not saved", "job", j.ID, "err", err)
	}
}

// pruneLocked clears the stored output of jobs past their retention period, and forgets
// records older than jobForgetAfter. Callers must hold mu.
func (m *execJobs) pruneLocked(now time.Time) {
	for id, j := range m.jobs {
		if j.Ended.IsZero() || j.Status == "running" {
			continue
		}
		age := now.Sub(j.Ended)
		if age > jobForgetAfter {
			delete(m.jobs, id)
			_ = os.Remove(filepath.Join(m.dir, id+".json"))
			continue
		}
		if age > jobRetention && (j.SpillPath != "" || j.Shaped.Stdout != "" || j.Shaped.Stderr != "") {
			j.SpillPath, j.Shaped = "", executor.ShapedOutput{}
			m.persist(j)
		}
	}
}

func jobSummary(command string) string {
	r := []rune(command)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return command
}

func newJobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "job-" + hex.EncodeToString(b[:])
}

// start launches the command in the background. The job holds one of the owner token's exec
// slots until the process has actually ended, so it counts against the same limit as
// synchronous exec.
func (m *execJobs) start(s *Server, p auth.Principal, clientIP, device, command, workdir string) (execJob, error) {
	if s.isRemote(device) {
		rp := s.remoteSessions()
		if rp == nil {
			return execJob{}, fmt.Errorf("device %s is offline", device)
		}
		return m.startRemote(s, rp, p, clientIP, device, command, workdir)
	}
	release, ok := s.acquireTokenExec(p)
	if !ok {
		return execJob{}, fmt.Errorf("%w: token concurrent exec limit reached", mcp.ErrRateLimited)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.max)
	j := &execJob{ID: newJobID(), Owner: p.TokenID, Device: device, Summary: jobSummary(command), Status: "running",
		MaxRuntimeS: int(jobMaxRuntime / time.Second), ClientIP: clientIP, Started: m.now(), command: command, cancel: cancel}
	m.mu.Lock()
	m.pruneLocked(m.now())
	m.jobs[j.ID] = j
	m.persist(j)
	view := *j
	m.mu.Unlock()
	go s.runJob(m, p, j, ctx, cancel, release, workdir)
	return view, nil
}

func (s *Server) runJob(m *execJobs, p auth.Principal, j *execJob, ctx context.Context, cancel context.CancelFunc, release func(), workdir string) {
	defer release()
	defer cancel()
	res := s.Exec.Run(ctx, j.command, workdir)
	m.finish(s, p, j, res, ctx.Err() == context.DeadlineExceeded)
}

// finish stores the output, settles the status and writes the end audit. It runs after the
// process has ended, even if the client that started the job has long gone.
func (m *execJobs) finish(s *Server, p auth.Principal, j *execJob, res executor.Result, deadline bool) {
	shaped, _ := executor.ShapeOutput(res.Stdout, res.Stderr, "raw", executor.DefaultOutputBytes)
	// The output expires 24 h after the job ended, so it is stored with that end time.
	m.mu.Lock()
	created := j.Ended
	if j.Status == "running" {
		created = m.now()
	}
	m.mu.Unlock()
	spill := ""
	if st := s.spillFor(); st != nil {
		if path, err := st.writeAt(p.TokenID, []byte(res.Stdout+res.Stderr), created); err != nil {
			slog.Warn("exec job output not spilled", "job", j.ID, "err", err)
		} else {
			spill = path
		}
	}
	m.mu.Lock()
	if j.Status == "running" {
		j.Ended = created
		j.Status = "exited"
		if deadline {
			j.Status, j.TimedOut, j.Reason = "killed", true, "timeout"
		}
	}
	j.SpillPath, j.Shaped, j.DurationMS, j.ExitCode = spill, shaped, res.DurationMS, res.ExitCode
	m.persist(j)
	status, reason, timedOut := j.Status, j.Reason, j.TimedOut
	m.mu.Unlock()

	endCtx := auth.WithPrincipal(context.Background(), p)
	code, msg := res.ExitCode, fmt.Sprintf("job=%s status=%s reason=%s %s", j.ID, status, reason, execAuditMessage(res))
	if status == "exited" {
		endCtx = withCommandExit(endCtx)
	} else {
		code = 124
		endCtx = withAuditResult(endCtx, "killed")
		if timedOut {
			endCtx = withAuditResult(endCtx, "timeout")
		}
	}
	_ = s.auditMCPContext(endCtx, j.ClientIP, j.Device, "exec", j.Summary, code, res.DurationMS, msg)
	m.mu.Lock()
	j.finalized = true
	m.mu.Unlock()
}

// get returns the job if owner created it and it has not been forgotten. Finished jobs report
// expired once the retention period has passed.
func (m *execJobs) get(owner, id string) (execJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.now())
	j, ok := m.jobs[id]
	if !ok || owner == "" || j.Owner != owner {
		return execJob{}, errJobNotFound
	}
	view := *j
	if view.Status != "running" && !view.Ended.IsZero() && m.now().Sub(view.Ended) > jobRetention {
		view.Status, view.SpillPath, view.Shaped = "expired", "", executor.ShapedOutput{}
	}
	return view, nil
}

// jobRef returns the live record of a job owned by owner, for operations that update it.
func (m *execJobs) jobRef(owner, id string) (*execJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || owner == "" || j.Owner != owner {
		return nil, errJobNotFound
	}
	return j, nil
}

// cancel stops a running job. The status becomes killed at once; the output and exit code are
// filled in when the process has actually been reaped.
func (m *execJobs) cancel(owner, id string) (execJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.now())
	j, ok := m.jobs[id]
	if !ok || owner == "" || j.Owner != owner {
		return execJob{}, errJobNotFound
	}
	if j.Status == "running" {
		j.Status, j.Reason, j.Ended = "killed", "cancel", m.now()
		if j.cancel != nil {
			j.cancel()
		}
		m.persist(j)
	}
	return *j, nil
}

// jobsFor returns the Hub's job manager, creating it on first use. It returns nil when the Hub
// has no data directory (in-memory database), in which case async exec is unavailable.
func (s *Server) jobsFor() *execJobs {
	s.jobsOnce.Do(func() {
		st := s.spillFor()
		if st == nil {
			return
		}
		m, err := newExecJobs(filepath.Join(st.dir, jobsDirName), time.Now)
		if err != nil {
			slog.Warn("async exec disabled", "err", err)
			return
		}
		s.jobs = m
	})
	return s.jobs
}

// jobView is the status shape returned to the caller: the unified exec fields plus job fields.
func (s *Server) jobView(j execJob) map[string]any {
	out := execOutputFields(j.Device, j.ExitCode, j.Shaped, j.DurationMS, j.SpillPath, "raw")
	if j.Status == "running" {
		out["exit_code"] = -1
		out["duration_ms"] = time.Since(j.Started).Milliseconds()
	}
	out["timed_out"] = j.TimedOut
	out["job_id"] = j.ID
	out["status"] = j.Status
	out["reason"] = j.Reason
	out["started_at"] = j.Started.UTC().Format(time.RFC3339)
	out["max_runtime_s"] = j.MaxRuntimeS
	if !j.Ended.IsZero() {
		out["ended_at"] = j.Ended.UTC().Format(time.RFC3339)
	}
	return out
}

// StartExec starts a command as a job and returns its first status (MCP exec_start).
func (b *MCPBackend) StartExec(ctx context.Context, clientIP, device, command, workdir string) (any, error) {
	if command == "" {
		return nil, errors.New("command required")
	}
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "exec", clientIP); err != nil {
		return nil, err
	}
	m := b.server.jobsFor()
	if m == nil {
		return nil, errors.New("async exec unavailable: no Hub data directory")
	}
	p, _ := auth.PrincipalFromContext(ctx)
	j, err := m.start(b.server, p, clientIP, device, command, workdir)
	if err != nil {
		return nil, err
	}
	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "exec_start", j.Summary, 0, 0, "job="+j.ID)
	}
	return b.server.jobView(j), nil
}

// ExecStatus returns the status of a job owned by the caller (MCP exec_status).
func (b *MCPBackend) ExecStatus(ctx context.Context, jobID string) (any, error) {
	m := b.server.jobsFor()
	if m == nil {
		return nil, errJobNotFound
	}
	p, _ := auth.PrincipalFromContext(ctx)
	j, err := m.get(p.TokenID, jobID)
	if err != nil {
		return nil, err
	}
	return b.server.jobView(j), nil
}

// CancelExec stops a job owned by the caller (MCP exec_cancel).
func (b *MCPBackend) CancelExec(ctx context.Context, clientIP, jobID string) (any, error) {
	m := b.server.jobsFor()
	if m == nil {
		return nil, errJobNotFound
	}
	p, _ := auth.PrincipalFromContext(ctx)
	ref, err := m.jobRef(p.TokenID, jobID)
	if err != nil {
		return nil, err
	}
	if ref.Remote {
		rp := b.server.remoteSessions()
		if rp == nil {
			return nil, errors.New("remote exec unavailable: no cluster")
		}
		view, err := m.cancelRemote(rp, ref)
		if err != nil {
			if b.server.Store != nil {
				cancelCtx := withAuditResult(auth.WithPrincipal(context.Background(), p), "error")
				_ = b.server.auditMCPContext(cancelCtx, clientIP, ref.Device, "exec_cancel", ref.Summary, 1, 0, "job="+ref.ID+" "+err.Error())
			}
			return nil, err
		}
		if b.server.Store != nil {
			_ = b.server.auditMCPContext(ctx, clientIP, view.Device, "exec_cancel", view.Summary, 0, 0, "job="+view.ID)
		}
		return b.server.jobView(view), nil
	}
	j, err := m.cancel(p.TokenID, jobID)
	if err != nil {
		return nil, err
	}
	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, j.Device, "exec_cancel", j.Summary, 0, 0, "job="+j.ID)
	}
	return b.server.jobView(j), nil
}
