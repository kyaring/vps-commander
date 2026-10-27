package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

// remoteSessionTTL is the agent's absolute session lifetime (executor.DefaultSessionTTL). The
// agent kills a session when it expires, and start_process has no TTL field, so a remote job
// cannot run longer than this without an agent upgrade.
const remoteSessionTTL = 30 * time.Minute

// remoteProcessAPI is the part of the cluster manager that remote jobs use. Tests replace it.
type remoteProcessAPI interface {
	Online(name string) bool
	StartProcess(ctx context.Context, name string, p cluster.StartProcessPayload) (map[string]any, error)
	ReadProcessOutput(ctx context.Context, name, sessionID, owner string) (cluster.ProcessOutputPayload, error)
	ForceTerminate(ctx context.Context, name, sessionID, owner string) error
}

func (s *Server) remoteSessions() remoteProcessAPI {
	if s.remoteProc != nil {
		return s.remoteProc
	}
	if s.Cluster != nil {
		return s.Cluster
	}
	return nil
}

// startRemote starts the command as an agent session named after the job and owned by the token.
// The job holds the token's exec slot until the session has ended or the job is marked lost.
func (m *execJobs) startRemote(s *Server, rp remoteProcessAPI, p auth.Principal, clientIP, device, command, workdir string) (execJob, error) {
	if !rp.Online(device) {
		return execJob{}, fmt.Errorf("device %s is offline", device)
	}
	release, ok := s.acquireTokenExec(p)
	if !ok {
		return execJob{}, fmt.Errorf("%w: token concurrent exec limit reached", mcp.ErrRateLimited)
	}
	id := newJobID()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	_, err := rp.StartProcess(ctx, device, cluster.StartProcessPayload{Command: command, Workdir: workdir, SessionID: id, Owner: p.TokenID})
	cancel()
	if err != nil {
		release()
		return execJob{}, err
	}
	j := &execJob{ID: id, Owner: p.TokenID, Device: device, Summary: jobSummary(command), Status: "running", Remote: true,
		MaxRuntimeS: int(remoteSessionTTL / time.Second), ClientIP: clientIP, Started: m.now(), command: command}
	m.mu.Lock()
	m.pruneLocked(m.now())
	m.jobs[j.ID] = j
	m.persist(j)
	view := *j
	m.mu.Unlock()
	go m.monitorRemote(s, rp, p, j, release)
	return view, nil
}

// monitorRemote follows the agent session once a second until it has ended, the node is gone, or
// the Hub deadline forces a kill. Only this goroutine settles a remote job, and it releases the
// token's exec slot when it does.
func (m *execJobs) monitorRemote(s *Server, rp remoteProcessAPI, p auth.Principal, j *execJob, release func()) {
	defer release()
	deadline := j.Started.Add(remoteSessionTTL)
	t := time.NewTicker(m.tick)
	defer t.Stop()
	for range t.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := rp.ReadProcessOutput(ctx, j.Device, j.ID, j.Owner)
		cancel()
		switch {
		case err != nil && !rp.Online(j.Device):
			m.settleRemote(s, p, j, executor.Result{}, "node_offline")
			return
		case err != nil && strings.Contains(err.Error(), "session not found"):
			m.settleRemote(s, p, j, executor.Result{}, "session_gone")
			return
		case err == nil && !out.Running:
			res := executor.Result{ExitCode: out.ExitCode, Stdout: out.Stdout, Stderr: out.Stderr, DurationMS: time.Since(j.Started).Milliseconds()}
			m.settleRemote(s, p, j, res, "")
			return
		}
		m.mu.Lock()
		if j.killReason == "" && time.Now().After(deadline) {
			j.killReason = "timeout"
		}
		send := j.killReason != "" && !j.killSent
		m.mu.Unlock()
		if send {
			kctx, kcancel := context.WithTimeout(context.Background(), 15*time.Second)
			kerr := rp.ForceTerminate(kctx, j.Device, j.ID, j.Owner)
			kcancel()
			m.mu.Lock()
			if kerr == nil {
				j.killSent = true
			}
			m.mu.Unlock()
			if kerr != nil {
				slog.Warn("remote job kill not confirmed yet", "job", j.ID, "err", kerr)
			}
		}
	}
}

// settleRemote records the outcome of a remote job, stores its output, writes the end audit and
// marks it finished. lostReason is set when the process state can no longer be known.
func (m *execJobs) settleRemote(s *Server, p auth.Principal, j *execJob, res executor.Result, lostReason string) {
	shaped, _ := executor.ShapeOutput(res.Stdout, res.Stderr, "raw", executor.DefaultOutputBytes)
	spill := ""
	if lostReason == "" {
		if st := s.spillFor(); st != nil {
			if path, err := st.write(p.TokenID, []byte(res.Stdout+res.Stderr)); err != nil {
				slog.Warn("remote job output not spilled", "job", j.ID, "err", err)
			} else {
				spill = path
			}
		}
	}
	m.mu.Lock()
	if j.Status != "running" {
		m.mu.Unlock()
		return
	}
	now := m.now()
	j.Ended = now
	status, reason, timedOut := "exited", "", false
	switch {
	case lostReason != "":
		status, reason = "lost", lostReason
	case j.killReason != "":
		status, reason, timedOut = "killed", j.killReason, j.killReason == "timeout"
	case now.Sub(j.Started) >= remoteSessionTTL-time.Minute:
		status, reason = "killed", "agent_session_ttl"
	}
	j.Status, j.Reason, j.TimedOut = status, reason, timedOut
	j.ExitCode = res.ExitCode
	if lostReason != "" {
		j.ExitCode = -1
	}
	j.SpillPath, j.Shaped, j.DurationMS = spill, shaped, res.DurationMS
	m.persist(j)
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

// cancelRemote asks the agent to terminate the session and returns only once the process is
// confirmed ended (the monitor observes the session not running). If the kill is refused, the
// error is returned and the job stays running.
func (m *execJobs) cancelRemote(rp remoteProcessAPI, j *execJob) (execJob, error) {
	m.mu.Lock()
	if j.Status != "running" {
		view := *j
		m.mu.Unlock()
		return view, nil
	}
	j.killReason = "cancel"
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := rp.ForceTerminate(ctx, j.Device, j.ID, j.Owner)
	cancel()
	m.mu.Lock()
	if err != nil {
		if j.killReason == "cancel" && !j.killSent {
			j.killReason = ""
		}
		view := *j
		m.mu.Unlock()
		return view, fmt.Errorf("kill failed, job still running: %w", err)
	}
	j.killSent = true
	m.mu.Unlock()

	wait := time.Now().Add(m.confirmWait)
	for {
		m.mu.Lock()
		view := *j
		m.mu.Unlock()
		if view.Status != "running" {
			return view, nil
		}
		if time.Now().After(wait) {
			return view, errors.New("kill sent but the process has not been confirmed ended; job still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
