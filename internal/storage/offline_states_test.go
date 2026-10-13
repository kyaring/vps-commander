package storage

import "testing"

func TestOfflineStateCRUD(t *testing.T) {
	st, err := Open(t.TempDir() + "/offline.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.DB.Exec(`SELECT device,since,sent,notified,updated_at FROM notification_offline_states LIMIT 0`); err != nil {
		t.Fatalf("offline state table was not created: %v", err)
	}
	first := OfflineState{Device: "node-a", Since: 100, Sent: 1, Notified: true}
	if err := st.SaveOfflineState(first); err != nil {
		t.Fatal(err)
	}
	second := OfflineState{Device: "node-a", Since: 200, Sent: 2, Notified: false}
	if err := st.SaveOfflineState(second); err != nil {
		t.Fatal(err)
	}
	states, err := st.ListOfflineStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("upsert returned %d rows, want 1: %#v", len(states), states)
	}
	got := states[0]
	if got.Device != second.Device || got.Since != second.Since || got.Sent != second.Sent || got.Notified != second.Notified || got.UpdatedAt == 0 {
		t.Fatalf("saved state = %#v, want updated values %#v and updated_at", got, second)
	}
	if err := st.DeleteOfflineState("node-a"); err != nil {
		t.Fatal(err)
	}
	states, err = st.ListOfflineStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("after delete got %#v, want no states", states)
	}
}
