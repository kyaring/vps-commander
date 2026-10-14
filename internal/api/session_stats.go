package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wjyhk/vps-commander/internal/auth"
)

const maxSessionStatsResponseBytes = 1536

func sessionBudgetPrincipalID(p auth.Principal) string {
	if p.TokenID != "" {
		return p.TokenID
	}
	if p.ID != "" {
		return p.Type + ":" + p.ID
	}
	return ""
}

func (s *Server) sessionStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeSessionStatsError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeSessionStatsError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.MCP == nil {
		writeSessionStatsError(w, http.StatusServiceUnavailable, "session stats unavailable")
		return
	}
	tokenID := sessionBudgetPrincipalID(principal)
	if tokenID == "" {
		writeSessionStatsError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	stats, found := s.MCP.SessionBudgetStats(tokenID, sessionID, r.RemoteAddr)
	if !found {
		// Do not reveal whether an unknown MCP session belongs to another token.
		writeSessionStatsError(w, http.StatusNotFound, "session not found")
		return
	}
	body, err := json.Marshal(stats)
	if err != nil || len(body) > maxSessionStatsResponseBytes {
		writeSessionStatsError(w, http.StatusInternalServerError, "session stats response unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeSessionStatsError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

type sessionBudgetCaptureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newSessionBudgetCaptureWriter() *sessionBudgetCaptureWriter {
	return &sessionBudgetCaptureWriter{header: make(http.Header)}
}

func (w *sessionBudgetCaptureWriter) Header() http.Header { return w.header }

func (w *sessionBudgetCaptureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *sessionBudgetCaptureWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func appendBudgetWarning(body []byte, warning string) []byte {
	if warning == "" || len(body) == 0 {
		return body
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return body
	}
	if _, exists := object["budget_warning"]; exists {
		return body
	}
	encodedWarning, err := json.Marshal(warning)
	if err != nil {
		return body
	}
	object["budget_warning"] = encodedWarning
	updated, err := json.Marshal(object)
	if err != nil {
		return body
	}
	return updated
}

func copySessionBudgetResponse(dst http.ResponseWriter, src *sessionBudgetCaptureWriter, body []byte) {
	for key, values := range src.header {
		for _, value := range values {
			dst.Header().Add(key, value)
		}
	}
	status := src.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(body)
}

// sessionBudgetMiddleware accounts authenticated REST API response bodies.
// It excludes the stats endpoint itself to avoid self-counting and recursion.
func (s *Server) sessionBudgetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1/session/stats" || s.MCP == nil {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		tokenID := ""
		if ok {
			tokenID = sessionBudgetPrincipalID(principal)
		}
		if tokenID == "" {
			next.ServeHTTP(w, r)
			return
		}

		sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
		warning := s.MCP.SessionBudgetWarning(tokenID, sessionID, r.RemoteAddr)
		capture := newSessionBudgetCaptureWriter()
		next.ServeHTTP(capture, r)
		originalBody := capture.body.Bytes()
		body := appendBudgetWarning(originalBody, warning)
		if !bytes.Equal(body, originalBody) {
			capture.header.Del("Content-Length")
			capture.header.Del("ETag")
			capture.header.Del("Content-Md5")
		}
		toolName := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
		if toolName == "" {
			toolName = "http"
		}
		s.MCP.RecordSessionResponse(tokenID, sessionID, r.RemoteAddr, toolName, len(body))
		copySessionBudgetResponse(w, capture, body)
	})
}
