package storage

import (
	"testing"
	"time"
)

func TestParseNotificationTaskPolicy(t *testing.T) {
	valid := `{"repeat_interval_seconds":300,"max_reminders":5,"quiet_start":"22:00","quiet_end":"07:30","timezone":"Asia/Manila","merge_window_seconds":60,"recovery_event_types":["node_recovery"],"recovery_for_event_types":["node_offline"],"notify_recovery":true,"recovery_cooldown_seconds":120}`
	p, err := ParseNotificationTaskPolicy(valid)
	if err != nil {
		t.Fatal(err)
	}
	if p.RepeatIntervalSeconds != 300 || p.MaxReminders != 5 || p.Timezone != "Asia/Manila" || len(p.RecoveryEventTypes) != 1 {
		t.Fatalf("unexpected parsed policy: %+v", p)
	}
	for _, invalid := range []string{
		`{"repeat_interval_seconds":-1}`,
		`{"max_reminders":1001}`,
		`{"quiet_start":"22:00"}`,
		`{"quiet_start":"25:00","quiet_end":"07:00"}`,
		`{"timezone":"Mars/Olympus"}`,
		`{"unknown_option":true}`,
		`null`,
		`{} {}`,
		`{"recovery_event_types":["node_recovery"]}`,
		`{"recovery_event_types":["node_recovery"],"recovery_for_event_types":["node_offline","node_offline"]}`,
		`{"recovery_event_types":["node_recovery","node_recovery"],"recovery_for_event_types":["node_offline"]}`,
	} {
		if _, err := ParseNotificationTaskPolicy(invalid); err == nil {
			t.Errorf("invalid policy accepted: %s", invalid)
		}
	}
}

func TestNotificationQuietUntilOvernightAndDaytime(t *testing.T) {
	p := NotificationTaskPolicy{QuietStart: "22:00", QuietEnd: "07:00", Timezone: "UTC"}
	cases := []struct{ now, want string }{
		{"2026-10-11T21:59:00Z", ""},
		{"2026-10-11T22:00:00Z", "2026-10-12T07:00:00Z"},
		{"2026-10-12T03:30:00Z", "2026-10-12T07:00:00Z"},
		{"2026-10-12T07:00:00Z", ""},
	}
	for _, tc := range cases {
		now, _ := time.Parse(time.RFC3339, tc.now)
		got := NotificationQuietUntil(now, p)
		if tc.want == "" {
			if !got.IsZero() {
				t.Errorf("%s: got %s, want no quiet delay", tc.now, got)
			}
			continue
		}
		want, _ := time.Parse(time.RFC3339, tc.want)
		if !got.Equal(want) {
			t.Errorf("%s: got %s, want %s", tc.now, got, want)
		}
	}
}

func TestNotificationQuietUntilTimezone(t *testing.T) {
	p := NotificationTaskPolicy{QuietStart: "22:00", QuietEnd: "07:00", Timezone: "Asia/Manila"}
	now, _ := time.Parse(time.RFC3339, "2026-10-11T15:00:00Z") // 23:00 local
	got := NotificationQuietUntil(now, p)
	want, _ := time.Parse(time.RFC3339, "2026-10-11T23:00:00Z") // 07:00 next local day
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}
