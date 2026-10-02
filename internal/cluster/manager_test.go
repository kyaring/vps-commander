package cluster

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNodeOperationGateAllowsReadsAndSerializesWrites(t *testing.T) {
	n := &Node{}
	release1 := n.acquireOperation("file_read")
	done := make(chan struct{})
	go func() { release2 := n.acquireOperation("edit_block"); close(done); release2() }()
	select {
	case <-done:
		t.Fatal("write entered while read active")
	case <-time.After(50 * time.Millisecond):
	}
	release1()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("write did not proceed")
	}
}
func TestNodeOperationGateAllowsConcurrentReads(t *testing.T) {
	n := &Node{}
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			release := n.acquireOperation("file_read")
			time.Sleep(30 * time.Millisecond)
			release()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reads did not complete")
	}
}
func TestOperationRiskUnknownFailsClosed(t *testing.T) {
	if operationRisk("unknown-danger") != 3 {
		t.Fatal("unknown action must be high risk")
	}
	_ = context.Background()
}
