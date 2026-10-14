package api

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseSystemdServiceStateProductionPropertyOrder(t *testing.T) {
	// Production emits NRestarts first and ActiveState second. Keep this fixture
	// in that order so a positional lines[0]/lines[1] parser regresses visibly.
	active, restarts, err := parseSystemdServiceState("NRestarts=0\nActiveState=active\n")
	if err != nil {
		t.Fatalf("parse production property order: %v", err)
	}
	if !active || restarts != 0 {
		t.Fatalf("got active=%t restarts=%d; want active=true restarts=0", active, restarts)
	}
}

func TestSystemdServiceStateRealIntegration(t *testing.T) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl is unavailable on this host")
	}
	active, restarts, err := systemdServiceState()
	if err != nil {
		t.Fatalf("real systemdServiceState probe failed: %v", err)
	}
	if restarts < 0 {
		t.Fatalf("invalid restart count: %d", restarts)
	}
	_ = active // The service may legitimately be active or inactive on test hosts.
}

func TestInternalHealthzURLUsesInjectedListenAddress(t *testing.T) {
	got, err := internalHealthzURLFromArgs([]string{"vps-commander-hub", "-addr", "192.168.20.132:9521"})
	if err != nil {
		t.Fatalf("build internal health URL: %v", err)
	}
	const want = "http://192.168.20.132:9521/healthz"
	if got != want {
		t.Fatalf("got URL %q; want %q", got, want)
	}
	if strings.Contains(got, "127.0.0.1") {
		t.Fatalf("internal URL unexpectedly uses hard-coded loopback: %q", got)
	}
}

func TestHealthzCheckTimeoutIsFiveSeconds(t *testing.T) {
	if healthzCheckTimeout != 5*time.Second {
		t.Fatalf("healthz timeout=%s; want 5s", healthzCheckTimeout)
	}
}

func TestParseSystemdServiceStateRejectsInvalidValues(t *testing.T) {
	for _, fixture := range []string{
		"NRestarts=active\nActiveState=0\n",
		"NRestarts=-1\nActiveState=active\n",
		"NRestarts=0\nActiveState=unknown\n",
		"NRestarts=0\n",
		"NRestarts=0\nNRestarts=1\nActiveState=active\n",
	} {
		if _, _, err := parseSystemdServiceState(fixture); err == nil {
			t.Errorf("parseSystemdServiceState(%q) succeeded; want error", fixture)
		}
	}
}
