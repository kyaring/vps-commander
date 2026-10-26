package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var ErrBlockNotUnique = errors.New("old_text must occur exactly once")
var ErrEditConflict = errors.New("file changed during edit")
var ErrHardlinked = errors.New("hardlinked file refused")

var editMu sync.Mutex

type EditBlockResult struct {
	BytesWritten int64
}

// EditBlock replaces the single occurrence of oldText in the file at path.
// A symbolic link is followed: the real target is edited and the link itself is
// left in place. A hardlinked file is refused, because the rename-based write
// would silently break the link.
func EditBlock(path, oldText, newText string, expectedHash ...string) (EditBlockResult, error) {
	editMu.Lock()
	defer editMu.Unlock()

	if path == "" {
		return EditBlockResult{}, errors.New("path required")
	}
	if oldText == "" {
		return EditBlockResult{}, errors.New("old_text required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return EditBlockResult{}, fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return EditBlockResult{}, err
	}
	if !info.Mode().IsRegular() {
		return EditBlockResult{}, fmt.Errorf("not a regular file: %s", resolved)
	}
	if linkCount(info) > 1 {
		return EditBlockResult{}, ErrHardlinked
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return EditBlockResult{}, err
	}
	initial := sha256.Sum256(data)
	initialHash := hex.EncodeToString(initial[:])
	if len(expectedHash) > 0 && expectedHash[0] != "" && expectedHash[0] != initialHash {
		return EditBlockResult{}, ErrEditConflict
	}

	src := string(data)
	count := strings.Count(src, oldText)
	if count != 1 {
		if count == 0 {
			return EditBlockResult{}, fmt.Errorf("%w: found 0 matches", ErrBlockNotUnique)
		}
		return EditBlockResult{}, fmt.Errorf("%w: found %d matches", ErrBlockNotUnique, count)
	}
	updated := strings.Replace(src, oldText, newText, 1)

	// Re-read immediately before commit. This is the CAS check against
	// writers outside this process; editMu serializes our own writers.
	latest, err := os.ReadFile(resolved)
	if err != nil {
		return EditBlockResult{}, err
	}
	latestSum := sha256.Sum256(latest)
	if hex.EncodeToString(latestSum[:]) != initialHash {
		return EditBlockResult{}, ErrEditConflict
	}

	dir := filepath.Dir(resolved)
	tmp, err := os.CreateTemp(dir, ".vps-commander-edit-*")
	if err != nil {
		return EditBlockResult{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return EditBlockResult{}, err
	}
	if _, err := tmp.WriteString(updated); err != nil {
		_ = tmp.Close()
		return EditBlockResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return EditBlockResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return EditBlockResult{}, err
	}
	if err := os.Rename(tmpName, resolved); err != nil {
		return EditBlockResult{}, err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return EditBlockResult{BytesWritten: int64(len(updated))}, nil
}

// linkCount returns the hardlink count of a regular file (1 when unavailable).
func linkCount(info os.FileInfo) uint64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 1
	}
	return uint64(st.Nlink)
}
