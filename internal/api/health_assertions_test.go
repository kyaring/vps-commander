package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func healthAssertionTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "health-assertions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func passingHealthProbes() healthAssertionProbes {
	return healthAssertionProbes{
		serviceState: func() (bool, int, error) { return true, 0, nil },
		internalHTTP: func() error { return nil },
		publicHTTP:   func() error { return nil },
	}
}

func TestHealthAssertionsAllPassAndBounded(t *testing.T) {
	store := healthAssertionTestStore(t)
	s := &Server{Store: store}
	result := s.collectHealthAssertions(passingHealthProbes())
	if len(result.Assertions) != 9 || len(result.Assertions) > healthAssertionsMaxCount {
		t.Fatalf("assertion count=%d", len(result.Assertions))
	}
	if result.Summary.Pass != 9 || result.Summary.Fail != 0 {
		t.Fatalf("summary=%+v", result.Summary)
	}
	for _, item := range result.Assertions {
		if !item.OK {
			t.Errorf("%s failed unexpectedly: %s", item.ID, item.Detail)
		}
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > healthAssertionsMaxBytes {
		t.Fatalf("response size=%d, limit=%d", len(body), healthAssertionsMaxBytes)
	}
}

func TestHealthAssertionsNoPlaintextFailsWithoutLeakingValue(t *testing.T) {
	store := healthAssertionTestStore(t)
	const secretURL = "https://secret.example.invalid/private-hook-should-not-appear"
	if _, err := store.DB.Exec(`INSERT INTO notification_channels(id,name,type,endpoint,config_json,enabled,created_at,updated_at) VALUES('test-channel','test','webhook',?,'{}',1,1,1)`, secretURL); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store}
	result := s.collectHealthAssertions(passingHealthProbes())
	var found bool
	for _, item := range result.Assertions {
		if item.ID == "notification.no_plaintext" {
			found = true
			if item.OK {
				t.Fatal("notification.no_plaintext passed with a configured endpoint")
			}
		}
	}
	if !found {
		t.Fatal("notification.no_plaintext assertion missing")
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secretURL) || strings.Contains(string(body), "private-hook") {
		t.Fatal("response leaked endpoint value")
	}
}

func TestHealthAssertionsOneProbeFailureDoesNotAbortCollection(t *testing.T) {
	store := healthAssertionTestStore(t)
	probes := passingHealthProbes()
	probes.publicHTTP = func() error { return errors.New("sensitive internal error text") }
	result := (&Server{Store: store}).collectHealthAssertions(probes)
	if result.Summary.Fail != 1 || result.Summary.Pass != 8 {
		t.Fatalf("summary=%+v", result.Summary)
	}
	if result.Assertions[3].ID != "hub.healthz_public" || result.Assertions[3].OK || result.Assertions[3].Detail != "public health check unavailable" {
		t.Fatalf("unexpected public health assertion: %+v", result.Assertions[3])
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "sensitive internal error text") {
		t.Fatal("raw probe error leaked")
	}
}

func TestHealthAssertionsAuthorizationAndReadOnlyMethod(t *testing.T) {
	s := &Server{Store: healthAssertionTestStore(t)}
	h := s.Routes()
	request := func(method string, scopes map[string]bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/health/assertions", nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Type: "api_token", ID: "test", Scopes: scopes}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if got := request(http.MethodGet, map[string]bool{"events:emit": true}); got.Code != http.StatusForbidden {
		t.Fatalf("wrong scope status=%d", got.Code)
	}
	if got := request(http.MethodPost, map[string]bool{"devices:read": true}); got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", got.Code)
	}
	for _, scope := range []string{"devices:read", "admin"} {
		got := request(http.MethodGet, map[string]bool{scope: true})
		if got.Code != http.StatusOK {
			t.Fatalf("scope %q status=%d body=%s", scope, got.Code, got.Body.String())
		}
		if got.Body.Len() > healthAssertionsMaxBytes {
			t.Fatalf("route response size=%d", got.Body.Len())
		}
	}
}

func TestHealthAssertionsIntegrityCheckIsCached(t *testing.T) {
	store := healthAssertionTestStore(t)
	s := &Server{Store: store}
	ok, detail := s.cachedIntegrityCheck()
	if !ok || detail != "ok" {
		t.Fatalf("integrity=(%t,%q)", ok, detail)
	}
	// A closed database proves the second read is served from the fresh cache.
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	checkedAt := s.healthIntegrityAt
	ok, detail = s.cachedIntegrityCheck()
	if !ok || detail != "ok" || s.healthIntegrityAt != checkedAt {
		t.Fatalf("cached integrity=(%t,%q), timestamp refreshed=%t", ok, detail, s.healthIntegrityAt != checkedAt)
	}
}
