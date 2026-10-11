package storage

import "testing"

func TestResourceAlertStateRoundTripAndDelete(t *testing.T) {
	s, err := Open(t.TempDir() + "/resource-alerts.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	want := ResourceAlertState{Device: "node-a", Metric: "mem", Label: "memory", StartedAt: 1234, Sent: 2, Value: 94.5, Threshold: 90, StrikeCount: 3, RecoveryCount: 1, Active: true}
	if err := s.SaveResourceAlertState(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListResourceAlertStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Device != want.Device || got[0].Metric != want.Metric || got[0].StartedAt != want.StartedAt || got[0].Sent != want.Sent || got[0].Value != want.Value || got[0].Threshold != want.Threshold || got[0].StrikeCount != want.StrikeCount || got[0].RecoveryCount != want.RecoveryCount || got[0].Active != want.Active {
		t.Fatalf("round-trip state = %#v, want %#v", got, want)
	}
	if err := s.DeleteResourceAlertState(want.Device, want.Metric); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListResourceAlertStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("states after delete = %#v, want empty", got)
	}
}
