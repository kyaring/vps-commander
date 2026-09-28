package cluster

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
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
	Name          string
	Conn          *websocket.Conn
	ConnectionID  string
	WriteMu       sync.Mutex
	Pending       sync.Map
	LastSeen      atomic.Int64
	LastHeartbeat atomic.Int64
	Connected     atomic.Bool
	LastProfile   *sysinfo.Profile
	Arch          string
	OS            string
	IsLocal       bool
	MCP           *MCPRuntime
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
	if m.Secret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(m.Secret)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	name := r.URL.Query().Get("device_name")
	if name == "" {
		http.Error(w, "device_name required", 400)
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
	n := &Node{Name: name, Conn: c, ConnectionID: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), MCP: NewMCPRuntime()}
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
			"last_heartbeat": n.LastHeartbeat.Load() / int64(time.Second),
		}
		if n.LastProfile != nil {
			item["profile"] = n.LastProfile
		}
		out = append(out, item)
	}
	return out
}

func (m *Manager) call(ctx context.Context, name, action string, payload any) (Message, error) {
	m.mu.RLock()
	n := m.nodes[name]
	m.mu.RUnlock()
	if n == nil || !m.Online(name) {
		return Message{}, errors.New("device offline")
	}
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

func (m *Manager) Exec(ctx context.Context, name string, p ExecPayload) (Result, error) {
	msg, err := m.call(ctx, name, "exec", p)
	if err != nil {
		return Result{}, err
	}
	return Result{msg.ExitCode, msg.Stdout, msg.Stderr, msg.DurationMS}, nil
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
	services, err := m.Store.ListMCPServicesForNode(n.Name)
	if err != nil {
		return
	}
	_ = n.write(Message{Event: "mcp_sync", Payload: services, TS: time.Now().Unix()})
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
	if n == nil {
		return nil, fmt.Errorf("device %s not found", name)
	}
	if n.MCP == nil {
		n.MCP = NewMCPRuntime()
	}
	return n.MCP.List(), nil
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
