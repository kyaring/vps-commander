package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/storage"
)

const atomicTestDevice = "wjyhk"

func newAtomicTestServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, LocalName: atomicTestDevice}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Type: "api_token", ID: "token-1", Name: "ops", TokenID: "token-1",
		Scopes:  map[string]bool{"files:write": true, "files:read": true},
		Devices: map[string]bool{atomicTestDevice: true},
	})
	return s, ctx
}

func atomicWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// 要求 1/2-a: MCP write_file on a local symlink writes the target and keeps the link.
func TestHubLocalWriteFollowsSymlink(t *testing.T) {
	s, ctx := newAtomicTestServer(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	atomicWriteFile(t, target, "old", 0o644)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	b := s.NewMCPBackend()
	if _, err := b.WriteFile(ctx, "127.0.0.1", atomicTestDevice, link, "new"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Fatalf("target = %q, want new", got)
	}
	li, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced by a regular file, mode=%v", li.Mode())
	}
}

// 要求 2-b: a hardlinked file is refused and neither name changes.
func TestHubLocalWriteRefusesHardlink(t *testing.T) {
	s, ctx := newAtomicTestServer(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b2 := filepath.Join(dir, "b.txt")
	atomicWriteFile(t, a, "orig", 0o644)
	if err := os.Link(a, b2); err != nil {
		t.Fatal(err)
	}
	_, err := s.NewMCPBackend().WriteFile(ctx, "127.0.0.1", atomicTestDevice, a, "new")
	if err == nil || !strings.Contains(err.Error(), "hardlinked file refused") {
		t.Fatalf("err = %v, want hardlinked file refused", err)
	}
	for _, p := range []string{a, b2} {
		got, _ := os.ReadFile(p)
		if string(got) != "orig" {
			t.Fatalf("%s = %q, want unchanged", p, got)
		}
	}
}

// 要求 2-c: a directory target is refused as not a regular file.
func TestHubLocalWriteRefusesDirectory(t *testing.T) {
	s, ctx := newAtomicTestServer(t)
	sub := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := s.NewMCPBackend().WriteFile(ctx, "127.0.0.1", atomicTestDevice, sub, "x")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want not a regular file", err)
	}
}

// 要求 4: an existing file keeps its permission bits.
func TestHubLocalWriteKeepsMode(t *testing.T) {
	s, ctx := newAtomicTestServer(t)
	path := filepath.Join(t.TempDir(), "secret.env")
	atomicWriteFile(t, path, "K=1", 0o600)
	if _, err := s.NewMCPBackend().WriteFile(ctx, "127.0.0.1", atomicTestDevice, path, "K=2"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

// 要求 1 (REST): hardlink refused with 500 and the error text in the body; file unchanged.
func TestHubRESTFileWriteRefusesHardlink(t *testing.T) {
	s, ctx := newAtomicTestServer(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b2 := filepath.Join(dir, "b.txt")
	atomicWriteFile(t, a, "orig", 0o644)
	if err := os.Link(a, b2); err != nil {
		t.Fatal(err)
	}
	body := `{"device":"` + atomicTestDevice + `","path":"` + a + `","content":"new"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/file/write", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	s.fileWrite(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "hardlinked file refused") {
		t.Fatalf("status=%d body=%q, want 500 with hardlinked file refused", w.Code, w.Body.String())
	}
	got, _ := os.ReadFile(a)
	if string(got) != "orig" {
		t.Fatalf("file changed: %q", got)
	}
}

// 要求 1 (REST): an ordinary existing file is replaced with 200.
func TestHubRESTFileWriteOrdinaryFile(t *testing.T) {
	s, ctx := newAtomicTestServer(t)
	path := filepath.Join(t.TempDir(), "plain.txt")
	atomicWriteFile(t, path, "old", 0o644)
	body := `{"device":"` + atomicTestDevice + `","path":"` + path + `","content":"fresh"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/file/write", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	s.fileWrite(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200", w.Code, w.Body.String())
	}
	got, _ := os.ReadFile(path)
	if string(got) != "fresh" {
		t.Fatalf("content = %q, want fresh", got)
	}
}
