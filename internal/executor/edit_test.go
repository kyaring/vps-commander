package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEditBlockUniqueAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(path, []byte("a\nTARGET\nz\n"), 0640); err != nil {
		t.Fatal(err)
	}
	res, err := EditBlock(path, "TARGET", "CHANGED")
	if err != nil {
		t.Fatal(err)
	}
	if res.BytesWritten != int64(len("a\nCHANGED\nz\n")) {
		t.Fatalf("bytes=%d", res.BytesWritten)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "a\nCHANGED\nz\n" {
		t.Fatalf("content=%q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode=%o, want 0640", info.Mode().Perm())
	}
}

func TestEditBlockRejectsZeroAndMultipleMatchesWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.txt")
	original := "TARGET\nother\nTARGET\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := EditBlock(path, "MISSING", "x")
	if !errors.Is(err, ErrBlockNotUnique) {
		t.Fatalf("missing match error=%v", err)
	}
	_, err = EditBlock(path, "TARGET", "x")
	if !errors.Is(err, ErrBlockNotUnique) {
		t.Fatalf("multiple match error=%v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("file mutated after rejected edit: %q", got)
	}
}
