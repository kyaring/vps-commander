package notify

import (
	"strings"
	"testing"
	"time"
)

func TestOfflinePersistDoesNotRepeatStageAfterRestart(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	start := clock.Unix()
	setDevice(t, st, "persist-node", "offline", start)
	*clock = time.Unix(start, 0).Add(61 * time.Second)
	m.check()
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "node_offline") {
		t.Fatalf("first stage notifications = %v, want one offline alert", *msgs)
	}

	restarted := New(st)
	restarted.Client = m.Client
	restarted.clock = func() time.Time { return *clock }
	*clock = time.Unix(start, 0).Add(5 * time.Minute)
	restarted.check()
	if len(*msgs) != 1 {
		t.Fatalf("restart repeated first stage: %v", *msgs)
	}
	*clock = time.Unix(start, 0).Add(10*time.Minute + time.Second)
	restarted.check()
	if len(*msgs) != 2 || !strings.Contains((*msgs)[1], "node_offline") || !strings.Contains((*msgs)[1], "10 分钟") {
		t.Fatalf("next stage notifications = %v, want only stage 2", *msgs)
	}
	states, err := st.ListOfflineStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Sent != 2 || !states[0].Notified {
		t.Fatalf("persisted state = %#v, want sent=2 notified=true", states)
	}
}

func TestOfflinePersistRecoveryAfterRestartDeletesState(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	start := clock.Unix()
	setDevice(t, st, "recovery-node", "offline", start)
	*clock = time.Unix(start, 0).Add(61 * time.Second)
	m.check()
	if len(*msgs) != 1 {
		t.Fatalf("offline notification count = %d, want 1", len(*msgs))
	}

	restarted := New(st)
	restarted.Client = m.Client
	restarted.clock = func() time.Time { return *clock }
	setDevice(t, st, "recovery-node", "online", nil)
	*clock = time.Unix(start, 0).Add(5 * time.Minute)
	restarted.check()
	if len(*msgs) != 2 || !strings.Contains((*msgs)[1], "node_recovery") {
		t.Fatalf("notifications after recovery = %v, want node_recovery", *msgs)
	}
	states, err := st.ListOfflineStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("recovery left persisted states: %#v", states)
	}
}

func TestOfflinePersistOfflineAtChangeStartsNewEpisode(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	start := clock.Unix()
	setDevice(t, st, "changed-episode", "offline", start)
	*clock = time.Unix(start, 0).Add(61 * time.Second)
	m.check()
	newStart := start + 5*60
	setDevice(t, st, "changed-episode", "offline", newStart)
	*clock = time.Unix(newStart, 0).Add(61 * time.Second)
	m.check()
	if len(*msgs) != 2 || !strings.Contains((*msgs)[0], "node_offline") || !strings.Contains((*msgs)[1], "node_offline") {
		t.Fatalf("new episode notifications = %v, want one alert for each episode", *msgs)
	}
	states, err := st.ListOfflineStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Since != newStart || states[0].Sent != 1 || !states[0].Notified {
		t.Fatalf("new episode state = %#v, want since=%d sent=1 notified=true", states, newStart)
	}
}

func TestOfflinePersistRemovedDeviceStateIsCleanedWithoutNotification(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	start := clock.Unix()
	setDevice(t, st, "removed-node", "offline", start)
	*clock = time.Unix(start, 0).Add(61 * time.Second)
	m.check()
	before := len(*msgs)
	if err := st.DeleteDevice("removed-node"); err != nil {
		t.Fatal(err)
	}
	restarted := New(st)
	restarted.Client = m.Client
	restarted.clock = func() time.Time { return *clock }
	restarted.check()
	if len(*msgs) != before {
		t.Fatalf("deleted device caused a notification: before=%v after=%v", before, *msgs)
	}
	states, err := st.ListOfflineStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("deleted device state was not cleaned: %#v", states)
	}
}

func TestOfflinePersistLateEpisodeRecoveryRemainsQuiet(t *testing.T) {
	m, st, msgs, clock := offlineFixture(t)
	start := clock.Unix()
	setDevice(t, st, "late-node", "offline", start)
	*clock = time.Unix(start, 0).Add(5 * time.Hour)
	m.check()
	if len(*msgs) != 0 {
		t.Fatalf("late-start episode emitted offline alert: %v", *msgs)
	}
	restarted := New(st)
	restarted.Client = m.Client
	restarted.clock = func() time.Time { return *clock }
	setDevice(t, st, "late-node", "online", nil)
	*clock = clock.Add(time.Minute)
	restarted.check()
	if len(*msgs) != 0 {
		t.Fatalf("late-start recovery should remain quiet, got %v", *msgs)
	}
	states, err := st.ListOfflineStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("late episode state not deleted on recovery: %#v", states)
	}
}
