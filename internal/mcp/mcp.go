package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
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

type SecurityGate interface {
	AuthorizeMCP(device, action, clientIP string) error
}

type Server struct {
	Backend ActionBackend
	Auth    *auth.Manager

	mu       sync.RWMutex
	sessions map[string]chan []byte
}

func NewServer(backend ActionBackend, authMgr *auth.Manager) *Server {
	return &Server{
		Backend:  backend,
		Auth:     authMgr,
		sessions: make(map[string]chan []byte),
	}
}

func (s *Server) newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SSE Handler: GET /mcp/sse
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

	sessionID := s.newSessionID()
	msgCh := make(chan []byte, 64)

	s.mu.Lock()
	s.sessions[sessionID] = msgCh
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		close(msgCh)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Endpoint event telling client where to send POST messages
	endpointURI := fmt.Sprintf("/mcp/message?sessionId=%s", sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURI)
	flusher.Flush()

	notify := r.Context().Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-notify:
			return
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// Message Handler: POST /mcp/message
func (s *Server) HandleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	s.mu.RLock()
	ch, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json-rpc", http.StatusBadRequest)
		return
	}

	resp := s.handleRequest(r.Context(), r, req)

	// If session exists, push response via SSE; otherwise reply directly via HTTP
	respBytes, _ := json.Marshal(resp)
	if exists && ch != nil {
		select {
		case ch <- respBytes:
		default:
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	} else {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
	}
}

func (s *Server) handleRequest(ctx context.Context, r *http.Request, req JSONRPCRequest) JSONRPCResponse {
	switch req.Method {
	case "initialize":
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
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
			{
				Name:        "edit_block",
				Description: "对指定文件执行唯一文本块的原子替换；old_text 必须恰好匹配一次",
				InputSchema: InputSchema{Type: "object", Properties: map[string]PropertyDef{"device": {Type: "string", Description: "目标节点名称"}, "path": {Type: "string", Description: "目标文件路径"}, "old_text": {Type: "string", Description: "必须唯一匹配的原文本块"}, "new_text": {Type: "string", Description: "替换后的文本块"}}, Required: []string{"path", "old_text", "new_text"}},
			},
			{
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

		clientIP := r.RemoteAddr
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

func (s *Server) callTool(ctx context.Context, clientIP string, params CallToolParams) (any, bool) {
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
			if err := gate.AuthorizeMCP(args.Device, "read", clientIP); err != nil {
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
			if err := gate.AuthorizeMCP(args.Device, "mcp", clientIP); err != nil {
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
	return s.handleRequest(ctx, &http.Request{RemoteAddr: "stdio"}, req)
}
