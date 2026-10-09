package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
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

func main() {
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
		return auth.Principal{Type: "api_token", ID: id, Name: name, TokenID: id, Scopes: scopes, Devices: devices, MaxSessions: t.MaxSessions}, true
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
	s := &api.Server{
		Auth:           authManager,
		Exec:           executor.Local{MaxOutput: 1024 * 1024},
		Cluster:        cluster.NewManager(clusterSecret, store),
		Store:          store,
		LocalName:      local,
		Notify:         notificationManager,
		AdminToken:     adminToken,
		InstallCommand: installCommand,
		ExecLimiter:    make(chan struct{}, 32),
		SearchLimiter:  make(chan struct{}, 8),
		LocalSessions:  executor.NewSessionManager(0),
	}
	s.Cluster.SetProfileInterval(profileInterval)
	log.Printf("agent profile interval: %s", s.Cluster.ProfileInterval)
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		log.Fatalf("load security policy snapshot: %v", err)
	}

	// MCP Server instance
	mcpServer := mcp.NewServer(s.NewMCPBackend(), authManager)
	mcpServer.SetLocalName(local)
	s.MCP = mcpServer

	protected := auth.Middleware{Manager: authManager}.Handler(s.Routes())
	mux := http.NewServeMux()
	mux.Handle("/api/", protected)

	// MCP Endpoints
	// Flexible Auth: allows optional token on SSE connect, but enforces token on Message calls
	mux.Handle("/mcp", auth.Middleware{Manager: authManager}.Handler(http.HandlerFunc(mcpServer.HandleStreamableHTTP)))
	mux.Handle("/mcp/sse", auth.Middleware{Manager: authManager}.Handler(http.HandlerFunc(mcpServer.HandleSSE)))
	mux.Handle("/mcp/message", auth.Middleware{Manager: authManager}.Handler(http.HandlerFunc(mcpServer.HandleMessage)))

	// OAuth Endpoints (For OpenAI OAuth auto-discovery)
	mux.HandleFunc("/.well-known/oauth-authorization-server", mcpServer.HandleOAuthMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", mcpServer.HandleOAuthMetadata)
	mux.HandleFunc("/oauth/authorize", mcpServer.HandleAuthorize)
	mux.HandleFunc("/oauth/token", mcpServer.HandleToken)

	mux.HandleFunc("/healthz", s.Routes().ServeHTTP)
	mux.HandleFunc("/openapi.json", s.Routes().ServeHTTP)
	mux.HandleFunc("/agent/ws", s.Routes().ServeHTTP)
	panel := s.Panel(webPassword)
	mux.Handle("/", panel)

	// Wrap root with logger and CORS
	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[REQ] %s %s from %s (Auth: %v)", r.Method, r.URL.String(), r.RemoteAddr, r.Header.Get("Authorization") != "")
		// Handle CORS Preflight
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		mux.ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:              *addr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
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
