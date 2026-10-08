package storage

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestAPITokenLegacyMigrationAndValidation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err := s.EnsureAPITokens("legacy-secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureAPITokens("legacy-secret"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM api_tokens").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("token count=%d, want 1", count)
	}
	id, name, scopes, devices, ok := s.ValidateAPIToken("legacy-secret", "127.0.0.1:1")
	if !ok || id != "legacy-env" || name != "legacy-env" {
		t.Fatalf("legacy validation failed: %v %s %s", ok, id, name)
	}
	if !scopes["exec"] || len(devices) != 0 {
		t.Fatalf("legacy scopes/devices invalid")
	}
	if _, _, _, _, ok := s.ValidateAPIToken("wrong", "127.0.0.1:1"); ok {
		t.Fatal("wrong token accepted")
	}
}

func TestAPITokenRevoke(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err := s.EnsureAPITokens(""); err != nil {
		t.Fatal(err)
	}
	scopesJSON, _ := json.Marshal([]string{"devices:read"})
	token := "vpc_abcdefgh_12345678901234567890123456789012"
	if err := s.CreateAPIToken(APIToken{ID: "t1", Name: "reviewer-c", Prefix: "abcdefgh", TokenHash: HashAPIToken(token), ScopesJSON: string(scopesJSON), DevicesJSON: "[]", RateRPS: 10, MaxSessions: 10, CreatedAt: 1, CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	_, _, scopes, _, ok := s.ValidateAPIToken(token, "127.0.0.1:1")
	if !ok || !scopes["devices:read"] || scopes["exec"] {
		t.Fatal("scopes not enforced")
	}
	if err := s.RevokeAPIToken("t1"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, ok := s.ValidateAPIToken(token, "127.0.0.1:1"); ok {
		t.Fatal("revoked token accepted")
	}
}
