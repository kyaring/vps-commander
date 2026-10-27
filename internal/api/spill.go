package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/model"
)

const (
	spillDirName    = "mcp-spill"
	spillTTL        = 24 * time.Hour
	spillTotalLimit = 256 << 20 // total bytes kept on disk across all tokens
)

// errSpillNotFound is returned for every reason a caller cannot read a spill file: the file
// does not exist, belongs to another token, has expired, or was evicted. The message is the
// same in all cases, so a caller learns nothing about other tokens' files.
var errSpillNotFound = errors.New("spill file not found or expired")

type spillRecord struct {
	owner   string
	size    int64
	created time.Time
}

// spillStore keeps the full output of truncated exec calls on the Hub's local disk.
// Ownership is held in memory only: after a restart no record survives, so no old file
// can be matched to a token.
type spillStore struct {
	mu       sync.Mutex
	dir      string
	records  map[string]spillRecord // absolute path -> record
	totalCap int64
	ttl      time.Duration
	now      func() time.Time
}

type spillChunk struct {
	Content   string
	Bytes     int64
	TotalSize int64
	HasMore   bool
	EOF       bool
}

func newSpillStore(dir string, totalCap int64, ttl time.Duration, now func() time.Time) (*spillStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	// Files from an earlier process cannot be matched to any token any more; remove them.
	ents, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if e.Type().IsRegular() {
			_ = os.Remove(filepath.Join(resolved, e.Name()))
		}
	}
	return &spillStore{dir: resolved, records: make(map[string]spillRecord), totalCap: totalCap, ttl: ttl, now: now}, nil
}

// write stores data as a new spill file owned by owner and returns its absolute path.
func (s *spillStore) write(owner string, data []byte) (string, error) {
	return s.writeAt(owner, data, s.now())
}

// writeAt stores data as a spill file created at the given time; that time sets when it expires.
func (s *spillStore) writeAt(owner string, data []byte, created time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := created
	s.pruneLocked(now)
	size := int64(len(data))
	if size > s.totalCap {
		return "", fmt.Errorf("output of %d bytes exceeds the spill cap of %d bytes", size, s.totalCap)
	}
	for s.usedLocked()+size > s.totalCap && len(s.records) > 0 {
		s.evictOldestLocked()
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, hex.EncodeToString(rnd[:])+".out")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	s.records[path] = spillRecord{owner: owner, size: size, created: now}
	return path, nil
}

// sweep removes expired spill files even when nobody reads them.
func (s *spillStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
}

// janitor sweeps expired spill files periodically for the life of the process.
func (s *spillStore) janitor(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		s.sweep()
	}
}

// read returns up to limit bytes of a spill file starting at offset. Only the owning token
// can read; every other outcome is errSpillNotFound.
func (s *spillStore) read(owner, path string, offset, limit int64) (spillChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	clean := filepath.Clean(path)
	rec, ok := s.records[clean]
	if !ok || rec.owner == "" || rec.owner != owner || filepath.Dir(clean) != s.dir {
		return spillChunk{}, errSpillNotFound
	}
	fi, err := os.Lstat(clean)
	if err != nil || !fi.Mode().IsRegular() {
		return spillChunk{}, errSpillNotFound
	}
	f, err := os.Open(clean)
	if err != nil {
		return spillChunk{}, errSpillNotFound
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return spillChunk{}, err
	}
	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	eof := err == io.EOF || err == io.ErrUnexpectedEOF
	if err != nil && !eof {
		return spillChunk{}, err
	}
	total := fi.Size()
	return spillChunk{Content: string(buf[:n]), Bytes: int64(n), TotalSize: total, HasMore: offset+int64(n) < total, EOF: eof}, nil
}

// pruneLocked removes every spill file whose TTL has passed. Callers must hold mu.
func (s *spillStore) pruneLocked(now time.Time) {
	for path, rec := range s.records {
		if !now.Before(rec.created.Add(s.ttl)) {
			s.removeLocked(path)
		}
	}
}

// evictOldestLocked removes the oldest spill file. Callers must hold mu.
func (s *spillStore) evictOldestLocked() {
	var oldest string
	var oldestAt time.Time
	for path, rec := range s.records {
		if oldest == "" || rec.created.Before(oldestAt) {
			oldest, oldestAt = path, rec.created
		}
	}
	if oldest != "" {
		s.removeLocked(oldest)
	}
}

func (s *spillStore) removeLocked(path string) {
	_ = os.Remove(path)
	delete(s.records, path)
}

func (s *spillStore) usedLocked() int64 {
	var total int64
	for _, rec := range s.records {
		total += rec.size
	}
	return total
}

// mainDatabaseFile returns the absolute path of the main database file, or "" when the
// database is in memory.
func mainDatabaseFile(db *sql.DB) (string, error) {
	var file sql.NullString
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil {
		return "", err
	}
	if !file.Valid || file.String == "" {
		return "", nil
	}
	return filepath.Abs(file.String)
}

// spillFor returns the Hub's spill store, created on first use in the mcp-spill directory next
// to the database file. It returns nil when spilling is unavailable.
func (s *Server) spillFor() *spillStore {
	s.spillOnce.Do(func() {
		if s.Store == nil || s.Store.DB == nil {
			return
		}
		dbFile, err := mainDatabaseFile(s.Store.DB)
		if err != nil || dbFile == "" {
			slog.Warn("exec output spilling disabled: database has no file", "err", err)
			return
		}
		st, err := newSpillStore(filepath.Join(filepath.Dir(dbFile), spillDirName), spillTotalLimit, spillTTL, time.Now)
		if err != nil {
			slog.Warn("exec output spilling disabled", "err", err)
			return
		}
		s.spill = st
		go st.janitor(time.Minute)
	})
	return s.spill
}

// principalOwner returns the TokenID of the authenticated caller, or "" when absent. Spill
// files belong to the token, not to the quota group, so two tokens sharing a quota cannot
// read each other's output.
func principalOwner(ctx context.Context) string {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return ""
	}
	return p.TokenID
}

// execOutputFields is the one response shape for exec in every output mode. The same keys are
// always present. count and none modes blank the output bodies (stdout and stderr).
func execOutputFields(device string, exitCode int, shaped executor.ShapedOutput, durationMS int64, spillPath, mode string) map[string]any {
	stdout, stderr := shaped.Stdout, shaped.Stderr
	if mode == "count" || mode == "none" {
		stdout, stderr = "", ""
	}
	spillDevice := ""
	if spillPath != "" {
		spillDevice = mcp.SpillDeviceName
	}
	return map[string]any{
		"device":       device,
		"exit_code":    exitCode,
		"stdout":       stdout,
		"stderr":       stderr,
		"duration_ms":  durationMS,
		"truncated":    shaped.Truncated,
		"total_bytes":  shaped.TotalBytes,
		"total_lines":  shaped.TotalLines,
		"sha256":       shaped.SHA256,
		"spill_path":   spillPath,
		"spill_device": spillDevice,
		"timed_out":    false,
	}
}

// execResponse shapes exec output for the requested mode. When the output is truncated, the
// full stdout+stderr byte stream is stored as a spill file owned by owner. If the spill cannot
// be written, the result is still returned with an empty spill_path and the failure is logged.
func (s *Server) execResponse(owner, device string, exitCode int, stdout, stderr string, durationMS int64, opts executor.OutputOptions) (map[string]any, error) {
	return s.execResponseTimed(owner, device, exitCode, stdout, stderr, durationMS, opts, false)
}

// execResponseTimed is execResponse for a synchronous call that may have hit its deadline.
func (s *Server) execResponseTimed(owner, device string, exitCode int, stdout, stderr string, durationMS int64, opts executor.OutputOptions, timedOut bool) (map[string]any, error) {
	shaped, err := executor.ShapeOutput(stdout, stderr, opts.Mode, opts.MaxBytes)
	if err != nil {
		return nil, err
	}
	spillPath := ""
	if shaped.Truncated && owner != "" {
		if st := s.spillFor(); st != nil {
			p, werr := st.write(owner, []byte(stdout+stderr))
			if werr != nil {
				slog.Warn("exec output spill failed", "device", device, "err", werr)
			} else {
				spillPath = p
			}
		}
	}
	fields := execOutputFields(device, exitCode, shaped, durationMS, spillPath, opts.Mode)
	fields["timed_out"] = timedOut
	return fields, nil
}

// readSpillPage serves one page of a spill file for the caller identified by owner.
func (s *Server) readSpillPage(owner, path string, offset, limit int64) (spillChunk, error) {
	if offset < 0 {
		return spillChunk{}, errors.New("invalid offset")
	}
	if limit <= 0 {
		limit = defaultFileLimit
	}
	if limit > maxFileLimit {
		limit = maxFileLimit
	}
	st := s.spillFor()
	if st == nil || owner == "" {
		return spillChunk{}, errSpillNotFound
	}
	return st.read(owner, path, offset, limit)
}

// readSpill is the MCP read_file path for SpillDeviceName. Scope is checked by the MCP layer
// (files:read); ownership is checked by the spill store.
func (b *MCPBackend) readSpill(ctx context.Context, clientIP, path string, offset, limit int64) (any, error) {
	chunk, err := b.server.readSpillPage(principalOwner(ctx), path, offset, limit)
	if err != nil {
		return nil, err
	}
	if b.server.Store != nil {
		_ = b.server.auditMCPContext(ctx, clientIP, mcp.SpillDeviceName, "file_read", path, 0, 0, "")
	}
	return model.FileReadResponse{
		Device:    mcp.SpillDeviceName,
		Path:      path,
		Offset:    offset,
		Content:   chunk.Content,
		Bytes:     chunk.Bytes,
		TotalSize: chunk.TotalSize,
		HasMore:   chunk.HasMore,
		EOF:       chunk.EOF,
	}, nil
}

// serveSpillRead is the REST path for /api/v1/file/read with device SpillDeviceName.
func (s *Server) serveSpillRead(w http.ResponseWriter, r *http.Request, path string, offset, limit int64) {
	p, ok := auth.PrincipalFromContext(r.Context())
	if !ok || !p.HasScope("files:read") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	chunk, err := s.readSpillPage(p.TokenID, path, offset, limit)
	if errors.Is(err, errSpillNotFound) {
		http.Error(w, errSpillNotFound.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.Store != nil {
		_ = s.auditMCPContext(r.Context(), auth.ClientIP(r), mcp.SpillDeviceName, "file_read", path, 0, 0, "")
	}
	jsonOut(w, model.FileReadResponse{
		Device:    mcp.SpillDeviceName,
		Path:      path,
		Offset:    offset,
		Content:   chunk.Content,
		Bytes:     chunk.Bytes,
		TotalSize: chunk.TotalSize,
		HasMore:   chunk.HasMore,
		EOF:       chunk.EOF,
	})
}
