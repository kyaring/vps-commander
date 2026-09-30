package notify

import (
	"github.com/wjyhk/vps-commander/internal/storage"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTestTargetAndPayload(t *testing.T) {
	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		got = string(b[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()
	db := t.TempDir() + "/test.db"
	st, err := storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	m := New(st)
	m.Client = srv.Client()
	if err := m.TestTarget(Target{ID: "t", Name: "test", Type: "generic", URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("empty webhook body")
	}
}

func TestWebhookStorage(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	if err := st.SaveWebhookTarget(storage.WebhookTarget{ID: "x", Name: "X", Type: "generic", URL: "http://127.0.0.1", Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	xs, err := st.ListWebhookTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(xs) != 1 || xs[0].ID != "x" {
		t.Fatalf("unexpected targets: %#v", xs)
	}
	if err := st.DeleteWebhookTarget("x"); err != nil {
		t.Fatal(err)
	}
}
