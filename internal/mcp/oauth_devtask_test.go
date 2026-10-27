package mcp

import (
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
