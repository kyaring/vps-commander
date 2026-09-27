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
)

type Manager struct {
	Secret string
	mu     sync.RWMutex
	nodes  map[string]*Node
}

type Node struct {
	Name     string
	Conn     *websocket.Conn
	WriteMu  sync.Mutex
	Pending  sync.Map
	LastSeen atomic.Int64
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

func NewManager(secret string) *Manager {
	return &Manager{Secret: secret, nodes: make(map[string]*Node)}
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
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	n := &Node{Name: name, Conn: c}
	n.LastSeen.Store(time.Now().UnixNano())
	m.mu.Lock()
	if old := m.nodes[name]; old != nil {
		_ = old.Conn.Close()
	}
	m.nodes[name] = n
	m.mu.Unlock()
	_ = n.write(Message{Event: "registered", Device: name, TS: time.Now().Unix()})
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
		m.mu.Lock()
		if m.nodes[n.Name] == n {
			delete(m.nodes, n.Name)
		}
		m.mu.Unlock()
		_ = n.Conn.Close()
		n.Pending.Range(func(key, v any) bool {
			if ch, ok := v.(chan Message); ok {
				select {
				case ch <- Message{Status: "error", Error: "device disconnected"}:
				default:
				}
				n.Pending.Delete(key)
			}
			return true
		})
	}()
	_ = n.Conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	for {
		var msg Message
		if err := n.Conn.ReadJSON(&msg); err != nil {
			return
		}
		n.LastSeen.Store(time.Now().UnixNano())
		_ = n.Conn.SetReadDeadline(time.Now().Add(75 * time.Second))
		if msg.Event == "pong" || msg.Event == "ping" {
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
	defer m.mu.RUnlock()
	n := m.nodes[name]
	return n != nil && time.Since(time.Unix(0, n.LastSeen.Load())) < 70*time.Second
}

func (m *Manager) Devices() []map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]map[string]any, 0, len(m.nodes))
	for name, n := range m.nodes {
		status := "online"
		lastSeen := time.Unix(0, n.LastSeen.Load())
		if time.Since(lastSeen) >= 70*time.Second {
			status = "offline"
		}
		out = append(out, map[string]any{"name": name, "status": status, "is_local": false, "last_heartbeat": lastSeen.Unix()})
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
