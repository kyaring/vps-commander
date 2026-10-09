package cluster

import (
	"testing"
	"time"
)

func TestSetProfileIntervalBounds(t *testing.T) {
	m := NewManager("", nil)
	m.SetProfileInterval(10 * time.Second)
	if m.ProfileInterval != time.Minute {
		t.Fatalf("minimum interval=%s, want 1m", m.ProfileInterval)
	}
	m.SetProfileInterval(10 * time.Minute)
	if m.ProfileInterval != 10*time.Minute {
		t.Fatalf("interval=%s, want 10m", m.ProfileInterval)
	}
	m.SetProfileInterval(2 * time.Hour)
	if m.ProfileInterval != time.Hour {
		t.Fatalf("maximum interval=%s, want 1h", m.ProfileInterval)
	}
}
