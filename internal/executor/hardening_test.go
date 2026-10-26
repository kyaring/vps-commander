package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// firstPID parses the pid printed on the first output line by the shell.
func firstPID(t *testing.T, out string) int {
	t.Helper()
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	pid, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("expected pid on first output line, got %q", out)
	}
	return pid
}

// killAtCleanup makes sure a background process started by a test never outlives it.
func killAtCleanup(t *testing.T, pid int) {
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}

// 要求 1-a: shell exits 0, a background child keeps stdout open.
func TestRunBackgroundChildHoldingPipeDoesNotHang(t *testing.T) {
	start := time.Now()
	res := Local{}.Run(context.Background(), `sleep 30 & echo $!; echo hi`, "")
	elapsed := time.Since(start)
	killAtCleanup(t, firstPID(t, res.Stdout))
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hi") {
		t.Fatalf("stdout = %q, want it to contain hi", res.Stdout)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v, want it bounded by WaitDelay (about 2s)", elapsed)
	}
}

// 要求 1-b: a descendant escapes the process group with setsid and survives the timeout kill.
func TestRunTimeoutWithEscapedDescendantReturns(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	res := Local{}.Run(ctx, `setsid sleep 60 & echo $!; sleep 60`, "")
	elapsed := time.Since(start)
	killAtCleanup(t, firstPID(t, res.Stdout))
	if res.ExitCode != 124 {
		t.Fatalf("exit = %d, want 124 on timeout", res.ExitCode)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("Run took %v after timeout, want about 1s + WaitDelay", elapsed)
	}
}

// 要求 1-c: a normal non-zero exit is unchanged.
func TestRunNonZeroExitUnchanged(t *testing.T) {
	res := Local{}.Run(context.Background(), `exit 3`, "")
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", res.ExitCode)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// 要求 2-a: a symlink is followed; the target is edited and the link is preserved.
func TestEditBlockThroughSymlinkEditsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	link := filepath.Join(dir, "link.conf")
	writeFile(t, target, "a=1\n", 0o644)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EditBlock(link, "a=1", "a=2"); err != nil {
		t.Fatalf("EditBlock: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "a=2\n" {
		t.Fatalf("target = %q, want %q", got, "a=2\n")
	}
	li, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced by a regular file, mode=%v", li.Mode())
	}
	if dest, _ := os.Readlink(link); dest != target {
		t.Fatalf("link points to %q, want %q", dest, target)
	}
}

// 要求 2-b: a dangling symlink is an error and creates nothing.
func TestEditBlockDanglingSymlinkErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(missing, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EditBlock(link, "a", "b"); err == nil {
		t.Fatal("EditBlock on dangling symlink: want error")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("dangling target was created: %v", err)
	}
}

// 要求 2-c: a hardlinked file is refused and neither name changes.
func TestEditBlockRefusesHardlink(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.conf")
	b := filepath.Join(dir, "b.conf")
	writeFile(t, a, "x=1\n", 0o644)
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
	_, err := EditBlock(a, "x=1", "x=2")
	if !errors.Is(err, ErrHardlinked) {
		t.Fatalf("err = %v, want ErrHardlinked", err)
	}
	for _, p := range []string{a, b} {
		got, _ := os.ReadFile(p)
		if string(got) != "x=1\n" {
			t.Fatalf("%s = %q, want unchanged", p, got)
		}
	}
}

// 要求 2-d: an ordinary file is edited and its permission bits are kept.
func TestEditBlockRegularFileKeepsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.conf")
	writeFile(t, path, "k=v\n", 0o640)
	if _, err := EditBlock(path, "k=v", "k=w"); err != nil {
		t.Fatalf("EditBlock: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "k=w\n" {
		t.Fatalf("content = %q", got)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", fi.Mode().Perm())
	}
}

// 要求 3-a: two matches are rejected with the same message as before.
func TestEditBlockTwoMatchesRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	writeFile(t, path, "x x\n", 0o644)
	_, err := EditBlock(path, "x", "y")
	if err == nil || !strings.Contains(err.Error(), "found 2 matches") {
		t.Fatalf("err = %v, want found 2 matches", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "x x\n" {
		t.Fatalf("file changed: %q", got)
	}
}

// 要求 3-b: zero matches are rejected with the same message as before.
func TestEditBlockZeroMatchesRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	writeFile(t, path, "abc\n", 0o644)
	_, err := EditBlock(path, "zzz", "y")
	if err == nil || !strings.Contains(err.Error(), "found 0 matches") {
		t.Fatalf("err = %v, want found 0 matches", err)
	}
}

// 要求 4-a: a file larger than limit is truncated and flagged.
func TestReadMultipleFilesTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.txt")
	writeFile(t, path, strings.Repeat("a", 3*1024), 0o644)
	items := ReadMultipleFiles([]string{path}, 1024)
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	it := items[0]
	if it.Error != "" {
		t.Fatalf("unexpected error: %s", it.Error)
	}
	if len(it.Content) != 1024 || !it.Truncated {
		t.Fatalf("len=%d truncated=%v, want 1024 and true", len(it.Content), it.Truncated)
	}
}

// 要求 4-b: a cut inside a multi-byte character leaves valid UTF-8.
func TestReadMultipleFilesUTF8Boundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.txt")
	// 1022 ASCII bytes followed by "中" (3 bytes): 1025 bytes total.
	writeFile(t, path, strings.Repeat("a", 1022)+"中", 0o644)
	cases := []struct {
		limit     int64
		wantLen   int
		truncated bool
	}{
		{1024, 1022, true}, // cut after the first 2 bytes of 中
		{1023, 1022, true}, // cut after the first byte of 中
		{1022, 1022, true}, // cut exactly before 中
		{1025, 1025, false},
	}
	for _, c := range cases {
		it := ReadMultipleFiles([]string{path}, c.limit)[0]
		if !utf8.ValidString(it.Content) {
			t.Fatalf("limit %d: content is not valid UTF-8", c.limit)
		}
		if len(it.Content) != c.wantLen || it.Truncated != c.truncated {
			t.Fatalf("limit %d: len=%d truncated=%v, want len=%d truncated=%v",
				c.limit, len(it.Content), it.Truncated, c.wantLen, c.truncated)
		}
	}
}

// 要求 4-c: a small file is returned whole, and the truncated field is omitted from JSON.
func TestReadMultipleFilesSmallFileNotFlagged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.txt")
	writeFile(t, path, "hello", 0o644)
	it := ReadMultipleFiles([]string{path}, 1024)[0]
	if it.Content != "hello" || it.Truncated {
		t.Fatalf("item = %+v", it)
	}
	b, _ := json.Marshal(it)
	if strings.Contains(string(b), "truncated") {
		t.Fatalf("json contains truncated field: %s", b)
	}
}

// 要求 4-d: an 8 GiB sparse file is read with bounded work and memory.
func TestReadMultipleFilesHugeSparseFileIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(8 << 30); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	start := time.Now()
	it := ReadMultipleFiles([]string{path}, 1024)[0]
	elapsed := time.Since(start)
	if it.Error != "" || len(it.Content) != 1024 || !it.Truncated {
		t.Fatalf("item: err=%q len=%d truncated=%v", it.Error, len(it.Content), it.Truncated)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("read took %v, want bounded", elapsed)
	}
}

// R1: a directory, directly or through a symlink, is refused as not a regular file.
// Directories have nlink >= 2, so the regular-file check must come before the hardlink check.
func TestEditBlockRefusesDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "sublink")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{sub, link} {
		_, err := EditBlock(p, "a", "b")
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("EditBlock(%s): err = %v, want not a regular file", p, err)
		}
		if errors.Is(err, ErrHardlinked) {
			t.Fatalf("EditBlock(%s): misreported as hardlink: %v", p, err)
		}
	}
}
