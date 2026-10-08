package storage

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type APIToken struct {
	ID          string
	Name        string
	Prefix      string
	TokenHash   string
	ScopesJSON  string
	DevicesJSON string
	RateRPS     int
	MaxSessions int
	Note        string
	CreatedAt   int64
	CreatedBy   string
	ExpiresAt   *int64
	LastUsedAt  *int64
	LastUsedIP  string
	RevokedAt   *int64
	IsLegacy    bool
}

func HashAPIToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Store) EnsureAPITokens(legacyKey string) error {
	_, err := s.DB.Exec("CREATE TABLE IF NOT EXISTS api_tokens (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL UNIQUE, token_hash TEXT NOT NULL, scopes_json TEXT NOT NULL, devices_json TEXT, rate_rps INTEGER NOT NULL DEFAULT 10, max_sessions INTEGER NOT NULL DEFAULT 10, note TEXT, created_at INTEGER NOT NULL, created_by TEXT NOT NULL, expires_at INTEGER, last_used_at INTEGER, last_used_ip TEXT, revoked_at INTEGER, is_legacy INTEGER NOT NULL DEFAULT 0)")
	if err != nil {
		return err
	}
	if _, err = s.DB.Exec("CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash)"); err != nil {
		return err
	}
	if legacyKey == "" {
		return nil
	}
	var n int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM api_tokens").Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return nil
	}
	now := time.Now().Unix()
	_, err = s.DB.Exec("INSERT INTO api_tokens(id,name,prefix,token_hash,scopes_json,rate_rps,max_sessions,created_at,created_by,is_legacy) VALUES(?,?,?,?,?,?,?,?,?,1)",
		"legacy-env", "legacy-env", legacyPrefix(legacyKey), HashAPIToken(legacyKey),
		`["devices:read","files:read","files:write","exec","process","mcp:call","admin"]`, 10, 10, now, "env:bootstrap")
	return err
}

func legacyPrefix(token string) string {
	if len(token) > 8 {
		return token[:8]
	}
	return token
}

func (s *Store) CreateAPIToken(t APIToken) error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Prefix) == "" || t.TokenHash == "" {
		return errors.New("token id, name, prefix and hash required")
	}
	if t.RateRPS <= 0 {
		t.RateRPS = 10
	}
	if t.MaxSessions <= 0 {
		t.MaxSessions = 10
	}
	_, err := s.DB.Exec("INSERT INTO api_tokens(id,name,prefix,token_hash,scopes_json,devices_json,rate_rps,max_sessions,note,created_at,created_by,expires_at,last_used_at,last_used_ip,revoked_at,is_legacy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		t.ID, t.Name, t.Prefix, t.TokenHash, t.ScopesJSON, t.DevicesJSON, t.RateRPS, t.MaxSessions, t.Note, t.CreatedAt, t.CreatedBy, t.ExpiresAt, t.LastUsedAt, t.LastUsedIP, t.RevokedAt, boolInt(t.IsLegacy))
	return err
}

func (s *Store) GetAPITokenByPrefix(prefix string) (APIToken, error) {
	var t APIToken
	var legacy int
	row := s.DB.QueryRow("SELECT id,name,prefix,token_hash,scopes_json,COALESCE(devices_json,''),rate_rps,max_sessions,COALESCE(note,''),created_at,created_by,expires_at,last_used_at,COALESCE(last_used_ip,''),revoked_at,is_legacy FROM api_tokens WHERE prefix=?", prefix)
	err := row.Scan(&t.ID, &t.Name, &t.Prefix, &t.TokenHash, &t.ScopesJSON, &t.DevicesJSON, &t.RateRPS, &t.MaxSessions, &t.Note, &t.CreatedAt, &t.CreatedBy, &t.ExpiresAt, &t.LastUsedAt, &t.LastUsedIP, &t.RevokedAt, &legacy)
	t.IsLegacy = legacy != 0
	return t, err
}

func (s *Store) ListAPITokens() ([]APIToken, error) {
	rows, err := s.DB.Query("SELECT id,name,prefix,token_hash,scopes_json,COALESCE(devices_json,''),rate_rps,max_sessions,COALESCE(note,''),created_at,created_by,expires_at,last_used_at,COALESCE(last_used_ip,''),revoked_at,is_legacy FROM api_tokens ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var legacy int
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.TokenHash, &t.ScopesJSON, &t.DevicesJSON, &t.RateRPS, &t.MaxSessions, &t.Note, &t.CreatedAt, &t.CreatedBy, &t.ExpiresAt, &t.LastUsedAt, &t.LastUsedIP, &t.RevokedAt, &legacy); err != nil {
			return nil, err
		}
		t.IsLegacy = legacy != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAPIToken(id, name, scopesJSON, devicesJSON string, rateRPS, maxSessions int, note string, expiresAt *int64) error {
	if rateRPS <= 0 {
		rateRPS = 10
	}
	if maxSessions <= 0 {
		maxSessions = 10
	}
	r, err := s.DB.Exec("UPDATE api_tokens SET name=?,scopes_json=?,devices_json=?,rate_rps=?,max_sessions=?,note=?,expires_at=? WHERE id=?", name, scopesJSON, devicesJSON, rateRPS, maxSessions, note, expiresAt, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token not found")
	}
	return nil
}

func (s *Store) RevokeAPIToken(id string) error {
	now := time.Now().Unix()
	r, err := s.DB.Exec("UPDATE api_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL", now, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token not found or already revoked")
	}
	return nil
}

func (s *Store) DeleteAPIToken(id string) error {
	r, err := s.DB.Exec("DELETE FROM api_tokens WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token not found")
	}
	return nil
}

func (s *Store) TouchAPIToken(id, ip string) error {
	_, err := s.DB.Exec("UPDATE api_tokens SET last_used_at=?,last_used_ip=? WHERE id=?", time.Now().Unix(), ip, id)
	return err
}

func (s *Store) ValidateAPIToken(token, ip string) (string, string, map[string]bool, map[string]bool, bool) {
	if len(token) == 0 {
		return "", "", nil, nil, false
	}
	prefix := token
	if strings.HasPrefix(token, "vpc_") {
		if i := strings.IndexByte(token[4:], '_'); i >= 0 {
			prefix = token[4 : 4+i]
		}
	} else if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	t, err := s.GetAPITokenByPrefix(prefix)
	if err != nil || t.RevokedAt != nil || (t.ExpiresAt != nil && *t.ExpiresAt <= time.Now().Unix()) {
		return "", "", nil, nil, false
	}
	got := HashAPIToken(token)
	if subtle.ConstantTimeCompare([]byte(got), []byte(t.TokenHash)) != 1 {
		return "", "", nil, nil, false
	}
	var scopes []string
	_ = json.Unmarshal([]byte(t.ScopesJSON), &scopes)
	sm := map[string]bool{}
	for _, v := range scopes {
		sm[v] = true
	}
	var devices []string
	_ = json.Unmarshal([]byte(t.DevicesJSON), &devices)
	dm := map[string]bool{}
	for _, v := range devices {
		dm[v] = true
	}
	_ = s.TouchAPIToken(t.ID, ip)
	return t.ID, t.Name, sm, dm, true
}
