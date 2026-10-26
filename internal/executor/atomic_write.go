package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WriteFileAtomic replaces the content of path through a temporary file in the
// same directory followed by rename, so a failure never leaves a truncated or
// half-written file behind.
//
//   - A symbolic link is followed: the real target is written and the link is kept.
//   - A dangling symbolic link is an error; nothing is created.
//   - A hardlinked file is refused (ErrHardlinked), matching EditBlock.
//   - A path that exists but is not a regular file (directory, device, FIFO) is refused.
//   - An existing file keeps its permission bits and owner; newFileMode applies
//     only when the target does not exist yet.
func WriteFileAtomic(path string, data []byte, newFileMode os.FileMode) error {
	if path == "" {
		return errors.New("path required")
	}
	resolved := path
	mode := newFileMode.Perm()
	var owner *syscall.Stat_t

	li, err := os.Lstat(path)
	switch {
	case err == nil:
		if li.Mode()&os.ModeSymlink != 0 {
			resolved, err = filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}
		}
		ti, err := os.Stat(resolved)
		if err != nil {
			return err
		}
		if !ti.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", resolved)
		}
		if linkCount(ti) > 1 {
			return ErrHardlinked
		}
		mode = ti.Mode().Perm()
		owner, _ = ti.Sys().(*syscall.Stat_t)
	case os.IsNotExist(err):
		// New file: resolved stays the requested path, mode is newFileMode.
	default:
		return err
	}

	dir := filepath.Dir(resolved)
	tmp, err := os.CreateTemp(dir, ".vps-commander-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if owner != nil {
		if err := tmp.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, resolved); err != nil {
		return err
	}
	committed = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
