package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testRunner struct{}

func (testRunner) DevicesJSON(w http.ResponseWriter, r *http.Request)  {}
func (testRunner) AuditsJSON(w http.ResponseWriter, r *http.Request)   {}
func (testRunner) PanelExec(w http.ResponseWriter, r *http.Request)    {}
func (testRunner) RotateAPIKey(w http.ResponseWriter, r *http.Request) {}
func (testRunner) DeleteDevice(w http.ResponseWriter, r *http.Request) {}

func TestStaticCSSContentType(t *testing.T) {
	p := New("test-password", "admin", testRunner{})
	req := httptest.NewRequest("GET", "/static/app.css", nil)
	rec := httptest.NewRecorder()

	p.static(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("content-type=%q, want text/css; charset=utf-8", got)
	}
	if strings.TrimSpace(rec.Body.String()) == "" {
		t.Fatal("CSS body is empty")
	}
}
