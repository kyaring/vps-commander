package main

import (
	"github.com/wjyhk/vps-commander/internal/sysinfo"
	"testing"
)

func TestProfileChangedEnoughThresholds(t *testing.T) {
	base := sysinfo.Profile{CPUCores: 4, CPUUsagePercent: 20, MemPercent: 40, DiskPercent: 50, Load1M: 1, BootTime: 100, UpdatedAt: 1, NetRXTotal: 100}
	minor := base
	minor.UpdatedAt = 2
	minor.NetRXTotal = 999
	minor.UptimeSeconds = 20
	if profileChangedEnough(base, minor) {
		t.Fatal("volatile timestamps/counters alone should not trigger profile update")
	}
	cpu := base
	cpu.CPUUsagePercent += 5
	if !profileChangedEnough(base, cpu) {
		t.Fatal("CPU change at threshold should trigger profile update")
	}
	memory := base
	memory.MemPercent += 3
	if !profileChangedEnough(base, memory) {
		t.Fatal("memory change at threshold should trigger profile update")
	}
}
