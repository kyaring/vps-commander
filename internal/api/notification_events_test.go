package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func emitEventRequest(t *testing.T, h http.Handler, body, tokenID string, scopes map[string]bool, idempotency string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", ID: tokenID, TokenID: tokenID, Name: tokenID, Scopes: scopes}))
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestExternalEventAPIIdempotencyRoutingAndSeverityGate(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "events-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(storage.NotificationChannel{ID: "event-hook", Name: "Event webhook", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(storage.NotificationTask{ID: "event-task", Name: "Development event", ChannelID: "event-hook", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["development.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store}
	h := s.Routes()
	scopes := map[string]bool{"events:emit": true}
	assertAudit := func(action string, code int, wantResult string) {
		t.Helper()
		rows := auditRows(t, store)
		for _, row := range rows {
			if row.Action == action && row.ExitCode == code {
				if row.Result != wantResult {
					t.Fatalf("audit action=%q exit_code=%d result=%q, want %q", action, code, row.Result, wantResult)
				}
				return
			}
		}
		t.Fatalf("missing audit row action=%q exit_code=%d in %#v", action, code, rows)
	}
	body := `{"template_id":"development-completed","variables":{"task":"AM","commit":"abc123","summary":"tests passed"}}`
	first := emitEventRequest(t, h, body, "token-events", scopes, "dev-job-42")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first emit status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResult map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if firstResult["matched_tasks"] != float64(1) || firstResult["deliveries_created"] != float64(1) {
		t.Fatalf("first result=%v", firstResult)
	}
	assertAudit("notification_event_emit", http.StatusAccepted, "ok")
	firstID, _ := firstResult["event_id"].(string)
	if firstID == "" {
		t.Fatalf("missing event id: %v", firstResult)
	}
	second := emitEventRequest(t, h, body, "token-events", scopes, "dev-job-42")
	if second.Code != http.StatusOK {
		t.Fatalf("idempotent replay status=%d body=%s", second.Code, second.Body.String())
	}
	var secondResult map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &secondResult); err != nil {
		t.Fatal(err)
	}
	if secondResult["event_id"] != firstID || secondResult["replayed"] != true {
		t.Fatalf("replay result=%v", secondResult)
	}
	assertAudit("notification_event_emit", http.StatusOK, "ok")
	changed := `{"template_id":"development-completed","variables":{"task":"AM","commit":"abc123","summary":"different payload"}}`
	conflict := emitEventRequest(t, h, changed, "token-events", scopes, "dev-job-42")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	p1 := `{"template_id":"development-completed","severity":"P1","variables":{"task":"AM","commit":"abc123","summary":"urgent"}}`
	denied := emitEventRequest(t, h, p1, "token-events", scopes, "urgent-job")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-admin P1 status=%d body=%s", denied.Code, denied.Body.String())
	}
	unknown := `{"template_id":"development-completed","variables":{"task":"AM","commit":"abc123","summary":"ok","other":"not allowed"}}`
	badVar := emitEventRequest(t, h, unknown, "token-events", scopes, "")
	if badVar.Code != http.StatusBadRequest {
		t.Fatalf("undeclared variable status=%d body=%s", badVar.Code, badVar.Body.String())
	}
	assertAudit("notification_event_emit_rejected", http.StatusBadRequest, "error")
	var events, deliveries int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if events != 1 || deliveries != 1 {
		t.Fatalf("event rows after replay/conflict/denial: events=%d deliveries=%d", events, deliveries)
	}
}

func TestExternalEventAPIRequiresScopeAndRateLimitsPerToken(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "events-rate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &Server{Store: store}
	h := s.Routes()
	noScope := emitEventRequest(t, h, `{}`, "token-no-scope", map[string]bool{"devices:read": true}, "")
	if noScope.Code != http.StatusForbidden {
		t.Fatalf("no-scope status=%d body=%s", noScope.Code, noScope.Body.String())
	}
	for i := 0; i < eventEmitMaxPerWindow; i++ {
		rec := emitEventRequest(t, h, `{}`, "token-rate", map[string]bool{"events:emit": true}, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d before limit status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	limited := emitEventRequest(t, h, `{}`, "token-rate", map[string]bool{"events:emit": true}, "")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit status=%d retry-after=%q body=%s", limited.Code, limited.Header().Get("Retry-After"), limited.Body.String())
	}
}

func TestExternalEventIdempotencyReplaySurvivesTemplateDisable(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "events-replay-template.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(storage.NotificationChannel{ID: "replay-hook", Name: "Replay hook", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(storage.NotificationTask{ID: "replay-task", Name: "Replay task", ChannelID: "replay-hook", TemplateID: "development-completed", Enabled: true, EventTypesJSON: `["development.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Store: store}).Routes()
	body := `{"template_id":"development-completed","variables":{"task":"task","commit":"abc","summary":"done"}}`
	first := emitEventRequest(t, h, body, "replay-token", map[string]bool{"events:emit": true}, "replay-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResult map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableNotificationTemplate("development-completed"); err != nil {
		t.Fatal(err)
	}
	replay := emitEventRequest(t, h, body, "replay-token", map[string]bool{"events:emit": true}, "replay-key")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay after template disable status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayResult map[string]any
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResult); err != nil {
		t.Fatal(err)
	}
	if replayResult["event_id"] != firstResult["event_id"] || replayResult["replayed"] != true {
		t.Fatalf("replay result=%v first=%v", replayResult, firstResult)
	}
	var events int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("events after replay=%d, want 1", events)
	}
}

func TestEventsEmitIsAcceptedAsAPITokenScope(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "events-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureAPITokens(""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAPIToken(storage.APIToken{ID: "token-events-scope", Name: "events-scope-token", Prefix: "events123", TokenHash: storage.HashAPIToken("vpc_events123_testsecret"), ScopesJSON: `["devices:read"]`, DevicesJSON: "[]", RateRPS: 10, MaxSessions: 10, CreatedAt: 1, CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Store: store}).Routes()
	updated := notificationAPIRequest(t, h, http.MethodPatch, "/api/v1/tokens/token-events-scope", `{"scopes":["events:emit"]}`, "system", nil)
	if updated.Code != http.StatusOK {
		t.Fatalf("update events:emit scope status=%d body=%s", updated.Code, updated.Body.String())
	}
	token, err := store.GetAPITokenByPrefix("events123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(token.ScopesJSON, "events:emit") {
		t.Fatalf("events:emit scope not saved: %s", token.ScopesJSON)
	}
	bad := notificationAPIRequest(t, h, http.MethodPatch, "/api/v1/tokens/token-events-scope", `{"scopes":["events:admin"]}`, "system", nil)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown scope status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestNotificationDeliveryHistoryAndManualRetryAPI(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "delivery-history-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveNotificationChannel(storage.NotificationChannel{ID: "history-hook", Name: "History", Type: "webhook", Endpoint: "https://example.invalid", ConfigJSON: "{}", Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotificationTask(storage.NotificationTask{ID: "history-task", Name: "History task", ChannelID: "history-hook", TemplateID: "review-completed", Enabled: true, EventTypesJSON: `["review.completed"]`, SeveritiesJSON: `["P3"]`, PolicyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	event := storage.NotificationEvent{ID: "evt-history", TemplateID: "review-completed", RequestHash: "history", EventType: "review.completed", Severity: "P3", Source: "test", Message: "failed", DataJSON: "{}", CreatedAt: time.Now().Unix()}
	if _, matched, _, err := store.AcceptNotificationEvent(event); err != nil || matched != 1 {
		t.Fatalf("accept matched=%d err=%v", matched, err)
	}
	jobs, err := store.ClaimNotificationDeliveries(time.Now().Unix()+1, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim jobs=%d err=%v", len(jobs), err)
	}
	if err := store.CompleteNotificationDelivery(jobs[0].Delivery.ID, jobs[0].Channel.ID, "failed", 500, "webhook http 500", 0, 0); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Store: store}).Routes()
	list := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/deliveries?limit=50", "", "system", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "webhook http 500") {
		t.Fatalf("delivery list status=%d body=%s", list.Code, list.Body.String())
	}
	attempts := notificationAPIRequest(t, h, http.MethodGet, "/api/v1/notifications/deliveries/attempts?id="+jobs[0].Delivery.ID, "", "system", nil)
	if attempts.Code != http.StatusOK || !strings.Contains(attempts.Body.String(), `"outcome":"failed"`) {
		t.Fatalf("attempt history status=%d body=%s", attempts.Code, attempts.Body.String())
	}
	retryBody, _ := json.Marshal(map[string]string{"id": jobs[0].Delivery.ID})
	retry := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/deliveries/retry", string(retryBody), "system", nil)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	if got := retry.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("retry content type=%q want application/json", got)
	}
	var auditExit int
	var auditResult string
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := store.DB.QueryRow(`SELECT exit_code,result FROM audit_logs WHERE action='notification_delivery_retry' ORDER BY id DESC LIMIT 1`).Scan(&auditExit, &auditResult)
		if err == nil {
			break
		}
		if err != sql.ErrNoRows || time.Now().After(deadline) {
			t.Fatalf("read retry audit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if auditExit != 0 || auditResult != "ok" {
		t.Fatalf("successful retry audit exit_code=%d result=%q", auditExit, auditResult)
	}
	retryAgain := notificationAPIRequest(t, h, http.MethodPost, "/api/v1/notifications/deliveries/retry", string(retryBody), "system", nil)
	if retryAgain.Code != http.StatusBadRequest {
		t.Fatalf("second retry while pending status=%d body=%s", retryAgain.Code, retryAgain.Body.String())
	}
	items, err := store.ListNotificationDeliveries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "pending" || items[0].AttemptCount != 1 {
		t.Fatalf("delivery after retry=%+v", items)
	}
}
