package devtask

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ModelClient is the minimal interface for an OpenAI-compatible chat call.
type ModelClient interface {
	// Complete returns assistant text or a structured error (e.g. status 429).
	Complete(ctx context.Context, model, system, user string) (text string, statusCode int, err error)
}

// RunnerConfig holds process configuration.
type RunnerConfig struct {
	WorkRoot string
	LeaseSec int
	PollSec  int
}

// Runner executes at most one claimed task at a time.
type Runner struct {
	Hub    *HubClient
	Model  ModelClient
	Config RunnerConfig
}

// RunOnce claims one task (if any) and processes it.
func (r *Runner) RunOnce(ctx context.Context) (handled bool, err error) {
	task, err := r.Hub.Claim(r.Config.LeaseSec)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, nil
	}
	return true, r.process(ctx, task)
}

func (r *Runner) process(ctx context.Context, task *Task) error {
	_ = r.Hub.PostEvents(task.ID, []map[string]any{
		{"type": "status", "payload": map[string]any{"phase": "start", "round": task.Round}},
	})

	deadline := time.Now().Add(time.Duration(task.TimeoutS) * time.Second)
	if task.TimeoutS <= 0 {
		deadline = time.Now().Add(30 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	hbDone := make(chan struct{})
	defer close(hbDone)
	go func() {
		t := time.NewTicker(time.Duration(max(15, r.Config.LeaseSec/3)) * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbDone:
				return
			case <-t.C:
				_ = r.Hub.Heartbeat(task.ID, r.Config.LeaseSec)
			}
		}
	}()

	workDir := filepath.Join(r.Config.WorkRoot, task.ID)
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return r.fail(task, "workdir", err.Error())
	}

	// Model step (single step for v1 skeleton; multi-step tool loop can extend later).
	system := "You are devtask-bot. Follow the task card only. Do not change out-of-scope files. Write a short result summary."
	user := task.SpecMD
	if r.Model == nil {
		return r.fail(task, "no_model", "model client not configured")
	}
	text, code, err := r.Model.Complete(ctx, task.Model, system, user)
	if err != nil || code == 429 || code == 401 || code == 403 || code >= 500 {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		_ = r.Hub.PostEvents(task.ID, []map[string]any{
			{"type": "model_error", "payload": map[string]any{"model": task.Model, "status": code, "detail": truncate(detail, 300)}},
		})
		return r.Hub.Complete(task.ID, CompleteResult{
			Status: "blocked_model", ErrorCode: "model_error",
			ErrorDetail: fmt.Sprintf("model=%s status=%d detail=%s", task.Model, code, truncate(detail, 300)),
		})
	}
	_ = r.Hub.PostEvents(task.ID, []map[string]any{
		{"type": "model_call", "payload": map[string]any{"model": task.Model, "chars": len(text)}},
	})

	// Optionally write result.md inside jail.
	if abs, err := ResolveInRoot(workDir, "result.md"); err == nil {
		_ = os.WriteFile(abs, []byte(text), 0o640)
	}

	testSummary := r.runAccept(ctx, workDir, task)
	status := "needs_review"
	errCode, errDetail := "", ""
	if strings.Contains(testSummary, "FAIL") {
		status = "failed"
		errCode = "accept_failed"
		errDetail = truncate(testSummary, 500)
	}

	return r.Hub.Complete(task.ID, CompleteResult{
		Status: status, TestSummary: testSummary, ResultMD: text,
		ErrorCode: errCode, ErrorDetail: errDetail,
	})
}

func (r *Runner) runAccept(ctx context.Context, workDir string, task *Task) string {
	if len(task.AcceptCmds) == 0 {
		return "no accept_cmds"
	}
	var b strings.Builder
	for _, line := range task.AcceptCmds {
		argv := strings.Fields(line)
		if !AllowedCommand(task.AcceptCmds, argv) && !AllowedCommand(task.AllowedCmds, argv) {
			// Accept cmds from the task are themselves the whitelist for acceptance.
			if !AllowedCommand([]string{line}, argv) {
				b.WriteString("SKIP (not allowed): " + line + "\n")
				continue
			}
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = workDir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + workDir, "GIT_TERMINAL_PROMPT=0"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			b.WriteString("FAIL " + line + ": " + truncate(string(out), 200) + "\n")
		} else {
			b.WriteString("OK " + line + "\n")
		}
	}
	return b.String()
}

func (r *Runner) fail(task *Task, code, detail string) error {
	return r.Hub.Complete(task.ID, CompleteResult{
		Status: "failed", ErrorCode: code, ErrorDetail: detail,
	})
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
