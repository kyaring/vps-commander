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

// Recorded on the base commit 30e0abc (identical tool definitions to 6c269e0). This card must
// not change the tools/list response by a single byte.
const (
	toolsListBaselineSHA256 = "e667502eb7e516d4c3454ced16daae5b386531cefccabbb7597a50fd94f86951"
	toolsListBaselineBytes  = 5426
	toolsListMaxBytes       = 10240
)

func TestToolsListIsByteIdenticalToBaseline(t *testing.T) {
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
