package storage

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestNotificationSecretEncryptionRoundTripAndTamperDetection(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{0x5a}, 32)))
	store, err := Open(t.TempDir() + "/notification-secrets.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	plain := []byte(`{"bot_token":"never-return-this","chat_id":"12345"}`)
	sealed, err := store.EncryptNotificationSecrets(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("never-return-this")) {
		t.Fatal("ciphertext contains plaintext secret")
	}
	opened, err := store.DecryptNotificationSecrets(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("decrypted=%q want=%q", opened, plain)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := store.DecryptNotificationSecrets(sealed); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestNotificationSecretEncryptionFailsClosedWithoutKey(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, "")
	store, err := Open(t.TempDir() + "/notification-secrets-no-key.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.EncryptNotificationSecrets([]byte(`{"token":"x"}`)); err == nil {
		t.Fatal("secret encryption succeeded without a key")
	}
}

func TestNotificationSecretKeyRejectsInvalidLength(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, "too-short")
	if _, err := Open(t.TempDir() + "/notification-secrets-invalid-key.db"); err == nil {
		t.Fatal("invalid encryption key was accepted")
	}
}

func TestSaveNotificationChannelEncryptsAndPreservesSecrets(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{0x2c}, 32)))
	store, err := Open(t.TempDir() + "/notification-channel.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channel := NotificationChannel{ID: "tg-alerts", Name: "Telegram alerts", Type: "telegram_bot", ConfigJSON: `{"chat_id":"12345"}`, Enabled: true}
	if err := store.SaveNotificationChannel(channel, `{"bot_token":"super-secret-token"}`); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := store.DB.QueryRow(`SELECT secrets_ciphertext FROM notification_channels WHERE id=?`, channel.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("super-secret-token")) {
		t.Fatal("database contains plaintext bot token")
	}
	opened, err := store.GetNotificationChannelSecrets(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(opened, []byte("super-secret-token")) {
		t.Fatalf("decrypted secret missing token: %s", opened)
	}
	channel.Name = "Telegram alerts renamed"
	if err := store.SaveNotificationChannel(channel, ""); err != nil {
		t.Fatal(err)
	}
	opened, err = store.GetNotificationChannelSecrets(channel.ID)
	if err != nil || !bytes.Contains(opened, []byte("super-secret-token")) {
		t.Fatalf("secret not preserved on blank update: %s err=%v", opened, err)
	}
}

func TestQQOfficialChannelIsStoredDisabledAndCannotHaveActiveTask(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{0x19}, 32)))
	store, err := Open(t.TempDir() + "/qq-channel.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channel := NotificationChannel{ID: "qq-ops", Name: "QQ official", Type: "qq_official_bot", ConfigJSON: `{"app_id":"123","target_type":"group","target_id":"group-openid"}`, Enabled: true}
	if err := store.SaveNotificationChannel(channel, `{"app_secret":"app-secret-value"}`); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetNotificationChannel(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.LastStatus != "unsupported" {
		t.Fatalf("QQ channel should be disabled and unsupported for proactive alerts: %+v", got)
	}
	if err := store.SaveNotificationTask(NotificationTask{ID: "qq-task", Name: "QQ task", ChannelID: channel.ID, TemplateID: legacyEventTemplateID, Enabled: true}); err == nil {
		t.Fatal("enabled proactive task for QQ official bot was accepted")
	}
}
