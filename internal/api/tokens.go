package api

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
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

func (s *Server) auditContext(ctx context.Context, ip, device, action, command string, code int, ms int64, msg string) error {
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
	if code != 0 {
		result = "error"
	}
	return s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Device: device, Action: action, Command: command, ExitCode: code, DurationMS: ms, ErrorMsg: msg, ActorType: p.Type, ActorID: p.ID, ActorName: p.Name, Transport: "rest", TargetType: targetType, Target: command, Result: result})
}

func (s *Server) audit(r *http.Request, device, action, command string, code int, ms int64, msg string) error {
	return s.auditContext(r.Context(), clientIP(r), device, action, command, code, ms, msg)
}

func requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principal(r).HasScope(scope) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"scope %s required"}`, scope)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) AuditExportCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	logs, err := s.Store.QueryAudits(r.URL.Query().Get("actor"), r.URL.Query().Get("device"), r.URL.Query().Get("action"), r.URL.Query().Get("result"), from, to, 0, 500)
	if err != nil {
		http.Error(w, "audit query failed", 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=audits.csv")
	c := csv.NewWriter(w)
	_ = c.Write([]string{"id", "timestamp", "actor_name", "actor_type", "transport", "device", "action", "target_type", "target", "result", "command", "exit_code", "duration_ms", "error_msg"})
	for _, v := range logs {
		_ = c.Write([]string{fmt.Sprint(v.ID), fmt.Sprint(v.Timestamp), v.ActorName, v.ActorType, v.Transport, v.Device, v.Action, v.TargetType, v.Target, v.Result, v.Command, fmt.Sprint(v.ExitCode), fmt.Sprint(v.DurationMS), v.ErrorMsg})
	}
	c.Flush()
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
		for _, v := range a {
			if x, ok := v.(string); ok {
				sc = append(sc, x)
			}
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
	_ = s.Store.RevokeAPIToken(id)
	t := storage.APIToken{ID: randomTokenPart(16), Name: old.Name, Prefix: prefix, TokenHash: storage.HashAPIToken(token), ScopesJSON: old.ScopesJSON, DevicesJSON: old.DevicesJSON, RateRPS: old.RateRPS, MaxSessions: old.MaxSessions, Note: old.Note, CreatedAt: time.Now().Unix(), CreatedBy: "api:admin", ExpiresAt: old.ExpiresAt}
	if err := s.Store.CreateAPIToken(t); err != nil {
		http.Error(w, "token rotate failed", 500)
		return
	}
	_ = s.audit(r, "", "token_rotate", old.Name, 0, 0, "")
	jsonOut(w, map[string]any{"id": t.ID, "name": t.Name, "token": token, "prefix": prefix})
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
