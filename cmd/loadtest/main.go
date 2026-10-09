// Command loadtest simulates Hub Agent WebSocket connections and MCP clients.
// It intentionally targets only an explicitly supplied Hub URL; it never
// restarts the Hub or performs writes other than safe echo commands on fake agents.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wjyhk/vps-commander/internal/cluster"
)

const (
	defaultAgents         = 500
	defaultMCPClients     = 50
	defaultDuration       = 10 * time.Minute
	defaultAgentRamp      = 110 * time.Second
	defaultMCPRamp        = 10 * time.Second
	defaultStartupWait    = 2 * time.Minute
	defaultMCPInterval    = 30 * time.Second
	defaultSampleInterval = 5 * time.Second
	requestTimeout        = 10 * time.Second
	maxReconnectBackoff   = 30 * time.Second
)

type config struct {
	hub            *url.URL
	agentToken     string
	mcpTokens      []string
	metricsToken   string
	agents         int
	mcpClients     int
	duration       time.Duration
	agentRamp      time.Duration
	mcpRamp        time.Duration
	startupTimeout time.Duration
	interval       time.Duration
	sampleInterval time.Duration
	agentPrefix    string
	tool           string
	strict         bool
}

type counters struct {
	agentDialAttempts     atomic.Int64
	agentConnects         atomic.Int64
	agentConnected        atomic.Int64
	agentDisconnects      atomic.Int64
	agent429              atomic.Int64
	agentErrors           atomic.Int64
	agentExecResponses    atomic.Int64
	mcpActive             atomic.Int64
	mcpHTTPRequests       atomic.Int64
	agentTargetCursor     atomic.Int64
	mcpSSESessions        atomic.Int64
	mcpStreamableSessions atomic.Int64
	mcpInitializeOK       atomic.Int64
	mcpToolsListOK        atomic.Int64
	mcpCalls              atomic.Int64
	mcpCallsOK            atomic.Int64
	mcp429                atomic.Int64
	mcpErrors             atomic.Int64
	mcpExecDenied         atomic.Int64
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

type sseEvent struct {
	name string
	data string
}

type sseConnectionError struct{ err error }

func (e sseConnectionError) Error() string { return "SSE connection lost: " + e.err.Error() }
func (e sseConnectionError) Unwrap() error { return e.err }

type sseClient struct {
	ctx       context.Context
	cancel    context.CancelFunc
	body      io.ReadCloser
	endpoint  string
	responses chan rpcResponse
	errors    chan error
	client    *http.Client
	token     string
	stats     *counters
	id        int64
	closeOnce sync.Once
}

func main() {
	var hubRaw, agentToken, mcpToken, mcpTokenFile, metricsToken string
	var agents, mcpClients int
	var duration, agentRamp, mcpRamp, startupTimeout, interval, sampleInterval time.Duration
	var agentPrefix, tool string
	var strict bool
	flag.StringVar(&hubRaw, "hub", "", "Hub base URL (http:// or https://); required")
	flag.StringVar(&agentToken, "agent-token", os.Getenv("VPS_COMMANDER_CLUSTER_SECRET"), "Agent Bearer token (cluster secret when using unregistered fake names)")
	flag.StringVar(&mcpToken, "mcp-token", os.Getenv("VPS_COMMANDER_LOADTEST_MCP_TOKEN"), "MCP API token; ignored when -mcp-token-file is supplied")
	flag.StringVar(&mcpTokenFile, "mcp-token-file", "", "newline-separated MCP API tokens, assigned round-robin to clients")
	flag.StringVar(&metricsToken, "metrics-token", os.Getenv("VPS_COMMANDER_LOADTEST_METRICS_TOKEN"), "optional admin-scope token for scraping /metrics; defaults to the first MCP token")
	flag.IntVar(&agents, "agents", defaultAgents, "number of fake Agent WebSocket clients")
	flag.IntVar(&mcpClients, "mcp-clients", defaultMCPClients, "number of MCP clients (half SSE, half Streamable HTTP)")
	flag.DurationVar(&duration, "duration", defaultDuration, "steady-state test duration")
	flag.DurationVar(&agentRamp, "agent-ramp", defaultAgentRamp, "spread initial Agent connections across this duration (single IP: >= agents/5 seconds)")
	flag.DurationVar(&mcpRamp, "mcp-ramp", defaultMCPRamp, "spread MCP client startup across this duration")
	flag.DurationVar(&startupTimeout, "startup-timeout", defaultStartupWait, "maximum time to wait for all fake Agents to connect before starting MCP clients")
	flag.DurationVar(&interval, "interval", defaultMCPInterval, "delay between each MCP tools/list + tools/call cycle")
	flag.DurationVar(&sampleInterval, "sample-interval", defaultSampleInterval, "interval for /metrics and /api/v1/devices latency sampling")
	flag.StringVar(&agentPrefix, "agent-prefix", "loadtest", "prefix for unique fake Agent names")
	flag.StringVar(&tool, "tool", "exec_command", "safe tool to call: exec_command or list_devices")
	flag.BoolVar(&strict, "strict", false, "exit nonzero if targets/sessions are missing at end or MCP calls made no progress")
	flag.Parse()

	cfg, err := buildConfig(hubRaw, agentToken, mcpToken, mcpTokenFile, metricsToken, agents, mcpClients, duration, agentRamp, mcpRamp, startupTimeout, interval, sampleInterval, agentPrefix, tool, strict)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

func buildConfig(hubRaw, agentToken, mcpToken, tokenFile, metricsToken string, agents, clients int, duration, agentRamp, mcpRamp, startupTimeout, interval, sampleInterval time.Duration, prefix, tool string, strict bool) (config, error) {
	if strings.TrimSpace(hubRaw) == "" {
		return config{}, errors.New("-hub is required; point this at a test Hub, not production")
	}
	hub, err := url.Parse(strings.TrimSpace(hubRaw))
	if err != nil || hub.Host == "" || (hub.Scheme != "http" && hub.Scheme != "https") {
		return config{}, errors.New("-hub must be an absolute http:// or https:// URL")
	}
	if agents < 0 || clients < 0 || (agents == 0 && clients == 0) {
		return config{}, errors.New("-agents and -mcp-clients must be nonnegative and not both zero")
	}
	if duration <= 0 || startupTimeout < 0 || agentRamp < 0 || mcpRamp < 0 || interval <= 0 || sampleInterval <= 0 {
		return config{}, errors.New("duration/interval/sample-interval must be positive; ramp/startup timeout must be nonnegative")
	}
	if strings.TrimSpace(agentToken) == "" && agents > 0 {
		return config{}, errors.New("-agent-token is required when -agents > 0")
	}
	if tool != "exec_command" && tool != "list_devices" {
		return config{}, errors.New("-tool must be exec_command or list_devices")
	}
	if tool == "exec_command" && agents == 0 && clients > 0 {
		return config{}, errors.New("exec_command load requires at least one fake Agent; use -tool list_devices for MCP-only tests")
	}
	if agents > 0 && agentRamp < time.Duration(agents)*time.Second/5 {
		fmt.Fprintf(os.Stderr, "[loadtest] warning: agent-ramp=%s is below agents/5=%s; a single source IP may hit the Hub handshake rate limit (5/s, burst 20)\n", agentRamp, time.Duration(agents)*time.Second/5)
	}
	if strings.TrimSpace(prefix) == "" {
		return config{}, errors.New("-agent-prefix cannot be empty")
	}

	var tokens []string
	if strings.TrimSpace(tokenFile) != "" {
		data, readErr := os.ReadFile(tokenFile)
		if readErr != nil {
			return config{}, fmt.Errorf("read MCP token file: %w", readErr)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				tokens = append(tokens, line)
			}
		}
	} else if strings.TrimSpace(mcpToken) != "" {
		tokens = []string{strings.TrimSpace(mcpToken)}
	}
	if clients > 0 && len(tokens) == 0 {
		return config{}, errors.New("provide -mcp-token or -mcp-token-file when -mcp-clients > 0")
	}
	if strings.TrimSpace(metricsToken) == "" && len(tokens) > 0 {
		metricsToken = tokens[0]
	}
	return config{hub: hub, agentToken: agentToken, mcpTokens: tokens, metricsToken: strings.TrimSpace(metricsToken), agents: agents, mcpClients: clients, duration: duration, agentRamp: agentRamp, mcpRamp: mcpRamp, startupTimeout: startupTimeout, interval: interval, sampleInterval: sampleInterval, agentPrefix: prefix, tool: tool, strict: strict}, nil
}

func run(cfg config) error {
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	startedAt := time.Now()
	steadyStarted := startedAt
	runID := time.Now().UTC().Format("20060102T150405") + fmt.Sprintf("-%04x", rand.Intn(1<<16))
	agentNames := make([]string, cfg.agents)
	for i := range agentNames {
		agentNames[i] = fmt.Sprintf("%s-%s-%04d", cfg.agentPrefix, runID, i+1)
	}
	stats := &counters{}
	var wg sync.WaitGroup
	for i, name := range agentNames {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			runAgent(ctx, cfg, name, stagger(i, cfg.agents, cfg.agentRamp), stats)
		}(i, name)
	}

	if cfg.agents > 0 {
		deadline := time.NewTimer(cfg.startupTimeout)
		ticker := time.NewTicker(250 * time.Millisecond)
		lastProgress := time.Now()
	waitLoop:
		for stats.agentConnected.Load() < int64(cfg.agents) {
			select {
			case <-ctx.Done():
				break waitLoop
			case <-deadline.C:
				fmt.Fprintf(os.Stderr, "[loadtest] startup timeout: %d/%d fake Agents connected; continuing to MCP phase\n", stats.agentConnected.Load(), cfg.agents)
				break waitLoop
			case <-ticker.C:
				if time.Since(lastProgress) >= 10*time.Second {
					printProgress(stats, cfg)
					lastProgress = time.Now()
				}
			}
		}
		ticker.Stop()
		if !deadline.Stop() {
			select {
			case <-deadline.C:
			default:
			}
		}
	}
	if ctx.Err() != nil {
		cancel()
		wg.Wait()
		return errors.New("interrupted before steady-state phase")
	}

	transport := newHTTPTransport()
	postClient := &http.Client{Transport: transport, Timeout: requestTimeout}
	streamClient := &http.Client{Transport: transport}
	for i := 0; i < cfg.mcpClients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runMCPClient(ctx, cfg, i, stagger(i, cfg.mcpClients, cfg.mcpRamp), agentNames, postClient, streamClient, stats)
		}(i)
	}

	if !waitContext(ctx, cfg.mcpRamp) {
		cancel()
		wg.Wait()
		return errors.New("interrupted during MCP client ramp")
	}
	fmt.Fprintf(os.Stderr, "[loadtest] target=%s agents=%d mcp_clients=%d steady_duration=%s tool=%s; names use run id %s\n", cfg.hub.String(), cfg.agents, cfg.mcpClients, cfg.duration, cfg.tool, runID)
	steadyStarted = time.Now()
	sampler := newHubSampler(cfg.hub, cfg.metricsToken, cfg.sampleInterval)
	samplerCtx, stopSampler := context.WithCancel(ctx)
	samplerDone := make(chan struct{})
	go func() { defer close(samplerDone); sampler.run(samplerCtx) }()
	testTimer := time.NewTimer(cfg.duration)
	progressTicker := time.NewTicker(10 * time.Second)
	checkTicker := time.NewTicker(time.Second)
loop:
	for {
		select {
		case <-signalCtx.Done():
			break loop
		case <-testTimer.C:
			break loop
		case <-progressTicker.C:
			printProgress(stats, cfg)
		case <-checkTicker.C:
			if stats.mcpExecDenied.Load() > 0 {
				fmt.Fprintln(os.Stderr, "[loadtest] preflight failed: Hub security policy denied exec; enable exec only on the disposable test Hub, then rerun")
				break loop
			}
		}
	}
	progressTicker.Stop()
	checkTicker.Stop()
	if !testTimer.Stop() {
		select {
		case <-testTimer.C:
		default:
		}
	}
	endAgents := stats.agentConnected.Load()
	endMCP := stats.mcpActive.Load()
	endSSE := stats.mcpSSESessions.Load()
	endHTTP := stats.mcpStreamableSessions.Load()
	mcpSessionsAtEnd := endSSE + endHTTP
	stopSampler()
	<-samplerDone
	// Capture the final live-state sample before cancellation removes fake Agents and MCP sessions.
	sampler.sample(context.Background())
	cancel()
	wg.Wait()
	transport.CloseIdleConnections()

	metrics := sampler.lastMetrics()
	deviceLatency := sampler.latencySummary()
	summary := map[string]any{
		"hub": cfg.hub.String(), "run_id": runID, "agent_name_prefix": cfg.agentPrefix + "-" + runID, "started_at": startedAt.UTC().Format(time.RFC3339), "duration_seconds": time.Since(startedAt).Seconds(), "steady_state_duration_seconds": time.Since(steadyStarted).Seconds(),
		"target_agents": cfg.agents, "agents_online_at_end": endAgents, "agent_dial_attempts": stats.agentDialAttempts.Load(), "agent_connects_total": stats.agentConnects.Load(), "agent_disconnects_total": stats.agentDisconnects.Load(), "agent_429_total": stats.agent429.Load(), "agent_errors_total": stats.agentErrors.Load(), "fake_agent_exec_responses": stats.agentExecResponses.Load(),
		"target_mcp_clients": cfg.mcpClients, "mcp_clients_active_at_end": endMCP, "mcp_sse_sessions_at_end": endSSE, "mcp_streamable_sessions_at_end": endHTTP, "mcp_sessions_at_end": mcpSessionsAtEnd, "mcp_http_requests": stats.mcpHTTPRequests.Load(), "mcp_initialize_ok": stats.mcpInitializeOK.Load(), "mcp_tools_list_ok": stats.mcpToolsListOK.Load(), "mcp_tool_calls": stats.mcpCalls.Load(), "mcp_tool_calls_ok": stats.mcpCallsOK.Load(), "mcp_429_total": stats.mcp429.Load(), "mcp_errors_total": stats.mcpErrors.Load(), "mcp_exec_denied_total": stats.mcpExecDenied.Load(), "hub_metrics": metrics, "hub_metrics_samples": sampler.metricsSamples(), "devices_latency_ms": deviceLatency, "hub_resources": sampler.resourceSummary(),
	}
	encoded, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(encoded))

	if stats.mcpExecDenied.Load() > 0 {
		return errors.New("preflight failed: Hub security policy denied exec; enable exec only on the disposable test Hub and rerun")
	}
	if cfg.strict {
		if err := strictAcceptanceError(cfg, endAgents, endMCP, mcpSessionsAtEnd, stats.mcpCalls.Load(), stats.mcpCallsOK.Load(), stats.mcpExecDenied.Load()); err != nil {
			return err
		}
	}
	return nil
}

func strictAcceptanceError(cfg config, agents, clients, sessions, calls, callsOK, denied int64) error {
	// 429s are reported but are not automatically failures; zero calls or zero successful calls is never a pass.
	if agents < int64(cfg.agents) || clients < int64(cfg.mcpClients) || sessions < int64(cfg.mcpClients) || calls == 0 || callsOK == 0 || denied > 0 {
		return errors.New("strict acceptance failed; inspect JSON summary and Hub metrics")
	}
	return nil
}

func newHTTPTransport() *http.Transport {
	return &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256, MaxConnsPerHost: 256, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
}

func stagger(index, total int, span time.Duration) time.Duration {
	if total <= 1 || span <= 0 {
		return 0
	}
	return time.Duration(index) * span / time.Duration(total)
}

func agentWSURL(base *url.URL, name string) string {
	u := *base
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/agent/ws"
	q := u.Query()
	q.Set("device_name", name)
	u.RawQuery = q.Encode()
	return u.String()
}

func httpEndpoint(base *url.URL, path string) string {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	return u.String()
}

func runAgent(ctx context.Context, cfg config, name string, initialDelay time.Duration, stats *counters) {
	if !waitContext(ctx, initialDelay) {
		return
	}
	backoff := time.Second
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, EnableCompression: false}
	for ctx.Err() == nil {
		stats.agentDialAttempts.Add(1)
		header := http.Header{}
		header.Set("Authorization", "Bearer "+cfg.agentToken)
		conn, response, err := dialer.DialContext(ctx, agentWSURL(cfg.hub, name), header)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			status := 0
			delay := backoff + time.Duration(rand.Intn(500))*time.Millisecond
			if response != nil {
				status = response.StatusCode
				if status == http.StatusTooManyRequests {
					stats.agent429.Add(1)
					delay = retryAfter(response.Header.Get("Retry-After")) + time.Duration(rand.Intn(1000))*time.Millisecond
				} else {
					stats.agentErrors.Add(1)
				}
				if response.Body != nil {
					_ = response.Body.Close()
				}
			} else {
				stats.agentErrors.Add(1)
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusBadRequest {
				return
			}
			if !waitContext(ctx, delay) {
				return
			}
			backoff = minDuration(backoff*2, maxReconnectBackoff)
			continue
		}
		backoff = time.Second
		stats.agentConnects.Add(1)
		stats.agentConnected.Add(1)
		err = runAgentConnection(ctx, conn, name, stats)
		_ = conn.Close()
		stats.agentConnected.Add(-1)
		if ctx.Err() != nil {
			return
		}
		stats.agentDisconnects.Add(1)
		if !waitContext(ctx, backoff+time.Duration(rand.Intn(1000))*time.Millisecond) {
			return
		}
		backoff = minDuration(backoff*2, maxReconnectBackoff)
	}
}

func runAgentConnection(ctx context.Context, conn *websocket.Conn, name string, stats *counters) error {
	var writeMu sync.Mutex
	write := func(msg cluster.Message) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(msg)
	}
	now := time.Now()
	hello := cluster.Message{Event: "hello", Device: name, Arch: "amd64", OS: "linux", TS: now.Unix(), ProtocolVersion: "2", AgentVersion: "loadtest", Capabilities: []string{"exec_command", "read_file", "write_file"}}
	if err := write(hello); err != nil {
		return err
	}
	stopClose := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopClose:
		}
	}()
	defer close(stopClose)
	heartbeatStop := make(chan struct{})
	defer close(heartbeatStop)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := write(cluster.Message{Event: "ping", Device: name, Arch: "amd64", OS: "linux", TS: time.Now().Unix()}); err != nil {
					_ = conn.Close()
					return
				}
			case <-heartbeatStop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		var msg cluster.Message
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		switch msg.Event {
		case "ping":
			if err := write(cluster.Message{Event: "pong", Device: name, Arch: "amd64", OS: "linux", TS: msg.TS}); err != nil {
				return err
			}
		case "revoked":
			return errors.New("fake agent revoked")
		case "registered", "mcp_sync":
			// Control messages do not require a response.
		default:
			if msg.ID != "" && msg.Action != "" {
				if msg.Action == "exec" {
					stats.agentExecResponses.Add(1)
				}
				if err := write(cluster.Message{ID: msg.ID, Status: "ok", ExitCode: 0, Stdout: "loadtest-ok", DurationMS: 1, TS: time.Now().Unix()}); err != nil {
					return err
				}
			}
		}
	}
	return ctx.Err()
}

func runMCPClient(ctx context.Context, cfg config, index int, delay time.Duration, agentNames []string, postClient, streamClient *http.Client, stats *counters) {
	if !waitContext(ctx, delay) {
		return
	}
	stats.mcpActive.Add(1)
	defer stats.mcpActive.Add(-1)
	token := cfg.mcpTokens[index%len(cfg.mcpTokens)]
	if index%2 == 0 {
		runStreamableClient(ctx, cfg, index, token, agentNames, postClient, stats)
	} else {
		runSSEClient(ctx, cfg, index, token, agentNames, postClient, streamClient, stats)
	}
}

func toolArguments(cfg config, index int, agentNames []string, stats *counters) map[string]any {
	if cfg.tool == "list_devices" {
		return map[string]any{}
	}
	device := ""
	if len(agentNames) > 0 {
		target := int(stats.agentTargetCursor.Add(1)-1) % len(agentNames)
		device = agentNames[target]
	}
	return map[string]any{"device": device, "command": "echo loadtest", "timeout": 10}
}

func initializeParams() map[string]any {
	return map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "vps-commander-loadtest", "version": "1.0.0"}}
}

func runStreamableClient(ctx context.Context, cfg config, index int, token string, agentNames []string, client *http.Client, stats *counters) {
	var id int64
	sessionID, protocolVersion := "", "2025-03-26"
	sessionCounted := false
	defer func() {
		if sessionCounted {
			stats.mcpStreamableSessions.Add(-1)
		}
		if sessionID != "" {
			deleteStreamableSession(cfg.hub, token, sessionID, client)
		}
	}()
	for ctx.Err() == nil {
		if sessionID == "" {
			resp, status, sid, version, err := streamableRPC(ctx, cfg.hub, token, "", "", client, rpcRequest{JSONRPC: "2.0", ID: nextID(&id), Method: "initialize", Params: initializeParams()}, stats)
			if err != nil || status != http.StatusOK || resp == nil || resp.Error != nil {
				if err != nil && !isHTTPStatusError(err, http.StatusTooManyRequests) {
					stats.mcpErrors.Add(1)
				} else if status != http.StatusTooManyRequests {
					stats.mcpErrors.Add(1)
				}
				_ = waitContext(ctx, cfg.interval)
				continue
			}
			sessionID = sid
			if version != "" {
				protocolVersion = version
			}
			if sessionID != "" && !sessionCounted {
				stats.mcpStreamableSessions.Add(1)
				sessionCounted = true
			}
			stats.mcpInitializeOK.Add(1)
		}

		listResp, status, _, _, err := streamableRPC(ctx, cfg.hub, token, sessionID, protocolVersion, client, rpcRequest{JSONRPC: "2.0", ID: nextID(&id), Method: "tools/list"}, stats)
		if status == http.StatusNotFound {
			sessionID = ""
			if sessionCounted {
				stats.mcpStreamableSessions.Add(-1)
				sessionCounted = false
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil || status != http.StatusOK || listResp == nil || listResp.Error != nil {
			if status != http.StatusTooManyRequests {
				stats.mcpErrors.Add(1)
			}
			if responseDeniesExec(listResp) {
				recordExecDenied(stats)
			}
			if !waitContext(ctx, cfg.interval) {
				return
			}
			continue
		}
		stats.mcpToolsListOK.Add(1)

		stats.mcpCalls.Add(1)
		callParams := map[string]any{"name": cfg.tool, "arguments": toolArguments(cfg, index, agentNames, stats)}
		callResp, status, _, _, err := streamableRPC(ctx, cfg.hub, token, sessionID, protocolVersion, client, rpcRequest{JSONRPC: "2.0", ID: nextID(&id), Method: "tools/call", Params: callParams}, stats)
		if status == http.StatusNotFound {
			sessionID = ""
			if sessionCounted {
				stats.mcpStreamableSessions.Add(-1)
				sessionCounted = false
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil || status != http.StatusOK || callResp == nil || callResp.Error != nil {
			if status != http.StatusTooManyRequests {
				stats.mcpErrors.Add(1)
			}
			if responseDeniesExec(callResp) {
				recordExecDenied(stats)
			}
		} else if toolResultIsError(callResp.Result) {
			stats.mcpErrors.Add(1)
			if responseDeniesExec(callResp) {
				recordExecDenied(stats)
			}
		} else {
			stats.mcpCallsOK.Add(1)
		}
		if !waitContext(ctx, cfg.interval) {
			return
		}
	}
}

// deleteStreamableSession ends the client's Streamable HTTP session so a run
// does not leave idle sessions on the Hub until they time out.
func deleteStreamableSession(base *url.URL, token, sessionID string, client *http.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, httpEndpoint(base, "/mcp"), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

func streamableRPC(ctx context.Context, base *url.URL, token, sessionID, protocolVersion string, client *http.Client, request rpcRequest, stats *counters) (*rpcResponse, int, string, string, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, 0, "", "", err
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, httpEndpoint(base, "/mcp"), strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if protocolVersion != "" {
		req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	}
	stats.mcpHTTPRequests.Add(1)
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, "", "", err
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status == http.StatusTooManyRequests {
		stats.mcp429.Add(1)
	}
	if status != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, status, response.Header.Get("Mcp-Session-Id"), response.Header.Get("Mcp-Protocol-Version"), fmt.Errorf("MCP HTTP status %d", status)
	}
	var rpcResp rpcResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&rpcResp); err != nil {
		return nil, status, response.Header.Get("Mcp-Session-Id"), response.Header.Get("Mcp-Protocol-Version"), err
	}
	return &rpcResp, status, response.Header.Get("Mcp-Session-Id"), response.Header.Get("Mcp-Protocol-Version"), nil
}

func runSSEClient(ctx context.Context, cfg config, index int, token string, agentNames []string, postClient, streamClient *http.Client, stats *counters) {
	var id int64
	for ctx.Err() == nil {
		stream, err := openSSE(ctx, cfg.hub, token, streamClient, stats)
		if err != nil {
			if ctx.Err() == nil && !isHTTPStatusError(err, http.StatusTooManyRequests) {
				stats.mcpErrors.Add(1)
			}
			if !waitContext(ctx, cfg.interval) {
				return
			}
			continue
		}
		stats.mcpSSESessions.Add(1)
		_, err = stream.call(ctx, rpcRequest{JSONRPC: "2.0", ID: nextID(&id), Method: "initialize", Params: initializeParams()}, postClient)
		if err != nil {
			stream.close()
			stats.mcpSSESessions.Add(-1)
			if ctx.Err() != nil {
				return
			}
			if !isHTTPStatusError(err, http.StatusTooManyRequests) {
				stats.mcpErrors.Add(1)
			}
			if !waitContext(ctx, cfg.interval) {
				return
			}
			continue
		}
		stats.mcpInitializeOK.Add(1)
		for ctx.Err() == nil {
			resp, err := stream.call(ctx, rpcRequest{JSONRPC: "2.0", ID: nextID(&id), Method: "tools/list"}, postClient)
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				if !isHTTPStatusError(err, http.StatusTooManyRequests) {
					stats.mcpErrors.Add(1)
				}
				if isConnectionError(err) {
					break
				}
				if !waitContext(ctx, cfg.interval) {
					break
				}
				continue
			}
			if resp.Error != nil {
				stats.mcpErrors.Add(1)
			} else {
				stats.mcpToolsListOK.Add(1)
			}

			stats.mcpCalls.Add(1)
			callParams := map[string]any{"name": cfg.tool, "arguments": toolArguments(cfg, index, agentNames, stats)}
			callResp, err := stream.call(ctx, rpcRequest{JSONRPC: "2.0", ID: nextID(&id), Method: "tools/call", Params: callParams}, postClient)
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				if !isHTTPStatusError(err, http.StatusTooManyRequests) {
					stats.mcpErrors.Add(1)
				}
				if isConnectionError(err) {
					break
				}
			} else if callResp.Error != nil || toolResultIsError(callResp.Result) {
				stats.mcpErrors.Add(1)
				if responseDeniesExec(callResp) {
					recordExecDenied(stats)
				}
			} else {
				stats.mcpCallsOK.Add(1)
			}
			if !waitContext(ctx, cfg.interval) {
				break
			}
		}
		stream.close()
		stats.mcpSSESessions.Add(-1)
		if ctx.Err() != nil {
			return
		}
		if !waitContext(ctx, cfg.interval) {
			return
		}
	}
}

func openSSE(ctx context.Context, base *url.URL, token string, client *http.Client, stats *counters) (*sseClient, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, httpEndpoint(base, "/mcp/sse"), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	stats.mcpHTTPRequests.Add(1)
	response, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		status := response.StatusCode
		if status == http.StatusTooManyRequests {
			stats.mcp429.Add(1)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		cancel()
		return nil, fmt.Errorf("SSE HTTP status %d", status)
	}
	reader := bufio.NewReaderSize(response.Body, 32*1024)
	eventCh := make(chan sseReadResult, 1)
	go func() { event, err := readSSEEvent(reader); eventCh <- sseReadResult{event: event, err: err} }()
	var first sseReadResult
	select {
	case first = <-eventCh:
	case <-ctx.Done():
		_ = response.Body.Close()
		cancel()
		return nil, ctx.Err()
	case <-time.After(requestTimeout):
		_ = response.Body.Close()
		cancel()
		return nil, errors.New("timeout waiting for SSE endpoint event")
	}
	if first.err != nil || first.event.name != "endpoint" {
		_ = response.Body.Close()
		cancel()
		if first.err != nil {
			return nil, first.err
		}
		return nil, fmt.Errorf("expected SSE endpoint event, got %q", first.event.name)
	}
	endpointRef, err := url.Parse(first.event.data)
	if err != nil {
		_ = response.Body.Close()
		cancel()
		return nil, err
	}
	endpoint := base.ResolveReference(endpointRef).String()
	clientState := &sseClient{ctx: streamCtx, cancel: cancel, body: response.Body, endpoint: endpoint, responses: make(chan rpcResponse, 256), errors: make(chan error, 1), client: client, token: token, stats: stats}
	go clientState.readLoop(reader)
	return clientState, nil
}

type sseReadResult struct {
	event sseEvent
	err   error
}

func (c *sseClient) readLoop(reader *bufio.Reader) {
	for {
		event, err := readSSEEvent(reader)
		if err != nil {
			select {
			case c.errors <- sseConnectionError{err: err}:
			default:
			}
			return
		}
		if event.name != "message" {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(event.data), &response); err != nil {
			continue
		}
		select {
		case c.responses <- response:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *sseClient) call(ctx context.Context, request rpcRequest, client *http.Client) (*rpcResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.stats.mcpHTTPRequests.Add(1)
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	status := response.StatusCode
	if status == http.StatusTooManyRequests {
		c.stats.mcp429.Add(1)
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		return nil, fmt.Errorf("SSE message HTTP status %d", status)
	}
	if status == http.StatusOK {
		defer response.Body.Close()
		var rpcResp rpcResponse
		if err := json.NewDecoder(response.Body).Decode(&rpcResp); err != nil {
			return nil, err
		}
		return &rpcResp, nil
	}
	_ = response.Body.Close()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-reqCtx.Done():
			return nil, reqCtx.Err()
		case err := <-c.errors:
			return nil, err
		case result := <-c.responses:
			if fmt.Sprint(result.ID) == fmt.Sprint(request.ID) {
				return &result, nil
			}
		}
	}
}

func (c *sseClient) close() {
	c.closeOnce.Do(func() { c.cancel(); _ = c.body.Close() })
}

func readSSEEvent(reader *bufio.Reader) (sseEvent, error) {
	var event string
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return sseEvent{}, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if event != "" || len(data) > 0 {
				return sseEvent{name: event, data: strings.Join(data, "\n")}, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func toolResultIsError(raw json.RawMessage) bool {
	var result struct {
		IsError bool `json:"isError"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil {
		return false
	}
	return result.IsError
}

func nextID(id *int64) int64 { *id = *id + 1; return *id }

func waitContext(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 1 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func isHTTPStatusError(err error, status int) bool {
	return err != nil && strings.Contains(err.Error(), fmt.Sprintf("HTTP status %d", status))
}
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var streamErr sseConnectionError
	return errors.As(err, &streamErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(strings.ToLower(err.Error()), "closed network connection") || strings.Contains(strings.ToLower(err.Error()), "use of closed network connection")
}

func printProgress(stats *counters, cfg config) {
	fmt.Fprintf(os.Stderr, "[loadtest] agents=%d/%d connects=%d 429=%d errors=%d exec_responses=%d | mcp_active=%d/%d sse=%d streamable=%d calls=%d/%d 429=%d errors=%d\n",
		stats.agentConnected.Load(), cfg.agents, stats.agentConnects.Load(), stats.agent429.Load(), stats.agentErrors.Load(), stats.agentExecResponses.Load(),
		stats.mcpActive.Load(), cfg.mcpClients, stats.mcpSSESessions.Load(), stats.mcpStreamableSessions.Load(), stats.mcpCallsOK.Load(), stats.mcpCalls.Load(), stats.mcp429.Load(), stats.mcpErrors.Load())
}

func responseDeniesExec(resp *rpcResponse) bool {
	if resp == nil {
		return false
	}
	if resp.Error != nil && strings.Contains(strings.ToLower(resp.Error.Message), "denies exec") {
		return true
	}
	return strings.Contains(strings.ToLower(string(resp.Result)), "denies exec")
}

type hubSampler struct {
	base      *url.URL
	token     string
	interval  time.Duration
	client    *http.Client
	mu        sync.Mutex
	metrics   []map[string]string
	latencies []float64
}

func newHubSampler(base *url.URL, token string, interval time.Duration) *hubSampler {
	return &hubSampler{base: base, token: token, interval: interval, client: &http.Client{Timeout: 3 * time.Second}}
}

func (s *hubSampler) run(ctx context.Context) {
	s.sample(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sample(ctx)
		}
	}
}

func (s *hubSampler) sample(ctx context.Context) {
	if s.token == "" {
		return
	}
	metrics := scrapeMetrics(s.base, s.token, s.client)
	latency := s.devicesLatency(ctx)
	s.mu.Lock()
	if metrics != nil {
		metrics["sampled_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		s.metrics = append(s.metrics, metrics)
	}
	if latency >= 0 {
		s.latencies = append(s.latencies, latency)
	}
	s.mu.Unlock()
}

func (s *hubSampler) devicesLatency(ctx context.Context) float64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpEndpoint(s.base, "/api/v1/devices"), nil)
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return -1
	}
	// Time the full response body: /api/v1/devices grows with the fleet.
	_, copyErr := io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(start)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || copyErr != nil {
		return -1
	}
	return float64(elapsed.Microseconds()) / 1000
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	idx := int(float64(len(copyValues)-1)*p + 0.999999)
	if idx >= len(copyValues) {
		idx = len(copyValues) - 1
	}
	return copyValues[idx]
}

func (s *hubSampler) latencySummary() map[string]any {
	s.mu.Lock()
	values := append([]float64(nil), s.latencies...)
	s.mu.Unlock()
	return map[string]any{"samples": len(values), "p50_ms": percentile(values, .50), "p95_ms": percentile(values, .95), "p99_ms": percentile(values, .99), "max_ms": percentile(values, 1)}
}

// resourceSummary derives Hub CPU (cores, averaged over the sampled window)
// and peak RSS from the periodic /metrics samples.
func (s *hubSampler) resourceSummary() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"samples": len(s.metrics)}
	var firstCPU, lastCPU float64
	var firstAt, lastAt time.Time
	var maxRSS float64
	cpuSamples := 0
	for _, m := range s.metrics {
		if v, err := strconv.ParseFloat(m["vps_commander_process_resident_memory_bytes"], 64); err == nil && v > maxRSS {
			maxRSS = v
		}
		cpu, err1 := strconv.ParseFloat(m["vps_commander_process_cpu_seconds_total"], 64)
		at, err2 := time.Parse(time.RFC3339Nano, m["sampled_at"])
		if err1 != nil || err2 != nil {
			continue
		}
		if cpuSamples == 0 {
			firstCPU, firstAt = cpu, at
		}
		lastCPU, lastAt = cpu, at
		cpuSamples++
	}
	out["rss_max_mb"] = maxRSS / (1024 * 1024)
	if cpuSamples >= 2 && lastAt.After(firstAt) && lastCPU >= firstCPU {
		out["cpu_avg_cores"] = (lastCPU - firstCPU) / lastAt.Sub(firstAt).Seconds()
	} else {
		out["cpu_avg_cores"] = nil
	}
	return out
}

func (s *hubSampler) metricsSamples() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]string, len(s.metrics))
	for i, m := range s.metrics {
		clone := make(map[string]string, len(m))
		for k, v := range m {
			clone[k] = v
		}
		out[i] = clone
	}
	return out
}

func (s *hubSampler) lastMetrics() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.metrics) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.metrics[len(s.metrics)-1]))
	for k, v := range s.metrics[len(s.metrics)-1] {
		out[k] = v
	}
	return out
}

func scrapeMetrics(base *url.URL, token string, client *http.Client) map[string]string {
	if token == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, httpEndpoint(base, "/metrics"), nil)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return map[string]string{"error": fmt.Sprintf("metrics endpoint returned HTTP %d (token may need admin scope)", response.StatusCode)}
	}
	out := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 1024*1024))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			out[fields[0]] = fields[1]
		}
	}
	if err := scanner.Err(); err != nil {
		out["error"] = err.Error()
	}
	return out
}

// recordExecDenied treats an exec denial as a policy preflight failure only
// before any tool call has succeeded. Once calls succeed, a denial is transient
// (for example a fake Agent briefly offline during a Hub restart, which the Hub
// reports as security mode "unknown") and is counted as an ordinary error.
func recordExecDenied(stats *counters) {
	if stats.mcpCallsOK.Load() == 0 {
		stats.mcpExecDenied.Add(1)
	}
}
