package notify

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/storage"
)

// logBuffer is a concurrency-safe log sink so the test can assert on log lines
// without racing the package's goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestNotifyDeliveryRecordFailureIsLogged pins the audit finding A2: a failed
// RecordWebhookDelivery used to be swallowed by `_ =`. The delivery itself must
// keep working exactly as before (no error surfaces to the caller), but the
// bookkeeping failure must now be visible in the log.
func TestNotifyDeliveryRecordFailureIsLogged(t *testing.T) {
	var logs logBuffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	delivered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case delivered <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store, err := storage.Open(t.TempDir() + "/notify-delivery-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveWebhookTarget(storage.WebhookTarget{ID: "target-log-fail", Name: "Log Fail", Type: "generic", URL: srv.URL, Config: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Fail the delivery bookkeeping only: the webhook POST itself still succeeds.
	if _, err := store.DB.Exec(`CREATE TRIGGER fail_webhook_delivery_state BEFORE INSERT ON webhook_delivery_state BEGIN SELECT RAISE(ABORT,'injected webhook_delivery_state failure'); END;`); err != nil {
		t.Fatal(err)
	}

	m := New(store)
	m.Client = srv.Client()
	// deliverEvent keeps its void signature: a bookkeeping failure must not
	// propagate to the caller (behavior unchanged).
	m.deliverEvent(context.Background(), Event{Type: "test", Severity: "info", Message: "record failure probe", At: time.Now().Unix()})

	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not delivered; the test did not reach the recording step")
	}
	const want = "notify: record webhook delivery for target target-log-fail failed"
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("log does not contain %q; got:\n%s", want, logs.String())
	}
}
