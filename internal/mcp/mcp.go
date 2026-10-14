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
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wjyhk/vps-commander/internal/auditutil"
	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/storage"
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
	Content       []TextContent `json:"content"`
	IsError       bool          `json:"isError,omitempty"`
	BudgetWarning string        `json:"budget_warning,omitempty"`
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
	return context.WithValue(ctx, auditIntentContextKey{}, auditutil.Truncate(auditutil.Redact(intent)))
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

func AuditIntent(tool string, raw json.RawMessage) string {
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
	case "memo_set", "memo_get":
		intent = str("key")
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
	return auditutil.Truncate(auditutil.Redact(intent))
}

type SecurityGate interface {
	AuthorizeMCP(ctx context.Context, device, action, clientIP string) error
}

const (
	sseSessionIdleTimeout = 30 * time.Minute
	sseOutboundQueueWait  = 5 * time.Second
)

type session struct {
	ch                chan []byte
	done              chan struct{}
	ownerTokenID      string
	ownerQuotaTokenID string
	closeOnce         sync.Once
	lastActivity      atomic.Int64
}

func newSession(ownerTokenID string, quotaIDs ...string) *session {
	ownerQuotaTokenID := ownerTokenID
	if len(quotaIDs) > 0 && quotaIDs[0] != "" {
		ownerQuotaTokenID = quotaIDs[0]
	}
	sess := &session{
		ch:                make(chan []byte, 64),
		done:              make(chan struct{}),
		ownerTokenID:      ownerTokenID,
		ownerQuotaTokenID: ownerQuotaTokenID,
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
	ownerTokenID      string
	ownerQuotaTokenID string
	protocolVersion   string
	lastActivity      time.Time
}

type sessionToolAggregate struct {
	calls int
	bytes int64
}

type sessionBudgetRecord struct {
	sessionID    string
	ownerTokenID string
	startedAt    time.Time
	lastActivity time.Time
	calls        int
	bytes        int64
	tools        map[string]*sessionToolAggregate
}

type SessionToolStats struct {
	Name  string `json:"name"`
	Calls int    `json:"calls"`
	Bytes int64  `json:"bytes"`
}

type SessionBudgetInfo struct {
	LimitBytes int64 `json:"limit_bytes"`
	UsedPct    int64 `json:"used_pct"`
	Warn       bool  `json:"warn"`
}

type SessionBudgetStats struct {
	SessionID     string             `json:"session_id"`
	StartedAt     string             `json:"started_at"`
	ToolCalls     int                `json:"tool_calls"`
	BytesReturned int64              `json:"bytes_returned"`
	EstTokens     int64              `json:"est_tokens"`
	TopTools      []SessionToolStats `json:"top_tools"`
	Budget        SessionBudgetInfo  `json:"budget"`
}

const defaultSessionBudgetBytes int64 = 2097152
const sessionBudgetIdleTTL = 24 * time.Hour
const defaultHTTPSessionIdleTTL = 30 * time.Minute

type Server struct {
	Backend                ActionBackend
	Auth                   *auth.Manager
	LocalName              string
	OAuthStore             *storage.Store
	OAuthPanelSessionValid func(*http.Request) bool

	oauthMu            sync.Mutex
	oauthPending       map[string]pendingOAuthRequest
	mu                 sync.RWMutex
	sessions           map[string]*session
	streamSessions     map[string]streamSession
	budgetMu           sync.Mutex
	sessionBudgets     map[string]*sessionBudgetRecord
	sessionBudgetLimit int64
	httpSessionIdleTTL time.Duration
	memoMu             sync.Mutex
	sessionMemos       map[string]map[string]sessionMemoEntry
}

func NewServer(backend ActionBackend, authMgr *auth.Manager) *Server {
	budgetLimit := defaultSessionBudgetBytes
	if raw := strings.TrimSpace(os.Getenv("VPS_COMMANDER_SESSION_BUDGET_BYTES")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			budgetLimit = parsed
		}
	}
	httpSessionIdleTTL := defaultHTTPSessionIdleTTL
	if raw := strings.TrimSpace(os.Getenv("VPS_COMMANDER_HTTP_SESSION_IDLE_MINUTES")); raw != "" {
		if minutes, err := strconv.ParseFloat(raw, 64); err == nil && minutes > 0 {
			if duration := time.Duration(minutes * float64(time.Minute)); duration > 0 {
				httpSessionIdleTTL = duration
			}
		}
	}
	return &Server{
		Backend:            backend,
		Auth:               authMgr,
		LocalName:          "local",
		sessions:           make(map[string]*session),
		streamSessions:     make(map[string]streamSession),
		sessionBudgets:     make(map[string]*sessionBudgetRecord),
		sessionBudgetLimit: budgetLimit,
		httpSessionIdleTTL: httpSessionIdleTTL,
		sessionMemos:       make(map[string]map[string]sessionMemoEntry),
		oauthPending:       make(map[string]pendingOAuthRequest),
	}
}

// sessionBudgetKey is the shared identity function for HTTP stats and memo:
// HTTP callers are keyed by authenticated token ID plus client IP (never port);
// MCP callers remain keyed by their server-issued MCP session ID.
func sessionBudgetKey(tokenID, sessionID, remoteAddr string) string {
	if sessionID != "" {
		return "mcp:" + sessionID
	}
	clientIP := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		clientIP = host
	}
	if ip := net.ParseIP(strings.Trim(clientIP, "[]")); ip != nil {
		clientIP = ip.String()
	}
	return "http:" + tokenID + "\x00" + clientIP
}

func (s *Server) budgetRecordLocked(key, tokenID, sessionID string, now time.Time, create bool) *sessionBudgetRecord {
	for oldKey, record := range s.sessionBudgets {
		idleTTL := sessionBudgetIdleTTL
		isHTTPSession := strings.HasPrefix(oldKey, "http:")
		if isHTTPSession {
			idleTTL = s.httpSessionIdleTTL
		}
		if now.Sub(record.lastActivity) > idleTTL {
			delete(s.sessionBudgets, oldKey)
			if isHTTPSession {
				// HTTP stats and memo share the same identity key. Expiring one
				// session must reset both in-memory stores together.
				s.memoMu.Lock()
				delete(s.sessionMemos, oldKey)
				s.memoMu.Unlock()
			}
		}
	}
	if record := s.sessionBudgets[key]; record != nil {
		if record.ownerTokenID != tokenID {
			return nil
		}
		record.lastActivity = now
		return record
	}
	if !create {
		return nil
	}
	if sessionID == "" {
		sessionID = "http-" + s.newSessionID()
	}
	record := &sessionBudgetRecord{sessionID: sessionID, ownerTokenID: tokenID, startedAt: now, lastActivity: now, tools: make(map[string]*sessionToolAggregate)}
	s.sessionBudgets[key] = record
	return record
}

// EnsureSessionBudget starts accounting at MCP initialize, without persisting
// session data or exposing token material in the public session ID.
func (s *Server) EnsureSessionBudget(tokenID, sessionID string, startedAt time.Time) {
	if tokenID == "" || sessionID == "" {
		return
	}
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	now := startedAt
	if now.IsZero() {
		now = time.Now()
	}
	key := sessionBudgetKey(tokenID, sessionID, "")
	if s.sessionBudgets[key] == nil {
		s.sessionBudgets[key] = &sessionBudgetRecord{sessionID: sessionID, ownerTokenID: tokenID, startedAt: now, lastActivity: now, tools: make(map[string]*sessionToolAggregate)}
	}
}

// SessionBudgetWarning returns a warning only after previously recorded bytes
// strictly exceed the configured limit. The response that crosses the limit
// is therefore not warned; subsequent tool responses are.
func (s *Server) SessionBudgetWarning(tokenID, sessionID, remoteAddr string) string {
	if tokenID == "" {
		return ""
	}
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	now := time.Now()
	record := s.budgetRecordLocked(sessionBudgetKey(tokenID, sessionID, remoteAddr), tokenID, sessionID, now, false)
	if record == nil || record.bytes <= s.sessionBudgetLimit {
		return ""
	}
	return fmt.Sprintf("session budget exceeded: %s / %s; consider narrower queries (output_mode=count, dedup_window_seconds)", formatSessionBudgetBytes(record.bytes), formatSessionBudgetBytes(s.sessionBudgetLimit))
}

func formatSessionBudgetBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// RecordSessionResponse accounts only response-body bytes delivered to the
// caller. Data remains in memory and is reset whenever the Hub process restarts.
func (s *Server) RecordSessionResponse(tokenID, sessionID, remoteAddr, toolName string, responseBytes int) {
	if tokenID == "" {
		return
	}
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	now := time.Now()
	key := sessionBudgetKey(tokenID, sessionID, remoteAddr)
	record := s.budgetRecordLocked(key, tokenID, sessionID, now, true)
	if record == nil {
		return
	}
	if toolName == "" {
		toolName = "http"
	}
	record.calls++
	if responseBytes > 0 {
		record.bytes += int64(responseBytes)
	}
	agg := record.tools[toolName]
	if agg == nil {
		agg = &sessionToolAggregate{}
		record.tools[toolName] = agg
	}
	agg.calls++
	if responseBytes > 0 {
		agg.bytes += int64(responseBytes)
	}
}

// SessionBudgetStats returns only the requesting token's own session. An MCP
// session ID must already exist and be owned by that token; HTTP sessions are
// keyed by token ID and the current connection's remote address.
func (s *Server) SessionBudgetStats(tokenID, sessionID, remoteAddr string) (SessionBudgetStats, bool) {
	if tokenID == "" {
		return SessionBudgetStats{}, false
	}
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	now := time.Now()
	key := sessionBudgetKey(tokenID, sessionID, remoteAddr)
	record := s.budgetRecordLocked(key, tokenID, sessionID, now, sessionID == "")
	if record == nil {
		return SessionBudgetStats{}, false
	}
	limit := s.sessionBudgetLimit
	usedPct := int64(0)
	if limit > 0 {
		usedPct = record.bytes * 100 / limit
	}
	top := make([]SessionToolStats, 0, len(record.tools))
	for name, agg := range record.tools {
		top = append(top, SessionToolStats{Name: name, Calls: agg.calls, Bytes: agg.bytes})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Bytes == top[j].Bytes {
			return top[i].Name < top[j].Name
		}
		return top[i].Bytes > top[j].Bytes
	})
	if len(top) > 5 {
		top = top[:5]
	}
	return SessionBudgetStats{SessionID: record.sessionID, StartedAt: record.startedAt.UTC().Format(time.RFC3339), ToolCalls: record.calls, BytesReturned: record.bytes, EstTokens: record.bytes / 4, TopTools: top, Budget: SessionBudgetInfo{LimitBytes: limit, UsedPct: usedPct, Warn: record.bytes > limit}}, true
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
		if sess.ownerTokenID == tokenID || sess.ownerQuotaTokenID == tokenID {
			delete(s.sessions, id)
			sess.close()
		}
	}
	for id, sess := range s.streamSessions {
		if sess.ownerTokenID == tokenID || sess.ownerQuotaTokenID == tokenID {
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
	sess := newSession(p.TokenID, p.QuotaID())
	s.mu.Lock()
	if len(s.sessions) >= 500 {
		s.mu.Unlock()
		http.Error(w, "too many MCP sessions", http.StatusTooManyRequests)
		return
	}
	if p.MaxSessions > 0 && p.QuotaID() != "" {
		count := 0
		for _, current := range s.sessions {
			if current.ownerQuotaTokenID == p.QuotaID() {
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
	ctx := r.Context()
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		ctx = withSessionMemoIdentity(ctx, p.TokenID, strings.TrimSpace(r.URL.Query().Get("sessionId")), r.RemoteAddr)
	}
	return s.handleRequest(ctx, r, req), true
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

	ctx := withSessionMemoIdentity(r.Context(), p.TokenID, clientSessionID, r.RemoteAddr)
	resp := s.handleRequest(ctx, r, req)
	toolName := ""
	if req.Method == "tools/call" {
		var params CallToolParams
		if json.Unmarshal(req.Params, &params) == nil {
			toolName = params.Name
		}
		if warning := s.SessionBudgetWarning(p.TokenID, clientSessionID, r.RemoteAddr); warning != "" {
			if result, ok := resp.Result.(CallToolResult); ok {
				result.BudgetWarning = warning
				resp.Result = result
			}
		}
	}
	if resp.rateLimited {
		responseBody, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, "failed to encode JSON-RPC response", http.StatusInternalServerError)
			return
		}
		responseBody = append(responseBody, '\n')
		if req.Method == "tools/call" {
			s.RecordSessionResponse(p.TokenID, clientSessionID, r.RemoteAddr, toolName, len(responseBody))
		}
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(responseBody)
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
			if p.MaxSessions > 0 && p.QuotaID() != "" {
				count := 0
				for _, stored := range s.streamSessions {
					if stored.ownerQuotaTokenID == p.QuotaID() {
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
			s.streamSessions[clientSessionID] = streamSession{ownerTokenID: p.TokenID, ownerQuotaTokenID: p.QuotaID(), protocolVersion: protocolVersion, lastActivity: now}
			s.mu.Unlock()
			s.EnsureSessionBudget(p.TokenID, clientSessionID, now)
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
	if req.Method == "tools/call" {
		s.RecordSessionResponse(p.TokenID, clientSessionID, r.RemoteAddr, toolName, len(responseBody))
	}
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
			{Name: "memo_set", Description: "将一条便签写入当前会话（仅内存，value 不进入审计或日志）", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"key": {Type: "string", Description: "便签键"}, "value": {Type: "string", Description: "便签内容，最多 4096 字节"}, "ttl_seconds": {Type: "integer", Description: "可选过期秒数"}}, Required: []string{"key", "value"}}},
			{Name: "memo_get", Description: "读取当前会话便签；不传 key 时只返回元数据列表，不返回 value", InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"key": {Type: "string", Description: "便签键；省略时列出元数据"}}}},
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
						"device":               {Type: "string", Description: "目标设备节点名称/别名（缺省则在当前 Hub 所在节点执行）"},
						"command":              {Type: "string", Description: "需要执行的 Shell 命令"},
						"workdir":              {Type: "string", Description: "可选的工作目录绝对路径"},
						"timeout":              {Type: "integer", Description: "超时时间（秒，默认 30，最大 300）"},
						"output_mode":          {Type: "string", Description: "输出模式（raw|tail|head|count|none，默认 raw）"},
						"max_bytes":            {Type: "integer", Description: "输出最大字节数（默认 4096，最大 1048576）"},
						"dedup_window_seconds": {Type: "integer", Description: "相同只读命令的去重窗口秒数（默认 0，关闭）"},
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
				if scope := mcpToolScope(tool.Name); (scope != "" && p.HasScope(scope)) || tool.Name == "memo_set" || tool.Name == "memo_get" {
					filtered = append(filtered, tool)
				}
			}
			tools = filtered
		} else {
			// Unauthenticated direct/legacy requests do not discover session tools.
			filtered := make([]Tool, 0, len(tools)-2)
			for _, tool := range tools {
				if tool.Name != "memo_set" && tool.Name != "memo_get" {
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
				sink.AuditMCPAction(ctx, clientIP, device, auditActionForTool(params.Name), AuditIntent(params.Name, params.Arguments), 1, 0, te.msg)
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
	if scope := mcpToolScope(name); scope == "" && name != "memo_set" && name != "memo_get" {
		return fmt.Errorf("unknown tool: %s", name)
	} else if scope != "" && !p.HasScope(scope) {
		return fmt.Errorf("scope denied: %s", scope)
	}
	if device != "" && name != "memo_set" && name != "memo_get" && !p.AllowsDevice(device) {
		return fmt.Errorf("device denied: %s", device)
	}
	return nil
}

func (s *Server) callTool(ctx context.Context, clientIP string, params CallToolParams) (any, bool) {
	ctx = WithAuditIntent(ctx, AuditIntent(params.Name, params.Arguments))
	device := ""
	var rawDevice map[string]any
	_ = json.Unmarshal(params.Arguments, &rawDevice)
	if v, ok := rawDevice["device"].(string); ok {
		device = v
	}
	if device == "" && params.Name != "list_devices" && params.Name != "memo_set" && params.Name != "memo_get" {
		device = "local"
	}
	if err := s.authorizeTool(ctx, params.Name, device); err != nil {
		if sink, ok := s.Backend.(AuditSink); ok {
			sink.AuditMCP(ctx, clientIP, device, params.Name, AuditIntent(params.Name, params.Arguments), "denied", err.Error())
		}
		return toolErrorFrom(err), true
	}
	switch params.Name {
	case "memo_set":
		var args struct {
			Key        string `json:"key"`
			Value      string `json:"value"`
			TTLSeconds *int64 `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return map[string]string{"error": "invalid memo_set arguments"}, true
		}
		identity := sessionMemoIdentityFromContext(ctx)
		if identity.tokenID == "" {
			return map[string]string{"error": "unauthorized"}, true
		}
		result, err := s.SetSessionMemo(identity.tokenID, identity.sessionID, identity.remoteAddr, args.Key, args.Value, args.TTLSeconds)
		status, n, detail := 0, len([]byte(args.Value)), ""
		if err != nil {
			status = 1
			detail = err.Error()
		}
		s.AuditSessionMemo(ctx, clientIP, "memo_set", args.Key, n, status, detail)
		if err != nil {
			return map[string]string{"error": err.Error()}, true
		}
		return result, false

	case "memo_get":
		var args struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return map[string]string{"error": "invalid memo_get arguments"}, true
		}
		identity := sessionMemoIdentityFromContext(ctx)
		if identity.tokenID == "" {
			return map[string]string{"error": "unauthorized"}, true
		}
		if args.Key == "" {
			items, err := s.ListSessionMemos(identity.tokenID, identity.sessionID, identity.remoteAddr)
			if err != nil {
				s.AuditSessionMemo(ctx, clientIP, "memo_get", "list", 0, 1, err.Error())
				return map[string]string{"error": err.Error()}, true
			}
			var total int
			for _, item := range items {
				total += item.Bytes
			}
			s.AuditSessionMemo(ctx, clientIP, "memo_get", "list", total, 0, "")
			return map[string]any{"memos": items}, false
		}
		item, found, err := s.GetSessionMemo(identity.tokenID, identity.sessionID, identity.remoteAddr, args.Key)
		status, n, detail := 0, 0, ""
		if err != nil {
			status = 1
			detail = err.Error()
		} else if found {
			n = item.Bytes
		} else {
			status = 1
			detail = "memo not found"
		}
		s.AuditSessionMemo(ctx, clientIP, "memo_get", args.Key, n, status, detail)
		if err != nil {
			return map[string]string{"error": err.Error()}, true
		}
		if !found {
			return map[string]string{"error": "memo not found"}, true
		}
		return item, false

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
			Device             string `json:"device"`
			Command            string `json:"command"`
			Workdir            string `json:"workdir"`
			Timeout            int    `json:"timeout"`
			OutputMode         string `json:"output_mode,omitempty"`
			MaxBytes           int    `json:"max_bytes,omitempty"`
			DedupWindowSeconds int    `json:"dedup_window_seconds,omitempty"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		if args.Command == "" {
			return map[string]string{"error": "command is required"}, true
		}
		var res any
		var err error
		if backend, ok := s.Backend.(interface {
			ExecCommandWithOptions(context.Context, string, string, string, string, int, string, int, int) (any, error)
		}); ok {
			res, err = backend.ExecCommandWithOptions(ctx, clientIP, args.Device, args.Command, args.Workdir, args.Timeout, args.OutputMode, args.MaxBytes, args.DedupWindowSeconds)
		} else {
			res, err = s.Backend.ExecCommand(ctx, clientIP, args.Device, args.Command, args.Workdir, args.Timeout)
		}
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
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		tokenID := p.TokenID
		if tokenID == "" && p.ID != "" {
			tokenID = p.Type + ":" + p.ID
		}
		ctx = withSessionMemoIdentity(ctx, tokenID, "", "stdio")
	}
	return s.handleRequest(ctx, &http.Request{RemoteAddr: "stdio"}, req)
}

// SessionMemoEntry is an in-memory, session-scoped scratchpad item. Values are
// never written to audit metadata or logs.
type sessionMemoEntry struct {
	value     string
	bytes     int
	updatedAt time.Time
	expiresAt time.Time
}

type SessionMemoSetResult struct {
	Key       string `json:"key"`
	Bytes     int    `json:"bytes"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type SessionMemoValue struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Bytes     int    `json:"bytes"`
	UpdatedAt string `json:"updated_at"`
}

type SessionMemoMetadata struct {
	Key       string `json:"key"`
	Bytes     int    `json:"bytes"`
	UpdatedAt string `json:"updated_at"`
}

type SessionMemoError struct {
	Status  int
	Message string
}

func (e *SessionMemoError) Error() string { return e.Message }

const (
	SessionMemoMaxValueBytes = 4 * 1024
	SessionMemoMaxEntries    = 32
	SessionMemoMaxTotalBytes = 32 * 1024
	SessionMemoMaxKeyBytes   = 256
)

type sessionMemoIdentityKey struct{}
type sessionMemoIdentity struct {
	tokenID    string
	sessionID  string
	remoteAddr string
}

func withSessionMemoIdentity(ctx context.Context, tokenID, sessionID, remoteAddr string) context.Context {
	return context.WithValue(ctx, sessionMemoIdentityKey{}, sessionMemoIdentity{tokenID: tokenID, sessionID: sessionID, remoteAddr: remoteAddr})
}

func sessionMemoIdentityFromContext(ctx context.Context) sessionMemoIdentity {
	if identity, ok := ctx.Value(sessionMemoIdentityKey{}).(sessionMemoIdentity); ok {
		return identity
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return sessionMemoIdentity{}
	}
	tokenID := p.TokenID
	if tokenID == "" && p.ID != "" {
		tokenID = p.Type + ":" + p.ID
	}
	return sessionMemoIdentity{tokenID: tokenID}
}

func (s *Server) sessionMemoIdentityKey(tokenID, sessionID, remoteAddr string) (string, error) {
	if strings.TrimSpace(tokenID) == "" {
		return "", &SessionMemoError{Status: http.StatusUnauthorized, Message: "Unauthorized"}
	}
	key := sessionBudgetKey(tokenID, sessionID, remoteAddr)
	if sessionID == "" {
		// HTTP memo access is activity in the same session used by stats. This
		// also expires an idle session (and clears its memos) before lookup.
		s.budgetMu.Lock()
		s.budgetRecordLocked(key, tokenID, "", time.Now(), true)
		s.budgetMu.Unlock()
		return key, nil
	}
	if sessionID != "" {
		s.budgetMu.Lock()
		record := s.sessionBudgets[key]
		validOwner := record != nil && record.ownerTokenID == tokenID
		hasBudgetRecord := record != nil
		s.budgetMu.Unlock()
		if !hasBudgetRecord {
			s.mu.RLock()
			if sess, ok := s.sessions[sessionID]; ok && sess.ownerTokenID == tokenID {
				validOwner = true
			}
			if sess, ok := s.streamSessions[sessionID]; ok && sess.ownerTokenID == tokenID {
				validOwner = true
			}
			s.mu.RUnlock()
		}
		if !validOwner {
			return "", &SessionMemoError{Status: http.StatusNotFound, Message: "session not found"}
		}
	}
	return key, nil
}

func (s *Server) cleanupSessionMemosLocked(sessionKey string, now time.Time) map[string]sessionMemoEntry {
	items := s.sessionMemos[sessionKey]
	for key, item := range items {
		if !item.expiresAt.IsZero() && !now.Before(item.expiresAt) {
			delete(items, key)
		}
	}
	if len(items) == 0 {
		delete(s.sessionMemos, sessionKey)
		return nil
	}
	return items
}

func sessionMemoUsage(items map[string]sessionMemoEntry) (count, total int) {
	for _, item := range items {
		count++
		total += item.bytes
	}
	return count, total
}

// SetSessionMemo writes or replaces a key in the caller's current session.
func (s *Server) SetSessionMemo(tokenID, sessionID, remoteAddr, key, value string, ttlSeconds *int64) (SessionMemoSetResult, error) {
	key = strings.TrimSpace(key)
	if key == "" || len([]byte(key)) > SessionMemoMaxKeyBytes {
		return SessionMemoSetResult{}, &SessionMemoError{Status: http.StatusBadRequest, Message: fmt.Sprintf("key must be 1-%d bytes", SessionMemoMaxKeyBytes)}
	}
	valueBytes := len([]byte(value))
	var expiresAt time.Time
	if ttlSeconds != nil {
		maxTTL := int64((1<<63 - 1) / int64(time.Second))
		if *ttlSeconds <= 0 || *ttlSeconds > maxTTL {
			return SessionMemoSetResult{}, &SessionMemoError{Status: http.StatusBadRequest, Message: "ttl_seconds must be a positive integer within the supported range"}
		}
		expiresAt = time.Now().Add(time.Duration(*ttlSeconds) * time.Second)
	}
	sessionKey, err := s.sessionMemoIdentityKey(tokenID, sessionID, remoteAddr)
	if err != nil {
		return SessionMemoSetResult{}, err
	}
	now := time.Now()
	s.memoMu.Lock()
	defer s.memoMu.Unlock()
	items := s.cleanupSessionMemosLocked(sessionKey, now)
	if items == nil {
		items = make(map[string]sessionMemoEntry)
		s.sessionMemos[sessionKey] = items
	}
	count, total := sessionMemoUsage(items)
	old, replacing := items[key]
	newCount := count
	newTotal := total - old.bytes + valueBytes
	if !replacing {
		newCount++
	}
	if valueBytes > SessionMemoMaxValueBytes {
		return SessionMemoSetResult{}, &SessionMemoError{Status: http.StatusRequestEntityTooLarge, Message: fmt.Sprintf("value bytes=%d exceeds per-value limit=%d; current usage=%d/%d bytes, entries=%d/%d", valueBytes, SessionMemoMaxValueBytes, total, SessionMemoMaxTotalBytes, count, SessionMemoMaxEntries)}
	}
	if newCount > SessionMemoMaxEntries {
		return SessionMemoSetResult{}, &SessionMemoError{Status: http.StatusRequestEntityTooLarge, Message: fmt.Sprintf("entry quota exceeded: current entries=%d, requested=%d, limit=%d; current usage=%d/%d bytes", count, newCount, SessionMemoMaxEntries, total, SessionMemoMaxTotalBytes)}
	}
	if newTotal > SessionMemoMaxTotalBytes {
		return SessionMemoSetResult{}, &SessionMemoError{Status: http.StatusRequestEntityTooLarge, Message: fmt.Sprintf("session byte quota exceeded: current usage=%d bytes, requested usage=%d bytes, limit=%d bytes; entries=%d/%d", total, newTotal, SessionMemoMaxTotalBytes, count, SessionMemoMaxEntries)}
	}
	items[key] = sessionMemoEntry{value: value, bytes: valueBytes, updatedAt: now, expiresAt: expiresAt}
	result := SessionMemoSetResult{Key: key, Bytes: valueBytes}
	if !expiresAt.IsZero() {
		result.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return result, nil
}

// GetSessionMemo reads one value from the caller's current session.
func (s *Server) GetSessionMemo(tokenID, sessionID, remoteAddr, key string) (SessionMemoValue, bool, error) {
	key = strings.TrimSpace(key)
	sessionKey, err := s.sessionMemoIdentityKey(tokenID, sessionID, remoteAddr)
	if err != nil {
		return SessionMemoValue{}, false, err
	}
	s.memoMu.Lock()
	defer s.memoMu.Unlock()
	items := s.cleanupSessionMemosLocked(sessionKey, time.Now())
	item, found := items[key]
	if !found {
		return SessionMemoValue{}, false, nil
	}
	return SessionMemoValue{Key: key, Value: item.value, Bytes: item.bytes, UpdatedAt: item.updatedAt.UTC().Format(time.RFC3339)}, true, nil
}

// ListSessionMemos returns metadata only; it intentionally has no value field.
func (s *Server) ListSessionMemos(tokenID, sessionID, remoteAddr string) ([]SessionMemoMetadata, error) {
	sessionKey, err := s.sessionMemoIdentityKey(tokenID, sessionID, remoteAddr)
	if err != nil {
		return nil, err
	}
	s.memoMu.Lock()
	defer s.memoMu.Unlock()
	items := s.cleanupSessionMemosLocked(sessionKey, time.Now())
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionMemoMetadata, 0, len(keys))
	for _, key := range keys {
		item := items[key]
		result = append(result, SessionMemoMetadata{Key: key, Bytes: item.bytes, UpdatedAt: item.updatedAt.UTC().Format(time.RFC3339)})
	}
	return result, nil
}

// AuditSessionMemo writes only the key, byte count and non-sensitive status.
func (s *Server) AuditSessionMemo(ctx context.Context, clientIP, action, key string, bytes int, status int, _ string) {
	if sink, ok := s.Backend.(ActionAuditSink); ok {
		sink.AuditMCPAction(ctx, clientIP, "", action, key, status, 0, fmt.Sprintf("bytes=%d", bytes))
	}
	// Suppress the generic MCP error audit for memo tools: the dedicated row
	// already contains only key, byte count and safe status metadata.
	MarkAuditRecorded(ctx)
}
