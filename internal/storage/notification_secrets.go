package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const notificationSecretKeyEnv = "VPS_COMMANDER_NOTIFICATION_KEY"

var notificationSecretAAD = []byte("vps-commander:notification-secrets:v1")

func loadNotificationSecretKey() ([]byte, error) {
	raw := os.Getenv(notificationSecretKeyEnv)
	if raw == "" {
		return nil, nil
	}
	var key []byte
	var err error
	if len(raw) == 64 {
		key, err = hex.DecodeString(raw)
	} else {
		key, err = base64.StdEncoding.DecodeString(raw)
	}
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must be a 32-byte key encoded as 64 hex characters or base64", notificationSecretKeyEnv)
	}
	return key, nil
}

// EncryptNotificationSecrets encrypts provider credentials with AES-256-GCM.
// The key is supplied out-of-band through VPS_COMMANDER_NOTIFICATION_KEY so a
// database backup alone does not contain the decryption key.
func (s *Store) EncryptNotificationSecrets(plaintext []byte) ([]byte, error) {
	return s.encryptNotificationSecretsWithAAD(plaintext, notificationSecretAAD)
}

func (s *Store) EncryptNotificationChannelSecrets(channelID string, plaintext []byte) ([]byte, error) {
	return s.encryptNotificationSecretsWithAAD(plaintext, []byte("vps-commander:notification-secrets:v1:channel:"+channelID))
}

func (s *Store) encryptNotificationSecretsWithAAD(plaintext, aad []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	if len(s.notificationSecretKey) != 32 {
		return nil, errors.New("notification secret encryption key is not configured")
	}
	block, err := aes.NewCipher(s.notificationSecretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 1, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	out[0] = 1 // envelope version
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, aad)
	return out, nil
}

// DecryptNotificationSecrets decrypts a versioned AES-GCM secret envelope.
func (s *Store) DecryptNotificationSecrets(ciphertext []byte) ([]byte, error) {
	return s.decryptNotificationSecretsWithAAD(ciphertext, notificationSecretAAD)
}

func (s *Store) DecryptNotificationChannelSecrets(channelID string, ciphertext []byte) ([]byte, error) {
	return s.decryptNotificationSecretsWithAAD(ciphertext, []byte("vps-commander:notification-secrets:v1:channel:"+channelID))
}

func (s *Store) decryptNotificationSecretsWithAAD(ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	if len(s.notificationSecretKey) != 32 {
		return nil, errors.New("notification secret encryption key is not configured")
	}
	if ciphertext[0] != 1 {
		return nil, fmt.Errorf("unsupported notification secret envelope version %d", ciphertext[0])
	}
	block, err := aes.NewCipher(s.notificationSecretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < 1+gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("notification secret envelope is truncated")
	}
	nonce := ciphertext[1 : 1+gcm.NonceSize()]
	body := ciphertext[1+gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, aad)
}
