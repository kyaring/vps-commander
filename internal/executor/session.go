package executor

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultRingCapacity   = 2 * 1024 * 1024
	MaxSessionInput       = 64 * 1024
	DefaultSessionTTL     = 30 * time.Minute
	SessionIdleTTL        = 10 * time.Minute
	MaxSessionBufferTotal = 8 * 1024 * 1024
)

type RingBuffer struct {
	mu   sync.RWMutex
	buf  []byte
	head int
	size int
}

func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = DefaultRingCapacity
	}
	return &RingBuffer{buf: make([]byte, capacity)}
}

func (r *RingBuffer) Write(p []byte) (int, error) {
	total := len(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(p) > 0 {
		n := len(p)
		if n > len(r.buf) {
			p = p[n-len(r.buf):]
			n = len(p)
		}
		first := len(r.buf) - r.head
		if first > n {
			first = n
		}
		copy(r.buf[r.head:r.head+first], p[:first])
		if first < n {
			copy(r.buf[:n-first], p[first:n])
		}
		r.head = (r.head + n) % len(r.buf)
		r.size += n
		if r.size > len(r.buf) {
			r.size = len(r.buf)
		}
		p = p[n:]
	}
	return total, nil
}

func (r *RingBuffer) Snapshot() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]byte, r.size)
	start := (r.head - r.size + len(r.buf)) % len(r.buf)
	first := len(r.buf) - start
	if first > r.size {
		first = r.size
	}
	copy(out, r.buf[start:start+first])
	if first < r.size {
		copy(out[first:], r.buf[:r.size-first])
	}
	return out
}

type Session struct {
	ID         string
	Command    string
	Owner      string
	StartedAt  time.Time
	ExpiresAt  time.Time
	LastAccess time.Time
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *RingBuffer
	stderr     *RingBuffer
	done       chan struct{}
	mu         sync.RWMutex
	err        error
	exitCode   int
}

func StartSession(ctx context.Context, id, command, workdir, owner string, capacity int) (*Session, error) {
	if id == "" || command == "" {
		return nil, errors.New("session id and command required")
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workdir != "" {
		cmd.Dir = workdir
	}
	stdout := NewRingBuffer(capacity)
	stderr := NewRingBuffer(capacity)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errOut, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	s := &Session{
		ID: id, Command: command, Owner: owner, StartedAt: now, LastAccess: now, ExpiresAt: now.Add(DefaultSessionTTL), cmd: cmd,
		stdin: stdin, stdout: stdout, stderr: stderr, done: make(chan struct{}), exitCode: -1,
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	go s.copy(out, stdout)
	go s.copy(errOut, stderr)
	go s.wait()
	go func() {
		select {
		case <-ctx.Done():
			s.Terminate()
		case <-s.done:
		}
	}()
	return s, nil
}

func (s *Session) copy(src io.Reader, dst io.Writer) {
	_, _ = io.Copy(dst, src)
}

func (s *Session) wait() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.err = err
	if err == nil {
		s.exitCode = 0
	} else if ee, ok := err.(*exec.ExitError); ok {
		s.exitCode = ee.ExitCode()
	} else {
		s.exitCode = 1
	}
	s.mu.Unlock()
	close(s.done)
}

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Status() (int, bool, error) {
	select {
	case <-s.done:
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.exitCode, true, s.err
	default:
		return -1, false, nil
	}
}

func (s *Session) Output() (stdout, stderr string) {
	stdout, stderr = string(s.stdout.Snapshot()), string(s.stderr.Snapshot())
	s.mu.Lock()
	if time.Now().Before(s.ExpiresAt) {
		s.LastAccess = time.Now()
	}
	s.mu.Unlock()
	return stdout, stderr
}

func (s *Session) Stdin(data []byte) error {
	if len(data) > MaxSessionInput {
		return errors.New("session input too large")
	}
	s.mu.RLock()
	stdin := s.stdin
	s.mu.RUnlock()
	if stdin == nil {
		return errors.New("session stdin unavailable")
	}
	_, err := stdin.Write(data)
	if err == nil {
		s.mu.Lock()
		s.LastAccess = time.Now()
		s.mu.Unlock()
	}
	return err
}

func (s *Session) Authorized(owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if owner == "" || s.Owner != owner || now.After(s.ExpiresAt) || now.Sub(s.LastAccess) > SessionIdleTTL {
		return false
	}
	s.LastAccess = now
	return true
}
func (s *Session) Expired() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	return now.After(s.ExpiresAt) || now.Sub(s.LastAccess) > SessionIdleTTL
}

func (s *Session) Terminate() error {
	s.mu.RLock()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	s.mu.RUnlock()
	if pid == 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func (s *Session) Kill() error {
	s.mu.RLock()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	s.mu.RUnlock()
	if pid == 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// Compile-time sanity for the output writer contract.
var _ io.Writer = (*RingBuffer)(nil)
