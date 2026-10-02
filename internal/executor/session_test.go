package executor

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRingBufferKeepsNewestData(t *testing.T) {
	r := NewRingBuffer(8)
	_, _ = r.Write([]byte("12345678"))
	_, _ = r.Write([]byte("90"))
	if got := string(r.Snapshot()); got != "34567890" {
		t.Fatalf("got %q", got)
	}
}
func TestSessionCapturesAndTerminates(t *testing.T) {
	s, err := StartSession(context.Background(), "t1", "printf hello; sleep 5", "", "owner", 1024)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	out, _ := s.Output()
	if !strings.Contains(out, "hello") {
		t.Fatalf("out=%q", out)
	}
	if err := s.Terminate(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not terminate")
	}
}
func TestSessionManagerDuplicateAndLimit(t *testing.T) {
	m := NewSessionManager(1)
	s, err := m.Start(context.Background(), "a", "sleep 1", "", "owner", 128)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill()
	if _, err := m.Start(context.Background(), "b", "true", "", "owner", 128); err == nil {
		t.Fatal("expected limit")
	}
	if _, err := m.Start(context.Background(), "a", "true", "", "owner", 128); err == nil {
		t.Fatal("expected duplicate")
	}
}

func TestSessionIdleAuthorizationTouch(t *testing.T) {
	s, err := StartSession(context.Background(), "idle-test", "sleep 2", "", "owner", 128)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill()

	s.mu.Lock()
	s.LastAccess = time.Now().Add(-(SessionIdleTTL + time.Second))
	s.mu.Unlock()
	if s.Authorized("owner") {
		t.Fatal("idle-expired session must not authorize")
	}

	s.mu.Lock()
	s.LastAccess = time.Now()
	s.mu.Unlock()
	if !s.Authorized("owner") {
		t.Fatal("active session should authorize")
	}
}
