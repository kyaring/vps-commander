package main

import (
	"errors"
	"net/http"
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

func TestFullJitterBackoffBounds(t *testing.T) {
	base, max := 10*time.Second, 30*time.Second
	if got := fullJitterBackoff(base, max, 0); got != 0 {
		t.Fatalf("minimum full jitter=%s, want 0s", got)
	}
	if got := fullJitterBackoff(base, max, 0.5); got != 5*time.Second {
		t.Fatalf("midpoint full jitter=%s, want 5s", got)
	}
	if got := fullJitterBackoff(base, max, 1); got != 10*time.Second {
		t.Fatalf("maximum full jitter=%s, want 10s", got)
	}
	if got := fullJitterBackoff(40*time.Second, max, 1); got != max {
		t.Fatalf("capped full jitter=%s, want %s", got, max)
	}
	if got := fullJitterBackoff(-time.Second, max, 0.5); got != 0 {
		t.Fatalf("negative base should clamp to zero, got %s", got)
	}
}

func TestInitialReconnectDelayAddsZeroToTenSeconds(t *testing.T) {
	base, max := time.Second, 30*time.Second
	if got := initialReconnectDelay(base, max, 0, 0); got != 0 {
		t.Fatalf("minimum initial reconnect delay=%s, want 0s", got)
	}
	if got := initialReconnectDelay(base, max, 1, 1); got != 11*time.Second {
		t.Fatalf("maximum initial reconnect delay=%s, want 11s", got)
	}
	if got := initialReconnectDelay(base, max, 0.5, 0.5); got != 5500*time.Millisecond {
		t.Fatalf("midpoint initial reconnect delay=%s, want 5.5s", got)
	}
}

func TestReconnectDelayResetsAfterEachStableSession(t *testing.T) {
	backoff := time.Second
	firstReconnect := true
	for round := 1; round <= 2; round++ {
		if !firstReconnect {
			t.Fatalf("round %d should be treated as a first reconnect", round)
		}
		delay := initialReconnectDelay(backoff, 30*time.Second, 0.5, 0.5)
		if delay < 5*time.Second || delay > 6*time.Second {
			t.Fatalf("round %d delay=%s, want 0–10s initial spread plus full jitter", round, delay)
		}
		firstReconnect = false // the initial reconnect delay has been consumed
		prepareReconnectAfterConnection(&backoff, &firstReconnect, true)
		if backoff != time.Second {
			t.Fatalf("round %d backoff=%s, want reset to 1s", round, backoff)
		}
	}
}

func TestDialFailureHonorsRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"3"}}}
	err := newDialFailure(errors.New("bad handshake"), resp)
	var failure *dialFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected dialFailure, got %T", err)
	}
	if failure.statusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", failure.statusCode)
	}
	if got := retryAfterFromError(err); got != 3*time.Second {
		t.Fatalf("Retry-After=%s, want 3s", got)
	}
	if got := applyRetryAfter(200*time.Millisecond, err); got != 3*time.Second {
		t.Fatalf("retry delay=%s, want to honor 3s Retry-After", got)
	}
	if got := applyRetryAfter(5*time.Second, err); got != 5*time.Second {
		t.Fatalf("retry delay=%s, should retain longer randomized delay", got)
	}
}

func TestDialFailureWithoutResponseKeepsOriginalError(t *testing.T) {
	want := errors.New("connection refused")
	if got := newDialFailure(want, nil); got != want {
		t.Fatalf("error without response changed: got %v", got)
	}
}
