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

// tools/list recorded after T-20261016-02 (exec_start, exec_status, exec_cancel added).
// Previous value (T-20261016-01, base 30e0abc): sha256 e667502eb7e516d4c3454ced16daae5b386531cefccabbb7597a50fd94f86951, 5426 bytes.
const (
	toolsListBaselineSHA256 = "3abe94ff8d644cd831dc5fe7ef548a2c742609e87df505f789befee0e7d99279"
	toolsListBaselineBytes  = 6433
	toolsListMaxBytes       = 10240
)

func TestToolsListMatchesRecordedHash(t *testing.T) {
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
