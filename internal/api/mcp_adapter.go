package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/model"
)

// MCPBackend implements mcp.ActionBackend
type MCPBackend struct {
	server *Server
}

func (s *Server) NewMCPBackend() *MCPBackend {
	return &MCPBackend{server: s}
}

func (b *MCPBackend) authorize(ctx context.Context, device, action, clientIP string) error {
	device = b.server.normalizeDevice(device)
	mode := b.server.GetEffectiveSecurityMode(device)
	if !securityAllows(mode, action) {
		if b.server.Store != nil {
			_ = b.server.auditMCPContext(ctx, clientIP, device, "security_denied", action, 0, 0, "MCP security gate")
		}
		return fmt.Errorf("security mode %s denies %s", mode, action)
	}
	return nil
}

func (b *MCPBackend) ListDevices(ctx context.Context) (any, error) {
	out := []model.Device{{Name: b.server.LocalName, Status: "online", Local: true, Arch: runtime.GOARCH, OS: runtime.GOOS}}
	if b.server.Cluster != nil {
		for _, d := range b.server.Cluster.Devices() {
			data, _ := json.Marshal(d)
			var dev model.Device
			_ = json.Unmarshal(data, &dev)
			out = append(out, dev)
		}
	}
	return out, nil
}

func (b *MCPBackend) ExecCommand(ctx context.Context, clientIP, device, command, workdir string, timeout int) (any, error) {
	return b.ExecCommandWithOptions(ctx, clientIP, device, command, workdir, timeout, "raw", 0, 0)
}

func (b *MCPBackend) ExecCommandWithOptions(ctx context.Context, clientIP, device, command, workdir string, timeout int, outputMode string, maxBytes, dedupWindowSeconds int) (any, error) {
	if command == "" {
		return nil, errors.New("command required")
	}
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "exec", clientIP); err != nil {
		return nil, err
	}
	opts, err := validateExecOutputOptions(outputMode, maxBytes, dedupWindowSeconds)
	if err != nil {
		return nil, err
	}
	if cached, ok := b.server.outputDedupLookup(device, workdir, command, opts); ok {
		code, _ := cached["exit_code"].(int)
		if b.server.Store != nil {
			_ = b.server.auditMCPContext(ctx, clientIP, device, "exec", command, code, 0, "unchanged=true")
		}
		return cached, nil
	}
	p, _ := auth.PrincipalFromContext(ctx)
	releaseTokenExec, allowed := b.server.acquireTokenExec(p)
	if !allowed {
		transport := mcp.TransportFromContext(ctx)
		if transport == "" {
			transport = "mcp"
		}
		b.server.auditRateLimitedEntry(clientIP, device, "exec_command: "+command, transport, "", p)
		mcp.MarkAuditRecorded(ctx)
		return nil, fmt.Errorf("%w: token concurrent exec limit reached", mcp.ErrRateLimited)
	}
	defer releaseTokenExec()
	releaseGlobalExec := b.server.acquireExec()
	defer releaseGlobalExec()
	finishExec := b.server.beginExec()
	defer finishExec()
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var res executor.Result
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		rr, err := b.server.Cluster.Exec(execCtx, device, cluster.ExecPayload{Command: command, Workdir: workdir, Timeout: timeout})
		if err != nil {
			return nil, err
		}
		res = executor.Result{ExitCode: rr.ExitCode, Stdout: rr.Stdout, Stderr: rr.Stderr, DurationMS: rr.DurationMS}
	} else {
		res = b.server.Exec.Run(execCtx, command, workdir)
	}

	if b.server.Store != nil {
		_ = b.server.auditMCPContext(withCommandExit(ctx), clientIP, device, "exec", command, res.ExitCode, res.DurationMS, execAuditMessage(res))
	}

	shaped, err := execOutputResponse(device, res.ExitCode, res.Stdout, res.Stderr, res.DurationMS, opts)
	if err != nil {
		return nil, err
	}
	b.server.outputDedupRemember(device, workdir, command, opts, execOutputHash(res.Stdout, res.Stderr), execOutputCachedBytes(res.Stdout, res.Stderr), res.ExitCode)
	return shaped, nil
}

func (b *MCPBackend) DiagnosticCommand(ctx context.Context, clientIP, device string, req executor.DiagnosticRequest) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "diagnostic", clientIP); err != nil {
		return nil, err
	}
	if req.Action == "" {
		return nil, errors.New("diagnostic action required")
	}
	var result any
	var exitCode int
	var durationMS int64
	var detail string
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		res, err := b.server.Cluster.Diagnostic(ctx, device, req)
		if err != nil {
			return nil, err
		}
		result, exitCode, durationMS, detail = res, res.ExitCode, res.DurationMS, res.Stderr
	} else {
		res, err := executor.Diagnostic(ctx, req)
		if err != nil {
			return nil, err
		}
		result, exitCode, durationMS, detail = res, res.ExitCode, res.DurationMS, res.Stderr
	}
	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "diagnostic_command", "action="+req.Action+" target="+req.Target, exitCode, durationMS, detail)
	}
	return result, nil
}

func (b *MCPBackend) ReadFile(ctx context.Context, clientIP, device, path string, offset, limit int64) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "read", clientIP); err != nil {
		return nil, err
	}
	if path == "" || !validPath(path) || offset < 0 {
		return nil, errors.New("invalid path or offset")
	}
	if limit <= 0 {
		limit = defaultFileLimit
	}
	if limit > maxFileLimit {
		limit = maxFileLimit
	}

	var content string
	var n int64
	var eof bool
	var totalSize int64
	var hasMore bool

	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		fr, err := b.server.Cluster.ReadFile(ctx, device, cluster.FileReadPayload{Path: path, Offset: offset, Limit: limit})
		if err != nil {
			return nil, err
		}
		content, n, totalSize, hasMore, eof = fr.Content, fr.Bytes, fr.TotalSize, fr.HasMore, fr.EOF
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open failed: %w", err)
		}
		defer f.Close()
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek failed: %w", err)
		}
		info, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("stat failed: %w", err)
		}
		totalSize = info.Size()
		buf := make([]byte, limit)
		rn, re := io.ReadFull(f, buf)
		eof = re == io.EOF || re == io.ErrUnexpectedEOF
		if re != nil && !eof {
			return nil, fmt.Errorf("read failed: %w", re)
		}
		content, n = string(buf[:rn]), int64(rn)
		hasMore = offset+n < totalSize
	}

	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "file_read", path, 0, 0, "")
	}

	return model.FileReadResponse{
		Device:    device,
		Path:      path,
		Offset:    offset,
		Content:   content,
		Bytes:     n,
		TotalSize: totalSize,
		HasMore:   hasMore,
		EOF:       eof,
	}, nil
}

func (b *MCPBackend) EditBlock(ctx context.Context, clientIP, device, path, oldText, newText string) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "write", clientIP); err != nil {
		return nil, err
	}
	if path == "" || !validPath(path) || oldText == "" {
		return nil, errors.New("path and old_text required")
	}
	if len(oldText) > int(maxFileLimit) || len(newText) > int(maxFileLimit) {
		return nil, errors.New("edit block too large")
	}
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		res, err := b.server.Cluster.EditBlock(ctx, device, cluster.EditBlockPayload{Path: path, OldText: oldText, NewText: newText})
		if err != nil {
			return nil, err
		}
		_ = b.server.auditMCPContext(ctx, clientIP, device, "edit_block", path+fmt.Sprintf(" (bytes=%d)", len(newText)), 0, 0, fmt.Sprintf("bytes_written=%d", res.BytesWritten))
		return map[string]any{"device": device, "path": path, "bytes_written": res.BytesWritten}, nil
	}
	res, err := executor.EditBlock(path, oldText, newText)
	if err != nil {
		return nil, err
	}
	_ = b.server.auditMCPContext(ctx, clientIP, device, "edit_block", path+fmt.Sprintf(" (bytes=%d)", len(newText)), 0, 0, fmt.Sprintf("bytes_written=%d", res.BytesWritten))
	return map[string]any{"device": device, "path": path, "bytes_written": res.BytesWritten}, nil
}

func (b *MCPBackend) WriteFile(ctx context.Context, clientIP, device, path, content string) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "write", clientIP); err != nil {
		return nil, err
	}
	if path == "" || !validPath(path) {
		return nil, errors.New("path required")
	}
	if int64(len(content)) > maxFileLimit {
		return nil, errors.New("content too large")
	}

	var n int64
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		var err error
		n, err = b.server.Cluster.WriteFile(ctx, device, cluster.FileWritePayload{Path: path, Content: content})
		if err != nil {
			return nil, err
		}
	} else {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write failed: %w", err)
		}
		n = int64(len(content))
	}

	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "file_write", path+fmt.Sprintf(" (bytes=%d)", len(content)), 0, 0, "")
	}

	return model.FileWriteResponse{
		Device: device,
		Path:   path,
		Bytes:  n,
	}, nil
}

func (b *MCPBackend) ListAgentMCP(ctx context.Context, device string) (any, error) {
	if device == "" {
		return nil, errors.New("device required")
	}
	if b.server.isRemote(device) && (b.server.Cluster == nil || !b.server.Cluster.Online(device)) {
		return nil, fmt.Errorf("device %s is offline", device)
	}
	if !b.server.isRemote(device) {
		return map[string]any{"device": b.server.LocalName, "services": []any{}}, nil
	}
	return b.server.Cluster.ListMCP(device)
}
func (b *MCPBackend) CallAgentMCP(ctx context.Context, clientIP, device, server, tool string, args any) (any, error) {
	if device == "" || server == "" || tool == "" {
		return nil, errors.New("device, server and tool required")
	}
	if b.server.isRemote(device) && (b.server.Cluster == nil || !b.server.Cluster.Online(device)) {
		return nil, fmt.Errorf("device %s is offline", device)
	}
	if !b.server.isRemote(device) {
		return nil, errors.New("agent MCP requires a remote device")
	}
	start := time.Now()
	v, err := b.server.Cluster.CallMCP(ctx, device, server, tool, args)
	ms := time.Since(start).Milliseconds()
	if b.server.Store != nil {
		if err != nil {
			_ = b.server.auditMCPContext(ctx, clientIP, device, "mcp_call", server+"/"+tool, 1, ms, err.Error())
		} else {
			_ = b.server.auditMCPContext(ctx, clientIP, device, "mcp_call", server+"/"+tool, 0, ms, "")
		}
	}
	return v, err
}

// AuditMCP forwards MCP authorization denials to the Hub audit store.
func (b *MCPBackend) AuditMCP(ctx context.Context, clientIP, device, tool, command, result, detail string) {
	b.server.AuditMCP(ctx, clientIP, device, tool, command, result, detail)
}

// AuditMCPAction records successful MCP tools that do not otherwise call an action backend.
func (b *MCPBackend) AuditMCPAction(ctx context.Context, clientIP, device, action, command string, code int, durationMS int64, detail string) {
	if b.server.Store == nil {
		return
	}
	_ = b.server.auditMCPContext(ctx, clientIP, device, action, command, code, durationMS, detail)
}

// AuthorizeMCP exposes the same Hub-side gate to the MCP transport layer.
func (b *MCPBackend) AuthorizeMCP(ctx context.Context, device, action, clientIP string) error {
	return b.authorize(ctx, device, action, clientIP)
}

func (b *MCPBackend) ReadMultipleFiles(ctx context.Context, clientIP, device string, paths []string, limit int64) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "read", clientIP); err != nil {
		return nil, err
	}
	if len(paths) > 32 {
		return nil, errors.New("too many paths")
	}
	var result any
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		v, err := b.server.Cluster.ReadMultipleFiles(ctx, device, cluster.ReadMultipleFilesPayload{Paths: paths, Limit: limit})
		if err != nil {
			return nil, err
		}
		result = v
	} else {
		result = executor.ReadMultipleFiles(paths, limit)
	}
	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "file_read", strings.Join(paths, ", "), 0, 0, "")
	}
	return result, nil
}
func (b *MCPBackend) CreateDirectory(ctx context.Context, clientIP, device, path string) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "write", clientIP); err != nil {
		return nil, err
	}
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		if err := b.server.Cluster.CreateDirectory(ctx, device, cluster.CreateDirectoryPayload{Path: path}); err != nil {
			return nil, err
		}
	} else {
		if err := executor.CreateDirectory(path); err != nil {
			return nil, err
		}
	}
	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "create_directory", path, 0, 0, "")
	}
	return map[string]any{"device": device, "path": path, "ok": true}, nil
}
func (b *MCPBackend) MoveFile(ctx context.Context, clientIP, device, source, destination string) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "write", clientIP); err != nil {
		return nil, err
	}
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		if err := b.server.Cluster.MoveFile(ctx, device, cluster.MoveFilePayload{Source: source, Destination: destination}); err != nil {
			return nil, err
		}
	} else {
		if err := executor.MoveFile(source, destination); err != nil {
			return nil, err
		}
	}
	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "move_file", source+" -> "+destination, 0, 0, "")
	}
	return map[string]any{"device": device, "source": source, "destination": destination, "ok": true}, nil
}
func (b *MCPBackend) ListProcesses(ctx context.Context, clientIP, device string) (any, error) {
	device = b.server.normalizeDevice(device)
	if device == "" {
		device = b.server.LocalName
	}
	if err := b.authorize(ctx, device, "read", clientIP); err != nil {
		return nil, err
	}
	var result any
	var err error
	if b.server.isRemote(device) {
		if b.server.Cluster == nil || !b.server.Cluster.Online(device) {
			return nil, fmt.Errorf("device %s is offline", device)
		}
		result, err = b.server.Cluster.ListProcesses(ctx, device)
	} else {
		result, err = executor.ListProcesses()
	}
	if err == nil && b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, device, "list_processes", "list_processes", 0, 0, "")
	}
	return result, err
}
