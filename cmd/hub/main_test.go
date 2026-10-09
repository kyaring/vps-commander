package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSOriginAllowlistIsExactAndDefaultClosed(t *testing.T) {
	t.Setenv("VPS_COMMANDER_CORS_ORIGINS", "https://panel.example, https://admin.example, *")
	allowed := configuredCORSOrigins()
	if !originAllowed("https://panel.example", allowed) {
		t.Fatal("configured origin should be allowed")
	}
	if originAllowed("https://evil.example", allowed) {
		t.Fatal("unlisted origin must be denied")
	}
	if originAllowed("*", allowed) {
		t.Fatal("wildcard must not open the allowlist")
	}
}

func TestShouldLogRequestSkipsNoisyPathsAndNotModified(t *testing.T) {
	for _, tc := range []struct {
		path   string
		status int
		want   bool
	}{
		{"/healthz", http.StatusOK, false},
		{"/static/app.js", http.StatusOK, false},
		{"/api/v1/devices", http.StatusNotModified, false},
		{"/api/v1/devices", http.StatusOK, true},
	} {
		if got := shouldLogRequest(tc.path, tc.status); got != tc.want {
			t.Errorf("shouldLogRequest(%q,%d)=%v want %v", tc.path, tc.status, got, tc.want)
		}
	}
}

func TestCORSPreflightAndActualResponseUseAllowlist(t *testing.T) {
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := withRequestLoggingAndCORS(mux, map[string]struct{}{"https://allowed.example": {}})

	allowed := httptest.NewRequest(http.MethodOptions, "/api/v1/devices", nil)
	allowed.Header.Set("Origin", "https://allowed.example")
	allowedW := httptest.NewRecorder()
	h.ServeHTTP(allowedW, allowed)
	if allowedW.Code != http.StatusNoContent || allowedW.Header().Get("Access-Control-Allow-Origin") != "https://allowed.example" {
		t.Fatalf("allowed preflight status=%d ACAO=%q", allowedW.Code, allowedW.Header().Get("Access-Control-Allow-Origin"))
	}

	denied := httptest.NewRequest(http.MethodOptions, "/api/v1/devices", nil)
	denied.Header.Set("Origin", "https://unlisted.example")
	deniedW := httptest.NewRecorder()
	h.ServeHTTP(deniedW, denied)
	if deniedW.Code != http.StatusForbidden || deniedW.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("denied preflight status=%d ACAO=%q", deniedW.Code, deniedW.Header().Get("Access-Control-Allow-Origin"))
	}

	actual := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	actual.Header.Set("Origin", "https://unlisted.example")
	actualW := httptest.NewRecorder()
	h.ServeHTTP(actualW, actual)
	if actualW.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origin got ACAO=%q", actualW.Header().Get("Access-Control-Allow-Origin"))
	}
}
