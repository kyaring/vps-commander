package web

import (
	"strings"
	"testing"
)

func TestNotificationChannelConfigPayloadIsObject(t *testing.T) {
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)

	functionStart := strings.Index(js, "async function saveNotificationChannel()")
	if functionStart < 0 {
		t.Fatal("app.js does not define saveNotificationChannel")
	}
	functionEndRel := strings.Index(js[functionStart:], "function resetNotificationTemplateForm()")
	if functionEndRel < 0 {
		t.Fatal("could not find the end of saveNotificationChannel")
	}
	functionBody := js[functionStart : functionStart+functionEndRel]
	if !strings.Contains(functionBody, "config_json:config") {
		t.Error("saveNotificationChannel must send config_json as an object")
	}
	if strings.Contains(functionBody, "config_json:JSON.stringify(") {
		t.Error("saveNotificationChannel must not stringify config_json")
	}
	if !strings.Contains(js, "variables_json:JSON.stringify(variables)") {
		t.Error("variables_json serialization must remain unchanged")
	}
	if !strings.Contains(js, "policy_json:JSON.stringify(policy)") {
		t.Error("policy_json serialization must remain unchanged")
	}
}
