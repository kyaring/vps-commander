package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auditutil"
	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/storage"
)

var tokenAlphabet = []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz")

func randomTokenPart(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	for i := range raw {
		raw[i] = tokenAlphabet[int(raw[i])%len(tokenAlphabet)]
	}
	return string(raw)
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.PrincipalFromContext(r.Context())
	return p
}

func auditTargetValue(action, command string) string {
	if (strings.Contains(action, "file") || action == "edit_block" || action == "create_directory") && strings.Contains(command, " (bytes=") {
		return strings.SplitN(command, " (bytes=", 2)[0]
	}
	return command
}

func (s *Server) auditContext(ctx context.Context, ip, device, action, command string, code int, ms int64, msg string) error {
	target := device
	if strings.HasPrefix(action, "token_") || strings.Contains(action, "file") || action == "edit_block" || strings.Contains(action, "webhook") || strings.Contains(action, "notification") {
		target = auditTargetValue(action, command)
	}
	return s.auditContextTarget(ctx, ip, device, action, target, command, code, ms, msg)
}

func (s *Server) auditContextTarget(ctx context.Context, ip, device, action, target, command string, code int, ms int64, msg string) error {
	p, _ := auth.PrincipalFromContext(ctx)
	targetType := "device"
	if strings.HasPrefix(action, "token_") {
		targetType = "token"
	} else if strings.Contains(action, "file") || action == "edit_block" {
		targetType = "file"
	} else if strings.Contains(action, "webhook") || strings.Contains(action, "notification") {
		targetType = "webhook"
	}
	result := "ok"
	transport := "rest"
	if p.Type == "panel" {
		transport = "panel"
	}
	if concrete := mcp.TransportFromContext(ctx); concrete != "" {
		transport = concrete
	}
	if code != 0 {
		result = "error"
	}
	if action == "device_denied" {
		targetType = "device"
		target = device
		result = "denied"
		command = device
		code = 0
	}
	if action == "mcp_denied" {
		targetType = "device"
		target = device
		result = "denied"
		code = 0
	}
	if action == "scope_denied" {
		if device == "" {
			targetType = "scope"
		} else {
			targetType = "device"
			target = device
		}
		result = "denied"
		code = 0
	}
	if !auditReadListEnabled() && code == 0 && isReadListAuditAction(action) {
		mcp.MarkAuditRecorded(ctx)
		return nil
	}
	err := s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Device: device, Action: action, Command: command, ExitCode: code, DurationMS: ms, ErrorMsg: msg, ActorType: p.Type, ActorID: p.ID, ActorName: p.AuditName(), OAuthClientID: p.OAuthClient, Transport: transport, TargetType: targetType, Target: target, Result: result})
	mcp.MarkAuditRecorded(ctx)
	return err
}

func (s *Server) AuditMCP(ctx context.Context, ip, device, tool, command, result, detail string) {
	device = s.normalizeDevice(device)
	if command == "" {
		command = tool
	}
	_ = s.auditMCPContext(ctx, ip, device, "mcp_denied", command, 0, 0, result+": "+detail)
}

func (s *Server) auditMCPContext(ctx context.Context, ip, device, action, command string, code int, ms int64, msg string) error {
	device = s.normalizeDevice(device)
	if action == "security_denied" {
		if intent := mcp.AuditIntentFromContext(ctx); intent != "" {
			command = intent
		}
	}
	p, _ := auth.PrincipalFromContext(ctx)
	targetType := "device"
	if action == "mcp_call" {
		targetType = "mcp_tool"
	} else if strings.Contains(action, "file") || action == "edit_block" || action == "create_directory" || action == "move_file" {
		targetType = "file"
	}
	result := "ok"
	if code != 0 {
		result = "error"
	}
	if action == "mcp_denied" || action == "security_denied" {
		result = "denied"
		code = 0
	}
	target := device
	if targetType == "mcp_tool" {
		target = command
	}
	if targetType == "file" {
		target = auditTargetValue(action, command)
	}
	if !auditReadListEnabled() && code == 0 && isReadListAuditAction(action) {
		mcp.MarkAuditRecorded(ctx)
		return nil
	}
	transport := mcp.TransportFromContext(ctx)
	if transport == "" {
		transport = "mcp"
	}
	err := s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Device: device, Action: action, Command: command, ExitCode: code, DurationMS: ms, ErrorMsg: msg, ActorType: p.Type, ActorID: p.ID, ActorName: p.AuditName(), OAuthClientID: p.OAuthClient, Transport: transport, TargetType: targetType, Target: target, Result: result})
	mcp.MarkAuditRecorded(ctx)
	return err
}

func (s *Server) audit(r *http.Request, device, action, command string, code int, ms int64, msg string) error {
	return s.auditContext(r.Context(), auth.ClientIP(r), device, action, command, code, ms, msg)
}

func requestAuditIntent(r *http.Request) (device, targetType, target, command string) {
	device, targetType = "", "device"
	path := r.URL.Path
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	get := func(key string) string { v, _ := fields[key].(string); return strings.TrimSpace(v) }
	device = get("device")
	switch {
	case strings.HasSuffix(path, "/exec"):
		command = get("command")
	case strings.Contains(path, "/file/read"), strings.Contains(path, "/file/list"), strings.Contains(path, "/file/info"), strings.Contains(path, "/file/search"):
		command = get("path")
		if command == "" {
			command = get("root")
		}
	case strings.Contains(path, "/file/write"):
		command = get("path") + fmt.Sprintf(" (bytes=%d)", len(get("content")))
	case strings.Contains(path, "/edit_block"):
		command = get("path") + fmt.Sprintf(" (bytes=%d)", len(get("new_text")))
	case strings.Contains(path, "/process/session"):
		command = "session=" + get("session_id")
	case strings.Contains(path, "/mcp/call"):
		command = get("server") + "/" + get("tool")
	case strings.Contains(path, "/mcp/list"):
		command = "list_agent_mcp"
	default:
		command = path
	}
	if command == "" {
		command = path
	}
	if device == "" {
		target = scopeTarget(path)
	} else {
		target = device
	}
	return device, targetType, target, auditutil.Truncate(auditutil.Redact(command))
}

func scopeTarget(path string) string { return path }

func (s *Server) requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principal(r).HasScope(scope) {
			device, _, target, command := requestAuditIntent(r)
			if device != "" {
				device = s.normalizeDevice(device)
			}
			if device == "" {
				target = scope
			} else {
				target = device
			}
			_ = s.auditContextTarget(r.Context(), auth.ClientIP(r), device, "scope_denied", target, command, http.StatusForbidden, 0, "required scope denied")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"scope %s required"}`, scope)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func auditActorLabel(v storage.AuditLog) string {
	if v.ActorType == "panel" || v.Transport == "panel" {
		return "面板"
	}
	if v.ActorID == "legacy-env" || v.ActorName == "legacy" || v.ActorName == "legacy-env" || v.ActorName == "unknown(legacy)" {
		return "legacy"
	}
	return v.ActorName
}

func (s *Server) AuditExportCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.Store == nil {
		http.Error(w, "audit query failed", http.StatusInternalServerError)
		return
	}
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	maxRows := 50000
	if raw := strings.TrimSpace(r.URL.Query().Get("max_rows")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < maxRows {
			maxRows = n
		}
	}
	actor, device, action, result, transport := r.URL.Query().Get("actor"), r.URL.Query().Get("device"), r.URL.Query().Get("action"), r.URL.Query().Get("result"), r.URL.Query().Get("transport")
	query := func(cursor int64, limit int) ([]storage.AuditLog, error) {
		return s.Store.QueryAudits(actor, device, action, result, transport, from, to, cursor, limit)
	}
	page, err := query(0, min(500, maxRows))
	if err != nil {
		slog.Error("audit CSV export failed before first page", "error", err)
		http.Error(w, "audit export query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=audits.csv")
	c := csv.NewWriter(w)
	if err := c.Write([]string{"id", "timestamp", "actor_name", "oauth_client_id", "actor_type", "transport", "device", "action", "target_type", "target", "result", "command", "exit_code", "duration_ms", "error_msg"}); err != nil {
		slog.Error("audit CSV header write failed", "error", err)
		return
	}
	rowsWritten := 0
	cursor := int64(0)
	writeRows := func(rows []storage.AuditLog) error {
		for _, v := range rows {
			if err := c.Write([]string{fmt.Sprint(v.ID), fmt.Sprint(v.Timestamp), auditActorLabel(v), v.OAuthClientID, v.ActorType, v.Transport, v.Device, v.Action, v.TargetType, v.Target, v.Result, v.Command, fmt.Sprint(v.ExitCode), fmt.Sprint(v.DurationMS), v.ErrorMsg}); err != nil {
				return err
			}
			rowsWritten++
			cursor = v.ID
		}
		c.Flush()
		return c.Error()
	}
	for {
		if err := writeRows(page); err != nil {
			slog.Error("audit CSV export write failed", "error", err)
			return
		}
		if rowsWritten >= maxRows {
			more, queryErr := query(cursor, 1)
			if queryErr != nil {
				slog.Error("audit CSV export failed after streaming began", "error", queryErr, "rows", rowsWritten)
				_ = c.Write([]string{"# error: export incomplete"})
				c.Flush()
				return
			}
			if len(more) > 0 {
				_ = c.Write([]string{fmt.Sprintf("# truncated at %d rows", rowsWritten)})
				c.Flush()
			}
			return
		}
		if len(page) < min(500, maxRows-rowsWritten) {
			return
		}
		page, err = query(cursor, min(500, maxRows-rowsWritten))
		if err != nil {
			slog.Error("audit CSV export failed after streaming began", "error", err, "rows", rowsWritten)
			_ = c.Write([]string{"# error: export incomplete"})
			c.Flush()
			return
		}
		if len(page) == 0 {
			return
		}
	}
}

func (s *Server) TokenListJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	tokens, err := s.Store.ListAPITokens()
	if err != nil {
		http.Error(w, "token query failed", 500)
		return
	}
	out := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		var scopes any
		_ = json.Unmarshal([]byte(t.ScopesJSON), &scopes)
		var devices any
		if t.DevicesJSON != "" {
			_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "prefix": t.Prefix, "scopes": scopes, "devices": devices, "rate_rps": t.RateRPS, "max_sessions": t.MaxSessions, "note": t.Note, "created_at": t.CreatedAt, "created_by": t.CreatedBy, "expires_at": t.ExpiresAt, "last_used_at": t.LastUsedAt, "last_used_ip": t.LastUsedIP, "revoked_at": t.RevokedAt, "is_legacy": t.IsLegacy})
	}
	jsonOut(w, out)
}

func (s *Server) normalizeTokenDevices(devices []string) ([]string, error) {
	if len(devices) == 0 {
		return []string{}, nil
	}
	known := make(map[string]bool)
	if s.LocalName != "" {
		known[s.LocalName] = true
		known["local"] = true
	}
	if s.Cluster != nil {
		for _, d := range s.Cluster.Devices() {
			if name, ok := d["name"].(string); ok && strings.TrimSpace(name) != "" {
				known[name] = true
			}
		}
	}
	out := make([]string, 0, len(devices))
	seen := make(map[string]bool)
	for _, raw := range devices {
		device := s.normalizeDevice(strings.TrimSpace(raw))
		if device == "" || !known[device] {
			return nil, fmt.Errorf("unknown device: %q", raw)
		}
		if !seen[device] {
			seen[device] = true
			out = append(out, device)
		}
	}
	return out, nil
}

func (s *Server) TokenCreateJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var q map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&q); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	name, _ := q["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	scopes := []string{"devices:read"}
	if a, ok := q["scopes"].([]any); ok && len(a) > 0 {
		scopes = nil
		for _, v := range a {
			if x, ok := v.(string); ok {
				scopes = append(scopes, x)
			}
		}
	}
	allowed := map[string]bool{"devices:read": true, "files:read": true, "files:write": true, "exec": true, "process": true, "mcp:call": true, "admin": true}
	for _, v := range scopes {
		if !allowed[v] {
			http.Error(w, "invalid scope: "+v, 400)
			return
		}
	}
	devices := []string{}
	if a, ok := q["devices"].([]any); ok {
		for _, v := range a {
			if x, ok := v.(string); ok {
				devices = append(devices, x)
			}
		}
	}
	devices, err := s.normalizeTokenDevices(devices)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	rate := 10
	if x, ok := q["rate_rps"].(float64); ok && int(x) > 0 {
		rate = int(x)
	}
	sessions := 10
	if x, ok := q["max_sessions"].(float64); ok && int(x) > 0 {
		sessions = int(x)
	}
	note, _ := q["note"].(string)
	id := randomTokenPart(16)
	prefix := randomTokenPart(8)
	token := "vpc_" + prefix + "_" + randomTokenPart(32)
	scopesJSON, _ := json.Marshal(scopes)
	devicesJSON, _ := json.Marshal(devices)
	t := storage.APIToken{ID: id, Name: name, Prefix: prefix, TokenHash: storage.HashAPIToken(token), ScopesJSON: string(scopesJSON), DevicesJSON: string(devicesJSON), RateRPS: rate, MaxSessions: sessions, Note: note, CreatedAt: time.Now().Unix(), CreatedBy: "api:admin"}
	if err := s.Store.CreateAPIToken(t); err != nil {
		http.Error(w, "token create failed", 409)
		return
	}
	_ = s.audit(r, "", "token_create", name, 0, 0, "")
	jsonOut(w, map[string]any{"id": id, "name": name, "token": token, "prefix": prefix, "scopes": scopes})
}

func (s *Server) TokenPatchJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/")
	tokens, err := s.Store.ListAPITokens()
	if err != nil {
		http.Error(w, "token lookup failed", 500)
		return
	}
	var old *storage.APIToken
	for i := range tokens {
		if tokens[i].ID == id {
			old = &tokens[i]
			break
		}
	}
	if old == nil {
		http.Error(w, "token not found", 404)
		return
	}
	var q map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&q); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	name := old.Name
	if x, ok := q["name"].(string); ok && strings.TrimSpace(x) != "" {
		name = strings.TrimSpace(x)
	}
	scopesJSON := old.ScopesJSON
	if a, ok := q["scopes"].([]any); ok {
		sc := []string{}
		allowed := map[string]bool{"devices:read": true, "files:read": true, "files:write": true, "exec": true, "process": true, "mcp:call": true, "admin": true}
		for _, v := range a {
			x, ok := v.(string)
			if !ok || !allowed[x] {
				http.Error(w, "invalid scope", 400)
				return
			}
			sc = append(sc, x)
		}
		b, _ := json.Marshal(sc)
		scopesJSON = string(b)
	}
	devicesJSON := old.DevicesJSON
	if a, ok := q["devices"].([]any); ok {
		ds := []string{}
		for _, v := range a {
			if x, ok := v.(string); ok {
				ds = append(ds, x)
			}
		}
		ds, err = s.normalizeTokenDevices(ds)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := json.Marshal(ds)
		devicesJSON = string(b)
	}
	rate := old.RateRPS
	if x, ok := q["rate_rps"].(float64); ok {
		rate = int(x)
	}
	maxSessions := old.MaxSessions
	if x, ok := q["max_sessions"].(float64); ok {
		maxSessions = int(x)
	}
	note := old.Note
	if x, ok := q["note"].(string); ok {
		note = x
	}
	if err := s.Store.UpdateAPIToken(id, name, scopesJSON, devicesJSON, rate, maxSessions, note, old.ExpiresAt); err != nil {
		http.Error(w, "token update failed", 500)
		return
	}
	_ = s.audit(r, "", "token_update", name, 0, 0, "")
	jsonOut(w, map[string]any{"ok": true})
}

func (s *Server) TokenRotateJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/"), "/rotate")
	tokens, err := s.Store.ListAPITokens()
	if err != nil {
		http.Error(w, "token lookup failed", 500)
		return
	}
	var old *storage.APIToken
	for i := range tokens {
		if tokens[i].ID == id {
			old = &tokens[i]
			break
		}
	}
	if old == nil {
		http.Error(w, "token not found", 404)
		return
	}
	prefix := randomTokenPart(8)
	token := "vpc_" + prefix + "_" + randomTokenPart(32)
	if err := s.Store.RotateAPIToken(id, prefix, storage.HashAPIToken(token)); err != nil {
		http.Error(w, "token rotate failed", 409)
		return
	}
	if s.MCP != nil {
		s.MCP.CloseSessionsForToken(id)
	}
	_ = s.audit(r, "", "token_rotate", old.Name, 0, 0, "")
	jsonOut(w, map[string]any{"id": id, "name": old.Name, "token": token, "prefix": prefix})
}

func (s *Server) TokenRevokeJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/"), "/revoke")
	if err := s.Store.RevokeAPIToken(id); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if s.MCP != nil {
		s.MCP.CloseSessionsForToken(id)
	}
	_ = s.audit(r, "", "token_revoke", id, 0, 0, "")
	jsonOut(w, map[string]any{"ok": true})
}

func (s *Server) TokenDeleteJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/")
	if err := s.Store.DeleteAPIToken(id); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	_ = s.audit(r, "", "token_delete", id, 0, 0, "")
	jsonOut(w, map[string]any{"ok": true})
}

func auditReadListEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("VPS_COMMANDER_AUDIT_READ_LIST")), "on")
}

func isReadListAuditAction(action string) bool {
	switch action {
	case "list_devices", "list_agent_mcp", "list_processes", "file_list", "file_info", "file_search":
		return true
	default:
		return false
	}
}
