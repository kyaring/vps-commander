package devtask

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeModel struct{}

func (fakeModel) Complete(ctx context.Context, model, system, user string) (string, int, error) {
	return "# result\nall good\n", 200, nil
}

func TestRunnerOnceWithFakeModel(t *testing.T) {
	var completed map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devtasks/claim", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "dt_test1", "project": "demo", "title": "t", "spec_md": "# goal",
			"accept_cmds": []string{}, "model": "global:deepseek-v4.1-flash",
			"status": "running", "round": 1, "max_rounds": 3, "max_steps": 10, "timeout_s": 60,
			"branch": "task/dt_test1",
		})
	})
	mux.HandleFunc("/api/v1/devtasks/dt_test1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/devtasks/dt_test1/events", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/devtasks/dt_test1/complete", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&completed)
		jsonOut(w, map[string]any{"ok": true})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	hub := &HubClient{BaseURL: ts.URL, Token: "test", ClaimedBy: "runner-test", HTTPClient: ts.Client()}
	r := &Runner{Hub: hub, Model: fakeModel{}, Config: RunnerConfig{WorkRoot: t.TempDir(), LeaseSec: 30, PollSec: 1}}
	handled, err := r.RunOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if completed["status"] != "needs_review" {
		t.Fatalf("complete payload: %#v", completed)
	}
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
