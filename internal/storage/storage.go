package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wjyhk/vps-commander/internal/auditutil"
)

type Store struct {
	DB *sql.DB

	auditMu              sync.Mutex
	auditWriteMu         sync.Mutex
	auditQueue           chan auditQueueItem
	auditStop            chan struct{}
	auditDone            chan struct{}
	auditClosed          bool
	auditCloseOnce       sync.Once
	auditFallbackCount   atomic.Uint64
	sqliteWriteLatencyNS atomic.Int64
	auditCloseErr        error
}

type auditQueueItem struct {
	entry   AuditEntry
	barrier chan error
}

const (
	auditQueueCapacity   = 1024
	auditBatchSize       = 100
	auditFlushInterval   = 500 * time.Millisecond
	auditWriteAttempts   = 3
	auditShutdownTimeout = 20 * time.Second
)

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	if _, err := s.DB.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return err
	}
	if _, err := s.DB.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return err
	}
	if _, err := s.DB.Exec("PRAGMA synchronous = NORMAL"); err != nil {
		return err
	}
	if _, err := s.DB.Exec("PRAGMA wal_autocheckpoint = 1000"); err != nil {
		return err
	}
	_, err := s.DB.Exec("CREATE TABLE IF NOT EXISTS webhook_targets (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, url TEXT NOT NULL, config_json TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); CREATE INDEX IF NOT EXISTS idx_webhook_targets_enabled ON webhook_targets(enabled); CREATE TABLE IF NOT EXISTS devices (" +
		"name TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'offline', is_local INTEGER NOT NULL DEFAULT 0, " +
		"arch TEXT, os TEXT, last_seen INTEGER NOT NULL DEFAULT 0, last_heartbeat INTEGER NOT NULL DEFAULT 0, " +
		"offline_at INTEGER, profile_json TEXT, created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0); " +
		"CREATE TABLE IF NOT EXISTS audit_logs (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL, caller_ip TEXT, device TEXT, action TEXT, command TEXT, " +
		"exit_code INTEGER, duration_ms INTEGER, error_msg TEXT); " +
		"CREATE TABLE IF NOT EXISTS revoked_devices (name TEXT PRIMARY KEY, revoked_at INTEGER NOT NULL, reason TEXT); CREATE TABLE IF NOT EXISTS device_credentials (name TEXT PRIMARY KEY, token_hash TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS security_settings (node_name TEXT PRIMARY KEY, mode TEXT NOT NULL, updated_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS security_policy_meta (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL); INSERT OR IGNORE INTO security_policy_meta(id,version) VALUES(1,1); CREATE TABLE IF NOT EXISTS mcp_services (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT, transport TEXT NOT NULL, command TEXT, args_json TEXT, env_json TEXT, url TEXT, headers_json TEXT, scope TEXT NOT NULL DEFAULT 'all', target_nodes_json TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); CREATE INDEX IF NOT EXISTS idx_mcp_services_enabled ON mcp_services(enabled);")
	if err != nil {
		return err
	}
	if err := s.migrateDevices(); err != nil {
		return err
	}
	if err := s.migrateAudit(); err != nil {
		return err
	}
	return s.migrateOAuth()
}

type MCPService struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Transport       string `json:"transport"`
	Command         string `json:"command,omitempty"`
	ArgsJSON        string `json:"args_json,omitempty"`
	EnvJSON         string `json:"env_json,omitempty"`
	URL             string `json:"url,omitempty"`
	HeadersJSON     string `json:"headers_json,omitempty"`
	Scope           string `json:"scope"`
	TargetNodesJSON string `json:"target_nodes_json,omitempty"`
	Enabled         bool   `json:"enabled"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

func (s *Store) SaveMCPService(v MCPService) error {
	now := time.Now().Unix()
	if v.CreatedAt <= 0 {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	if v.Scope == "" {
		v.Scope = "all"
	}
	if v.Transport == "" {
		return errors.New("transport required")
	}
	_, err := s.DB.Exec(`INSERT INTO mcp_services(id,name,description,transport,command,args_json,env_json,url,headers_json,scope,target_nodes_json,enabled,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,transport=excluded.transport,command=excluded.command,args_json=excluded.args_json,env_json=excluded.env_json,url=excluded.url,headers_json=excluded.headers_json,scope=excluded.scope,target_nodes_json=excluded.target_nodes_json,enabled=excluded.enabled,updated_at=excluded.updated_at`,
		v.ID, v.Name, v.Description, v.Transport, v.Command, v.ArgsJSON, v.EnvJSON, v.URL, v.HeadersJSON, v.Scope, v.TargetNodesJSON, boolInt(v.Enabled), v.CreatedAt, v.UpdatedAt)
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Store) DeleteMCPService(id string) error {
	_, err := s.DB.Exec(`DELETE FROM mcp_services WHERE id=?`, id)
	return err
}
func (s *Store) ListMCPServices() ([]MCPService, error) {
	return s.listMCP(`SELECT id,name,description,transport,COALESCE(command,''),COALESCE(args_json,''),COALESCE(env_json,''),COALESCE(url,''),COALESCE(headers_json,''),scope,COALESCE(target_nodes_json,''),enabled,created_at,updated_at FROM mcp_services ORDER BY id`)
}
func (s *Store) ListMCPServicesForNode(node string) ([]MCPService, error) {
	all, err := s.listMCP(`SELECT id,name,description,transport,COALESCE(command,''),COALESCE(args_json,''),COALESCE(env_json,''),COALESCE(url,''),COALESCE(headers_json,''),scope,COALESCE(target_nodes_json,''),enabled,created_at,updated_at FROM mcp_services WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make([]MCPService, 0, len(all))
	for _, v := range all {
		if v.Scope != "custom" || containsNode(v.TargetNodesJSON, node) {
			out = append(out, v)
		}
	}
	return out, nil
}
func containsNode(raw, node string) bool {
	var a []string
	if json.Unmarshal([]byte(raw), &a) != nil {
		return false
	}
	for _, v := range a {
		if v == node {
			return true
		}
	}
	return false
}
func (s *Store) listMCP(q string) ([]MCPService, error) {
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPService{}
	for rows.Next() {
		var v MCPService
		var en int
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.Transport, &v.Command, &v.ArgsJSON, &v.EnvJSON, &v.URL, &v.HeadersJSON, &v.Scope, &v.TargetNodesJSON, &en, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Enabled = en != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) migrateDevices() error {
	cols := map[string]bool{}
	rows, err := s.DB.Query("PRAGMA table_info(devices)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	migrations := []struct{ name, sql string }{
		{"last_seen", "ALTER TABLE devices ADD COLUMN last_seen INTEGER NOT NULL DEFAULT 0"},
		{"last_heartbeat", "ALTER TABLE devices ADD COLUMN last_heartbeat INTEGER NOT NULL DEFAULT 0"},
		{"offline_at", "ALTER TABLE devices ADD COLUMN offline_at INTEGER"},
		{"profile_json", "ALTER TABLE devices ADD COLUMN profile_json TEXT"},
		{"created_at", "ALTER TABLE devices ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0"},
	}
	for _, m := range migrations {
		if !cols[m.name] {
			if _, err := s.DB.Exec(m.sql); err != nil {
				return err
			}
		}
	}
	_, err = s.DB.Exec("UPDATE devices SET created_at=CASE WHEN created_at=0 THEN updated_at ELSE created_at END, " +
		"last_seen=CASE WHEN last_seen=0 THEN updated_at ELSE last_seen END, " +
		"last_heartbeat=CASE WHEN last_heartbeat=0 THEN updated_at ELSE last_heartbeat END " +
		"WHERE created_at=0 OR last_seen=0 OR last_heartbeat=0")
	return err
}

func (s *Store) UpsertLocal(name, arch, osName string) error {
	now := time.Now().Unix()
	_, err := s.DB.Exec("INSERT INTO devices(name,status,is_local,arch,os,last_seen,last_heartbeat,offline_at,profile_json,created_at,updated_at) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET status='online', is_local=1, arch=excluded.arch, os=excluded.os, offline_at=NULL, updated_at=excluded.updated_at",
		name, "online", 1, arch, osName, now, now, nil, nil, now, now)
	return err
}

func (s *Store) UpsertRemote(name, status, arch, osName string, lastHeartbeat int64, profileJSON string) error {
	now := time.Now().Unix()
	if lastHeartbeat <= 0 {
		lastHeartbeat = now
	}
	_, err := s.DB.Exec("INSERT INTO devices(name,status,is_local,arch,os,last_seen,last_heartbeat,offline_at,profile_json,created_at,updated_at) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET status=excluded.status, is_local=0, arch=excluded.arch, os=excluded.os, "+
		"last_seen=excluded.last_seen, last_heartbeat=excluded.last_heartbeat, offline_at=NULL, profile_json=excluded.profile_json, updated_at=excluded.updated_at",
		name, status, 0, arch, osName, lastHeartbeat, lastHeartbeat, nil, profileJSON, now, now)
	return err
}

type RemoteProfileUpdate struct {
	Name          string
	LastHeartbeat int64
	Arch          string
	OS            string
	ProfileJSON   string
}

// UpdateRemoteProfiles persists the latest profile snapshots in one transaction.
// Device status is deliberately not changed here: a queued profile may be flushed
// after the connection has already transitioned offline.
func (s *Store) UpdateRemoteProfiles(updates []RemoteProfileUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE devices SET arch=?, os=?, last_seen=?, last_heartbeat=?, profile_json=?, updated_at=? WHERE name=? AND is_local=0 AND last_heartbeat<=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	for _, u := range updates {
		heartbeat := u.LastHeartbeat
		if heartbeat <= 0 {
			heartbeat = now
		}
		if _, err := stmt.Exec(u.Arch, u.OS, heartbeat, heartbeat, u.ProfileJSON, now, u.Name, heartbeat); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateRemoteProfile(name string, lastHeartbeat int64, arch, osName, profileJSON string) error {
	now := time.Now().Unix()
	if lastHeartbeat <= 0 {
		lastHeartbeat = now
	}
	_, err := s.DB.Exec("UPDATE devices SET status='online', arch=?, os=?, last_seen=?, last_heartbeat=?, offline_at=NULL, profile_json=?, updated_at=? WHERE name=? AND is_local=0",
		arch, osName, lastHeartbeat, lastHeartbeat, profileJSON, now, name)
	return err
}

func (s *Store) UpdateDeviceStatus(name, status string, lastHeartbeat int64) error {
	now := time.Now().Unix()
	if lastHeartbeat <= 0 {
		lastHeartbeat = now
	}
	// Keep the original offline_at while a node stays offline (e.g. Hub restart
	// re-marks known nodes offline) so offline duration and alerts stay correct.
	_, err := s.DB.Exec("UPDATE devices SET status=?, last_seen=?, last_heartbeat=?, offline_at=CASE WHEN ?='offline' THEN COALESCE(offline_at, ?) ELSE NULL END, updated_at=? WHERE name=? AND is_local=0",
		status, lastHeartbeat, lastHeartbeat, status, now, now, name)
	return err
}

type DeviceRecord struct {
	Name, Status, Arch, OS, ProfileJSON string
	IsLocal                             bool
	LastSeen, LastHeartbeat             int64
	OfflineAt                           *int64
	UpdatedAt                           int64
}

func (s *Store) ListDevices() ([]DeviceRecord, error) {
	rows, err := s.DB.Query("SELECT name,status,is_local,COALESCE(arch,''),COALESCE(os,''),last_seen,last_heartbeat,offline_at,COALESCE(profile_json,''),updated_at FROM devices ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRecord
	for rows.Next() {
		var d DeviceRecord
		var local int
		var offline sql.NullInt64
		if err := rows.Scan(&d.Name, &d.Status, &local, &d.Arch, &d.OS, &d.LastSeen, &d.LastHeartbeat, &offline, &d.ProfileJSON, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.IsLocal = local != 0
		if offline.Valid {
			v := offline.Int64
			d.OfflineAt = &v
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDevice(name string) error {
	_, err := s.DB.Exec("DELETE FROM devices WHERE name=? AND is_local=0", name)
	return err
}

func (s *Store) migrateAudit() error {
	rows, err := s.DB.Query("PRAGMA table_info(audit_logs)")
	if err != nil {
		return err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	migrations := []struct{ name, sql string }{
		{"actor_type", "ALTER TABLE audit_logs ADD COLUMN actor_type TEXT"},
		{"actor_id", "ALTER TABLE audit_logs ADD COLUMN actor_id TEXT"},
		{"actor_name", "ALTER TABLE audit_logs ADD COLUMN actor_name TEXT"},
		{"oauth_client_id", "ALTER TABLE audit_logs ADD COLUMN oauth_client_id TEXT"},
		{"transport", "ALTER TABLE audit_logs ADD COLUMN transport TEXT"},
		{"target_type", "ALTER TABLE audit_logs ADD COLUMN target_type TEXT"},
		{"target", "ALTER TABLE audit_logs ADD COLUMN target TEXT"},
		{"result", "ALTER TABLE audit_logs ADD COLUMN result TEXT"},
		{"request_id", "ALTER TABLE audit_logs ADD COLUMN request_id TEXT"},
		{"session_id", "ALTER TABLE audit_logs ADD COLUMN session_id TEXT"},
	}
	for _, m := range migrations {
		if !cols[m.name] {
			if _, err := s.DB.Exec(m.sql); err != nil {
				return err
			}
		}
	}
	// Normalize legacy OAuth attribution into its own column. New audit rows never
	// encode the client ID inside actor_name, but preserve attribution for old rows.
	_, err = s.DB.Exec("UPDATE audit_logs SET oauth_client_id=substr(actor_name,instr(actor_name,' via=oauth:')+length(' via=oauth:')), actor_name=substr(actor_name,1,instr(actor_name,' via=oauth:')-1) WHERE instr(actor_name,' via=oauth:')>0 AND COALESCE(oauth_client_id,'')=''")
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("UPDATE audit_logs SET actor_name=COALESCE(actor_name,'unknown(legacy)'), result=COALESCE(result,CASE WHEN exit_code=0 THEN 'ok' ELSE 'error' END), transport=COALESCE(transport,'legacy') WHERE actor_name IS NULL OR result IS NULL OR transport IS NULL")
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(timestamp); CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_logs(actor_name,timestamp); CREATE INDEX IF NOT EXISTS idx_audit_device ON audit_logs(device,timestamp)")
	return err
}

type AuditEntry struct {
	Timestamp     int64 `json:"timestamp"`
	CallerIP      string
	Device        string
	Action        string
	Command       string
	ExitCode      int
	DurationMS    int64
	ErrorMsg      string
	ActorType     string
	ActorID       string
	ActorName     string
	OAuthClientID string
	Transport     string
	TargetType    string
	Target        string
	Result        string
	RequestID     string
	SessionID     string
}

type AuditLog struct {
	ID            int64  `json:"id"`
	Timestamp     int64  `json:"timestamp"`
	CallerIP      string `json:"caller_ip"`
	Device        string `json:"device"`
	Action        string `json:"action"`
	Command       string `json:"command"`
	ExitCode      int    `json:"exit_code"`
	DurationMS    int64  `json:"duration_ms"`
	ErrorMsg      string `json:"error_msg"`
	ActorType     string `json:"actor_type"`
	ActorID       string `json:"actor_id"`
	ActorName     string `json:"actor_name"`
	OAuthClientID string `json:"oauth_client_id"`
	Transport     string `json:"transport"`
	TargetType    string `json:"target_type"`
	Target        string `json:"target"`
	Result        string `json:"result"`
	RequestID     string `json:"request_id"`
	SessionID     string `json:"session_id"`
}

func normalizeAuditEntry(e AuditEntry) AuditEntry {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	if e.ActorName == "" {
		e.ActorName = "unknown(legacy)"
	}
	if e.Transport == "" {
		e.Transport = "legacy"
	}
	if e.Result == "" {
		if e.ExitCode == 0 {
			e.Result = "ok"
		} else {
			e.Result = "error"
		}
	}
	e.Command = auditutil.Truncate(auditutil.Redact(e.Command))
	e.ErrorMsg = auditutil.Redact(e.ErrorMsg)
	e.ActorName = auditutil.Redact(e.ActorName)
	e.TargetType = auditutil.Redact(e.TargetType)
	e.Target = auditutil.Redact(e.Target)
	e.RequestID = auditutil.Redact(e.RequestID)
	e.SessionID = auditutil.Redact(e.SessionID)
	return e
}

func (s *Store) AuditEntry(e AuditEntry) error {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	e = normalizeAuditEntry(e)
	s.auditMu.Lock()
	if s.auditClosed {
		s.auditMu.Unlock()
		err := s.writeAuditEntry(e)
		if err != nil {
			s.writeAuditFallback(e, err)
		}
		return err
	}
	if s.auditQueue == nil {
		s.auditQueue = make(chan auditQueueItem, auditQueueCapacity)
		s.auditStop = make(chan struct{})
		s.auditDone = make(chan struct{})
		go s.auditWriter(s.auditQueue, s.auditStop, s.auditDone)
	}
	select {
	case s.auditQueue <- auditQueueItem{entry: e}:
		s.auditMu.Unlock()
		return nil
	default:
		log.Printf("WARN audit queue full (capacity=%d); synchronously writing action=%q device=%q", auditQueueCapacity, e.Action, e.Device)
		err := s.writeAuditEntry(e)
		if err != nil {
			s.writeAuditFallback(e, err)
		}
		s.auditMu.Unlock()
		return err
	}
}

func (s *Store) writeAuditEntry(e AuditEntry) error {
	s.auditWriteMu.Lock()
	defer s.auditWriteMu.Unlock()
	started := time.Now()
	_, err := s.DB.Exec("INSERT INTO audit_logs(timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg,actor_type,actor_id,actor_name,oauth_client_id,transport,target_type,target,result,request_id,session_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		e.Timestamp, e.CallerIP, e.Device, e.Action, e.Command, e.ExitCode, e.DurationMS, e.ErrorMsg,
		e.ActorType, e.ActorID, e.ActorName, e.OAuthClientID, e.Transport, e.TargetType, e.Target, e.Result, e.RequestID, e.SessionID)
	s.sqliteWriteLatencyNS.Store(time.Since(started).Nanoseconds())
	return err
}

func (s *Store) writeAuditBatch(entries []AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	s.auditWriteMu.Lock()
	defer s.auditWriteMu.Unlock()
	started := time.Now()
	defer func() { s.sqliteWriteLatencyNS.Store(time.Since(started).Nanoseconds()) }()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO audit_logs(timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg,actor_type,actor_id,actor_name,oauth_client_id,transport,target_type,target,result,request_id,session_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, e := range entries {
		if _, err = stmt.Exec(e.Timestamp, e.CallerIP, e.Device, e.Action, e.Command, e.ExitCode, e.DurationMS, e.ErrorMsg,
			e.ActorType, e.ActorID, e.ActorName, e.OAuthClientID, e.Transport, e.TargetType, e.Target, e.Result, e.RequestID, e.SessionID); err != nil {
			break
		}
	}
	_ = stmt.Close()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) writeAuditFallback(e AuditEntry, cause error) {
	payload := struct {
		Type      string     `json:"type"`
		Timestamp int64      `json:"timestamp"`
		Entry     AuditEntry `json:"entry"`
		Error     string     `json:"persistence_error"`
	}{Type: "vps_commander_audit_fallback", Timestamp: e.Timestamp, Entry: e, Error: cause.Error()}
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("CRITICAL audit fallback JSON encode failed; timestamp=%d action=%q device=%q persistence_error=%q encode_error=%q", e.Timestamp, e.Action, e.Device, cause, err)
		s.auditFallbackCount.Add(1)
		return
	}
	if _, err := fmt.Fprintln(os.Stderr, string(b)); err != nil {
		log.Printf("CRITICAL audit fallback stderr write failed; timestamp=%d action=%q device=%q persistence_error=%q stderr_error=%q", e.Timestamp, e.Action, e.Device, cause, err)
	}
	s.auditFallbackCount.Add(1)
}

// AuditFallbackCount reports the number of audit entries diverted to stderr after DB failure.
func (s *Store) AuditFallbackCount() uint64 { return s.auditFallbackCount.Load() }

func (s *Store) auditWriter(queue <-chan auditQueueItem, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(auditFlushInterval)
	defer ticker.Stop()
	batch := make([]AuditEntry, 0, auditBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		entries := append([]AuditEntry(nil), batch...)
		var err error
		for attempt := 1; attempt <= auditWriteAttempts; attempt++ {
			err = s.writeAuditBatch(entries)
			if err == nil {
				break
			}
			log.Printf("ERROR audit batch write attempt %d/%d failed (%d entries): %v", attempt, auditWriteAttempts, len(entries), err)
			if attempt < auditWriteAttempts {
				select {
				case <-stop:
					// During shutdown, do not spend the remaining retry budget on a failing DB.
					attempt = auditWriteAttempts
				default:
					time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
				}
			}
		}
		if err != nil {
			for _, entry := range entries {
				s.writeAuditFallback(entry, err)
			}
		}
		batch = batch[:0]
		return err
	}
	for {
		select {
		case item := <-queue:
			if item.barrier != nil {
				item.barrier <- flush()
				continue
			}
			batch = append(batch, item.entry)
			if len(batch) >= auditBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-stop:
			for {
				select {
				case item := <-queue:
					if item.barrier != nil {
						item.barrier <- flush()
					} else {
						batch = append(batch, item.entry)
						if len(batch) >= auditBatchSize {
							flush()
						}
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// FlushAudits waits for a queue barrier, but never blocks indefinitely.
func (s *Store) FlushAudits() error {
	s.auditMu.Lock()
	if s.auditClosed {
		done := s.auditDone
		s.auditMu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
			s.auditMu.Lock()
			err := s.auditCloseErr
			s.auditMu.Unlock()
			return err
		case <-time.After(auditShutdownTimeout):
			return fmt.Errorf("timed out waiting for audit writer")
		}
	}
	if s.auditQueue == nil {
		s.auditMu.Unlock()
		return nil
	}
	ack := make(chan error, 1)
	select {
	case s.auditQueue <- auditQueueItem{barrier: ack}:
		s.auditMu.Unlock()
	case <-time.After(auditShutdownTimeout):
		s.auditMu.Unlock()
		return fmt.Errorf("timed out queueing audit flush barrier")
	}
	select {
	case err := <-ack:
		return err
	case <-time.After(auditShutdownTimeout):
		return fmt.Errorf("timed out waiting for audit flush")
	}
}

// Close stops the audit writer with a bounded wait. If it cannot stop in time,
// the DB remains open so the writer is not raced by database closure.
func (s *Store) Close() error {
	s.auditCloseOnce.Do(func() {
		s.auditMu.Lock()
		s.auditClosed = true
		stop, done := s.auditStop, s.auditDone
		if stop != nil {
			close(stop)
		}
		s.auditMu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(auditShutdownTimeout):
				s.auditMu.Lock()
				s.auditCloseErr = fmt.Errorf("timed out after %s waiting for audit writer; database left open to avoid racing the writer", auditShutdownTimeout)
				err := s.auditCloseErr
				s.auditMu.Unlock()
				log.Printf("CRITICAL %v", err)
				return
			}
		}
		err := s.DB.Close()
		s.auditMu.Lock()
		s.auditCloseErr = err
		s.auditMu.Unlock()
	})
	s.auditMu.Lock()
	err := s.auditCloseErr
	s.auditMu.Unlock()
	return err
}

func (s *Store) Audit(ip, device, action, command string, code int, ms int64, msg string) error {
	return s.AuditEntry(AuditEntry{CallerIP: ip, Device: device, Action: action, Command: command, ExitCode: code, DurationMS: ms, ErrorMsg: msg})
}

type WebhookTarget struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	Config    string `json:"config_json"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Store) SaveWebhookTarget(v WebhookTarget) error {
	now := time.Now().Unix()
	if v.ID == "" || v.Name == "" || v.Type == "" || v.URL == "" {
		return errors.New("id, name, type and url required")
	}
	if v.CreatedAt <= 0 {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	_, err := s.DB.Exec(`INSERT INTO webhook_targets(id,name,type,url,config_json,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,url=excluded.url,config_json=excluded.config_json,enabled=excluded.enabled,updated_at=excluded.updated_at`, v.ID, v.Name, v.Type, v.URL, v.Config, boolInt(v.Enabled), v.CreatedAt, v.UpdatedAt)
	return err
}
func (s *Store) DeleteWebhookTarget(id string) error {
	_, err := s.DB.Exec(`DELETE FROM webhook_targets WHERE id=?`, id)
	return err
}
func (s *Store) ListWebhookTargets() ([]WebhookTarget, error) {
	rows, err := s.DB.Query(`SELECT id,name,type,url,COALESCE(config_json,''),enabled,created_at,updated_at FROM webhook_targets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebhookTarget{}
	for rows.Next() {
		var v WebhookTarget
		var en int
		if err := rows.Scan(&v.ID, &v.Name, &v.Type, &v.URL, &v.Config, &en, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Enabled = en != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) QueryAudits(actor, device, action, result, transport string, from, to, cursor int64, limit int) ([]AuditLog, error) {
	if err := s.FlushAudits(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := "SELECT id,timestamp,COALESCE(caller_ip,''),COALESCE(device,''),COALESCE(action,''),COALESCE(command,''),exit_code,duration_ms,COALESCE(error_msg,''),COALESCE(actor_type,''),COALESCE(actor_id,''),COALESCE(actor_name,''),COALESCE(oauth_client_id,''),COALESCE(transport,''),COALESCE(target_type,''),COALESCE(target,''),COALESCE(result,''),COALESCE(request_id,''),COALESCE(session_id,'') FROM audit_logs WHERE 1=1"
	args := []any{}
	if actor != "" {
		switch actor {
		case "legacy":
			q += " AND (actor_name IN ('legacy','legacy-env','unknown(legacy)') OR actor_id='legacy-env')"
		case "panel":
			q += " AND (actor_type='panel' OR transport='panel')"
		default:
			// Selecting a named token includes its OAuth-attributed audit rows too.
			q += " AND actor_name=?"
			args = append(args, actor)
		}
	}
	if device != "" {
		q += " AND device=?"
		args = append(args, device)
	}
	if action != "" {
		q += " AND action=?"
		args = append(args, action)
	}
	if result != "" {
		q += " AND result=?"
		args = append(args, result)
	}
	if transport != "" {
		if transport == "mcp" {
			q += " AND (transport LIKE 'mcp-%' OR transport='mcp')"
		} else {
			q += " AND transport=?"
			args = append(args, transport)
		}
	}
	if from > 0 {
		q += " AND timestamp>=?"
		args = append(args, from)
	}
	if to > 0 {
		q += " AND timestamp<=?"
		args = append(args, to)
	}
	if cursor > 0 {
		q += " AND id<?"
		args = append(args, cursor)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditLog, 0, limit)
	for rows.Next() {
		var v AuditLog
		if err := rows.Scan(&v.ID, &v.Timestamp, &v.CallerIP, &v.Device, &v.Action, &v.Command, &v.ExitCode, &v.DurationMS, &v.ErrorMsg, &v.ActorType, &v.ActorID, &v.ActorName, &v.OAuthClientID, &v.Transport, &v.TargetType, &v.Target, &v.Result, &v.RequestID, &v.SessionID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CleanupAudits(retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = 90
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	_, err := s.DB.Exec("DELETE FROM audit_logs WHERE timestamp<?", cutoff)
	return err
}

func (s *Store) RecentAudits(limit int) ([]AuditLog, error) {
	if err := s.FlushAudits(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.DB.Query("SELECT id,timestamp,COALESCE(caller_ip,''),COALESCE(device,''),COALESCE(action,''),COALESCE(command,''),exit_code,duration_ms,COALESCE(error_msg,''),COALESCE(actor_type,''),COALESCE(actor_id,''),COALESCE(actor_name,''),COALESCE(oauth_client_id,''),COALESCE(transport,''),COALESCE(target_type,''),COALESCE(target,''),COALESCE(result,''),COALESCE(request_id,''),COALESCE(session_id,'') FROM audit_logs ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditLog, 0, limit)
	for rows.Next() {
		var v AuditLog
		if err := rows.Scan(&v.ID, &v.Timestamp, &v.CallerIP, &v.Device, &v.Action, &v.Command, &v.ExitCode, &v.DurationMS, &v.ErrorMsg, &v.ActorType, &v.ActorID, &v.ActorName, &v.OAuthClientID, &v.Transport, &v.TargetType, &v.Target, &v.Result, &v.RequestID, &v.SessionID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RevokeDevice(name, reason string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO revoked_devices(name,revoked_at,reason) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET revoked_at=excluded.revoked_at,reason=excluded.reason",
		name, time.Now().Unix(), reason); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM devices WHERE name=? AND is_local=0", name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnrevokeDevice(name string) error {
	_, err := s.DB.Exec("DELETE FROM revoked_devices WHERE name = ?", name)
	return err
}

func (s *Store) IsRevoked(name string) (bool, error) {
	var count int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM revoked_devices WHERE name = ?", name).Scan(&count)
	return count > 0, err
}

func normalizeSecurityMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "inherit", "low", "medium", "high":
		return mode
	}
	return ""
}

type SecurityPolicyState struct {
	GlobalMode string
	Nodes      map[string]string
	Version    uint64
}

func (s *Store) LoadSecurityPolicy() (SecurityPolicyState, error) {
	state := SecurityPolicyState{GlobalMode: "medium", Nodes: map[string]string{}, Version: 1}
	if err := s.DB.QueryRow("SELECT mode FROM security_settings WHERE node_name='' LIMIT 1").Scan(&state.GlobalMode); err != nil && err != sql.ErrNoRows {
		return state, err
	}
	if m := normalizeSecurityMode(state.GlobalMode); m != "" && m != "inherit" {
		state.GlobalMode = m
	} else {
		state.GlobalMode = "medium"
	}
	var version int64
	if err := s.DB.QueryRow("SELECT version FROM security_policy_meta WHERE id=1").Scan(&version); err != nil {
		return state, err
	}
	if version > 0 {
		state.Version = uint64(version)
	}
	rows, err := s.DB.Query("SELECT node_name,mode FROM security_settings WHERE node_name<>''")
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var node, mode string
		if err := rows.Scan(&node, &mode); err != nil {
			return state, err
		}
		if m := normalizeSecurityMode(mode); m != "" {
			state.Nodes[node] = m
		}
	}
	return state, rows.Err()
}

func (s *Store) bumpSecurityPolicyVersion(tx *sql.Tx) error {
	_, err := tx.Exec("UPDATE security_policy_meta SET version=version+1 WHERE id=1")
	return err
}

func (s *Store) SecurityPolicyVersion() uint64 {
	var version int64
	if err := s.DB.QueryRow("SELECT version FROM security_policy_meta WHERE id=1").Scan(&version); err != nil || version < 1 {
		return 1
	}
	return uint64(version)
}

func (s *Store) GetGlobalSecurityMode() string {
	var mode string
	err := s.DB.QueryRow("SELECT mode FROM security_settings WHERE node_name='' LIMIT 1").Scan(&mode)
	if err != nil {
		return "medium"
	}
	if m := normalizeSecurityMode(mode); m != "" && m != "inherit" {
		return m
	}
	return "medium"
}

func (s *Store) SetGlobalSecurityMode(mode string) error {
	mode = normalizeSecurityMode(mode)
	if mode == "" || mode == "inherit" {
		return errors.New("invalid global security mode")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO security_settings(node_name,mode,updated_at) VALUES('',?,?) ON CONFLICT(node_name) DO UPDATE SET mode=excluded.mode,updated_at=excluded.updated_at", mode, time.Now().Unix()); err != nil {
		return err
	}
	if err = s.bumpSecurityPolicyVersion(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetNodeSecurityMode(node string) (string, bool) {
	var mode string
	err := s.DB.QueryRow("SELECT mode FROM security_settings WHERE node_name=?", node).Scan(&mode)
	if err != nil {
		return "inherit", false
	}
	m := normalizeSecurityMode(mode)
	if m == "" {
		return "inherit", false
	}
	return m, true
}

func (s *Store) SetNodeSecurityMode(node, mode string) error {
	if node == "" {
		return errors.New("node name required")
	}
	mode = normalizeSecurityMode(mode)
	if mode == "" {
		return errors.New("invalid security mode")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO security_settings(node_name,mode,updated_at) VALUES(?,?,?) ON CONFLICT(node_name) DO UPDATE SET mode=excluded.mode,updated_at=excluded.updated_at", node, mode, time.Now().Unix()); err != nil {
		return err
	}
	if err = s.bumpSecurityPolicyVersion(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type SecuritySetting struct {
	Node string `json:"node"`
	Mode string `json:"mode"`
}

func (s *Store) ListNodeSecurityModes() ([]SecuritySetting, error) {
	rows, err := s.DB.Query("SELECT node_name,mode FROM security_settings WHERE node_name<>'' ORDER BY node_name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecuritySetting{}
	for rows.Next() {
		var v SecuritySetting
		if err := rows.Scan(&v.Node, &v.Mode); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func hashDeviceToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// SetDeviceCredential stores only a SHA-256 hash of the per-device token.
func (s *Store) DeviceCredentialConfigured(name string) (bool, error) {
	return s.HasDeviceCredential(name)
}

func (s *Store) SetDeviceCredential(name, token string) error {
	if strings.TrimSpace(name) == "" || token == "" {
		return errors.New("device name and token required")
	}
	now := time.Now().Unix()
	_, err := s.DB.Exec(`INSERT INTO device_credentials(name,token_hash,created_at,updated_at)
VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET token_hash=excluded.token_hash,updated_at=excluded.updated_at`, name, hashDeviceToken(token), now, now)
	return err
}

func (s *Store) VerifyDeviceCredential(name, token string) (bool, error) {
	if name == "" || token == "" {
		return false, nil
	}
	var stored string
	err := s.DB.QueryRow("SELECT token_hash FROM device_credentials WHERE name=?", name).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return subtleConstantTimeEqual(stored, hashDeviceToken(token)), nil
}

func subtleConstantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func (s *Store) HasDeviceCredential(name string) (bool, error) {
	var count int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM device_credentials WHERE name=?", name).Scan(&count)
	return count > 0, err
}

// AuditQueueDepth reports the current buffered audit queue depth.
func (s *Store) AuditQueueDepth() int {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if s.auditQueue == nil {
		return 0
	}
	return len(s.auditQueue)
}

// LastSQLiteWriteLatency reports the most recent audit SQLite write duration.
func (s *Store) LastSQLiteWriteLatency() time.Duration {
	return time.Duration(s.sqliteWriteLatencyNS.Load())
}
