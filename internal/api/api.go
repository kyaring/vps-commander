package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/model"
	"github.com/wjyhk/vps-commander/internal/notify"
	"github.com/wjyhk/vps-commander/internal/security"
	"github.com/wjyhk/vps-commander/internal/storage"
	"github.com/wjyhk/vps-commander/internal/sysinfo"
	panelweb "github.com/wjyhk/vps-commander/internal/web"
)

//go:embed openapi.json
var openAPIFS embed.FS

const (
	defaultFileLimit int64 = 256 * 1024
	maxFileLimit     int64 = 1024 * 1024
)

type Server struct {
	Auth             *auth.Manager
	MCP              *mcp.Server
	Exec             executor.Local
	Cluster          *cluster.Manager
	Store            *storage.Store
	LocalName        string
	Notify           *notify.Manager
	Policy           *security.Snapshot
	AdminToken       string
	InstallCommand   string
	ExecLimiter      chan struct{}
	SearchLimiter    chan struct{}
	TokenLimiter     *TokenLimiter
	LocalSessions    *executor.SessionManager
	inFlightExec     atomic.Int64
	http429Count     atomic.Uint64
	legacyAuthMu     sync.Mutex
	legacyAuthLast   map[string]time.Time
	eventEmitMu      sync.Mutex
	eventEmitWindows map[string]eventEmitWindow
}

const (
	SecurityLow     = "low"
	SecurityMedium  = "medium"
	SecurityHigh    = "high"
	SecurityInherit = "inherit"
)

func (s *Server) LoadSecurityPolicySnapshot() error {
	if s.Store == nil {
		return fmt.Errorf("security store unavailable")
	}
	state, err := s.Store.LoadSecurityPolicy()
	if err != nil {
		return err
	}
	devices := make(map[string]security.DevicePolicy, len(state.Nodes))
	for node, mode := range state.Nodes {
		devices[node] = security.DevicePolicy{Mode: mode}
	}
	global := security.ModeRisk(state.GlobalMode)
	if global == 0 {
		global = security.RiskMedium
	}
	if s.Policy == nil {
		s.Policy = security.New(global, devices, state.Version)
		return nil
	}
	return s.Policy.Publish(global, devices, state.Version)
}

func (s *Server) GetEffectiveSecurityMode(node string) string {
	// "local" is the public MCP/CLI device alias for the Hub control node.
	// Normalize it before security-policy lookup; otherwise Phase 2 treats it
	// as a literal, unregistered remote node and returns "unknown" (default deny).
	if node == "local" {
		node = s.LocalName
	}
	if s.Policy == nil && s.Store != nil {
		if mode, ok := s.Store.GetNodeSecurityMode(node); ok {
			if mode != SecurityInherit {
				return mode
			}
			return s.Store.GetGlobalSecurityMode()
		}
		if node == s.LocalName {
			return s.Store.GetGlobalSecurityMode()
		}
		return "unknown"
	}
	if s.Policy != nil {
		snap := s.Policy.Current()
		risk := s.Policy.EffectiveRisk(node)
		if p, ok := snap.Devices[node]; ok && security.ModeRisk(p.Mode) != 0 {
			return p.Mode
		}
		if node != s.LocalName && (s.Cluster == nil || !s.Cluster.Online(node)) {
			return "unknown"
		}
		for mode, v := range map[string]int{"low": security.RiskLow, "medium": security.RiskMedium, "high": security.RiskHigh} {
			if v == risk {
				return mode
			}
		}
	}
	if node == s.LocalName && s.Store != nil {
		return s.Store.GetGlobalSecurityMode()
	}
	if s.Cluster != nil && s.Cluster.Online(node) && s.Store != nil {
		return s.Store.GetGlobalSecurityMode()
	}
	return "unknown"
}
func securityAllows(mode, action string) bool {
	required, ok := security.RequiredRiskForAction(action)
	if !ok {
		return false
	}
	return security.ModeRisk(mode) >= required
}

func (s *Server) normalizeDevice(device string) string {
	device = strings.TrimSpace(device)
	if device == "" || device == "local" {
		return s.LocalName
	}
	return device
}

func (s *Server) requireDeviceAccess(w http.ResponseWriter, r *http.Request, device string) bool {
	device = s.normalizeDevice(device)
	p, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("WWW-Authenticate", `Bearer realm="vps-commander"`)
		_ = s.audit(r, device, "auth_denied", "", http.StatusUnauthorized, 0, "missing principal")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
		return false
	}
	if !p.AllowsDevice(device) {
		_ = s.audit(r, device, "device_denied", "", 0, 0, "token device whitelist denied")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"device denied","device":%q}`, device)))
		return false
	}
	return true
}

func (s *Server) requireSecurity(w http.ResponseWriter, r *http.Request, device, action, detail string) bool {
	device = s.normalizeDevice(device)
	if !s.requireDeviceAccess(w, r, device) {
		return false
	}
	mode := s.GetEffectiveSecurityMode(device)
	if securityAllows(mode, action) {
		return true
	}
	reason := fmt.Sprintf("security mode %s denies %s", mode, action)
	if detail != "" {
		reason += ": " + detail
	}
	_ = s.audit(r, device, "security_denied", action, http.StatusForbidden, 0, reason)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"forbidden","device":%q,"mode":%q,"reason":%q}`, device, mode, reason)))
	return false
}

func (s *Server) NotificationSettingsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings, err := s.Store.GetNotificationSettings()
	if err != nil {
		http.Error(w, "notification settings unavailable", http.StatusInternalServerError)
		return
	}
	jsonOut(w, settings)
}

func validateNotificationSettings(v storage.NotificationSettings) error {
	if len(v.OfflineReminderMinutes) != 3 || len(v.ResourceReminderMinutes) != 3 {
		return fmt.Errorf("each reminder schedule must contain exactly three time points")
	}
	for i, n := range v.OfflineReminderMinutes {
		if n < 1 || n > 10080 || (i > 0 && n <= v.OfflineReminderMinutes[i-1]) {
			return fmt.Errorf("offline reminder minutes must be ascending values from 1 to 10080")
		}
	}
	for i, n := range v.ResourceReminderMinutes {
		if n < 0 || n > 10080 || (i > 0 && n <= v.ResourceReminderMinutes[i-1]) {
			return fmt.Errorf("resource reminder minutes must be ascending values from 0 to 10080")
		}
	}
	morning, err := time.Parse("15:04", v.MorningReportTime)
	if err != nil {
		return fmt.Errorf("invalid morning report time")
	}
	evening, err := time.Parse("15:04", v.EveningReportTime)
	if err != nil {
		return fmt.Errorf("invalid evening report time")
	}
	if morning.Hour() == evening.Hour() && morning.Minute() == evening.Minute() {
		return fmt.Errorf("morning and evening report times must differ")
	}
	for _, threshold := range []float64{v.MemoryThreshold, v.DiskThreshold, v.CPUThreshold} {
		if threshold < 1 || threshold > 100 {
			return fmt.Errorf("resource thresholds must be between 1 and 100")
		}
	}
	return nil
}

func (s *Server) UpdateNotificationSettingsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.AdminToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(s.AdminToken)) != 1 {
		http.Error(w, "admin authorization required", http.StatusForbidden)
		return
	}
	var q storage.NotificationSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&q); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := validateNotificationSettings(q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Store.SetNotificationSettings(q); err != nil {
		http.Error(w, "notification settings save failed", http.StatusInternalServerError)
		return
	}
	_ = s.Store.AuditEntry(storage.AuditEntry{Action: "notification_setting", ActorType: "panel", ActorID: "panel", ActorName: "panel:admin", Transport: "panel", TargetType: "setting", Target: "notification_settings", Result: "ok"})
	jsonOut(w, map[string]any{"ok": true, "settings": q})
}

func (s *Server) WebhookTargetsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	items, err := s.Store.ListWebhookTargets()
	if err != nil {
		http.Error(w, "notification query failed", 500)
		return
	}
	// Legacy webhook URLs are credentials; expose only scheme and host.
	for i := range items {
		items[i].URL = maskLegacyWebhookURL(items[i].URL)
	}
	jsonOut(w, items)
}
func maskWebhookURL(v string) string {
	if len(v) <= 12 {
		return "***"
	}
	return v[:8] + "***" + v[len(v)-4:]
}
func (s *Server) legacyWebhookGone(w http.ResponseWriter, r *http.Request) {
	_ = s.audit(r, "", "notification_setting_legacy_gone", r.URL.Path, http.StatusGone, 0, "legacy webhook API removed; use /api/v1/notifications/channels")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "legacy webhook API removed; use /api/v1/notifications/channels"})
}

func (s *Server) UpdateWebhookTargetJSON(w http.ResponseWriter, r *http.Request) {
	s.legacyWebhookGone(w, r)
}
func (s *Server) DeleteWebhookTargetJSON(w http.ResponseWriter, r *http.Request) {
	s.legacyWebhookGone(w, r)
}
func (s *Server) TestWebhookJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var v notify.Target
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&v); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if s.Notify == nil {
		http.Error(w, "notification engine unavailable", 503)
		return
	}
	if err := s.Notify.TestTarget(v); err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}

func (s *Server) LegacyKeyStatusJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	disabled, err := s.Store.LegacyKeyDisabled()
	if err != nil {
		http.Error(w, "legacy key status unavailable", http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]any{"disabled": disabled})
}

func (s *Server) UpdateLegacyKeyStatusJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.AdminToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(s.AdminToken)) != 1 {
		http.Error(w, "admin authorization required", http.StatusForbidden)
		return
	}
	var q struct {
		Disabled *bool `json:"disabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&q); err != nil || q.Disabled == nil {
		http.Error(w, "disabled boolean required", http.StatusBadRequest)
		return
	}
	if err := s.Store.SetLegacyKeyDisabled(*q.Disabled); err != nil {
		http.Error(w, "legacy key setting update failed", http.StatusInternalServerError)
		return
	}
	_ = s.Store.AuditEntry(storage.AuditEntry{Action: "legacy_key_setting", ActorType: "panel", ActorID: "panel", ActorName: "panel:admin", Transport: "panel", TargetType: "setting", Target: "legacy_key_disabled", Result: "ok", Command: fmt.Sprintf("disabled=%t", *q.Disabled)})
	jsonOut(w, map[string]any{"ok": true, "disabled": *q.Disabled})
}

func (s *Server) RecordLegacyRejected(r *http.Request, p auth.Principal) {
	if s.Store == nil {
		return
	}
	transport := "rest"
	switch r.URL.Path {
	case "/mcp/sse", "/mcp/message":
		transport = "mcp-sse"
	case "/mcp":
		transport = "mcp-http"
	}
	ip := auth.ClientIP(r)
	const window = 10 * time.Minute
	now := time.Now()
	key := "legacy_rejected\x00" + ip + "\x00" + transport
	s.legacyAuthMu.Lock()
	if s.legacyAuthLast == nil {
		s.legacyAuthLast = make(map[string]time.Time)
	}
	if last := s.legacyAuthLast[key]; !last.IsZero() && now.Sub(last) < window {
		s.legacyAuthMu.Unlock()
		return
	}
	// Persist the throttle by querying the same action/IP/transport tuple used
	// for successful legacy auth, so rejected retries remain bounded after restart.
	if recent, err := s.Store.HasRecentAuditAction("legacy_rejected", ip, transport, now.Add(-window).Unix()); err == nil && recent {
		s.legacyAuthLast[key] = now
		s.legacyAuthMu.Unlock()
		return
	}
	for k, last := range s.legacyAuthLast {
		if now.Sub(last) >= window {
			delete(s.legacyAuthLast, k)
		}
	}
	s.legacyAuthLast[key] = now
	s.legacyAuthMu.Unlock()
	_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_rejected", Command: r.UserAgent(), ErrorMsg: "legacy API key disabled; use a named API token", ActorType: "api_token", ActorID: "legacy-env", ActorName: "legacy-env", Transport: transport, TargetType: "path", Target: r.URL.Path, Result: "denied", ExitCode: http.StatusUnauthorized})
}

func (s *Server) LegacyUsageJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Store == nil {
		http.Error(w, "storage unavailable", http.StatusInternalServerError)
		return
	}
	summary, err := s.Store.LegacyUsageSummary(time.Now().Add(-7 * 24 * time.Hour).Unix())
	if err != nil {
		http.Error(w, "legacy usage query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}

func (s *Server) SecuritySettingsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	nodes, err := s.Store.ListNodeSecurityModes()
	if err != nil {
		http.Error(w, "settings query failed", 500)
		return
	}
	jsonOut(w, map[string]any{"global": s.Store.GetGlobalSecurityMode(), "nodes": nodes, "effective": func() map[string]string {
		out := map[string]string{}
		for _, d := range s.clusterDeviceNames() {
			out[d] = s.GetEffectiveSecurityMode(d)
		}
		return out
	}()})
}

func (s *Server) ProvisionDeviceCredentialJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.AdminToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(s.AdminToken)) != 1 {
		http.Error(w, "admin authorization required", 403)
		return
	}
	var q struct {
		Device string `json:"device"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&q); err != nil || strings.TrimSpace(q.Device) == "" {
		http.Error(w, "device required", 400)
		return
	}
	if q.Device == s.LocalName || q.Device == "local" {
		http.Error(w, "local control node does not use agent credential", 400)
		return
	}
	if strings.ContainsAny(q.Device, "/\\?&=#%") {
		http.Error(w, "invalid device name", 400)
		return
	}
	configured, err := s.Store.HasDeviceCredential(q.Device)
	if err != nil {
		http.Error(w, "credential lookup failed", 500)
		return
	}
	if configured {
		http.Error(w, "device credential already exists; revoke/rotate it before provisioning again", 409)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "credential generation failed", 500)
		return
	}
	token := hex.EncodeToString(b)
	if err := s.Store.SetDeviceCredential(q.Device, token); err != nil {
		http.Error(w, "credential save failed", 500)
		return
	}
	_ = s.audit(r, q.Device, "device_credential_provision", "device="+q.Device, 0, 0, "credential provisioned")
	jsonOut(w, map[string]any{"ok": true, "device": q.Device, "token": token, "install_command": s.InstallCommand})
}

func (s *Server) UpdateSecuritySettingsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.AdminToken == "" || r.Header.Get("X-Admin-Token") != s.AdminToken {
		http.Error(w, "admin authorization required", http.StatusForbidden)
		return
	}
	var q struct {
		Global string `json:"global"`
		Node   string `json:"node"`
		Mode   string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&q); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	oldGlobal := s.GetEffectiveSecurityMode(s.LocalName)
	oldNode := ""
	if q.Node != "" {
		oldNode = s.GetEffectiveSecurityMode(q.Node)
	}
	if q.Global != "" {
		if err := s.Store.SetGlobalSecurityMode(q.Global); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	if q.Node != "" {
		if err := s.Store.SetNodeSecurityMode(q.Node, q.Mode); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		http.Error(w, "security snapshot publish failed", 500)
		return
	}
	newGlobal := s.GetEffectiveSecurityMode(s.LocalName)
	if q.Global != "" {
		_ = s.audit(r, "", "security_setting", "global="+q.Global, 0, 0, fmt.Sprintf("old=%s new=%s version=%d", oldGlobal, newGlobal, s.Policy.Current().Version))
	}
	if q.Node != "" {
		_ = s.audit(r, q.Node, "security_setting", "mode="+q.Mode, 0, 0, fmt.Sprintf("old=%s new=%s version=%d", oldNode, s.GetEffectiveSecurityMode(q.Node), s.Policy.Current().Version))
	}
	jsonOut(w, map[string]any{"ok": true, "global": s.Store.GetGlobalSecurityMode(), "node": q.Node, "mode": q.Mode, "version": s.Policy.Current().Version})
}

func (s *Server) clusterDeviceNames() []string {
	out := []string{s.LocalName}
	if s.Cluster == nil {
		return out
	}
	for _, d := range s.Cluster.Devices() {
		out = append(out, d["name"].(string))
	}
	return out
}

func (s *Server) Panel(password string) *panelweb.Panel {
	p := panelweb.New(password, s.AdminToken, s)
	go p.Cleanup()
	return p
}

// RecordLegacyAuth records successful legacy-key authentication at most once per
// source IP and transport in each ten-minute window. The request path is stored
// as the target and the User-Agent as the command field for existing audit schema.
func (s *Server) RecordLegacyAuth(r *http.Request, p auth.Principal) {
	if s.Store == nil || (p.ID != "legacy-env" && p.TokenID != "legacy-env" && p.Name != "legacy-env") {
		return
	}
	ip := auth.ClientIP(r)
	transport := "rest"
	switch r.URL.Path {
	case "/mcp/sse", "/mcp/message":
		transport = "mcp-sse"
	case "/mcp":
		transport = "mcp-http"
	}
	key := ip + "\x00" + transport
	now := time.Now()
	s.legacyAuthMu.Lock()
	if s.legacyAuthLast == nil {
		s.legacyAuthLast = make(map[string]time.Time)
	}
	if last := s.legacyAuthLast[key]; !last.IsZero() && now.Sub(last) < 10*time.Minute {
		s.legacyAuthMu.Unlock()
		return
	}
	// Keep the throttle effective across Hub restarts as well as concurrent requests.
	if recent, err := s.Store.HasRecentLegacyAuth(ip, transport, now.Add(-10*time.Minute).Unix()); err == nil && recent {
		s.legacyAuthLast[key] = now
		s.legacyAuthMu.Unlock()
		return
	}
	for k, last := range s.legacyAuthLast {
		if now.Sub(last) >= 10*time.Minute {
			delete(s.legacyAuthLast, k)
		}
	}
	s.legacyAuthLast[key] = now
	s.legacyAuthMu.Unlock()
	_ = s.Store.AuditEntry(storage.AuditEntry{CallerIP: ip, Action: "legacy_auth", Command: r.UserAgent(), ActorType: "api_token", ActorID: "legacy-env", ActorName: "legacy-env", Transport: transport, TargetType: "path", Target: r.URL.Path, Result: "ok"})
}

func (s *Server) DevicesJSON(w http.ResponseWriter, r *http.Request) { s.devices(w, r) }
func (s *Server) PanelExec(w http.ResponseWriter, r *http.Request)   { s.exec(w, r) }

func (s *Server) RotateAPIKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.Auth == nil {
		http.Error(w, "auth manager unavailable", 500)
		return
	}
	jsonOut(w, map[string]string{"api_key": s.Auth.Rotate()})
}

func (s *Server) AuditsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 500)
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	logs, err := s.Store.QueryAudits(r.URL.Query().Get("actor"), r.URL.Query().Get("device"), r.URL.Query().Get("action"), r.URL.Query().Get("result"), r.URL.Query().Get("transport"), from, to, cursor, min(limit, 500), r.URL.Query().Get("oauth_client_id"))
	if err != nil {
		http.Error(w, "audit query failed", 500)
		return
	}
	jsonOut(w, logs)
}

type MCPServiceRequest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Scope       string            `json:"scope"`
	TargetNodes []string          `json:"target_nodes"`
	Enabled     bool              `json:"enabled"`
}
type MCPCallRequest struct {
	Device    string `json:"device"`
	Server    string `json:"server"`
	Tool      string `json:"tool"`
	Arguments any    `json:"arguments"`
}

type MCPTestRequest struct {
	Device  string            `json:"device"`
	Service MCPServiceRequest `json:"service"`
}

func (s *Server) TestMCPJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var q MCPTestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&q); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if q.Device == "" {
		q.Device = s.LocalName
	}
	q.Device = s.normalizeDevice(q.Device)
	if !s.requireDeviceAccess(w, r, q.Device) {
		return
	}
	q.Service.Transport = strings.ToLower(strings.TrimSpace(q.Service.Transport))
	if q.Service.Transport == "" {
		http.Error(w, "transport required", 400)
		return
	}
	if q.Service.Transport == "stdio" {
		if strings.TrimSpace(q.Service.Command) == "" {
			http.Error(w, "stdio command required", 400)
			return
		}
	} else if q.Service.Transport == "http" || q.Service.Transport == "sse" {
		if strings.TrimSpace(q.Service.URL) == "" {
			http.Error(w, "HTTP/SSE URL required", 400)
			return
		}
	} else {
		http.Error(w, "unsupported transport", 400)
		return
	}
	service := cluster.MCPService{ID: q.Service.ID, Name: q.Service.Name, Description: q.Service.Description, Transport: q.Service.Transport, Command: q.Service.Command, Args: q.Service.Args, Env: q.Service.Env, URL: q.Service.URL, Headers: q.Service.Headers}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var tools []map[string]any
	var err error
	if s.isRemote(q.Device) {
		if s.Cluster == nil || !s.Cluster.Online(q.Device) {
			err = fmt.Errorf("device %s is offline", q.Device)
		} else {
			tools, err = s.Cluster.TestMCP(ctx, q.Device, service)
		}
	} else {
		tools, err = cluster.ProbeMCP(ctx, service)
	}
	ms := time.Since(start).Milliseconds()
	if err != nil {
		_ = s.audit(r, q.Device, "mcp_test", service.Name, 1, ms, err.Error())
		jsonOut(w, map[string]any{"success": false, "device": q.Device, "transport": service.Transport, "duration_ms": ms, "tool_count": 0, "tools": []any{}, "error": map[string]any{"code": "MCP_TEST_FAILED", "message": err.Error()}})
		return
	}
	_ = s.audit(r, q.Device, "mcp_test", service.Name, 0, ms, "")
	jsonOut(w, map[string]any{"success": true, "device": q.Device, "transport": service.Transport, "duration_ms": ms, "tool_count": len(tools), "tools": tools, "error": nil})
}

func (s *Server) MCPServicesJSON(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		http.Error(w, "storage unavailable", 500)
		return
	}
	switch r.Method {
	case http.MethodGet:
		v, err := s.Store.ListMCPServices()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonOut(w, v)
	case http.MethodPost:
		var q MCPServiceRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&q); err != nil {
			http.Error(w, "bad json", 400)
			return
		}
		if q.ID == "" || q.Name == "" || q.Transport == "stdio" && q.Command == "" || (q.Transport != "stdio" && q.URL == "") {
			http.Error(w, "invalid MCP service", 400)
			return
		}
		args, _ := json.Marshal(q.Args)
		env, _ := json.Marshal(q.Env)
		headers, _ := json.Marshal(q.Headers)
		nodes, _ := json.Marshal(q.TargetNodes)
		if q.Scope == "" {
			q.Scope = "all"
		}
		if q.Scope != "all" && q.Scope != "custom" {
			http.Error(w, "invalid scope", 400)
			return
		}
		v := storage.MCPService{ID: q.ID, Name: q.Name, Description: q.Description, Transport: q.Transport, Command: q.Command, ArgsJSON: string(args), EnvJSON: string(env), URL: q.URL, HeadersJSON: string(headers), Scope: q.Scope, TargetNodesJSON: string(nodes), Enabled: q.Enabled}
		if err := s.Store.SaveMCPService(v); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if s.Cluster != nil {
			s.Cluster.SyncMCPServices()
		}
		_ = s.audit(r, s.LocalName, "mcp_service_save", q.ID, 0, 0, "")
		jsonOut(w, v)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
func (s *Server) DeleteMCPService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", 400)
		return
	}
	if err := s.Store.DeleteMCPService(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if s.Cluster != nil {
		s.Cluster.SyncMCPServices()
	}
	_ = s.audit(r, s.LocalName, "mcp_service_delete", id, 0, 0, "")
	jsonOut(w, map[string]any{"ok": true})
}
func (s *Server) ListAgentMCPJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	device := r.URL.Query().Get("device")
	if device == "" {
		http.Error(w, "device required", 400)
		return
	}
	device = s.normalizeDevice(device)
	if !s.requireDeviceAccess(w, r, device) {
		return
	}
	if s.isRemote(device) && (s.Cluster == nil || !s.Cluster.Online(device)) {
		http.Error(w, "device offline", 409)
		return
	}
	if s.isRemote(device) {
		v, err := s.Cluster.ListMCP(device)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		jsonOut(w, map[string]any{"device": device, "services": v})
		return
	}
	jsonOut(w, map[string]any{"device": s.LocalName, "services": []any{}})
}
func (s *Server) CallAgentMCPJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var q MCPCallRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&q); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if q.Device == "" {
		http.Error(w, "device required", 400)
		return
	}
	q.Device = s.normalizeDevice(q.Device)
	if !s.requireSecurity(w, r, q.Device, "mcp", q.Server+"/"+q.Tool) {
		return
	}
	if q.Server == "" || q.Tool == "" {
		http.Error(w, "server and tool required", 400)
		return
	}
	if s.isRemote(q.Device) && (s.Cluster == nil || !s.Cluster.Online(q.Device)) {
		if s.Store != nil {
			_ = s.audit(r, q.Device, "mcp_call", q.Server+"/"+q.Tool, 1, 0, "device offline")
		}
		http.Error(w, "device offline", 409)
		return
	}
	if !s.isRemote(q.Device) {
		http.Error(w, "agent MCP requires a remote device", 400)
		return
	}
	start := time.Now()
	v, err := s.Cluster.CallMCP(r.Context(), q.Device, q.Server, q.Tool, q.Arguments)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		_ = s.audit(r, q.Device, "mcp_call", q.Server+"/"+q.Tool, 1, ms, err.Error())
		http.Error(w, err.Error(), 502)
		return
	}
	_ = s.audit(r, q.Device, "mcp_call", q.Server+"/"+q.Tool, 0, ms, "")
	jsonOut(w, v)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.Handle("/metrics", s.requireScope("admin", http.HandlerFunc(s.Metrics)))
	mux.Handle("/api/v1/devices", s.requireScope("devices:read", http.HandlerFunc(s.devices)))
	mux.Handle("/api/v1/exec", s.requireScope("exec", http.HandlerFunc(s.exec)))
	mux.Handle("/api/v1/file/read", s.requireScope("files:read", http.HandlerFunc(s.fileRead)))
	mux.Handle("/api/v1/file/list", s.requireScope("files:read", http.HandlerFunc(s.listDirectory)))
	mux.Handle("/api/v1/file/info", s.requireScope("files:read", http.HandlerFunc(s.fileInfo)))
	mux.Handle("/api/v1/file/search", s.requireScope("files:read", http.HandlerFunc(s.searchFiles)))
	mux.Handle("/api/v1/process/session", s.requireScope("process", http.HandlerFunc(s.processSession)))
	mux.Handle("/api/v1/file/write", s.requireScope("files:write", http.HandlerFunc(s.fileWrite)))
	mux.Handle("/api/v1/devices/file/edit_block", s.requireScope("files:write", http.HandlerFunc(s.editBlock)))
	mux.Handle("/api/v1/mcp/services", s.requireScope("admin", http.HandlerFunc(s.MCPServicesJSON)))
	mux.Handle("/api/v1/mcp/service", s.requireScope("admin", http.HandlerFunc(s.DeleteMCPService)))
	mux.Handle("/api/v1/mcp/list", s.requireScope("mcp:call", http.HandlerFunc(s.ListAgentMCPJSON)))
	mux.Handle("/api/v1/mcp/call", s.requireScope("mcp:call", http.HandlerFunc(s.CallAgentMCPJSON)))
	mux.Handle("/api/v1/mcp/test", s.requireScope("mcp:call", http.HandlerFunc(s.TestMCPJSON)))
	mux.Handle("/api/v1/security/settings", s.requireScope("admin", http.HandlerFunc(s.SecuritySettingsJSON)))
	mux.Handle("/api/v1/security/settings/update", s.requireScope("admin", http.HandlerFunc(s.UpdateSecuritySettingsJSON)))
	mux.Handle("/api/v1/security/device-credentials/provision", s.requireScope("admin", http.HandlerFunc(s.ProvisionDeviceCredentialJSON)))
	mux.Handle("/api/v1/events", s.requireScope("events:emit", http.HandlerFunc(s.EmitNotificationEventJSON)))
	mux.Handle("/api/v1/notifications/deliveries/attempts", s.requireScope("admin", http.HandlerFunc(s.NotificationDeliveryAttemptsV1JSON)))
	mux.Handle("/api/v1/notifications/deliveries/retry", s.requireScope("admin", http.HandlerFunc(s.NotificationDeliveryRetryV1JSON)))
	mux.Handle("/api/v1/notifications/deliveries", s.requireScope("admin", http.HandlerFunc(s.NotificationDeliveriesV1JSON)))
	mux.Handle("/api/v1/notifications/channels/test", s.requireScope("admin", http.HandlerFunc(s.NotificationChannelTestJSON)))
	mux.Handle("/api/v1/notifications/legacy/retire", s.requireScope("admin", http.HandlerFunc(s.RetireLegacyWebhookJSON)))
	mux.Handle("/api/v1/notifications/channels", s.requireScope("admin", http.HandlerFunc(s.NotificationChannelsV1JSON)))
	mux.Handle("/api/v1/notifications/templates", s.requireScope("admin", http.HandlerFunc(s.NotificationTemplatesV1JSON)))
	mux.Handle("/api/v1/notifications/tasks", s.requireScope("admin", http.HandlerFunc(s.NotificationTasksV1JSON)))
	mux.Handle("/api/v1/notifications/webhooks", s.requireScope("admin", http.HandlerFunc(s.WebhookTargetsJSON)))
	mux.Handle("/api/v1/notifications/webhook/update", s.requireScope("admin", http.HandlerFunc(s.UpdateWebhookTargetJSON)))
	mux.Handle("/api/v1/notifications/webhook/delete", s.requireScope("admin", http.HandlerFunc(s.DeleteWebhookTargetJSON)))
	mux.Handle("/api/v1/notifications/webhook/test", s.requireScope("admin", http.HandlerFunc(s.TestWebhookJSON)))
	mux.Handle("/api/v1/audits/export.csv", s.requireScope("admin", http.HandlerFunc(s.AuditExportCSV)))
	mux.Handle("/api/v1/audits", s.requireScope("admin", http.HandlerFunc(s.AuditsJSON)))
	mux.Handle("/api/v1/oauth/clients", s.requireScope("admin", http.HandlerFunc(s.OAuthClientsJSON)))
	mux.Handle("/api/v1/oauth/clients/", s.requireScope("admin", http.HandlerFunc(s.OAuthClientDisableJSON)))
	mux.Handle("/api/v1/oauth/grants", s.requireScope("admin", http.HandlerFunc(s.OAuthGrantsJSON)))
	mux.Handle("/api/v1/oauth/grants/", s.requireScope("admin", http.HandlerFunc(s.OAuthGrantRevokeJSON)))
	mux.Handle("/api/v1/tokens", s.requireScope("admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.TokenCreateJSON(w, r)
			return
		}
		s.TokenListJSON(w, r)
	})))
	mux.Handle("/api/v1/tokens/", s.requireScope("admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			s.TokenPatchJSON(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/rotate") {
			s.TokenRotateJSON(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			s.TokenRevokeJSON(w, r)
			return
		}
		s.TokenDeleteJSON(w, r)
	})))
	mux.HandleFunc("/agent/ws", s.agentWS)
	mux.HandleFunc("/openapi.json", s.openapi)
	return mux
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	jsonOut(w, map[string]string{"status": "ok"})
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	localProf := sysinfo.Collect("CONTROL")
	localCC, localCountry := cluster.ResolveLocalPublicIP()
	localDev := model.Device{
		Name:        s.LocalName,
		Status:      "online",
		Local:       true,
		Arch:        runtime.GOARCH,
		OS:          runtime.GOOS,
		CountryCode: localCC,
		Country:     localCountry,
		Profile:     &localProf,
	}

	var remoteDevs []model.Device
	if s.Cluster != nil {
		for _, d := range s.Cluster.Devices() {
			b, _ := json.Marshal(d)
			var v model.Device
			_ = json.Unmarshal(b, &v)
			remoteDevs = append(remoteDevs, v)
		}
	}

	// 稳定排序：远程节点按名称字典序排列
	sort.Slice(remoteDevs, func(i, j int) bool {
		return remoteDevs[i].Name < remoteDevs[j].Name
	})

	p := principal(r)
	if len(p.Devices) > 0 && !p.AllowsDevice(s.LocalName) && !p.AllowsDevice("local") {
		localDev = model.Device{}
	}
	filtered := make([]model.Device, 0, len(remoteDevs))
	for _, d := range remoteDevs {
		if p.AllowsDevice(d.Name) {
			filtered = append(filtered, d)
		}
	}
	out := filtered
	if localDev.Name != "" {
		out = append([]model.Device{localDev}, out...)
	}
	jsonOut(w, out)
}

func (s *Server) DeleteDevice(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "device name required", 400)
		return
	}
	if name == s.LocalName || name == "local" {
		http.Error(w, "cannot delete local control node", 403)
		return
	}
	if s.Cluster != nil {
		if err := s.Cluster.RevokeDevice(name, "revoked by admin via web console"); err != nil {
			http.Error(w, "revoke failed: "+err.Error(), 500)
			return
		}
	} else if s.Store != nil {
		_ = s.Store.RevokeDevice(name, "revoked by admin via web console")
	}
	_ = s.audit(r, name, "revoke_device", "revoked by admin", 0, 0, "")
	jsonOut(w, map[string]any{"ok": true, "revoked": name})
}

func (s *Server) agentWS(w http.ResponseWriter, r *http.Request) {
	if s.Cluster == nil {
		http.Error(w, "cluster unavailable", 503)
		return
	}
	s.Cluster.ServeWS(w, r)
}

func (s *Server) isRemote(device string) bool {
	return device != "" && device != "local" && device != s.LocalName
}
func (s *Server) beginExec() func() {
	s.inFlightExec.Add(1)
	return func() { s.inFlightExec.Add(-1) }
}

func (s *Server) InFlightExec() int64  { return s.inFlightExec.Load() }
func (s *Server) HTTP429Count() uint64 { return s.http429Count.Load() }
func (s *Server) RecordHTTPStatus(status int) {
	if status == http.StatusTooManyRequests {
		s.http429Count.Add(1)
	}
}

func (s *Server) acquireExec() func() {
	if s.ExecLimiter == nil {
		return func() {}
	}
	s.ExecLimiter <- struct{}{}
	return func() { <-s.ExecLimiter }
}
func (s *Server) acquireSearch() func() {
	if s.SearchLimiter == nil {
		return func() {}
	}
	s.SearchLimiter <- struct{}{}
	return func() { <-s.SearchLimiter }
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req model.ExecRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		http.Error(w, "command required", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	req.Device = s.normalizeDevice(req.Device)
	if !s.requireSecurity(w, r, req.Device, "exec", req.Command) {
		return
	}
	if releaseTokenExec, allowed := s.acquireTokenExec(principal(r)); !allowed {
		s.rejectTokenExecLimit(w, r, principal(r))
		return
	} else {
		defer releaseTokenExec()
	}
	releaseExec := s.acquireExec()
	defer releaseExec()
	finishExec := s.beginExec()
	defer finishExec()
	if req.Timeout <= 0 {
		req.Timeout = 30
	}
	if req.Timeout > 300 {
		req.Timeout = 300
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()
	var res executor.Result
	if s.isRemote(req.Device) {
		if s.Cluster == nil || !s.Cluster.Online(req.Device) {
			http.Error(w, "device offline", 409)
			return
		}
		rr, err := s.Cluster.Exec(ctx, req.Device, cluster.ExecPayload{Command: req.Command, Workdir: req.Workdir, Timeout: req.Timeout})
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		res = executor.Result{ExitCode: rr.ExitCode, Stdout: rr.Stdout, Stderr: rr.Stderr, DurationMS: rr.DurationMS}
	} else {
		res = s.Exec.Run(ctx, req.Command, req.Workdir)
	}
	if err := s.audit(r.WithContext(withCommandExit(r.Context())), req.Device, "exec", req.Command, res.ExitCode, res.DurationMS, execAuditMessage(res)); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	jsonOut(w, model.ExecResponse{Device: req.Device, ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr, DurationMS: res.DurationMS})
}
func sessionOwner(r *http.Request) string {
	v := r.Header.Get("Authorization") + "|" + r.Header.Get("X-Operator-ID") + "|" + auth.ClientIP(r)
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}
func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) processSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Device       string `json:"device"`
		SessionID    string `json:"session_id"`
		Command      string `json:"command"`
		Workdir      string `json:"workdir"`
		Data         string `json:"data"`
		Force        bool   `json:"force"`
		RingCapacity int    `json:"ring_capacity"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	if !s.requireSecurity(w, r, req.Device, "exec", req.SessionID) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	action := r.URL.Query().Get("action")
	owner := sessionOwner(r)
	var result any
	var err error
	if s.isRemote(req.Device) {
		if s.Cluster == nil || !s.Cluster.Online(req.Device) {
			http.Error(w, "device offline", 409)
			return
		}
		switch action {
		case "start":
			if req.SessionID == "" {
				req.SessionID, err = newSessionID()
				if err != nil {
					http.Error(w, "session id generation failed", 500)
					return
				}
			}
			result, err = s.Cluster.StartProcess(ctx, req.Device, cluster.StartProcessPayload{Command: req.Command, Workdir: req.Workdir, SessionID: req.SessionID, Owner: owner, RingCapacity: req.RingCapacity})
		case "read":
			result, err = s.Cluster.ReadProcessOutput(ctx, req.Device, req.SessionID, owner)
		case "input":
			err = s.Cluster.InteractProcess(ctx, req.Device, req.SessionID, req.Data, owner)
			result = map[string]any{"ok": true}
		case "stop":
			err = s.Cluster.ForceTerminate(ctx, req.Device, req.SessionID, owner)
			result = map[string]any{"ok": true}
		case "list":
			result, err = s.Cluster.ListSessions(ctx, req.Device)
		default:
			http.Error(w, "unknown action", 400)
			return
		}
	} else {
		if s.LocalSessions == nil {
			http.Error(w, "local session manager unavailable", 503)
			return
		}
		switch action {
		case "start":
			if req.SessionID == "" {
				req.SessionID, err = newSessionID()
				if err != nil {
					http.Error(w, "session id generation failed", 500)
					return
				}
			}
			var sess *executor.Session
			sess, err = s.LocalSessions.Start(context.Background(), req.SessionID, req.Command, req.Workdir, owner, req.RingCapacity)
			if err == nil {
				result = map[string]any{"session_id": sess.ID, "started_at": sess.StartedAt.Unix()}
			}
		case "read":
			var sess *executor.Session
			sess, ok := s.LocalSessions.Get(req.SessionID)
			if !ok {
				err = fmt.Errorf("session not found")
				break
			}
			if !sess.Authorized(owner) {
				err = fmt.Errorf("session ownership denied")
				break
			}
			code, done, _ := sess.Status()
			stdout, stderr := sess.Output()
			result = cluster.ProcessOutputPayload{SessionID: req.SessionID, Stdout: stdout, Stderr: stderr, ExitCode: code, Running: !done}
		case "input":
			var sess *executor.Session
			sess, ok := s.LocalSessions.Get(req.SessionID)
			if !ok {
				err = fmt.Errorf("session not found")
				break
			}
			if !sess.Authorized(owner) {
				err = fmt.Errorf("session ownership denied")
				break
			}
			err = sess.Stdin([]byte(req.Data))
			result = map[string]any{"ok": true}
		case "stop":
			var sess *executor.Session
			sess, ok := s.LocalSessions.Get(req.SessionID)
			if !ok {
				err = fmt.Errorf("session not found")
				break
			}
			if !sess.Authorized(owner) {
				err = fmt.Errorf("session ownership denied")
				break
			}
			err = s.LocalSessions.Stop(req.SessionID, true)
			result = map[string]any{"ok": true}
		case "list":
			result = s.LocalSessions.List()
		default:
			http.Error(w, "unknown action", 400)
			return
		}
	}
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	jsonOut(w, result)
}

func (s *Server) listDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		_ = s.audit(r, "", "file_list", "", 405, 0, "method not allowed")
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Device, Path string
		Recursive    bool `json:"recursive"`
		MaxEntries   int  `json:"max_entries"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		_ = s.audit(r, "", "file_list", "", 400, 0, "bad json")
		http.Error(w, "bad json", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	if !s.requireSecurity(w, r, req.Device, "read", req.Path) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var v []cluster.Entry
	var err error
	if s.isRemote(req.Device) {
		v, err = s.Cluster.ListDirectory(ctx, req.Device, cluster.ListDirectoryPayload{Path: req.Path, Recursive: req.Recursive, MaxEntries: req.MaxEntries})
	} else {
		var ev []executor.Entry
		ev, err = executor.ListDirectory(req.Path, req.Recursive, req.MaxEntries)
		if err == nil {
			b, _ := json.Marshal(ev)
			err = json.Unmarshal(b, &v)
		}
	}
	if err != nil {
		_ = s.audit(r, req.Device, "file_list", req.Path, 409, 0, err.Error())
		http.Error(w, err.Error(), 409)
		return
	}
	_ = s.audit(r, req.Device, "file_list", req.Path, 0, 0, "")
	jsonOut(w, v)
}
func (s *Server) fileInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		_ = s.audit(r, "", "file_info", "", 405, 0, "method not allowed")
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct{ Device, Path string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		_ = s.audit(r, "", "file_info", "", 400, 0, "bad json")
		http.Error(w, "bad json", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	if !s.requireSecurity(w, r, req.Device, "read", req.Path) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var v cluster.FileInfo
	var err error
	if s.isRemote(req.Device) {
		v, err = s.Cluster.GetFileInfo(ctx, req.Device, cluster.FileInfoPayload{Path: req.Path})
	} else {
		var ev executor.FileInfo
		ev, err = executor.GetFileInfo(req.Path)
		if err == nil {
			b, _ := json.Marshal(ev)
			err = json.Unmarshal(b, &v)
		}
	}
	if err != nil {
		_ = s.audit(r, req.Device, "file_info", req.Path, 404, 0, err.Error())
		http.Error(w, err.Error(), 404)
		return
	}
	_ = s.audit(r, req.Device, "file_info", req.Path, 0, 0, "")
	jsonOut(w, v)
}
func (s *Server) searchFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		_ = s.audit(r, "", "file_search", "", 405, 0, "method not allowed")
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Device, Root, Query string
		MaxResults          int `json:"max_results"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		_ = s.audit(r, "", "file_search", "", 400, 0, "bad json")
		http.Error(w, "bad json", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	if !s.requireSecurity(w, r, req.Device, "read", req.Root) {
		return
	}
	releaseSearch := s.acquireSearch()
	defer releaseSearch()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var v []cluster.SearchResult
	var err error
	if s.isRemote(req.Device) {
		v, err = s.Cluster.Search(ctx, req.Device, cluster.SearchPayload{Root: req.Root, Query: req.Query, MaxResults: req.MaxResults})
	} else {
		var ev []executor.SearchResult
		ev, err = executor.Search(ctx, req.Root, req.Query, req.MaxResults)
		if err == nil {
			b, _ := json.Marshal(ev)
			err = json.Unmarshal(b, &v)
		}
	}
	if err != nil {
		_ = s.audit(r, req.Device, "file_search", req.Root, 409, 0, err.Error())
		http.Error(w, err.Error(), 409)
		return
	}
	_ = s.audit(r, req.Device, "file_search", req.Root, 0, 0, "")
	jsonOut(w, v)
}

func (s *Server) fileRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req model.FileReadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	if !s.requireSecurity(w, r, req.Device, "read", req.Path) {
		return
	}
	if req.Path == "" || !validPath(req.Path) || req.Offset < 0 {
		http.Error(w, "invalid path or offset", 400)
		return
	}
	if req.Limit <= 0 {
		req.Limit = defaultFileLimit
	}
	if req.Limit > maxFileLimit {
		req.Limit = maxFileLimit
	}
	var content string
	var n int64
	var eof bool
	var totalSize int64
	var hasMore bool
	if s.isRemote(req.Device) {
		if s.Cluster == nil || !s.Cluster.Online(req.Device) {
			http.Error(w, "device offline", 404)
			return
		}
		fr, err := s.Cluster.ReadFile(r.Context(), req.Device, cluster.FileReadPayload{Path: req.Path, Offset: req.Offset, Limit: req.Limit})
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		content, n, totalSize, hasMore, eof = fr.Content, fr.Bytes, fr.TotalSize, fr.HasMore, fr.EOF
	} else {
		f, err := os.Open(req.Path)
		if err != nil {
			http.Error(w, "open failed: "+err.Error(), 404)
			return
		}
		defer f.Close()
		if _, err = f.Seek(req.Offset, io.SeekStart); err != nil {
			http.Error(w, "seek failed: "+err.Error(), 400)
			return
		}
		info, err := f.Stat()
		if err != nil {
			http.Error(w, "stat failed: "+err.Error(), 500)
			return
		}
		totalSize = info.Size()
		buf := make([]byte, req.Limit)
		rn, re := io.ReadFull(f, buf)
		eof = re == io.EOF || re == io.ErrUnexpectedEOF
		if re != nil && !eof {
			http.Error(w, "read failed: "+re.Error(), 500)
			return
		}
		content, n = string(buf[:rn]), int64(rn)
		hasMore = req.Offset+n < totalSize
	}
	if err := s.audit(r, req.Device, "file_read", req.Path, 0, 0, ""); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	jsonOut(w, model.FileReadResponse{Device: req.Device, Path: req.Path, Offset: req.Offset, Content: content, Bytes: n, TotalSize: totalSize, HasMore: hasMore, EOF: eof})
}
func (s *Server) editBlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Device  string `json:"device,omitempty"`
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFileLimit+64*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json or content too large", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	if !s.requireSecurity(w, r, req.Device, "write", req.Path) {
		return
	}
	if req.Path == "" || !validPath(req.Path) || req.OldText == "" {
		http.Error(w, "path and old_text required", 400)
		return
	}
	if len(req.OldText) > int(maxFileLimit) || len(req.NewText) > int(maxFileLimit) {
		http.Error(w, "edit block too large", 413)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var result executor.EditBlockResult
	if s.isRemote(req.Device) {
		if s.Cluster == nil || !s.Cluster.Online(req.Device) {
			http.Error(w, "device offline", 409)
			return
		}
		rr, err := s.Cluster.EditBlock(ctx, req.Device, cluster.EditBlockPayload{Path: req.Path, OldText: req.OldText, NewText: req.NewText})
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		result.BytesWritten = rr.BytesWritten
	} else {
		var err error
		result, err = executor.EditBlock(req.Path, req.OldText, req.NewText)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
	}
	if err := s.audit(r, req.Device, "edit_block", req.Path+fmt.Sprintf(" (bytes=%d)", len(req.NewText)), 0, 0, fmt.Sprintf("bytes_written=%d", result.BytesWritten)); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	jsonOut(w, map[string]any{"device": req.Device, "path": req.Path, "bytes_written": result.BytesWritten})
}

func (s *Server) fileWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req model.FileWriteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFileLimit+16*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json or content too large", 400)
		return
	}
	if req.Device == "" {
		req.Device = s.LocalName
	}
	if !s.requireSecurity(w, r, req.Device, "write", req.Path) {
		return
	}
	if req.Path == "" || !validPath(req.Path) {
		http.Error(w, "path required", 400)
		return
	}
	if int64(len(req.Content)) > maxFileLimit {
		http.Error(w, "content too large", 413)
		return
	}
	var n int64
	if s.isRemote(req.Device) {
		if s.Cluster == nil || !s.Cluster.Online(req.Device) {
			http.Error(w, "device offline", 404)
			return
		}
		var err error
		n, err = s.Cluster.WriteFile(r.Context(), req.Device, cluster.FileWritePayload{Path: req.Path, Content: req.Content})
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	} else {
		if err := os.WriteFile(req.Path, []byte(req.Content), 0644); err != nil {
			http.Error(w, "write failed: "+err.Error(), 500)
			return
		}
		n = int64(len(req.Content))
	}
	if err := s.audit(r, req.Device, "file_write", req.Path+fmt.Sprintf(" (bytes=%d)", len(req.Content)), 0, 0, ""); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	jsonOut(w, model.FileWriteResponse{Device: req.Device, Path: req.Path, Bytes: n})
}

func validPath(path string) bool {
	return !strings.ContainsRune(path, 0) && !strings.HasPrefix(path, "-")
}

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	data, err := openAPIFS.ReadFile("openapi.json")
	if err != nil {
		http.Error(w, "openapi unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
