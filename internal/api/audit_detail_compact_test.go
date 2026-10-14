package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func TestAuditListCompactsLargePageAndDetailKeepsFullRecord(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	longCommand := strings.Repeat("x", 4096)
	longError := strings.Repeat("e", 1024)
	longTarget := strings.Repeat("t", 512)
	longResult := strings.Repeat("r", 768)
	for i := 0; i < 100; i++ {
		errorMsg, target, result := "", "", "ok"
		if i == 99 {
			errorMsg, target, result = longError, longTarget, longResult
		}
		// Insert legacy-sized fixtures directly so the test exercises old rows
		// larger than the current write-path command-intent safety bound.
		if _, err := store.DB.Exec(`INSERT INTO audit_logs(timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg,actor_type,actor_id,actor_name,oauth_client_id,transport,target_type,target,result,request_id,session_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			int64(i+1), "192.0.2.10", "node-a", "exec", longCommand, 0, 12, errorMsg, "api_token", "token-1", "hark-2", "", "rest", "", target, result, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Store: store}
	routes := s.Routes()
	admin := auth.Principal{Type: "api_token", ID: "admin-test", Name: "admin-test", Scopes: map[string]bool{"admin": true}}
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/audits", nil)
	listReq = listReq.WithContext(auth.WithPrincipal(listReq.Context(), admin))
	listRec := httptest.NewRecorder()
	routes.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	t.Logf("default 100-row compact response: %d bytes (limit %d)", listRec.Body.Len(), 48*1024)
	if got := listRec.Body.Len(); got > 48*1024 {
		t.Fatalf("default 100-row response is %d bytes, exceeds 48 KiB", got)
	}
	var rows []auditListRecord
	if err := json.Unmarshal(listRec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 100 {
		t.Fatalf("list returned %d rows, want 100", len(rows))
	}
	first := rows[0]
	if !first.Truncated || len(first.Command) > auditListTextLimit || !strings.HasSuffix(first.Command, "…") {
		t.Fatalf("command not compacted correctly: truncated=%v len=%d suffix=%q", first.Truncated, len(first.Command), first.Command[len(first.Command)-min(3, len(first.Command)):])
	}
	if len(first.ErrorMsg) > auditListTextLimit || len(first.Target) > auditListTextLimit || len(first.Result) > auditListTextLimit || !strings.HasSuffix(first.ErrorMsg, "…") || !strings.HasSuffix(first.Target, "…") || !strings.HasSuffix(first.Result, "…") {
		t.Fatalf("long error/target fields were not compacted: error=%d target=%d", len(first.ErrorMsg), len(first.Target))
	}

	deniedReq := httptest.NewRequest(http.MethodGet, "/api/v1/audits/"+jsonNumber(first.ID), nil)
	deniedRec := httptest.NewRecorder()
	routes.ServeHTTP(deniedRec, deniedReq)
	if deniedRec.Code != http.StatusForbidden {
		t.Fatalf("unauthorized detail status=%d, want 403", deniedRec.Code)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/audits/"+jsonNumber(first.ID), nil)
	detailReq = detailReq.WithContext(auth.WithPrincipal(detailReq.Context(), admin))
	detailRec := httptest.NewRecorder()
	routes.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}
	var detail storage.AuditLog
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Command != longCommand || detail.ErrorMsg != longError || detail.Target != longTarget || detail.Result != longResult {
		t.Fatalf("detail response was not complete: command=%d error=%d target=%d", len(detail.Command), len(detail.ErrorMsg), len(detail.Target))
	}

	csvReq := httptest.NewRequest(http.MethodGet, "/api/v1/audits/export.csv?actor=hark-2&max_rows=1", nil)
	csvReq = csvReq.WithContext(auth.WithPrincipal(csvReq.Context(), admin))
	csvRec := httptest.NewRecorder()
	routes.ServeHTTP(csvRec, csvReq)
	if csvRec.Code != http.StatusOK || !strings.Contains(csvRec.Body.String(), longCommand) {
		t.Fatalf("CSV export status=%d or full command missing (body bytes=%d)", csvRec.Code, csvRec.Body.Len())
	}
}

func jsonNumber(n int64) string { return strconv.FormatInt(n, 10) }
