package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed static/*
var staticFS embed.FS

type Runner interface {
	DevicesJSON(http.ResponseWriter, *http.Request)
	AuditsJSON(http.ResponseWriter, *http.Request)
	PanelExec(http.ResponseWriter, *http.Request)
	RotateAPIKey(http.ResponseWriter, *http.Request)
	DeleteDevice(http.ResponseWriter, *http.Request)
}

type Panel struct {
	Password string
	Runner   Runner
	mu       sync.RWMutex
	sessions map[string]time.Time
}

func New(password string, runner Runner) *Panel {
	return &Panel{Password: password, Runner: runner, sessions: make(map[string]time.Time)}
}

func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/panel/login", p.login)
	mux.HandleFunc("/panel/logout", p.logout)
	mux.HandleFunc("/static/", p.static)
	mux.HandleFunc("/panel/api/devices", p.protected(p.handleDevices))
	mux.HandleFunc("/panel/api/audits", p.protected(p.Runner.AuditsJSON))
	mux.HandleFunc("/panel/api/exec", p.protected(p.Runner.PanelExec))
	mux.HandleFunc("/panel/api/key/rotate", p.protected(p.Runner.RotateAPIKey))
	mux.HandleFunc("/", p.index)
	return mux
}

func (p *Panel) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		p.Runner.DeleteDevice(w, r)
		return
	}
	p.Runner.DevicesJSON(w, r)
}

func (p *Panel) index(w http.ResponseWriter, r *http.Request) {
	if !p.valid(r) {
		http.Redirect(w, r, "/panel/login", http.StatusFound)
		return
	}
	data, _ := staticFS.ReadFile("static/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (p *Panel) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		data, _ := staticFS.ReadFile("static/login.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if p.Password == "" || subtle.ConstantTimeCompare([]byte(req.Password), []byte(p.Password)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "session error", 500)
		return
	}
	token := hex.EncodeToString(b)
	p.mu.Lock()
	p.sessions[token] = time.Now().Add(12 * time.Hour)
	p.mu.Unlock()
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: "vpc_session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("vpc_session"); err == nil {
		p.mu.Lock()
		delete(p.sessions, c.Value)
		p.mu.Unlock()
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: "vpc_session", Value: "", Path: "/", HttpOnly: true, Secure: secure, MaxAge: -1, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) valid(r *http.Request) bool {
	c, err := r.Cookie("vpc_session")
	if err != nil || c.Value == "" {
		return false
	}
	p.mu.RLock()
	expiry, ok := p.sessions[c.Value]
	p.mu.RUnlock()
	return ok && time.Now().Before(expiry)
}

func (p *Panel) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.valid(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		next(w, r)
	}
}

func (p *Panel) static(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if len(path) < 9 {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/" + path[len("/static/"):])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	if len(path) >= 3 && path[len(path)-3:] == ".js" {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	if strings.HasSuffix(path, ".png") {
		w.Header().Set("Content-Type", "image/png")
	}
	_, _ = w.Write(data)
}

func (p *Panel) Cleanup() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for now := range t.C {
		p.mu.Lock()
		for k, v := range p.sessions {
			if now.After(v) {
				delete(p.sessions, k)
			}
		}
		p.mu.Unlock()
	}
}
