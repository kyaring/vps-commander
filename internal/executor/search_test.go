package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 要求 1-a: files above MaxSearchFileBytes are skipped; smaller matching files still match.
func TestSearchSkipsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	// Plain text, no NUL padding: size is the only reason this file is skipped.
	writeFile(t, big, "needle\n"+strings.Repeat("y", MaxSearchFileBytes+1-7), 0o644)
	writeFile(t, filepath.Join(dir, "small.txt"), "needle\n", 0o644)

	res, err := Search(context.Background(), dir, "needle", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || filepath.Base(res[0].Path) != "small.txt" || res[0].Line != 1 {
		t.Fatalf("results = %+v, want only small.txt line 1", res)
	}
}

// 要求 1-b: a file exactly at MaxSearchFileBytes is searched.
func TestSearchAcceptsFileAtLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "edge.txt")
	writeFile(t, p, "needle\n"+strings.Repeat("y", MaxSearchFileBytes-7), 0o644)
	res, err := Search(context.Background(), dir, "needle", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v, want 1 match", res)
	}
}

// 要求 2-a: a NUL in the first 8 KiB marks the file as binary and it is skipped.
func TestSearchSkipsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bin.dat"), "\x00needle\n", 0o644)
	res, err := Search(context.Background(), dir, "needle", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("binary file matched: %+v", res)
	}
}

// 要求 2-b: a NUL after the 8 KiB probe window does not make the file binary (pinned heuristic).
func TestSearchNulBeyondProbeWindowIsNotBinary(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	buf.WriteString(strings.Repeat("x\n", 4500)) // 9000 bytes of text
	buf.WriteString("\x00needle\n")
	writeFile(t, filepath.Join(dir, "late.txt"), buf.String(), 0o644)
	res, err := Search(context.Background(), dir, "needle", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Line != 4501 {
		t.Fatalf("results = %+v, want one match at line 4501", res)
	}
}

// 要求 3: an unreadable file is skipped without an error (skipped when running as root).
func TestSearchSkipsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file; cannot construct an unreadable file")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked.txt")
	writeFile(t, locked, "needle\n", 0o000)
	writeFile(t, filepath.Join(dir, "ok.txt"), "needle\n", 0o644)
	res, err := Search(context.Background(), dir, "needle", 100)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(res) != 1 || filepath.Base(res[0].Path) != "ok.txt" {
		t.Fatalf("results = %+v, want only ok.txt", res)
	}
}

// 要求 4-a: maxResults caps the result and line numbers are reported.
func TestSearchMaxResultsAndLineNumber(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "m.txt"), "a\nneedle\nneedle\n", 0o644)
	res, err := Search(context.Background(), dir, "needle", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Line != 2 {
		t.Fatalf("results = %+v, want 1 result at line 2", res)
	}
}

// 要求 4-b: a missing root keeps the previous behaviour (error).
func TestSearchMissingRootErrors(t *testing.T) {
	if _, err := Search(context.Background(), filepath.Join(t.TempDir(), "nope"), "x", 10); err == nil {
		t.Fatal("want error for missing root")
	}
}
