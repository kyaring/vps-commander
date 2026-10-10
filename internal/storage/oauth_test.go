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
	if err := s.RegisterOAuthClient(OAuthClient{ID: "client-1", Name: "Test MCP", RedirectURIsJSON: string(uris), ClientSecretHash: HashAPIToken("client-secret"), CreatedAt: 10}); err != nil {
		t.Fatal(err)
	}
	return s, raw, "http://127.0.0.1:9876/callback"
}

func createOAuthGrantForTest(t *testing.T, s *Store, g OAuthGrant, access, refresh OAuthToken) error {
	t.Helper()
	if g.CodeHash == "" {
		var scopes []string
		if json.Unmarshal([]byte(g.ScopesJSON), &scopes) != nil {
			return s.CreateOAuthGrant(g, access, refresh)
		}
		codeScopesJSON := g.ScopesJSON
		for _, scope := range scopes {
			if scope == "admin" {
				codeScopesJSON = `["devices:read"]`
				break
			}
		}
		g.CodeHash = HashAPIToken("test-code-" + g.ID)
		createdAt := g.CreatedAt - 1
		code := OAuthAuthorizationCode{
			CodeHash: g.CodeHash, ClientID: g.ClientID, APITokenID: g.APITokenID,
			APITokenPrefix: g.APITokenPrefix, APITokenVersion: g.APITokenVersion,
			RedirectURI: "http://127.0.0.1:9876/callback", ScopesJSON: codeScopesJSON,
			CodeChallenge: "test-challenge", Resource: g.Resource,
			CreatedAt: createdAt, ExpiresAt: g.CreatedAt + 60,
		}
		if err := s.CreateOAuthCode(code); err != nil {
			return err
		}
		if _, err := s.ConsumeOAuthCode(g.CodeHash, g.ClientID, code.RedirectURI, g.CreatedAt); err != nil {
			return err
		}
	}
	return s.CreateOAuthGrant(g, access, refresh)
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

func TestCreateOAuthGrantRequiresCodeHash(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(1000)
	g := OAuthGrant{ID: "grant-no-code", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["devices:read"]`, Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 3600}
	a := OAuthToken{ID: "access-no-code", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-no-code"), CreatedAt: now, ExpiresAt: now + 3600}
	if err := s.CreateOAuthGrant(g, a, OAuthToken{}); err == nil {
		t.Fatal("CreateOAuthGrant accepted an empty CodeHash")
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
	if err := createOAuthGrantForTest(t, s, g, access, refresh); err != nil {
		t.Fatal(err)
	}
	p, ok := s.ValidateOAuthAccessToken("access-secret", "https://hub.example.com/mcp", "192.0.2.1", now+1)
	if !ok || !p.Scopes["devices:read"] || p.Scopes["files:read"] || !p.Scopes["mcp:call"] || p.Scopes["exec"] != false {
		t.Fatalf("access principal=%+v ok=%v; expected grant scopes intersected with current API token scopes", p, ok)
	}
	var lastUsed sql.NullInt64
	if err := s.DB.QueryRow("SELECT last_used_at FROM oauth_grants WHERE id=?", g.ID).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if !lastUsed.Valid || lastUsed.Int64 != now+1 {
		t.Fatalf("grant last_used_at=%v, want %d", lastUsed, now+1)
	}
	grants, err := s.ListOAuthGrants()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].LastUsedAt == nil || *grants[0].LastUsedAt != now+1 {
		t.Fatalf("grant record missing last use: %+v", grants)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-secret", "https://other.example/mcp", "192.0.2.1", now+1); ok {
		t.Fatal("OAuth access token was accepted for a different resource")
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
	if _, ok := s.ValidateOAuthAccessToken("access-secret-2", "https://hub.example.com/mcp", "192.0.2.1", now+4); ok {
		t.Fatal("access token remained valid after refresh replay revoked the grant")
	}
}

func TestOAuthRevokedAPITokenInvalidatesAccess(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(1000)
	g := OAuthGrant{ID: "grant-2", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-4", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-secret-4"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-4", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-secret-4"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAPIToken("api-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-secret-4", "https://hub.example.com/mcp", "192.0.2.2", now+1); ok {
		t.Fatal("OAuth access survived underlying API token revocation")
	}
}

func TestOAuthClientDisableRevokesGrantAndAccess(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(2000)
	g := OAuthGrant{ID: "grant-disable", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-disable", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-disable-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-disable", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-disable-secret"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableOAuthClient("client-1", now+1); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-disable-secret", "https://hub.example.com/mcp", "192.0.2.5", now+2); ok {
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
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeOAuthToken("refresh-revoke-secret", "client-1", now+1); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidateOAuthAccessToken("access-revoke-secret", "https://hub.example.com/mcp", "192.0.2.5", now+2); ok {
		t.Fatal("access token remained valid after refresh-token revocation")
	}
}

func TestOAuthGrantScopesStayNarrowAfterAPIKeyScopeWidening(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := time.Now().Unix()
	g := OAuthGrant{ID: "grant-scope-narrow", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: "[\"devices:read\",\"mcp:call\"]", Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 30*86400}
	a := OAuthToken{ID: "access-scope-narrow", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-scope-narrow"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "refresh-scope-narrow", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-scope-narrow"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
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
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAPIToken("api-1", "desktop", "[\"devices:read\",\"mcp:call\",\"exec\"]", "[]", 7, 3, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAPIToken("api-1", "desktop", "[\"devices:read\",\"mcp:call\",\"exec\"]", "[\"node-1\",\"node-2\"]", 7, 3, "", nil); err != nil {
		t.Fatal(err)
	}
	principal, ok := s.ValidateOAuthAccessToken("access-device-narrow", "https://hub.example.com/mcp", "192.0.2.9", time.Now().Unix())
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
	g := OAuthGrant{ID: "grant-stale-code", ClientID: "client-1", APITokenID: consumed.APITokenID, APITokenPrefix: consumed.APITokenPrefix, APITokenVersion: consumed.APITokenVersion, ScopesJSON: consumed.ScopesJSON, Resource: consumed.Resource, CodeHash: consumed.CodeHash, CreatedAt: now + 2, RefreshExpiresAt: now + 86400}
	a := OAuthToken{ID: "access-stale-code", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-stale-code"), CreatedAt: now + 2, ExpiresAt: now + 3600}
	if err := createOAuthGrantForTest(t, s, g, a, OAuthToken{}); err == nil {
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

func TestOAuthClientSecretStoredAsHashAndAuthenticated(t *testing.T) {
	s, _, _ := oauthFixture(t)
	var stored string
	if err := s.DB.QueryRow("SELECT client_secret_hash FROM oauth_clients WHERE client_id='client-1'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != HashAPIToken("client-secret") || stored == "client-secret" {
		t.Fatalf("unexpected client secret storage: %q", stored)
	}
	if _, ok := s.AuthenticateOAuthClient("client-1", "client-secret"); !ok {
		t.Fatal("valid client secret rejected")
	}
	if _, ok := s.AuthenticateOAuthClient("client-1", "wrong-secret"); ok {
		t.Fatal("wrong client secret accepted")
	}
	if _, ok := s.AuthenticateOAuthClient("client-old", "anything"); ok {
		t.Fatal("legacy client without a secret hash authenticated")
	}
}

func TestOAuthAdminScopeRejectedAndNeverValidated(t *testing.T) {
	s, _, redirect := oauthFixture(t)
	now := time.Now().Unix()
	code := OAuthAuthorizationCode{CodeHash: HashAPIToken("admin-code"), ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, RedirectURI: redirect, ScopesJSON: `["admin"]`, CodeChallenge: "challenge", Resource: "https://hub.example.com/mcp", CreatedAt: now, ExpiresAt: now + 60}
	if err := s.CreateOAuthCode(code); err == nil {
		t.Fatal("authorization code with admin scope accepted")
	}
	g := OAuthGrant{ID: "admin-grant", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["admin","devices:read"]`, Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 3600}
	a := OAuthToken{ID: "admin-access", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("admin-access-secret"), CreatedAt: now, ExpiresAt: now + 600}
	r := OAuthToken{ID: "admin-refresh", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("admin-refresh-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	if err := createOAuthGrantForTest(t, s, g, a, r); err == nil {
		t.Fatal("grant with admin scope accepted")
	}
	// Simulate corrupt/legacy storage: validation must still fail closed for admin.
	g.ScopesJSON = `["admin","devices:read"]`
	if err := s.DB.QueryRow("SELECT 1").Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO oauth_grants(id,client_id,api_token_id,scopes_json,devices_json,resource,created_at,refresh_expires_at,code_hash) VALUES(?,?,?,?,?,?,?,?,?)", g.ID, g.ClientID, g.APITokenID, g.ScopesJSON, `[]`, g.Resource, g.CreatedAt, g.RefreshExpiresAt, ""); err != nil {
		t.Fatal(err)
	}
	if err := insertOAuthTokenForTest(s, a); err != nil {
		t.Fatal(err)
	}
	p, ok := s.ValidateOAuthAccessToken("admin-access-secret", "https://hub.example.com/mcp", "192.0.2.10", now+1)
	if !ok || p.Scopes["admin"] || !p.Scopes["devices:read"] {
		t.Fatalf("admin scope not filtered: principal=%+v ok=%v", p, ok)
	}
}

func insertOAuthTokenForTest(s *Store, tkn OAuthToken) error {
	_, err := s.DB.Exec("INSERT INTO oauth_tokens(id,grant_id,kind,token_hash,created_at,expires_at) VALUES(?,?,?,?,?,?)", tkn.ID, tkn.GrantID, tkn.Kind, tkn.TokenHash, tkn.CreatedAt, tkn.ExpiresAt)
	return err
}

func TestOAuthAuthorizationCodeReplayRevokesIssuedGrant(t *testing.T) {
	s, _, redirect := oauthFixture(t)
	now := time.Now().Unix()
	codeHash := HashAPIToken("replay-code")
	code := OAuthAuthorizationCode{CodeHash: codeHash, ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, RedirectURI: redirect, ScopesJSON: `["devices:read"]`, CodeChallenge: "challenge", Resource: "https://hub.example.com/mcp", CreatedAt: now, ExpiresAt: now + 60}
	if err := s.CreateOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuthCode(codeHash, "client-1", redirect, now+1); err != nil {
		t.Fatal(err)
	}
	g := OAuthGrant{ID: "replay-grant", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["devices:read"]`, Resource: "https://hub.example.com/mcp", CodeHash: codeHash, CreatedAt: now + 1, RefreshExpiresAt: now + 3600}
	a := OAuthToken{ID: "replay-access", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("replay-access-secret"), CreatedAt: now + 1, ExpiresAt: now + 600}
	r := OAuthToken{ID: "replay-refresh", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("replay-refresh-secret"), CreatedAt: now + 1, ExpiresAt: now + 3600}
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuthCode(codeHash, "client-1", redirect, now+1000); !errors.Is(err, ErrOAuthCodeInvalid) {
		t.Fatalf("replay error=%v", err)
	}
	if _, ok := s.ValidateOAuthAccessToken("replay-access-secret", "https://hub.example.com/mcp", "192.0.2.11", now+3); ok {
		t.Fatal("replayed code did not revoke the issued grant")
	}
}

func TestOAuthRefreshRejectsExpiredOrLegacyUnderlyingToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expire bool
		legacy bool
	}{
		{name: "expired", expire: true}, {name: "legacy", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := oauthFixture(t)
			now := time.Now().Unix()
			if tc.expire {
				if _, err := s.DB.Exec("UPDATE api_tokens SET expires_at=? WHERE id='api-1'", now-1); err != nil {
					t.Fatal(err)
				}
			}
			if tc.legacy {
				if _, err := s.DB.Exec("UPDATE api_tokens SET is_legacy=1 WHERE id='api-1'"); err != nil {
					t.Fatal(err)
				}
			}
			g := OAuthGrant{ID: "refresh-policy", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["devices:read"]`, Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 3600}
			r := OAuthToken{ID: "policy-refresh", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("policy-refresh-secret"), CreatedAt: now, ExpiresAt: now + 3600}
			// Grant creation intentionally rejects invalid underlying token; seed the grant directly to test refresh defense.
			if _, err := s.DB.Exec("INSERT INTO oauth_grants(id,client_id,api_token_id,scopes_json,devices_json,resource,created_at,refresh_expires_at) VALUES(?,?,?,?,?,?,?,?)", g.ID, g.ClientID, g.APITokenID, g.ScopesJSON, `["node-1"]`, g.Resource, g.CreatedAt, g.RefreshExpiresAt); err != nil {
				t.Fatal(err)
			}
			if err := insertOAuthTokenForTest(s, r); err != nil {
				t.Fatal(err)
			}
			_, err := s.RotateOAuthRefresh(HashAPIToken("policy-refresh-secret"), "client-1", now+1,
				OAuthToken{ID: "policy-access-2", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("policy-access-2-secret"), CreatedAt: now + 1, ExpiresAt: now + 601},
				OAuthToken{ID: "policy-refresh-2", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("policy-refresh-2-secret"), CreatedAt: now + 1, ExpiresAt: now + 3600})
			if !errors.Is(err, ErrOAuthRefreshInvalid) {
				t.Fatalf("refresh should reject invalid underlying token, got %v", err)
			}
		})
	}
}

func TestOAuthAccessRejectsUnderlyingLegacyToken(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := time.Now().Unix()
	g := OAuthGrant{ID: "legacy-access-grant", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["devices:read"]`, Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: now + 3600}
	a := OAuthToken{ID: "legacy-access", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("legacy-access-secret"), CreatedAt: now, ExpiresAt: now + 600}
	r := OAuthToken{ID: "legacy-refresh", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("legacy-refresh-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE api_tokens SET is_legacy=1 WHERE id='api-1'"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidateOAuthAccessToken("legacy-access-secret", "https://hub.example.com/mcp", "192.0.2.12", now+1); ok {
		t.Fatal("OAuth access token backed by a legacy API token was accepted")
	}
}

func TestOAuthGrantListingAndRevoke(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := time.Now().Unix()
	g := OAuthGrant{ID: "grant-list", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["devices:read","mcp:call"]`, DevicesJSON: `["node-1"]`, Resource: "https://hub.example.test/mcp", CreatedAt: now, RefreshExpiresAt: now + 3600}
	access := OAuthToken{ID: "access-list", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("access-list-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	refresh := OAuthToken{ID: "refresh-list", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("refresh-list-secret"), CreatedAt: now, ExpiresAt: now + 3600}
	if err := createOAuthGrantForTest(t, s, g, access, refresh); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListOAuthGrants()
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListOAuthGrants len=%d err=%v", len(rows), err)
	}
	if rows[0].ClientName != "Test MCP" || rows[0].APITokenName != "desktop" || len(rows[0].Scopes) != 2 || rows[0].RevokedAt != nil {
		t.Fatalf("grant record=%+v", rows[0])
	}
	if err := s.RevokeOAuthGrant(g.ID, now+1); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListOAuthGrants()
	if err != nil || len(rows) != 1 || rows[0].RevokedAt == nil {
		t.Fatalf("revoked grant list=%+v err=%v", rows, err)
	}
	if err := s.RevokeOAuthGrant("missing-grant", now+1); err == nil {
		t.Fatal("missing grant revoke should fail")
	}
}

func TestOAuthRefreshSlidingExpiryWithAbsoluteCap(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(1000)
	day := int64(86400)
	g := OAuthGrant{ID: "grant-sliding", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["devices:read"]`, Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: OAuthRefreshExpiry(now, now)}
	if g.RefreshExpiresAt != now+30*day {
		t.Fatalf("initial refresh expiry=%d want %d", g.RefreshExpiresAt, now+30*day)
	}
	a := OAuthToken{ID: "sl-a0", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("sl-a0"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "sl-r0", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("sl-r0"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	cur := "sl-r0"
	step := 0
	rotate := func(at int64) (OAuthGrant, error) {
		step++
		next := "sl-r" + string(rune('a'+step))
		acc := OAuthToken{ID: "sl-a" + string(rune('a'+step)), GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("sl-acc" + next), CreatedAt: at, ExpiresAt: at + 3600}
		// Caller-supplied ExpiresAt must not matter: storage enforces policy.
		ref := OAuthToken{ID: next, GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken(next), CreatedAt: at, ExpiresAt: at + 1000*day}
		out, err := s.RotateOAuthRefresh(HashAPIToken(cur), "client-1", at, acc, ref)
		if err == nil {
			cur = next
		}
		return out, err
	}
	grantExpiry := func() int64 {
		var v int64
		if err := s.DB.QueryRow("SELECT refresh_expires_at FROM oauth_grants WHERE id=?", g.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	tokenExpiry := func(id string) int64 {
		var v int64
		if err := s.DB.QueryRow("SELECT expires_at FROM oauth_tokens WHERE id=?", id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Used within 30 days: slides forward 30 days from use, past the original expiry.
	at := now + 20*day
	out, err := rotate(at)
	if err != nil {
		t.Fatalf("rotate day 20: %v", err)
	}
	if out.RefreshExpiresAt != at+30*day || grantExpiry() != at+30*day || tokenExpiry(cur) != at+30*day {
		t.Fatalf("day 20 expiry ret=%d grant=%d token=%d want %d", out.RefreshExpiresAt, grantExpiry(), tokenExpiry(cur), at+30*day)
	}
	at = now + 45*day // beyond the original 30-day expiry
	if _, err := rotate(at); err != nil {
		t.Fatalf("rotate day 45 (sliding) failed: %v", err)
	}
	// Keep using every 29 days until close to the absolute cap.
	for at+29*day < now+360*day {
		at += 29 * day
		if _, err := rotate(at); err != nil {
			t.Fatalf("rotate at day %d: %v", (at-now)/day, err)
		}
	}
	if want := now + 360*day; grantExpiry() != want || tokenExpiry(cur) != want {
		t.Fatalf("near cap expiry grant=%d token=%d want cap %d", grantExpiry(), tokenExpiry(cur), want)
	}
	if _, err := rotate(now + 360*day); !errors.Is(err, ErrOAuthRefreshInvalid) {
		t.Fatalf("rotation at absolute cap error=%v, want invalid", err)
	}
}

func TestOAuthRefreshIdleExpiry(t *testing.T) {
	s, _, _ := oauthFixture(t)
	now := int64(1000)
	g := OAuthGrant{ID: "grant-idle", ClientID: "client-1", APITokenID: "api-1", APITokenPrefix: "abcd1234", APITokenVersion: 1, ScopesJSON: `["devices:read"]`, Resource: "https://hub.example.com/mcp", CreatedAt: now, RefreshExpiresAt: OAuthRefreshExpiry(now, now)}
	a := OAuthToken{ID: "idle-a0", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("idle-a0"), CreatedAt: now, ExpiresAt: now + 3600}
	r := OAuthToken{ID: "idle-r0", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("idle-r0"), CreatedAt: now, ExpiresAt: g.RefreshExpiresAt}
	if err := createOAuthGrantForTest(t, s, g, a, r); err != nil {
		t.Fatal(err)
	}
	at := now + 30*86400
	_, err := s.RotateOAuthRefresh(HashAPIToken("idle-r0"), "client-1", at,
		OAuthToken{ID: "idle-a1", GrantID: g.ID, Kind: "access", TokenHash: HashAPIToken("idle-a1"), CreatedAt: at, ExpiresAt: at + 3600},
		OAuthToken{ID: "idle-r1", GrantID: g.ID, Kind: "refresh", TokenHash: HashAPIToken("idle-r1"), CreatedAt: at, ExpiresAt: at + 30*86400})
	if !errors.Is(err, ErrOAuthRefreshInvalid) {
		t.Fatalf("refresh after 30 idle days error=%v, want invalid", err)
	}
}
