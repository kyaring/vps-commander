package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

func TestRequestLogActorAndCredentialRedaction(t *testing.T) {
	previous := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	const sentinel = "sentinel-token-value-must-never-be-logged"
	const paramsSentinel = "mcp-arguments-must-not-be-logged"
	digest := sha256.Sum256([]byte(sentinel))
	sentinelHash := hex.EncodeToString(digest[:])
	authCalls := 0
	authenticated := (auth.Middleware{
		Manager: auth.NewManagerWithValidator("", auth.TokenValidatorFunc(func(token, _ string) (auth.Principal, bool) {
			authCalls++
			if token != sentinel {
				t.Errorf("validator received unexpected token")
			}
			return auth.Principal{Type: "api_token", ID: "tok-7", Name: "hark-2", Scopes: map[string]bool{"devices:read": true}}, true
		})),
	}).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mcpHandler := (auth.Middleware{
		Manager: auth.NewManagerWithValidator("", auth.TokenValidatorFunc(func(token, _ string) (auth.Principal, bool) {
			authCalls++
			if token != sentinel {
				t.Errorf("validator received unexpected MCP token")
			}
			return auth.Principal{Type: "api_token", ID: "tok-7", Name: "hark-2", Scopes: map[string]bool{"devices:read": true}}, true
		})),
	}).Handler(http.HandlerFunc(mcp.NewServer(&requestLogMCPBackend{}, nil).HandleStreamableHTTP))
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-token":
			authenticated.ServeHTTP(w, r)
		case "/mcp":
			mcpHandler.ServeHTTP(w, r)
		case "/panel":
			auth.WithPrincipal(r.Context(), auth.Principal{Type: "panel", ID: "admin", Name: "panel:admin"})
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	})
	h := withRequestLoggingAndCORS(mux, nil, nil)

	largeBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_devices", "arguments": map[string]any{
			"secret": paramsSentinel, "large": strings.Repeat("x", 100*1024),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(largeBody) <= 64*1024 {
		t.Fatalf("test MCP body is only %d bytes", len(largeBody))
	}
	unauthBody := &readTrackingBody{reader: strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"must_not_be_logged"}}`)}
	for _, tc := range []struct {
		path          string
		body          string
		authenticated bool
		tracked       *readTrackingBody
	}{
		{path: "/api-token", authenticated: true},
		{path: "/unauthorized"},
		{path: "/panel", authenticated: true},
		{path: "/mcp", body: string(largeBody), authenticated: true},
		{path: "/mcp", tracked: unauthBody},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		if tc.tracked != nil {
			req.Body = tc.tracked
		}
		if tc.authenticated {
			req.Header.Set("Authorization", "Bearer "+sentinel)
		}
		if tc.path == "/mcp" {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if tc.tracked != nil && tc.tracked.reads != 0 {
			t.Fatalf("unauthenticated /mcp request body was read %d times", tc.tracked.reads)
		}
		if tc.path == "/mcp" && tc.authenticated && rec.Code != http.StatusOK {
			t.Fatalf("authenticated large MCP request status=%d body=%s", rec.Code, rec.Body.String())
		}
		if tc.path == "/mcp" && !tc.authenticated && rec.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated MCP request status=%d, want 401", rec.Code)
		}
	}
	if authCalls != 2 {
		t.Fatalf("authentication executed %d times, want once per authenticated request", authCalls)
	}
	allLogs := logs.String()
	for _, forbidden := range []string{sentinel, sentinelHash, "Authorization", "Cookie", paramsSentinel, "arguments"} {
		if strings.Contains(allLogs, forbidden) {
			t.Fatalf("request log leaked sensitive content %q", forbidden)
		}
	}

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(allLogs), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid slog JSON: %v", err)
		}
		records = append(records, record)
	}
	if len(records) != 5 {
		t.Fatalf("got %d log records, want 5", len(records))
	}
	byPath := map[string]map[string]any{}
	for _, record := range records {
		byPath[record["path"].(string)] = record
	}
	if got := byPath["/api-token"]["actor"]; got != "hark-2" {
		t.Fatalf("api_token actor=%v, want hark-2", got)
	}
	if got := byPath["/api-token"]["actor_type"]; got != "api_token" {
		t.Fatalf("api_token actor_type=%v, want api_token", got)
	}
	if got := byPath["/unauthorized"]["actor"]; got != "-" {
		t.Fatalf("unauthorized actor=%v, want -", got)
	}
	if got := byPath["/unauthorized"]["actor_type"]; got != "-" {
		t.Fatalf("unauthorized actor_type=%v, want -", got)
	}
	if got := byPath["/panel"]["actor"]; got != "panel" {
		t.Fatalf("panel actor=%v, want panel", got)
	}
	if got := byPath["/panel"]["actor_type"]; got != "panel" {
		t.Fatalf("panel actor_type=%v, want panel", got)
	}
	mcpAuthenticated, mcpUnauthenticated := false, false
	for _, record := range records {
		if record["path"] != "/mcp" {
			continue
		}
		if record["status"] == float64(http.StatusOK) {
			mcpAuthenticated = true
			if got := record["mcp_tool"]; got != "list_devices" {
				t.Fatalf("large-body mcp_tool=%v, want list_devices", got)
			}
		} else if record["status"] == float64(http.StatusUnauthorized) {
			mcpUnauthenticated = true
			if _, exists := record["mcp_tool"]; exists {
				t.Fatalf("unauthenticated /mcp log unexpectedly has mcp_tool: %#v", record["mcp_tool"])
			}
		}
	}
	if !mcpAuthenticated || !mcpUnauthenticated {
		t.Fatalf("missing authenticated/unauthenticated MCP log records: authenticated=%v unauthenticated=%v", mcpAuthenticated, mcpUnauthenticated)
	}
}

// readTrackingBody detects any attempt to inspect an unauthenticated request body.
type readTrackingBody struct {
	reader *strings.Reader
	reads  int
}

func (b *readTrackingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.reader.Read(p)
}

func (b *readTrackingBody) Close() error { return nil }

// requestLogMCPBackend is deliberately inert: this test only needs a valid
// tools/call dispatch to exercise the real MCP request parser and actor holder.
type requestLogMCPBackend struct{}

func (*requestLogMCPBackend) ListDevices(context.Context) (any, error) {
	return []map[string]string{{"name": "test"}}, nil
}
func (*requestLogMCPBackend) ExecCommand(context.Context, string, string, string, string, int) (any, error) {
	return nil, nil
}
func (*requestLogMCPBackend) ReadFile(context.Context, string, string, string, int64, int64) (any, error) {
	return nil, nil
}
func (*requestLogMCPBackend) WriteFile(context.Context, string, string, string, string) (any, error) {
	return nil, nil
}
func (*requestLogMCPBackend) EditBlock(context.Context, string, string, string, string, string) (any, error) {
	return nil, nil
}
