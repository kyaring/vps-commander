package executor

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// executorLogBuffer is a concurrency-safe log sink for asserting on log lines.
type executorLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *executorLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *executorLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestAtomicWriteDirSyncFailureIsLogged pins the silent durability risk: when
// the post-rename directory fsync fails, the caller still sees success (the
// data is in the file, only crash durability is at stake), but the failure used
// to be swallowed by `_ = d.Sync()`. Behavior is unchanged: no error returned,
// content replaced, no temp file left behind.
func TestAtomicWriteDirSyncFailureIsLogged(t *testing.T) {
	var logs executorLogBuffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	original := dirSyncFile
	dirSyncFile = func(*os.File) error { return errors.New("injected directory fsync failure") }
	defer func() { dirSyncFile = original }()

	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	writeFile(t, path, "old", 0o644)
	if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic must not surface the directory fsync failure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Fatalf("leftover temp files: %v", left)
	}

	want := "executor: fsync directory " + dir + " after rename failed"
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("log does not contain %q; got:\n%s", want, logs.String())
	}
}
