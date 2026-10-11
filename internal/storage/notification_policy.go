package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// NotificationTaskPolicy is deliberately declarative: policy decisions are
// persisted separately from the durable event and delivery records.
type NotificationTaskPolicy struct {
	RepeatIntervalSeconds   int64    `json:"repeat_interval_seconds,omitempty"`
	MaxReminders            int      `json:"max_reminders,omitempty"`
	QuietStart              string   `json:"quiet_start,omitempty"` // local HH:MM, inclusive
	QuietEnd                string   `json:"quiet_end,omitempty"`   // local HH:MM, exclusive
	Timezone                string   `json:"timezone,omitempty"`    // IANA name; defaults to UTC
	MergeWindowSeconds      int64    `json:"merge_window_seconds,omitempty"`
	RecoveryEventTypes      []string `json:"recovery_event_types,omitempty"`
	RecoveryForEventTypes   []string `json:"recovery_for_event_types,omitempty"`
	NotifyRecovery          *bool    `json:"notify_recovery,omitempty"`
	RecoveryCooldownSeconds int64    `json:"recovery_cooldown_seconds,omitempty"`
}

var notificationClockRE = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

func ParseNotificationTaskPolicy(raw string) (NotificationTaskPolicy, error) {
	var p NotificationTaskPolicy
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if strings.TrimSpace(raw) == "null" {
		return p, errors.New("task policy must be a JSON object")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("task policy must be a valid policy object: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return p, errors.New("task policy must contain only one JSON object")
	} else if err != io.EOF {
		return p, errors.New("task policy has invalid trailing data")
	}
	if p.RepeatIntervalSeconds < 0 || p.RepeatIntervalSeconds > 30*24*60*60 {
		return p, errors.New("repeat_interval_seconds must be between 0 and 2592000")
	}
	if p.MaxReminders < 0 || p.MaxReminders > 1000 {
		return p, errors.New("max_reminders must be between 0 and 1000")
	}
	if p.MergeWindowSeconds < 0 || p.MergeWindowSeconds > 24*60*60 {
		return p, errors.New("merge_window_seconds must be between 0 and 86400")
	}
	if p.RecoveryCooldownSeconds < 0 || p.RecoveryCooldownSeconds > 30*24*60*60 {
		return p, errors.New("recovery_cooldown_seconds must be between 0 and 2592000")
	}
	if (p.QuietStart == "") != (p.QuietEnd == "") {
		return p, errors.New("quiet_start and quiet_end must be set together")
	}
	if p.QuietStart != "" && (!notificationClockRE.MatchString(p.QuietStart) || !notificationClockRE.MatchString(p.QuietEnd)) {
		return p, errors.New("quiet_start and quiet_end must use HH:MM")
	}
	if p.QuietStart != "" && p.QuietStart == p.QuietEnd {
		return p, errors.New("quiet window cannot have identical start and end")
	}
	if p.Timezone != "" {
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return p, errors.New("timezone must be a valid IANA time zone")
		}
	}
	if len(p.RecoveryEventTypes) > 32 || len(p.RecoveryForEventTypes) > 32 {
		return p, errors.New("recovery event type lists may contain at most 32 event types each")
	}
	if len(p.RecoveryEventTypes) > 0 && len(p.RecoveryForEventTypes) == 0 {
		return p, errors.New("recovery_for_event_types is required when recovery_event_types is configured")
	}
	if len(p.RecoveryEventTypes) == 0 && len(p.RecoveryForEventTypes) > 0 {
		return p, errors.New("recovery_event_types is required when recovery_for_event_types is configured")
	}
	for _, group := range [][]string{p.RecoveryEventTypes, p.RecoveryForEventTypes} {
		seen := map[string]bool{}
		for _, eventType := range group {
			eventType = strings.TrimSpace(eventType)
			if eventType == "" || eventType == "*" || len(eventType) > 128 {
				return p, errors.New("recovery event type list contains an invalid event type")
			}
			for _, ch := range eventType {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("._:-", ch)) {
					return p, errors.New("recovery event type list contains an invalid event type")
				}
			}
			if seen[eventType] {
				return p, fmt.Errorf("duplicate recovery event type %q", eventType)
			}
			seen[eventType] = true
		}
	}
	return p, nil
}

// NotificationQuietUntil returns the first allowed delivery time. A zero
// return means the current instant is outside the quiet window.
func NotificationQuietUntil(now time.Time, p NotificationTaskPolicy) time.Time {
	if p.QuietStart == "" || p.QuietEnd == "" {
		return time.Time{}
	}
	loc := time.UTC
	if p.Timezone != "" {
		if loaded, err := time.LoadLocation(p.Timezone); err == nil {
			loc = loaded
		}
	}
	localNow := now.In(loc)
	startHour, startMinute := parseNotificationClock(p.QuietStart)
	endHour, endMinute := parseNotificationClock(p.QuietEnd)
	start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), startHour, startMinute, 0, 0, loc)
	end := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), endHour, endMinute, 0, 0, loc)
	if !end.After(start) { // overnight window
		if !localNow.Before(start) {
			end = end.AddDate(0, 0, 1)
		} else {
			start = start.AddDate(0, 0, -1)
		}
	}
	if !localNow.Before(start) && localNow.Before(end) {
		return end
	}
	return time.Time{}
}

func parseNotificationClock(value string) (int, int) {
	var hour, minute int
	_, _ = fmt.Sscanf(value, "%d:%d", &hour, &minute)
	return hour, minute
}
