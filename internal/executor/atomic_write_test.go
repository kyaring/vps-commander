package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func leftoverTemps(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".vps-commander-write-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// 要求 1-a: an existing file is replaced and no temp file is left behind.
func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	writeFile(t, path, "old", 0o644)
	if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Fatalf("leftover temp files: %v", left)
	}
}

// 要求 1-b: the permission bits of an existing file are kept.
func TestWriteFileAtomicKeepsExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.env")
	writeFile(t, path, "K=1", 0o600)
	if err := WriteFileAtomic(path, []byte("K=2"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (existing mode kept, not newFileMode)", fi.Mode().Perm())
	}
}

// 要求 1-c: a new file gets exactly newFileMode.
func TestWriteFileAtomicNewFileUsesNewFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	if err := WriteFileAtomic(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Fatalf("leftover temp files: %v", left)
	}
}

// 要求 1-d: a failed write returns an error and leaves nothing behind.
func TestWriteFileAtomicMissingDirFailsCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such-dir", "x.txt")
	if err := WriteFileAtomic(path, []byte("x"), 0o644); err == nil {
		t.Fatal("want error for missing parent directory")
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Fatalf("leftover temp files: %v", left)
	}
}

// 要求 1 (a failed write must not change the existing file): a directory target is refused untouched.
func TestWriteFileAtomicRefusesDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "d")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("x"), 0o644); err == nil {
		t.Fatal("want error when target is a directory")
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		t.Fatalf("directory was disturbed: fi=%v err=%v", fi, err)
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Fatalf("leftover temp files: %v", left)
	}
}

// 要求 1-e: a symlink is followed; the target is written and the link is kept.
func TestWriteFileAtomicThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	writeFile(t, target, "old", 0o644)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(link, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "x" {
		t.Fatalf("target = %q, want x", got)
	}
	li, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced by a regular file, mode=%v", li.Mode())
	}
}

// 要求 1-f: a dangling symlink is an error and creates nothing.
func TestWriteFileAtomicDanglingSymlinkErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(missing, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(link, []byte("x"), 0o644); err == nil {
		t.Fatal("want error on dangling symlink")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("dangling target was created: %v", err)
	}
}

// 要求 1-g: a hardlinked file is refused and neither name changes.
func TestWriteFileAtomicRefusesHardlink(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	writeFile(t, a, "orig", 0o644)
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
	err := WriteFileAtomic(a, []byte("new"), 0o644)
	if !errors.Is(err, ErrHardlinked) {
		t.Fatalf("err = %v, want ErrHardlinked", err)
	}
	for _, p := range []string{a, b} {
		got, _ := os.ReadFile(p)
		if string(got) != "orig" {
			t.Fatalf("%s = %q, want unchanged", p, got)
		}
	}
}
