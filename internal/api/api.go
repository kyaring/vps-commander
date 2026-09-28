package api

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/model"
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
	Auth      *auth.Manager
	Exec      executor.Local
	Cluster   *cluster.Manager
	Store     *storage.Store
	LocalName string
}

func (s *Server) Panel(password string) http.Handler {
	p := panelweb.New(password, s)
	go p.Cleanup()
	return p.Handler()
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
		limit = v
	}
	logs, err := s.Store.RecentAudits(limit)
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
		_ = s.Store.Audit(clientIP(r), "", "mcp_service_save", q.ID, 0, 0, "")
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
	_ = s.Store.Audit(clientIP(r), "", "mcp_service_delete", id, 0, 0, "")
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
	if q.Device == "" || q.Server == "" || q.Tool == "" {
		http.Error(w, "device, server and tool required", 400)
		return
	}
	if s.isRemote(q.Device) && (s.Cluster == nil || !s.Cluster.Online(q.Device)) {
		if s.Store != nil {
			_ = s.Store.Audit(clientIP(r), q.Device, "mcp_call", q.Server+"/"+q.Tool, 1, 0, "device offline")
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
		_ = s.Store.Audit(clientIP(r), q.Device, "mcp_call", q.Server+"/"+q.Tool, 1, ms, err.Error())
		http.Error(w, err.Error(), 502)
		return
	}
	_ = s.Store.Audit(clientIP(r), q.Device, "mcp_call", q.Server+"/"+q.Tool, 0, ms, "")
	jsonOut(w, v)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/v1/devices", s.devices)
	mux.HandleFunc("/api/v1/exec", s.exec)
	mux.HandleFunc("/api/v1/file/read", s.fileRead)
	mux.HandleFunc("/api/v1/file/write", s.fileWrite)
	mux.HandleFunc("/api/v1/mcp/services", s.MCPServicesJSON)
	mux.HandleFunc("/api/v1/mcp/service", s.DeleteMCPService)
	mux.HandleFunc("/api/v1/mcp/list", s.ListAgentMCPJSON)
	mux.HandleFunc("/api/v1/mcp/call", s.CallAgentMCPJSON)
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
	out := []model.Device{{Name: s.LocalName, Status: "online", Local: true, Arch: runtime.GOARCH, OS: runtime.GOOS, Profile: &localProf}}
	if s.Cluster != nil {
		for _, d := range s.Cluster.Devices() {
			b, _ := json.Marshal(d)
			var v model.Device
			_ = json.Unmarshal(b, &v)
			out = append(out, v)
		}
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
	_ = s.Store.Audit(clientIP(r), name, "revoke_device", "revoked by admin", 0, 0, "")
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
	if err := s.Store.Audit(clientIP(r), req.Device, "exec", req.Command, res.ExitCode, res.DurationMS, res.Stderr); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	jsonOut(w, model.ExecResponse{Device: req.Device, ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr, DurationMS: res.DurationMS})
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
	if err := s.Store.Audit(clientIP(r), req.Device, "file_read", req.Path, 0, 0, ""); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	jsonOut(w, model.FileReadResponse{Device: req.Device, Path: req.Path, Offset: req.Offset, Content: content, Bytes: n, TotalSize: totalSize, HasMore: hasMore, EOF: eof})
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
	if err := s.Store.Audit(clientIP(r), req.Device, "file_write", req.Path, 0, 0, ""); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	jsonOut(w, model.FileWriteResponse{Device: req.Device, Path: req.Path, Bytes: n})
}

func validPath(path string) bool {
	return !strings.ContainsRune(path, 0) && !strings.HasPrefix(path, "-")
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
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
