package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
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
			return auth.Principal{Type: "api_token", ID: "tok-7", Name: "hark-2"}, true
		})),
	}).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			body, err := io.ReadAll(r.Body)
			if err != nil || !strings.Contains(string(body), paramsSentinel) {
				t.Errorf("MCP request body was not preserved")
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-token", "/mcp":
			authenticated.ServeHTTP(w, r)
		case "/panel":
			auth.WithPrincipal(r.Context(), auth.Principal{Type: "panel", ID: "admin", Name: "panel:admin"})
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	})
	h := withRequestLoggingAndCORS(mux, nil, nil)

	for _, tc := range []struct{ path, body string }{
		{"/api-token", ""},
		{"/unauthorized", ""},
		{"/panel", ""},
		{"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_devices","arguments":{"secret":"` + paramsSentinel + `"}}}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		if tc.path != "/unauthorized" {
			req.Header.Set("Authorization", "Bearer "+sentinel)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
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
	if len(records) != 4 {
		t.Fatalf("got %d log records, want 4", len(records))
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
	if got := byPath["/mcp"]["mcp_tool"]; got != "list_devices" {
		t.Fatalf("mcp_tool=%v, want list_devices", got)
	}
}
