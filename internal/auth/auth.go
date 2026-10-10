package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Principal struct {
	Type        string
	ID          string
	Name        string
	Scopes      map[string]bool
	Devices     map[string]bool
	TokenID     string
	MaxSessions int
	RateRPS     int
}

func (p Principal) HasScope(scope string) bool {
	return p.Type == "panel" || p.Type == "system" || p.Scopes[scope]
}

func (p Principal) AllowsDevice(device string) bool {
	return len(p.Devices) == 0 || p.Devices[device]
}

type TokenValidator interface {
	ValidateAPIToken(token string, ip string) (Principal, bool)
}

type TokenValidatorFunc func(token string, ip string) (Principal, bool)

func (f TokenValidatorFunc) ValidateAPIToken(token, ip string) (Principal, bool) { return f(token, ip) }

type Manager struct {
	mu        sync.RWMutex
	token     string
	validator TokenValidator
}

func NewManager(token string) *Manager { return &Manager{token: token} }
func NewManagerWithValidator(token string, v TokenValidator) *Manager {
	return &Manager{token: token, validator: v}
}

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

func (m *Manager) PrincipalFromRequest(r *http.Request) (Principal, bool) {
	got := bearer(r)
	if got == "" {
		return Principal{}, false
	}
	if m.validator != nil {
		if p, ok := m.validator.ValidateAPIToken(got, ClientIP(r)); ok {
			return p, true
		}
		return Principal{}, false
	}
	expected := m.Token()
	if expected != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1 {
		return Principal{Type: "api_token", ID: "legacy-env", Name: "legacy-env", TokenID: "legacy-env", RateRPS: 10,
			Scopes: map[string]bool{"devices:read": true, "files:read": true, "files:write": true, "exec": true, "process": true, "mcp:call": true, "admin": true}}, true
	}
	return Principal{}, false
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

type Middleware struct {
	Token           string
	Manager         *Manager
	Optional        bool
	WWWAuthenticate string
}

func (m Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := bearer(r); got != "" {
			if m.Manager != nil {
				if p, ok := m.Manager.PrincipalFromRequest(r); ok {
					next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
					return
				}
			} else if m.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(m.Token)) == 1 {
				p := Principal{Type: "api_token", ID: "legacy-env", Name: "legacy-env", TokenID: "legacy-env", RateRPS: 10,
					Scopes: map[string]bool{"devices:read": true, "files:read": true, "files:write": true, "exec": true, "process": true, "mcp:call": true, "admin": true}}
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
				return
			}
		}
		if m.Optional {
			next.ServeHTTP(w, r)
			return
		}
		unauthorizedWithChallenge(w, m.WWWAuthenticate)
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func ClientIP(r *http.Request) string {
	if strings.EqualFold(os.Getenv("VPS_COMMANDER_TRUST_PROXY"), "on") {
		// X-Real-IP is often forwarded unchanged by reverse proxies and can be
		// supplied by a direct client. Prefer the rightmost valid XFF entry,
		// which must be appended by the trusted edge proxy.
		if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
			parts := strings.Split(v, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				if ip := strings.TrimSpace(parts[i]); net.ParseIP(ip) != nil {
					return ip
				}
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func unauthorized(w http.ResponseWriter) {
	unauthorizedWithChallenge(w, "")
}

func unauthorizedWithChallenge(w http.ResponseWriter, challenge string) {
	w.Header().Set("Content-Type", "application/json")
	if challenge == "" {
		challenge = `Bearer realm="vps-commander"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
}

func Expired(at *int64) bool { return at != nil && *at <= time.Now().Unix() }
