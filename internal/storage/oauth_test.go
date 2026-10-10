package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func oauthFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnsureAPITokens(""); err != nil {
		t.Fatal(err)
	}
	scopes, _ := json.Marshal([]string{"devices:read", "mcp:call", "exec"})
	devices, _ := json.Marshal([]string{"node-1"})
	raw := "vpc_abcd1234_01234567890123456789012345678901"
	if err := s.CreateAPIToken(APIToken{ID: "api-1", Name: "desktop", Prefix: "abcd1234", TokenHash: HashAPIToken(raw), ScopesJSON: string(scopes), DevicesJSON: string(devices), RateRPS: 7, MaxSessions: 3, CreatedAt: 10, CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	uris, _ := json.Marshal([]string{"http://127.0.0.1:9876/callback"})
	if err := s.RegisterOAuthClient(OAuthClient{ID: "client-1", Name: "Test MCP", RedirectURIsJSON: string(uris), CreatedAt: 10}); err != nil {
		t.Fatal(err)
	}
	return s, raw, "http://127.0.0.1:9876/callback"
}

func TestOAuthMigrationIdempotent(t *testing.T) {
	s, _, _ := oauthFixture(t)
	if err := s.migrateOAuth(); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	for _, table := range []string{"oauth_clients", "oauth_codes", "oauth_grants", "oauth_tokens"} {
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("table %s exists=%d", table, n)
		}
	}
}

func TestOAuthAuthorizationCodeSingleUseAndBinding(t *testing.T) {
	s, _, redirect := oauthFixture(t)
	code := OAuthAuthorizationCode{CodeHash: HashAPIToken("code-raw"), ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, RedirectURI: redirect, ScopesJSON: "[\"devices:read\"]", CodeChallenge: "challenge", Resource: "https://hub.example.com/mcp", CreatedAt: 100, ExpiresAt: 160}
	if err := s.CreateOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuthCode(code.CodeHash, "other-client", redirect, 101); !errors.Is(err, ErrOAuthCodeInvalid) {
		t.Fatalf("wrong client error=%v", err)
	}
	consumed, err := s.ConsumeOAuthCode(code.CodeHash, "client-1", redirect, 101)
	if err != nil || consumed.UsedAt == nil || *consumed.UsedAt != 101 {
		t.Fatalf("consume result=%+v err=%v", consumed, err)
	}
	if _, err := s.ConsumeOAuthCode(code.CodeHash, "client-1", redirect, 102); !errors.Is(err, ErrOAuthCodeInvalid) {
		t.Fatalf("replay error=%v", err)
	}
}

func TestOAuthAccessScopeIntersectionAndRefreshReplayRevokesFamily(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(1000)
	g := OAuthGrant{ID: "grant-1", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\",\"files:read\",\"mcp:call\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	access := OAuthToken{ID: "access-1", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	refresh := OAuthToken{ID: "refresh-1", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-secret"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := s.CreateOAuthGrant(g, access, refresh); err != nil {
		t.Fatal(err)
	}
	p, ok := s.ValidateOAuthAccessToken("access-secret", "192.0.2.1", now+1)
	if !ok || !p.Scopes["devices:read"] || p.Scopes["files:read"] || !p.Scopes["mcp:call"] || p.Scopes["exec"] != false {
		t.Fatalf("access principal=%+v ok=%v; expected grant scopes intersected with current API token scopes", p, ok)
	}
	newAccess := OAuthToken{ID: "access-2", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-secret-2"), CreatedAt: now + 2, ExpiresAt: now + 3602}
	newRefresh := OAuthToken{ID: "refresh-2", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-secret-2"), CreatedAt: now + 2, ExpiresAt: g.RefreshExpiresAt}
	if _, err := s.RotateOAuthRefresh(HashAPIToken("refresh-secret"), "client-1", now+2, newAccess, newRefresh); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.RotateOAuthRefresh(HashAPIToken("refresh-secret"), "client-1", now+3,
		OAuthToken{ID: "access-3", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-secret-3"), CreatedAt: now + 3, ExpiresAt: now + 3603},
		OAuthToken{ID: "refresh-3", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-secret-3"), CreatedAt: now + 3, ExpiresAt: g.RefreshExpiresAt}); !errors.Is(err, ErrOAuthRefreshReplay) {
		t.Fatalf("refresh replay error=%v", err)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-secret-2", "192.0.2.1", now+4); ok {
		t.Fatal("access token remained valid after refresh replay revoked the grant")
	}
}

func TestOAuthRevokedAPITokenInvalidatesAccess(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(1000)
	g := OAuthGrant{ID: "grant-2", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-4", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-secret-4"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-4", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-secret-4"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := s.CreateOAuthGrant(g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAPIToken("api-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-secret-4", "192.0.2.2", now+1); ok {
		t.Fatal("OAuth access survived underlying API token revocation")
	}
}

func TestOAuthClientDisableRevokesGrantAndAccess(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(2000)
	g := OAuthGrant{ID: "grant-disable", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-disable", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-disable-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-disable", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-disable-secret"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := s.CreateOAuthGrant(g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableOAuthClient("client-1", now+1); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-disable-secret", "192.0.2.5", now+2); ok {
		t.Fatal("access token remained valid after client disable")
	}
	client, err := s.GetOAuthClient("client-1")
	if err != nil || client.DisabledAt == nil {
		t.Fatalf("client disabled state=%+v err=%v", client, err)
	}
}

func TestOAuthRevokingOneTokenRevokesItsGrant(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(3000)
	g := OAuthGrant{ID: "grant-revoke", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-revoke", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-revoke-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-revoke", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-revoke-secret"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := s.CreateOAuthGrant(g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeOAuthToken("refresh-revoke-secret", "client-1", now+1); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-revoke-secret", "192.0.2.5", now+2); ok {
		t.Fatal("access token remained valid after refresh-token revocation")
	}
}

func TestOAuthGrantScopesStayNarrowAfterAPIKeyScopeWidening(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := time.Now().Unix()
	g := OAuthGrant{ID: "grant-scope-narrow", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\",\"mcp:call\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-scope-narrow", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-scope-narrow"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-scope-narrow", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-scope-narrow"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := s.CreateOAuthGrant(g, a, r); err != nil {
		t.Fatal(err)
	}
	var devices string
	if err := s.DB.QueryRow("SELECT devices_json FROM api_tokens WHERE id='api-1'").Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAPIToken("api-1", "desktop", "[\"devices:read\"]", devices, 7, 3, "", nil); err != nil {
		t.Fatal(err)
	}
	scopes, err := s.GetOAuthGrantScopes(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0] != "devices:read" {
		t.Fatalf("narrowed grant scopes=%v", scopes)
	}
	if err := s.UpdateAPIToken("api-1", "desktop", "[\"devices:read\",\"mcp:call\",\"exec\"]", devices, 7, 3, "", nil); err != nil {
		t.Fatal(err)
	}
	scopes, err = s.GetOAuthGrantScopes(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0] != "devices:read" {
		t.Fatalf("scope widening restored old OAuth scope: %v", scopes)
	}
}

func TestAPITokenRotationInvalidatesPendingOAuthCode(t *testing.T) {
	s, _, redirect := oauthFixture(t)
	code := OAuthAuthorizationCode{CodeHash: HashAPIToken("pending-code"), ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, RedirectURI: redirect, ScopesJSON: "[\"devices:read\"]", CodeChallenge: "challenge", Resource: "https://hub.example.com/mcp", CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + 60}
	if err := s.CreateOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	if err := s.RotateAPIToken("api-1", "newprefix", HashAPIToken("new-api-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuthCode(code.CodeHash, "client-1", redirect, time.Now().Unix()); !errors.Is(err, ErrOAuthCodeInvalid) {
		t.Fatalf("pending code survived API token rotation: %v", err)
	}
}

func TestOAuthDeviceGrantDoesNotExpandWhenUnderlyingTokenWidens(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := time.Now().Unix()
	g := OAuthGrant{ID: "grant-device-narrow", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\"]", DevicesJSON: "[]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-device-narrow", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-device-narrow"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-device-narrow", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-device-narrow"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := s.CreateOAuthGrant(g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAPIToken("api-1", "desktop", "[\"devices:read\",\"mcp:call\",\"exec\"]", "[]", 7, 3, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAPIToken("api-1", "desktop", "[\"devices:read\",\"mcp:call\",\"exec\"]", "[\"node-1\",\"node-2\"]", 7, 3, "", nil); err != nil {
		t.Fatal(err)
	}
	principal, ok := s.ValidateOAuthAccessToken("access-device-narrow", "192.0.2.9", time.Now().Unix())
	if !ok || !principal.Devices["node-1"] || principal.Devices["node-2"] {
		t.Fatalf("delegated device ACL expanded: principal=%+v ok=%v", principal, ok)
	}
}

func TestConsumedOAuthCodeCannotIssueAfterTokenPolicyVersionChanges(t *testing.T) {
	s, _, redirect := oauthFixture(t)
	now := time.Now().Unix()
	code := OAuthAuthorizationCode{CodeHash: HashAPIToken("consumed-before-policy-change"), ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, RedirectURI: redirect, ScopesJSON: "[\"devices:read\"]", CodeChallenge: "challenge", Resource: "https://hub.example.com/mcp", CreatedAt: now, ExpiresAt: now + 60}
	if err := s.CreateOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	consumed, err := s.ConsumeOAuthCode(code.CodeHash, "client-1", redirect, now+1)
	if err != nil {
		t.Fatal(err)
	}
	var devices string
	if err := s.DB.QueryRow("SELECT devices_json FROM api_tokens WHERE id='api-1'").Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAPIToken("api-1", "desktop", "[\"devices:read\"]", devices, 7, 3, "", nil); err != nil {
		t.Fatal(err)
	}
	g := OAuthGrant{ID: "grant-stale-code", ClientID: "client-1", APITokenID: consumed.APITokenID, APITokenPrefix: consumed.APITokenPrefix, APITokenVersion: consumed.APITokenVersion, ScopesJSON: consumed.ScopesJSON, Resource: consumed.Resource, CreatedAt: now + 2, RefreshExpiresAt: now + 86400}
	a := OAuthToken{ID: "access-stale-code", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-stale-code"), CreatedAt: now + 2, ExpiresAt: now + 3600}
	if err := s.CreateOAuthGrant(g, a, OAuthToken{}); err == nil {
		t.Fatal("consumed code issued grant after underlying token policy version changed")
	}
}

func TestOAuthMigrationsFromLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE api_tokens (
 id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL UNIQUE, token_hash TEXT NOT NULL,
 scopes_json TEXT NOT NULL, devices_json TEXT, rate_rps INTEGER NOT NULL DEFAULT 10,
 max_sessions INTEGER NOT NULL DEFAULT 10, note TEXT, created_at INTEGER NOT NULL, created_by TEXT NOT NULL,
 expires_at INTEGER, last_used_at INTEGER, last_used_ip TEXT, revoked_at INTEGER, is_legacy INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE oauth_clients (client_id TEXT PRIMARY KEY, client_name TEXT NOT NULL, redirect_uris_json TEXT NOT NULL, created_at INTEGER NOT NULL, disabled_at INTEGER);
CREATE TABLE oauth_codes (code_hash TEXT PRIMARY KEY, client_id TEXT NOT NULL, api_token_id TEXT NOT NULL, redirect_uri TEXT NOT NULL, scopes_json TEXT NOT NULL, code_challenge TEXT NOT NULL, resource TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER);
CREATE TABLE oauth_grants (id TEXT PRIMARY KEY, client_id TEXT NOT NULL, api_token_id TEXT NOT NULL, scopes_json TEXT NOT NULL, resource TEXT NOT NULL, created_at INTEGER NOT NULL, refresh_expires_at INTEGER NOT NULL, revoked_at INTEGER);
INSERT INTO api_tokens(id,name,prefix,token_hash,scopes_json,devices_json,rate_rps,max_sessions,created_at,created_by,is_legacy)
 VALUES('api-old','old','oldprefix','hash','["devices:read"]','["node-old"]',10,5,100,'migration-test',0);
INSERT INTO oauth_clients(client_id,client_name,redirect_uris_json,created_at) VALUES('client-old','old client','["http://127.0.0.1:1234/callback"]',100);
INSERT INTO oauth_grants(id,client_id,api_token_id,scopes_json,resource,created_at,refresh_expires_at)
 VALUES('grant-old','client-old','api-old','["devices:read"]','https://hub.example.com/mcp',100,9999999999);
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnsureAPITokens(""); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureAPITokens(""); err != nil {
		t.Fatalf("second EnsureAPITokens was not idempotent: %v", err)
	}
	var version int64
	if err := s.DB.QueryRow("SELECT oauth_version FROM api_tokens WHERE id='api-old'").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("migrated token version=%d", version)
	}
	var devices string
	if err := s.DB.QueryRow("SELECT devices_json FROM oauth_grants WHERE id='grant-old'").Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if devices != "[\"node-old\"]" {
		t.Fatalf("legacy grant device snapshot=%s", devices)
	}
	var grantTypes, responseTypes string
	if err := s.DB.QueryRow("SELECT grant_types_json,response_types_json FROM oauth_clients WHERE client_id='client-old'").Scan(&grantTypes, &responseTypes); err != nil {
		t.Fatal(err)
	}
	if grantTypes != "[]" || responseTypes != "[]" {
		t.Fatalf("legacy client migration should fail closed: grants=%s responses=%s", grantTypes, responseTypes)
	}
	var codeVersion int64
	if err := s.DB.QueryRow("SELECT api_token_version FROM oauth_codes LIMIT 1").Scan(&codeVersion); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}
