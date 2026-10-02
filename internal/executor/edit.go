package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrBlockNotUnique = errors.New("old_text must occur exactly once")
var ErrEditConflict = errors.New("file changed during edit")

var editMu sync.Mutex

type EditBlockResult struct {
	BytesWritten int64
}

func EditBlock(path, oldText, newText string, expectedHash ...string) (EditBlockResult, error) {
	editMu.Lock()
	defer editMu.Unlock()

	if path == "" {
		return EditBlockResult{}, errors.New("path required")
	}
	if oldText == "" {
		return EditBlockResult{}, errors.New("old_text required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return EditBlockResult{}, err
	}
	initial := sha256.Sum256(data)
	initialHash := hex.EncodeToString(initial[:])
	if len(expectedHash) > 0 && expectedHash[0] != "" && expectedHash[0] != initialHash {
		return EditBlockResult{}, ErrEditConflict
	}

	src := string(data)
	count := countNonOverlapping(src, oldText)
	if count != 1 {
		if count == 0 {
			return EditBlockResult{}, fmt.Errorf("%w: found 0 matches", ErrBlockNotUnique)
		}
		return EditBlockResult{}, fmt.Errorf("%w: found %d matches", ErrBlockNotUnique, count)
	}
	updated := replaceOnce(src, oldText, newText)

	info, err := os.Stat(path)
	if err != nil {
		return EditBlockResult{}, err
	}

	// Re-read immediately before commit. This is the CAS check against
	// writers outside this process; editMu serializes our own writers.
	latest, err := os.ReadFile(path)
	if err != nil {
		return EditBlockResult{}, err
	}
	latestSum := sha256.Sum256(latest)
	if hex.EncodeToString(latestSum[:]) != initialHash {
		return EditBlockResult{}, ErrEditConflict
	}

	dir := filepath.Dir(path)
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
	if err := os.Rename(tmpName, path); err != nil {
		return EditBlockResult{}, err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return EditBlockResult{BytesWritten: int64(len(updated))}, nil
}

func countNonOverlapping(src, old string) int {
	n, count := 0, 0
	for {
		i := indexString(src[n:], old)
		if i < 0 {
			return count
		}
		count++
		n += i + len(old)
		if n > len(src) {
			return count
		}
	}
}

func replaceOnce(src, old, new string) string {
	i := indexString(src, old)
	return src[:i] + new + src[i+len(old):]
}

func indexString(src, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(src); i++ {
		if src[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
