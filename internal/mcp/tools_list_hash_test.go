package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
)

// tools/list recorded after T-20261113-02: include D4 dev_task_* tools, their annotations, and expanded descriptions.
// Earlier T-20261101-11 baseline: tools/list recorded after completing five descriptions and clearing contract exemptions.
// Earlier value (T-20261101-01): sha256 ce0a64e40a4f7025023959dfcfb818f5e72d719594bf43ba72245c2b2ea36c29, 8218 bytes.
// tools/list recorded after T-20261027-06 (exec_start description now covers remote nodes).
// Earlier value (T-20261016-02 rework): sha256 221832ceb72739dbf1b39fafdab3fb901519534121d09b51b5588ec61c0435a2, 6427 bytes.
// Before T-20261027-06 (remote exec_start refused): see T-20261016-02.
// Previous value (T-20261016-01, base 30e0abc): sha256 e667502eb7e516d4c3454ced16daae5b386531cefccabbb7597a50fd94f86951, 5426 bytes.
const (
	toolsListBaselineSHA256 = "8cc6ba4550d85df66f456c3adb269a6a8419ab75dcf1094fc299b725ac361cf9"
	toolsListBaselineBytes  = 16346
	toolsListMaxBytes       = 16384
)

func TestToolsListMatchesRecordedHash(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	srv := NewServer(&mockBackend{}, nil)
	r := httptest.NewRequest("POST", "/mcp", nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: "panel"})
	resp := srv.handleRequest(ctx, r, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(b))
	if got != toolsListBaselineSHA256 || len(b) != toolsListBaselineBytes {
		t.Fatalf("tools/list changed: sha256 old=%s new=%s; bytes old=%d new=%d",
			toolsListBaselineSHA256, got, toolsListBaselineBytes, len(b))
	}
	if len(b) > toolsListMaxBytes {
		t.Fatalf("tools/list is %d bytes, limit %d", len(b), toolsListMaxBytes)
	}
}
