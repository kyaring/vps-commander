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
	clusterSecret := os.Getenv("VPS_COMMANDER_CLUSTER_SECRET")
	webPassword := os.Getenv("VPS_COMMANDER_WEB_PASSWORD")
	adminToken := os.Getenv("VPS_COMMANDER_ADMIN_TOKEN")
	installCommand := os.Getenv("VPS_COMMANDER_INSTALL_COMMAND")
	if adminToken == "" {
		adminToken = webPassword
	}
	if apiKey == "" || webPassword == "" {
		log.Fatal("VPS_COMMANDER_API_KEY and VPS_COMMANDER_WEB_PASSWORD are required via environment")
	}
	authManager := auth.NewManager(apiKey)
	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.DB.Close()
	local := "local"
	if h, err := os.Hostname(); err == nil && h != "" {
		local = h
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
	if err := s.LoadSecurityPolicySnapshot(); err != nil {
		log.Fatalf("load security policy snapshot: %v", err)
	}

	// MCP Server instance
	mcpServer := mcp.NewServer(s.NewMCPBackend(), authManager)

	protected := auth.Middleware{Manager: authManager}.Handler(s.Routes())
	mux := http.NewServeMux()
	mux.Handle("/api/", protected)

	// MCP Endpoints
	// Flexible Auth: allows optional token on SSE connect, but enforces token on Message calls
	mux.HandleFunc("/mcp/sse", mcpServer.HandleSSE)
	mux.Handle("/mcp/message", auth.Middleware{Manager: authManager, Optional: true}.Handler(http.HandlerFunc(mcpServer.HandleMessage)))

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
}
