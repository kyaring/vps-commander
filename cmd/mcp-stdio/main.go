package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wjyhk/vps-commander/internal/mcp"
)

type HubRemoteBackend struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func (h *HubRemoteBackend) post(path string, reqBody any, out any) error {
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	url := strings.TrimRight(h.BaseURL, "/") + path
	req, err := http.NewRequest("POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.APIKey)

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bs, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bs))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (h *HubRemoteBackend) ListDevices(ctx context.Context) (any, error) {
	url := strings.TrimRight(h.BaseURL, "/") + "/api/v1/devices"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.APIKey)

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bs, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bs))
	}
	var res any
	err = json.NewDecoder(resp.Body).Decode(&res)
	return res, err
}

func (h *HubRemoteBackend) ExecCommand(ctx context.Context, clientIP, device, command, workdir string, timeout int) (any, error) {
	payload := map[string]any{
		"device":  device,
		"command": command,
		"workdir": workdir,
		"timeout": timeout,
	}
	var res any
	err := h.post("/api/v1/exec", payload, &res)
	return res, err
}

func (h *HubRemoteBackend) ReadFile(ctx context.Context, clientIP, device, path string, offset, limit int64) (any, error) {
	payload := map[string]any{
		"device": device,
		"path":   path,
		"offset": offset,
		"limit":  limit,
	}
	var res any
	err := h.post("/api/v1/file/read", payload, &res)
	return res, err
}

func (h *HubRemoteBackend) EditBlock(ctx context.Context, clientIP, device, path, oldText, newText string) (any, error) {
	payload := map[string]any{"device": device, "path": path, "old_text": oldText, "new_text": newText}
	var res any
	err := h.post("/api/v1/devices/file/edit_block", payload, &res)
	return res, err
}

func (h *HubRemoteBackend) WriteFile(ctx context.Context, clientIP, device, path, content string) (any, error) {
	payload := map[string]any{
		"device":  device,
		"path":    path,
		"content": content,
	}
	var res any
	err := h.post("/api/v1/file/write", payload, &res)
	return res, err
}

func main() {
	baseURL := flag.String("hub", "http://127.0.0.1:9521", "VPS-Commander Hub base URL")
	apiKey := flag.String("key", os.Getenv("VPS_COMMANDER_API_KEY"), "VPS-Commander Hub API Key")
	flag.Parse()

	if *apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: -key or VPS_COMMANDER_API_KEY environment variable is required")
		os.Exit(1)
	}

	backend := &HubRemoteBackend{
		BaseURL:    *baseURL,
		APIKey:     *apiKey,
		HTTPClient: &http.Client{Timeout: 310 * time.Second},
	}

	srv := mcp.NewServer(backend, nil)
	scanner := bufio.NewScanner(os.Stdin)

	// stdio line-by-line JSON-RPC
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var req mcp.JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		resp := srv.HandleDirectRequest(context.Background(), req)
		respBytes, _ := json.Marshal(resp)
		os.Stdout.Write(respBytes)
		os.Stdout.WriteString("\n")
	}
}

func (h *HubRemoteBackend) ListAgentMCP(ctx context.Context, device string) (any, error) {
	var res any
	err := h.get(ctx, "/api/v1/mcp/list?device="+url.QueryEscape(device), &res)
	return res, err
}
func (h *HubRemoteBackend) CallAgentMCP(ctx context.Context, clientIP, device, server, tool string, args any) (any, error) {
	payload := map[string]any{"device": device, "server": server, "tool": tool, "arguments": args}
	var res any
	return res, h.post("/api/v1/mcp/call", payload, &res)
}
func (h *HubRemoteBackend) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.APIKey)
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
