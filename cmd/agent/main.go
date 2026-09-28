package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/sysinfo"
)

func main() {
	hub := flag.String("hub", "", "Hub WebSocket URL")
	name := flag.String("name", "", "device name")
	token := flag.String("token", os.Getenv("VPS_COMMANDER_CLUSTER_SECRET"), "cluster secret")
	insecure := flag.Bool("insecure", false, "skip TLS verification")
	flag.Parse()
	if *hub == "" || *name == "" || *token == "" {
		log.Fatal("-hub, -name and -token are required")
	}
	backoff := time.Second
	for {
		if err := run(*hub, *name, *token, *insecure); err != nil {
			log.Printf("agent connection: %v", err)
		}
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func run(hub, name, token string, insecure bool) error {
	u := hub + "?device_name=" + name
	dialer := websocket.DefaultDialer
	if insecure {
		dialer = &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	c, _, err := dialer.Dial(u, header)
	if err != nil {
		return err
	}
	defer c.Close()
	var writeMu sync.Mutex
	writeJSON := func(msg cluster.Message) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := c.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		return c.WriteJSON(msg)
	}
	if err := c.SetReadDeadline(time.Now().Add(75 * time.Second)); err != nil {
		return err
	}
	exec := executor.Local{MaxOutput: 1024 * 1024}
	mcpRuntime := cluster.NewMCPRuntime()

	// 初始握手附带画像
	initProf := sysinfo.Collect("APPLICATION")
	if err := writeJSON(cluster.Message{Event: "hello", Device: name, Arch: runtime.GOARCH, OS: runtime.GOOS, Profile: &initProf, TS: time.Now().Unix()}); err != nil {
		return err
	}

	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				prof := sysinfo.Collect("APPLICATION")
				_ = writeJSON(cluster.Message{Event: "ping", Device: name, Arch: runtime.GOARCH, OS: runtime.GOOS, Profile: &prof, TS: time.Now().Unix()})
			case <-stopPing:
				return
			}
		}
	}()

	for {
		var msg cluster.Message
		if err := c.ReadJSON(&msg); err != nil {
			return err
		}
		_ = c.SetReadDeadline(time.Now().Add(75 * time.Second))
		if msg.Event == "registered" {
			continue
		}
		if msg.Event == "mcp_sync" {
			var services []cluster.MCPService
			if err := decodePayload(msg.Payload, &services); err != nil {
				log.Printf("mcp sync failed: %v", err)
			} else {
				mcpRuntime.Sync(services)
				log.Printf("MCP services synced: %d", len(services))
			}
			continue
		}
		if msg.Event == "revoked" {
			log.Printf("device revoked by admin: %s", msg.Error)
			return errors.New("revoked by admin")
		}
		if msg.Event == "ping" {
			prof := sysinfo.Collect("APPLICATION")
			if err := writeJSON(cluster.Message{Event: "pong", Device: name, Arch: runtime.GOARCH, OS: runtime.GOOS, Profile: &prof, TS: msg.TS}); err != nil {
				return err
			}
			continue
		}
		if msg.Action == "" || msg.ID == "" {
			continue
		}
		go func(msg cluster.Message) {
			if err := handle(writeJSON, exec, mcpRuntime, msg); err != nil {
				log.Printf("request %s failed: %v", msg.ID, err)
				_ = c.Close()
			}
		}(msg)
	}
}

func handle(writeJSON func(cluster.Message) error, exec executor.Local, mcpRuntime *cluster.MCPRuntime, msg cluster.Message) error {
	switch msg.Action {
	case "mcp_call":
		var p struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
			Args   any    `json:"args"`
		}
		if err := decodePayload(msg.Payload, &p); err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		if p.Server == "" || p.Tool == "" {
			return sendError(writeJSON, msg.ID, errors.New("server and tool required"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()
		start := time.Now()
		result, err := mcpRuntime.Call(ctx, p.Server, p.Tool, p.Args)
		duration := time.Since(start).Milliseconds()
		if err != nil {
			_ = writeJSON(cluster.Message{ID: msg.ID, Status: "error", Error: err.Error(), Payload: map[string]any{"server": p.Server, "tool": p.Tool, "duration_ms": duration}})
			return nil
		}
		return writeJSON(cluster.Message{ID: msg.ID, Status: "success", Payload: map[string]any{"server": p.Server, "tool": p.Tool, "result": result, "duration_ms": duration}})
	case "exec":
		var p cluster.ExecPayload
		if err := decodePayload(msg.Payload, &p); err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = 30
		}
		if timeout > 300 {
			timeout = 300
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		res := exec.Run(ctx, p.Command, p.Workdir)
		cancel()
		return writeJSON(cluster.Message{ID: msg.ID, Status: "success", ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr, DurationMS: res.DurationMS})
	case "file_read":
		var p cluster.FileReadPayload
		if err := decodePayload(msg.Payload, &p); err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		if p.Offset < 0 {
			return sendError(writeJSON, msg.ID, errors.New("negative offset"))
		}
		f, err := os.Open(p.Path)
		if err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		if _, err = f.Seek(p.Offset, io.SeekStart); err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		limit := p.Limit
		if limit <= 0 || limit > 1024*1024 {
			limit = 256 * 1024
		}
		buf := make([]byte, limit)
		n, err := io.ReadFull(f, buf)
		eof := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !eof {
			return sendError(writeJSON, msg.ID, err)
		}
		return writeJSON(cluster.Message{ID: msg.ID, Status: "success", Payload: map[string]any{"content": string(buf[:n]), "bytes": n, "total_size": info.Size(), "has_more": p.Offset+int64(n) < info.Size(), "eof": eof}})
	case "file_write":
		var p cluster.FileWritePayload
		if err := decodePayload(msg.Payload, &p); err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		if len(p.Content) > 1024*1024 {
			return sendError(writeJSON, msg.ID, errors.New("content too large"))
		}
		if err := os.WriteFile(p.Path, []byte(p.Content), 0644); err != nil {
			return sendError(writeJSON, msg.ID, err)
		}
		return writeJSON(cluster.Message{ID: msg.ID, Status: "success", Payload: map[string]any{"bytes": len(p.Content)}})
	default:
		return sendError(writeJSON, msg.ID, errors.New("unsupported action"))
	}
}

func decodePayload(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func sendError(writeJSON func(cluster.Message) error, id string, err error) error {
	return writeJSON(cluster.Message{ID: id, Status: "error", Error: err.Error()})
}
