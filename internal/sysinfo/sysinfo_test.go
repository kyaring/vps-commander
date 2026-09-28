package sysinfo

import (
	"testing"
)

func TestCollect(t *testing.T) {
	p := Collect("CONTROL")
	if p.Role != "CONTROL" {
		t.Fatalf("expected CONTROL, got %s", p.Role)
	}
	if p.CPUCores <= 0 {
		t.Fatalf("expected >0 CPU cores, got %d", p.CPUCores)
	}
	t.Logf("Profile: %+v", p)
}
