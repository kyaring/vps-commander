package cluster

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type MCPService struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type MCPCallResult struct {
	Server     string `json:"server"`
	Tool       string `json:"tool"`
	Result     any    `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type mcpRPC struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

type MCPRuntime struct {
	mu       sync.RWMutex
	services map[string]MCPService
}

func NewMCPRuntime() *MCPRuntime { return &MCPRuntime{services: make(map[string]MCPService)} }

func (r *MCPRuntime) Sync(services []MCPService) {
	next := make(map[string]MCPService, len(services))
	for _, s := range services {
		if s.ID == "" || s.Transport == "" {
			continue
		}
		next[s.ID] = s
	}
	r.mu.Lock()
	r.services = next
	r.mu.Unlock()
}

func (r *MCPRuntime) List() []MCPService {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]MCPService, 0, len(r.services))
	for _, s := range r.services {
		out = append(out, s)
	}
	return out
}

func (r *MCPRuntime) Get(id string) (MCPService, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.services[id]
	return s, ok
}

func (r *MCPRuntime) Call(ctx context.Context, server, tool string, args any) (any, error) {
	s, ok := r.Get(server)
	if !ok {
		return nil, fmt.Errorf("MCP service %q is not mounted", server)
	}
	switch strings.ToLower(s.Transport) {
	case "stdio":
		return callStdio(ctx, s, tool, args)
	case "http", "sse":
		return callHTTP(ctx, s, tool, args)
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", s.Transport)
	}
}

func callStdio(ctx context.Context, s MCPService, tool string, args any) (any, error) {
	if s.Command == "" {
		return nil, errors.New("MCP stdio command is empty")
	}
	cmd := exec.CommandContext(ctx, s.Command, s.Args...)
	if len(s.Env) > 0 {
		cmd.Env = append(os.Environ(), envList(s.Env)...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	enc := json.NewEncoder(stdin)
	dec := bufio.NewReaderSize(stdout, 64*1024)
	id := int64(1)
	if err := enc.Encode(mcpRPC{JSONRPC: "2.0", ID: id, Method: "initialize", Params: map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "vps-commander-agent", "version": "1.0"},
	}}); err != nil {
		return nil, err
	}
	if _, err := readRPC(ctx, dec, id); err != nil {
		return nil, err
	}
	if err := enc.Encode(mcpRPC{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}}); err != nil {
		return nil, err
	}
	id++
	if err := enc.Encode(mcpRPC{JSONRPC: "2.0", ID: id, Method: "tools/call", Params: map[string]any{"name": tool, "arguments": args}}); err != nil {
		return nil, err
	}
	return readRPC(ctx, dec, id)
}

func readRPC(ctx context.Context, r *bufio.Reader, wantID int64) (any, error) {
	for {
		lineCh := make(chan []byte, 1)
		errCh := make(chan error, 1)
		go func() {
			b, err := r.ReadBytes('\n')
			if err != nil && len(b) == 0 {
				errCh <- err
				return
			}
			lineCh <- bytes.TrimSpace(b)
		}()
		var line []byte
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-errCh:
			return nil, err
		case line = <-lineCh:
		}
		if len(line) == 0 {
			continue
		}
		var resp mcpRPCResponse
		if json.Unmarshal(line, &resp) != nil {
			continue
		}
		if resp.ID == nil {
			continue
		}
		if n, ok := resp.ID.(float64); !ok || int64(n) != wantID {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		var out any
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &out); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
}

func callHTTP(ctx context.Context, s MCPService, tool string, args any) (any, error) {
	if s.URL == "" {
		return nil, errors.New("MCP HTTP URL is empty")
	}
	client := &http.Client{Timeout: 0}
	var session string
	post := func(req mcpRPC) (any, http.Header, error) {
		b, _ := json.Marshal(req)
		h := make(http.Header)
		h.Set("Content-Type", "application/json")
		h.Set("Accept", "application/json, text/event-stream")
		for k, v := range s.Headers {
			h.Set(k, v)
		}
		if session != "" {
			h.Set("Mcp-Session-Id", session)
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(b))
		if err != nil {
			return nil, nil, err
		}
		hr.Header = h
		resp, err := client.Do(hr)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, resp.Header, fmt.Errorf("MCP HTTP status %s", resp.Status)
		}
		if v := resp.Header.Get("Mcp-Session-Id"); v != "" {
			session = v
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		if err != nil {
			return nil, nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, resp.Header, nil
		}
		if bytes.Contains(data, []byte("event:")) {
			data = parseSSEData(data)
		}
		var rr mcpRPCResponse
		if err := json.Unmarshal(bytes.TrimSpace(data), &rr); err != nil {
			return nil, resp.Header, err
		}
		if rr.Error != nil {
			return nil, resp.Header, fmt.Errorf("MCP error %d: %s", rr.Error.Code, rr.Error.Message)
		}
		var out any
		if len(rr.Result) > 0 {
			if err := json.Unmarshal(rr.Result, &out); err != nil {
				return nil, resp.Header, err
			}
		}
		return out, resp.Header, nil
	}
	if strings.EqualFold(s.Transport, "sse") {
		endpoint, err := discoverSSEEndpoint(ctx, s)
		if err != nil {
			return nil, err
		}
		old := s.URL
		s.URL = endpoint
		defer func() { s.URL = old }()
	}
	if _, _, err := post(mcpRPC{JSONRPC: "2.0", ID: int64(1), Method: "initialize", Params: map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "vps-commander-agent", "version": "1.0"}}}); err != nil {
		return nil, err
	}
	if _, _, err := post(mcpRPC{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}}); err != nil {
		return nil, err
	}
	return func() (any, error) {
		v, _, err := post(mcpRPC{JSONRPC: "2.0", ID: int64(2), Method: "tools/call", Params: map[string]any{"name": tool, "arguments": args}})
		return v, err
	}()
}

func discoverSSEEndpoint(ctx context.Context, s MCPService) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("MCP SSE status %s", resp.Status)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 256*1024))
	isEndpoint := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			isEndpoint = strings.TrimSpace(strings.TrimPrefix(line, "event:")) == "endpoint"
			continue
		}
		if isEndpoint && strings.HasPrefix(line, "data:") {
			u := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if strings.HasPrefix(u, "/") {
				base := *resp.Request.URL
				base.Path = u
				base.RawQuery = ""
				return base.String(), nil
			}
			return u, nil
		}
	}
	return "", errors.New("MCP SSE endpoint event not received")
}

func parseSSEData(b []byte) []byte {
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return b
}
func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
