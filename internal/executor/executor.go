package executor

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type Result struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMS int64
}

type Local struct{ MaxOutput int }

type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return len(p), nil
	}
	if b.buf.Len() < b.max {
		n := b.max - b.buf.Len()
		if n > len(p) {
			n = len(p)
		}
		_, _ = b.buf.Write(p[:n])
	}
	return len(p), nil
}

func (e Local) Run(ctx context.Context, command, workdir string) Result {
	start := time.Now()
	if e.MaxOutput <= 0 {
		e.MaxOutput = 1024 * 1024
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Bound the output-copy wait so a background descendant that escaped the
	// process group (setsid) and kept stdout open cannot hang the call forever.
	cmd.WaitDelay = 2 * time.Second
	if workdir != "" {
		cmd.Dir = workdir
	}
	var stdout, stderr cappedBuffer
	stdout.max, stderr.max = e.MaxOutput, e.MaxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Result{ExitCode: 1, Stderr: err.Error(), DurationMS: time.Since(start).Milliseconds()}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	timedOut := false
	var err error
	select {
	case err = <-waitCh:
	case <-ctx.Done():
		timedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = <-waitCh
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		// The shell exited 0; only a leftover background descendant kept the
		// pipes open past WaitDelay. That is not a failure of the command.
		err = nil
	}
	code := 0
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	if timedOut || ctx.Err() != nil {
		code = 124
	}
	return Result{code, stdout.buf.String(), strings.TrimSpace(stderr.buf.String()), time.Since(start).Milliseconds()}
}
