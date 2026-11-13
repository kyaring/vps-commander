package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DevProvider is a model provider registration. Secret material (base URL and API key) is kept
// only as sealed bytes in sealed; the plaintext never leaves the sealing functions below.
type DevProvider struct {
	ID          string
	DisplayName string
	Enabled     bool
	KeySet      bool
	sealed      []byte
	CreatedAt   int64
	UpdatedAt   int64
}

// ErrDevProviderNotFound is returned for an unknown provider id.
var ErrDevProviderNotFound = errors.New("devtask provider not found")

const devProviderSchema = `
CREATE TABLE IF NOT EXISTS dev_model_providers (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	key_set INTEGER NOT NULL DEFAULT 0,
	sealed_blob BLOB,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);`

func (s *Store) ensureDevProviderSchema() error {
	_, err := s.DB.Exec(devProviderSchema)
	return err
}

// SealProviderSecret encrypts plain with AES-256-GCM. The provider id is bound as additional data,
// so a sealed value copied to another provider fails to open. Output is nonce || ciphertext.
func SealProviderSecret(key []byte, providerID string, plain []byte) ([]byte, error) {
	gcm, err := providerGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, []byte(providerID)), nil
}

// OpenProviderSecret reverses SealProviderSecret.
func OpenProviderSecret(key []byte, providerID string, sealed []byte) ([]byte, error) {
	gcm, err := providerGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("sealed provider secret too short")
	}
	ns := gcm.NonceSize()
	return gcm.Open(nil, sealed[:ns], sealed[ns:], []byte(providerID))
}

func providerGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("provider key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ListDevProviders returns every provider, ordered by id.
func (s *Store) ListDevProviders() ([]DevProvider, error) {
	if err := s.ensureDevProviderSchema(); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query("SELECT id, display_name, enabled, key_set, sealed_blob, created_at, updated_at FROM dev_model_providers ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DevProvider
	for rows.Next() {
		p, err := scanDevProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetDevProvider returns one provider by id.
func (s *Store) GetDevProvider(id string) (DevProvider, error) {
	if err := s.ensureDevProviderSchema(); err != nil {
		return DevProvider{}, err
	}
	p, err := scanDevProvider(s.DB.QueryRow("SELECT id, display_name, enabled, key_set, sealed_blob, created_at, updated_at FROM dev_model_providers WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return DevProvider{}, ErrDevProviderNotFound
	}
	return p, err
}

func scanDevProvider(sc interface{ Scan(...any) error }) (DevProvider, error) {
	var p DevProvider
	var enabled, keySet int
	err := sc.Scan(&p.ID, &p.DisplayName, &enabled, &keySet, &p.sealed, &p.CreatedAt, &p.UpdatedAt)
	p.Enabled, p.KeySet = enabled != 0, keySet != 0
	return p, err
}

// Sealed returns a copy of the sealed secret bytes (nil when no key is stored). Only the API
// layer calls it, to open the secret for a permitted operation.
func (p DevProvider) Sealed() []byte {
	return append([]byte(nil), p.sealed...)
}

// UpsertDevProviderMeta stores the non-secret fields and keeps any sealed secret unchanged.
func (s *Store) UpsertDevProviderMeta(id, displayName string, enabled bool) error {
	if err := s.ensureDevProviderSchema(); err != nil {
		return err
	}
	now := time.Now().Unix()
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.DB.Exec(`INSERT INTO dev_model_providers(id, display_name, enabled, key_set, sealed_blob, created_at, updated_at)
		VALUES(?,?,?,0,NULL,?,?)
		ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		id, displayName, en, now, now)
	return err
}

// SetDevProviderSecret replaces the sealed secret. A nil sealed value clears the key.
func (s *Store) SetDevProviderSecret(id string, sealed []byte) error {
	if err := s.ensureDevProviderSchema(); err != nil {
		return err
	}
	keySet := 0
	if len(sealed) > 0 {
		keySet = 1
	}
	res, err := s.DB.Exec("UPDATE dev_model_providers SET sealed_blob=?, key_set=?, updated_at=? WHERE id=?", sealed, keySet, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDevProviderNotFound
	}
	return nil
}
