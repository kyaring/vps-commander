package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
}

type FileInfo struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mod_time"`
}

type SearchResult struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
}

func ListDirectory(path string, recursive bool, maxEntries int) ([]Entry, error) {
	if path == "" {
		return nil, errors.New("path required")
	}
	if maxEntries <= 0 || maxEntries > 10000 {
		maxEntries = 1000
	}
	out := make([]Entry, 0, maxEntries)
	add := func(p string, d os.DirEntry) error {
		if len(out) >= maxEntries {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, Entry{Name: d.Name(), Path: p, IsDir: d.IsDir(), Size: info.Size(), Mode: info.Mode().String()})
		return nil
	}
	if !recursive {
		ds, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			if err := add(filepath.Join(path, d.Name()), d); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == path {
			return nil
		}
		if len(out) >= maxEntries {
			return filepath.SkipDir
		}
		return add(p, d)
	})
	return out, err
}

func GetFileInfo(path string) (FileInfo, error) {
	if path == "" {
		return FileInfo{}, errors.New("path required")
	}
	i, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: path, Name: i.Name(), IsDir: i.IsDir(), Size: i.Size(), Mode: i.Mode().String(), ModTime: i.ModTime().Unix()}, nil
}

func Search(ctx context.Context, root, query string, maxResults int) ([]SearchResult, error) {
	if root == "" || query == "" {
		return nil, errors.New("root and query required")
	}
	if maxResults <= 0 || maxResults > 1000 {
		maxResults = 100
	}
	out := make([]SearchResult, 0, maxResults)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(out) >= maxResults {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(b), query) {
			for n, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, query) {
					out = append(out, SearchResult{Path: path, Line: n + 1, Text: line})
					if len(out) >= maxResults {
						break
					}
				}
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

type FileReadItem struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}
type ProcessInfo struct {
	PID      int    `json:"pid"`
	Command  string `json:"command"`
	CPU      string `json:"cpu,omitempty"`
	MemoryKB uint64 `json:"memory_kb,omitempty"`
}

func ReadMultipleFiles(paths []string, limit int64) []FileReadItem {
	if limit <= 0 || limit > 1024*1024 {
		limit = 256 * 1024
	}
	out := make([]FileReadItem, 0, len(paths))
	for _, p := range paths {
		v := FileReadItem{Path: p}
		b, e := os.ReadFile(p)
		if e != nil {
			v.Error = e.Error()
		} else {
			if int64(len(b)) > limit {
				b = b[:limit]
			}
			v.Content = string(b)
		}
		out = append(out, v)
	}
	return out
}
func CreateDirectory(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path required")
	}
	return os.MkdirAll(path, 0755)
}
func MoveFile(src, dst string) error {
	if src == "" || dst == "" {
		return errors.New("source and destination required")
	}
	return os.Rename(src, dst)
}
func ListProcesses() ([]ProcessInfo, error) {
	ents, e := os.ReadDir("/proc")
	if e != nil {
		return nil, e
	}
	out := make([]ProcessInfo, 0)
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		pid, e := strconv.Atoi(ent.Name())
		if e != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", ent.Name(), "comm"))
		stat, _ := os.ReadFile(filepath.Join("/proc", ent.Name(), "statm"))
		cmd, _ := os.ReadFile(filepath.Join("/proc", ent.Name(), "cmdline"))
		v := ProcessInfo{PID: pid, Command: strings.TrimSpace(string(comm))}
		if len(cmd) > 0 {
			v.Command = strings.ReplaceAll(string(cmd), "\x00", " ")
			v.Command = strings.TrimSpace(v.Command)
		}
		f := strings.Fields(string(stat))
		if len(f) >= 2 {
			if pages, e := strconv.ParseUint(f[1], 10, 64); e == nil {
				v.MemoryKB = pages * uint64(os.Getpagesize()) / 1024
			}
		}
		out = append(out, v)
	}
	return out, nil
}

var _ = fmt.Sprintf
var _ = exec.Command
