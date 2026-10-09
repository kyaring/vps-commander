package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
)

// MCP Protocol / JSON-RPC 2.0 Types
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`

	// rateLimited is set when a tool call failed with ErrRateLimited. It is not
	// serialized; transports use it to answer with HTTP 429 + Retry-After.
	rateLimited bool
}

// ErrRateLimited is wrapped by backends when a per-token limit rejected a tool
// call. Transports detect it with errors.Is instead of matching error text.
var ErrRateLimited = errors.New("rate_limited")

// toolError is the JSON body of a failed tool call ({"error": "..."}) and keeps
// whether the failure was a rate limit.
type toolError struct {
	msg         string
	rateLimited bool
}

func (e toolError) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"error": e.msg})
}

func toolErrorFrom(err error) toolError {
	return toolError{msg: err.Error(), rateLimited: errors.Is(err, ErrRateLimited)}
}

// shortSessionID keeps session identifiers out of logs in full.
func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Tool Definition Structures
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

type InputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]PropertyDef `json:"properties"`
	Required   []string               `json:"required,omitempty"`
}

type PropertyDef struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type CallToolResult struct {
	Content []TextContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Backend interface for invoking actions
type ActionBackend interface {
	ListDevices(ctx context.Context) (any, error)
	ExecCommand(ctx context.Context, clientIP, device, command, workdir string, timeout int) (any, error)
	ReadFile(ctx context.Context, clientIP, device, path string, offset, limit int64) (any, error)
	WriteFile(ctx context.Context, clientIP, device, path, content string) (any, error)
	EditBlock(ctx context.Context, clientIP, device, path, oldText, newText string) (any, error)
}

type DiagnosticBackend interface {
	DiagnosticCommand(ctx context.Context, clientIP, device string, req executor.DiagnosticRequest) (any, error)
}

type ExtendedBackend interface {
	ReadMultipleFiles(ctx context.Context, clientIP, device string, paths []string, limit int64) (any, error)
	CreateDirectory(ctx context.Context, clientIP, device, path string) (any, error)
	MoveFile(ctx context.Context, clientIP, device, source, destination string) (any, error)
	ListProcesses(ctx context.Context, clientIP, device string) (any, error)
}

type AgentMCPBackend interface {
	ListAgentMCP(ctx context.Context, device string) (any, error)
	CallAgentMCP(ctx context.Context, clientIP, device, server, tool string, args any) (any, error)
}

type AuditSink interface {
	AuditMCP(ctx context.Context, clientIP, device, tool, command, result, detail string)
}

type ActionAuditSink interface {
	AuditMCPAction(ctx context.Context, clientIP, device, action, command string, code int, durationMS int64, detail string)
}

type transportContextKey struct{}
type auditIntentContextKey struct{}
type auditStateContextKey struct{}
type auditState struct{ recorded atomic.Bool }

// MarkAuditRecorded marks that a backend already persisted an audit row for this tool call.
func MarkAuditRecorded(ctx context.Context) {
	if state, ok := ctx.Value(auditStateContextKey{}).(*auditState); ok {
		state.recorded.Store(true)
	}
}

func auditAlreadyRecorded(ctx context.Context) bool {
	state, ok := ctx.Value(auditStateContextKey{}).(*auditState)
	return ok && state.recorded.Load()
}

func auditActionForTool(tool string) string {
	switch tool {
	case "exec_command":
		return "exec"
	case "read_file", "read_multiple_files":
		return "file_read"
	case "write_file":
		return "file_write"
	case "list_devices", "list_agent_mcp", "list_processes", "create_directory", "move_file", "edit_block", "diagnostic_command":
		return tool
	case "call_agent_mcp":
		return "mcp_call"
	default:
		return tool
	}
}

// WithAuditIntent attaches a sanitized description of the operation being attempted.
func WithAuditIntent(ctx context.Context, intent string) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, truncateAuditIntent(intent))
}

// AuditIntentFromContext returns the sanitized operation intent when available.
func AuditIntentFromContext(ctx context.Context) string {
	intent, _ := ctx.Value(auditIntentContextKey{}).(string)
	return intent
}

// WithTransport marks the concrete MCP transport for downstream audit records.
func WithTransport(ctx context.Context, transport string) context.Context {
	return context.WithValue(ctx, transportContextKey{}, transport)
}

// TransportFromContext returns the concrete MCP transport when available.
func TransportFromContext(ctx context.Context) string {
	transport, _ := ctx.Value(transportContextKey{}).(string)
	return transport
}

func auditIntent(tool string, raw json.RawMessage) string {
	var a map[string]any
	_ = json.Unmarshal(raw, &a)
	str := func(key string) string { v, _ := a[key].(string); return strings.TrimSpace(v) }
	var intent string
	switch tool {
	case "exec_command":
		intent = str("command")
	case "read_file", "create_directory":
		intent = str("path")
	case "read_multiple_files":
		if paths, ok := a["paths"].([]any); ok {
			parts := make([]string, 0, len(paths))
			for _, path := range paths {
				if v, ok := path.(string); ok {
					parts = append(parts, v)
				}
			}
			intent = strings.Join(parts, ", ")
		}
	case "move_file":
		intent = str("source") + " -> " + str("destination")
	case "call_agent_mcp":
		intent = str("server") + "/" + str("tool")
	case "diagnostic_command":
		intent = "action=" + str("action") + " target=" + str("target")
	case "write_file":
		content, _ := a["content"].(string)
		intent = str("path") + fmt.Sprintf(" (bytes=%d)", len(content))
	case "edit_block":
		oldText, _ := a["old_text"].(string)
		newText, _ := a["new_text"].(string)
		intent = str("path") + fmt.Sprintf(" (bytes=%d)", len(newText))
		_ = oldText // Never retain either file body in audit metadata.
	case "list_processes", "list_devices", "list_agent_mcp":
		intent = tool
	default:
		intent = tool
	}
	return truncateAuditIntent(intent)
}

func truncateAuditIntent(value string) string {
	runes := []rune(value)
	if len(runes) > 512 {
		return string(runes[:511]) + "…"
	}
	return value
}

type SecurityGate interface {
	AuthorizeMCP(ctx context.Context, device, action, clientIP string) error
}

const (
	sseSessionIdleTimeout = 30 * time.Minute
	sseOutboundQueueWait  = 5 * time.Second
)

type session struct {
	ch           chan []byte
	done         chan struct{}
	ownerTokenID string
	closeOnce    sync.Once
	lastActivity atomic.Int64
}

func newSession(ownerTokenID string) *session {
	sess := &session{
		ch:           make(chan []byte, 64),
		done:         make(chan struct{}),
		ownerTokenID: ownerTokenID,
	}
	sess.touch()
	return sess
}

func (sess *session) touch() {
	sess.lastActivity.Store(time.Now().UnixNano())
}

func (sess *session) idleFor() time.Duration {
	last := sess.lastActivity.Load()
	if last == 0 {
		return time.Duration(1<<63 - 1)
	}
	return time.Since(time.Unix(0, last))
}

func (sess *session) close() {
	sess.closeOnce.Do(func() { close(sess.done) })
}

type streamSession struct {
	ownerTokenID    string
	protocolVersion string
	lastActivity    time.Time
}

type Server struct {
	Backend   ActionBackend
	Auth      *auth.Manager
	LocalName string

	mu             sync.RWMutex
	sessions       map[string]*session
	streamSessions map[string]streamSession
}

func NewServer(backend ActionBackend, authMgr *auth.Manager) *Server {
	return &Server{
		Backend:        backend,
		Auth:           authMgr,
		LocalName:      "local",
		sessions:       make(map[string]*session),
		streamSessions: make(map[string]streamSession),
	}
}

func (s *Server) SessionCounts() (sse, streamableHTTP int) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.streamSessions {
		if now.Sub(sess.lastActivity) >= sseSessionIdleTimeout {
			delete(s.streamSessions, id)
			slog.Warn("MCP session closed", "transport", "streamable_http", "session_id", shortSessionID(id), "actor", sess.ownerTokenID, "reason", "idle_timeout")
		}
	}
	return len(s.sessions), len(s.streamSessions)
}

func (s *Server) SetLocalName(name string) {
	if name != "" {
		s.LocalName = name
	}
}

func (s *Server) removeSession(sessionID string, expected *session) {
	s.mu.Lock()
	if current, ok := s.sessions[sessionID]; ok && (expected == nil || current == expected) {
		delete(s.sessions, sessionID)
		current.close()
	}
	s.mu.Unlock()
}

// CloseAllSessions releases legacy SSE handlers and invalidates Streamable HTTP
// sessions during graceful shutdown. SSE handlers observe done and return.
func (s *Server) CloseAllSessions(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		delete(s.sessions, id)
		sess.close()
		slog.Warn("MCP session closed", "transport", "legacy_sse", "session_id", shortSessionID(id), "actor", sess.ownerTokenID, "reason", reason)
	}
	for id, sess := range s.streamSessions {
		delete(s.streamSessions, id)
		slog.Warn("MCP session closed", "transport", "streamable_http", "session_id", shortSessionID(id), "actor", sess.ownerTokenID, "reason", reason)
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, format string, args ...any) error {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("set SSE write deadline: %w", err)
	}
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) CloseSessionsForToken(tokenID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		if sess.ownerTokenID == tokenID {
			delete(s.sessions, id)
			sess.close()
		}
	}
	for id, sess := range s.streamSessions {
		if sess.ownerTokenID == tokenID {
			delete(s.streamSessions, id)
		}
	}
}

func (s *Server) newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SSE Handler: GET /mcp/sse (legacy compatibility transport).
func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(WithTransport(r.Context(), "mcp-sse"))
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	p, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := s.newSessionID()
	sess := newSession(p.TokenID)
	s.mu.Lock()
	if len(s.sessions) >= 500 {
		s.mu.Unlock()
		http.Error(w, "too many MCP sessions", http.StatusTooManyRequests)
		return
	}
	if p.MaxSessions > 0 && p.TokenID != "" {
		count := 0
		for _, current := range s.sessions {
			if current.ownerTokenID == p.TokenID {
				count++
			}
		}
		if count >= p.MaxSessions {
			s.mu.Unlock()
			http.Error(w, "token MCP session limit reached", http.StatusTooManyRequests)
			return
		}
	}
	s.sessions[sessionID] = sess
	s.mu.Unlock()
	defer s.removeSession(sessionID, sess)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	endpointURI := fmt.Sprintf("/mcp/message?sessionId=%s", sessionID)
	if err := writeSSE(w, flusher, "event: endpoint\ndata: %s\n\n", endpointURI); err != nil {
		slog.Warn("MCP session closed", "transport", "legacy_sse", "session_id", shortSessionID(sessionID), "actor", p.TokenID, "reason", "endpoint_write_failed", "error", err)
		return
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sess.done:
			return
		case msg := <-sess.ch:
			if err := writeSSE(w, flusher, "event: message\ndata: %s\n\n", string(msg)); err != nil {
				slog.Warn("MCP session closed", "transport", "legacy_sse", "session_id", shortSessionID(sessionID), "actor", sess.ownerTokenID, "reason", "message_write_failed", "error", err)
				return
			}
		case <-ticker.C:
			if sess.idleFor() >= sseSessionIdleTimeout {
				slog.Warn("MCP session closed", "transport", "legacy_sse", "session_id", shortSessionID(sessionID), "actor", sess.ownerTokenID, "reason", "idle_timeout")
				s.removeSession(sessionID, sess)
				return
			}
			if err := writeSSE(w, flusher, ": keepalive\n\n"); err != nil {
				slog.Warn("MCP session closed", "transport", "legacy_sse", "session_id", shortSessionID(sessionID), "actor", sess.ownerTokenID, "reason", "keepalive_write_failed", "error", err)
				return
			}
		}
	}
}

// HandleMessage is the legacy POST /mcp/message endpoint. A full outbound
// queue is never silently ignored: the sender gets up to five seconds, after
// which the affected SSE session is closed and the request fails explicitly.
func (s *Server) HandleMessage(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(WithTransport(r.Context(), "mcp-sse"))
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	s.mu.RLock()
	sess, exists := s.sessions[sessionID]
	s.mu.RUnlock()
	if sessionID != "" && !exists {
		http.Error(w, "unknown or closed MCP SSE session", http.StatusNotFound)
		return
	}
	if exists {
		p, _ := auth.PrincipalFromContext(r.Context())
		if sess.ownerTokenID != "" && p.TokenID != sess.ownerTokenID {
			http.Error(w, "session owner mismatch", http.StatusForbidden)
			return
		}
		sess.touch()
	}

	resp, ok := s.decodeAndHandleRequest(w, r)
	if !ok {
		return
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to encode JSON-RPC response", http.StatusInternalServerError)
		return
	}
	if resp.rateLimited {
		w.Header().Set("Retry-After", "1")
	}
	if exists {
		// The JSON-RPC response (including a rate-limit error) always goes to the
		// SSE stream so legacy clients waiting on the stream see their request id.
		timer := time.NewTimer(sseOutboundQueueWait)
		defer timer.Stop()
		select {
		case sess.ch <- respBytes:
			if resp.rateLimited {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("rate_limited"))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("accepted"))
		case <-sess.done:
			http.Error(w, "MCP SSE session is closed", http.StatusGone)
		case <-timer.C:
			slog.Warn("MCP session closed", "transport", "legacy_sse", "session_id", shortSessionID(sessionID), "actor", sess.ownerTokenID, "reason", "outbound_backpressure_timeout", "wait", sseOutboundQueueWait.String())
			s.removeSession(sessionID, sess)
			http.Error(w, "MCP SSE outbound queue remained full for 5 seconds; session closed", http.StatusServiceUnavailable)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if resp.rateLimited {
		w.WriteHeader(http.StatusTooManyRequests)
	}
	_, _ = w.Write(respBytes)
}

func (s *Server) decodeAndHandleRequest(w http.ResponseWriter, r *http.Request) (JSONRPCResponse, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return JSONRPCResponse{}, false
	}
	req, ok := decodeJSONRPCRequest(w, body)
	if !ok {
		return JSONRPCResponse{}, false
	}
	return s.handleRequest(r.Context(), r, req), true
}

// decodeJSONRPCRequest parses one JSON-RPC request. On failure it writes HTTP
// 400 with a JSON-RPC error body: -32700 for unparseable JSON, -32600 for a
// batch or a structurally invalid request.
func decodeJSONRPCRequest(w http.ResponseWriter, body []byte) (JSONRPCRequest, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if json.Valid(trimmed) {
			writeJSONRPCError(w, -32600, "Invalid Request: batch requests are not supported")
		} else {
			writeJSONRPCError(w, -32700, "Parse error")
		}
		return JSONRPCRequest{}, false
	}
	var req JSONRPCRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			writeJSONRPCError(w, -32600, "Invalid Request")
		} else {
			writeJSONRPCError(w, -32700, "Parse error")
		}
		return JSONRPCRequest{}, false
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		writeJSONRPCError(w, -32600, "Invalid Request")
		return JSONRPCRequest{}, false
	}
	return req, true
}

func writeJSONRPCError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": JSONRPCError{Code: code, Message: message}})
}

// HandleStreamableHTTP implements MCP Streamable HTTP using ordinary JSON
// responses. Session IDs are optional: clients can use stateless requests, or
// retain the ID returned by initialize to bind later requests to the same token.
func (s *Server) HandleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(WithTransport(r.Context(), "mcp-http"))
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodDelete {
		s.deleteStreamSession(w, r, p)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	accept := r.Header.Get("Accept")
	if accept != "" && !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*") {
		http.Error(w, "Accept must allow application/json", http.StatusNotAcceptable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusBadRequest)
		return
	}
	req, ok := decodeJSONRPCRequest(w, body)
	if !ok {
		return
	}

	if req.ID == nil && !strings.HasPrefix(req.Method, "notifications/") {
		http.Error(w, "JSON-RPC messages without an id must be notifications/*", http.StatusBadRequest)
		return
	}

	clientSessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	protocolVersion := "2025-03-26"
	if clientSessionID != "" {
		s.mu.Lock()
		stored, exists := s.streamSessions[clientSessionID]
		if exists && time.Since(stored.lastActivity) >= sseSessionIdleTimeout {
			delete(s.streamSessions, clientSessionID)
			slog.Warn("MCP session closed", "transport", "streamable_http", "session_id", shortSessionID(clientSessionID), "actor", stored.ownerTokenID, "reason", "idle_timeout")
			exists = false
		}
		if !exists {
			s.mu.Unlock()
			http.Error(w, "unknown or expired MCP session", http.StatusNotFound)
			return
		}
		if stored.ownerTokenID != "" && stored.ownerTokenID != p.TokenID {
			s.mu.Unlock()
			http.Error(w, "session owner mismatch", http.StatusForbidden)
			return
		}
		stored.lastActivity = time.Now()
		if stored.protocolVersion != "" {
			protocolVersion = stored.protocolVersion
		}
		s.streamSessions[clientSessionID] = stored
		s.mu.Unlock()
	}

	resp := s.handleRequest(r.Context(), r, req)
	if resp.rateLimited {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	if req.Method == "initialize" {
		if result, ok := resp.Result.(map[string]any); ok {
			if v, ok := result["protocolVersion"].(string); ok && v != "" {
				protocolVersion = v
			}
		}
		if req.ID == nil {
			http.Error(w, "initialize must be a JSON-RPC request", http.StatusBadRequest)
			return
		}
		if clientSessionID == "" {
			now := time.Now()
			s.mu.Lock()
			for id, stored := range s.streamSessions {
				if now.Sub(stored.lastActivity) >= sseSessionIdleTimeout {
					delete(s.streamSessions, id)
					slog.Warn("MCP session closed", "transport", "streamable_http", "session_id", shortSessionID(id), "actor", stored.ownerTokenID, "reason", "idle_timeout")
				}
			}
			if len(s.streamSessions) >= 500 {
				s.mu.Unlock()
				http.Error(w, "too many MCP sessions", http.StatusTooManyRequests)
				return
			}
			if p.MaxSessions > 0 && p.TokenID != "" {
				count := 0
				for _, stored := range s.streamSessions {
					if stored.ownerTokenID == p.TokenID {
						count++
					}
				}
				if count >= p.MaxSessions {
					s.mu.Unlock()
					http.Error(w, "token MCP session limit reached", http.StatusTooManyRequests)
					return
				}
			}
			clientSessionID = s.newSessionID()
			s.streamSessions[clientSessionID] = streamSession{ownerTokenID: p.TokenID, protocolVersion: protocolVersion, lastActivity: now}
			s.mu.Unlock()
		}
		w.Header().Set("Mcp-Session-Id", clientSessionID)
	}
	w.Header().Set("Mcp-Protocol-Version", protocolVersion)
	if req.ID == nil {
		// JSON-RPC notifications have no response body.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	responseBody, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to encode JSON-RPC response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(responseBody)
}

// deleteStreamSession ends a Streamable HTTP session (MCP 2025-03-26 session
// termination). Only the token that created the session may delete it.
func (s *Server) deleteStreamSession(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	stored, exists := s.streamSessions[sessionID]
	if !exists {
		s.mu.Unlock()
		http.Error(w, "unknown or expired MCP session", http.StatusNotFound)
		return
	}
	if stored.ownerTokenID != "" && stored.ownerTokenID != p.TokenID {
		s.mu.Unlock()
		http.Error(w, "session owner mismatch", http.StatusForbidden)
		return
	}
	delete(s.streamSessions, sessionID)
	s.mu.Unlock()
	slog.Info("MCP session closed", "transport", "streamable_http", "session_id", shortSessionID(sessionID), "actor", stored.ownerTokenID, "reason", "client_delete")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRequest(ctx context.Context, r *http.Request, req JSONRPCRequest) JSONRPCResponse {
	switch req.Method {
	case "initialize":
		protocolVersion := "2024-11-05"
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &initParams) == nil && initParams.ProtocolVersion == "2025-03-26" {
			protocolVersion = initParams.ProtocolVersion
		}
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo": map[string]string{
					"name":    "vps-commander",
					"version": "1.2.0",
				},
				"capabilities": map[string]any{
					"tools": map[string]bool{"listChanged": false},
				},
			},
		}

	case "notifications/initialized":
		return JSONRPCResponse{JSONRPC: "2.0"}

	case "ping":
		return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{}}

	case "tools/list":
		tools := []Tool{
			{
				Name:        "list_devices",
				Description: "列出当前所有已注册的 VPS 节点及其在线状态与系统架构",
				InputSchema: InputSchema{
					Type:       "object",
					Properties: map[string]PropertyDef{},
				},
			},
			{
				Name:        "exec_command",
				Description: "在指定受控节点（默认本机 local）执行任意 Shell 命令并返回输出和退出码",
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]PropertyDef{
						"device":  {Type: "string", Description: "目标设备节点名称/别名（缺省则在当前 Hub 所在节点执行）"},
						"command": {Type: "string", Description: "需要执行的 Shell 命令"},
						"workdir": {Type: "string", Description: "可选的工作目录绝对路径"},
						"timeout": {Type: "integer", Description: "超时时间（秒，默认 30，最大 300）"},
					},
					Required: []string{"command"},
				},
			},
			{
				Name:        "read_file",
				Description: "从指定受控节点读取文件内容（支持偏移与字节分页）",
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]PropertyDef{
						"device": {Type: "string", Description: "目标设备节点名称/别名（缺省为 local）"},
						"path":   {Type: "string", Description: "文件的绝对路径"},
						"offset": {Type: "integer", Description: "读取的起始字节偏移量（默认 0）"},
						"limit":  {Type: "integer", Description: "读取的最大字节数（默认 262144，最大 1048576）"},
					},
					Required: []string{"path"},
				},
			},
			{Name: "read_multiple_files", Description: "一次读取多个文件", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标设备"}, "paths": {Type: "array", Description: "文件路径数组"}, "limit": {Type: "integer", Description: "单文件最大字节数"}}, Required: []string{"paths"}}},
			{Name: "create_directory", Description: "创建目录", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标设备"}, "path": {Type: "string", Description: "目录路径"}}, Required: []string{"path"}}},
			{Name: "move_file", Description: "移动或重命名文件", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标设备"}, "source": {Type: "string", Description: "源路径"}, "destination": {Type: "string", Description: "目标路径"}}, Required: []string{"source", "destination"}}},
			{Name: "list_processes", Description: "列出受控节点当前进程", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标设备"}}}},
			{Name: "diagnostic_command", Description: "执行严格白名单的只读系统诊断，不接受任意 Shell", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标设备"}, "action": {Type: "string", Description: "docker_ps|docker_logs|docker_inspect|systemctl_status|journalctl|ss_listen|df|free|uptime"}, "target": {Type: "string", Description: "容器名或 systemd unit（按 action 使用）"}, "lines": {Type: "integer", Description: "docker_logs 最大行数，1-500"}}, Required: []string{"action"}}},
			{
				Name:        "edit_block",
				Description: "对指定文件执行唯一文本块的原子替换；old_text 必须恰好匹配一次",
				InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标节点名称"}, "path": {Type: "string", Description: "目标文件路径"}, "old_text": {Type: "string", Description: "必须唯一匹配的原文本块"}, "new_text": {Type: "string", Description: "替换后的文本块"}}, Required: []string{"path", "old_text", "new_text"}},
			},
			{
				Name:        "write_file",
				Description: "向指定受控节点创建或覆写文件",
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]PropertyDef{
						"device":  {Type: "string", Description: "目标设备节点名称/别名（缺省为 local）"},
						"path":    {Type: "string", Description: "文件的绝对路径"},
						"content": {Type: "string", Description: "要写入的文件文本内容"},
					},
					Required: []string{"path", "content"},
				},
			},
			{
				Name: "list_agent_mcp", Description: "列出指定 Agent 当前通过 Hub 动态挂载的 MCP 服务及状态",
				InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标 Agent 节点名称"}}, Required: []string{"device"}},
			},
			{
				Name: "call_agent_mcp", Description: "通过 Hub 调度指定 Agent 上已挂载的 MCP 工具",
				InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标 Agent 节点名称"}, "server": {Type: "string", Description: "MCP 服务 ID"}, "tool": {Type: "string", Description: "MCP 工具名称"}, "arguments": {Type: "object", Description: "MCP 工具参数 JSON 对象"}}, Required: []string{"device", "server", "tool"}},
			},
		}
		if p, ok := auth.PrincipalFromContext(ctx); ok {
			filtered := make([]Tool, 0, len(tools))
			for _, tool := range tools {
				if scope := mcpToolScope(tool.Name); scope != "" && p.HasScope(scope) {
					filtered = append(filtered, tool)
				}
			}
			tools = filtered
		}
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"tools": tools},
		}

	case "tools/call":
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &JSONRPCError{Code: -32602, Message: "invalid tool params"},
			}
		}

		clientIP := auth.ClientIP(r)
		state := &auditState{}
		ctx = context.WithValue(ctx, auditStateContextKey{}, state)
		res, isErr := s.callTool(ctx, clientIP, params)
		resJSON, _ := json.MarshalIndent(res, "", "  ")
		te, _ := res.(toolError)
		if isErr && !te.rateLimited && !auditAlreadyRecorded(ctx) {
			if sink, ok := s.Backend.(ActionAuditSink); ok {
				device := ""
				var args map[string]any
				_ = json.Unmarshal(params.Arguments, &args)
				if v, ok := args["device"].(string); ok {
					device = v
				}
				sink.AuditMCPAction(ctx, clientIP, device, auditActionForTool(params.Name), auditIntent(params.Name, params.Arguments), 1, 0, te.msg)
			}
		}

		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []TextContent{
					{Type: "text", Text: string(resJSON)},
				},
				IsError: isErr,
			},
			rateLimited: te.rateLimited,
		}

	default:
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

func mcpToolScope(name string) string {
	switch name {
	case "list_devices":
		return "devices:read"
	case "exec_command":
		return "exec"
	case "read_file", "read_multiple_files", "diagnostic_command", "list_agent_mcp":
		return "files:read"
	case "list_processes":
		return "process"
	case "create_directory", "move_file", "edit_block", "write_file":
		return "files:write"
	case "call_agent_mcp":
		return "mcp:call"
	default:
		return ""
	}
}

func (s *Server) authorizeTool(ctx context.Context, name, device string) error {
	device = strings.TrimSpace(device)
	if device == "local" && s.LocalName != "" {
		device = s.LocalName
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return fmt.Errorf("unauthorized")
	}
	if scope := mcpToolScope(name); scope == "" {
		return fmt.Errorf("unknown tool: %s", name)
	} else if !p.HasScope(scope) {
		return fmt.Errorf("scope denied: %s", scope)
	}
	if device != "" && !p.AllowsDevice(device) {
		return fmt.Errorf("device denied: %s", device)
	}
	return nil
}

func (s *Server) callTool(ctx context.Context, clientIP string, params CallToolParams) (any, bool) {
	ctx = WithAuditIntent(ctx, auditIntent(params.Name, params.Arguments))
	device := ""
	var rawDevice map[string]any
	_ = json.Unmarshal(params.Arguments, &rawDevice)
	if v, ok := rawDevice["device"].(string); ok {
		device = v
	}
	if device == "" && params.Name != "list_devices" {
		device = "local"
	}
	if err := s.authorizeTool(ctx, params.Name, device); err != nil {
		if sink, ok := s.Backend.(AuditSink); ok {
			sink.AuditMCP(ctx, clientIP, device, params.Name, auditIntent(params.Name, params.Arguments), "denied", err.Error())
		}
		return toolErrorFrom(err), true
	}
	switch params.Name {
	case "list_devices":
		devs, err := s.Backend.ListDevices(ctx)
		if err != nil {
			return toolErrorFrom(err), true
		}
		if sink, ok := s.Backend.(ActionAuditSink); ok {
			sink.AuditMCPAction(ctx, clientIP, device, "list_devices", "list_devices", 0, 0, "")
		}
		return devs, false

	case "exec_command":
		var args struct {
			Device  string `json:"device"`
			Command string `json:"command"`
			Workdir string `json:"workdir"`
			Timeout int    `json:"timeout"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		if args.Command == "" {
			return map[string]string{"error": "command is required"}, true
		}
		res, err := s.Backend.ExecCommand(ctx, clientIP, args.Device, args.Command, args.Workdir, args.Timeout)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false

	case "read_file":
		var args struct {
			Device string `json:"device"`
			Path   string `json:"path"`
			Offset int64  `json:"offset"`
			Limit  int64  `json:"limit"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		if args.Path == "" {
			return map[string]string{"error": "path is required"}, true
		}
		res, err := s.Backend.ReadFile(ctx, clientIP, args.Device, args.Path, args.Offset, args.Limit)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false

	case "read_multiple_files":
		var a struct {
			Device string   `json:"device"`
			Paths  []string `json:"paths"`
			Limit  int64    `json:"limit"`
		}
		_ = json.Unmarshal(params.Arguments, &a)
		b, ok := s.Backend.(ExtendedBackend)
		if !ok {
			return map[string]string{"error": "extended tools unavailable"}, true
		}
		res, err := b.ReadMultipleFiles(ctx, clientIP, a.Device, a.Paths, a.Limit)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false
	case "create_directory":
		var a struct{ Device, Path string }
		_ = json.Unmarshal(params.Arguments, &a)
		b, ok := s.Backend.(ExtendedBackend)
		if !ok {
			return map[string]string{"error": "extended tools unavailable"}, true
		}
		res, err := b.CreateDirectory(ctx, clientIP, a.Device, a.Path)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false
	case "move_file":
		var a struct{ Device, Source, Destination string }
		_ = json.Unmarshal(params.Arguments, &a)
		b, ok := s.Backend.(ExtendedBackend)
		if !ok {
			return map[string]string{"error": "extended tools unavailable"}, true
		}
		res, err := b.MoveFile(ctx, clientIP, a.Device, a.Source, a.Destination)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false
	case "diagnostic_command":
		var a struct {
			Device string `json:"device"`
			Action string `json:"action"`
			Target string `json:"target"`
			Lines  int    `json:"lines"`
		}
		_ = json.Unmarshal(params.Arguments, &a)
		b, ok := s.Backend.(DiagnosticBackend)
		if !ok {
			return map[string]string{"error": "diagnostic tools unavailable"}, true
		}
		res, err := b.DiagnosticCommand(ctx, clientIP, a.Device, executor.DiagnosticRequest{Action: a.Action, Target: a.Target, Lines: a.Lines})
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false

	case "list_processes":
		var a struct {
			Device string `json:"device"`
		}
		_ = json.Unmarshal(params.Arguments, &a)
		b, ok := s.Backend.(ExtendedBackend)
		if !ok {
			return map[string]string{"error": "extended tools unavailable"}, true
		}
		res, err := b.ListProcesses(ctx, clientIP, a.Device)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false
	case "edit_block":
		var args struct{ Device, Path, OldText, NewText string }
		_ = json.Unmarshal(params.Arguments, &args)
		if args.Path == "" || args.OldText == "" {
			return map[string]string{"error": "path and old_text are required"}, true
		}
		res, err := s.Backend.EditBlock(ctx, clientIP, args.Device, args.Path, args.OldText, args.NewText)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false

	case "write_file":
		var args struct {
			Device  string `json:"device"`
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		if args.Path == "" {
			return map[string]string{"error": "path is required"}, true
		}
		res, err := s.Backend.WriteFile(ctx, clientIP, args.Device, args.Path, args.Content)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false

	case "list_agent_mcp":
		var args struct {
			Device string `json:"device"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		b, ok := s.Backend.(AgentMCPBackend)
		if !ok {
			return map[string]string{"error": "agent MCP unavailable"}, true
		}
		if gate, ok := s.Backend.(SecurityGate); ok {
			if err := gate.AuthorizeMCP(ctx, args.Device, "read", clientIP); err != nil {
				return toolErrorFrom(err), true
			}
		}
		res, err := b.ListAgentMCP(ctx, args.Device)
		if err != nil {
			return toolErrorFrom(err), true
		}
		if sink, ok := s.Backend.(ActionAuditSink); ok {
			sink.AuditMCPAction(ctx, clientIP, args.Device, "list_agent_mcp", "list_agent_mcp", 0, 0, "")
		}
		return res, false

	case "call_agent_mcp":
		var args struct {
			Device    string          `json:"device"`
			Server    string          `json:"server"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		b, ok := s.Backend.(AgentMCPBackend)
		if !ok {
			return map[string]string{"error": "agent MCP unavailable"}, true
		}
		var av any
		if len(args.Arguments) > 0 {
			_ = json.Unmarshal(args.Arguments, &av)
		}
		if gate, ok := s.Backend.(SecurityGate); ok {
			if err := gate.AuthorizeMCP(ctx, args.Device, "mcp", clientIP); err != nil {
				return toolErrorFrom(err), true
			}
		}
		res, err := b.CallAgentMCP(ctx, clientIP, args.Device, args.Server, args.Tool, av)
		if err != nil {
			return toolErrorFrom(err), true
		}
		return res, false

	default:
		return map[string]string{"error": "unknown tool: " + params.Name}, true
	}
}

func (s *Server) HandleDirectRequest(ctx context.Context, req JSONRPCRequest) JSONRPCResponse {
	if _, ok := auth.PrincipalFromContext(ctx); !ok {
		ctx = auth.WithPrincipal(ctx, auth.Principal{Type: "system", ID: "stdio", Name: "stdio"})
	}
	return s.handleRequest(ctx, &http.Request{RemoteAddr: "stdio"}, req)
}
