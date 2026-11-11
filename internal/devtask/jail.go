package devtask

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveInRoot resolves path under root and rejects escapes and .git writes.
// Symlinks that leave root are rejected via EvalSymlinks when the path exists.
func ResolveInRoot(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// root may not exist yet
		rootAbs, _ = filepath.Abs(root)
	}
	clean := path
	if !filepath.IsAbs(path) {
		clean = filepath.Join(rootAbs, path)
	}
	clean = filepath.Clean(clean)
	// If the file exists, resolve symlinks.
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	} else {
		// Resolve existing parents.
		dir := filepath.Dir(clean)
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			clean = filepath.Join(r, filepath.Base(clean))
		}
	}
	rel, err := filepath.Rel(rootAbs, clean)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("path escapes worktree: %s", path)
	}
	// Forbid writes under .git
	parts := strings.Split(rel, string(os.PathSeparator))
	for _, p := range parts {
		if p == ".git" {
			return "", fmt.Errorf("writes under .git are forbidden")
		}
	}
	return clean, nil
}

// AllowedCommand reports whether argv is permitted by the whitelist prefixes.
// Each whitelist entry is a space-separated prefix (e.g. "go test", "gofmt -l").
func AllowedCommand(whitelist []string, argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	joined := strings.Join(argv, " ")
	for _, entry := range whitelist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if joined == entry || strings.HasPrefix(joined, entry+" ") {
			return true
		}
		// Also allow exact program-only entries matching argv[0]
		if entry == argv[0] {
			return true
		}
	}
	return false
}
