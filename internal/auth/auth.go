package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

type Manager struct {
	mu    sync.RWMutex
	token string
}

func NewManager(token string) *Manager { return &Manager{token: token} }

func (m *Manager) Token() string { m.mu.RLock(); defer m.mu.RUnlock(); return m.token }

func (m *Manager) Rotate() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	t := hex.EncodeToString(b)
	m.mu.Lock()
	m.token = t
	m.mu.Unlock()
	return t
}

type Middleware struct {
	Token    string
	Manager  *Manager
	Optional bool
}

func (m Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := ""
		h := r.Header.Get("Authorization")
		if strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		} else if q := r.URL.Query().Get("token"); q != "" {
			got = q
		} else if q := r.URL.Query().Get("key"); q != "" {
			got = q
		} else if q := r.URL.Query().Get("api_key"); q != "" {
			got = q
		}

		expected := m.Token
		if m.Manager != nil {
			expected = m.Manager.Token()
		}

		if got == "" && m.Optional {
			next.ServeHTTP(w, r)
			return
		}

		if expected == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer realm=\"vps-commander\"")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("{\"error\":\"Unauthorized\"}"))
}
