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
	Password   string
	AdminToken string
	Runner     Runner
	mu         sync.RWMutex
	sessions   map[string]time.Time
}

func New(password, adminToken string, runner Runner) *Panel {
	return &Panel{Password: password, AdminToken: adminToken, Runner: runner, sessions: make(map[string]time.Time)}
}

func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/panel/login", p.login)
	mux.HandleFunc("/panel/logout", p.logout)
	mux.HandleFunc("/panel", p.index)
	mux.HandleFunc("/panel/", p.index)
	mux.HandleFunc("/probe/summary", p.probeSummary)
	mux.HandleFunc("/static/", p.static)
	mux.HandleFunc("/panel/api/devices", p.protected(p.handleDevices))
	mux.HandleFunc("/panel/api/audits", p.protected(p.Runner.AuditsJSON))
	mux.HandleFunc("/panel/api/exec", p.protected(p.Runner.PanelExec))
	mux.HandleFunc("/panel/api/key/rotate", p.protected(p.Runner.RotateAPIKey))
	mux.HandleFunc("/panel/api/mcp/services", p.protected(p.mcpServices))
	mux.HandleFunc("/panel/api/mcp/service", p.protected(p.deleteMCPService))
	mux.HandleFunc("/panel/api/mcp/list", p.protected(p.listAgentMCP))
	mux.HandleFunc("/panel/api/mcp/call", p.protected(p.callAgentMCP))
	mux.HandleFunc("/panel/api/mcp/test", p.protected(p.testMCP))
	mux.HandleFunc("/panel/api/security/settings", p.protected(p.securitySettings))
	mux.HandleFunc("/panel/api/security/settings/update", p.protected(p.updateSecuritySettings))
	mux.HandleFunc("/panel/api/notifications/webhooks", p.protected(p.webhookTargets))
	mux.HandleFunc("/panel/api/notifications/webhook/update", p.protected(p.updateWebhookTarget))
	mux.HandleFunc("/panel/api/notifications/webhook/delete", p.protected(p.deleteWebhookTarget))
	mux.HandleFunc("/panel/api/notifications/webhook/test", p.protected(p.testWebhook))
	mux.HandleFunc("/", p.probe)
	return mux
}

type securityPanelRunner interface {
	SecuritySettingsJSON(http.ResponseWriter, *http.Request)
	UpdateSecuritySettingsJSON(http.ResponseWriter, *http.Request)
}

type mcpPanelRunner interface {
	MCPServicesJSON(http.ResponseWriter, *http.Request)
	DeleteMCPService(http.ResponseWriter, *http.Request)
	ListAgentMCPJSON(http.ResponseWriter, *http.Request)
	CallAgentMCPJSON(http.ResponseWriter, *http.Request)
	TestMCPJSON(http.ResponseWriter, *http.Request)
}

func (p *Panel) mcpServices(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(mcpPanelRunner); ok {
		v.MCPServicesJSON(w, r)
		return
	}
	http.Error(w, "MCP API unavailable", 501)
}
func (p *Panel) deleteMCPService(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(mcpPanelRunner); ok {
		v.DeleteMCPService(w, r)
		return
	}
	http.Error(w, "MCP API unavailable", 501)
}
func (p *Panel) listAgentMCP(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(mcpPanelRunner); ok {
		v.ListAgentMCPJSON(w, r)
		return
	}
	http.Error(w, "MCP API unavailable", 501)
}
func (p *Panel) callAgentMCP(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(mcpPanelRunner); ok {
		v.CallAgentMCPJSON(w, r)
		return
	}
	http.Error(w, "MCP API unavailable", 501)
}
func (p *Panel) securitySettings(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(securityPanelRunner); ok {
		v.SecuritySettingsJSON(w, r)
		return
	}
	http.Error(w, "security API unavailable", 501)
}
func (p *Panel) updateSecuritySettings(w http.ResponseWriter, r *http.Request) {
	if p.AdminToken != "" {
		r.Header.Set("X-Admin-Token", p.AdminToken)
	}
	if v, ok := p.Runner.(securityPanelRunner); ok {
		v.UpdateSecuritySettingsJSON(w, r)
		return
	}
	http.Error(w, "security API unavailable", 501)
}

func (p *Panel) testMCP(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(mcpPanelRunner); ok {
		v.TestMCPJSON(w, r)
		return
	}
	http.Error(w, "MCP API unavailable", 501)
}

type notificationPanelRunner interface {
	WebhookTargetsJSON(http.ResponseWriter, *http.Request)
	UpdateWebhookTargetJSON(http.ResponseWriter, *http.Request)
	DeleteWebhookTargetJSON(http.ResponseWriter, *http.Request)
	TestWebhookJSON(http.ResponseWriter, *http.Request)
}

func (p *Panel) webhookTargets(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(notificationPanelRunner); ok {
		v.WebhookTargetsJSON(w, r)
		return
	}
	http.Error(w, "notification API unavailable", 501)
}
func (p *Panel) updateWebhookTarget(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(notificationPanelRunner); ok {
		v.UpdateWebhookTargetJSON(w, r)
		return
	}
	http.Error(w, "notification API unavailable", 501)
}
func (p *Panel) deleteWebhookTarget(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(notificationPanelRunner); ok {
		v.DeleteWebhookTargetJSON(w, r)
		return
	}
	http.Error(w, "notification API unavailable", 501)
}
func (p *Panel) testWebhook(w http.ResponseWriter, r *http.Request) {
	if v, ok := p.Runner.(notificationPanelRunner); ok {
		v.TestWebhookJSON(w, r)
		return
	}
	http.Error(w, "notification API unavailable", 501)
}

func (p *Panel) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		p.Runner.DeleteDevice(w, r)
		return
	}
	p.Runner.DevicesJSON(w, r)
}

func (p *Panel) probeSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	p.Runner.DevicesJSON(w, r)
}

func (p *Panel) probe(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := staticFS.ReadFile("static/probe.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
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
