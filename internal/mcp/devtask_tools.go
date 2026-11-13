package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

const devTaskSpecMaxBytes = 64 * 1024

func devtaskEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEVTASK_ENABLED"))) {
	case "on", "true", "1":
		return true
	default:
		return false
	}
}

func isDevTaskTool(name string) bool {
	switch name {
	case "dev_task_dispatch", "dev_task_status", "dev_task_list", "dev_task_diff", "dev_task_logs", "dev_task_revise", "dev_task_cancel":
		return true
	default:
		return false
	}
}

func devtaskToolDefs() []Tool {
	return []Tool{
		{Name: "dev_task_dispatch", Description: "把编码任务派发到已登记项目。只能选已登记项目和白名单模型；返回 task_id、branch、model", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{
			"project":         {Type: "string", Description: "已登记项目名"},
			"title":           {Type: "string", Description: "任务标题"},
			"spec_md":         {Type: "string", Description: "任务卡原文，最大 64KB"},
			"accept_cmds":     {Type: "array", Description: "验收命令；省略则用项目默认"},
			"allowed_paths":   {Type: "array", Description: "允许改动的路径"},
			"forbidden_paths": {Type: "array", Description: "额外禁止路径，与项目禁止路径取并集"},
			"model":           {Type: "string", Description: "可选；必须在白名单内，省略则用项目默认模型"},
		}, Required: []string{"project", "title", "spec_md"}}},
		{Name: "dev_task_status", Description: "查询任务状态、轮次、模型、结果摘要和错误信息", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{
			"task_id": {Type: "string", Description: "任务 id"},
		}, Required: []string{"task_id"}}},
		{Name: "dev_task_list", Description: "按项目和状态筛选，并分页列出开发任务及其执行状态", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{
			"project": {Type: "string", Description: "项目名，可省略"},
			"status":  {Type: "string", Description: "状态，可省略"},
			"limit":   {Type: "integer", Description: "每页条数，默认 50，最大 200"},
			"offset":  {Type: "integer", Description: "偏移"},
		}}},
		{Name: "dev_task_diff", Description: "返回 runner 上报的 diff_stat，按字节偏移分页。完整补丁要等 runner 交付", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{
			"task_id": {Type: "string", Description: "任务 id"},
			"offset":  {Type: "integer", Description: "起始字节，默认 0"},
			"limit":   {Type: "integer", Description: "最大字节，默认 8192，最大 65536"},
		}, Required: []string{"task_id"}}},
		{Name: "dev_task_logs", Description: "按任务分页读取执行事件记录，且不会写入审计日志", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{
			"task_id":   {Type: "string", Description: "任务 id"},
			"after_seq": {Type: "integer", Description: "只返回 seq 大于该值的事件"},
			"limit":     {Type: "integer", Description: "条数，默认 100，最大 500"},
		}, Required: []string{"task_id"}}},
		{Name: "dev_task_revise", Description: "对 needs_review 或 failed 的任务开新一轮；超过 max_rounds 拒绝", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{
			"task_id": {Type: "string", Description: "任务 id"},
			"note":    {Type: "string", Description: "审核意见，写入事件而不写入审计"},
		}, Required: []string{"task_id"}}},
		{Name: "dev_task_cancel", Description: "取消尚未结束的开发任务，并返回最新任务状态信息", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{
			"task_id": {Type: "string", Description: "任务 id"},
		}, Required: []string{"task_id"}}},
	}
}

func appendDevTaskTools(ctx context.Context, tools []Tool) []Tool {
	if !devtaskEnabled() {
		return tools
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return tools
	}
	for _, tool := range devtaskToolDefs() {
		if scope := mcpToolScope(tool.Name); scope != "" && p.HasScope(scope) {
			tools = append(tools, tool)
		}
	}
	return tools
}

func (s *Server) callDevTaskTool(ctx context.Context, params CallToolParams) (any, bool) {
	if s.OAuthStore == nil {
		return map[string]string{"error": "devtask store unavailable"}, true
	}
	switch params.Name {
	case "dev_task_dispatch":
		return s.devtaskDispatchTool(ctx, params.Arguments)
	case "dev_task_status":
		return s.devtaskStatusTool(params.Arguments)
	case "dev_task_list":
		return s.devtaskListTool(params.Arguments)
	case "dev_task_diff":
		return s.devtaskDiffTool(params.Arguments)
	case "dev_task_logs":
		return s.devtaskLogsTool(params.Arguments)
	case "dev_task_revise":
		return s.devtaskReviseTool(ctx, params.Arguments)
	case "dev_task_cancel":
		return s.devtaskCancelTool(ctx, params.Arguments)
	default:
		return map[string]string{"error": "unknown devtask tool"}, true
	}
}

func (s *Server) devtaskDispatchTool(ctx context.Context, raw json.RawMessage) (any, bool) {
	var in struct {
		Project        string   `json:"project"`
		Title          string   `json:"title"`
		SpecMD         string   `json:"spec_md"`
		AcceptCmds     []string `json:"accept_cmds"`
		AllowedPaths   []string `json:"allowed_paths"`
		ForbiddenPaths []string `json:"forbidden_paths"`
		Model          string   `json:"model"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return map[string]string{"error": "invalid dev_task_dispatch arguments"}, true
	}
	in.Project = strings.TrimSpace(in.Project)
	in.Title = strings.TrimSpace(in.Title)
	in.SpecMD = strings.TrimSpace(in.SpecMD)
	if in.Project == "" || in.Title == "" || in.SpecMD == "" {
		return map[string]string{"error": "project, title and spec_md required"}, true
	}
	if len(in.Title) > 200 {
		return map[string]string{"error": "title exceeds 200 bytes"}, true
	}
	if len(in.SpecMD) > devTaskSpecMaxBytes {
		return map[string]string{"error": "spec_md exceeds 65536 bytes"}, true
	}
	proj, err := s.OAuthStore.GetDevProject(in.Project)
	if errors.Is(err, storage.ErrDevProjectNotFound) {
		return map[string]string{"error": "project not found"}, true
	}
	if err != nil {
		return map[string]string{"error": "load project failed"}, true
	}
	if !proj.Enabled {
		return map[string]string{"error": "project disabled"}, true
	}
	allowed, err := s.OAuthStore.GetDevTaskAllowedModels()
	if err != nil {
		return map[string]string{"error": "read model whitelist failed"}, true
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = proj.DefaultModel
	}
	if !containsString(allowed, model) {
		return map[string]string{"error": fmt.Sprintf("model %q is not in the devtask model whitelist", model)}, true
	}
	accept := in.AcceptCmds
	if len(accept) == 0 {
		accept = decodeStringList(proj.DefaultAcceptCmdsJSON)
	}
	forbidden := unionStrings(decodeStringList(proj.ForbiddenPathsJSON), in.ForbiddenPaths)
	id, err := newDevTaskID()
	if err != nil {
		return map[string]string{"error": "id generation failed"}, true
	}
	p, _ := auth.PrincipalFromContext(ctx)
	t := storage.DevTask{
		ID: id, Project: proj.Name, Title: in.Title, SpecMD: in.SpecMD,
		AcceptCmdsJSON: marshalStringList(accept), AllowedPathsJSON: marshalStringList(in.AllowedPaths),
		ForbiddenPathsJSON: marshalStringList(forbidden),
		BaseRef:            proj.BaseRef, Branch: "task/" + id, Model: model,
		Status: storage.DevTaskQueued, Round: 1, MaxRounds: proj.MaxRounds, MaxSteps: proj.MaxSteps, TimeoutS: proj.TimeoutS,
		ActorName: p.AuditName(), OAuthClientID: p.OAuthClient,
	}
	if err := s.OAuthStore.CreateDevTask(t); err != nil {
		return map[string]string{"error": "create task failed"}, true
	}
	s.auditDevTask(ctx, "devtask_dispatch", id, fmt.Sprintf("model=%s title=%s", model, in.Title))
	return map[string]any{"task_id": id, "branch": t.Branch, "model": model}, false
}

func (s *Server) devtaskStatusTool(raw json.RawMessage) (any, bool) {
	id, err := devtaskRequiredID(raw)
	if err != nil {
		return map[string]string{"error": err.Error()}, true
	}
	t, err := s.OAuthStore.GetDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		return map[string]string{"error": "not found"}, true
	}
	if err != nil {
		return map[string]string{"error": "load failed"}, true
	}
	return devtaskStatusView(t), false
}

func (s *Server) devtaskListTool(raw json.RawMessage) (any, bool) {
	var in struct {
		Project string `json:"project"`
		Status  string `json:"status"`
		Limit   int    `json:"limit"`
		Offset  int    `json:"offset"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return map[string]string{"error": "invalid dev_task_list arguments"}, true
		}
	}
	list, err := s.OAuthStore.ListDevTasks(in.Project, in.Status, in.Limit, in.Offset)
	if err != nil {
		return map[string]string{"error": "list failed"}, true
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"task_id": t.ID, "project": t.Project, "title": t.Title, "status": t.Status,
			"round": t.Round, "model": t.Model, "branch": t.Branch, "created_at": t.CreatedAt,
		})
	}
	return map[string]any{"tasks": out}, false
}

func (s *Server) devtaskDiffTool(raw json.RawMessage) (any, bool) {
	var in struct {
		TaskID string `json:"task_id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.TaskID) == "" {
		return map[string]string{"error": "task_id required"}, true
	}
	t, err := s.OAuthStore.GetDevTask(strings.TrimSpace(in.TaskID))
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		return map[string]string{"error": "not found"}, true
	}
	if err != nil {
		return map[string]string{"error": "load failed"}, true
	}
	page, total := paginateBytes(t.DiffStat, in.Offset, in.Limit)
	return map[string]any{
		"task_id": t.ID, "base_sha": t.BaseSHA, "commit_sha": t.CommitSHA,
		"offset": in.Offset, "diff": page, "total_bytes": total,
		"note": "diff is the runner-reported diff_stat",
	}, false
}

func (s *Server) devtaskLogsTool(raw json.RawMessage) (any, bool) {
	var in struct {
		TaskID   string `json:"task_id"`
		AfterSeq int    `json:"after_seq"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.TaskID) == "" {
		return map[string]string{"error": "task_id required"}, true
	}
	events, err := s.OAuthStore.ListDevTaskEvents(strings.TrimSpace(in.TaskID), in.AfterSeq, in.Limit)
	if err != nil {
		return map[string]string{"error": "list events failed"}, true
	}
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{"task_id": e.TaskID, "seq": e.Seq, "ts": e.TS, "round": e.Round, "type": e.Type, "payload": json.RawMessage(e.PayloadJSON)})
	}
	return map[string]any{"events": out}, false
}

func (s *Server) devtaskReviseTool(ctx context.Context, raw json.RawMessage) (any, bool) {
	var in struct {
		TaskID string `json:"task_id"`
		Note   string `json:"note"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.TaskID) == "" {
		return map[string]string{"error": "task_id required"}, true
	}
	id := strings.TrimSpace(in.TaskID)
	t, err := s.OAuthStore.ReviseDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		return map[string]string{"error": "not found"}, true
	}
	if errors.Is(err, storage.ErrDevTaskIllegalTransition) || errors.Is(err, storage.ErrDevTaskConflict) {
		return map[string]string{"error": err.Error()}, true
	}
	if err != nil {
		return map[string]string{"error": "revise failed"}, true
	}
	if note := strings.TrimSpace(in.Note); note != "" {
		payload, _ := json.Marshal(map[string]string{"note": note})
		_ = s.OAuthStore.AppendDevTaskEvents(id, t.Round, []storage.DevTaskEvent{{Type: "note", PayloadJSON: string(payload)}})
	}
	s.auditDevTask(ctx, "devtask_revise", id, fmt.Sprintf("round=%d", t.Round))
	return devtaskStatusView(t), false
}

func (s *Server) devtaskCancelTool(ctx context.Context, raw json.RawMessage) (any, bool) {
	id, err := devtaskRequiredID(raw)
	if err != nil {
		return map[string]string{"error": err.Error()}, true
	}
	err = s.OAuthStore.CancelDevTask(id)
	if errors.Is(err, storage.ErrDevTaskNotFound) {
		return map[string]string{"error": "not found"}, true
	}
	if errors.Is(err, storage.ErrDevTaskIllegalTransition) || errors.Is(err, storage.ErrDevTaskConflict) {
		return map[string]string{"error": err.Error()}, true
	}
	if err != nil {
		return map[string]string{"error": "cancel failed"}, true
	}
	s.auditDevTask(ctx, "devtask_cancel", id, "")
	return map[string]any{"ok": true, "task_id": id, "status": storage.DevTaskCancelled}, false
}

func (s *Server) auditDevTask(ctx context.Context, action, taskID, detail string) {
	if s.OAuthStore == nil {
		return
	}
	p, _ := auth.PrincipalFromContext(ctx)
	_ = s.OAuthStore.AuditEntry(storage.AuditEntry{
		Timestamp:     time.Now().Unix(),
		Action:        action,
		Device:        taskID,
		Command:       detail,
		ActorType:     p.Type,
		ActorID:       p.ID,
		ActorName:     p.AuditName(),
		OAuthClientID: p.OAuthClient,
		Transport:     "mcp",
		TargetType:    "devtask",
		Target:        taskID,
		Result:        "ok",
	})
}

func devtaskStatusView(t storage.DevTask) map[string]any {
	result := t.ResultMD
	if len(result) > 4096 {
		result = result[:4096]
	}
	return map[string]any{
		"task_id": t.ID, "project": t.Project, "title": t.Title, "status": t.Status,
		"round": t.Round, "max_rounds": t.MaxRounds, "model": t.Model, "branch": t.Branch,
		"base_sha": t.BaseSHA, "commit_sha": t.CommitSHA, "test_summary": t.TestSummary,
		"result_md": result, "error_code": t.ErrorCode, "error_detail": t.ErrorDetail,
	}
}

func devtaskRequiredID(raw json.RawMessage) (string, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", errors.New("task_id required")
	}
	id := strings.TrimSpace(in.TaskID)
	if id == "" {
		return "", errors.New("task_id required")
	}
	return id, nil
}

func newDevTaskID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "dt_" + hex.EncodeToString(b), nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func decodeStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func marshalStringList(items []string) string {
	if items == nil {
		items = []string{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func unionStrings(a, b []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(a)+len(b))
	for _, item := range append(append([]string{}, a...), b...) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func paginateBytes(s string, offset, limit int) (string, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(s) {
		offset = len(s)
	}
	if limit <= 0 {
		limit = 8192
	}
	if limit > 65536 {
		limit = 65536
	}
	end := offset + limit
	if end > len(s) {
		end = len(s)
	}
	return s[offset:end], len(s)
}
