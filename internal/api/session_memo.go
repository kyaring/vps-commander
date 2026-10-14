package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/mcp"
)

func (s *Server) sessionMemo(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeSessionMemoError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	tokenID := sessionBudgetPrincipalID(principal)
	if tokenID == "" {
		writeSessionMemoError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.MCP == nil {
		writeSessionMemoError(w, http.StatusServiceUnavailable, "session memo unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	remoteAddr := r.RemoteAddr
	switch r.Method {
	case http.MethodPost:
		var request struct {
			Key        string `json:"key"`
			Value      string `json:"value"`
			TTLSeconds *int64 `json:"ttl_seconds,omitempty"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024+1))
		if err := decoder.Decode(&request); err != nil {
			writeSessionMemoError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			writeSessionMemoError(w, http.StatusBadRequest, "request body must contain one JSON object")
			return
		}
		result, err := s.MCP.SetSessionMemo(tokenID, sessionID, remoteAddr, request.Key, request.Value, request.TTLSeconds)
		status, detail := 0, ""
		if err != nil {
			status = 1
			detail = err.Error()
		}
		s.MCP.AuditSessionMemo(r.Context(), auth.ClientIP(r), "memo_set", strings.TrimSpace(request.Key), len([]byte(request.Value)), status, detail)
		if err != nil {
			writeSessionMemoError(w, sessionMemoHTTPStatus(err), err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		jsonOut(w, result)
	case http.MethodGet:
		if r.URL.Query().Has("key") {
			key := r.URL.Query().Get("key")
			item, found, err := s.MCP.GetSessionMemo(tokenID, sessionID, remoteAddr, key)
			status, n, detail := 0, 0, ""
			if err != nil {
				status, detail = 1, err.Error()
			} else if found {
				n = item.Bytes
			} else {
				status, detail = 1, "memo not found"
			}
			s.MCP.AuditSessionMemo(r.Context(), auth.ClientIP(r), "memo_get", key, n, status, detail)
			if err != nil {
				writeSessionMemoError(w, sessionMemoHTTPStatus(err), err.Error())
				return
			}
			if !found {
				writeSessionMemoError(w, http.StatusNotFound, "memo not found")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			jsonOut(w, item)
			return
		}
		items, err := s.MCP.ListSessionMemos(tokenID, sessionID, remoteAddr)
		status, total, detail := 0, 0, ""
		if err != nil {
			status, detail = 1, err.Error()
		} else {
			for _, item := range items {
				total += item.Bytes
			}
		}
		s.MCP.AuditSessionMemo(r.Context(), auth.ClientIP(r), "memo_get", "list", total, status, detail)
		if err != nil {
			writeSessionMemoError(w, sessionMemoHTTPStatus(err), err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		jsonOut(w, map[string]any{"memos": items})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeSessionMemoError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func sessionMemoHTTPStatus(err error) int {
	var memoErr *mcp.SessionMemoError
	if errors.As(err, &memoErr) {
		return memoErr.Status
	}
	// Keep unexpected internal errors opaque to callers.
	return http.StatusInternalServerError
}

func writeSessionMemoError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
