package main

import (
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/cluster"
	"github.com/wjyhk/vps-commander/internal/sysinfo"
)

func TestProfileChangedEnoughThresholds(t *testing.T) {
	base := sysinfo.Profile{CPUCores: 4, CPUUsagePercent: 20, MemPercent: 40, DiskPercent: 50, Load1M: 1, BootTime: 100, UpdatedAt: 1, NetRXTotal: 100}
	minor := base
	minor.UpdatedAt = 2
	minor.NetRXTotal = 999
	minor.UptimeSeconds = 20
	minor.CPUUsagePercent += 4
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

func TestProfileChangedEnoughAllThresholds(t *testing.T) {
	base := sysinfo.Profile{CPUCores: 4, CPUUsagePercent: 20, MemPercent: 40, DiskPercent: 50, Load1M: 1, BootTime: 100}
	cases := []struct {
		name   string
		mutate func(*sysinfo.Profile)
	}{
		{"disk", func(p *sysinfo.Profile) { p.DiskPercent++ }},
		{"load-up", func(p *sysinfo.Profile) { p.Load1M += 0.5 }},
		{"load-down", func(p *sysinfo.Profile) { p.Load1M -= 0.5 }},
		{"boot-time", func(p *sysinfo.Profile) { p.BootTime++ }},
		{"cpu-cores", func(p *sysinfo.Profile) { p.CPUCores++ }},
		{"docker", func(p *sysinfo.Profile) { p.Docker = !p.Docker }},
		{"rx-rate", func(p *sysinfo.Profile) { p.NetRXRate = 600000 }},
		{"tx-rate", func(p *sysinfo.Profile) { p.NetTXRate = 600000 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := base
			tc.mutate(&current)
			if !profileChangedEnough(base, current) {
				t.Fatalf("expected %s change to trigger", tc.name)
			}
		})
	}
	if rateChangedEnough(100000, 120000, 20000) {
		t.Fatal("rate change below absolute threshold triggered")
	}
	if rateChangedEnough(600000, 700000, 100000) {
		t.Fatal("rate change below 1 Mbps absolute threshold triggered")
	}
	if rateChangedEnough(600000, 730000, 130000) {
		t.Fatal("rate change below 20% relative threshold triggered")
	}
}

func TestProfileMaximumIntervalForcesReport(t *testing.T) {
	base := sysinfo.Profile{CPUCores: 4, CPUUsagePercent: 20, MemPercent: 40, DiskPercent: 50, Load1M: 1, BootTime: 100}
	if shouldReportProfile(base, base, 4*time.Minute+59*time.Second, 5*time.Minute) {
		t.Fatal("unchanged profile should not report before max interval")
	}
	if !shouldReportProfile(base, base, 5*time.Minute, 5*time.Minute) {
		t.Fatal("unchanged profile must report at max interval")
	}
}

func TestProfileIntervalHubConfigurationCompatibility(t *testing.T) {
	if got := profileIntervalForMessage(5*time.Minute, cluster.Message{Event: "ping"}); got != 5*time.Minute {
		t.Fatalf("legacy Hub ping changed interval: got %s", got)
	}
	if got := profileIntervalForMessage(5*time.Minute, cluster.Message{Event: "ping", ProfileIntervalSeconds: 600}); got != 10*time.Minute {
		t.Fatalf("Hub interval not applied: got %s", got)
	}
	for _, seconds := range []int{1, 59, 3601} {
		if got := profileIntervalForMessage(5*time.Minute, cluster.Message{ProfileIntervalSeconds: seconds}); got != 5*time.Minute {
			t.Fatalf("invalid interval %d should preserve current interval, got %s", seconds, got)
		}
	}
}

func TestJitteredBackoffBounds(t *testing.T) {
	base, max := 10*time.Second, 30*time.Second
	if got := jitteredBackoff(base, max, 0); got != 8*time.Second {
		t.Fatalf("minimum jitter=%s, want 8s", got)
	}
	if got := jitteredBackoff(base, max, 0.5); got != 10*time.Second {
		t.Fatalf("midpoint jitter=%s, want 10s", got)
	}
	if got := jitteredBackoff(base, max, 1); got != 12*time.Second {
		t.Fatalf("maximum jitter=%s, want 12s", got)
	}
	if got := jitteredBackoff(40*time.Second, max, 1); got != max {
		t.Fatalf("capped jitter=%s, want %s", got, max)
	}
}
