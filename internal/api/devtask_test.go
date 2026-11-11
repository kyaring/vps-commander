package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func seedDevProject(t *testing.T, s *Server) {
	t.Helper()
	if err := s.Store.UpsertDevProject(storage.DevProject{
		Name: "demo", RepoPath: "/tmp/demo", BaseRef: "main", DefaultModel: storage.DevTaskDefaultModel,
		AllowedCmdsJSON: `[]`, DefaultAcceptCmdsJSON: `["go test"]`, ForbiddenPathsJSON: `[]`,
		MaxRounds: 3, MaxSteps: 20, TimeoutS: 600, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDevTaskAPIRequiresScopesAndDisabled404(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true})
	if rec := devtaskDo(t, s, admin, http.MethodGet, "/api/v1/devtasks", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled list: %d", rec.Code)
	}
	t.Setenv("DEVTASK_ENABLED", "on")
	// recreate server so env is read on each request (devtaskEnabled reads env live)
	s, _ = newSpillTestServer(t)
	seedDevProject(t, s)
	noscope := devtaskCtx(map[string]bool{"devices:read": true})
	if rec := devtaskDo(t, s, noscope, http.MethodGet, "/api/v1/devtasks", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no scope: %d", rec.Code)
	}
}

func TestDevTaskDispatchModelWhitelist(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	seedDevProject(t, s)
	submit := devtaskCtx(map[string]bool{"devtask:submit": true})
	body := `{"project":"demo","title":"x","spec_md":"# g","model":"global:not-allowed"}`
	if rec := devtaskDo(t, s, submit, http.MethodPost, "/api/v1/devtasks", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad model: %d %s", rec.Code, rec.Body.String())
	}
	okBody := `{"project":"demo","title":"x","spec_md":"# g"}`
	rec := devtaskDo(t, s, submit, http.MethodPost, "/api/v1/devtasks", okBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		TaskID string `json:"task_id"`
		Model  string `json:"model"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.TaskID == "" || out.Model != storage.DevTaskDefaultModel {
		t.Fatalf("out=%+v err=%v body=%s", out, err, rec.Body.String())
	}
}

func TestDevTaskRunnerRoutesRequireRunScope(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	seedDevProject(t, s)
	submit := devtaskCtx(map[string]bool{"devtask:submit": true})
	body := `{"project":"demo","title":"x","spec_md":"# g"}`
	rec := devtaskDo(t, s, submit, http.MethodPost, "/api/v1/devtasks", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch: %d", rec.Code)
	}
	readOnly := devtaskCtx(map[string]bool{"devtask:read": true})
	if rec := devtaskDo(t, s, readOnly, http.MethodPost, "/api/v1/devtasks/claim", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("claim without run: %d", rec.Code)
	}
	runner := devtaskCtx(map[string]bool{"devtask:run": true})
	runner = devtaskCtx(map[string]bool{"devtask:run": true})
	// principal TokenID from helper is tok-adm
	claim := devtaskDo(t, s, runner, http.MethodPost, "/api/v1/devtasks/claim", `{}`)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", claim.Code, claim.Body.String())
	}
	var task map[string]any
	_ = json.Unmarshal(claim.Body.Bytes(), &task)
	id, _ := task["id"].(string)
	if id == "" {
		t.Fatalf("no id in %s", claim.Body.String())
	}
	if rec := devtaskDo(t, s, runner, http.MethodPost, "/api/v1/devtasks/"+id+"/heartbeat", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d", rec.Code)
	}
	if rec := devtaskDo(t, s, runner, http.MethodPost, "/api/v1/devtasks/"+id+"/events", `{"events":[{"type":"note","payload":{"m":"hi"}}]}`); rec.Code != http.StatusOK {
		t.Fatalf("events: %d %s", rec.Code, rec.Body.String())
	}
	if rec := devtaskDo(t, s, runner, http.MethodPost, "/api/v1/devtasks/"+id+"/complete", `{"status":"needs_review","commit_sha":"abc","result_md":"done"}`); rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDevTaskHeartbeatAPIRejectsOtherRunner(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	seedDevProject(t, s)

	submit := devtaskCtx(map[string]bool{"devtask:submit": true})
	rec := devtaskDo(t, s, submit, http.MethodPost, "/api/v1/devtasks", `{"project":"demo","title":"test heartbeat other runner","spec_md":"# goal"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch: %d", rec.Code)
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	id := out.TaskID

	runnerA := auth.WithPrincipal(context.Background(), auth.Principal{
		Type: "api_token", ID: "tok-runner-A", Name: "runner-A", TokenID: "tok-runner-A",
		Scopes: map[string]bool{"devtask:run": true},
	})
	runnerB := auth.WithPrincipal(context.Background(), auth.Principal{
		Type: "api_token", ID: "tok-runner-B", Name: "runner-B", TokenID: "tok-runner-B",
		Scopes: map[string]bool{"devtask:run": true},
	})

	claimRec := devtaskDo(t, s, runnerA, http.MethodPost, "/api/v1/devtasks/claim", `{}`)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("claim failed: %d %s", claimRec.Code, claimRec.Body.String())
	}

	hbB := devtaskDo(t, s, runnerB, http.MethodPost, "/api/v1/devtasks/"+id+"/heartbeat", `{"lease_sec":60}`)
	if hbB.Code != http.StatusConflict {
		t.Fatalf("runner B heartbeat status = %d, want 409 Conflict; body=%s", hbB.Code, hbB.Body.String())
	}

	hbA := devtaskDo(t, s, runnerA, http.MethodPost, "/api/v1/devtasks/"+id+"/heartbeat", `{"lease_sec":60}`)
	if hbA.Code != http.StatusOK {
		t.Fatalf("runner A heartbeat status = %d, want 200 OK; body=%s", hbA.Code, hbA.Body.String())
	}
}

func TestDevTaskAuditFailureIsVisible(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	seedDevProject(t, s)

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	submit := devtaskCtx(map[string]bool{"devtask:submit": true})
	rec := devtaskDo(t, s, submit, http.MethodPost, "/api/v1/devtasks", `{"project":"demo","title":"audit-fail","spec_md":"# goal"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch: %d", rec.Code)
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	id := out.TaskID

	if _, err := s.Store.DB.Exec("CREATE TRIGGER test_fail_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(FAIL, 'simulated audit failure'); END;"); err != nil {
		t.Fatal(err)
	}
	s.Store.CloseAuditWriter()

	cancel := devtaskCtx(map[string]bool{"devtask:cancel": true})
	cancelRec := devtaskDo(t, s, cancel, http.MethodPost, "/api/v1/devtasks/"+id+"/cancel", `{}`)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200 despite audit failure; body=%s", cancelRec.Code, cancelRec.Body.String())
	}

	task, err := s.Store.GetDevTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != storage.DevTaskCancelled {
		t.Fatalf("task status = %q, want %q", task.Status, storage.DevTaskCancelled)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "devtask_cancel audit write failed") {
		t.Fatalf("expected audit write failure log, got:\n%s", logOutput)
	}
}

func TestDevTaskNotificationNoBodyLeak(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "on")
	s, _ := newSpillTestServer(t)
	seedDevProject(t, s)

	secretSpec := "SUPER_SECRET_SPEC_CONTENT_998877"
	secretResult := "SUPER_SECRET_RESULT_CONTENT_665544"

	submit := devtaskCtx(map[string]bool{"devtask:submit": true})
	dispatchBody := `{"project":"demo","title":"leak-test","spec_md":"` + secretSpec + `"}`
	rec := devtaskDo(t, s, submit, http.MethodPost, "/api/v1/devtasks", dispatchBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	taskID := out.TaskID

	runner := devtaskCtx(map[string]bool{"devtask:run": true})
	claimRec := devtaskDo(t, s, runner, http.MethodPost, "/api/v1/devtasks/claim", `{}`)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("claim failed: %d %s", claimRec.Code, claimRec.Body.String())
	}

	completeBody := `{"status":"needs_review","commit_sha":"c123","result_md":"` + secretResult + `","test_summary":"passed 3 tests"}`
	completeRec := devtaskDo(t, s, runner, http.MethodPost, "/api/v1/devtasks/"+taskID+"/complete", completeBody)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("complete failed: %d %s", completeRec.Code, completeRec.Body.String())
	}

	var dataJSON string
	err := s.Store.DB.QueryRow("SELECT data_json FROM notification_events WHERE id LIKE 'evt_devtask_%' ORDER BY created_at DESC LIMIT 1").Scan(&dataJSON)
	if err != nil {
		t.Fatalf("query notification event data_json: %v", err)
	}

	if strings.Contains(dataJSON, secretSpec) {
		t.Fatalf("notification payload leaked spec_md: %s", dataJSON)
	}
	if strings.Contains(dataJSON, secretResult) {
		t.Fatalf("notification payload leaked result_md: %s", dataJSON)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["task_id"] != taskID {
		t.Fatalf("payload task_id = %v, want %s", payload["task_id"], taskID)
	}
	if payload["title"] != "leak-test" {
		t.Fatalf("payload title = %v, want leak-test", payload["title"])
	}
	if payload["status"] != "needs_review" {
		t.Fatalf("payload status = %v, want needs_review", payload["status"])
	}
}

func TestDevTaskPanelRoutesRequireGate(t *testing.T) {
	t.Setenv("DEVTASK_ENABLED", "")
	s, _ := newSpillTestServer(t)
	admin := devtaskCtx(map[string]bool{"admin": true, "devtask:submit": true, "devtask:read": true, "devtask:run": true, "devtask:cancel": true})

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/devtasks", ""},
		{http.MethodPost, "/api/v1/devtasks", `{"project":"demo","title":"t","spec_md":"g"}`},
		{http.MethodPost, "/api/v1/devtasks/claim", `{}`},
		{http.MethodGet, "/api/v1/devtasks/dt_dummy", ""},
		{http.MethodGet, "/api/v1/devtasks/dt_dummy/events", ""},
		{http.MethodPost, "/api/v1/devtasks/dt_dummy/revise", `{}`},
		{http.MethodPost, "/api/v1/devtasks/dt_dummy/cancel", `{}`},
		{http.MethodPost, "/api/v1/devtasks/dt_dummy/done", `{}`},
		{http.MethodPost, "/api/v1/devtasks/dt_dummy/heartbeat", `{}`},
		{http.MethodPost, "/api/v1/devtasks/dt_dummy/complete", `{}`},
		{http.MethodGet, "/api/v1/devtask-projects", ""},
		{http.MethodPost, "/api/v1/devtask-projects", `{"name":"p","repo_path":"/tmp","base_ref":"main"}`},
		{http.MethodGet, "/api/v1/devtask-settings", ""},
		{http.MethodPut, "/api/v1/devtask-settings", `{"allowed_models":["m"]}`},
	}

	for _, r := range routes {
		rec := devtaskDo(t, s, admin, r.method, r.path, r.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("route %s %s with DEVTASK_ENABLED='' returned status %d, want 404 Not Found", r.method, r.path, rec.Code)
		}
	}
}
