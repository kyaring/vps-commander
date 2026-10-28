package mcp

import (
	"net/http"
	"strings"
	"testing"
)

func TestOAuthRejectsDevTaskRun(t *testing.T) {
	if _, err := parseOAuthScopes("exec devtask:run"); err == nil || !strings.Contains(err.Error(), "devtask:run") {
		t.Fatalf("devtask:run requested: err = %v, want refusal", err)
	}
}

func TestOAuthGrantsSubmitReadCancel(t *testing.T) {
	got, err := parseOAuthScopes("devtask:submit devtask:read devtask:cancel")
	if err != nil || len(got) != 3 {
		t.Fatalf("submit/read/cancel: %v, %v", got, err)
	}
}

func TestOAuthDefaultScopesExcludeDevTask(t *testing.T) {
	for _, sc := range oauthDefaultScopes {
		if strings.HasPrefix(sc, "devtask:") {
			t.Fatalf("default scope set contains %s", sc)
		}
	}
	found := false
	for _, sc := range oauthSupportedScopes {
		if sc == "devtask:run" {
			t.Fatal("devtask:run is in the supported scope list")
		}
		if sc == "devtask:submit" {
			found = true
		}
	}
	if !found {
		t.Fatal("devtask:submit should be a supported (requestable) scope")
	}
}

// TestOAuthAuthorizeRejectsDevTaskRunScope hits the authorize endpoint (not only parseOAuthScopes).
func TestOAuthAuthorizeRejectsDevTaskRunScope(t *testing.T) {
	srv, _, clientID, _, redirect, verifierChallenge := oauthFlowFixture(t)
	challenge := strings.SplitN(verifierChallenge, "|", 2)[1]
	q := authorizeDefaultsQuery(clientID, redirect, challenge)
	q.Set("resource", "https://hub.example.test/mcp")
	q.Set("scope", "exec devtask:run")
	bad := authorizeDefaultsRequest(t, srv, q)
	if bad.Code != http.StatusFound || !strings.Contains(bad.Header().Get("Location"), "error=invalid_scope") {
		t.Fatalf("devtask:run via authorize should be invalid_scope: status=%d loc=%s body=%s",
			bad.Code, bad.Header().Get("Location"), bad.Body.String())
	}
}
