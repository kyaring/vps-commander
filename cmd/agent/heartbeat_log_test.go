package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/cluster"
)

// agentLogBuffer is a concurrency-safe log sink for asserting on log lines.
type agentLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *agentLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *agentLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestAgentHeartbeatWriteFailureIsLogged pins the audit finding that a failing
// 15s heartbeat ping write was swallowed by `_ = writeJSON(...)`. A persistent
// failure means the connection is already broken, so it must be visible, and
// the counter must make a run of failures obvious. The loop's behavior is
// unchanged: it keeps ticking and never returns an error.
func TestAgentHeartbeatWriteFailureIsLogged(t *testing.T) {
	var logs agentLogBuffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	stop := make(chan struct{})
	defer close(stop)
	var attempts int
	var attemptsMu sync.Mutex
	writeJSON := func(msg cluster.Message) error {
		attemptsMu.Lock()
		attempts++
		attemptsMu.Unlock()
		if msg.Event != "ping" {
			t.Errorf("heartbeat sent event %q, want ping", msg.Event)
		}
		return errors.New("injected write failure")
	}

	go heartbeatLoop("node-agent-x", 5*time.Millisecond, writeJSON, stop)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "consecutive failures: 2") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	const want = "heartbeat ping write failed for device node-agent-x (consecutive failures: "
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("log does not contain %q; got:\n%s", want, logs.String())
	}
	if !strings.Contains(logs.String(), "injected write failure") {
		t.Fatalf("log does not carry the underlying error; got:\n%s", logs.String())
	}
	// The counter must increase across consecutive failures so a persistent
	// outage is distinguishable from a single blip.
	if !strings.Contains(logs.String(), "consecutive failures: 2") {
		t.Fatalf("consecutive failure counter did not advance; got:\n%s", logs.String())
	}
	attemptsMu.Lock()
	defer attemptsMu.Unlock()
	if attempts < 2 {
		t.Fatalf("heartbeat attempted %d writes, want at least 2", attempts)
	}
}
