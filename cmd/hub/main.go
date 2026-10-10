package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wjyhk/vps-commander/internal/api"
	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/notify"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func positiveEnvInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

type requestLogWriter struct {
	http.ResponseWriter
	status   int
	bytes    int64
	onStatus func(int)
}

func (w *requestLogWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if w.onStatus != nil {
		w.onStatus(status)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestLogWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	return n, err
}

func (w *requestLogWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *requestLogWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil && w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

func (w *requestLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func configureRequestLogging() {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VPS_COMMANDER_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	case "info", "":
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func configuredCORSOrigins() map[string]struct{} {
	origins := make(map[string]struct{})
	for _, value := range strings.Split(os.Getenv("VPS_COMMANDER_CORS_ORIGINS"), ",") {
		origin := strings.TrimSpace(value)
		if origin != "" && origin != "*" {
			origins[origin] = struct{}{}
		}
	}
	return origins
}

func originAllowed(origin string, allowed map[string]struct{}) bool {
	_, ok := allowed[origin]
	return origin != "" && ok
}

func addVaryOrigin(h http.Header) {
	for _, value := range h.Values("Vary") {
		for _, item := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), "Origin") {
				return
			}
		}
	}
	h.Add("Vary", "Origin")
}

func shouldLogRequest(path string, status int) bool {
	return path != "/healthz" && !strings.HasPrefix(path, "/static/") && status != http.StatusNotModified
}

func withRequestLoggingAndCORS(mux http.Handler, allowedOrigins map[string]struct{}, onStatus func(int)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rw := &requestLogWriter{ResponseWriter: w, onStatus: onStatus}
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		allowed := originAllowed(origin, allowedOrigins)
		if allowed {
			rw.Header().Set("Access-Control-Allow-Origin", origin)
			addVaryOrigin(rw.Header())
		}
		if r.Method == http.MethodOptions {
			if origin != "" && !allowed {
				http.Error(rw, "CORS origin not allowed", http.StatusForbidden)
			} else {
				if allowed {
					rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD, DELETE, PATCH")
					rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, If-None-Match, Mcp-Session-Id, Mcp-Protocol-Version")
				}
				rw.WriteHeader(http.StatusNoContent)
			}
		} else {
			mux.ServeHTTP(rw, r)
		}
		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}
		if shouldLogRequest(r.URL.Path, status) {
			slog.Info("http_request", "method", r.Method, "path", r.URL.Path, "client_ip", auth.ClientIP(r), "status", status, "duration_ms", time.Since(started).Milliseconds(), "bytes", rw.bytes)
		}
	})
}

// wirePanel mounts the panel and connects its live session validator to OAuth.
// Keep this as the single assembly path so the login -> consent flow is tested
// with the same Panel instance that owns the session cookie.
func wirePanel(mux *http.ServeMux, s *api.Server, webPassword string, mcpServer *mcp.Server) {
	panel := s.Panel(webPassword)
	mcpServer.OAuthPanelSessionValid = panel.ValidSession
	mux.Handle("/", panel.Handler())
}

func main() {
	configureRequestLogging()
	addr := flag.String("addr", "127.0.0.1:9521", "listen address")
	dbPath := flag.String("db", "data/commander.db", "sqlite database")
	flag.Parse()
	apiKey := os.Getenv("VPS_COMMANDER_API_KEY")
	legacyKeyOff := strings.EqualFold(os.Getenv("VPS_COMMANDER_LEGACY_KEY"), "off")
	clusterSecret := os.Getenv("VPS_COMMANDER_CLUSTER_SECRET")
	webPassword := os.Getenv("VPS_COMMANDER_WEB_PASSWORD")
	adminToken := os.Getenv("VPS_COMMANDER_ADMIN_TOKEN")
	installCommand := os.Getenv("VPS_COMMANDER_INSTALL_COMMAND")
	auditRetentionDays := 90
	if v, err := strconv.Atoi(os.Getenv("VPS_COMMANDER_AUDIT_RETENTION_DAYS")); err == nil && v > 0 {
		auditRetentionDays = v
	}
	if adminToken == "" {
		adminToken = webPassword
	}
	if webPassword == "" || (apiKey == "" && !legacyKeyOff) {
		log.Fatal("VPS_COMMANDER_WEB_PASSWORD is required; VPS_COMMANDER_API_KEY is required unless VPS_COMMANDER_LEGACY_KEY=off")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	var journalModeString string
	var synchronous int
	var busyTimeout, walAutoCheckpoint int
	_ = store.DB.QueryRow("PRAGMA journal_mode").Scan(&journalModeString)
	_ = store.DB.QueryRow("PRAGMA synchronous").Scan(&synchronous)
	_ = store.DB.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)
	_ = store.DB.QueryRow("PRAGMA wal_autocheckpoint").Scan(&walAutoCheckpoint)
	log.Printf("sqlite pragmas: journal_mode=%s synchronous=%d busy_timeout=%d wal_autocheckpoint=%d", journalModeString, synchronous, busyTimeout, walAutoCheckpoint)
	if err := store.CleanupAudits(auditRetentionDays); err != nil {
		log.Printf("audit retention cleanup: %v", err)
	}
	if err := store.EnsureAPITokens(apiKey); err != nil {
		log.Fatalf("ensure api tokens: %v", err)
	}
	authManager := auth.NewManagerWithValidator(apiKey, auth.TokenValidatorFunc(func(raw, ip string) (auth.Principal, bool) {
		id, name, scopes, devices, ok := store.ValidateAPIToken(raw, ip)
		if !ok {
			return auth.Principal{}, false
		}
		t, err := store.GetAPITokenByID(id)
		if err != nil || (legacyKeyOff && t.IsLegacy) {
			return auth.Principal{}, false
		}
		return auth.Principal{Type: "api_token", ID: id, Name: name, TokenID: id, Scopes: scopes, Devices: devices, MaxSessions: t.MaxSessions, RateRPS: t.RateRPS}, true
	}))
	local := "local"
	if h, err := os.Hostname(); err == nil && h != "" {
		local = h
	}
	profileInterval := 5 * time.Minute
	if seconds, err := strconv.Atoi(os.Getenv("VPS_COMMANDER_PROFILE_INTERVAL_SECONDS")); err == nil && seconds > 0 {
		profileInterval = time.Duration(seconds) * time.Second
	}
	if err := store.UpsertLocal(local, runtime.GOARCH, runtime.GOOS); err != nil {
		log.Fatal(err)
	}
	notificationManager := notify.New(store)
	notificationManager.Start()
	defer notificationManager.Stop()
	execConcurrency := positiveEnvInt("VPS_COMMANDER_EXEC_CONCURRENCY", 32)
	searchConcurrency := positiveEnvInt("VPS_COMMANDER_SEARCH_CONCURRENCY", 8)
	tokenExecConcurrency := positiveEnvInt("VPS_COMMANDER_TOKEN_EXEC_CONCURRENCY", 2)
	s := &api.Server{
		Auth:           authManager,
		Exec:           executor.Local{MaxOutput: 1024 * 1024},
		Cluster:        cluster.NewManager(clusterSecret, store),
		Store:          store,
		LocalName:      local,
		Notify:         notificationManager,
		AdminToken:     adminToken,
		InstallCommand: installCommand,
		ExecLimiter:    make(chan struct{}, execConcurrency),
		SearchLimiter:  make(chan struct{}, searchConcurrency),
		TokenLimiter:   api.NewTokenLimiter(tokenExecConcurrency),
		LocalSessions:  executor.NewSessionManager(0),
	}
	s.Cluster.SetProfileInterval(profileInterval)
	log.Printf("agent profile interval: %s", s.Cluster.ProfileInterval)
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		log.Fatalf("load security policy snapshot: %v", err)
	}

	// MCP Server instance
	mcpServer := mcp.NewServer(s.NewMCPBackend(), authManager)
	mcpServer.OAuthStore = store
	mcpServer.SetLocalName(local)
	s.MCP = mcpServer

	protected := auth.Middleware{Manager: authManager, OnAuthenticated: s.RecordLegacyAuth}.Handler(s.TokenRateLimitMiddleware(s.Routes()))
	mux := http.NewServeMux()
	mux.Handle("/api/", protected)
	mux.Handle("/metrics", protected)

	// MCP Endpoints. OAuth access tokens are accepted only on the canonical
	// Streamable HTTP endpoint, and only for the configured PUBLIC_URL/mcp resource.
	oauthResource, _ := mcp.OAuthResourceURL()
	mcpAuth := auth.Middleware{
		Manager: authManager, OnAuthenticated: s.RecordLegacyAuth, WWWAuthenticate: mcp.OAuthWWWAuthenticate(), OAuthResource: oauthResource,
		OAuthTokenValidator: func(raw, resource, ip string, now int64) (auth.Principal, bool) {
			o, ok := store.ValidateOAuthAccessToken(raw, resource, ip, now)
			if !ok {
				return auth.Principal{}, false
			}
			// Keep the underlying named API token visible in audit actor labels;
			// keep grant-specific session ownership while sharing quotas with the underlying API token.
			return auth.Principal{
				Type: "api_token", ID: o.APITokenID, Name: o.TokenName,
				TokenID: "oauth:" + o.GrantID, QuotaTokenID: o.APITokenID,
				AuthMethod: "oauth", OAuthClient: o.ClientID,
				Scopes: o.Scopes, Devices: o.Devices,
				MaxSessions: o.MaxSessions, RateRPS: o.RateRPS,
			}, true
		},
	}
	mux.Handle("/mcp", mcpAuth.Handler(s.TokenRateLimitMiddleware(http.HandlerFunc(mcpServer.HandleStreamableHTTP))))
	mux.Handle("/mcp/sse", mcpAuth.Handler(s.TokenRateLimitMiddleware(http.HandlerFunc(mcpServer.HandleSSE))))
	mux.Handle("/mcp/message", mcpAuth.Handler(s.TokenRateLimitMiddleware(http.HandlerFunc(mcpServer.HandleMessage))))

	// OAuth Endpoints
	mux.HandleFunc("/.well-known/oauth-authorization-server", mcpServer.HandleOAuthMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", mcpServer.HandleOAuthMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", mcpServer.HandleOAuthMetadata)
	mux.HandleFunc("/oauth/authorize", mcpServer.HandleAuthorize)
	mux.HandleFunc("/oauth/token", mcpServer.HandleToken)
	mux.HandleFunc("/healthz", s.Routes().ServeHTTP)
	mux.HandleFunc("/openapi.json", s.Routes().ServeHTTP)
	mux.HandleFunc("/agent/ws", s.Routes().ServeHTTP)
	wirePanel(mux, s, webPassword, mcpServer)

	// Structured request logging and an explicit CORS origin allowlist. An
	// unset VPS_COMMANDER_CORS_ORIGINS means no cross-origin browser access.
	rootHandler := withRequestLoggingAndCORS(mux, configuredCORSOrigins(), s.RecordHTTPStatus)

	server := &http.Server{
		Addr:              *addr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	server.RegisterOnShutdown(func() {
		mcpServer.CloseAllSessions("server_shutdown")
	})
	log.Printf("vps-commander-hub listening on %s as %s", *addr, local)
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := store.CleanupAudits(auditRetentionDays); err != nil {
				log.Printf("audit retention cleanup: %v", err)
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.LocalSessions.Cleanup()
		}
	}()
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown: %v", err)
	}
	if s.Cluster != nil {
		s.Cluster.Close()
	}
}
