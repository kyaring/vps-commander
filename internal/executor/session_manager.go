package executor

import (
	"context"
	"errors"
	"sync"
	"time"
)

const MaxSessionsPerManager = MaxSessionBufferTotal / DefaultRingCapacity

type SessionInfo struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
	Running   bool      `json:"running"`
	ExitCode  int       `json:"exit_code"`
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	limit    int
}

func NewSessionManager(limit int) *SessionManager {
	if limit <= 0 || limit > MaxSessionsPerManager {
		limit = MaxSessionsPerManager
	}
	return &SessionManager{sessions: make(map[string]*Session), limit: limit}
}
func (m *SessionManager) Start(ctx context.Context, id, command, workdir, owner string, capacity int) (*Session, error) {
	m.mu.Lock()
	if _, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return nil, errors.New("session already exists")
	}
	if len(m.sessions) >= m.limit {
		m.mu.Unlock()
		return nil, errors.New("session limit reached")
	}
	s, err := StartSession(ctx, id, command, workdir, owner, capacity)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if _, exists := m.sessions[id]; exists {
		m.mu.Unlock()
		_ = s.Kill()
		return nil, errors.New("session already exists")
	}
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}
func (m *SessionManager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}
func (m *SessionManager) Remove(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return false
	}
	delete(m.sessions, id)
	return true
}
func (m *SessionManager) List() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		code, done, _ := s.Status()
		out = append(out, SessionInfo{ID: s.ID, Command: s.Command, StartedAt: s.StartedAt, Running: !done, ExitCode: code})
	}
	return out
}
func (m *SessionManager) Stop(id string, force bool) error {
	s, ok := m.Get(id)
	if !ok {
		return errors.New("session not found")
	}
	if force {
		return s.Kill()
	}
	return s.Terminate()
}
func (m *SessionManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		if s.Expired() {
			_, done, _ := s.Status()
			if !done {
				_ = s.Kill()
				continue
			}
			delete(m.sessions, id)
		}
	}
}
