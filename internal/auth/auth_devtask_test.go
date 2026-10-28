package auth

import "testing"

// legacyBootstrapScopes mirrors the fixed scope map used for legacy-env / env bootstrap
// principals in auth.go (must stay in sync with those literals).
var legacyBootstrapScopes = map[string]bool{
	"devices:read": true,
	"files:read":   true,
	"files:write":  true,
	"exec":         true,
	"process":      true,
	"mcp:call":     true,
	"admin":        true,
}

func TestDevTaskScopesAdminImplication(t *testing.T) {
	admin := Principal{Type: "api_token", Scopes: map[string]bool{"admin": true}}
	for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel"} {
		if !admin.HasScope(sc) {
			t.Fatalf("admin lacks %s", sc)
		}
	}
	if admin.HasScope("devtask:run") {
		t.Fatal("admin must not imply devtask:run")
	}
	if admin.HasScope("exec") {
		t.Fatal("admin must not imply non-devtask scopes")
	}
	user := Principal{Type: "api_token", Scopes: map[string]bool{"devtask:submit": true}}
	if !user.HasScope("devtask:submit") || user.HasScope("devtask:run") || user.HasScope("devtask:read") {
		t.Fatal("token scopes must not leak to other devtask scopes")
	}
}

func TestDevTaskRunNotImpliedByAdmin(t *testing.T) {
	admin := Principal{Type: "api_token", ID: "adm", Scopes: map[string]bool{"admin": true}}
	if admin.HasScope("devtask:run") {
		t.Fatal("admin HasScope(devtask:run) = true, want false")
	}
}

func TestDevTaskRunNotImpliedForLegacyAndBootstrap(t *testing.T) {
	// Same scope map as auth.go legacy-env and env bootstrap principals.
	legacy := Principal{Type: "api_token", ID: "legacy-env", Name: "legacy-env", TokenID: "legacy-env", Scopes: copyScopes(legacyBootstrapScopes)}
	bootstrap := Principal{Type: "api_token", ID: "legacy-env", Name: "legacy-env", TokenID: "legacy-env", Scopes: copyScopes(legacyBootstrapScopes)}
	for _, p := range []Principal{legacy, bootstrap} {
		if p.HasScope("devtask:run") {
			t.Fatalf("legacy/bootstrap %q HasScope(devtask:run) = true, want false", p.ID)
		}
		// admin still implies the non-run devtask scopes
		for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel"} {
			if !p.HasScope(sc) {
				t.Fatalf("legacy/bootstrap lacks implied %s", sc)
			}
		}
	}
}

func TestDevTaskRunExplicitNamedToken(t *testing.T) {
	runner := Principal{Type: "api_token", ID: "runner", Scopes: map[string]bool{"devtask:run": true}}
	if !runner.HasScope("devtask:run") {
		t.Fatal("explicit devtask:run must be granted")
	}
	if runner.HasScope("devtask:submit") {
		t.Fatal("explicit run must not imply submit")
	}
	both := Principal{Type: "api_token", Scopes: map[string]bool{"admin": true, "devtask:run": true}}
	if !both.HasScope("devtask:run") {
		t.Fatal("admin + explicit run must allow run")
	}
}

func TestDevTaskScopesNotInLegacySet(t *testing.T) {
	legacy := Principal{Type: "legacy", Scopes: copyScopes(legacyBootstrapScopes)}
	for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel", "devtask:run"} {
		if legacy.Scopes[sc] {
			t.Fatalf("legacy scope set contains %s", sc)
		}
	}
}

func copyScopes(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestDevTaskRunNotImpliedForPanelAndSystem(t *testing.T) {
	for _, typ := range []string{"panel", "system"} {
		p := Principal{Type: typ, ID: typ, Scopes: map[string]bool{}}
		if p.HasScope("devtask:run") {
			t.Fatalf("%s HasScope(devtask:run) = true, want false", typ)
		}
		// Other scopes unchanged: panel/system still pass through the type early-return.
		for _, sc := range []string{"devtask:submit", "devtask:read", "devtask:cancel", "exec", "admin", "files:read"} {
			if !p.HasScope(sc) {
				t.Fatalf("%s HasScope(%s) = false, want true (unchanged)", typ, sc)
			}
		}
	}
	// Explicit run on a non-panel principal still works.
	named := Principal{Type: "api_token", Scopes: map[string]bool{"devtask:run": true}}
	if !named.HasScope("devtask:run") {
		t.Fatal("explicit run on named token must remain true")
	}
}
