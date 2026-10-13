package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"io"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func taskReportedRequest(t *testing.T, h http.Handler, body, tokenID, name, source, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", ID: tokenID, TokenID: tokenID, Name: name, Scopes: taskReportedScopes(tokenID)}))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if source != "" {
		var bodyMap map[string]any
		if err := json.Unmarshal([]byte(body), &bodyMap); err == nil {
			bodyMap["source"] = source
			encoded, err := json.Marshal(bodyMap)
			if err != nil {
				t.Fatal(err)
			}
			req.Body = io.NopCloser(strings.NewReader(string(encoded)))
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func taskReportedScopes(tokenID string) map[string]bool {
	scopes := map[string]bool{"events:emit": true}
	if strings.HasSuffix(tokenID, "-admin") {
		scopes["admin"] = true
	}
	return scopes
}

func taskReportedFixture(t *testing.T) (*storage.Store, http.Handler) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "task-reported-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, (&Server{Store: store}).Routes()
}

func taskReportedStoredData(t *testing.T, store *storage.Store, id string) (string, string, string) {
	t.Helper()
	var message, dataJSON, severity string
	if err := store.DB.QueryRow(`SELECT message,data_json,severity FROM notification_events WHERE id=?`, id).Scan(&message, &dataJSON, &severity); err != nil {
		t.Fatal(err)
	}
	return message, dataJSON, severity
}

func TestTaskReportedDoneDefaultsAndIdempotency(t *testing.T) {
	store, h := taskReportedFixture(t)
	body := `{"template_id":"task-reported","variables":{"task":"T-10","summary":"tests passed","employee":"SWS-E002-gpt","project":"vps-commander","type":"开发","status":"done"}}`
	first := taskReportedRequest(t, h, body, "token-task", "SWS-E002-gpt", "", "task:T-10:done:abc")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	id, _ := result["event_id"].(string)
	message, dataJSON, severity := taskReportedStoredData(t, store, id)
	if !strings.HasPrefix(message, "【SWS-E002-gpt】开发完成：vps-commander\n") || severity != "P3" {
		t.Fatalf("message=%q severity=%s", message, severity)
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		t.Fatal(err)
	}
	if data["status"] != "完成" || data["ref"] != "-" {
		t.Fatalf("stored variables=%v", data)
	}
	replay := taskReportedRequest(t, h, body, "token-task", "SWS-E002-gpt", "", "task:T-10:done:abc")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayResult map[string]any
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResult); err != nil {
		t.Fatal(err)
	}
	if replayResult["event_id"] != id || replayResult["replayed"] != true {
		t.Fatalf("replay result=%v first=%v", replayResult, result)
	}
}

func TestTaskReportedBlockedSeverityAndExplicitOverride(t *testing.T) {
	store, h := taskReportedFixture(t)
	for _, tc := range []struct{ severity, want string }{{"", "P2"}, {"P1", "P1"}} {
		body := fmt.Sprintf(`{"template_id":"task-reported","severity":%q,"variables":{"task":"T-10","summary":"受阻：等待 Hark 修复","status":"blocked","type":"开发"}}`, tc.severity)
		rec := taskReportedRequest(t, h, body, map[bool]string{true: "token-blocked-admin", false: "token-blocked"}[tc.severity == "P1"], "SWS-E002-gpt", "", "")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("severity %q status=%d body=%s", tc.severity, rec.Code, rec.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		id := result["event_id"].(string)
		message, dataJSON, severity := taskReportedStoredData(t, store, id)
		if severity != tc.want || !strings.HasPrefix(message, "【SWS-E002-gpt】开发受阻：-\n") {
			t.Fatalf("severity=%s message=%q want=%s", severity, message, tc.want)
		}
		var data map[string]string
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			t.Fatal(err)
		}
		if data["status"] != "受阻" || data["employee"] != "SWS-E002-gpt" || data["project"] != "-" || data["ref"] != "-" || data["type"] != "开发" {
			t.Fatalf("stored variables=%v", data)
		}
	}
}

func TestTaskReportedFailedDefaultsToP2(t *testing.T) {
	store, h := taskReportedFixture(t)
	body := `{"template_id":"task-reported","variables":{"task":"T-10","summary":"失败：测试未通过","status":"failed","type":"测试"}}`
	rec := taskReportedRequest(t, h, body, "token-failed", "SWS-E002-gpt", "", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	message, dataJSON, severity := taskReportedStoredData(t, store, result["event_id"].(string))
	if severity != "P2" || !strings.HasPrefix(message, "【SWS-E002-gpt】测试失败：-\n") {
		t.Fatalf("severity=%s message=%q", severity, message)
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		t.Fatal(err)
	}
	if data["status"] != "失败" {
		t.Fatalf("stored status=%q", data["status"])
	}
}

func TestTaskReportedValidationAndNameFallback(t *testing.T) {
	store, h := taskReportedFixture(t)
	valid := func(vars map[string]string) string {
		b, _ := json.Marshal(map[string]any{"template_id": "task-reported", "variables": vars})
		return string(b)
	}
	for _, status := range []string{"", "ok"} {
		rec := taskReportedRequest(t, h, valid(map[string]string{"task": "t", "summary": "s", "status": status}), "token-invalid", "SWS-E002-gpt", "", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %q accepted: %d %s", status, rec.Code, rec.Body.String())
		}
	}
	for _, tc := range []struct{ name, value string }{
		{"employee", strings.Repeat("a", 33)}, {"employee", "bad\nname"}, {"employee", "【bad"}, {"project", strings.Repeat("界", 33)}, {"type", strings.Repeat("界", 17)}, {"ref", "bad】ref"}, {"ref", strings.Repeat("r", 65)}, {"project", "bad\tproject"},
	} {
		vars := map[string]string{"task": "t", "summary": "s", "status": "done", tc.name: tc.value}
		rec := taskReportedRequest(t, h, valid(vars), "token-invalid-field", "SWS-E002-gpt", "", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s=%q accepted: %d %s", tc.name, tc.value, rec.Code, rec.Body.String())
		}
	}
	vars := map[string]string{"task": "t", "summary": "s", "status": "done", "employee": strings.Repeat("界", 32)}
	rec := taskReportedRequest(t, h, valid(vars), "token-max", "SWS-E002-gpt", "", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("32-rune employee rejected: %d %s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	_, dataJSON, _ := taskReportedStoredData(t, store, result["event_id"].(string))
	var data map[string]string
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		t.Fatal(err)
	}
	if data["project"] != "-" || data["ref"] != "-" || data["type"] != "任务" {
		t.Fatalf("defaults=%v", data)
	}

	body := valid(map[string]string{"task": "t", "summary": "s", "status": "done"})
	fallback := taskReportedRequest(t, h, body, "token-name-fallback", "Named Agent", "", "")
	if fallback.Code != http.StatusAccepted {
		t.Fatalf("name fallback status=%d body=%s", fallback.Code, fallback.Body.String())
	}
	var fallbackResult map[string]any
	if err := json.Unmarshal(fallback.Body.Bytes(), &fallbackResult); err != nil {
		t.Fatal(err)
	}
	_, fallbackJSON, _ := taskReportedStoredData(t, store, fallbackResult["event_id"].(string))
	if err := json.Unmarshal([]byte(fallbackJSON), &data); err != nil {
		t.Fatal(err)
	}
	if data["employee"] != "Named Agent" {
		t.Fatalf("employee fallback=%q", data["employee"])
	}

	anonymous := taskReportedRequest(t, h, body, "token-empty-name", "", "source-agent", "")
	if anonymous.Code != http.StatusAccepted {
		t.Fatalf("source fallback status=%d body=%s", anonymous.Code, anonymous.Body.String())
	}
	var anonResult map[string]any
	if err := json.Unmarshal(anonymous.Body.Bytes(), &anonResult); err != nil {
		t.Fatal(err)
	}
	_, anonJSON, _ := taskReportedStoredData(t, store, anonResult["event_id"].(string))
	if err := json.Unmarshal([]byte(anonJSON), &data); err != nil {
		t.Fatal(err)
	}
	if data["employee"] != "source-agent" {
		t.Fatalf("source fallback=%q", data["employee"])
	}
}

func TestTaskReportedRequiredFieldsAndExistingTemplatesUnaffected(t *testing.T) {
	_, h := taskReportedFixture(t)
	for _, vars := range []map[string]string{{"summary": "s", "status": "done"}, {"task": "t", "status": "done"}} {
		b, _ := json.Marshal(map[string]any{"template_id": "task-reported", "variables": vars})
		rec := taskReportedRequest(t, h, string(b), "token-required", "agent", "", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("missing required field accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
	dev := taskReportedRequest(t, h, `{"template_id":"development-completed","variables":{"task":"t","commit":"abc","summary":"s","employee":"SWS-E002-gpt"}}`, "token-existing", "agent", "", "")
	if dev.Code != http.StatusBadRequest {
		t.Fatalf("development-completed accepted undeclared employee: %d %s", dev.Code, dev.Body.String())
	}
	review := taskReportedRequest(t, h, `{"template_id":"review-completed","variables":{"result":"PASS","commit":"abc","summary":"s"}}`, "token-review", "agent", "", "")
	if review.Code != http.StatusAccepted {
		t.Fatalf("review-completed changed: %d %s", review.Code, review.Body.String())
	}
}

func TestTaskReportedHashUsesUnfilledVariables(t *testing.T) {
	store, h := taskReportedFixture(t)
	body := `{"template_id":"task-reported","variables":{"task":"t","summary":"s","status":"done"}}`
	first := taskReportedRequest(t, h, body, "token-hash", "Agent Name", "", "same-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	replay := taskReportedRequest(t, h, body, "token-hash", "Different Name", "", "same-key")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var a, b map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if a["event_id"] != b["event_id"] {
		t.Fatalf("replay ids differ: %v %v", a, b)
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE template_id='task-reported'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored events=%d", count)
	}
}
