package storage

import (
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

var (
	ErrOAuthCodeInvalid    = errors.New("oauth authorization code invalid or already used")
	ErrOAuthRefreshInvalid = errors.New("oauth refresh token invalid")
	ErrOAuthRefreshReplay  = errors.New("oauth refresh token replay detected")
)

type OAuthClient struct {
	ID, Name, RedirectURIsJSON, GrantTypesJSON, ResponseTypesJSON, ClientSecretHash string
	CreatedAt                                                                       int64
	DisabledAt                                                                      *int64
}
type OAuthAuthorizationCode struct {
	CodeHash, ClientID, APITokenID, APITokenPrefix, RedirectURI, ScopesJSON, CodeChallenge, Resource string
	APITokenVersion, CreatedAt, ExpiresAt                                                            int64
	UsedAt                                                                                           *int64
}
type OAuthGrant struct {
	ID, ClientID, APITokenID, APITokenPrefix, ScopesJSON, DevicesJSON, Resource, CodeHash string
	APITokenVersion, CreatedAt, RefreshExpiresAt                                          int64
	RevokedAt                                                                             *int64
}
type OAuthToken struct {
	ID, GrantID, Kind, TokenHash string
	CreatedAt, ExpiresAt         int64
	ConsumedAt, RevokedAt        *int64
	ReplacedBy                   string
}
type OAuthAccessPrincipal struct {
	APITokenID, GrantID, TokenName, ClientID, ClientName string
	Scopes, Devices                                      map[string]bool
	RateRPS, MaxSessions                                 int
}

func (s *Store) migrateOAuth() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS oauth_clients (
 client_id TEXT PRIMARY KEY, client_name TEXT NOT NULL,
 redirect_uris_json TEXT NOT NULL, created_at INTEGER NOT NULL, disabled_at INTEGER,
 grant_types_json TEXT NOT NULL DEFAULT '["authorization_code","refresh_token"]',
 response_types_json TEXT NOT NULL DEFAULT '["code"]',
 client_secret_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_oauth_clients_created ON oauth_clients(created_at);
CREATE TABLE IF NOT EXISTS oauth_codes (
 code_hash TEXT PRIMARY KEY, client_id TEXT NOT NULL, api_token_id TEXT NOT NULL,
 redirect_uri TEXT NOT NULL, scopes_json TEXT NOT NULL, code_challenge TEXT NOT NULL,
 resource TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER,
 api_token_prefix TEXT NOT NULL DEFAULT '', api_token_version INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_oauth_codes_expiry ON oauth_codes(expires_at);
CREATE TABLE IF NOT EXISTS oauth_grants (
 id TEXT PRIMARY KEY, client_id TEXT NOT NULL, api_token_id TEXT NOT NULL,
 scopes_json TEXT NOT NULL, devices_json TEXT NOT NULL DEFAULT '[]', resource TEXT NOT NULL, created_at INTEGER NOT NULL,
 refresh_expires_at INTEGER NOT NULL, revoked_at INTEGER, code_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_oauth_grants_client ON oauth_grants(client_id, created_at);
CREATE INDEX IF NOT EXISTS idx_oauth_grants_api_token ON oauth_grants(api_token_id, revoked_at);
CREATE TABLE IF NOT EXISTS oauth_tokens (
 id TEXT PRIMARY KEY, grant_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('access','refresh')), token_hash TEXT NOT NULL UNIQUE,
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, consumed_at INTEGER,
 revoked_at INTEGER, replaced_by TEXT
);
CREATE INDEX IF NOT EXISTS idx_oauth_tokens_grant ON oauth_tokens(grant_id, kind);
CREATE INDEX IF NOT EXISTS idx_oauth_tokens_expiry ON oauth_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_oauth_tokens_hash_kind ON oauth_tokens(token_hash, kind);
`)
	if err != nil {
		return err
	}
	cols := map[string]bool{}
	rows, err := s.DB.Query("PRAGMA table_info(oauth_clients)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		cols[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !cols["grant_types_json"] {
		if _, err := s.DB.Exec("ALTER TABLE oauth_clients ADD COLUMN grant_types_json TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return err
		}
	}
	if !cols["response_types_json"] {
		if _, err := s.DB.Exec("ALTER TABLE oauth_clients ADD COLUMN response_types_json TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return err
		}
	}
	if !cols["client_secret_hash"] {
		if _, err := s.DB.Exec("ALTER TABLE oauth_clients ADD COLUMN client_secret_hash TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	codeCols := map[string]bool{}
	codeRows, err := s.DB.Query("PRAGMA table_info(oauth_codes)")
	if err != nil {
		return err
	}
	for codeRows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := codeRows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			codeRows.Close()
			return err
		}
		codeCols[name] = true
	}
	if err := codeRows.Close(); err != nil {
		return err
	}
	if !codeCols["api_token_prefix"] {
		if _, err := s.DB.Exec("ALTER TABLE oauth_codes ADD COLUMN api_token_prefix TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if !codeCols["api_token_version"] {
		if _, err := s.DB.Exec("ALTER TABLE oauth_codes ADD COLUMN api_token_version INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	grantCols := map[string]bool{}
	grantRows, err := s.DB.Query("PRAGMA table_info(oauth_grants)")
	if err != nil {
		return err
	}
	for grantRows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := grantRows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			grantRows.Close()
			return err
		}
		grantCols[name] = true
	}
	if err := grantRows.Close(); err != nil {
		return err
	}
	if !grantCols["devices_json"] {
		if _, err := s.DB.Exec("ALTER TABLE oauth_grants ADD COLUMN devices_json TEXT"); err != nil {
			return err
		}
	}
	if !grantCols["code_hash"] {
		if _, err := s.DB.Exec("ALTER TABLE oauth_grants ADD COLUMN code_hash TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

func validOAuthRedirectURI(raw string) bool {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.Contains(raw, "*") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Opaque != "" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Store) RegisterOAuthClient(c OAuthClient) error {
	if c.ID == "" || len(c.ID) > 128 || c.Name == "" || len(c.Name) > 200 || c.RedirectURIsJSON == "" || len(c.ClientSecretHash) != 64 {
		return errors.New("oauth client id, name, redirect URIs and SHA-256 client secret hash required")
	}
	if _, err := hex.DecodeString(c.ClientSecretHash); err != nil {
		return errors.New("client secret hash must be SHA-256 hex")
	}
	c.ClientSecretHash = strings.ToLower(c.ClientSecretHash)
	var uris []string
	if err := json.Unmarshal([]byte(c.RedirectURIsJSON), &uris); err != nil || len(uris) == 0 || len(uris) > 10 {
		return errors.New("valid redirect URI list required")
	}
	seenURIs := map[string]bool{}
	for _, uri := range uris {
		if !validOAuthRedirectURI(uri) || seenURIs[uri] {
			return errors.New("redirect URIs must be unique absolute HTTPS or loopback HTTP URLs without fragments or userinfo")
		}
		seenURIs[uri] = true
	}
	var grants, responses []string
	if err := json.Unmarshal([]byte(c.GrantTypesJSON), &grants); err != nil && c.GrantTypesJSON != "" {
		return errors.New("invalid OAuth grant types")
	}
	if len(grants) == 0 && c.GrantTypesJSON != "" {
		return errors.New("OAuth authorization_code grant is required")
	}
	if c.GrantTypesJSON != "" {
		seen := map[string]bool{}
		for _, grant := range grants {
			if (grant != "authorization_code" && grant != "refresh_token") || seen[grant] {
				return errors.New("unsupported or duplicate OAuth grant type")
			}
			seen[grant] = true
		}
		if !seen["authorization_code"] {
			return errors.New("OAuth authorization_code grant is required")
		}
	}
	if err := json.Unmarshal([]byte(c.ResponseTypesJSON), &responses); err != nil && c.ResponseTypesJSON != "" {
		return errors.New("invalid OAuth response types")
	}
	if c.ResponseTypesJSON != "" && (len(responses) != 1 || responses[0] != "code") {
		return errors.New("only OAuth response_type code is supported")
	}
	if c.CreatedAt <= 0 {
		c.CreatedAt = time.Now().Unix()
	}
	if c.GrantTypesJSON == "" {
		c.GrantTypesJSON = "[\"authorization_code\",\"refresh_token\"]"
	}
	if c.ResponseTypesJSON == "" {
		c.ResponseTypesJSON = "[\"code\"]"
	}
	_, err := s.DB.Exec("INSERT INTO oauth_clients(client_id,client_name,redirect_uris_json,created_at,disabled_at,grant_types_json,response_types_json,client_secret_hash) VALUES(?,?,?,?,?,?,?,?)", c.ID, c.Name, c.RedirectURIsJSON, c.CreatedAt, c.DisabledAt, c.GrantTypesJSON, c.ResponseTypesJSON, c.ClientSecretHash)
	return err
}
func (s *Store) GetOAuthClient(id string) (OAuthClient, error) {
	var c OAuthClient
	err := s.DB.QueryRow("SELECT client_id,client_name,redirect_uris_json,created_at,disabled_at,grant_types_json,response_types_json,client_secret_hash FROM oauth_clients WHERE client_id=?", id).Scan(&c.ID, &c.Name, &c.RedirectURIsJSON, &c.CreatedAt, &c.DisabledAt, &c.GrantTypesJSON, &c.ResponseTypesJSON, &c.ClientSecretHash)
	return c, err
}
func (s *Store) AuthenticateOAuthClient(id, secret string) (OAuthClient, bool) {
	c, err := s.GetOAuthClient(id)
	if err != nil || c.DisabledAt != nil || len(c.ClientSecretHash) != 64 || secret == "" {
		return OAuthClient{}, false
	}
	candidate := HashAPIToken(secret)
	if subtle.ConstantTimeCompare([]byte(candidate), []byte(c.ClientSecretHash)) != 1 {
		return OAuthClient{}, false
	}
	return c, true
}

func (s *Store) CreateOAuthCode(c OAuthAuthorizationCode) error {
	if c.CodeHash == "" || c.ClientID == "" || c.APITokenID == "" || c.APITokenPrefix == "" || c.APITokenVersion <= 0 || c.RedirectURI == "" || c.CodeChallenge == "" || c.Resource == "" || c.ExpiresAt <= c.CreatedAt {
		return errors.New("incomplete or expired oauth authorization code")
	}
	var scopes []string
	if err := json.Unmarshal([]byte(c.ScopesJSON), &scopes); err != nil || len(scopes) == 0 {
		return errors.New("valid OAuth code scopes required")
	}
	for _, scope := range scopes {
		if scope == "admin" {
			return errors.New("OAuth clients cannot request admin scope")
		}
	}
	_, err := s.DB.Exec("INSERT INTO oauth_codes(code_hash,client_id,api_token_id,redirect_uri,scopes_json,code_challenge,resource,created_at,expires_at,used_at,api_token_prefix,api_token_version) VALUES(?,?,?,?,?,?,?,?,?,NULL,?,?)", c.CodeHash, c.ClientID, c.APITokenID, c.RedirectURI, c.ScopesJSON, c.CodeChallenge, c.Resource, c.CreatedAt, c.ExpiresAt, c.APITokenPrefix, c.APITokenVersion)
	return err
}
func (s *Store) GetOAuthCodeForExchange(hash, clientID, redirectURI string, now int64) (OAuthAuthorizationCode, error) {
	var c OAuthAuthorizationCode
	err := s.DB.QueryRow("SELECT code_hash,client_id,api_token_id,api_token_prefix,redirect_uri,scopes_json,code_challenge,resource,created_at,expires_at,used_at,api_token_version FROM oauth_codes WHERE code_hash=?", hash).Scan(&c.CodeHash, &c.ClientID, &c.APITokenID, &c.APITokenPrefix, &c.RedirectURI, &c.ScopesJSON, &c.CodeChallenge, &c.Resource, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt, &c.APITokenVersion)
	if err != nil || c.ClientID != clientID || c.RedirectURI != redirectURI || c.UsedAt != nil || c.ExpiresAt <= now {
		return OAuthAuthorizationCode{}, ErrOAuthCodeInvalid
	}
	return c, nil
}

func (s *Store) ConsumeOAuthCode(hash, clientID, redirectURI string, now int64) (OAuthAuthorizationCode, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return OAuthAuthorizationCode{}, err
	}
	defer tx.Rollback()
	var c OAuthAuthorizationCode
	err = tx.QueryRow("SELECT code_hash,client_id,api_token_id,api_token_prefix,redirect_uri,scopes_json,code_challenge,resource,created_at,expires_at,used_at,api_token_version FROM oauth_codes WHERE code_hash=?", hash).Scan(&c.CodeHash, &c.ClientID, &c.APITokenID, &c.APITokenPrefix, &c.RedirectURI, &c.ScopesJSON, &c.CodeChallenge, &c.Resource, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt, &c.APITokenVersion)
	if err != nil || c.ClientID != clientID || c.RedirectURI != redirectURI {
		return OAuthAuthorizationCode{}, ErrOAuthCodeInvalid
	}
	if c.UsedAt != nil {
		if err := revokeOAuthCodeGrantTx(tx, c.CodeHash, now); err != nil {
			return OAuthAuthorizationCode{}, err
		}
		if err := tx.Commit(); err != nil {
			return OAuthAuthorizationCode{}, err
		}
		return OAuthAuthorizationCode{}, ErrOAuthCodeInvalid
	}
	if c.ExpiresAt <= now {
		return OAuthAuthorizationCode{}, ErrOAuthCodeInvalid
	}
	res, err := tx.Exec("UPDATE oauth_codes SET used_at=? WHERE code_hash=? AND used_at IS NULL AND expires_at>?", now, hash, now)
	if err != nil {
		return OAuthAuthorizationCode{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return OAuthAuthorizationCode{}, err
	}
	if n != 1 {
		return OAuthAuthorizationCode{}, ErrOAuthCodeInvalid
	}
	c.UsedAt = &now
	if err := tx.Commit(); err != nil {
		return OAuthAuthorizationCode{}, err
	}
	return c, nil
}
func (s *Store) CreateOAuthGrant(g OAuthGrant, access, refresh OAuthToken) error {
	if g.ID == "" || g.ClientID == "" || g.APITokenID == "" || g.Resource == "" || g.CodeHash == "" || access.Kind != "access" || access.GrantID != g.ID || (refresh.ID != "" && (refresh.Kind != "refresh" || refresh.GrantID != g.ID)) {
		return errors.New("invalid oauth grant or tokens")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prefix, currentScopesJSON, currentDevicesJSON string
	var codeScopesJSON string
	var codeScopes []string
	var apiRevoked, apiExpiry, clientDisabled sql.NullInt64
	var legacy int
	var tokenVersion int64
	if err := tx.QueryRow("SELECT a.prefix,a.scopes_json,COALESCE(a.devices_json,'[]'),a.revoked_at,a.expires_at,a.is_legacy,a.oauth_version,c.disabled_at FROM api_tokens a JOIN oauth_clients c ON c.client_id=? WHERE a.id=?", g.ClientID, g.APITokenID).Scan(&prefix, &currentScopesJSON, &currentDevicesJSON, &apiRevoked, &apiExpiry, &legacy, &tokenVersion, &clientDisabled); err != nil || g.APITokenPrefix == "" || prefix != g.APITokenPrefix || g.APITokenVersion != tokenVersion || apiRevoked.Valid || clientDisabled.Valid || legacy != 0 || (apiExpiry.Valid && apiExpiry.Int64 <= g.CreatedAt) {
		return errors.New("underlying API token changed or is unavailable")
	}
	var codeClient, codeToken, codeResource string
	var codeUsed, codeExpiry sql.NullInt64
	if err := tx.QueryRow("SELECT client_id,api_token_id,resource,used_at,expires_at,scopes_json FROM oauth_codes WHERE code_hash=?", g.CodeHash).Scan(&codeClient, &codeToken, &codeResource, &codeUsed, &codeExpiry, &codeScopesJSON); err != nil || !codeUsed.Valid || codeUsed.Int64 > g.CreatedAt || !codeExpiry.Valid || codeExpiry.Int64 <= codeUsed.Int64 || codeClient != g.ClientID || codeToken != g.APITokenID || codeResource != g.Resource || json.Unmarshal([]byte(codeScopesJSON), &codeScopes) != nil {
		return errors.New("OAuth grant is not bound to a consumed authorization code")
	}
	var requestedScopes, currentScopes []string
	if json.Unmarshal([]byte(g.ScopesJSON), &requestedScopes) != nil || json.Unmarshal([]byte(currentScopesJSON), &currentScopes) != nil {
		return errors.New("invalid OAuth grant scopes")
	}
	codeScopeSet := map[string]bool{}
	for _, scope := range codeScopes {
		codeScopeSet[scope] = true
	}
	for _, scope := range requestedScopes {
		if scope == "admin" {
			return errors.New("OAuth grants cannot contain admin scope")
		}
		if !codeScopeSet[scope] {
			return errors.New("OAuth grant scope exceeds authorization code")
		}
	}
	currentSet := map[string]bool{}
	for _, scope := range currentScopes {
		currentSet[scope] = true
	}
	effectiveScopes := make([]string, 0, len(requestedScopes))
	for _, scope := range requestedScopes {
		if scope != "admin" && currentSet[scope] {
			effectiveScopes = append(effectiveScopes, scope)
		}
	}
	if len(effectiveScopes) == 0 {
		return errors.New("OAuth grant has no remaining scopes")
	}
	effectiveJSON, _ := json.Marshal(effectiveScopes)
	g.ScopesJSON = string(effectiveJSON)
	if g.DevicesJSON == "" {
		g.DevicesJSON = currentDevicesJSON
	}
	var grantDevices, currentDevices []string
	if json.Unmarshal([]byte(g.DevicesJSON), &grantDevices) != nil || json.Unmarshal([]byte(currentDevicesJSON), &currentDevices) != nil {
		return errors.New("invalid OAuth grant devices")
	}
	effectiveDevices, devicesErr := intersectDeviceLists(grantDevices, currentDevices)
	if devicesErr != nil {
		return devicesErr
	}
	devicesJSON, _ := json.Marshal(effectiveDevices)
	g.DevicesJSON = string(devicesJSON)
	if _, err := tx.Exec("INSERT INTO oauth_grants(id,client_id,api_token_id,scopes_json,devices_json,resource,created_at,refresh_expires_at,revoked_at,code_hash) VALUES(?,?,?,?,?,?,?,?,NULL,?)", g.ID, g.ClientID, g.APITokenID, g.ScopesJSON, g.DevicesJSON, g.Resource, g.CreatedAt, g.RefreshExpiresAt, g.CodeHash); err != nil {
		return err
	}
	if err := insertOAuthToken(tx, access); err != nil {
		return err
	}
	if refresh.ID != "" {
		if err := insertOAuthToken(tx, refresh); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func revokeOAuthCodeGrantTx(tx *sql.Tx, codeHash string, now int64) error {
	rows, err := tx.Query("SELECT id FROM oauth_grants WHERE code_hash=? AND revoked_at IS NULL", codeHash)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := revokeOAuthGrantTx(tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

func insertOAuthToken(tx *sql.Tx, t OAuthToken) error {
	if t.ID == "" || t.GrantID == "" || t.TokenHash == "" || (t.Kind != "access" && t.Kind != "refresh") {
		return errors.New("invalid oauth token")
	}
	_, err := tx.Exec("INSERT INTO oauth_tokens(id,grant_id,kind,token_hash,created_at,expires_at,consumed_at,revoked_at,replaced_by) VALUES(?,?,?,?,?,?,?,?,?)", t.ID, t.GrantID, t.Kind, t.TokenHash, t.CreatedAt, t.ExpiresAt, t.ConsumedAt, t.RevokedAt, t.ReplacedBy)
	return err
}
func (s *Store) GetOAuthGrantForRefresh(hash, clientID string) (OAuthGrant, error) {
	var g OAuthGrant
	err := s.DB.QueryRow("SELECT g.id,g.client_id,g.api_token_id,g.scopes_json,g.devices_json,g.resource,g.created_at,g.refresh_expires_at,g.code_hash FROM oauth_tokens t JOIN oauth_grants g ON g.id=t.grant_id WHERE t.token_hash=? AND t.kind='refresh' AND g.client_id=?", hash, clientID).Scan(&g.ID, &g.ClientID, &g.APITokenID, &g.ScopesJSON, &g.DevicesJSON, &g.Resource, &g.CreatedAt, &g.RefreshExpiresAt, &g.CodeHash)
	if err != nil {
		return OAuthGrant{}, ErrOAuthRefreshInvalid
	}
	return g, nil
}

func (s *Store) RotateOAuthRefresh(hash, clientID string, now int64, access, refresh OAuthToken) (OAuthGrant, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return OAuthGrant{}, err
	}
	defer tx.Rollback()
	var g OAuthGrant
	var consumed, tokenRevoked, grantRevoked, apiRevoked, apiExpiry, clientDisabled sql.NullInt64
	var tokenExpiry int64
	var legacy int
	var tokenID string
	err = tx.QueryRow("SELECT t.id,t.grant_id,t.consumed_at,t.revoked_at,t.expires_at,g.client_id,g.api_token_id,g.scopes_json,g.devices_json,g.resource,g.created_at,g.refresh_expires_at,g.revoked_at,a.revoked_at,a.expires_at,a.is_legacy,c.disabled_at FROM oauth_tokens t JOIN oauth_grants g ON g.id=t.grant_id JOIN api_tokens a ON a.id=g.api_token_id JOIN oauth_clients c ON c.client_id=g.client_id WHERE t.token_hash=? AND t.kind='refresh'", hash).Scan(&tokenID, &g.ID, &consumed, &tokenRevoked, &tokenExpiry, &g.ClientID, &g.APITokenID, &g.ScopesJSON, &g.DevicesJSON, &g.Resource, &g.CreatedAt, &g.RefreshExpiresAt, &grantRevoked, &apiRevoked, &apiExpiry, &legacy, &clientDisabled)
	if err != nil || g.ClientID != clientID {
		return OAuthGrant{}, ErrOAuthRefreshInvalid
	}
	if consumed.Valid {
		if _, err := tx.Exec("UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,?) WHERE id=?", now, g.ID); err != nil {
			return OAuthGrant{}, err
		}
		if _, err := tx.Exec("UPDATE oauth_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE grant_id=?", now, g.ID); err != nil {
			return OAuthGrant{}, err
		}
		if err := tx.Commit(); err != nil {
			return OAuthGrant{}, err
		}
		return OAuthGrant{}, ErrOAuthRefreshReplay
	}
	if tokenRevoked.Valid || grantRevoked.Valid || apiRevoked.Valid || clientDisabled.Valid || legacy != 0 || (apiExpiry.Valid && apiExpiry.Int64 <= now) || tokenExpiry <= now || g.RefreshExpiresAt <= now {
		return OAuthGrant{}, ErrOAuthRefreshInvalid
	}
	if access.GrantID != g.ID || refresh.GrantID != g.ID || access.Kind != "access" || refresh.Kind != "refresh" {
		return OAuthGrant{}, errors.New("invalid oauth rotation tokens")
	}
	res, err := tx.Exec("UPDATE oauth_tokens SET consumed_at=?,replaced_by=? WHERE id=? AND consumed_at IS NULL AND revoked_at IS NULL", now, refresh.ID, tokenID)
	if err != nil {
		return OAuthGrant{}, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return OAuthGrant{}, ErrOAuthRefreshInvalid
	}
	if err := insertOAuthToken(tx, access); err != nil {
		return OAuthGrant{}, err
	}
	if err := insertOAuthToken(tx, refresh); err != nil {
		return OAuthGrant{}, err
	}
	if err := tx.Commit(); err != nil {
		return OAuthGrant{}, err
	}
	return g, nil
}

// Empty device lists mean unrestricted. If both grant and token are restricted,
// their effective set is the intersection; an empty intersection is unusable.
func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if value != "" && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

func intersectDeviceLists(grant, current []string) ([]string, error) {
	if len(grant) == 0 {
		return uniqueStrings(current), nil
	}
	if len(current) == 0 {
		return uniqueStrings(grant), nil
	}
	currentSet := map[string]bool{}
	for _, v := range current {
		currentSet[v] = true
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range grant {
		if currentSet[v] && !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	if len(out) == 0 {
		return nil, errors.New("OAuth grant has no remaining devices")
	}
	return out, nil
}

func (s *Store) ValidateOAuthAccessToken(raw, resource, ip string, now int64) (OAuthAccessPrincipal, bool) {
	var out OAuthAccessPrincipal
	var tokenRevoked, grantRevoked, apiRevoked, apiExpires, clientDisabled sql.NullInt64
	var grantScopes, apiScopesJSON, grantDevicesJSON, currentDevicesJSON, grantResource string
	var tokenExpiry int64
	var legacy int
	err := s.DB.QueryRow("SELECT g.api_token_id,g.id,a.name,c.client_id,c.client_name,g.scopes_json,a.scopes_json,g.devices_json,COALESCE(a.devices_json,'[]'),a.rate_rps,a.max_sessions,t.revoked_at,g.revoked_at,a.revoked_at,a.expires_at,t.expires_at,c.disabled_at,g.resource,a.is_legacy FROM oauth_tokens t JOIN oauth_grants g ON g.id=t.grant_id JOIN api_tokens a ON a.id=g.api_token_id JOIN oauth_clients c ON c.client_id=g.client_id WHERE t.token_hash=? AND t.kind='access'", HashAPIToken(raw)).Scan(&out.APITokenID, &out.GrantID, &out.TokenName, &out.ClientID, &out.ClientName, &grantScopes, &apiScopesJSON, &grantDevicesJSON, &currentDevicesJSON, &out.RateRPS, &out.MaxSessions, &tokenRevoked, &grantRevoked, &apiRevoked, &apiExpires, &tokenExpiry, &clientDisabled, &grantResource, &legacy)
	if err != nil || resource == "" || grantResource != resource || legacy != 0 || tokenRevoked.Valid || grantRevoked.Valid || apiRevoked.Valid || clientDisabled.Valid || (apiExpires.Valid && apiExpires.Int64 <= now) || tokenExpiry <= now {
		return OAuthAccessPrincipal{}, false
	}
	var granted, current, grantDevices, currentDevices []string
	if json.Unmarshal([]byte(grantScopes), &granted) != nil || json.Unmarshal([]byte(apiScopesJSON), &current) != nil || json.Unmarshal([]byte(grantDevicesJSON), &grantDevices) != nil || json.Unmarshal([]byte(currentDevicesJSON), &currentDevices) != nil {
		return OAuthAccessPrincipal{}, false
	}
	currentScopes := make(map[string]bool, len(current))
	for _, scope := range current {
		currentScopes[scope] = true
	}
	out.Scopes = make(map[string]bool)
	for _, scope := range granted {
		if scope != "admin" && currentScopes[scope] {
			out.Scopes[scope] = true
		}
	}
	devices, devicesErr := intersectDeviceLists(grantDevices, currentDevices)
	if devicesErr != nil {
		return OAuthAccessPrincipal{}, false
	}
	out.Devices = make(map[string]bool, len(devices))
	for _, device := range devices {
		out.Devices[device] = true
	}
	_ = s.TouchAPIToken(out.APITokenID, ip)
	return out, true
}
func (s *Store) RevokeOAuthToken(raw, clientID string, now int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var grantID, owner string
	err = tx.QueryRow("SELECT t.grant_id,g.client_id FROM oauth_tokens t JOIN oauth_grants g ON g.id=t.grant_id WHERE t.token_hash=?", HashAPIToken(raw)).Scan(&grantID, &owner)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if owner != clientID {
		return tx.Commit()
	}
	if _, err := tx.Exec("UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,?) WHERE id=?", now, grantID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE oauth_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE grant_id=?", now, grantID); err != nil {
		return err
	}
	return tx.Commit()
}
func revokeOAuthForAPITokenTx(tx *sql.Tx, apiTokenID string, now int64) error {
	if _, err := tx.Exec("UPDATE oauth_codes SET used_at=COALESCE(used_at,?) WHERE api_token_id=?", now, apiTokenID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,?) WHERE api_token_id=?", now, apiTokenID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE oauth_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE grant_id IN (SELECT id FROM oauth_grants WHERE api_token_id=?)", now, apiTokenID); err != nil {
		return err
	}
	return nil
}
func revokeOAuthGrantTx(tx *sql.Tx, grantID string, now int64) error {
	if _, err := tx.Exec("UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,?) WHERE id=?", now, grantID); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE oauth_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE grant_id=?", now, grantID)
	return err
}

// constrainOAuthGrantScopesTx permanently intersects existing grants with the
// API token's current scopes. Later widening the API token cannot silently
// restore scopes that were removed from an already-authorized grant.
func constrainOAuthGrantScopesTx(tx *sql.Tx, apiTokenID string, now int64) error {
	if _, err := tx.Exec("UPDATE oauth_codes SET used_at=COALESCE(used_at,?) WHERE api_token_id=?", now, apiTokenID); err != nil {
		return err
	}
	var currentJSON, currentDevicesJSON string
	var revoked, expires sql.NullInt64
	var legacy int
	if err := tx.QueryRow("SELECT scopes_json,COALESCE(devices_json,'[]'),revoked_at,expires_at,is_legacy FROM api_tokens WHERE id=?", apiTokenID).Scan(&currentJSON, &currentDevicesJSON, &revoked, &expires, &legacy); err != nil {
		return err
	}
	if revoked.Valid || legacy != 0 || (expires.Valid && expires.Int64 <= now) {
		return revokeOAuthForAPITokenTx(tx, apiTokenID, now)
	}
	var current, currentDevices []string
	if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(currentDevicesJSON), &currentDevices); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, scope := range current {
		allowed[scope] = true
	}
	type grantScopes struct{ id, raw, devices string }
	rows, err := tx.Query("SELECT id,scopes_json,devices_json FROM oauth_grants WHERE api_token_id=? AND revoked_at IS NULL", apiTokenID)
	if err != nil {
		return err
	}
	grants := []grantScopes{}
	for rows.Next() {
		var g grantScopes
		if err := rows.Scan(&g.id, &g.raw, &g.devices); err != nil {
			rows.Close()
			return err
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, grant := range grants {
		var old []string
		if json.Unmarshal([]byte(grant.raw), &old) != nil {
			if err := revokeOAuthGrantTx(tx, grant.id, now); err != nil {
				return err
			}
			continue
		}
		narrowed := make([]string, 0, len(old))
		for _, scope := range old {
			if allowed[scope] {
				narrowed = append(narrowed, scope)
			}
		}
		if len(narrowed) == 0 {
			if err := revokeOAuthGrantTx(tx, grant.id, now); err != nil {
				return err
			}
			continue
		}
		encoded, _ := json.Marshal(narrowed)
		var oldDevices []string
		if json.Unmarshal([]byte(grant.devices), &oldDevices) != nil {
			if err := revokeOAuthGrantTx(tx, grant.id, now); err != nil {
				return err
			}
			continue
		}
		narrowedDevices, devicesErr := intersectDeviceLists(oldDevices, currentDevices)
		if devicesErr != nil {
			if err := revokeOAuthGrantTx(tx, grant.id, now); err != nil {
				return err
			}
			continue
		}
		devicesEncoded, _ := json.Marshal(narrowedDevices)
		if _, err := tx.Exec("UPDATE oauth_grants SET scopes_json=?,devices_json=? WHERE id=? AND revoked_at IS NULL", string(encoded), string(devicesEncoded), grant.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RevokeOAuthGrantForAPIToken(apiTokenID string, now int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := revokeOAuthForAPITokenTx(tx, apiTokenID, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DisableOAuthClient(id string, now int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE oauth_clients SET disabled_at=COALESCE(disabled_at,?) WHERE client_id=?", now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("oauth client not found")
	}
	if _, err := tx.Exec("UPDATE oauth_codes SET used_at=COALESCE(used_at,?) WHERE client_id=?", now, id); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,?) WHERE client_id=?", now, id); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE oauth_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE grant_id IN (SELECT id FROM oauth_grants WHERE client_id=?)", now, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CleanupOAuth(now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	if _, err := s.DB.Exec("DELETE FROM oauth_codes WHERE expires_at<?", now-86400); err != nil {
		return err
	}
	if _, err := s.DB.Exec("DELETE FROM oauth_tokens WHERE expires_at<?", now-30*86400); err != nil {
		return err
	}
	_, err := s.DB.Exec("DELETE FROM oauth_grants WHERE ((revoked_at IS NOT NULL AND revoked_at<?) OR refresh_expires_at<?) AND NOT EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.grant_id=oauth_grants.id)", now-30*86400, now-30*86400)
	return err
}

type OAuthGrantRecord struct {
	ID               string   `json:"grant_id"`
	ClientID         string   `json:"client_id"`
	ClientName       string   `json:"client_name"`
	APITokenID       string   `json:"api_token_id"`
	APITokenName     string   `json:"api_token_name"`
	Scopes           []string `json:"scopes"`
	Resource         string   `json:"resource"`
	CreatedAt        int64    `json:"created_at"`
	RefreshExpiresAt int64    `json:"refresh_expires_at"`
	RevokedAt        *int64   `json:"revoked_at"`
}

func (s *Store) ListOAuthGrants() ([]OAuthGrantRecord, error) {
	rows, err := s.DB.Query("SELECT g.id,g.client_id,c.client_name,g.api_token_id,a.name,g.scopes_json,g.resource,g.created_at,g.refresh_expires_at,g.revoked_at FROM oauth_grants g JOIN oauth_clients c ON c.client_id=g.client_id JOIN api_tokens a ON a.id=g.api_token_id ORDER BY g.created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthGrantRecord{}
	for rows.Next() {
		var g OAuthGrantRecord
		var scopes string
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.APITokenID, &g.APITokenName, &scopes, &g.Resource, &g.CreatedAt, &g.RefreshExpiresAt, &g.RevokedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(scopes), &g.Scopes); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) RevokeOAuthGrant(id string, now int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,?) WHERE id=?", now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("oauth grant not found")
	}
	if _, err := tx.Exec("UPDATE oauth_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE grant_id=?", now, id); err != nil {
		return err
	}
	return tx.Commit()
}

type OAuthClientRecord struct {
	ID               string   `json:"client_id"`
	Name             string   `json:"client_name"`
	RedirectURIsJSON string   `json:"-"`
	RedirectURIs     []string `json:"redirect_uris"`
	CreatedAt        int64    `json:"created_at"`
	DisabledAt       *int64   `json:"disabled_at"`
}

func (s *Store) ListOAuthClients() ([]OAuthClientRecord, error) {
	rows, err := s.DB.Query("SELECT client_id,client_name,redirect_uris_json,created_at,disabled_at FROM oauth_clients ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthClientRecord{}
	for rows.Next() {
		var c OAuthClientRecord
		if err := rows.Scan(&c.ID, &c.Name, &c.RedirectURIsJSON, &c.CreatedAt, &c.DisabledAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(c.RedirectURIsJSON), &c.RedirectURIs); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListOAuthGrantIDsForAPIToken(apiTokenID string) ([]string, error) {
	rows, err := s.DB.Query("SELECT id FROM oauth_grants WHERE api_token_id=?", apiTokenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) OAuthGrantIDForToken(raw, clientID string) (string, bool) {
	var id string
	err := s.DB.QueryRow("SELECT g.id FROM oauth_tokens t JOIN oauth_grants g ON g.id=t.grant_id WHERE t.token_hash=? AND g.client_id=?", HashAPIToken(raw), clientID).Scan(&id)
	return id, err == nil
}

func (s *Store) ListOAuthGrantIDsForClient(clientID string) ([]string, error) {
	rows, err := s.DB.Query("SELECT id FROM oauth_grants WHERE client_id=?", clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetOAuthGrantScopes(id string) ([]string, error) {
	var raw string
	if err := s.DB.QueryRow("SELECT scopes_json FROM oauth_grants WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	var scopes []string
	if err := json.Unmarshal([]byte(raw), &scopes); err != nil {
		return nil, err
	}
	return scopes, nil
}
