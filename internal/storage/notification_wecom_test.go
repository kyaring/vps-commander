package storage

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestWeComChannelValidationAndSecretRetention(t *testing.T) {
	t.Setenv(notificationSecretKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{0x47}, 32)))
	store, err := Open(t.TempDir() + "/wecom.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const key = "hark-test-wecom-key"
	webhookURL := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + key
	channel := NotificationChannel{ID: "wecom-alerts", Name: "WeCom alerts", Type: "wecom_bot", ConfigJSON: `{"mentioned_list":["@all","ops"]}`, Enabled: true}
	if err := store.SaveNotificationChannel(channel, ""); err == nil {
		t.Fatal("new WeCom channel without secrets was accepted")
	}
	for _, invalidURL := range []string{
		"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + key,
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
		"https://example.com/cgi-bin/webhook/send?key=" + key,
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=",
	} {
		if err := store.SaveNotificationChannel(channel, `{"webhook_url":"`+invalidURL+`"}`); err == nil {
			t.Fatalf("invalid URL accepted: %q", invalidURL)
		}
	}
	channel.ConfigJSON = `{"mentioned_list":["ops"],"other":"no"}`
	if err := store.SaveNotificationChannel(channel, `{"webhook_url":"`+webhookURL+`"}`); err == nil {
		t.Fatal("unsupported config key was accepted")
	}
	channel.ConfigJSON = `{"mentioned_list":["@all","ops"]}`
	if err := store.SaveNotificationChannel(channel, `{"webhook_url":"`+webhookURL+`"}`); err != nil {
		t.Fatalf("create WeCom channel: %v", err)
	}

	var ciphertext []byte
	if err := store.DB.QueryRow(`SELECT secrets_ciphertext FROM notification_channels WHERE id=?`, channel.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(key)) || strings.Contains(string(ciphertext), webhookURL) {
		t.Fatal("WeCom webhook URL/key appears in plaintext ciphertext storage")
	}
	opened, err := store.GetNotificationChannelSecrets(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(opened, []byte(webhookURL)) {
		t.Fatalf("decrypted secrets did not retain webhook_url: %s", opened)
	}

	channel.Name = "WeCom alerts renamed"
	if err := store.SaveNotificationChannel(channel, ""); err != nil {
		t.Fatalf("update while retaining credentials: %v", err)
	}
	opened, err = store.GetNotificationChannelSecrets(channel.ID)
	if err != nil || !bytes.Contains(opened, []byte(key)) {
		t.Fatalf("secret not retained on blank update: %s err=%v", opened, err)
	}

	channel.ConfigJSON = `{"mentioned_list":["too-long-entry"]}`
	tooLong := strings.Repeat("x", 65)
	channel.ConfigJSON = `{"mentioned_list":["` + tooLong + `"]}`
	if err := store.SaveNotificationChannel(channel, ""); err == nil {
		t.Fatal("overlong mentioned_list entry was accepted")
	}
}

func TestDefaultTemplateMigrationIsIdempotentAndPreservesEdits(t *testing.T) {
	path := t.TempDir() + "/default-template.db"
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetNotificationTemplate("builtin-default")
	if err != nil {
		t.Fatal(err)
	}
	if got.EventType != "custom.default" || got.Severity != "P3" || got.Format != "text" || got.TitleTemplate != "{{title}}" || got.BodyTemplate != "{{message}}" || got.VariablesJSON != `["title","message"]` || !got.Enabled {
		t.Fatalf("unexpected builtin-default template: %+v", got)
	}
	got.BodyTemplate = "custom body: {{message}}"
	if err := store.SaveNotificationTemplate(got); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err = store.GetNotificationTemplate("builtin-default")
	if err != nil {
		t.Fatal(err)
	}
	if got.BodyTemplate != "custom body: {{message}}" {
		t.Fatalf("migration overwrote manually edited template body: %q", got.BodyTemplate)
	}
}
