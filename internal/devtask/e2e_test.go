package devtask

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// statefulHub is a minimal in-memory Hub for E2E without production deps.
type statefulHub struct {
	mu        sync.Mutex
	task      map[string]any
	claimed   bool
	events    int
	completed map[string]any
}

func (h *statefulHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/api/v1/devtasks/claim" && r.Method == http.MethodPost:
		if h.claimed || h.task == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.claimed = true
		h.task["status"] = "running"
		writeJSON(w, h.task)
	case strings.HasSuffix(p, "/heartbeat") && r.Method == http.MethodPost:
		writeJSON(w, map[string]any{"ok": true})
	case strings.HasSuffix(p, "/events") && r.Method == http.MethodPost:
		h.events++
		writeJSON(w, map[string]any{"ok": true})
	case strings.HasSuffix(p, "/complete") && r.Method == http.MethodPost:
		_ = json.NewDecoder(r.Body).Decode(&h.completed)
		if h.task != nil && h.completed != nil {
			h.task["status"] = h.completed["status"]
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newE2ETask() map[string]any {
	return map[string]any{
		"id": "dt_e2e1", "project": "demo", "title": "e2e", "spec_md": "# implement X",
		"accept_cmds": []string{}, "allowed_cmds": []string{}, "model": "global:deepseek-v4.1-flash",
		"status": "queued", "round": 1, "max_rounds": 3, "max_steps": 10, "timeout_s": 120,
		"branch": "task/dt_e2e1", "base_ref": "main",
	}
}

func TestDevTaskE2EDispatchClaimComplete(t *testing.T) {
	h := &statefulHub{task: newE2ETask()}
	ts := httptest.NewServer(h)
	defer ts.Close()

	r := &Runner{
		Hub:    &HubClient{BaseURL: ts.URL, Token: "runner", ClaimedBy: "e2e", HTTPClient: ts.Client()},
		Model:  fakeModel{},
		Config: RunnerConfig{WorkRoot: t.TempDir(), LeaseSec: 60, PollSec: 1},
	}
	handled, err := r.RunOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	h.mu.Lock()
	status := h.completed["status"]
	events := h.events
	h.mu.Unlock()
	if status != "needs_review" {
		t.Fatalf("status=%v events=%d completed=%#v", status, events, h.completed)
	}
	if events < 1 {
		t.Fatalf("expected events, got %d", events)
	}
	handled, err = r.RunOnce(context.Background())
	if err != nil || handled {
		t.Fatalf("second claim handled=%v err=%v", handled, err)
	}
}

func TestDevTaskE2EFakeHubAndModel(t *testing.T) {
	TestDevTaskE2EDispatchClaimComplete(t)
}

type blockedModel struct{}

func (blockedModel) Complete(ctx context.Context, model, system, user string) (string, int, error) {
	return "", 429, errModelLimited
}

type limitedError struct{}

func (limitedError) Error() string { return "rate limited" }

var errModelLimited = limitedError{}

func TestDevTaskE2EModelBlocked(t *testing.T) {
	h := &statefulHub{task: newE2ETask()}
	ts := httptest.NewServer(h)
	defer ts.Close()

	r := &Runner{
		Hub:    &HubClient{BaseURL: ts.URL, Token: "runner", ClaimedBy: "e2e", HTTPClient: ts.Client()},
		Model:  blockedModel{},
		Config: RunnerConfig{WorkRoot: t.TempDir(), LeaseSec: 60, PollSec: 1},
	}
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.completed["status"] != "blocked_model" {
		t.Fatalf("want blocked_model, got %#v", h.completed)
	}
	if h.completed["error_code"] != "model_error" {
		t.Fatalf("error_code=%v", h.completed["error_code"])
	}
}
