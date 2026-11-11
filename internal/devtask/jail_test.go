package devtask

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathJailRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveInRoot(root, "ok.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInRoot(root, "../outside"); err == nil {
		t.Fatal("expected escape rejection")
	}
	if _, err := ResolveInRoot(root, ".git/config"); err == nil {
		t.Fatal("expected .git rejection")
	}
	// Symlink leaving the root
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInRoot(root, "escape/secret"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestDevTaskRunnerPathJail(t *testing.T) {
	root := t.TempDir()

	// 1. Normalization requirement: must return clean normalized path
	uncleanAbs := filepath.Join(root, "foo") + "/./bar/../baz.txt"
	res, err := ResolveInRoot(root, uncleanAbs)
	if err != nil {
		t.Fatalf("valid path resolution failed: %v", err)
	}
	expected := filepath.Join(root, "foo", "baz.txt")
	if res != expected {
		t.Fatalf("expected normalized path %q, got %q", expected, res)
	}

	// 2. Traversal rejection (..)
	traversals := []string{
		"../outside",
		"foo/../../outside",
		"a/b/../../../etc/passwd",
		"..",
	}
	for _, p := range traversals {
		if _, err := ResolveInRoot(root, p); err == nil {
			t.Fatalf("expected .. escape rejection for %q", p)
		}
	}

	// 3. Absolute path outside root
	absPaths := []string{
		"/etc/passwd",
		"/tmp/random-file",
		filepath.Dir(root),
	}
	for _, p := range absPaths {
		if _, err := ResolveInRoot(root, p); err == nil {
			t.Fatalf("expected absolute path escape rejection for %q", p)
		}
	}

	// 4. Empty and whitespace paths
	emptyPaths := []string{
		"",
		"   ",
		"\t\n",
	}
	for _, p := range emptyPaths {
		if _, err := ResolveInRoot(root, p); err == nil {
			t.Fatalf("expected empty path rejection for %q", p)
		}
	}

	// 5. Symlink escape
	outside := t.TempDir()
	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInRoot(root, "escape-link/secret.txt"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}

	// 6. .git writes forbidden
	gitPaths := []string{
		".git/config",
		".git",
		"subdir/.git/HEAD",
	}
	for _, p := range gitPaths {
		if _, err := ResolveInRoot(root, p); err == nil {
			t.Fatalf("expected .git rejection for %q", p)
		}
	}
}

func TestCommandWhitelist(t *testing.T) {
	wl := []string{"go test", "go vet", "gofmt -l"}
	if !AllowedCommand(wl, []string{"go", "test", "./..."}) {
		t.Fatal("go test ./... should be allowed")
	}
	if AllowedCommand(wl, []string{"bash", "-c", "rm -rf /"}) {
		t.Fatal("bash must be denied")
	}
	if AllowedCommand(wl, []string{"go", "mod", "download"}) {
		t.Fatal("go mod download not in whitelist")
	}
}
