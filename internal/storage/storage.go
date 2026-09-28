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
	// SQLite has a single-writer model. Keep one application connection so
	// concurrent callers queue instead of opening competing writer connections.
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
	if _, err := s.DB.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS devices (
		name TEXT PRIMARY KEY, status TEXT NOT NULL, is_local INTEGER NOT NULL,
		arch TEXT, os TEXT, updated_at INTEGER NOT NULL
	);	CREATE TABLE IF NOT EXISTS audit_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL,
		caller_ip TEXT, device TEXT, action TEXT, command TEXT,
		exit_code INTEGER, duration_ms INTEGER, error_msg TEXT
	);`)
	return err
}

func (s *Store) UpsertLocal(name, arch, osName string) error {
	_, err := s.DB.Exec(`INSERT INTO devices(name,status,is_local,arch,os,updated_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET status=excluded.status,
		arch=excluded.arch, os=excluded.os, updated_at=excluded.updated_at`,
		name, "online", 1, arch, osName, time.Now().Unix())
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
	_, err := s.DB.Exec(`INSERT INTO audit_logs(timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg)
		VALUES(?,?,?,?,?,?,?,?)`, time.Now().Unix(), ip, device, action, command, code, ms, msg)
	return err
}

func (s *Store) RecentAudits(limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.DB.Query(`SELECT id,timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg
		FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
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

func (s *Store) InitRevocationTable() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS revoked_devices (
		name TEXT PRIMARY KEY,
		revoked_at INTEGER NOT NULL,
		reason TEXT
	);`)
	return err
}

func (s *Store) RevokeDevice(name, reason string) error {
	_, err := s.DB.Exec(`INSERT INTO revoked_devices(name, revoked_at, reason)
		VALUES(?, ?, ?) ON CONFLICT(name) DO UPDATE SET revoked_at=excluded.revoked_at, reason=excluded.reason`,
		name, time.Now().Unix(), reason)
	return err
}

func (s *Store) UnrevokeDevice(name string) error {
	_, err := s.DB.Exec(`DELETE FROM revoked_devices WHERE name = ?`, name)
	return err
}

func (s *Store) IsRevoked(name string) (bool, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM revoked_devices WHERE name = ?`, name).Scan(&count)
	return count > 0, err
}
