package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DiagnosticRequest is a structured, read-only diagnostic operation. It is deliberately
// not a shell command: every action maps to a fixed executable/argument set.
type DiagnosticRequest struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Lines  int    `json:"lines,omitempty"`
}

type DiagnosticResult struct {
	Action     string `json:"action"`
	Target     string `json:"target,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
}

func Diagnostic(ctx context.Context, req DiagnosticRequest) (DiagnosticResult, error) {
	args, err := diagnosticArgs(req)
	if err != nil {
		return DiagnosticResult{}, err
	}
	if req.Lines <= 0 {
		req.Lines = 100
	}
	if req.Lines > 500 {
		req.Lines = 500
	}
	// docker logs is the only operation where a caller-controlled numeric limit is useful.
	if req.Action == "docker_logs" {
		args = []string{"logs", "--tail", strconv.Itoa(req.Lines), req.Target}
	}

	start := time.Now()
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, args[0], args[1:]...)
	out, runErr := cmd.CombinedOutput()
	const maxOutput = 256 * 1024
	if len(out) > maxOutput {
		out = append(out[:maxOutput], []byte("\n[output truncated at 256 KiB]\n")...)
	}
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	result := DiagnosticResult{Action: req.Action, Target: req.Target, ExitCode: code, Stdout: string(out), DurationMS: time.Since(start).Milliseconds()}
	if runErr != nil && code == -1 {
		return result, fmt.Errorf("diagnostic failed: %w", runErr)
	}
	return result, nil
}

func diagnosticArgs(req DiagnosticRequest) ([]string, error) {
	target := strings.TrimSpace(req.Target)
	switch req.Action {
	case "docker_ps":
		if target != "" {
			return nil, errors.New("docker_ps does not accept target")
		}
		return []string{"docker", "ps", "--no-trunc"}, nil
	case "docker_logs":
		if target == "" || strings.ContainsAny(target, " \t\r\n;|&<>$`") {
			return nil, errors.New("invalid docker container target")
		}
		return []string{"docker", "logs", "--tail", "100", target}, nil
	case "docker_inspect":
		if target == "" || strings.ContainsAny(target, " \t\r\n;|&<>$`") {
			return nil, errors.New("invalid docker target")
		}
		return []string{"docker", "inspect", target}, nil
	case "systemctl_status":
		if target == "" || strings.ContainsAny(target, " \t\r\n;|&<>$`") {
			return nil, errors.New("invalid systemd unit")
		}
		return []string{"systemctl", "status", "--no-pager", "--full", target}, nil
	case "journalctl":
		if target == "" || strings.ContainsAny(target, " \t\r\n;|&<>$`") {
			return nil, errors.New("invalid journal unit")
		}
		return []string{"journalctl", "--no-pager", "-u", target, "-n", "100"}, nil
	case "ss_listen":
		if target != "" {
			return nil, errors.New("ss_listen does not accept target")
		}
		return []string{"ss", "-lntp"}, nil
	case "df":
		if target != "" {
			return nil, errors.New("df does not accept target")
		}
		return []string{"df", "-h"}, nil
	case "free":
		if target != "" {
			return nil, errors.New("free does not accept target")
		}
		return []string{"free", "-h"}, nil
	case "uptime":
		if target != "" {
			return nil, errors.New("uptime does not accept target")
		}
		return []string{"uptime"}, nil
	default:
		return nil, fmt.Errorf("unsupported diagnostic action: %s", req.Action)
	}
}
