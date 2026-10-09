package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	AuditMCP(ctx context.Context, clientIP, device, tool, result, detail string)
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
	w.Header().Set("Access-Control-Allow-Origin", "*")

	endpointURI := fmt.Sprintf("/mcp/message?sessionId=%s", sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURI)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sess.done:
			return
		case msg := <-sess.ch:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
			flusher.Flush()
		case <-ticker.C:
			if sess.idleFor() >= sseSessionIdleTimeout {
				s.removeSession(sessionID, sess)
				return
			}
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// HandleMessage is the legacy POST /mcp/message endpoint. A full outbound
// queue is never silently ignored: the sender gets up to five seconds, after
// which the affected SSE session is closed and the request fails explicitly.
func (s *Server) HandleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	s.mu.RLock()
	sess, exists := s.sessions[sessionID]
	s.mu.RUnlock()
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
	if isRateLimitedResponse(resp) {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to encode JSON-RPC response", http.StatusInternalServerError)
		return
	}
	if exists {
		timer := time.NewTimer(sseOutboundQueueWait)
		defer timer.Stop()
		select {
		case sess.ch <- respBytes:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("accepted"))
		case <-sess.done:
			http.Error(w, "MCP SSE session is closed", http.StatusGone)
		case <-timer.C:
			s.removeSession(sessionID, sess)
			http.Error(w, "MCP SSE outbound queue remained full for 5 seconds; session closed", http.StatusServiceUnavailable)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(respBytes)
}

func isRateLimitedResponse(resp JSONRPCResponse) bool {
	result, ok := resp.Result.(CallToolResult)
	if !ok || !result.IsError {
		return false
	}
	for _, content := range result.Content {
		if strings.Contains(content.Text, "rate_limited") {
			return true
		}
	}
	return false
}

func (s *Server) decodeAndHandleRequest(w http.ResponseWriter, r *http.Request) (JSONRPCResponse, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return JSONRPCResponse{}, false
	}
	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		http.Error(w, "invalid json-rpc", http.StatusBadRequest)
		return JSONRPCResponse{}, false
	}
	return s.handleRequest(r.Context(), r, req), true
}

// HandleStreamableHTTP implements MCP Streamable HTTP using ordinary JSON
// responses. Session IDs are optional: clients can use stateless requests, or
// retain the ID returned by initialize to bind later requests to the same token.
func (s *Server) HandleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
		return
	}

	clientSessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	protocolVersion := "2025-03-26"
	if clientSessionID != "" {
		s.mu.Lock()
		stored, exists := s.streamSessions[clientSessionID]
		if exists && time.Since(stored.lastActivity) > sseSessionIdleTimeout {
			delete(s.streamSessions, clientSessionID)
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
	if isRateLimitedResponse(resp) {
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
		res, isErr := s.callTool(ctx, clientIP, params)
		resJSON, _ := json.MarshalIndent(res, "", "  ")

		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []TextContent{
					{Type: "text", Text: string(resJSON)},
				},
				IsError: isErr,
			},
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
			sink.AuditMCP(ctx, clientIP, device, params.Name, "denied", err.Error())
		}
		return map[string]string{"error": err.Error()}, true
	}
	switch params.Name {
	case "list_devices":
		devs, err := s.Backend.ListDevices(ctx)
		if err != nil {
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
			return map[string]string{"error": err.Error()}, true
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
				return map[string]string{"error": err.Error()}, true
			}
		}
		res, err := b.ListAgentMCP(ctx, args.Device)
		if err != nil {
			return map[string]string{"error": err.Error()}, true
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
				return map[string]string{"error": err.Error()}, true
			}
		}
		res, err := b.CallAgentMCP(ctx, clientIP, args.Device, args.Server, args.Tool, av)
		if err != nil {
			return map[string]string{"error": err.Error()}, true
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
