package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func newDevTaskID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "dt_" + hex.EncodeToString(b), nil
}

func taskToJSON(t storage.DevTask) map[string]any {
	return map[string]any{
		"id": t.ID, "project": t.Project, "title": t.Title, "spec_md": t.SpecMD,
		"accept_cmds": unmarshalStrings(t.AcceptCmdsJSON), "allowed_paths": unmarshalStrings(t.AllowedPathsJSON),
		"forbidden_paths": unmarshalStrings(t.ForbiddenPathsJSON), "base_ref": t.BaseRef, "base_sha": t.BaseSHA,
		"branch": t.Branch, "model": t.Model, "status": t.Status, "round": t.Round, "max_rounds": t.MaxRounds,
		"max_steps": t.MaxSteps, "timeout_s": t.TimeoutS, "parent_task_id": t.ParentTaskID,
		"commit_sha": t.CommitSHA, "diff_stat": t.DiffStat, "test_summary": t.TestSummary, "result_md": t.ResultMD,
		"error_code": t.ErrorCode, "error_detail": t.ErrorDetail, "actor_name": t.ActorName,
		"oauth_client_id": t.OAuthClientID, "claimed_by": t.ClaimedBy, "lease_until": t.LeaseUntil,
		"heartbeat_at": t.HeartbeatAt, "lease_requeues": t.LeaseRequeues,
		"created_at": t.CreatedAt, "started_at": t.StartedAt, "finished_at": t.FinishedAt,
	}
}

func principalActor(p auth.Principal) (name, oauthClient string) {
	name = p.AuditName()
	if name == "" {
		name = p.Name
	}
	oauthClient = p.OAuthClient
	return
}

func (s *Server) devtaskDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Project        string   `json:"project"`
		Title          string   `json:"title"`
		SpecMD         string   `json:"spec_md"`
		AcceptCmds     []string `json:"accept_cmds"`
		AllowedPaths   []string `json:"allowed_paths"`
		ForbiddenPaths []string `json:"forbidden_paths"`
		Model          string   `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	in.Project = strings.TrimSpace(in.Project)
	in.Title = strings.TrimSpace(in.Title)
	if in.Project == "" || in.Title == "" || strings.TrimSpace(in.SpecMD) == "" {
		http.Error(w, "project, title and spec_md required", http.StatusBadRequest)
		return
	}
	proj, err := s.Store.GetDevProject(in.Project)
	if errors.Is(err, storage.ErrDevProjectNotFound) {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "load project failed", http.StatusInternalServerError)
		return
	}
	if !proj.Enabled {
		http.Error(w, "project disabled", http.StatusBadRequest)
		return
	}
	allowed, err := s.Store.GetDevTaskAllowedModels()
	if err != nil {
		http.Error(w, "read model whitelist failed", http.StatusInternalServerError)
		return
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = proj.DefaultModel
	}
	if !containsString(allowed, model) {
		http.Error(w, fmt.Sprintf("model %q is not in the devtask model whitelist", model), http.StatusBadRequest)
		return
	}
	accept := in.AcceptCmds
	if len(accept) == 0 {
		accept = unmarshalStrings(proj.DefaultAcceptCmdsJSON)
	}
	id, err := newDevTaskID()
	if err != nil {
		http.Error(w, "id generation failed", http.StatusInternalServerError)
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	actor, oauth := principalActor(p)
	t := storage.DevTask{
		ID: id, Project: proj.Name, Title: in.Title, SpecMD: in.SpecMD,
		AcceptCmdsJSON: marshalStrings(accept), AllowedPathsJSON: marshalStrings(in.AllowedPaths),
		ForbiddenPathsJSON: marshalStrings(in.ForbiddenPaths),
		BaseRef:            proj.BaseRef, Branch: "task/" + id, Model: model,
		Status: storage.DevTaskQueued, Round: 1, MaxRounds: proj.MaxRounds, MaxSteps: proj.MaxSteps, TimeoutS: proj.TimeoutS,
		ActorName: actor, OAuthClientID: oauth,
	}
	if err := s.Store.CreateDevTask(t); err != nil {
		http.Error(w, "create task failed", http.StatusInternalServerError)
		return
	}
	if err := s.audit(r, id, "devtask_dispatch", proj.Name, 0, 0, fmt.Sprintf("model=%s title=%s", model, in.Title)); err != nil {
		slog.Error("devtask_dispatch audit write failed", "error", err, "task_id", id, "project", proj.Name)
	}
	jsonOut(w, map[string]any{"task_id": id, "branch": t.Branch, "model": model})
}

func (s *Server) devtaskList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.Store.ListDevTasks(r.URL.Query().Get("project"), r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, taskToJSON(t))
	}
	jsonOut(w, map[string]any{"tasks": out})
}

func (s *Server) devtaskGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/devtasks/")
	id = strings.SplitN(id, "/", 2)[0]
	t, err := s.Store.GetDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	jsonOut(w, taskToJSON(t))
}

func (s *Server) devtaskEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/devtasks/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after_seq"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.Store.ListDevTaskEvents(parts[0], after, limit)
	if err != nil {
		http.Error(w, "list events failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{"task_id": e.TaskID, "seq": e.Seq, "ts": e.TS, "round": e.Round, "type": e.Type, "payload": json.RawMessage(e.PayloadJSON)})
	}
	jsonOut(w, map[string]any{"events": out})
}

func (s *Server) devtaskRevise(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := devtaskIDFromActionPath(r.URL.Path, "revise")
	t, err := s.Store.ReviseDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, storage.ErrDevTaskIllegalTransition) || errors.Is(err, storage.ErrDevTaskConflict) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "revise failed", http.StatusInternalServerError)
		return
	}
	if err := s.audit(r, id, "devtask_revise", t.Project, 0, 0, fmt.Sprintf("round=%d", t.Round)); err != nil {
		slog.Error("devtask_revise audit write failed", "error", err, "task_id", id, "project", t.Project)
	}
	jsonOut(w, taskToJSON(t))
}

func (s *Server) devtaskCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := devtaskIDFromActionPath(r.URL.Path, "cancel")
	err := s.Store.CancelDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, storage.ErrDevTaskIllegalTransition) || errors.Is(err, storage.ErrDevTaskConflict) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "cancel failed", http.StatusInternalServerError)
		return
	}
	if err := s.audit(r, id, "devtask_cancel", "", 0, 0, ""); err != nil {
		slog.Error("devtask_cancel audit write failed", "error", err, "task_id", id)
	}
	jsonOut(w, map[string]any{"ok": true, "id": id, "status": storage.DevTaskCancelled})
}

func (s *Server) devtaskDone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := devtaskIDFromActionPath(r.URL.Path, "done")
	err := s.Store.MarkDoneDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, storage.ErrDevTaskIllegalTransition) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "done failed", http.StatusInternalServerError)
		return
	}
	if err := s.audit(r, id, "devtask_done", "", 0, 0, ""); err != nil {
		slog.Error("devtask_done audit write failed", "error", err, "task_id", id)
	}
	jsonOut(w, map[string]any{"ok": true, "id": id, "status": storage.DevTaskDone})
}

func (s *Server) devtaskClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	claimedBy := p.TokenID
	if claimedBy == "" {
		claimedBy = p.ID
	}
	var in struct {
		ClaimedBy string `json:"claimed_by"`
		LeaseSec  int    `json:"lease_sec"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	if strings.TrimSpace(in.ClaimedBy) != "" {
		claimedBy = strings.TrimSpace(in.ClaimedBy)
	}
	t, err := s.Store.ClaimDevTask(claimedBy, in.LeaseSec)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if errors.Is(err, storage.ErrDevTaskConflict) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "claim failed", http.StatusInternalServerError)
		return
	}
	jsonOut(w, taskToJSON(t))
}

func (s *Server) devtaskHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := devtaskIDFromActionPath(r.URL.Path, "heartbeat")
	p, _ := auth.PrincipalFromContext(r.Context())
	claimedBy := p.TokenID
	if claimedBy == "" {
		claimedBy = p.ID
	}
	var in struct {
		LeaseSec int `json:"lease_sec"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	if err := s.Store.HeartbeatDevTask(id, claimedBy, in.LeaseSec); err != nil {
		if errors.Is(err, storage.ErrDevTaskConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "heartbeat failed", http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}

func (s *Server) devtaskPostEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := devtaskIDFromActionPath(r.URL.Path, "events")
	t, err := s.Store.GetDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	var in struct {
		Events []struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
			Round   int             `json:"round"`
		} `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	evs := make([]storage.DevTaskEvent, 0, len(in.Events))
	for _, e := range in.Events {
		payload := string(e.Payload)
		if payload == "" {
			payload = "{}"
		}
		evs = append(evs, storage.DevTaskEvent{Type: e.Type, PayloadJSON: payload, Round: e.Round})
	}
	if err := s.Store.AppendDevTaskEvents(id, t.Round, evs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonOut(w, map[string]any{"ok": true, "count": len(evs)})
}

func (s *Server) devtaskComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := devtaskIDFromActionPath(r.URL.Path, "complete")
	p, _ := auth.PrincipalFromContext(r.Context())
	claimedBy := p.TokenID
	if claimedBy == "" {
		claimedBy = p.ID
	}
	var in storage.CompleteDevTaskResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.Store.CompleteDevTask(id, claimedBy, in); err != nil {
		if errors.Is(err, storage.ErrDevTaskConflict) || errors.Is(err, storage.ErrDevTaskIllegalTransition) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "complete failed", http.StatusInternalServerError)
		return
	}
	s.emitDevTaskNotification(id, in)
	jsonOut(w, map[string]any{"ok": true, "id": id, "status": in.Status})
}

func (s *Server) emitDevTaskNotification(taskID string, result storage.CompleteDevTaskResult) {
	if s.Store == nil {
		return
	}
	var eventType, templateID string
	switch result.Status {
	case storage.DevTaskNeedsReview:
		eventType, templateID = "devtask.needs_review", "devtask-needs-review"
	case storage.DevTaskFailed:
		eventType, templateID = "devtask.failed", "devtask-failed"
	case storage.DevTaskBlockedModel:
		eventType, templateID = "devtask.blocked_model", "devtask-blocked-model"
	default:
		return
	}
	task, err := s.Store.GetDevTask(taskID)
	if err != nil {
		return
	}
	// Never include spec_md or result_md in notification payload
	summary := result.TestSummary
	if summary == "" {
		summary = result.ErrorDetail
	}
	if summary == "" {
		summary = result.Status
	}
	data, _ := json.Marshal(map[string]any{
		"task_id": task.ID, "project": task.Project, "title": task.Title,
		"status": result.Status, "model": task.Model, "round": task.Round,
		"summary": summary,
	})
	msg := fmt.Sprintf("%s %s (%s)", eventType, task.Title, task.ID)
	evt := storage.NotificationEvent{
		ID: "evt_devtask_" + task.ID + "_" + result.Status, TemplateID: templateID, TemplateFormat: "text",
		EventType: eventType, Severity: "P3", Source: "devtask", Message: msg,
		DataJSON: string(data), CreatedAt: time.Now().Unix(),
	}
	_, _, _, _ = s.Store.AcceptNotificationEvent(evt)
}

func devtaskIDFromActionPath(path, action string) string {
	path = strings.TrimPrefix(path, "/api/v1/devtasks/")
	path = strings.TrimSuffix(path, "/"+action)
	return path
}

// devtaskRouter dispatches /api/v1/devtasks and subpaths after the feature gate and scope checks are applied per-route.
func (s *Server) registerDevTaskRoutes(mux *http.ServeMux) {
	mux.Handle("/api/v1/devtasks", devtaskGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.requireScope("devtask:submit", http.HandlerFunc(s.devtaskDispatch)).ServeHTTP(w, r)
		case http.MethodGet:
			s.requireScope("devtask:read", http.HandlerFunc(s.devtaskList)).ServeHTTP(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})))
	mux.Handle("/api/v1/devtasks/claim", devtaskGate(s.requireScope("devtask:run", http.HandlerFunc(s.devtaskClaim))))

	// Sub-resource router: /api/v1/devtasks/{id}[/{action}]
	mux.Handle("/api/v1/devtasks/", devtaskGate(http.HandlerFunc(s.devtaskSubroutes)))
}

func (s *Server) devtaskSubroutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/devtasks/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		s.requireScope("devtask:read", http.HandlerFunc(s.devtaskGet)).ServeHTTP(w, r)
		return
	}
	switch parts[1] {
	case "events":
		if r.Method == http.MethodGet {
			s.requireScope("devtask:read", http.HandlerFunc(s.devtaskEvents)).ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost {
			s.requireScope("devtask:run", http.HandlerFunc(s.devtaskPostEvents)).ServeHTTP(w, r)
			return
		}
	case "revise":
		s.requireScope("devtask:submit", http.HandlerFunc(s.devtaskRevise)).ServeHTTP(w, r)
		return
	case "cancel":
		s.requireScope("devtask:cancel", http.HandlerFunc(s.devtaskCancel)).ServeHTTP(w, r)
		return
	case "done":
		s.requireScope("devtask:submit", http.HandlerFunc(s.devtaskDone)).ServeHTTP(w, r)
		return
	case "heartbeat":
		s.requireScope("devtask:run", http.HandlerFunc(s.devtaskHeartbeat)).ServeHTTP(w, r)
		return
	case "complete":
		s.requireScope("devtask:run", http.HandlerFunc(s.devtaskComplete)).ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}
