package cluster

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/security"
	"github.com/wjyhk/vps-commander/internal/storage"
	"github.com/wjyhk/vps-commander/internal/sysinfo"
)

const deviceOfflineAfter = 70 * time.Second

type Manager struct {
	Secret string
	Store  *storage.Store
	mu     sync.RWMutex
	nodes  map[string]*Node
}

type Node struct {
	Name            string
	Conn            *websocket.Conn
	ConnectionID    string
	WriteMu         sync.Mutex
	OpMu            sync.RWMutex
	Pending         sync.Map
	LastSeen        atomic.Int64
	LastHeartbeat   atomic.Int64
	Connected       atomic.Bool
	LastProfile     *sysinfo.Profile
	Arch            string
	OS              string
	IsLocal         bool
	RemoteIP        string
	CountryCode     string
	Country         string
	MCP             *MCPRuntime
	ProtocolVersion string
	AgentVersion    string
	Capabilities    map[string]bool
}

type Result struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMS int64
}

type FileResult struct {
	Content   string `json:"content"`
	Bytes     int64  `json:"bytes"`
	TotalSize int64  `json:"total_size"`
	HasMore   bool   `json:"has_more"`
	EOF       bool   `json:"eof"`
}

func NewManager(secret string, store *storage.Store) *Manager {
	m := &Manager{Secret: secret, Store: store, nodes: make(map[string]*Node)}
	if store != nil {
		if records, err := store.ListDevices(); err == nil {
			for _, d := range records {
				if d.IsLocal {
					continue
				}
				n := &Node{Name: d.Name, Arch: d.Arch, OS: d.OS, MCP: NewMCPRuntime()}
				n.LastSeen.Store(d.LastSeen * int64(time.Second))
				n.LastHeartbeat.Store(d.LastHeartbeat * int64(time.Second))
				if d.ProfileJSON != "" {
					var p sysinfo.Profile
					if json.Unmarshal([]byte(d.ProfileJSON), &p) == nil {
						n.LastProfile = &p
					}
				}
				n.Connected.Store(false)
				m.nodes[d.Name] = n
				_ = store.UpdateDeviceStatus(d.Name, "offline", d.LastHeartbeat)
			}
		}
	}
	return m
}

func (m *Manager) SetStore(s *storage.Store) {
	m.Store = s
}

func (m *Manager) ServeWS(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	provided := ""
	if len(auth) >= 7 && auth[:7] == "Bearer " {
		provided = auth[7:]
	}
	name := r.URL.Query().Get("device_name")
	if name == "" {
		http.Error(w, "device_name required", 400)
		return
	}
	if m.Store != nil {
		configured, err := m.Store.HasDeviceCredential(name)
		if err != nil {
			http.Error(w, "credential lookup failed", 500)
			return
		}
		if configured {
			valid, err := m.Store.VerifyDeviceCredential(name, provided)
			if err != nil || !valid {
				http.Error(w, "unauthorized", 401)
				return
			}
		} else if m.Secret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(m.Secret)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
	} else if m.Secret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(m.Secret)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	if m.Store != nil {
		if revoked, _ := m.Store.IsRevoked(name); revoked {
			http.Error(w, "device has been revoked by admin", 403)
			return
		}
	}

	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	clientIP := ExtractIPFromRequest(r)
	cc, countryName := ResolveIP(clientIP)
	n := &Node{
		Name:         name,
		Conn:         c,
		ConnectionID: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()),
		RemoteIP:     clientIP,
		CountryCode:  cc,
		Country:      countryName,
		MCP:          NewMCPRuntime(),
		Capabilities: make(map[string]bool),
	}
	n.LastSeen.Store(time.Now().UnixNano())
	n.LastHeartbeat.Store(time.Now().UnixNano())
	n.Connected.Store(true)
	m.mu.Lock()
	old := m.nodes[name]
	m.nodes[name] = n
	m.mu.Unlock()
	if old != nil && old.Conn != nil {
		_ = old.Conn.Close()
	}
	_ = n.write(Message{Event: "registered", Device: name, TS: time.Now().Unix()})
	m.syncMCP(n)
	go m.heartbeat(n)
	go m.readLoop(n)
}

func (n *Node) write(msg Message) error {
	n.WriteMu.Lock()
	defer n.WriteMu.Unlock()
	if err := n.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return n.Conn.WriteJSON(msg)
}

func (m *Manager) readLoop(n *Node) {
	defer func() {
		n.Connected.Store(false)
		conn := n.Conn
		m.mu.RLock()
		current := m.nodes[n.Name] == n
		m.mu.RUnlock()
		if conn != nil {
			_ = conn.Close()
		}
		n.Pending.Range(func(key, v any) bool {
			if ch, ok := v.(chan Message); ok {
				select {
				case ch <- Message{Status: "error", Error: "device disconnected"}:
				default:
				}
			}
			n.Pending.Delete(key)
			return true
		})
		if current && m.Store != nil {
			_ = m.Store.UpdateDeviceStatus(n.Name, "offline", n.LastHeartbeat.Load()/int64(time.Second))
		}
	}()
	conn := n.Conn
	if conn == nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		now := time.Now().UnixNano()
		n.LastSeen.Store(now)
		_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
		if msg.Arch != "" {
			n.Arch = msg.Arch
		}
		if msg.OS != "" {
			n.OS = msg.OS
		}
		if msg.ProtocolVersion != "" {
			n.ProtocolVersion = msg.ProtocolVersion
		}
		if msg.AgentVersion != "" {
			n.AgentVersion = msg.AgentVersion
		}
		if msg.Capabilities != nil {
			n.Capabilities = make(map[string]bool, len(msg.Capabilities))
			for _, c := range msg.Capabilities {
				n.Capabilities[c] = true
			}
		}
		if msg.Profile != nil {
			n.LastProfile = msg.Profile
			n.LastHeartbeat.Store(now)
			if m.Store != nil {
				b, _ := json.Marshal(msg.Profile)
				_ = m.Store.UpsertRemote(n.Name, "online", n.Arch, n.OS, now/int64(time.Second), string(b))
			}
		} else {
			n.LastHeartbeat.Store(now)
			if m.Store != nil {
				_ = m.Store.UpdateDeviceStatus(n.Name, "online", now/int64(time.Second))
			}
		}
		if msg.Event == "pong" || msg.Event == "ping" || msg.Event == "hello" || msg.Event == "profile" {
			continue
		}
		if msg.ID != "" {
			if v, ok := n.Pending.LoadAndDelete(msg.ID); ok {
				if ch, ok := v.(chan Message); ok {
					select {
					case ch <- msg:
					default:
					}
				}
			}
		}
	}
}

func (m *Manager) Online(name string) bool {
	m.mu.RLock()
	n := m.nodes[name]
	m.mu.RUnlock()
	if n == nil || !n.Connected.Load() {
		return false
	}
	return time.Since(time.Unix(0, n.LastSeen.Load())) < deviceOfflineAfter
}

func (m *Manager) RevokeDevice(name, reason string) error {
	m.mu.Lock()
	n := m.nodes[name]
	delete(m.nodes, name)
	m.mu.Unlock()
	if n != nil {
		n.Connected.Store(false)
		if n.Conn != nil {
			_ = n.write(Message{Event: "revoked", Error: "revoked by admin"})
			_ = n.Conn.Close()
		}
	}
	if m.Store != nil {
		return m.Store.RevokeDevice(name, reason)
	}
	return nil
}

func (m *Manager) Devices() []map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]map[string]any, 0, len(m.nodes))
	for name, n := range m.nodes {
		status := "offline"
		lastSeen := time.Unix(0, n.LastSeen.Load())
		if n.Connected.Load() && time.Since(lastSeen) < deviceOfflineAfter {
			status = "online"
		}
		item := map[string]any{
			"name": name, "status": status, "is_local": n.IsLocal,
			"arch": n.Arch, "os": n.OS,
			"remote_ip":      n.RemoteIP,
			"country_code":   n.CountryCode,
			"country":        n.Country,
			"last_heartbeat": n.LastHeartbeat.Load() / int64(time.Second),
		}
		if n.LastProfile != nil {
			item["profile"] = n.LastProfile
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		nameI := fmt.Sprintf("%v", out[i]["name"])
		nameJ := fmt.Sprintf("%v", out[j]["name"])
		return nameI < nameJ
	})
	return out
}

func operationRisk(action string) int {
	if r, ok := security.RequiredRiskForAction(action); ok {
		return r
	}
	if spec, ok := security.Lookup(action); ok {
		return spec.RequiredRisk
	}
	return security.RiskHigh
}

func (n *Node) acquireOperation(action string) func() {
	if operationRisk(action) <= security.RiskLow {
		n.OpMu.RLock()
		return n.OpMu.RUnlock
	}
	n.OpMu.Lock()
	return n.OpMu.Unlock
}

func (m *Manager) call(ctx context.Context, name, action string, payload any) (Message, error) {
	m.mu.RLock()
	n := m.nodes[name]
	m.mu.RUnlock()
	if n == nil || !m.Online(name) {
		return Message{}, errors.New("device offline")
	}
	capability := actionCapability(action)
	if capability != "" && len(n.Capabilities) > 0 && !n.Capabilities[capability] {
		return Message{}, fmt.Errorf("capability missing: %s", capability)
	}
	release := n.acquireOperation(action)
	defer release()
	id := fmt.Sprintf("task_%d", time.Now().UnixNano())
	ch := make(chan Message, 1)
	n.Pending.Store(id, ch)
	if err := n.write(Message{ID: id, Action: action, Payload: payload}); err != nil {
		n.Pending.Delete(id)
		return Message{}, err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return Message{}, errors.New("device disconnected")
		}
		if msg.Status == "error" {
			return Message{}, errors.New(msg.Error)
		}
		return msg, nil
	case <-ctx.Done():
		n.Pending.Delete(id)
		return Message{}, ctx.Err()
	}
}

func actionCapability(action string) string {
	switch action {
	case "exec":
		return "exec_command"
	case "file_read":
		return "read_file"
	case "file_write":
		return "write_file"
	default:
		return action
	}
}

func (m *Manager) StartProcess(ctx context.Context, name string, p StartProcessPayload) (map[string]any, error) {
	msg, err := m.call(ctx, name, "start_process", p)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
func (m *Manager) ReadProcessOutput(ctx context.Context, name, sessionID, owner string) (ProcessOutputPayload, error) {
	msg, err := m.call(ctx, name, "read_process_output", map[string]string{"session_id": sessionID, "owner": owner})
	if err != nil {
		return ProcessOutputPayload{}, err
	}
	var out ProcessOutputPayload
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}
func (m *Manager) InteractProcess(ctx context.Context, name, sessionID, data, owner string) error {
	_, err := m.call(ctx, name, "interact_with_process", ProcessInputPayload{SessionID: sessionID, Data: data, Owner: owner})
	return err
}
func (m *Manager) ForceTerminate(ctx context.Context, name, sessionID, owner string) error {
	_, err := m.call(ctx, name, "force_terminate", map[string]string{"session_id": sessionID, "owner": owner})
	return err
}
func (m *Manager) ListSessions(ctx context.Context, name string) (any, error) {
	msg, err := m.call(ctx, name, "list_sessions", nil)
	if err != nil {
		return nil, err
	}
	return msg.Payload, nil
}

func (m *Manager) ListDirectory(ctx context.Context, name string, p ListDirectoryPayload) ([]Entry, error) {
	msg, err := m.call(ctx, name, "list_directory", p)
	if err != nil {
		return nil, err
	}
	var v []Entry
	b, _ := json.Marshal(msg.Payload)
	err = json.Unmarshal(b, &v)
	return v, err
}
func (m *Manager) GetFileInfo(ctx context.Context, name string, p FileInfoPayload) (FileInfo, error) {
	msg, err := m.call(ctx, name, "get_file_info", p)
	if err != nil {
		return FileInfo{}, err
	}
	var v FileInfo
	b, _ := json.Marshal(msg.Payload)
	err = json.Unmarshal(b, &v)
	return v, err
}
func (m *Manager) Search(ctx context.Context, name string, p SearchPayload) ([]SearchResult, error) {
	msg, err := m.call(ctx, name, "start_search", p)
	if err != nil {
		return nil, err
	}
	var v []SearchResult
	b, _ := json.Marshal(msg.Payload)
	err = json.Unmarshal(b, &v)
	return v, err
}

func (m *Manager) Exec(ctx context.Context, name string, p ExecPayload) (Result, error) {
	msg, err := m.call(ctx, name, "exec", p)
	if err != nil {
		return Result{}, err
	}
	return Result{msg.ExitCode, msg.Stdout, msg.Stderr, msg.DurationMS}, nil
}

func (m *Manager) Diagnostic(ctx context.Context, name string, req executor.DiagnosticRequest) (executor.DiagnosticResult, error) {
	msg, err := m.call(ctx, name, "diagnostic_command", req)
	if err != nil {
		return executor.DiagnosticResult{}, err
	}
	b, _ := json.Marshal(msg.Payload)
	var out executor.DiagnosticResult
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (m *Manager) ReadFile(ctx context.Context, name string, p FileReadPayload) (FileResult, error) {
	msg, err := m.call(ctx, name, "file_read", p)
	if err != nil {
		return FileResult{}, err
	}
	var v FileResult
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &v); err != nil {
		return FileResult{}, err
	}
	return v, nil
}

func (m *Manager) EditBlock(ctx context.Context, name string, p EditBlockPayload) (EditBlockResult, error) {
	msg, err := m.call(ctx, name, "edit_block", p)
	if err != nil {
		return EditBlockResult{}, err
	}
	var v EditBlockResult
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &v); err != nil {
		return EditBlockResult{}, err
	}
	return v, nil
}

func (m *Manager) ReadMultipleFiles(ctx context.Context, name string, p ReadMultipleFilesPayload) (any, error) {
	msg, err := m.call(ctx, name, "read_multiple_files", p)
	if err != nil {
		return nil, err
	}
	return msg.Payload, nil
}
func (m *Manager) CreateDirectory(ctx context.Context, name string, p CreateDirectoryPayload) error {
	_, err := m.call(ctx, name, "create_directory", p)
	return err
}
func (m *Manager) MoveFile(ctx context.Context, name string, p MoveFilePayload) error {
	_, err := m.call(ctx, name, "move_file", p)
	return err
}
func (m *Manager) ListProcesses(ctx context.Context, name string) (any, error) {
	msg, err := m.call(ctx, name, "list_processes", nil)
	if err != nil {
		return nil, err
	}
	return msg.Payload, nil
}
func (m *Manager) WriteFile(ctx context.Context, name string, p FileWritePayload) (int64, error) {
	msg, err := m.call(ctx, name, "file_write", p)
	if err != nil {
		return 0, err
	}
	var v struct {
		Bytes int64 `json:"bytes"`
	}
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &v); err != nil {
		return 0, err
	}
	return v.Bytes, nil
}

func (m *Manager) syncMCP(n *Node) {
	if n == nil || n.MCP == nil || m.Store == nil {
		return
	}
	stored, err := m.Store.ListMCPServicesForNode(n.Name)
	if err != nil {
		return
	}
	services := make([]MCPService, 0, len(stored))
	for _, v := range stored {
		services = append(services, decodeStoredMCPService(v))
	}
	_ = n.write(Message{Event: "mcp_sync", Payload: services, TS: time.Now().Unix()})
}

func decodeStoredMCPService(v storage.MCPService) MCPService {
	out := MCPService{ID: v.ID, Name: v.Name, Description: v.Description, Transport: v.Transport, Command: v.Command, URL: v.URL}
	_ = json.Unmarshal([]byte(v.ArgsJSON), &out.Args)
	_ = json.Unmarshal([]byte(v.EnvJSON), &out.Env)
	_ = json.Unmarshal([]byte(v.HeadersJSON), &out.Headers)
	return out
}

func (m *Manager) SyncMCPServices() {
	m.mu.RLock()
	nodes := make([]*Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		nodes = append(nodes, n)
	}
	m.mu.RUnlock()
	for _, n := range nodes {
		if n.Connected.Load() {
			m.syncMCP(n)
		}
	}
}

func (m *Manager) ListMCP(name string) ([]MCPService, error) {
	m.mu.RLock()
	n := m.nodes[name]
	m.mu.RUnlock()
	if n == nil || !m.Online(name) {
		return nil, fmt.Errorf("device %s is offline", name)
	}
	msg, err := m.call(context.Background(), name, "mcp_list", nil)
	if err != nil {
		return nil, err
	}
	var out []MCPService
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if n.MCP == nil {
		n.MCP = NewMCPRuntime()
	}
	n.MCP.Sync(out)
	return out, nil
}

func (m *Manager) CallMCP(ctx context.Context, name, server, tool string, args any) (MCPCallResult, error) {
	m.mu.RLock()
	n := m.nodes[name]
	m.mu.RUnlock()
	if n == nil || !m.Online(name) {
		return MCPCallResult{}, fmt.Errorf("device %s is offline", name)
	}
	msg, err := m.call(ctx, name, "mcp_call", map[string]any{"server": server, "tool": tool, "args": args})
	if err != nil {
		return MCPCallResult{}, err
	}
	var out MCPCallResult
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &out); err != nil {
		return MCPCallResult{}, err
	}
	return out, nil
}

func (m *Manager) TestMCP(ctx context.Context, name string, service MCPService) ([]map[string]any, error) {
	m.mu.RLock()
	n := m.nodes[name]
	m.mu.RUnlock()
	if n == nil || !m.Online(name) {
		return nil, fmt.Errorf("device %s is offline", name)
	}
	msg, err := m.call(ctx, name, "mcp_test", service)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	b, _ := json.Marshal(msg.Payload)
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Manager) heartbeat(n *Node) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.RLock()
		current := m.nodes[n.Name] == n
		m.mu.RUnlock()
		if !current {
			return
		}
		if err := n.write(Message{Event: "ping", TS: time.Now().Unix()}); err != nil {
			_ = n.Conn.Close()
			return
		}
	}
}
