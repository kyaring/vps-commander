package security

import "testing"

func TestSnapshotEffectiveRiskAndPublish(t *testing.T) {
	s := New(RiskMedium, map[string]DevicePolicy{"hwsg": {Mode: "high"}}, 7)
	if got := s.EffectiveRisk("hwsg"); got != RiskHigh {
		t.Fatalf("hwsg risk=%d, want %d", got, RiskHigh)
	}
	if got := s.EffectiveRisk("n3450"); got != RiskMedium {
		t.Fatalf("default risk=%d, want %d", got, RiskMedium)
	}
	if err := s.Publish(RiskLow, map[string]DevicePolicy{"n3450": {Mode: "medium"}}, 8); err != nil {
		t.Fatal(err)
	}
	cur := s.Current()
	if cur.Version != 8 {
		t.Fatalf("version=%d, want 8", cur.Version)
	}
	if got := s.EffectiveRisk("hwsg"); got != RiskLow {
		t.Fatalf("old node risk=%d, want %d", got, RiskLow)
	}
	if got := s.EffectiveRisk("n3450"); got != RiskMedium {
		t.Fatalf("override risk=%d, want %d", got, RiskMedium)
	}
}

func TestModeRiskRejectsUnknown(t *testing.T) {
	if got := ModeRisk("inherit"); got != 0 {
		t.Fatalf("inherit risk=%d, want 0", got)
	}
	if got := ModeRisk("root"); got != 0 {
		t.Fatalf("unknown risk=%d, want 0", got)
	}
}
