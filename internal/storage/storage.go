package storage

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Store struct{ DB *sql.DB }

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
	s := &Store{db}
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
	_, err := s.DB.Exec("CREATE TABLE IF NOT EXISTS devices (" +
		"name TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'offline', is_local INTEGER NOT NULL DEFAULT 0, " +
		"arch TEXT, os TEXT, last_seen INTEGER NOT NULL DEFAULT 0, last_heartbeat INTEGER NOT NULL DEFAULT 0, " +
		"offline_at INTEGER, profile_json TEXT, created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0); " +
		"CREATE TABLE IF NOT EXISTS audit_logs (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL, caller_ip TEXT, device TEXT, action TEXT, command TEXT, " +
		"exit_code INTEGER, duration_ms INTEGER, error_msg TEXT); " +
		"CREATE TABLE IF NOT EXISTS revoked_devices (name TEXT PRIMARY KEY, revoked_at INTEGER NOT NULL, reason TEXT);")
	if err != nil {
		return err
	}
	return s.migrateDevices()
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
	var offlineAt any
	if status == "offline" {
		offlineAt = now
	}
	_, err := s.DB.Exec("UPDATE devices SET status=?, last_seen=?, last_heartbeat=?, offline_at=?, updated_at=? WHERE name=? AND is_local=0",
		status, lastHeartbeat, lastHeartbeat, offlineAt, now, name)
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

type AuditLog struct {
	ID         int64  `json:"id"`
	Timestamp  int64  `json:"timestamp"`
	CallerIP   string `json:"caller_ip"`
	Device     string `json:"device"`
	Action     string `json:"action"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	ErrorMsg   string `json:"error_msg"`
}

func (s *Store) Audit(ip, device, action, command string, code int, ms int64, msg string) error {
	_, err := s.DB.Exec("INSERT INTO audit_logs(timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg) VALUES(?,?,?,?,?,?,?,?)",
		time.Now().Unix(), ip, device, action, command, code, ms, msg)
	return err
}

func (s *Store) RecentAudits(limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.DB.Query("SELECT id,timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg FROM audit_logs ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditLog, 0, limit)
	for rows.Next() {
		var v AuditLog
		if err := rows.Scan(&v.ID, &v.Timestamp, &v.CallerIP, &v.Device, &v.Action, &v.Command, &v.ExitCode, &v.DurationMS, &v.ErrorMsg); err != nil {
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
