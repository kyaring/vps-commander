package auth

import "testing"

func TestDevTaskScopesAdminImplication(t *testing.T) {
	admin := Principal{Type: "api_token", Scopes: map[string]bool{"admin": true}}
	for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel", "devtask:run"} {
		if !admin.HasScope(sc) {
			t.Fatalf("admin lacks %s", sc)
		}
	}
	if admin.HasScope("exec") != false {
		t.Fatal("admin must not imply non-devtask scopes")
	}
	user := Principal{Type: "api_token", Scopes: map[string]bool{"devtask:submit": true}}
	if !user.HasScope("devtask:submit") || user.HasScope("devtask:run") || user.HasScope("devtask:read") {
		t.Fatal("token scopes must not leak to other devtask scopes")
	}
}

func TestDevTaskScopesNotInLegacySet(t *testing.T) {
	legacy := Principal{Type: "legacy", Scopes: map[string]bool{"devices:read": true, "files:read": true, "files:write": true, "exec": true, "process": true, "mcp:call": true, "admin": true}}
	for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel", "devtask:run"} {
		if legacy.Scopes[sc] {
			t.Fatalf("legacy scope set contains %s", sc)
		}
	}
}
