package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStartSessionCapturesOutputCompletely(t *testing.T) {
	for i := 0; i < 300; i++ {
		s, err := StartSession(context.Background(), fmt.Sprintf("seq-%d", i), "seq 1 2000", "", "test", 1<<20)
		if err != nil {
			t.Fatalf("iteration %d: StartSession: %v", i, err)
		}
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: session timed out", i)
		}
		stdout, _ := s.Output()
		if !strings.HasSuffix(strings.TrimSpace(stdout), "2000") {
			t.Fatalf("iteration %d: incomplete output (len=%d, tail=%q)", i, len(stdout), tail(stdout, 80))
		}
	}
}

func TestStartSessionBackgroundChildDoesNotHang(t *testing.T) {
	s, err := StartSession(context.Background(), "background-child", "echo hi; sleep 30 &", "", "test", 1024)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		_ = s.Terminate()
		t.Fatal("session hung waiting for background child holding output pipes")
	}
	stdout, _ := s.Output()
	if !strings.Contains(stdout, "hi") {
		t.Fatalf("expected captured output, got %q", stdout)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
