package mcp

import (
	"testing"
	"time"
)

// 同一令牌的两个会话，预算累计到同一个汇总。
func TestBudgetSumsAllSessionsOfOneToken(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	srv.RecordSessionResponse("tok-A", "s1", "", "read_file", 1000)
	srv.RecordSessionResponse("tok-A", "s2", "", "exec_command", 2000)
	stats, ok := srv.SessionBudgetStats("tok-A", "s1", "")
	if !ok {
		t.Fatal("no stats")
	}
	if stats.BytesReturned != 3000 || stats.ToolCalls != 2 {
		t.Fatalf("bytes=%d calls=%d, want 3000 and 2", stats.BytesReturned, stats.ToolCalls)
	}
}

// 不同令牌互不影响。
func TestBudgetOtherTokenUnaffected(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	srv.RecordSessionResponse("tok-B", "sB", "", "read_file", 500)
	stats, ok := srv.SessionBudgetStats("tok-A", "", "")
	if !ok {
		t.Fatal("no stats for tok-A")
	}
	if stats.BytesReturned != 0 {
		t.Fatalf("tok-A bytes = %d, want 0", stats.BytesReturned)
	}
}

// 会话过期后，它的字节不再计入。
func TestBudgetExpiredSessionDropsOut(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	srv.RecordSessionResponse("tok-A", "s1", "", "read_file", 1000)
	srv.RecordSessionResponse("tok-A", "s2", "", "read_file", 2000)
	srv.budgetMu.Lock()
	srv.sessionBudgets[sessionMemoKey("tok-A", "s1", "")].lastActivity = time.Now().Add(-25 * time.Hour)
	srv.budgetMu.Unlock()
	stats, ok := srv.SessionBudgetStats("tok-A", "s2", "")
	if !ok {
		t.Fatal("no stats")
	}
	if stats.BytesReturned != 2000 {
		t.Fatalf("bytes = %d, want 2000 (expired session excluded)", stats.BytesReturned)
	}
}

// 告警按令牌的汇总判断：单个会话未超限，但合计超过 2 MiB 的阈值（这里用小阈值）时告警。
func TestBudgetWarningUsesTokenTotal(t *testing.T) {
	srv := NewServer(&mockBackend{}, nil)
	srv.sessionBudgetLimit = 1500
	srv.RecordSessionResponse("tok-A", "s1", "", "read_file", 1000)
	if w := srv.SessionBudgetWarning("tok-A", "s1", ""); w != "" {
		t.Fatalf("warned at 1000 of 1500: %q", w)
	}
	srv.RecordSessionResponse("tok-A", "s2", "", "read_file", 1000)
	if w := srv.SessionBudgetWarning("tok-A", "s2", ""); w == "" {
		t.Fatal("no warning although the token total is 2000 > 1500")
	}
	if w := srv.SessionBudgetWarning("tok-B", "", ""); w != "" {
		t.Fatalf("other token warned: %q", w)
	}
}
