package executor

import (
	"context"
	"strings"
	"testing"
)

func TestDiagnosticRejectsShellInjection(t *testing.T) {
	_, err := Diagnostic(context.Background(), DiagnosticRequest{Action: "docker_logs", Target: "edgeever;rm -rf /"})
	if err == nil || !strings.Contains(err.Error(), "invalid docker") {
		t.Fatalf("expected target rejection, got %v", err)
	}
}

func TestDiagnosticAllowsReadOnlyUptime(t *testing.T) {
	got, err := Diagnostic(context.Background(), DiagnosticRequest{Action: "uptime"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 0 || strings.TrimSpace(got.Stdout) == "" {
		t.Fatalf("unexpected uptime result: %#v", got)
	}
}
